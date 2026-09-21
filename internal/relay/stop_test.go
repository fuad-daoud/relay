package relay

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// stopEntries returns the binding's KindStop entries, oldest first.
func stopEntries(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindStop {
			out = append(out, e)
		}
	}
	return out
}

func TestStopDecisionTable(t *testing.T) {
	graceMS := int((5 * time.Minute) / time.Millisecond)
	open := store.Binding{Round: 1, RoundStartedAt: baseTime}
	early := open
	early.StopRequestedAt = baseTime.Add(-time.Minute)
	early.StopGraceMS = graceMS
	late := open
	late.StopRequestedAt = baseTime.Add(-6 * time.Minute)
	late.StopGraceMS = graceMS
	lateHeadless := late
	lateHeadless.Builder.Mode = store.ModeHeadless

	cases := []struct {
		name string
		b    store.Binding
		want stopAction
	}{
		{"no round", store.Binding{Round: 1}, stopNothing},
		{"open, not requested", open, stopRequest},
		{"requested 1m ago, grace 5m", early, stopWait},
		{"requested 6m ago, headless", lateHeadless, stopKill},
		{"requested 6m ago, pane", late, stopAbandon},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stopDecision(c.b, baseTime); got != c.want {
				t.Errorf("stopDecision = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStopPaneRequestsAndRecords pins the pane path: the wrap-up prompt goes
// through the same delivery path as a plan, and the request and its grace are
// recorded while the round stays open.
func TestStopPaneRequestsAndRecords(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)

	res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "requested" || res.Round != 1 {
		t.Fatalf("StopResult = %+v, want requested for round 1", res)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("prompts = %+v, want exactly the stop prompt", f.prompts)
	}
	text := f.prompts[0].Text
	for _, want := range []string{
		"stop requested by the planner",
		rt.Store.ReportPath("webshop", 1),
		rt.Store.DonePath("webshop", 1),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("stop prompt must contain %q, got %q", want, text)
		}
	}
	if f.prompts[0].Target != "w2:p4" {
		t.Errorf("prompt target = %q, want the builder pane", f.prompts[0].Target)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.StopRequestedAt.IsZero() {
		t.Error("StopRequestedAt must be set after a stop request")
	}
	if got.StopGraceMS != 300000 {
		t.Errorf("StopGraceMS = %d, want 300000", got.StopGraceMS)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active: the round is still open", got.State)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindStop {
		t.Errorf("last log kind = %s, want stop", last.Kind)
	}
	if last.Note != "stop requested (grace 5m0s)" {
		t.Errorf("last log note = %q, want \"stop requested (grace 5m0s)\"", last.Note)
	}
}

func TestStopPaneMarkerWithinGraceClosesGraceful(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if b.StopRequestedAt.IsZero() {
		t.Fatal("StopRequestedAt must be set for this test")
	}

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("I stopped where I was\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the marker closed the round", got.Round)
	}
	if !got.StopRequestedAt.IsZero() {
		t.Errorf("StopRequestedAt = %s, want zero once the round closed", got.StopRequestedAt)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindReport {
		t.Fatalf("pending kind = %s, want report", pending.Kind)
	}
	if !strings.Contains(pending.Note, "stopped") {
		t.Errorf("report note = %q, want it to say stopped", pending.Note)
	}

	stops := stopEntries(t, rt, "webshop")
	if len(stops) != 2 || stops[len(stops)-1].Note != "stopped/graceful" {
		t.Fatalf("stop entries = %+v, want the request then stopped/graceful", stops)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if entries[len(entries)-1].Kind != store.KindStop {
		t.Errorf("the stopped/graceful entry must follow the report, got last %s", entries[len(entries)-1].Kind)
	}
}

func TestStopPaneGraceElapsedAbandons(t *testing.T) {
	f := &fakeHerdr{}
	fg := &fakeGit{}
	rt, _ := sentBinding(t, f)
	rt.Git = fg
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)

	if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(6 * time.Minute)

	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %s, want needs_you once the grace elapsed", got.State)
	}
	if !strings.Contains(got.Halt, "did not stop within") {
		t.Errorf("Halt = %q, want it to say the builder did not stop within the grace", got.Halt)
	}

	stops := stopEntries(t, rt, "webshop")
	if len(stops) != 2 || stops[len(stops)-1].Note != "stopped/abandoned" {
		t.Errorf("stop entries = %+v, want the request then stopped/abandoned", stops)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("a stop must not remove the worktree: %+v", fg.removeWorktreeCalls)
	}
	if len(f.closed) != 0 {
		t.Errorf("a stop must not close the pane: %v", f.closed)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "did not stop within") {
		t.Errorf("notices = %+v, want one halt notice naming the grace", f.notices)
	}
}

func TestStopPaneNowAbandonsAtOnce(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)

	res, err := Stop(context.Background(), rt, "webshop", StopOptions{Now: true})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "abandoned" {
		t.Errorf("Action = %q, want abandoned", res.Action)
	}
	if len(f.prompts) != 0 {
		t.Errorf("--now must not prompt the builder: %+v", f.prompts)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %s, want needs_you immediately", got.State)
	}
	if !strings.Contains(got.Halt, "stopped now") {
		t.Errorf("Halt = %q, want it to say the stop was immediate", got.Halt)
	}
}

// TestStopHeadlessKillsAndClosesWithoutSwitch pins design question 1's option
// (a): a headless round has no stdin, so a stop kills now and closes the
// round without a report -- never through the exit-without-report path, which
// would switch the builder and charge the round.
func TestStopHeadlessKillsAndClosesWithoutSwitch(t *testing.T) {
	t.Run("no report", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := sentHeadless(t, f, fr)
		h := handleOf(b.Builder)

		res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if res.Action != "killed" {
			t.Errorf("Action = %q, want killed", res.Action)
		}

		if len(fr.kills) != 1 || fr.kills[0] != h {
			t.Fatalf("kills = %+v, want the round's process %+v", fr.kills, h)
		}
		if len(fr.specs) != 1 {
			t.Errorf("specs = %d, want 1: a stop must not start a replacement", len(fr.specs))
		}

		got, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		if got.Round != 2 {
			t.Errorf("Round = %d, want 2: the round closed", got.Round)
		}
		if got.RoundSwitches != 0 {
			t.Errorf("RoundSwitches = %d, want 0: a stop never charges a switch", got.RoundSwitches)
		}
		if got.RoundExcluded != nil {
			t.Errorf("RoundExcluded = %v, want it to exclude nobody", got.RoundExcluded)
		}
		if got.Builder.PID != 0 {
			t.Errorf("Builder.PID = %d, want 0 after the process was stopped", got.Builder.PID)
		}

		pending, found, err := rt.Store.PendingForPlanner("webshop")
		if err != nil || !found {
			t.Fatalf("report must be queued: found=%v err=%v", found, err)
		}
		if !strings.Contains(pending.Note, "noreport stopped") {
			t.Errorf("report note = %q, want noreport stopped", pending.Note)
		}
		stops := stopEntries(t, rt, "webshop")
		if len(stops) != 1 || stops[0].Note != "stopped/killed" {
			t.Errorf("stop entries = %+v, want one stopped/killed", stops)
		}
	})

	t.Run("with a report on disk", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, _ := sentHeadless(t, f, fr)
		reportPath := rt.Store.ReportPath("webshop", 1)
		if err := os.WriteFile(reportPath, []byte("I stopped where I was\n"), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}

		if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		pending, found, err := rt.Store.PendingForPlanner("webshop")
		if err != nil || !found {
			t.Fatalf("report must be queued: found=%v err=%v", found, err)
		}
		if !strings.Contains(pending.Note, "stopped") {
			t.Errorf("report note = %q, want it to say stopped", pending.Note)
		}
		if !strings.Contains(pending.Payload, reportPath) {
			t.Errorf("payload = %q, must name the report on disk", pending.Payload)
		}
	})
}

func TestStopNothingToStop(t *testing.T) {
	t.Run("no open round", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		b.RoundStartedAt = time.Time{}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); !errors.Is(err, ErrNothingToStop) {
			t.Fatalf("err = %v, want ErrNothingToStop", err)
		}
	})

	t.Run("done", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		b.State = store.StateDone
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err == nil || !strings.Contains(err.Error(), "done") {
			t.Fatalf("err = %v, want a refusal naming done", err)
		}
	})

	t.Run("paused", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		b.State = store.StatePaused
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err == nil || !strings.Contains(err.Error(), "paused") {
			t.Fatalf("err = %v, want a refusal naming paused", err)
		}
	})

	t.Run("remote", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		b.Builder.Mode = store.ModeRemote
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err == nil || !strings.Contains(err.Error(), "remote") {
			t.Fatalf("err = %v, want a refusal naming remote", err)
		}
	})
}

func TestSendClearsStopRequest(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.StopRequestedAt = baseTime
	b.StopGraceMS = 300000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "keep going"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if !got.StopRequestedAt.IsZero() || got.StopGraceMS != 0 {
		t.Errorf("a send must clear the stop bookkeeping: at=%s grace=%d", got.StopRequestedAt, got.StopGraceMS)
	}
}
