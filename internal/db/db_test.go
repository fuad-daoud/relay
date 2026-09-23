package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// errFake is a sentinel Tx callers return to force a rollback in tests.
var errFake = errors.New("fake failure")

// seedNewerSchema creates path with a schema_version row above every embedded
// migration, as a newer relevo would have left it, and returns the table count
// before this binary opens it.
func seedNewerSchema(t *testing.T, path string) int {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sqlDB.Close()

	if _, err := sqlDB.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (99, '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert version 99: %v", err)
	}

	var tables int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	return tables
}

func TestOpenCreatesAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	v, err := d.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != 1 {
		t.Errorf("Version() = %d, want 1", v)
	}

	var mode string
	if err := d.sqlDB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	d1, err := Open(path)
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	d1.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("Open (2nd): %v", err)
	}
	defer d2.Close()

	v, err := d2.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != 1 {
		t.Errorf("Version() = %d, want 1", v)
	}

	var count int
	if err := d2.sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&count); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_version has %d rows, want 1", count)
	}
}

// TestOpenLeavesANewerSchemaAlone pins #372 §4.5: a schema above this relevo's
// highest embedded migration is returned with Newer() == true and is not
// migrated (the table count does not change).
func TestOpenLeavesANewerSchemaAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	before := seedNewerSchema(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if !d.Newer() {
		t.Fatal("Newer() = false, want true for a schema above this relevo's migrations")
	}
	have, know := d.SchemaVersions()
	if have != 99 || know != 1 {
		t.Errorf("SchemaVersions() = (%d, %d), want (99, 1)", have, know)
	}

	var after int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&after); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if after != before {
		t.Errorf("table count = %d, want %d: a newer schema must not be migrated", after, before)
	}
}

// TestCheckMigrateRefusesANewerSchema pins the function `relevo db migrate`
// calls: it errors on a newer schema (wrapping ErrNewerSchema) and returns nil
// on one this relevo may migrate (#372 §4.5).
func TestCheckMigrateRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedNewerSchema(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.CheckMigrate(); err == nil {
		t.Fatal("CheckMigrate() = nil, want an error on a newer schema")
	} else if !errors.Is(err, ErrNewerSchema) {
		t.Errorf("CheckMigrate() error = %v, want errors.Is(..., ErrNewerSchema)", err)
	}

	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	defer fresh.Close()
	if err := fresh.CheckMigrate(); err != nil {
		t.Errorf("CheckMigrate() on a fresh schema = %v, want nil", err)
	}
}

// TestConcurrentOpenAppliesEachMigrationOnce pins #372 §4.5's migration guard:
// two processes opening the same fresh database concurrently both succeed, and
// schema_version holds exactly one row per migration. The in-transaction
// re-check is what makes this hold; remove it and this fails.
func TestConcurrentOpenAppliesEachMigrationOnce(t *testing.T) {
	for i := 0; i < 20; i++ {
		path := filepath.Join(t.TempDir(), "relevo.db")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j := range errs {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				d, err := Open(path)
				if err != nil {
					errs[j] = err
					return
				}
				_ = d.Close()
			}(j)
		}
		wg.Wait()

		for j, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: Open %d: %v", i, j, err)
			}
		}

		sqlDB, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatalf("iteration %d: sql.Open: %v", i, err)
		}
		rows, err := sqlDB.Query(`SELECT version FROM schema_version`)
		if err != nil {
			sqlDB.Close()
			t.Fatalf("iteration %d: query schema_version: %v", i, err)
		}
		versions := map[int]int{}
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				sqlDB.Close()
				t.Fatalf("iteration %d: scan version: %v", i, err)
			}
			versions[v]++
		}
		rows.Close()
		sqlDB.Close()

		if len(versions) != 1 || versions[1] != 1 {
			t.Fatalf("iteration %d: schema_version rows = %v, want exactly one version 1", i, versions)
		}
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	origin := "https://example.test/repo.git"
	wantErr := errFake

	err = d.Tx(func(tx *Tx) error {
		if _, err := tx.UpsertRepo(Repo{OriginURL: &origin}); err != nil {
			t.Fatalf("UpsertRepo inside Tx: %v", err)
		}
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("Tx returned %v, want %v", err, wantErr)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM repo`).Scan(&count); err != nil {
		t.Fatalf("count repo: %v", err)
	}
	if count != 0 {
		t.Errorf("repo has %d rows after rollback, want 0", count)
	}
}
