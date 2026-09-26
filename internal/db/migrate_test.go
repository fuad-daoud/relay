package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// rawSQLDB opens a driver connection straight to path, without Open's
// migration, so a test can drive applyMigrations itself.
func rawSQLDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

// TestMigrationsApplyInOrder injects a second migration through the unexported
// applyMigrations(db, fs) entry point, so it never touches the embedded set.
func TestMigrationsApplyInOrder(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))

	fsys := fstest.MapFS{
		"migrations/001_initial.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE IF NOT EXISTS a (id TEXT PRIMARY KEY);`)},
		"migrations/002_second.sql":  &fstest.MapFile{Data: []byte(`CREATE TABLE IF NOT EXISTS b (id TEXT PRIMARY KEY);`)},
	}

	if err := applyMigrations(sqlDB, fsys); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}

	rows, err := sqlDB.Query(`SELECT version FROM schema_version ORDER BY version ASC`)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		versions = append(versions, v)
	}

	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("schema_version versions = %v, want [1 2]", versions)
	}

	for _, tbl := range []string{"a", "b"} {
		var name string
		if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name); err != nil {
			t.Errorf("table %s missing: %v", tbl, err)
		}
	}
}

func TestMigrationsApplyInOrderIsIdempotent(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))

	fsys := fstest.MapFS{
		"migrations/001_initial.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE IF NOT EXISTS a (id TEXT PRIMARY KEY);`)},
	}

	if err := applyMigrations(sqlDB, fsys); err != nil {
		t.Fatalf("applyMigrations (1st): %v", err)
	}
	if err := applyMigrations(sqlDB, fsys); err != nil {
		t.Fatalf("applyMigrations (2nd): %v", err)
	}

	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&count); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_version has %d rows, want 1", count)
	}
}
