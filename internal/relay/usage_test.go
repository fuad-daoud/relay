package relay

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// closeRound drives the ordinary close: report file, done marker, idle
// builder, one reconcile. Returns the report entry.
func closeRound(t *testing.T, rt Runtime, b store.Binding) store.LogEntry {
	t.Helper()
	if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath(b.Name, b.Round))
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
	next, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// reconcile (reconcile_test.go) mirrors Reconcile's own contract -- "does
	// NOT persist anything: ... the caller must tx.Save it" -- by returning
	// the updated binding without saving it, exactly as daemon.go's tick
	// does before its own tx.Save. Every other caller of this helper only
	// inspects the returned value in memory; this is the first to also
	// assert on the persisted store, so it does the daemon's save itself.
	if err := rt.Store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindReport && e.Round == b.Round {
			return e
		}
	}
	t.Fatal("no report entry")
	return store.LogEntry{}
}

func TestRoundCloseRecordsUsage(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	rt.Git = &fakeGit{snapshotTreeID: "t", diffResult: git.Diff{}}
	fu := &fakeUsage{samples: []usage.Sample{{Provider: "test", Model: "m", Tokens: usage.Tokens{In: 10, Out: 2}, USD: 0.5, HasCost: true}}}
	rt.Usage = fu
	// The round was sent at baseTime; close it 90 s later.
	rt.Now = func() time.Time { return baseTime.Add(90 * time.Second) }

	e := closeRound(t, rt, b)
	if e.Usage == nil {
		t.Fatal("report entry has no usage")
	}
	if e.Usage.Cost.Basis != usage.Measured || e.Usage.Cost.USD != 0.5 || e.Usage.Tokens != (usage.Tokens{In: 10, Out: 2}) {
		t.Errorf("usage = %+v", e.Usage)
	}
	if e.Usage.DurationMS != 90_000 {
		t.Errorf("DurationMS = %d, want 90000 (RoundStartedAt to close)", e.Usage.DurationMS)
	}
	if e.Usage.Harness != "agy" {
		t.Errorf("Harness = %q, want the builder's kind", e.Usage.Harness)
	}
	if len(fu.sources) != 1 {
		t.Fatalf("reader called %d times, want 1", len(fu.sources))
	}
	src := fu.sources[0]
	if src.Harness != "agy" || src.Mode != usage.ModeHeadless || src.Provider != "test" || src.Model != "m" {
		t.Errorf("source = %+v", src)
	}
	if !src.Start.Equal(baseTime) || !src.End.Equal(baseTime.Add(90*time.Second)) {
		t.Errorf("window = %v..%v", src.Start, src.End)
	}
}

func TestRoundCloseWithNoReaderIsUnknownAndStillCloses(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	rt.Usage = nil
	e := closeRound(t, rt, b)
	if e.Usage == nil || e.Usage.Cost.Basis != usage.Unknown || e.Usage.Note != "no reader" {
		t.Errorf("usage = %+v, want unknown/no reader", e.Usage)
	}
	got, _ := rt.Store.Load(b.Name)
	if got.Round != b.Round+1 || !got.RoundStartedAt.IsZero() {
		t.Errorf("round did not close normally: %+v", got)
	}
}

func TestRoundCloseReaderNoteIsUnknown(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	rt.Usage = &fakeUsage{note: "no stream"}
	e := closeRound(t, rt, b)
	if e.Usage.Cost.Basis != usage.Unknown || e.Usage.Note != "no stream" {
		t.Errorf("usage = %+v", e.Usage)
	}
}

func TestRoundCloseReaderTimeoutStillCloses(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	rt.Usage = &fakeUsage{block: true}
	done := make(chan store.LogEntry, 1)
	go func() { done <- closeRound(t, rt, b) }()
	select {
	case e := <-done:
		if e.Usage == nil || e.Usage.Cost.Basis != usage.Unknown {
			t.Errorf("usage = %+v", e.Usage)
		}
	case <-time.After(usageDeadline + 5*time.Second):
		t.Fatal("round close hung on the reader")
	}
}

func TestRoundSourceHeadlessAndPlan(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.Builder.Mode = store.ModeHeadless
	b.Worktree = "/wt"
	src := roundSource(rt, b, baseTime, baseTime.Add(time.Minute))
	if src.Mode != usage.ModeHeadless || src.StreamPath != rt.Store.BuilderStreamPath(b.Name, b.Round) {
		t.Errorf("headless source = %+v", src)
	}
	if src.Worktree != "/wt" {
		t.Errorf("Worktree = %q", src.Worktree)
	}
	if src.Plan {
		t.Error("test candidates carry no plan flag")
	}
	b.BuilderCandidate = "" // adopted builder
	src = roundSource(rt, b, baseTime, baseTime)
	if src.Provider != "" || src.Model != "" {
		t.Errorf("adopted builder must have no candidate provider/model: %+v", src)
	}
}

func TestRecordUsageFoldsWithRuntimePrices(t *testing.T) {
	rt := Runtime{Now: func() time.Time { return baseTime }}
	rt.Usage = &fakeUsage{samples: []usage.Sample{{Provider: "test", Model: "m", Tokens: usage.Tokens{In: 1_000_000}}}}
	rt.Prices = usage.Prices{Models: map[string]usage.ModelPrice{"test/m": {In: 2}}}
	u := recordUsage(context.Background(), rt, usage.Source{Harness: "claude", Mode: usage.ModeHeadless, Start: baseTime, End: baseTime.Add(time.Second)})
	if u.Cost.Basis != usage.Estimated || u.Cost.USD != 2 {
		t.Errorf("usage = %+v, want estimated $2", u)
	}
	if u.DurationMS != 1000 || u.Harness != "claude" {
		t.Errorf("duration/harness = %d/%q", u.DurationMS, u.Harness)
	}
}
