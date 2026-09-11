package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/herdr"
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

func TestBindRecordsASpawnFailure(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4", startErr: errors.New("agent start: exit 1")}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("expected Bind to fail when StartAgent fails")
	}
	if !strings.Contains(err.Error(), "agent start: exit 1") {
		t.Errorf("err = %q, want it to contain the start error", err.Error())
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	e := l.Entries[0]
	if e.Kind != ledger.SpawnFailed {
		t.Errorf("Kind = %v, want SpawnFailed", e.Kind)
	}
	if e.Subject != testAgyRef {
		t.Errorf("Subject = %q, want %q", e.Subject, testAgyRef)
	}
	if e.Binding != "webshop" {
		t.Errorf("Binding = %q, want webshop", e.Binding)
	}
	if e.Source != "relay" {
		t.Errorf("Source = %q, want relay", e.Source)
	}
	if !e.Until.Equal(baseTime.Add(SpawnFailedCooldown)) {
		t.Errorf("Until = %v, want %v", e.Until, baseTime.Add(SpawnFailedCooldown))
	}
	if !strings.Contains(e.Note, "agent start") {
		t.Errorf("Note = %q, want it to contain %q", e.Note, "agent start")
	}
}

func TestAddRecordsASpawnFailure(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9", startErr: errors.New("agent start: exit 1")}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, f, fg, nil)
	repo := addRepo(t)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, PlannerPane: "w2:p3", Repo: repo,
	})
	if err == nil {
		t.Fatal("expected Add to fail when StartAgent fails")
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	e := l.Entries[0]
	if e.Kind != ledger.SpawnFailed || e.Subject != testAgyRef {
		t.Errorf("entry = %+v, want SpawnFailed for %q", e, testAgyRef)
	}
	if e.Binding != "frontend" {
		t.Errorf("Binding = %q, want frontend", e.Binding)
	}
}

func TestForkRecordsASpawnFailure(t *testing.T) {
	ctx := context.Background()
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5", startErr: errors.New("agent start: exit 1")}
	fg := &fakeGit{headCommitID: "commit-123"}
	rt := newForkRuntime(t, f, fg, nil)
	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)

	_, err := Fork(ctx, rt, ForkOptions{
		Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3", Candidate: testClaudeRef,
	})
	if err == nil {
		t.Fatal("expected Fork to fail when StartAgent fails")
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	e := l.Entries[0]
	if e.Kind != ledger.SpawnFailed || e.Subject != testClaudeRef {
		t.Errorf("entry = %+v, want SpawnFailed for %q", e, testClaudeRef)
	}
	if e.Binding != "alt" {
		t.Errorf("Binding = %q, want alt", e.Binding)
	}
}

func TestAskRecordsASpawnFailure(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	f.startErr = errors.New("agent start: exit 1")
	q := writeQuestion(t, "x")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err == nil {
		t.Fatal("expected Ask to fail when StartAgent fails")
	}
	if res.Consult.State != store.ConsultSilent {
		t.Errorf("consult state = %q, want silent", res.Consult.State)
	}
	if !strings.HasPrefix(res.Consult.Note, "start failed:") {
		t.Errorf("Note = %q, want prefix %q", res.Consult.Note, "start failed:")
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	e := l.Entries[0]
	if e.Kind != ledger.SpawnFailed || e.Subject != testClaudeRef {
		t.Errorf("entry = %+v, want SpawnFailed for %q", e, testClaudeRef)
	}
	if e.Binding != "webshop" {
		t.Errorf("Binding = %q, want webshop", e.Binding)
	}
}

func TestSplitFailureIsNotASpawnFailure(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: ""}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("expected Bind to fail when the split fails")
	}

	l := loadLedger(t, rt)
	if len(l.Entries) != 0 {
		t.Fatalf("got %d ledger entries, want 0: %+v", len(l.Entries), l.Entries)
	}
}

func TestSpawnFailureDoesNotMaskTheError(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4", startErr: errors.New("agent start: exit 1")}
	rt := newRuntime(t, f)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt.LedgerPath = filepath.Join(blocker, "ledger.json")

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("expected Bind to fail")
	}
	if !strings.Contains(err.Error(), "agent start: exit 1") {
		t.Errorf("err = %q, want it to still contain the start error despite the ledger write failing", err.Error())
	}
}

func TestUnavailableRecordsTheProvider(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

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
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
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
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

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
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "first"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "second"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	provider, removed, err := Available(rt, "test")
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

	provider, removed, err = Available(rt, testClaudeRef)
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
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	recordSpawnFailure(rt, testClaudeRef, "webshop", errors.New("boom"))
	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	_, removed, err := Available(rt, "test")
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
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	if got := Gates(rt); got != nil {
		t.Errorf("Gates() = %+v, want nil", got)
	}
}

func TestGatesProjectsOntoCandidates(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

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

func TestGatesToleratesABadLedger(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	if err := os.WriteFile(rt.LedgerPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got := Gates(rt); got != nil {
		t.Errorf("Gates() = %+v, want nil", got)
	}
}

func TestGateUntilText(t *testing.T) {
	if got := GateUntilText(time.Time{}); got != "until cleared" {
		t.Errorf("GateUntilText(zero) = %q, want %q", got, "until cleared")
	}

	fixed := baseTime
	want := "until " + fixed.Local().Format("15:04")
	if got := GateUntilText(fixed); got != want {
		t.Errorf("GateUntilText(fixed) = %q, want %q", got, want)
	}
}

func TestGatedNoteEmptyWhenNotGated(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	if got := gatedNote(rt, testClaudeRef); got != "" {
		t.Errorf("gatedNote() = %q, want empty", got)
	}
}

func TestGatedNoteFormatsEveryGate(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

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
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

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
