package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// archiveFixture creates a binding dir with a log and tars it the way gc
// does, returning the archive path.
func archiveFixture(t *testing.T, s *Store, name, stamp string, entries []LogEntry) string {
	t.Helper()
	dir := s.Dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The fixture is a tarball, not a live binding: build its log member with
	// the bytes Archive writes into one (D2: a live log is a DB row now).
	var logBuf []byte
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		logBuf = append(logBuf, raw...)
		logBuf = append(logBuf, '\n')
	}
	if len(logBuf) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), logBuf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(s.ArchiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(s.ArchiveDir(), name+"-"+stamp+".tar.gz")
	if err := tarGzDir(dir, name, dest); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestListArchivesParsesNameAndStamp(t *testing.T) {
	s := New(t.TempDir())
	if got, err := s.ListArchives(); err != nil || len(got) != 0 {
		t.Fatalf("empty store: %v, %v", got, err)
	}
	archiveFixture(t, s, "webshop", "20260911-215319", nil)
	archiveFixture(t, s, "api-v2", "20260905-135037", nil)
	if err := os.WriteFile(filepath.Join(s.ArchiveDir(), "junk.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListArchives()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("archives = %+v, want 2", got)
	}
	if got[0].Name != "api-v2" || !got[0].At.Equal(time.Date(2026, 9, 5, 13, 50, 37, 0, time.UTC)) {
		t.Errorf("oldest first: %+v", got[0])
	}
	if got[1].Name != "webshop" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestReadArchivedLog(t *testing.T) {
	s := New(t.TempDir())
	entries := []LogEntry{
		{TS: time.Unix(1, 0).UTC(), Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "p"},
		{TS: time.Unix(2, 0).UTC(), Round: 1, Direction: DirToPlanner, Kind: KindDiff},
	}
	path := archiveFixture(t, s, "webshop", "20260911-215319", entries)
	got, err := s.ReadArchivedLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != KindReport || got[1].Round != 1 {
		t.Errorf("got %+v", got)
	}
	empty := archiveFixture(t, s, "nolog", "20260911-215320", nil)
	if got, err := s.ReadArchivedLog(empty); err != nil || got != nil {
		t.Errorf("archive without a log: got %v, %v; want nil, nil", got, err)
	}
	if _, err := s.ReadArchivedLog(filepath.Join(s.ArchiveDir(), "missing.tar.gz")); err == nil {
		t.Error("missing archive must error")
	}
}
