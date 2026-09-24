package db

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

func TestKVRoundTrip(t *testing.T) {
	d := openTestDB(t)

	// Absent reads.
	if _, ok, err := d.KVGet("ledger"); err != nil || ok {
		t.Fatalf("KVGet absent = (_, %v, %v), want (_, false, nil)", ok, err)
	}

	if err := d.KVPut("ledger", []byte(`{"entries":[]}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	got, ok, err := d.KVGet("ledger")
	if err != nil || !ok {
		t.Fatalf("KVGet = (_, %v, %v), want (_, true, nil)", ok, err)
	}
	if string(got) != `{"entries":[]}` {
		t.Errorf("KVGet = %q, want {\"entries\":[]}", got)
	}

	// Upsert overwrites the whole document.
	if err := d.KVPut("ledger", []byte(`{"entries":[1]}`)); err != nil {
		t.Fatalf("KVPut overwrite: %v", err)
	}
	if got, _, _ := d.KVGet("ledger"); string(got) != `{"entries":[1]}` {
		t.Errorf("KVGet after overwrite = %q, want {\"entries\":[1]}", got)
	}

	if err := d.KVDelete("ledger"); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	if _, ok, err := d.KVGet("ledger"); err != nil || ok {
		t.Fatalf("KVGet after delete = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	// A delete of an absent key is not an error.
	if err := d.KVDelete("ledger"); err != nil {
		t.Fatalf("KVDelete absent: %v", err)
	}
}

func TestKVKeysSortedAndFiltered(t *testing.T) {
	d := openTestDB(t)
	for _, k := range []string{"serve.ui", "latency", "ui", "ledger"} {
		if err := d.KVPut(k, []byte(`{}`)); err != nil {
			t.Fatalf("KVPut %s: %v", k, err)
		}
	}

	all, err := d.KVKeys("")
	if err != nil {
		t.Fatalf("KVKeys(\"\"): %v", err)
	}
	want := []string{"latency", "ledger", "serve.ui", "ui"}
	if !reflect.DeepEqual(all, want) {
		t.Errorf("KVKeys(\"\") = %v, want %v", all, want)
	}

	serve, err := d.KVKeys("serve.")
	if err != nil {
		t.Fatalf("KVKeys(serve.): %v", err)
	}
	if !reflect.DeepEqual(serve, []string{"serve.ui"}) {
		t.Errorf("KVKeys(serve.) = %v, want [serve.ui]", serve)
	}

	if none, err := d.KVKeys("nope"); err != nil || len(none) != 0 {
		t.Errorf("KVKeys(nope) = (%v, %v), want ([], nil)", none, err)
	}
}

func TestKVPutRejectsInvalidJSON(t *testing.T) {
	d := openTestDB(t)
	if err := d.KVPut("ledger", []byte("{not json")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("KVPut invalid JSON err = %v, want ErrInvalid", err)
	}
	if _, ok, err := d.KVGet("ledger"); err != nil || ok {
		t.Fatalf("KVGet after refused put = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}

// TestKVSchemaOneTolerance: a database migrated only to v1 has no kv table, so
// a read-only open reports every key as absent rather than erroring -- the same
// tolerance ConfigGet has (P3b plan §4.1).
func TestKVSchemaOneTolerance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	one, err := migrationFiles.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatalf("read migration 001: %v", err)
	}
	fsys := fstest.MapFS{
		"migrations/001_initial.sql": &fstest.MapFile{Data: one},
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

	if _, ok, err := d.KVGet("ledger"); err != nil || ok {
		t.Fatalf("KVGet on schema 1 = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	if keys, err := d.KVKeys(""); err != nil || len(keys) != 0 {
		t.Fatalf("KVKeys on schema 1 = (%v, %v), want ([], nil)", keys, err)
	}
}

func TestKVImportFileImportsThenDeletes(t *testing.T) {
	d := openTestDB(t)
	path := filepath.Join(t.TempDir(), "ledger.json")
	body := `{"entries":[]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, ok, err := KVImportFile(d, "ledger", path)
	if err != nil || !ok {
		t.Fatalf("KVImportFile = (_, %v, %v), want (_, true, nil)", ok, err)
	}
	if string(got) != body {
		t.Errorf("KVImportFile = %q, want %q", got, body)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("imported file still present: stat err = %v, want not-exist", err)
	}
	if row, ok, _ := d.KVGet("ledger"); !ok || string(row) != body {
		t.Errorf("row after import = (%q, %v), want (%q, true)", row, ok, body)
	}
}

func TestKVImportFilePrefersRow(t *testing.T) {
	d := openTestDB(t)
	if err := d.KVPut("ledger", []byte(`{"entries":[1]}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(path, []byte(`{"entries":[2]}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, ok, err := KVImportFile(d, "ledger", path)
	if err != nil || !ok {
		t.Fatalf("KVImportFile = (_, %v, %v), want (_, true, nil)", ok, err)
	}
	if string(got) != `{"entries":[1]}` {
		t.Errorf("KVImportFile = %q, want the row's document", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("KVImportFile removed the file though the row won: %v", err)
	}
}

func TestKVImportFileMissingIsNoOp(t *testing.T) {
	d := openTestDB(t)
	path := filepath.Join(t.TempDir(), "absent.json")

	got, ok, err := KVImportFile(d, "ledger", path)
	if err != nil || ok || got != nil {
		t.Fatalf("KVImportFile(missing) = (%q, %v, %v), want (nil, false, nil)", got, ok, err)
	}
}

func TestKVImportFileInvalidJSONNamesPath(t *testing.T) {
	d := openTestDB(t)
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, ok, err := KVImportFile(d, "ledger", path)
	if err == nil || ok {
		t.Fatalf("KVImportFile(invalid) = (_, %v, %v), want (_, false, err)", ok, err)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("KVImportFile(invalid) err = %v, want wrapping ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("KVImportFile(invalid) err = %q, want it to name %q", err, path)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Errorf("an invalid file must not be deleted: stat err = %v", serr)
	}
}

// failPutKV fails every put: the mutation guard for KVImportFile's order (P3b
// plan step 1). If KVImportFile deleted the file before the put, this test's
// file-exists assertion would fail.
type failPutKV struct{}

func (failPutKV) KVGet(string) ([]byte, bool, error) { return nil, false, nil }
func (failPutKV) KVPut(string, []byte) error         { return errors.New("put failed") }
func (failPutKV) KVDelete(string) error              { return nil }

func TestKVImportFileFailedPutKeepsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(path, []byte(`{"entries":[]}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, ok, err := KVImportFile(failPutKV{}, "ledger", path)
	if err == nil || ok {
		t.Fatalf("KVImportFile with a failing put = (_, %v, %v), want (_, false, err)", ok, err)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Errorf("file removed though the put failed: stat err = %v", serr)
	}
}
