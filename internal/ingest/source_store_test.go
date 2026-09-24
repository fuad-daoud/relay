package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestIngestStoreSourceFillsTheMirror is P3a §8 step 4's test: a binding held
// in the store's database ingests into the mirror exactly as the same fixture
// did through DirSource -- a binding row and its event rows -- even though
// bind.json and log.jsonl no longer exist as files.
func TestIngestStoreSourceFillsTheMirror(t *testing.T) {
	ctx := context.Background()
	deps := Deps{Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }}

	// DirSource over a fresh copy of the fixture, for the baseline mirror.
	dir := copyFixture(t)
	dirDB := openTestDB(t)
	if _, err := Ingest(ctx, DirSource(dir), dirDB, deps); err != nil {
		t.Fatalf("Ingest(DirSource): %v", err)
	}

	// The same fixture adopted into a store root, so the store's database is
	// the binding's home and bind.json/log.jsonl are gone.
	root := t.TempDir()
	st := store.New(root)
	if err := os.MkdirAll(st.Dir("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(st.Dir("fixture"), e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Load("fixture"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, base := range []string{"bind.json", "log.jsonl"} {
		if _, err := os.Stat(filepath.Join(st.Dir("fixture"), base)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is still a file after the import: %v", base, err)
		}
	}

	storeDB := openTestDB(t)
	if _, err := Ingest(ctx, StoreSource(st, "fixture"), storeDB, deps); err != nil {
		t.Fatalf("Ingest(StoreSource): %v", err)
	}

	dirBinding := mustBinding(t, dirDB, "fixture")
	storeBinding := mustBinding(t, storeDB, "fixture")
	if storeBinding.CWD != dirBinding.CWD || !strEq(storeBinding.FinalState, "needs_you") {
		t.Errorf("StoreSource binding = %+v, want DirSource's %+v", storeBinding, dirBinding)
	}

	dirEvents, err := dirDB.Events(dirBinding.ID, 0)
	if err != nil {
		t.Fatalf("Events(dir): %v", err)
	}
	storeEvents, err := storeDB.Events(storeBinding.ID, 0)
	if err != nil {
		t.Fatalf("Events(store): %v", err)
	}
	if len(storeEvents) == 0 {
		t.Fatal("StoreSource ingested no event rows")
	}
	if len(storeEvents) != len(dirEvents) {
		t.Fatalf("StoreSource ingested %d events, DirSource %d", len(storeEvents), len(dirEvents))
	}
	for i := range storeEvents {
		if got, want := entryOf(t, storeEvents[i].EntryJSON), entryOf(t, dirEvents[i].EntryJSON); got != want {
			t.Errorf("event %d = %s, want %s\nstore: %s\n  dir: %s",
				i, got, want, storeEvents[i].EntryJSON, dirEvents[i].EntryJSON)
		}
	}

	// Round files still come from the binding directory, not from Load.
	if rounds := mustRounds(t, storeDB, storeBinding.ID); len(rounds) != 3 {
		t.Errorf("StoreSource ingested %d rounds, want 3 (the round files)", len(rounds))
	}
}

// entryOf decodes an event's entry_json to the fields both sources must agree
// on. The bytes differ by construction -- DirSource passes the file's line
// through and StoreSource re-marshals a decoded LogEntry -- so the test
// compares meaning, not bytes.
func entryOf(t *testing.T, entryJSON string) string {
	t.Helper()
	var e store.LogEntry
	if err := json.Unmarshal([]byte(entryJSON), &e); err != nil {
		t.Fatalf("decode %s: %v", entryJSON, err)
	}
	return fmt.Sprintf("round=%d %s %s %s %q", e.Round, e.Direction, e.Kind, e.Path, e.Note)
}
