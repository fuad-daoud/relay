package relay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/history"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
)

// loadLedger reads the runtime's ledger for assertions.
func loadLedger(t *testing.T, rt Runtime) ledger.Ledger {
	t.Helper()
	l, err := ledger.Load(rt.LedgerPath)
	if err != nil {
		t.Fatalf("Load ledger: %v", err)
	}
	return l
}

// loadHistory reads the runtime's history for assertions.
func loadHistory(t *testing.T, rt Runtime) history.History {
	t.Helper()
	h, err := history.Load(rt.AvailabilityPath)
	if err != nil {
		t.Fatalf("Load history: %v", err)
	}
	return h
}

// TestRecordSpawnFailureLockedUnderHeldLock pins the fix for the deadlock
// the T2 round-2 report found: switchBuilder runs inside Reconcile's
// Store.WithLock, and recordSpawnFailureLocked must be able to record a
// failed replacement spawn from in there without trying to re-take that
// (non-reentrant) lock. If it ever does, this test hangs until the 5s
// timer fires instead of failing fast, which is why the assertion is a
// select against a timer rather than a bare call.
func TestRecordSpawnFailureLockedUnderHeldLock(t *testing.T) {
	rt := newRuntime(t)

	done := make(chan error, 1)
	go func() {
		done <- rt.Store.WithLock(func(*store.Tx) error {
			recordSpawnFailureLocked(rt, testAgyRef, "webshop", errors.New("boom"))
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WithLock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked")
	}

	l := loadLedger(t, rt)
	count := 0
	for _, e := range l.Entries {
		if e.Kind == ledger.SpawnFailed && e.Subject == testAgyRef {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("spawn_failed entries for %s = %d, want 1", testAgyRef, count)
	}
}

func TestUnavailableRecordsTheProvider(t *testing.T) {
	rt := newRuntime(t)

	provider, err := Unavailable(rt, testClaudeRef, time.Time{}, "5h window")
	if err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if provider != "test" {
		t.Errorf("provider = %q, want test", provider)
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	e := l.Entries[0]
	if e.Kind != ledger.RateLimited || e.Subject != "test" {
		t.Errorf("entry = %+v, want RateLimited for provider test", e)
	}
	if !e.Until.IsZero() {
		t.Errorf("Until = %v, want zero", e.Until)
	}
	if e.Note != "5h window" {
		t.Errorf("Note = %q, want %q", e.Note, "5h window")
	}
	if e.Source != "planner" {
		t.Errorf("Source = %q, want planner", e.Source)
	}
	if e.Binding != "" {
		t.Errorf("Binding = %q, want empty", e.Binding)
	}
}

func TestUnavailableWithUntil(t *testing.T) {
	rt := newRuntime(t)
	until := baseTime.Add(2 * time.Hour)

	if _, err := Unavailable(rt, testClaudeRef, until, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	if !l.Entries[0].Until.Equal(until) {
		t.Errorf("Until = %v, want %v", l.Entries[0].Until, until)
	}
}

func TestUnavailableRefusesAnUnknownToken(t *testing.T) {
	rt := newRuntime(t)

	if _, err := Unavailable(rt, "claude/test/nope", time.Time{}, ""); !errors.Is(err, candidate.ErrUnknownCandidate) {
		t.Errorf("err = %v, want ErrUnknownCandidate", err)
	}
	l := loadLedger(t, rt)
	if len(l.Entries) != 0 {
		t.Errorf("got %d ledger entries, want 0", len(l.Entries))
	}

	if _, err := Unavailable(rt, "claude/test", time.Time{}, ""); !errors.Is(err, candidate.ErrBadRef) {
		t.Errorf("err = %v, want ErrBadRef", err)
	}
}

func TestAvailableByTokenAndByProvider(t *testing.T) {
	rt := newRuntime(t)

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "first"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "second"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	provider, removed, err := Available(rt, "test", ClearedByPlanner)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if provider != "test" || removed != 2 {
		t.Errorf("Available(provider) = %q, %d, want test, 2", provider, removed)
	}
	l := loadLedger(t, rt)
	if len(l.Entries) != 0 {
		t.Errorf("got %d ledger entries, want 0", len(l.Entries))
	}

	provider, removed, err = Available(rt, testClaudeRef, ClearedByPlanner)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	if provider != "test" {
		t.Errorf("provider = %q, want test", provider)
	}
}

func TestAvailableLeavesSpawnFailures(t *testing.T) {
	rt := newRuntime(t)

	recordSpawnFailure(rt, testClaudeRef, "webshop", errors.New("boom"))
	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	_, removed, err := Available(rt, "test", ClearedByPlanner)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	if l.Entries[0].Kind != ledger.SpawnFailed {
		t.Errorf("remaining entry kind = %v, want SpawnFailed", l.Entries[0].Kind)
	}
}

func TestGatesEmptyWhenNoLedger(t *testing.T) {
	rt := newRuntime(t)

	if got := Gates(rt); got != nil {
		t.Errorf("Gates() = %+v, want nil", got)
	}
}

func TestGatesProjectsOntoCandidates(t *testing.T) {
	rt := newRuntime(t)

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	recordSpawnFailure(rt, testAgyRef, "webshop", errors.New("x"))

	gates := Gates(rt)
	if len(gates) != 4 {
		t.Fatalf("got %d gates, want 4: %+v", len(gates), gates)
	}

	// The first two gates are both for agy/test/m: one RateLimited (from
	// the provider-wide gate) and one SpawnFailed. Both have Since ==
	// baseTime, so the sort between them is not guaranteed; assert as a set.
	agyKinds := map[ledger.Kind]bool{}
	for _, g := range gates[:2] {
		if g.Token != testAgyRef {
			t.Errorf("gate = %+v, want token %q", g, testAgyRef)
		}
		agyKinds[g.Kind] = true
	}
	if !agyKinds[ledger.RateLimited] || !agyKinds[ledger.SpawnFailed] {
		t.Errorf("first two gates = %+v, want one RateLimited and one SpawnFailed for %q", gates[:2], testAgyRef)
	}

	if gates[2].Token != testClaudeRef || gates[2].Kind != ledger.RateLimited {
		t.Errorf("gate 2 = %+v, want RateLimited for %q", gates[2], testClaudeRef)
	}
	if gates[3].Token != testOpencodeRef || gates[3].Kind != ledger.RateLimited {
		t.Errorf("gate 3 = %+v, want RateLimited for %q", gates[3], testOpencodeRef)
	}
}

// TestGatesToleratesABadLedger pins that a ledger relay cannot parse is still
// read as empty with its stderr note, never a crash.
func TestGatesToleratesABadLedger(t *testing.T) {
	rt := newRuntime(t)

	if err := os.WriteFile(rt.LedgerPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got := Gates(rt); got != nil {
		t.Errorf("Gates() = %+v, want nil", got)
	}
}

// TestMutateLedgerCarriesUnknownEntries pins #372 §4.2: an entry with an
// unknown kind or source rides through the Load -> Prune -> mutate -> Save
// path mutateLedgerLocked takes, untouched.
func TestMutateLedgerCarriesUnknownEntries(t *testing.T) {
	rt := newRuntime(t)

	doc := `{"entries":[
  {"kind":"future_kind","subject":"test","at":"2026-09-11T15:00:00Z","source":"relay"},
  {"kind":"spawn_failed","subject":"future/subject","at":"2026-09-11T15:00:00Z","source":"future_source"}
]}`
	if err := os.WriteFile(rt.LedgerPath, []byte(doc), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := mutateLedgerLocked(rt, func(l ledger.Ledger) ledger.Ledger {
		return l.Append(ledger.Entry{
			Kind:    ledger.RateLimited,
			Subject: "test",
			At:      baseTime,
			Source:  "planner",
		})
	})
	if err != nil {
		t.Fatalf("mutateLedgerLocked: %v", err)
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 || l.Entries[0].Kind != ledger.RateLimited {
		t.Fatalf("Entries = %+v, want the one appended rate_limited", l.Entries)
	}
	if len(l.Other) != 2 {
		t.Fatalf("Other = %v, want the 2 unknown entries preserved", l.Other)
	}
}

// TestGatesIgnoresUnknownEntries pins that the preserved entries in Other are
// invisible to readers: Gates still returns exactly the known gates (#372
// §4.2).
func TestGatesIgnoresUnknownEntries(t *testing.T) {
	rt := newRuntime(t)

	doc := `{"entries":[
  {"kind":"future_kind","subject":"test","at":"2026-09-11T15:00:00Z","source":"relay"},
  {"kind":"spawn_failed","subject":"future/subject","at":"2026-09-11T15:00:00Z","source":"future_source"},
  {"kind":"rate_limited","subject":"test","at":"2026-09-11T15:00:00Z","source":"planner"}
]}`
	if err := os.WriteFile(rt.LedgerPath, []byte(doc), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	gates := Gates(rt)
	if len(gates) != 3 {
		t.Fatalf("got %d gates, want 3 (one per candidate on provider test): %+v", len(gates), gates)
	}
	for _, g := range gates {
		if g.Kind != ledger.RateLimited {
			t.Errorf("gate = %+v, want only the known rate_limited gate", g)
		}
	}
}

// TestGateKindTextExitedNoReport pins the wording switchBuilder's synthesised
// gate renders through ErrAllGated and skipText (#191): it must never drift
// from "exited without a report".
func TestGateKindTextExitedNoReport(t *testing.T) {
	if got := GateKindText(ledger.ExitedNoReport); got != "exited without a report" {
		t.Errorf("GateKindText(ExitedNoReport) = %q, want %q", got, "exited without a report")
	}
}

// TestGateKindTextRolesMissing pins #238's wording in status, candidates and
// doctor.
func TestGateKindTextRolesMissing(t *testing.T) {
	if got := GateKindText(ledger.RolesMissing); got != "roles missing" {
		t.Errorf("GateKindText(RolesMissing) = %q, want %q", got, "roles missing")
	}
}

// TestGateTimeText pins the one formatter every gate time goes through: the
// clock time alone on today's local date, the date as well otherwise.
func TestGateTimeText(t *testing.T) {
	t.Cleanup(SetGateClock(func() time.Time { return time.Date(2026, 9, 23, 14, 0, 0, 0, time.Local) }))

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"today", time.Date(2026, 9, 23, 22, 16, 0, 0, time.Local), "22:16"},
		{"26 days out", time.Date(2026, 10, 19, 22, 16, 0, 0, time.Local), "Oct 19 22:16"},
		{"tomorrow", time.Date(2026, 9, 24, 0, 5, 0, 0, time.Local), "Sep 24 00:05"},
		{"next year", time.Date(2027, 1, 2, 3, 4, 0, 0, time.Local), "2027-01-02 03:04"},
	}
	for _, tt := range tests {
		if got := GateTimeText(tt.in); got != tt.want {
			t.Errorf("GateTimeText(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestGateUntilText(t *testing.T) {
	if got := GateUntilText(time.Time{}); got != "until cleared" {
		t.Errorf("GateUntilText(zero) = %q, want %q", got, "until cleared")
	}

	fixed := baseTime
	t.Cleanup(SetGateClock(func() time.Time { return fixed }))
	want := "until " + fixed.Local().Format("15:04")
	if got := GateUntilText(fixed); got != want {
		t.Errorf("GateUntilText(fixed) = %q, want %q", got, want)
	}

	// A gate 26 days out shows its date: the --for 632h example.
	t.Cleanup(SetGateClock(func() time.Time { return time.Date(2026, 9, 23, 14, 0, 0, 0, time.Local) }))
	if got := GateUntilText(time.Date(2026, 10, 19, 22, 16, 0, 0, time.Local)); got != "until Oct 19 22:16" {
		t.Errorf("GateUntilText(26d) = %q, want %q", got, "until Oct 19 22:16")
	}
}

func TestGatedNoteEmptyWhenNotGated(t *testing.T) {
	rt := newRuntime(t)

	if got := gatedNote(rt, testClaudeRef); got != "" {
		t.Errorf("gatedNote() = %q, want empty", got)
	}
}

func TestGatedNoteFormatsEveryGate(t *testing.T) {
	rt := newRuntime(t)

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	recordSpawnFailure(rt, testClaudeRef, "webshop", errors.New("boom"))

	got := gatedNote(rt, testClaudeRef)
	if !strings.HasPrefix(got, fmt.Sprintf("note: %s is gated: ", testClaudeRef)) {
		t.Fatalf("gatedNote() = %q, want prefix %q", got, fmt.Sprintf("note: %s is gated: ", testClaudeRef))
	}
	if !strings.HasSuffix(got, "; proceeding") {
		t.Errorf("gatedNote() = %q, want suffix %q", got, "; proceeding")
	}
	if strings.Count(got, "\n") != 0 {
		t.Errorf("gatedNote() = %q, want one line", got)
	}
	if !strings.Contains(got, "rate-limited") || !strings.Contains(got, "spawn failed") {
		t.Errorf("gatedNote() = %q, want both gate kinds present", got)
	}
	if !strings.Contains(got, "boom") {
		t.Errorf("gatedNote() = %q, want the spawn-failure note included", got)
	}
}

func TestMutateLedgerPrunes(t *testing.T) {
	rt := newRuntime(t)

	expired := ledger.Entry{
		Kind:    ledger.RateLimited,
		Subject: "stale",
		At:      baseTime.Add(-time.Hour),
		Until:   baseTime.Add(-time.Minute),
		Source:  "planner",
	}
	if err := ledger.Save(rt.LedgerPath, ledger.Ledger{Entries: []ledger.Entry{expired}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	if l.Entries[0].Subject != "test" {
		t.Errorf("remaining entry subject = %q, want test", l.Entries[0].Subject)
	}
}

func TestUnavailableRecordsHistory(t *testing.T) {
	rt := newRuntime(t)

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}

	h := loadHistory(t, rt)
	if len(h.Events) != 1 {
		t.Fatalf("got %d history events, want 1: %+v", len(h.Events), h.Events)
	}
	want := history.Event{Kind: ledger.RateLimited, Provider: "test", Token: "", Source: "planner", Note: "5h window", At: baseTime}
	if h.Events[0] != want {
		t.Errorf("history event = %+v, want %+v", h.Events[0], want)
	}
}

// TestSwitchSpawnFailureRecordsHistory mirrors
// TestRecordSpawnFailureLockedUnderHeldLock's already-held-lock setup, since
// that is the daemon-switch path recordSpawnFailureLocked serves.
func TestSwitchSpawnFailureRecordsHistory(t *testing.T) {
	rt := newRuntime(t)

	done := make(chan error, 1)
	go func() {
		done <- rt.Store.WithLock(func(*store.Tx) error {
			recordSpawnFailureLocked(rt, testAgyRef, "webshop", errors.New("boom"))
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WithLock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked")
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}

	h := loadHistory(t, rt)
	if len(h.Events) != 1 {
		t.Fatalf("got %d history events, want 1: %+v", len(h.Events), h.Events)
	}
	if h.Events[0].Kind != ledger.SpawnFailed || h.Events[0].Token != testAgyRef {
		t.Errorf("history event = %+v, want SpawnFailed for %q", h.Events[0], testAgyRef)
	}
}

// TestAvailableRecordsClear: a clear that removed something is an
// observation after all (#302). The history gains a Cleared event whose
// Since is the At of the entry the clear removed, so At - Since is how long
// the provider was blocked.
func TestAvailableRecordsClear(t *testing.T) {
	rt := newRuntime(t)

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	rt.Now = func() time.Time { return baseTime.Add(5 * time.Hour) }

	provider, removed, err := Available(rt, "test", ClearedByPlanner)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if provider != "test" || removed != 1 {
		t.Errorf("Available = %q, %d, want test, 1", provider, removed)
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 0 {
		t.Errorf("got %d ledger entries, want 0: %+v", len(l.Entries), l.Entries)
	}

	h := loadHistory(t, rt)
	if len(h.Events) != 2 {
		t.Fatalf("got %d history events, want 2: %+v", len(h.Events), h.Events)
	}
	ev := h.Events[1]
	if ev.Kind != history.Cleared {
		t.Errorf("kind = %q, want %q", ev.Kind, history.Cleared)
	}
	if ev.Provider != "test" {
		t.Errorf("provider = %q, want test", ev.Provider)
	}
	if ev.Source != ClearedByPlanner {
		t.Errorf("source = %q, want %q", ev.Source, ClearedByPlanner)
	}
	if !ev.Since.Equal(baseTime) {
		t.Errorf("Since = %v, want %v", ev.Since, baseTime)
	}
	if !ev.At.Equal(baseTime.Add(5 * time.Hour)) {
		t.Errorf("At = %v, want %v", ev.At, baseTime.Add(5*time.Hour))
	}
}

// TestAvailableNothingClearedRecordsNothing: zero removed is not an error and
// is not an observation either, so the history stays empty.
func TestAvailableNothingClearedRecordsNothing(t *testing.T) {
	rt := newRuntime(t)

	provider, removed, err := Available(rt, "test", ClearedByPlanner)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if provider != "test" || removed != 0 {
		t.Errorf("Available = %q, %d, want test, 0", provider, removed)
	}

	h := loadHistory(t, rt)
	if len(h.Events) != 0 {
		t.Errorf("got %d history events, want 0: %+v", len(h.Events), h.Events)
	}
}

// TestAvailableRefusesUnknownWritesNothing: the refusal has to stop the save,
// so the ledger is not even rewritten to drop its expired entry. The gate is
// given an Until that has passed by the time the clear runs precisely so the
// pruned-and-saved ledger would differ from the file the refusal must leave.
func TestAvailableRefusesUnknownWritesNothing(t *testing.T) {
	rt := newRuntime(t)

	if _, err := Unavailable(rt, testClaudeRef, baseTime.Add(time.Hour), "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	rt.Now = func() time.Time { return baseTime.Add(5 * time.Hour) }

	beforeLedger := readFileBytes(t, rt.LedgerPath)
	beforeHistory := readFileBytes(t, rt.AvailabilityPath)

	if _, _, err := Available(rt, "tset", ClearedByPlanner); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("Available(tset) err = %v, want ErrUnknownProvider", err)
	}

	if got := readFileBytes(t, rt.LedgerPath); string(got) != string(beforeLedger) {
		t.Errorf("ledger.json = %s, want it untouched at %s", got, beforeLedger)
	}
	if got := readFileBytes(t, rt.AvailabilityPath); string(got) != string(beforeHistory) {
		t.Errorf("availability.json = %s, want it untouched at %s", got, beforeHistory)
	}
}

// TestAvailableRejectsBadSource: source is one of two constants, and a
// caller that passes anything else has a bug -- nothing is written.
func TestAvailableRejectsBadSource(t *testing.T) {
	rt := newRuntime(t)

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	beforeLedger := readFileBytes(t, rt.LedgerPath)
	beforeHistory := readFileBytes(t, rt.AvailabilityPath)

	if _, _, err := Available(rt, "test", "bogus"); err == nil {
		t.Fatal("Available(source=\"bogus\") err = nil, want an error")
	}

	if got := readFileBytes(t, rt.LedgerPath); string(got) != string(beforeLedger) {
		t.Errorf("ledger.json = %s, want it untouched at %s", got, beforeLedger)
	}
	if got := readFileBytes(t, rt.AvailabilityPath); string(got) != string(beforeHistory) {
		t.Errorf("availability.json = %s, want it untouched at %s", got, beforeHistory)
	}
}

// readFileBytes reads path for a byte-identity assertion.
func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return data
}

func TestHistoryFailureDoesNotFailTheLedger(t *testing.T) {
	rt := newRuntime(t)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt.AvailabilityPath = filepath.Join(blocker, "availability.json")

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
}

func TestBindingsOnProvider(t *testing.T) {
	bindings := []store.Binding{
		{
			// active + open round on anthropic: in.
			Name: "b-web", State: store.StateActive, RoundStartedAt: baseTime,
			BuilderCandidate: "agy/anthropic/sonnet",
		},
		{
			// active + open round, but a different provider: out.
			Name: "google-binding", State: store.StateActive, RoundStartedAt: baseTime,
			BuilderCandidate: "agy/google/gemini",
		},
		{
			// active but no round open: out.
			Name: "no-round", State: store.StateActive, RoundStartedAt: time.Time{},
			BuilderCandidate: "agy/anthropic/sonnet",
		},
		{
			// open round on anthropic, but not active: out.
			Name: "needs-you", State: store.StateNeedsYou, RoundStartedAt: baseTime,
			BuilderCandidate: "agy/anthropic/sonnet",
		},
		{
			// active + open round, but adopted (no candidate): out.
			Name: "adopted", State: store.StateActive, RoundStartedAt: baseTime,
			BuilderCandidate: "",
		},
		{
			// active + open round on anthropic, named so the sort is checked: in.
			Name: "a-api", State: store.StateActive, RoundStartedAt: baseTime,
			BuilderCandidate: "agy/anthropic/sonnet",
		},
	}

	got := BindingsOnProvider(bindings, "anthropic")
	want := []string{"a-api", "b-web"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("BindingsOnProvider() = %v, want %v", got, want)
	}
}
