package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// This file is the schema-v2 key-value surface (docs/specs/
// 2026-09-24-db-as-record-design.md §3.2, §4.1). One kv row holds one whole
// JSON document, exactly as the small state file it replaces held it: the
// medium changes, the document does not. Reads come on both *DB and *Tx
// through the same queryer the other readers use; writes are *Tx only, with
// *DB wrappers that open their own short transaction.

// KV is the minimal key-value surface the small stores share. *DB implements
// it. Packages that must not import internal/store -- harness, reached through
// usage from store -- take a db.KV instead.
type KV interface {
	KVGet(key string) ([]byte, bool, error)
	KVPut(key string, value []byte) error
	KVDelete(key string) error
}

// *DB is the production KV a Runtime carries.
var _ KV = (*DB)(nil)

// KVGet returns the JSON document stored for key, and ok false when the row is
// absent. A database whose schema predates kv (< 2) reports every key as
// absent -- via isMissingTable -- rather than erroring, so a read-only open of
// a schema-1 file (daemon --check) sees no keys, not a failure.
func (d *DB) KVGet(key string) ([]byte, bool, error) {
	return kvGet(context.Background(), d.sqlDB, key)
}

// KVGet is the transaction form of *DB.KVGet.
func (t *Tx) KVGet(key string) ([]byte, bool, error) { return kvGet(t.ctx, t.conn, key) }

func kvGet(ctx context.Context, q queryer, key string) ([]byte, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value_json FROM kv WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("db: kv get %s: %w", key, mapBusy(err))
	}
	return []byte(value), true, nil
}

// KVPut upserts key's whole document in one short transaction. value must be
// valid JSON, else ErrInvalid: the row's contract is "the exact JSON document
// the file held", so a caller that marshalled a struct always satisfies it.
func (d *DB) KVPut(key string, value []byte) error {
	return d.Tx(func(t *Tx) error { return t.KVPut(key, value) })
}

// KVPut is the transaction form of *DB.KVPut. The caller owns the transaction,
// so a multi-key write is atomic; a plain write is one short autocommit tx.
func (t *Tx) KVPut(key string, value []byte) error {
	if !json.Valid(value) {
		return fmt.Errorf("db: kv put %s: value is not valid JSON: %w", key, ErrInvalid)
	}
	if _, err := t.exec(`INSERT OR REPLACE INTO kv (key, value_json, updated_at) VALUES (?, ?, ?)`,
		key, string(value), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("db: kv put %s: %w", key, mapBusy(err))
	}
	return nil
}

// KVDelete removes key's row. A key that is not there is not an error.
func (d *DB) KVDelete(key string) error {
	return d.Tx(func(t *Tx) error { return t.KVDelete(key) })
}

// KVDelete is the transaction form of *DB.KVDelete.
func (t *Tx) KVDelete(key string) error {
	if _, err := t.exec(`DELETE FROM kv WHERE key = ?`, key); err != nil {
		return fmt.Errorf("db: kv delete %s: %w", key, mapBusy(err))
	}
	return nil
}

// KVKeys returns every key with the given prefix, sorted. A missing kv table
// (schema < 2) is an empty list. Only the *DB form is exercised today; the Tx
// form is here so a writer inside a transaction can read its own keys.
func (d *DB) KVKeys(prefix string) ([]string, error) {
	return kvKeys(context.Background(), d.sqlDB, prefix)
}

// KVKeys is the transaction form of *DB.KVKeys.
func (t *Tx) KVKeys(prefix string) ([]string, error) { return kvKeys(t.ctx, t.conn, prefix) }

func kvKeys(ctx context.Context, q queryer, prefix string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key FROM kv ORDER BY key`)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: kv keys: %w", mapBusy(err))
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("db: kv keys: %w", mapBusy(err))
		}
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: kv keys: %w", mapBusy(err))
	}
	return out, nil
}

// KVImportFile is the one import helper every package below uses
// (docs/specs/2026-09-24-db-as-record-design.md §4.3). The rule is "a file
// that is present is imported":
//
//   - the key's row wins when it exists: the file is ignored and left where it
//     is, so a machine that has already migrated is never re-read;
//   - otherwise a present path is read, validated as JSON and put under key,
//     and only then removed. A file that is not there is (nil, false, nil);
//   - invalid JSON is an error naming path, and the file is not deleted, so a
//     hand-edited file is reported rather than silently lost;
//   - a remove failure is logged, not returned: the row is the record now, and
//     a leftover file is harmless (the next read finds the row).
//
// The write happens before the delete on purpose: a crash between them leaves
// the file, and the next read imports it again. Deleting first would lose the
// document on a failed put.
func KVImportFile(kv KV, key, path string) ([]byte, bool, error) {
	v, ok, err := kv.KVGet(key)
	if err != nil {
		return nil, false, err
	}
	if ok {
		return v, true, nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if !json.Valid(data) {
		return nil, false, fmt.Errorf("decode %s: invalid JSON: %w", path, ErrInvalid)
	}

	if err := kv.KVPut(key, data); err != nil {
		return nil, false, err
	}
	if err := os.Remove(path); err != nil {
		slog.Warn("kv import: could not remove imported file", "key", key, "path", path, "err", err)
	}
	return data, true, nil
}
