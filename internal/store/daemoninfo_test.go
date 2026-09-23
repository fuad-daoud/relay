package store

import (
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
// must not write an empty reexec_from or a null reexec_failed.
func TestWriteDaemonInfoOmitsEmptyOptional(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.WriteDaemonInfo(DaemonInfo{Version: "v1"}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, daemonInfoFileName))
	if err != nil {
		t.Fatalf("read daemon.json: %v", err)
	}
	for _, key := range []string{"reexec_from", "reexec_failed"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("daemon.json contains %q for a plain start: %s", key, raw)
		}
	}
}

func TestReadDaemonInfoMissingIsNotAnError(t *testing.T) {
	s := New(t.TempDir())
	info, ok, err := s.ReadDaemonInfo()
	if err != nil {
		t.Fatalf("ReadDaemonInfo on a missing file: %v", err)
	}
	if ok {
		t.Error("ok = true for a missing file, want false")
	}
	if !reflect.DeepEqual(info, DaemonInfo{}) {
		t.Errorf("info = %+v, want the zero value", info)
	}
}

func TestReadDaemonInfoMalformedIsAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, daemonInfoFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed malformed daemon.json: %v", err)
	}
	if _, _, err := New(root).ReadDaemonInfo(); err == nil {
		t.Fatal("ReadDaemonInfo on malformed JSON: err = nil, want an error")
	}
}

// TestWriteDaemonInfoIsAtomic checks the write leaves no temp file behind: the
// temp-and-rename either lands daemon.json or nothing, never both.
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
	if _, err := os.Stat(filepath.Join(root, daemonInfoFileName)); err != nil {
		t.Errorf("daemon.json missing after write: %v", err)
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
