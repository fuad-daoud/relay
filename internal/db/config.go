package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// This file is the schema-v2 config and secrets surface (docs/specs/
// 2026-09-24-db-as-record-design.md §3.1, §4.1). Reads come on both *DB and
// *Tx through the same queryer the round readers use; writes are *Tx only,
// with *DB wrappers that open their own transaction, exactly as write.go does.
//
// A database whose schema predates config_doc (schema < 2) reports every
// config and secret read as absent rather than erroring: `relevo daemon
// --check` and `--preflight` open such a database read-only and must see an
// empty config, not a failure (#4.6).

// hasConfig reports whether this database carries the schema-v2 config tables.
// A normally opened *DB has them (Open migrates); only OpenReadOnly on a
// schema-1 file does not.
func (d *DB) hasConfig() bool { return d.have >= 2 }

// ConfigGet returns the JSON body stored for name, and ok false when the
// section is absent (or the schema predates config_doc).
func (d *DB) ConfigGet(name string) ([]byte, bool, error) {
	if !d.hasConfig() {
		return nil, false, nil
	}
	return configGet(context.Background(), d.sqlDB, name)
}

// ConfigGet is the transaction form of *DB.ConfigGet.
func (t *Tx) ConfigGet(name string) ([]byte, bool, error) {
	return configGet(t.ctx, t.conn, name)
}

func configGet(ctx context.Context, q queryer, name string) ([]byte, bool, error) {
	var body string
	err := q.QueryRowContext(ctx, `SELECT body FROM config_doc WHERE name = ?`, name).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("db: config get %s: %w", name, mapBusy(err))
	}
	return []byte(body), true, nil
}

// ConfigPut upserts name's body and bumps config_meta.version by one. The
// caller owns the transaction, so a multi-section write is atomic.
func (t *Tx) ConfigPut(name string, body []byte, now time.Time) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO config_doc (name, body, updated_at) VALUES (?, ?, ?)`,
		name, string(body), formatTime(now)); err != nil {
		return fmt.Errorf("db: config put %s: %w", name, mapBusy(err))
	}
	return t.bumpConfigVersion()
}

// ConfigDelete removes name's body, if present, and bumps config_meta.version
// by one. A delete of an absent name still bumps: the version is a change
// counter for readers, not a count of stored sections.
func (t *Tx) ConfigDelete(name string) error {
	if _, err := t.exec(`DELETE FROM config_doc WHERE name = ?`, name); err != nil {
		return fmt.Errorf("db: config delete %s: %w", name, mapBusy(err))
	}
	return t.bumpConfigVersion()
}

func (t *Tx) bumpConfigVersion() error {
	if _, err := t.exec(`UPDATE config_meta SET version = version + 1 WHERE id = 1`); err != nil {
		return fmt.Errorf("db: config version: %w", mapBusy(err))
	}
	return nil
}

// ConfigVersion returns config_meta.version, 0 when the table is absent
// (schema < 2) or holds no row.
func (d *DB) ConfigVersion() (int64, error) {
	if !d.hasConfig() {
		return 0, nil
	}
	return configVersion(context.Background(), d.sqlDB)
}

// ConfigVersion is the transaction form of *DB.ConfigVersion.
func (t *Tx) ConfigVersion() (int64, error) {
	return configVersion(t.ctx, t.conn)
}

func configVersion(ctx context.Context, q queryer) (int64, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT version FROM config_meta WHERE id = 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("db: config version: %w", mapBusy(err))
	}
	return v, nil
}

// SecretGet returns name's value, and ok false when it is absent (or the
// schema predates the secret table).
func (d *DB) SecretGet(name string) ([]byte, bool, error) {
	if !d.hasConfig() {
		return nil, false, nil
	}
	return secretGet(context.Background(), d.sqlDB, name)
}

// SecretGet is the transaction form of *DB.SecretGet.
func (t *Tx) SecretGet(name string) ([]byte, bool, error) {
	return secretGet(t.ctx, t.conn, name)
}

func secretGet(ctx context.Context, q queryer, name string) ([]byte, bool, error) {
	var value []byte
	err := q.QueryRowContext(ctx, `SELECT value FROM secret WHERE name = ?`, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("db: secret get %s: %w", name, mapBusy(err))
	}
	return value, true, nil
}

// SecretPut upserts name's value. It does not bump config_meta.version: that
// counter tracks the config sections the daemon reloads; a secret is read once
// at runtime construction.
func (t *Tx) SecretPut(name string, value []byte, now time.Time) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		name, value, formatTime(now)); err != nil {
		return fmt.Errorf("db: secret put %s: %w", name, mapBusy(err))
	}
	return nil
}

// SecretDelete removes name's value, if present.
func (t *Tx) SecretDelete(name string) error {
	if _, err := t.exec(`DELETE FROM secret WHERE name = ?`, name); err != nil {
		return fmt.Errorf("db: secret delete %s: %w", name, mapBusy(err))
	}
	return nil
}

// SecretNames returns every stored secret's name, sorted.
func (d *DB) SecretNames() ([]string, error) {
	if !d.hasConfig() {
		return nil, nil
	}
	rows, err := d.sqlDB.QueryContext(context.Background(), `SELECT name FROM secret ORDER BY name`)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: secret names: %w", mapBusy(err))
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("db: secret names: %w", mapBusy(err))
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: secret names: %w", mapBusy(err))
	}
	return names, nil
}

// ConfigImportRecord appends one imported file to the audit trail: the file's
// name and path and the raw bytes that were stored, so the import is
// reconstructable after the file itself is deleted.
func (t *Tx) ConfigImportRecord(name, sourcePath string, body []byte, now time.Time) error {
	if _, err := t.exec(`INSERT INTO config_import (name, source_path, body, imported_at) VALUES (?, ?, ?, ?)`,
		name, sourcePath, body, formatTime(now)); err != nil {
		return fmt.Errorf("db: config import record %s: %w", name, mapBusy(err))
	}
	return nil
}

// OpenReadOnly opens path without ever migrating or writing it, for readers
// that must not create or change the database (`relevo daemon --preflight`
// and `--check`). A missing file's error wraps os.ErrNotExist. A file whose
// schema predates this binary's config tables reads as an empty config.
func OpenReadOnly(path string) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("db: open readonly %s: %w", path, err)
	}

	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open readonly %s: %w: %w", path, ErrOpen, err)
	}

	if err := ping(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: open readonly %s: ping: %w: %w", path, ErrOpen, err)
	}

	have, err := maxVersion(sqlDB)
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: open readonly %s: version: %w: %w", path, ErrOpen, err)
	}
	know, err := maxEmbedded(migrationFiles)
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: open readonly %s: migrations: %w: %w", path, ErrOpen, err)
	}

	return &DB{sqlDB: sqlDB, newer: have > know, have: have, know: know}, nil
}

// isMissingTable reports whether err is sqlite's "no such table" for a schema
// that predates the table. Only OpenReadOnly on a pre-v2 file reaches it,
// where the caller wants an absent answer, not an error.
func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
