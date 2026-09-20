package db

import (
	"database/sql"
	"embed"
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
// order, skipping any whose leading number is already recorded in
// schema_version. Each file runs inside one transaction together with its
// schema_version insert. It is unexported and takes fsys as a parameter so
// tests can inject a second migration without touching the embedded set.
func applyMigrations(sqlDB *sql.DB, fsys fs.FS) error {
	if _, err := sqlDB.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	applied, err := appliedVersions(sqlDB)
	if err != nil {
		return err
	}

	names, err := migrationNames(fsys)
	if err != nil {
		return err
	}

	for _, name := range names {
		n, err := migrationNumber(name)
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if applied[n] {
			continue
		}

		if err := applyOneMigration(sqlDB, fsys, name, n); err != nil {
			return err
		}
		applied[n] = true
	}

	return nil
}

func appliedVersions(sqlDB *sql.DB) (map[int]bool, error) {
	rows, err := sqlDB.Query(`SELECT version FROM schema_version`)
	if err != nil {
		return nil, fmt.Errorf("read schema_version: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan schema_version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_version: %w", err)
	}
	return applied, nil
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

	tx, err := sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	if _, err = tx.Exec(string(data)); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}

	if _, err = tx.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`,
		n, formatTime(time.Now())); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}

	return nil
}
