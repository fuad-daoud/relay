package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestDefaultWaitRound(t *testing.T) {
	t.Run("no entries falls back to b.Round", func(t *testing.T) {
		b := store.Binding{Round: 1}
		if got := DefaultWaitRound(b, nil); got != 1 {
			t.Errorf("DefaultWaitRound = %d, want 1", got)
		}
	})

	t.Run("an open round's plan entry is the default", func(t *testing.T) {
		b := store.Binding{Round: 3}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPlan},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3", got)
		}
	})

	t.Run("after a close, the newest planned round wins over b.Round", func(t *testing.T) {
		b := store.Binding{Round: 4}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPlan},
			{Round: 3, Direction: store.DirToPlanner, Kind: store.KindReport},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3", got)
		}
	})

	t.Run("a nudge is not a send", func(t *testing.T) {
		b := store.Binding{Round: 4}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPlan},
			{Round: 3, Direction: store.DirToPlanner, Kind: store.KindReport},
			{Round: 4, Direction: store.DirToBuilder, Kind: store.KindPlan, Note: nudgeNote},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3 (the round-4 entry is a nudge, not a send)", got)
		}
	})
}

func TestWaitOutcome(t *testing.T) {
	noQuestion := mapQuestion(nil)

	t.Run("a marked close is WaitClosed", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("unmarked is WaitUnmarked with the report path", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: "unmarked"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("scraped is WaitUnmarked with the report path", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: "scraped"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("noreport is WaitUnmarked with a dash", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: "noreport"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "-", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("DONE with no report is WaitGone", func(t *testing.T) {
		b := store.Binding{Round: 1, State: store.StateDone}
		got := WaitOutcome(b, nil, 1, noQuestion)
		want := WaitResult{Code: WaitGone, Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("needs_you with a halt is WaitNeedsYou", func(t *testing.T) {
		b := store.Binding{
			Round: 1, State: store.StateNeedsYou,
			Halt: "round 1 has run past 2h0m0s", HaltAt: time.Unix(1757000000, 0).UTC(),
		}
		got := WaitOutcome(b, nil, 1, noQuestion)
		if got.Code != WaitNeedsYou || got.Line != b.Halt || !got.Done {
			t.Errorf("WaitOutcome = %+v", got)
		}
	})

	t.Run("active with an open round and no report is not done", func(t *testing.T) {
		b := store.Binding{Round: 1, State: store.StateActive}
		got := WaitOutcome(b, nil, 1, noQuestion)
		if got.Done {
			t.Errorf("WaitOutcome = %+v, want Done == false", got)
		}
	})

	t.Run("DONE with a report for the asked round: the report wins", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateDone}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("broken after a clean close, asked for the closed round, still reports the close", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateBroken}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("the same broken binding asked for the next, unsent round needs a human", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateBroken}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 2, noQuestion)
		if got.Code != WaitNeedsYou || !got.Done {
			t.Errorf("WaitOutcome = %+v", got)
		}
	})
}

// manualSent seeds a binding one round into an open, active send, using only
// the store -- no herdr, no Bind/Send -- so a Wait test can build several
// independent bindings without wiring a shared fakeHerdr agent list.
func manualSent(t *testing.T, rt Runtime, name, cwd string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: cwd, Round: 1, State: store.StateActive,
		RoundStartedAt: rt.Now().UTC(),
		Planner:        store.Endpoint{PaneID: "w2:p3"},
		Builder:        store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
	if err := rt.Store.AppendLog(name, store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan,
		Path: rt.Store.PlanPath(name, 1), Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog %s: %v", name, err)
	}
	return b
}

func TestWaitReturnsAtOnceWhenAlreadyClosed(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	reportPath := rt.Store.ReportPath("webshop", 1)
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Path: reportPath, Payload: "done",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Interval is an hour: if the loop slept before checking, the 5s guard
	// fires first and the test fails with context deadline exceeded.
	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"webshop"}, Timeout: time.Minute, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitClosed || res.Line != reportPath || !res.Done {
		t.Errorf("Wait = (%q, %+v), want (webshop, {%d %q true})", name, res, WaitClosed, reportPath)
	}
}

func TestWaitAnyReturnsTheFirstThatCloses(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	manualSent(t, rt, "first", "/repo/first")
	manualSent(t, rt, "second", "/repo/second")

	reportPath := rt.Store.ReportPath("second", 1)
	if err := rt.Store.AppendLog("second", store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Path: reportPath, Payload: "done",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"first", "second"}, Timeout: time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "second" || res.Code != WaitClosed {
		t.Errorf("Wait = (%q, %+v), want second closed", name, res)
	}
}

func TestWaitGoneWhenUnboundMidWait(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)

	calls := 0
	rt.Now = func() time.Time {
		calls++
		if calls == 4 {
			_ = rt.Store.Delete("webshop")
		}
		return baseTime
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"webshop"}, Timeout: time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitGone || !res.Done {
		t.Errorf("Wait = (%q, %+v), want (webshop, {%d ... true})", name, res, WaitGone)
	}
}

func TestWaitTimesOut(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)

	tick := 0
	rt.Now = func() time.Time {
		now := baseTime.Add(time.Duration(tick) * time.Minute)
		tick++
		return now
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"webshop"}, Timeout: 5 * time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "" || res.Code != WaitTimeout || !res.Done {
		t.Errorf("Wait = (%q, %+v), want (\"\", {%d ... true})", name, res, WaitTimeout)
	}
}

func TestWaitNamesUnknownBindingIsAnError(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err := Wait(ctx, rt, WaitOptions{Names: []string{"nope"}, Timeout: time.Minute, Interval: time.Millisecond})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want it to wrap store.ErrNotFound", err)
	}
}

func TestWaitDefaultRoundIsTheNewestPlanned(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	reportPath := rt.Store.ReportPath("webshop", 1)
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Path: reportPath, Payload: "done",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	// Leave b.Round at the next, unsent round: no plan entry exists for it.
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Round: 0 must default to round 1 (the newest planned round), not
	// b.Round (2, which never closes) -- exit 0, not a 124 timeout.
	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"webshop"}, Round: 0, Timeout: time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitClosed || res.Line != reportPath || !res.Done {
		t.Errorf("Wait = (%q, %+v), want closed round 1", name, res)
	}
}
