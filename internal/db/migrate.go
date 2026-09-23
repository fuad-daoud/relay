package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// applyMigrations runs every *.sql file under "migrations" in fsys, in name
// order. Each file runs in its own BEGIN IMMEDIATE transaction on a dedicated
// connection, and re-reads schema_version inside that transaction, skipping
// the file if its number is already recorded there. It is unexported and
// takes fsys as a parameter so tests can inject a second migration without
// touching the embedded set.
func applyMigrations(sqlDB *sql.DB, fsys fs.FS) error {
	names, err := migrationNames(fsys)
	if err != nil {
		return err
	}

	for _, name := range names {
		n, err := migrationNumber(name)
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}

		if err := applyOneMigration(sqlDB, fsys, name, n); err != nil {
			return err
		}
	}

	return nil
}

// maxVersion returns the highest applied schema_version, or 0 when the
// schema_version table does not exist yet (a fresh database). It never
// creates or writes anything.
func maxVersion(sqlDB *sql.DB) (int, error) {
	var name string
	err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='schema_version'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}

	var v sql.NullInt64
	if err := sqlDB.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// maxEmbedded returns the highest migration number fsys ships, or 0 when it
// ships none.
func maxEmbedded(fsys fs.FS) (int, error) {
	names, err := migrationNames(fsys)
	if err != nil {
		return 0, err
	}

	max := 0
	for _, name := range names {
		n, err := migrationNumber(name)
		if err != nil {
			return 0, fmt.Errorf("migration %s: %w", name, err)
		}
		if n > max {
			max = n
		}
	}
	return max, nil
}

func migrationNames(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// migrationNumber parses the leading integer of a migration file name, e.g.
// "001_initial.sql" -> 1.
func migrationNumber(name string) (int, error) {
	i := strings.IndexByte(name, '_')
	if i < 0 {
		return 0, fmt.Errorf("no leading number in %q", name)
	}
	n, err := strconv.Atoi(name[:i])
	if err != nil {
		return 0, fmt.Errorf("leading number in %q: %w", name, err)
	}
	return n, nil
}

func applyOneMigration(sqlDB *sql.DB, fsys fs.FS, name string, n int) (err error) {
	data, err := fs.ReadFile(fsys, "migrations/"+name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}

	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer conn.Close()

	// schema_version is created outside the immediate transaction so a fresh
	// database has somewhere to record versions; IF NOT EXISTS makes the
	// concurrent first-open race a no-op.
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	// BEGIN IMMEDIATE takes the write lock up front, so two processes opening
	// the same fresh database serialise here rather than both applying the
	// migration (#372 §4.5).
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin migration %s: %w", name, mapBusy(err))
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if _, rerr := conn.ExecContext(ctx, "ROLLBACK"); rerr != nil {
			err = fmt.Errorf("migration %s: %v, and rollback failed: %w", name, err, rerr)
		}
	}()

	// Re-select inside the transaction: another process may have applied this
	// migration while this one waited for the write lock.
	var applied bool
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_version WHERE version = ?)`, n).Scan(&applied); err != nil {
		return fmt.Errorf("check migration %s: %w", name, err)
	}
	if applied {
		if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, mapBusy(err))
		}
		committed = true
		return nil
	}

	if _, err = conn.ExecContext(ctx, string(data)); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}

	if _, err = conn.ExecContext(ctx, `INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`,
		n, formatTime(time.Now())); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}

	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, mapBusy(err))
	}
	committed = true

	return nil
}
