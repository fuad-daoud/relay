package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testRunLog returns a KVLog over a fresh temp database, with dir the state
// root a legacy hooks.log would live in.
func testRunLog(t *testing.T) (*KVLog, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return NewKVLog(db.TxKV{DB: d}, dir), dir
}

func TestKVLogAppendAndRead(t *testing.T) {
	log, _ := testRunLog(t)
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	if err := log.Append(HookRun{At: at, Event: "state_changed", Argv: []string{"/bin/true"}, Output: "ok\n"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := log.Append(HookRun{At: at, Event: "round_started", Argv: []string{"/bin/false"}, ExitCode: 1, Error: "exit status 1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	runs, err := log.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("Runs returned %d entries, want 2", len(runs))
	}
	if runs[0].Event != "state_changed" || runs[0].Output != "ok\n" {
		t.Errorf("first run = %+v", runs[0])
	}
	if runs[1].Event != "round_started" || runs[1].Error != "exit status 1" || runs[1].ExitCode != 1 {
		t.Errorf("second run = %+v", runs[1])
	}
	if !runs[1].At.Equal(at) {
		t.Errorf("At = %v, want %v", runs[1].At, at)
	}
}

// TestKVLogCapsAt200 pins the cap: the newest 200 runs survive and the oldest
// are dropped.
func TestKVLogCapsAt200(t *testing.T) {
	log, _ := testRunLog(t)
	for i := 0; i < runLogCap+5; i++ {
		if err := log.Append(HookRun{At: time.Now().UTC(), Event: "e", Output: string(rune('a' + i%26))}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	runs, err := log.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != runLogCap {
		t.Fatalf("Runs returned %d entries, want the cap %d", len(runs), runLogCap)
	}
}

// TestKVLogImportsLegacyFile pins the import: a present <root>/hooks.log
// becomes one "imported" run holding the file's last 4 KiB, and the file is
// removed.
func TestKVLogImportsLegacyFile(t *testing.T) {
	log, dir := testRunLog(t)
	path := filepath.Join(dir, legacyLogName)
	body := strings.Repeat("x", importOutputCap) + "THE-TAIL"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	runs, err := log.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Event != "imported" {
		t.Fatalf("Runs = %+v, want one imported run", runs)
	}
	if !strings.HasSuffix(runs[0].Output, "THE-TAIL") {
		t.Errorf("imported output does not end with the file's tail: %q", runs[0].Output)
	}
	if len(runs[0].Output) != importOutputCap {
		t.Errorf("imported output is %d bytes, want the last %d", len(runs[0].Output), importOutputCap)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Errorf("the imported file is still there: %v", serr)
	}
}

// TestKVLogKeepsExistingRowOverFile pins the KVImportFile rule: a row already
// present wins, and the file is left where it is.
func TestKVLogKeepsExistingRowOverFile(t *testing.T) {
	log, dir := testRunLog(t)
	if err := log.Append(HookRun{At: time.Now().UTC(), Event: "state_changed"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := filepath.Join(dir, legacyLogName)
	if err := os.WriteFile(path, []byte("old log\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	runs, err := log.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Event != "state_changed" {
		t.Fatalf("Runs = %+v, want the row's own run", runs)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Errorf("the row won, so the file must stay: %v", serr)
	}
}
