package db

import (
	"errors"
	"path/filepath"
	"testing"
)

// errFake is a sentinel Tx callers return to force a rollback in tests.
var errFake = errors.New("fake failure")

func TestOpenCreatesAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")

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
	path := filepath.Join(t.TempDir(), "relay.db")

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

func TestTxRollsBackOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")

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
