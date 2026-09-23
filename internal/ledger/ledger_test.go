package ledger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/legacy"
)

func TestExpired(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		until time.Time
		want  bool
	}{
		{"zero Until", time.Time{}, false},
		{"Until in future", now.Add(time.Minute), false},
		{"Until equals now", now, true},
		{"Until in past", now.Add(-time.Minute), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Entry{Until: tt.until}
			if got := e.Expired(now); got != tt.want {
				t.Errorf("Expired(%v) with Until %v = %v, want %v", now, tt.until, got, tt.want)
			}
		})
	}
}

// TestLoadReadsLegacySource pins #292 §1: an entry recorded before the rename
// carries "source":"relay" and must read as relevo's own, not as an unknown // name-guard: legacy
// source preserved in Other. A Save then writes "relevo".
func TestLoadReadsLegacySource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	doc := `{"entries":[{"kind":"rate_limited","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"` + legacy.LedgerSource + `"}]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(l.Entries) != 1 {
		t.Fatalf("Entries = %+v, want the one pre-rename entry read as relevo's", l.Entries)
	}
	if len(l.Other) != 0 {
		t.Fatalf("Other = %+v, want empty: a relay source must not be kept raw", l.Other) // name-guard: legacy
	}
	if got := l.Entries[0].Source; got != "relevo" {
		t.Errorf("Source = %q, want \"relevo\"", got)
	}

	if err := Save(path, l); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"source": "relevo"`) {
		t.Errorf("saved ledger = %s, want the source rewritten to \"relevo\"", raw)
	}
	if strings.Contains(string(raw), `"source": "`+legacy.LedgerSource+`"`) {
		t.Errorf("saved ledger = %s, want no relay source left", raw) // name-guard: legacy
	}
}

func TestPruneAppendClearArePure(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	e1 := Entry{
		Kind:    SpawnFailed,
		Subject: "claude/anthropic/sonnet",
		At:      now.Add(-2 * time.Hour),
		Until:   now.Add(time.Hour),
		Source:  "relevo",
	}
	e2 := Entry{
		Kind:    RateLimited,
		Subject: "anthropic",
		At:      now.Add(-2 * time.Hour),
		Until:   now.Add(-time.Hour),
		Source:  "planner",
	}
	e3 := Entry{
		Kind:    SpawnFailed,
		Subject: "opencode/openrouter/deepseek",
		At:      now.Add(-time.Hour),
		Source:  "relevo",
	}

	origEntries := []Entry{e1, e2, e3}
	l := Ledger{Entries: origEntries}
	origCopy := append([]Entry(nil), origEntries...)

	// Prune drops expired entry e2
	pruned := l.Prune(now)
	if !reflect.DeepEqual(l.Entries, origCopy) {
		t.Fatalf("Prune mutated receiver entries: got %v, want %v", l.Entries, origCopy)
	}
	wantPruned := []Entry{e1, e3}
	if !reflect.DeepEqual(pruned.Entries, wantPruned) {
		t.Errorf("Prune() = %v, want %v", pruned.Entries, wantPruned)
	}

	// Append adds e4 at the end without mutating original
	e4 := Entry{
		Kind:    RateLimited,
		Subject: "google",
		At:      now,
		Source:  "planner",
	}
	appended := l.Append(e4)
	if !reflect.DeepEqual(l.Entries, origCopy) {
		t.Fatalf("Append mutated receiver entries: got %v, want %v", l.Entries, origCopy)
	}
	wantAppended := []Entry{e1, e2, e3, e4}
	if !reflect.DeepEqual(appended.Entries, wantAppended) {
		t.Errorf("Append() = %v, want %v", appended.Entries, wantAppended)
	}

	// Clear removes e1 matching SpawnFailed and subject
	cleared := l.Clear(SpawnFailed, "claude/anthropic/sonnet")
	if !reflect.DeepEqual(l.Entries, origCopy) {
		t.Fatalf("Clear mutated receiver entries: got %v, want %v", l.Entries, origCopy)
	}
	wantCleared := []Entry{e2, e3}
	if !reflect.DeepEqual(cleared.Entries, wantCleared) {
		t.Errorf("Clear() = %v, want %v", cleared.Entries, wantCleared)
	}
}

func TestClearMatchesKindAndSubject(t *testing.T) {
	e1 := Entry{Kind: RateLimited, Subject: "anthropic"}
	e2 := Entry{Kind: RateLimited, Subject: "google"}
	e3 := Entry{Kind: SpawnFailed, Subject: "anthropic"}

	l := Ledger{Entries: []Entry{e1, e2, e3}}
	got := l.Clear(RateLimited, "anthropic")

	want := []Entry{e2, e3}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Errorf("Clear(RateLimited, anthropic) = %v, want %v", got.Entries, want)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	l, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) unexpected error: %v", path, err)
	}
	if len(l.Entries) != 0 {
		t.Errorf("Load(%q) got %d entries, want 0", path, len(l.Entries))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	tz := time.FixedZone("EST", -5*3600)
	orig := Ledger{
		Entries: []Entry{
			{
				Kind:    SpawnFailed,
				Subject: "claude/anthropic/sonnet",
				At:      time.Date(2026, 9, 11, 14, 0, 0, 0, tz),
				Until:   time.Date(2026, 9, 11, 14, 10, 0, 0, tz),
				Note:    "exit 1",
				Source:  "relevo",
				Binding: "cand-a",
			},
			{
				Kind:    RateLimited,
				Subject: "anthropic",
				At:      time.Date(2026, 9, 11, 15, 0, 0, 0, tz),
				Until:   time.Time{},
				Note:    "5-hour window hit",
				Source:  "planner",
				Binding: "cand-b",
			},
		},
	}

	if err := Save(path, orig); err != nil {
		t.Fatalf("Save(%q) failed: %v", path, err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) failed: %v", path, err)
	}

	norm := func(l Ledger) Ledger {
		entries := make([]Entry, len(l.Entries))
		for i, e := range l.Entries {
			e.At = e.At.UTC()
			if !e.Until.IsZero() {
				e.Until = e.Until.UTC()
			}
			entries[i] = e
		}
		return Ledger{Entries: entries}
	}

	if !reflect.DeepEqual(norm(orig), norm(loaded)) {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", norm(loaded), norm(orig))
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantWhy string
	}{
		{
			name: "subject is empty",
			json: `{
  "entries": [
    {
      "kind": "spawn_failed",
      "subject": "",
      "at": "2026-09-11T15:00:00Z",
      "source": "relevo"
    }
  ]
}`,
			wantWhy: "subject is empty",
		},
		{
			name: "at is zero",
			json: `{
  "entries": [
    {
      "kind": "spawn_failed",
      "subject": "anthropic",
      "source": "relevo"
    }
  ]
}`,
			wantWhy: "at is zero",
		},
		{
			name: "until precedes at",
			json: `{
  "entries": [
    {
      "kind": "spawn_failed",
      "subject": "anthropic",
      "at": "2026-09-11T15:00:00Z",
      "until": "2026-09-11T14:00:00Z",
      "source": "relevo"
    }
  ]
}`,
			wantWhy: "until precedes at",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger.json")
			if err := os.WriteFile(path, []byte(tt.json), 0o644); err != nil {
				t.Fatalf("WriteFile failed: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load() expected error, got nil")
			}
			if !errors.Is(err, ErrBadEntry) {
				t.Errorf("Load() err = %v, want errors.Is(..., ErrBadEntry)", err)
			}
			if !strings.Contains(err.Error(), "entry 0") {
				t.Errorf("Load() err %q does not contain %q", err.Error(), "entry 0")
			}
			if !strings.Contains(err.Error(), tt.wantWhy) {
				t.Errorf("Load() err %q does not contain %q", err.Error(), tt.wantWhy)
			}
		})
	}
}

// TestLoadUnknownKindOrSourceIsPreserved pins #372 §4.2: an entry whose kind
// or source this binary does not know is kept raw in Other instead of failing
// the whole ledger, so a record a newer relevo wrote survives a rollback.
func TestLoadUnknownKindOrSourceIsPreserved(t *testing.T) {
	unknownKind := `{"kind":"future_kind","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"relevo"}`
	unknownSource := `{"kind":"spawn_failed","subject":"future/subject","at":"2026-09-11T15:00:00Z","source":"future_source"}`
	known := `{"kind":"rate_limited","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"planner"}`

	path := filepath.Join(t.TempDir(), "ledger.json")
	doc := `{"entries":[` + unknownKind + `,` + unknownSource + `,` + known + `]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	l, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if len(l.Entries) != 1 || l.Entries[0].Kind != RateLimited {
		t.Fatalf("Entries = %+v, want the one known rate_limited entry", l.Entries)
	}
	if len(l.Other) != 2 {
		t.Fatalf("Other has %d entries, want 2: %v", len(l.Other), l.Other)
	}
	if !sameJSON(t, l.Other[0], unknownKind) || !sameJSON(t, l.Other[1], unknownSource) {
		t.Errorf("Other = %v, want the unknown entries preserved verbatim", l.Other)
	}
}

// TestSaveCarriesOtherThroughMutation is the survival test: Other must ride
// through Load -> Prune/Append -> Save untouched. Drop Other from Save and
// this test fails (#372 §4.2).
func TestSaveCarriesOtherThroughMutation(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	unknownKind := `{"kind":"future_kind","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"relevo"}`
	unknownSource := `{"kind":"spawn_failed","subject":"future/subject","at":"2026-09-11T15:00:00Z","source":"future_source"}`
	known := `{"kind":"rate_limited","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"planner"}`

	path := filepath.Join(t.TempDir(), "ledger.json")
	doc := `{"entries":[` + unknownKind + `,` + unknownSource + `,` + known + `]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	l, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	appended := Entry{
		Kind:    RateLimited,
		Subject: "google",
		At:      now,
		Source:  "planner",
	}
	if err := Save(path, l.Prune(now).Append(appended)); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() after Save unexpected error: %v", err)
	}
	if len(reloaded.Entries) != 2 {
		t.Fatalf("Entries after Save = %+v, want the 2 known entries", reloaded.Entries)
	}
	if len(reloaded.Other) != 2 {
		t.Fatalf("Other after Save has %d entries, want 2: %v", len(reloaded.Other), reloaded.Other)
	}
	if !sameJSON(t, reloaded.Other[0], unknownKind) || !sameJSON(t, reloaded.Other[1], unknownSource) {
		t.Errorf("Other after Save = %v, want the unknown entries preserved verbatim", reloaded.Other)
	}
}

// sameJSON reports whether raw and want encode the same JSON value, ignoring
// formatting differences.
func sameJSON(t *testing.T, raw json.RawMessage, want string) bool {
	t.Helper()
	var got, exp any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal raw %q: %v", raw, err)
	}
	if err := json.Unmarshal([]byte(want), &exp); err != nil {
		t.Fatalf("unmarshal want %q: %v", want, err)
	}
	return reflect.DeepEqual(got, exp)
}

func TestGated(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	refs := []string{
		"agy/google/m",
		"claude/anthropic/opus",
		"claude/anthropic/sonnet",
		"opencode/openrouter/z-ai/m",
	}
	providerOf := func(token string) string {
		return strings.Split(token, "/")[1]
	}

	l := Ledger{
		Entries: []Entry{
			{Kind: RateLimited, Subject: "anthropic", At: now, Source: "planner"},
			{Kind: SpawnFailed, Subject: "agy/google/m", At: now, Until: now.Add(10 * time.Minute), Source: "relevo"},
			{Kind: SpawnFailed, Subject: "opencode/openrouter/z-ai/m", At: now, Until: now.Add(-time.Minute), Source: "relevo"},
			{Kind: SpawnFailed, Subject: "claude/anthropic/haiku", At: now, Until: now.Add(10 * time.Minute), Source: "relevo"},
		},
	}

	got := Gated(l, refs, providerOf, now)

	wantTokens := []string{"agy/google/m", "claude/anthropic/opus", "claude/anthropic/sonnet"}
	if len(got) != len(wantTokens) {
		t.Fatalf("Gated() returned %d gates, want %d: %+v", len(got), len(wantTokens), got)
	}
	for i, tok := range wantTokens {
		if got[i].Token != tok {
			t.Errorf("gate %d token = %q, want %q", i, got[i].Token, tok)
		}
	}
	if got[0].Kind != SpawnFailed {
		t.Errorf("gate 0 kind = %v, want SpawnFailed", got[0].Kind)
	}
	if got[1].Kind != RateLimited || got[2].Kind != RateLimited {
		t.Errorf("gates 1,2 kind = %v, %v, want RateLimited", got[1].Kind, got[2].Kind)
	}

	if empty := Gated(Ledger{}, refs, providerOf, now); empty != nil {
		t.Errorf("Gated() on empty ledger = %v, want nil", empty)
	}
}

func TestGatedOrdersByTokenThenSince(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	refs := []string{"claude/anthropic/sonnet"}
	providerOf := func(token string) string {
		return strings.Split(token, "/")[1]
	}

	l := Ledger{
		Entries: []Entry{
			{Kind: SpawnFailed, Subject: "claude/anthropic/sonnet", At: now, Source: "relevo"},
			{Kind: SpawnFailed, Subject: "claude/anthropic/sonnet", At: now.Add(-time.Hour), Source: "relevo"},
		},
	}

	got := Gated(l, refs, providerOf, now)
	if len(got) != 2 {
		t.Fatalf("Gated() returned %d gates, want 2: %+v", len(got), got)
	}
	if !got[0].Since.Equal(now.Add(-time.Hour)) || !got[1].Since.Equal(now) {
		t.Errorf("gates not ordered by Since: got %v, %v", got[0].Since, got[1].Since)
	}
}

func TestSaveCreatesParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "sub", "ledger.json")
	l := Ledger{
		Entries: []Entry{
			{
				Kind:    SpawnFailed,
				Subject: "claude/anthropic/sonnet",
				At:      time.Now(),
				Source:  "relevo",
			},
		},
	}
	if err := Save(path, l); err != nil {
		t.Fatalf("Save(%q) failed: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file %q does not exist after Save: %v", path, err)
	}
}
