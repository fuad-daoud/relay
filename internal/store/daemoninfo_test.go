package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDaemonInfoRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	base := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	want := DaemonInfo{
		Version:    "v1.2.3",
		PID:        4242,
		StartedAt:  base,
		Exe:        "/usr/local/bin/relevo",
		ExeID:      FileID{Dev: 1, Ino: 2, Size: 3, ModTime: base},
		ReexecFrom: "v1.2.2",
		ReexecFailed: &ReexecFailure{
			ExeID:  FileID{Dev: 4, Ino: 5, Size: 6, ModTime: base.Add(time.Hour)},
			At:     base.Add(time.Minute),
			Reason: "policy.json: unknown field",
		},
	}

	if err := s.WriteDaemonInfo(want); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}
	got, ok, err := s.ReadDaemonInfo()
	if err != nil {
		t.Fatalf("ReadDaemonInfo: %v", err)
	}
	if !ok {
		t.Fatal("ReadDaemonInfo: ok = false, want true")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestWriteDaemonInfoOmitsEmptyOptional pins the omitempty tags: a plain start
// must not store an empty reexec_from or a null reexec_failed.
func TestWriteDaemonInfoOmitsEmptyOptional(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.WriteDaemonInfo(DaemonInfo{Version: "v1"}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	raw, ok, err := d.KVGet(daemonInfoKey)
	if err != nil || !ok {
		t.Fatalf("KVGet(%s) = (_, %v, %v), want the row", daemonInfoKey, ok, err)
	}
	for _, key := range []string{"reexec_from", "reexec_failed"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("daemon row contains %q for a plain start: %s", key, raw)
		}
	}
}

func TestReadDaemonInfoMissingIsNotAnError(t *testing.T) {
	s := New(t.TempDir())
	info, ok, err := s.ReadDaemonInfo()
	if err != nil {
		t.Fatalf("ReadDaemonInfo on a missing record: %v", err)
	}
	if ok {
		t.Error("ok = true for a missing record, want false")
	}
	if !reflect.DeepEqual(info, DaemonInfo{}) {
		t.Errorf("info = %+v, want the zero value", info)
	}
}

// TestReadDaemonInfoImportsLegacyFile pins the import rule (P3b plan §4.4): a
// legacy daemon.json beside an existing database is imported on first read and
// removed.
func TestReadDaemonInfoImportsLegacyFile(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	// Create the database without writing the record, so the legacy file is
	// the only source.
	if _, err := s.DB(); err != nil {
		t.Fatalf("DB: %v", err)
	}

	want := DaemonInfo{Version: "v1.2.3", PID: 4242}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	legacy := filepath.Join(root, daemonInfoFileName)
	if err := os.WriteFile(legacy, raw, 0o644); err != nil {
		t.Fatalf("seed daemon.json: %v", err)
	}

	got, ok, err := s.ReadDaemonInfo()
	if err != nil || !ok {
		t.Fatalf("ReadDaemonInfo = (_, %v, %v), want the imported record", ok, err)
	}
	if got.Version != want.Version || got.PID != want.PID {
		t.Errorf("imported record = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("daemon.json still exists after import: err = %v", err)
	}
}

func TestReadDaemonInfoMalformedIsAnError(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	// The database must exist for the legacy file to be read at all.
	if _, err := s.DB(); err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, daemonInfoFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed malformed daemon.json: %v", err)
	}
	if _, _, err := s.ReadDaemonInfo(); err == nil {
		t.Fatal("ReadDaemonInfo on malformed JSON: err = nil, want an error")
	}
}

// TestWriteDaemonInfoIsAtomic checks the write leaves no temp file behind and
// the record reads back: the kv upsert either lands or it does not.
func TestWriteDaemonInfoIsAtomic(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.WriteDaemonInfo(DaemonInfo{Version: "v1"}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp") {
			t.Errorf("temp file %q survived the write", e.Name())
		}
	}
	if info, ok, err := s.ReadDaemonInfo(); err != nil || !ok || info.Version != "v1" {
		t.Errorf("ReadDaemonInfo after write = (%+v, %v, %v), want the record", info, ok, err)
	}
}

func TestRemoveDaemonInfo(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.WriteDaemonInfo(DaemonInfo{Version: "v1"}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}
	if err := s.RemoveDaemonInfo(); err != nil {
		t.Fatalf("RemoveDaemonInfo: %v", err)
	}
	if _, ok, err := s.ReadDaemonInfo(); err != nil || ok {
		t.Fatalf("after RemoveDaemonInfo: ok=%v err=%v, want false, nil", ok, err)
	}
	// Removing again is a no-op: the clean-shutdown path may race nothing, but
	// it must never fail on a file already gone.
	if err := s.RemoveDaemonInfo(); err != nil {
		t.Fatalf("RemoveDaemonInfo on a missing file: %v", err)
	}
}
