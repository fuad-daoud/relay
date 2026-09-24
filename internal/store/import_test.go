package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacyFixture returns the ingest package's binding fixture, the legacy
// bind.json + log.jsonl pair the import must adopt.
func legacyFixture(t *testing.T, base string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", "binding-three-rounds", base))
	if err != nil {
		t.Fatalf("read fixture %s: %v", base, err)
	}
	return raw
}

// seedLegacyDir writes a legacy binding directory named name, holding the
// fixture's bind.json (renamed to the directory, as the store always wrote it)
// and its log.jsonl, optionally patched by patchLog. It returns the log bytes
// it wrote.
func seedLegacyDir(t *testing.T, root, name string, patchLog func([]byte) []byte) []byte {
	t.Helper()

	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(legacyFixture(t, "bind.json"), &doc); err != nil {
		t.Fatalf("decode fixture bind.json: %v", err)
	}
	doc["name"] = name
	bindJSON, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), bindJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	logJSON := legacyFixture(t, "log.jsonl")
	if patchLog != nil {
		logJSON = patchLog(logJSON)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), logJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	return logJSON
}

// TestImportPresentAdoptsLegacyDirs is P3a §8 step 2's case: a root holding two
// legacy binding directories (bind.json + log.jsonl, from
// internal/ingest/testdata) lists both, the files are gone, and ReadLog
// returns the same entries -- Seq, Confirmed, DeliveredAt, Route, and a key
// this binary does not know kept in entry_json.
func TestImportPresentAdoptsLegacyDirs(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	logOne := seedLegacyDir(t, root, "one", nil)
	seedLegacyDir(t, root, "two", func(raw []byte) []byte {
		// One entry as a newer relevo might have written it: an unknown key,
		// a route and a delivery stamp.
		patched := bytes.Replace(raw, []byte(`"kind":"pick"`),
			[]byte(`"kind":"pick","future_key":"kept","route":"channel","delivered_at":"2026-09-10T10:00:04.000Z"`), 1)
		if bytes.Equal(patched, raw) {
			t.Fatal("the fixture no longer holds the pick entry to patch")
		}
		return patched
	})

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "one" || got[1].Name != "two" {
		t.Fatalf("List = %+v, want the two imported bindings in name order", got)
	}
	if got[0].State != StateNeedsYou || got[0].Round != 3 || got[0].CWD != "/work/fixture" {
		t.Errorf("imported binding = %+v, want the fixture's fields", got[0])
	}

	// The files are gone once their DB writes committed.
	for _, name := range []string{"one", "two"} {
		for _, base := range []string{"bind.json", "log.jsonl"} {
			if _, err := os.Stat(filepath.Join(root, name, base)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s/%s is still present after the import: %v", name, base, err)
			}
		}
	}

	// ReadLog returns the same entries the file held.
	want, err := decodeLog(bytes.NewReader(logOne))
	if err != nil {
		t.Fatalf("decodeLog fixture: %v", err)
	}
	entries, err := s.ReadLog("one")
	if err != nil {
		t.Fatalf("ReadLog(one): %v", err)
	}
	if len(entries) != len(want) {
		t.Fatalf("ReadLog(one) returned %d entries, want %d", len(entries), len(want))
	}
	for i := range entries {
		if entries[i].Seq != want[i].Seq {
			t.Errorf("entry %d Seq = %d, want %d", i, entries[i].Seq, want[i].Seq)
		}
		if entries[i].Confirmed != want[i].Confirmed {
			t.Errorf("entry %d Confirmed = %v, want %v", i, entries[i].Confirmed, want[i].Confirmed)
		}
		if (entries[i].DeliveredAt == nil) != (want[i].DeliveredAt == nil) {
			t.Errorf("entry %d DeliveredAt = %v, want %v", i, entries[i].DeliveredAt, want[i].DeliveredAt)
		}
		if entries[i].Route != want[i].Route {
			t.Errorf("entry %d Route = %q, want %q", i, entries[i].Route, want[i].Route)
		}
	}

	// The patched entry keeps its key, its route and its delivery stamp.
	two, err := s.ReadLog("two")
	if err != nil {
		t.Fatalf("ReadLog(two): %v", err)
	}
	if len(two) == 0 {
		t.Fatal("ReadLog(two) returned no entries")
	}
	if two[0].Route != "channel" {
		t.Errorf("entry 0 Route = %q, want channel", two[0].Route)
	}
	if two[0].DeliveredAt == nil || !two[0].DeliveredAt.Equal(time.Date(2026, 9, 10, 10, 0, 4, 0, time.UTC)) {
		t.Errorf("entry 0 DeliveredAt = %v, want 2026-09-10T10:00:04Z", two[0].DeliveredAt)
	}
	stored := bindingEvents(t, s, "two")
	if len(stored) == 0 || !strings.Contains(stored[0].JSON, `"future_key":"kept"`) {
		t.Errorf("entry_json lost the unknown key: %+v", stored)
	}
}

// TestImportPresentKeepsANewerFormatFile is the other half of §8 step 2: a
// format-3 bind.json is refused with ErrNewerFormat and the file stays.
func TestImportPresentKeepsANewerFormatFile(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	b := newBinding("webshop", "/home/dev/projects/webshop")
	b.Format = BindingFormat + 1
	if err := os.MkdirAll(s.Dir(b.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir(b.Name), "bind.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = s.Load(b.Name)
	var newer *ErrNewerFormat
	if !errors.As(err, &newer) {
		t.Fatalf("Load of a format-%d binding = %v, want *ErrNewerFormat", BindingFormat+1, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Errorf("the refused import changed the file:\nbefore:\n%s\nafter:\n%s", raw, after)
	}
}

// TestListSkipsANewerFormatBinding pins §B4: one bind.json written by a newer
// relevo must not break List for the bindings this binary understands. The
// import leaves the newer file exactly where it is and skips that name, while
// load() for it still refuses it with ErrNewerFormat.
func TestListSkipsANewerFormatBinding(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	seedLegacyDir(t, root, "ok", nil)

	b := newBinding("future", "/work/future")
	b.Format = BindingFormat + 1
	if err := os.MkdirAll(s.Dir(b.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir(b.Name), "bind.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("List = %+v, want only the binding this binary understands", got)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the newer-format bind.json was removed: %v", err)
	}
	if !bytes.Equal(raw, after) {
		t.Errorf("the skipped import changed the file:\nbefore:\n%s\nafter:\n%s", raw, after)
	}

	var newer *ErrNewerFormat
	if _, err := s.Load("future"); !errors.As(err, &newer) {
		t.Fatalf("Load(future) = %v, want *ErrNewerFormat", err)
	}
}
