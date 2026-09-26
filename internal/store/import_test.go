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

// TestImportPresentAdoptsLegacyDirs pins the adoption of legacy binding
// directories: both list, the files are gone, and the entries -- including a
// key this binary does not know -- read back unchanged.
func TestImportPresentAdoptsLegacyDirs(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	logOne := seedLegacyDir(t, root, "one", nil)
	seedLegacyDir(t, root, "two", func(raw []byte) []byte {
		// One entry as a newer relevo might have written it.
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
	checkEntriesEqual(t, entries, want)

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

func checkEntriesEqual(t *testing.T, got, want []LogEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Seq != want[i].Seq {
			t.Errorf("entry %d Seq = %d, want %d", i, got[i].Seq, want[i].Seq)
		}
		if got[i].Confirmed != want[i].Confirmed {
			t.Errorf("entry %d Confirmed = %v, want %v", i, got[i].Confirmed, want[i].Confirmed)
		}
		if (got[i].DeliveredAt == nil) != (want[i].DeliveredAt == nil) {
			t.Errorf("entry %d DeliveredAt = %v, want %v", i, got[i].DeliveredAt, want[i].DeliveredAt)
		}
		if got[i].Route != want[i].Route {
			t.Errorf("entry %d Route = %q, want %q", i, got[i].Route, want[i].Route)
		}
	}
}

// TestListSkipsANewerFormatBinding pins that one bind.json written by a newer
// relevo must not break List for the other bindings.
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

// TestImportPresentAdoptsViewedSidecar pins the .viewed sidecar import: the
// file becomes the record's viewed_at and goes; an existing stamp is kept.
func TestImportPresentAdoptsViewedSidecar(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 2
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stamp := time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC)
	viewed := s.ViewedPath("webshop")
	if err := os.WriteFile(viewed, []byte("2026-09-20T08:30:00Z\n"), 0o644); err != nil {
		t.Fatalf("write .viewed: %v", err)
	}
	if err := os.Chtimes(viewed, stamp, stamp); err != nil {
		t.Fatalf("chtimes .viewed: %v", err)
	}

	got, ok := s.ViewedAt("webshop")
	if !ok || !got.Equal(stamp) {
		t.Fatalf("ViewedAt after the import = (%v, %v), want (%v, true)", got, ok, stamp)
	}
	if _, err := os.Stat(viewed); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".viewed is still present after the import: %v", err)
	}

	// A stamp the record already has is kept, and the sidecar still goes.
	kept := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	if err := s.MarkViewed("webshop", kept); err != nil {
		t.Fatalf("MarkViewed: %v", err)
	}
	if err := os.WriteFile(viewed, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("rewrite .viewed: %v", err)
	}
	if err := os.Chtimes(viewed, stamp, stamp); err != nil {
		t.Fatalf("chtimes .viewed: %v", err)
	}
	got, ok = s.ViewedAt("webshop")
	if !ok || !got.Equal(kept) {
		t.Fatalf("ViewedAt after a stale sidecar = (%v, %v), want (%v, true)", got, ok, kept)
	}
	if _, err := os.Stat(viewed); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a stale .viewed was not removed: %v", err)
	}
}
