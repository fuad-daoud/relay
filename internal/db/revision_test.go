package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestRevisionInsertAndRead(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	var revs []int64
	for i, source := range []string{"cli", "rollback"} {
		err := d.Tx(func(tx *Tx) error {
			exists, err := tx.RevisionsExist()
			if err != nil {
				return err
			}
			if want := i > 0; exists != want {
				t.Errorf("RevisionsExist before insert %d = %v, want %v", i+1, exists, want)
			}
			rev, err := tx.RevisionInsert(RevisionRow{
				At:       now,
				Source:   source,
				Message:  "message " + source,
				Version:  int64(i + 1),
				Changes:  []byte(`[{"path":"policy.x","op":"add","after":1}]`),
				Snapshot: []byte(`{"policy":{}}`),
			})
			if err != nil {
				return err
			}
			revs = append(revs, rev)
			return nil
		})
		if err != nil {
			t.Fatalf("Tx %d: %v", i+1, err)
		}
	}
	if revs[0] != 1 || revs[1] != 2 {
		t.Fatalf("revs = %v, want [1 2]", revs)
	}

	rows, err := d.Revisions(0)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Revisions(0) = %d rows, want 2", len(rows))
	}
	if rows[0].Rev != 2 || rows[1].Rev != 1 {
		t.Errorf("Revisions newest first = [%d %d], want [2 1]", rows[0].Rev, rows[1].Rev)
	}
	if rows[0].Snapshot != nil {
		t.Errorf("Revisions left Snapshot = %q, want nil", rows[0].Snapshot)
	}
	if rows[0].Source != "rollback" || rows[0].Message != "message rollback" || rows[0].Version != 2 {
		t.Errorf("rows[0] = %+v, want rollback / message rollback / version 2", rows[0])
	}
	if !rows[0].At.Equal(now) {
		t.Errorf("rows[0].At = %v, want %v", rows[0].At, now)
	}
	if string(rows[0].Changes) != `[{"path":"policy.x","op":"add","after":1}]` {
		t.Errorf("rows[0].Changes = %q", rows[0].Changes)
	}

	one, err := d.Revisions(1)
	if err != nil {
		t.Fatalf("Revisions(1): %v", err)
	}
	if len(one) != 1 || one[0].Rev != 2 {
		t.Errorf("Revisions(1) = %v, want just rev 2", one)
	}

	got, ok, err := d.Revision(1)
	if err != nil || !ok {
		t.Fatalf("Revision(1) = (_, %v, %v), want present", ok, err)
	}
	if string(got.Snapshot) != `{"policy":{}}` {
		t.Errorf("Revision(1).Snapshot = %q, want the stored object", got.Snapshot)
	}
	if got.Source != "cli" {
		t.Errorf("Revision(1).Source = %q, want cli", got.Source)
	}

	if _, ok, err := d.Revision(99); err != nil || ok {
		t.Errorf("Revision(99) = (_, %v, %v), want absent", ok, err)
	}
}

// TestRevisionsOnSchemaFive: a database migrated only to v5 has no
// config_revision table, and every read reports empty or absent rather than
// erroring. The fstest.MapFS pattern is TestOpenReadOnlySchemaOne's
// (config_test.go:146).
func TestRevisionsOnSchemaFive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	fsys := fstest.MapFS{}
	for _, name := range []string{
		"001_initial.sql",
		"002_config.sql",
		"003_binding_record.sql",
		"004_round_file.sql",
		"005_owner_scope.sql",
	} {
		data, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		fsys["migrations/"+name] = &fstest.MapFile{Data: data}
	}

	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := applyMigrations(sqlDB, fsys); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	d, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer d.Close()

	rows, err := d.Revisions(0)
	if err != nil || len(rows) != 0 {
		t.Fatalf("Revisions on schema 5 = (%v, %v), want (empty, nil)", rows, err)
	}
	if _, ok, err := d.Revision(1); err != nil || ok {
		t.Fatalf("Revision on schema 5 = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}
