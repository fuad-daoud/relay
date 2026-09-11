package history

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/ledger"
)

// now is the fixed clock every test in this package reasons from.
var now = time.Date(2026, 9, 11, 21, 15, 0, 0, time.UTC)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")

	h := History{}.
		Append(Event{At: now, Kind: ledger.RateLimited, Provider: "anthropic", Source: "planner", Note: "5h"}).
		Append(Event{At: now.Add(time.Minute), Kind: ledger.SpawnFailed, Provider: "anthropic", Token: "claude/anthropic/sonnet", Source: "relay", Binding: "webshop"})

	if err := Save(path, h); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Events) != len(h.Events) {
		t.Fatalf("got %d events, want %d: %+v", len(got.Events), len(h.Events), got.Events)
	}
	for i, e := range got.Events {
		want := h.Events[i]
		if !e.At.Equal(want.At) {
			t.Errorf("event %d At = %v, want %v", i, e.At, want.At)
		}
		if e.Kind != want.Kind || e.Provider != want.Provider || e.Token != want.Token ||
			e.Source != want.Source || e.Binding != want.Binding || e.Note != want.Note {
			t.Errorf("event %d = %+v, want %+v", i, e, want)
		}
	}
}

func TestRoundTripMissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "history.json")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	if len(got.Events) != 0 {
		t.Errorf("got %d events, want 0: %+v", len(got.Events), got.Events)
	}
}

func TestPruneWindow(t *testing.T) {
	kept := Event{At: now.Add(-RetainWindow), Kind: ledger.RateLimited, Provider: "test", Source: "planner"}
	dropped := Event{At: now.Add(-RetainWindow - time.Second), Kind: ledger.RateLimited, Provider: "test", Source: "planner"}

	h := History{}.Append(kept).Append(dropped)
	pruned := h.Prune(now)

	if len(pruned.Events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(pruned.Events), pruned.Events)
	}
	if !pruned.Events[0].At.Equal(kept.At) {
		t.Errorf("kept event At = %v, want %v", pruned.Events[0].At, kept.At)
	}
}

func TestFromEntry(t *testing.T) {
	rateLimited := ledger.Entry{Kind: ledger.RateLimited, Subject: "anthropic", At: now, Source: "planner", Note: "5h"}
	got := FromEntry(rateLimited, func(string) string { return "" })
	want := Event{Provider: "anthropic", Token: "", Kind: ledger.RateLimited, Source: "planner", Note: "5h", At: now}
	if got != want {
		t.Errorf("FromEntry(rate_limited) = %+v, want %+v", got, want)
	}

	spawnFailed := ledger.Entry{Kind: ledger.SpawnFailed, Subject: "claude/anthropic/sonnet", Binding: "webshop", Source: "relay"}
	got = FromEntry(spawnFailed, func(string) string { return "anthropic" })
	want = Event{Provider: "anthropic", Token: "claude/anthropic/sonnet", Kind: ledger.SpawnFailed, Binding: "webshop", Source: "relay"}
	if got != want {
		t.Errorf("FromEntry(spawn_failed) = %+v, want %+v", got, want)
	}
}

func TestHourCounts(t *testing.T) {
	at2130 := time.Date(2026, 9, 11, 21, 30, 0, 0, time.UTC)
	at2159 := time.Date(2026, 9, 11, 21, 59, 0, 0, time.UTC)
	at2200 := time.Date(2026, 9, 11, 22, 0, 0, 0, time.UTC)
	otherProvider := time.Date(2026, 9, 11, 21, 10, 0, 0, time.UTC)
	otherKind := time.Date(2026, 9, 11, 21, 20, 0, 0, time.UTC)

	h := History{}.
		Append(Event{At: at2130, Kind: ledger.RateLimited, Provider: "test"}).
		Append(Event{At: at2159, Kind: ledger.RateLimited, Provider: "test"}).
		Append(Event{At: at2200, Kind: ledger.RateLimited, Provider: "test"}).
		Append(Event{At: otherProvider, Kind: ledger.RateLimited, Provider: "other"}).
		Append(Event{At: otherKind, Kind: ledger.SpawnFailed, Provider: "test"})

	counts := HourCounts(h, "test", ledger.RateLimited, time.UTC)
	for hour, c := range counts {
		want := 0
		switch hour {
		case 21:
			want = 2
		case 22:
			want = 1
		}
		if c != want {
			t.Errorf("UTC counts[%d] = %d, want %d", hour, c, want)
		}
	}

	plus1 := time.FixedZone("plus1", 3600)
	counts = HourCounts(h, "test", ledger.RateLimited, plus1)
	for hour, c := range counts {
		want := 0
		switch hour {
		case 22:
			want = 2
		case 23:
			want = 1
		}
		if c != want {
			t.Errorf("plus1 counts[%d] = %d, want %d", hour, c, want)
		}
	}
}
