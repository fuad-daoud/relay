package relay

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func builderAgent(status string) herdr.Agent {
	return herdr.Agent{
		Name: "webshop-builder",
		Kind: "agy", Status: status, CWD: "/repo", PaneID: "w2:p4",
		Title: "webshop-builder",
	}
}

// sentBinding puts a binding one Send into round 1, with a working builder.
func sentBinding(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := seedBound(t, f)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f.prompts = nil
	return rt, b
}

// sentBindingWithBuilderSession is sentBinding but the builder pane is
// spawned with a known session id recorded on the binding, which the
// broken-recovery tests need to exercise the session-id match.
func sentBindingWithBuilderSession(t *testing.T, f *fakeHerdr, sessionID string) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []herdr.Agent{
		plannerAgent(),
		{Kind: "agy", Status: herdr.StatusWorking, PaneID: "w2:p4", Session: herdr.Session{Value: sessionID}},
	}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f.prompts = nil
	return rt, b
}

// reconcile wraps Reconcile with the state lock a daemon would hold across
// the whole read-reconcile-write, since Reconcile itself takes a *store.Tx
// rather than locking on its own.
func reconcile(t *testing.T, rt Runtime, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = Reconcile(context.Background(), rt, tx, b, agents)
		return err
	})
	return out, err
}

func TestReconcileQueuesReportWhenBuilderIdleAndMarkerExists(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	b.RoundSwitches = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after a report", got.Round)
	}
	// The new round has not been sent, so it carries no deadline of its own
	// and no memory of an earlier round's halt notification.
	if !got.RoundStartedAt.IsZero() {
		t.Errorf("RoundStartedAt = %s, want zero until Send stamps the new round", got.RoundStartedAt)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d, want 0 on a fresh round", got.HaltNotifiedRound)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 on a fresh round", got.RoundSwitches)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("payload must name the report path, got %q", pending.Payload)
	}
}

func TestReconcileNudgesOnceWhenReportFileMissing(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("a nudge must not advance the round, got %d", got.Round)
	}
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0].Text, rt.Store.ReportPath("webshop", 1)) ||
		!strings.Contains(f.prompts[0].Text, rt.Store.DonePath("webshop", 1)) {
		t.Fatalf("expected one nudge naming the report and marker paths, got %+v", f.prompts)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("a nudge must not queue anything for the planner")
	}
}

func TestNudgeStallLateRecordsLate(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
	f.stalls = 1
	f.readOut = "builder output...\n" + nudgeFingerprint + "\n..."
	f.prompts = nil
	f.reads = nil

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("a nudge must not advance the round, got %d", got.Round)
	}
	if len(f.prompts) != 0 {
		t.Fatalf("expected 0 accepted retry prompts (no retry), got %+v", f.prompts)
	}
	if len(f.reads) < 1 {
		t.Fatalf("expected at least 1 read, got %d", len(f.reads))
	}
	if f.reads[0].Source != "visible" || f.reads[0].Lines != lateScanLines {
		t.Errorf("read[0] = %+v, want visible with %d lines", f.reads[0], lateScanLines)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	nudgeEntry := entries[len(entries)-1]
	if nudgeEntry.Note != nudgeNote {
		t.Errorf("nudgeEntry.Note = %q, want %q", nudgeEntry.Note, nudgeNote)
	}
	if !nudgeEntry.Late {
		t.Errorf("nudgeEntry.Late = false, want true")
	}

	nt, ok := nudgeTime(entries, 1)
	if !ok || nt.IsZero() {
		t.Errorf("nudgeTime(entries, 1) = (%v, %v), want valid time", nt, ok)
	}

	var realPlanSends int
	for _, e := range entries {
		if e.Round == 1 && e.Direction == store.DirToBuilder && e.Kind == store.KindPlan && e.Note != nudgeNote {
			realPlanSends++
		}
	}
	if realPlanSends != 1 {
		t.Errorf("real plan sends = %d, want 1", realPlanSends)
	}
	if !HasEntry(entries, 1, store.DirToBuilder, store.KindPlan) {
		t.Errorf("HasEntry should see the initial plan send")
	}
}

func TestReconcileScrapesAfterNudgeFails(t *testing.T) {
	// The builder's terminal output is assumed stable across ticks (here a single
	// unchanging string), so relay observes a still screen across nudgeGrace and
	// proceeds to the scrape fallback.
	const stableOutput = "I implemented the guard clause but could not write the file."
	f := &fakeHerdr{readOut: stableOutput}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}

	// The scrape is only reachable once the builder has had its grace period
	// to answer the nudge; see TestReconcileWaitsOutNudgeGraceBeforeScraping.
	clock.Advance(nudgeGrace + time.Second)

	got, err := reconcile(t, rt, b, agents) // scrape
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after the scrape fallback", got.Round)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("scrape must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "SCRAPED") {
		t.Errorf("a scraped report must be labelled unreliable, got %q", pending.Payload)
	}

	body, err := os.ReadFile(rt.Store.ReportPath("webshop", 1))
	if err != nil {
		t.Fatalf("scrape must be written to the report path: %v", err)
	}
	if !strings.Contains(string(body), "guard clause") {
		t.Errorf("scrape body = %q", body)
	}

	// A report is prose the builder printed, so it comes from the scrollback.
	// The blocked-dialog path is the one that needs --source detection.
	// Fingerprinting and scraping both read recent-unwrapped.
	for _, r := range f.reads {
		if r.Source != "recent-unwrapped" {
			t.Errorf("scrape reads = %+v, want recent-unwrapped reads", f.reads)
			break
		}
	}
}

// TestReconcileWaitsOutNudgeGraceBeforeScraping is the regression test for a
// scrape that raced the builder: relay nudged on one tick and scraped on the
// next, two seconds later, then advanced the round -- so the builder's real
// report was written to an abandoned round's path and never relayed.
func TestReconcileWaitsOutNudgeGraceBeforeScraping(t *testing.T) {
	// The builder's terminal output is assumed stable across ticks (an idle
	// builder that stopped emitting), so any delay before scraping is due to the
	// clock waiting out nudgeGrace, not due to screen changes resetting it.
	const stableOutput = "half a screen of output"
	f := &fakeHerdr{readOut: stableOutput}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("nudge Reconcile: %v", err)
	}

	// The very next poll, before the builder could plausibly have answered.
	clock.Advance(2 * time.Second)
	b, err = reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("in-grace Reconcile: %v", err)
	}
	if b.Round != 1 {
		t.Errorf("round = %d, want 1: the round must not be abandoned inside the grace", b.Round)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing may be queued inside the nudge grace")
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); !os.IsNotExist(err) {
		t.Error("the terminal must not be scraped inside the grace")
	}

	// Still nothing one second short of the grace.
	clock.Advance(nudgeGrace - 3*time.Second)
	b, err = reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("edge Reconcile: %v", err)
	}
	if b.Round != 1 {
		t.Errorf("round = %d, want 1 one second short of the grace", b.Round)
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); !os.IsNotExist(err) {
		t.Error("the terminal must not be scraped one second short of grace")
	}

	clock.Advance(2 * time.Second)
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("post-grace Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2: past the grace the scrape is the fallback", got.Round)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); !pending {
		t.Error("the scraped report must be queued once the grace has elapsed")
	}
}

func TestReconcileQuiescenceTerminalChangedResetsGrace(t *testing.T) {
	// Regression test for #11: an agent waiting on subagents emits output,
	// moving the terminal. When the terminal changes across the grace period,
	// relay must not scrape or advance the round, and must reset the grace clock.
	f := &fakeHerdr{readOut: "terminal at nudge: waiting on subagent"}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("nudge Reconcile: %v", err)
	}
	screenAtNudge := b.BuilderScreenAt
	if screenAtNudge.IsZero() {
		t.Fatal("expected BuilderScreenAt to be recorded at nudge")
	}

	// Advance past the grace period.
	clock.Advance(nudgeGrace + time.Second)

	// Terminal changed: subagent finished or emitted output.
	f.readOut = "terminal changed: subagent completed task"

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("reconcile after terminal change: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("round = %d, want 1: terminal changed, round must not be abandoned", got.Round)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing may be queued for the planner when terminal changed")
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); !os.IsNotExist(err) {
		t.Error("report must not be scraped when terminal changed")
	}
	if !got.BuilderScreenAt.After(screenAtNudge) {
		t.Errorf("BuilderScreenAt = %v, want after %v", got.BuilderScreenAt, screenAtNudge)
	}
}

func TestReconcileQuiescenceTerminalUnchangedScrapes(t *testing.T) {
	// Terminal unchanged for the full grace period establishes that the builder
	// has genuinely stopped, so relay scrapes and advances the round.
	const output = "I crashed without writing a report"
	f := &fakeHerdr{readOut: output}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("nudge Reconcile: %v", err)
	}

	clock.Advance(nudgeGrace + time.Second)

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("scrape Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2: unchanged terminal must scrape and advance", got.Round)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("scrape must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "SCRAPED") {
		t.Errorf("scraped report must be labelled unreliable, got %q", pending.Payload)
	}
	body, err := os.ReadFile(rt.Store.ReportPath("webshop", 1))
	if err != nil {
		t.Fatalf("report file must exist: %v", err)
	}
	if !strings.Contains(string(body), output) {
		t.Errorf("report body = %q, want containing %q", string(body), output)
	}
}

func TestReconcileQuiescenceResetThenUnchangedScrapes(t *testing.T) {
	// Proves the reset is a reset and not a permanent reprieve:
	// a changed screen resets grace once, but if the screen stops moving for
	// another full grace, relay scrapes.
	f := &fakeHerdr{readOut: "terminal at nudge"}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("nudge Reconcile: %v", err)
	}

	// 1st grace elapses, terminal changes -> grace resets
	clock.Advance(nudgeGrace + time.Second)
	f.readOut = "terminal moved: builder active"

	b, err = reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("reconcile after move: %v", err)
	}
	if b.Round != 1 {
		t.Fatalf("round = %d, want 1 after reset", b.Round)
	}

	// Advance another full grace with terminal now unchanged.
	clock.Advance(nudgeGrace + time.Second)

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("reconcile second grace: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2: still terminal after reset must scrape", got.Round)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); !pending {
		t.Error("the scraped report must be queued once second grace elapses")
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); err != nil {
		t.Errorf("report file must exist after scrape: %v", err)
	}
}

func TestReconcileQuiescenceReadErrorLeavesRoundOpen(t *testing.T) {
	// A herdr terminal read failure at check time is not evidence the builder stopped;
	// relay must leave the round open and not advance.
	f := &fakeHerdr{readOut: "terminal at nudge"}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("nudge Reconcile: %v", err)
	}

	clock.Advance(nudgeGrace + time.Second)
	f.readErr = errors.New("simulated herdr read error")

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("reconcile should swallow terminal read error: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("round = %d, want 1: terminal read error must not advance round", got.Round)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing must be queued when terminal read fails")
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); !os.IsNotExist(err) {
		t.Error("report must not be scraped when terminal read fails")
	}
}

func TestReconcileRefusesNudgeInsideStartGrace(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	clock.Advance(5 * time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("round = %d, want 1", got.Round)
	}
	if len(f.prompts) != 0 {
		t.Fatalf("expected zero prompts inside start grace, got %+v", f.prompts)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if _, nudged := nudgeTime(entries, 1); nudged {
		t.Error("a nudge log entry must not be appended inside start grace")
	}
}

func TestReconcileNudgesAfterStartGrace(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	clock.Advance(31 * time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("a nudge must not advance the round, got %d", got.Round)
	}
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0].Text, rt.Store.ReportPath("webshop", 1)) {
		t.Fatalf("expected one nudge naming the report path, got %+v", f.prompts)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("a nudge must not queue anything for the planner")
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if _, nudged := nudgeTime(entries, 1); !nudged {
		t.Error("expected a nudge log entry after start grace elapsed")
	}
}

func TestReconcileRefusesNudgeWhenRoundStartedAtZero(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	clock.Advance(31 * time.Second)
	b.RoundStartedAt = time.Time{}
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("round = %d, want 1", got.Round)
	}
	if len(f.prompts) != 0 {
		t.Fatalf("expected zero prompts when RoundStartedAt is zero, got %+v", f.prompts)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if _, nudged := nudgeTime(entries, 1); nudged {
		t.Error("a nudge log entry must not be appended when RoundStartedAt is zero")
	}
}

func TestReconcileQueuesReportInsideStartGrace(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	clock.Advance(5 * time.Second)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after a report", got.Round)
	}
	if len(f.prompts) != 0 {
		t.Fatalf("expected zero prompts when the marker is present, got %+v", f.prompts)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("payload must name the report path, got %q", pending.Payload)
	}
}

func TestReconcileMarksBrokenWhenBuilderGone(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerWith(herdr.StatusIdle, false)})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want broken", got.State)
	}
}

func TestReconcileIgnoresWorkingBuilder(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 || len(f.prompts) != 0 {
		t.Errorf("a working builder must be left alone: round=%d prompts=%+v", got.Round, f.prompts)
	}
}

func TestReconcileNeverTreatsUnknownAsDone(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusUnknown)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Error("herdr documents that unknown does not prove completion; the round must not advance")
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing may be queued off an unknown status")
	}
}

func TestReconcileRecoversFromBrokenOnSessionMatch(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBindingWithBuilderSession(t, f, "builder-sess")
	b.State = store.StateBroken

	// The builder is genuinely working, not idle: this isolates the
	// session-match recovery from queueReport, which would set Active on its
	// own and mask a missing (or wrong) recovery check.
	agents := []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		{Kind: "agy", Status: herdr.StatusWorking, PaneID: "w2:p4", Session: herdr.Session{Value: "builder-sess"}},
	}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active: a matching session id must clear broken", got.State)
	}
}

func TestReconcileRecoveredBindingProceedsNormally(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBindingWithBuilderSession(t, f, "builder-sess")
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	b.State = store.StateBroken

	agents := []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		{Kind: "agy", Status: herdr.StatusIdle, PaneID: "w2:p4", Session: herdr.Session{Value: "builder-sess"}},
	}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want a recovered binding to queue the ready report normally", got.Round)
	}
}

func TestReconcileStaysBrokenOnPaneOnlyMatch(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBindingWithBuilderSession(t, f, "builder-sess")
	b.State = store.StateBroken

	// A different agent now occupies the same pane: relaying into it would
	// hand plans to a stranger.
	agents := []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		{Kind: "agy", Status: herdr.StatusIdle, PaneID: "w2:p4", Session: herdr.Session{Value: "impostor-sess"}},
	}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want still broken: pane match alone must not clear it", got.State)
	}
	if len(f.prompts) != 0 {
		t.Error("nothing may be relayed into a pane that might hold a different agent")
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing may be queued while still broken")
	}
}

// TestReconcileUnbreaksWhenPaneAndKindMatchWithoutSession covers the case where an
// agent is inspected before any session has been reported or recorded, so there is
// nothing yet to backfill and pane plus kind is the whole identity. Unlike
// TestReconcileUnbreaksSessionlessBuilderAndBackfillsSession (which exercises the
// path where herdr already reports a session to backfill), this exercises recovery
// during the pre-session window (e.g. freshly spawned idle agy builder).
func TestReconcileUnbreaksWhenPaneAndKindMatchWithoutSession(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f) // seedBound's agents carry no builder pane entry, so no session was ever recorded
	if b.Builder.SessionID != "" {
		t.Fatalf("test setup: want no recorded session, got %q", b.Builder.SessionID)
	}
	b.State = store.StateBroken

	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State == store.StateBroken {
		t.Errorf("state = %s, want un-broken: pane and kind match", got.State)
	}
}

func TestReconcileDiffCapture(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 2, Insertions: 10, Deletions: 3},
			Patch: []byte("diff content"),
		},
	}
	rt.Git = fg
	b.RoundBaselineTree = "tree-start"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	reportFile := rt.Store.ReportPath("webshop", 1)
	if err := os.WriteFile(reportFile, []byte("report content"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	// First tick
	bAfter, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile 1: %v", err)
	}
	if bAfter.Round != 2 {
		t.Fatalf("expected round 2, got %d", bAfter.Round)
	}
	if bAfter.RoundBaselineTree != "" {
		t.Errorf("RoundBaselineTree not cleared, got %q", bAfter.RoundBaselineTree)
	}

	log, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	// Verify log order: KindDiff before KindReport
	var diffIdx, reportIdx int = -1, -1
	diffCount := 0
	for i, entry := range log {
		if entry.Round == 1 && entry.Kind == store.KindDiff {
			diffIdx = i
			diffCount++
			if !entry.Confirmed {
				t.Error("KindDiff entry must be confirmed")
			}
			if entry.Direction != store.DirToPlanner {
				t.Errorf("KindDiff direction = %s, want to_planner", entry.Direction)
			}
			if entry.Path != rt.Store.DiffPath("webshop", 1) {
				t.Errorf("KindDiff path = %s", entry.Path)
			}
		}
		if entry.Round == 1 && entry.Kind == store.KindReport {
			reportIdx = i
		}
	}
	if diffCount != 1 {
		t.Fatalf("expected exactly 1 KindDiff entry, got %d", diffCount)
	}
	if diffIdx == -1 || reportIdx == -1 || diffIdx >= reportIdx {
		t.Fatalf("KindDiff (%d) must be ordered before KindReport (%d)", diffIdx, reportIdx)
	}

	// Verify report payload carries diff line
	reportEntry := log[reportIdx]
	wantLine := "Diff: " + rt.Store.DiffPath("webshop", 1) + " (2 files, +10 -3)"
	if !strings.Contains(reportEntry.Payload, wantLine) {
		t.Fatalf("report payload %q does not contain %q", reportEntry.Payload, wantLine)
	}

	// PendingForPlanner still returns the report
	pending, ok, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !ok {
		t.Fatalf("PendingForPlanner: ok=%v, err=%v", ok, err)
	}
	if pending.Kind != store.KindReport {
		t.Fatalf("pending kind = %s, want report", pending.Kind)
	}

	// Second tick: should not append duplicate diff entry
	_, err = reconcile(t, rt, bAfter, agents)
	if err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}
	log2, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	diffCount2 := 0
	for _, entry := range log2 {
		if entry.Round == 1 && entry.Kind == store.KindDiff {
			diffCount2++
		}
	}
	if diffCount2 != 1 {
		t.Fatalf("expected still 1 KindDiff entry after tick 2, got %d", diffCount2)
	}
}

func TestQueueReportRecordsCommitFacts(t *testing.T) {
	closeRound := func(t *testing.T, fg *fakeGit, head string) (store.Binding, []store.LogEntry) {
		t.Helper()
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		rt.Git = fg
		b.RoundBaselineTree = "tree-start"
		b.RoundBaselineHead = head
		b.Branch = "relay/webshop"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
		got, err := reconcile(t, rt, b, agents)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		return got, entries
	}
	diffAndReport := func(t *testing.T, entries []store.LogEntry) (store.LogEntry, store.LogEntry) {
		t.Helper()
		var diff, report store.LogEntry
		for _, e := range entries {
			if e.Round != 1 {
				continue
			}
			switch e.Kind {
			case store.KindDiff:
				diff = e
			case store.KindReport:
				report = e
			}
		}
		if diff.Kind == "" || report.Kind == "" {
			t.Fatalf("missing diff or report entry in %+v", entries)
		}
		return diff, report
	}
	changed := git.Diff{Stat: git.Stat{FilesChanged: 2, Insertions: 10, Deletions: 3}, Patch: []byte("diff content")}

	t.Run("commits and clean", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-end", revListCount: 3}
		got, entries := closeRound(t, fg, "head-start")
		diff, report := diffAndReport(t, entries)
		if diff.Commits != 3 || diff.Tree != "clean" {
			t.Errorf("diff entry facts = (%d, %q), want (3, clean)", diff.Commits, diff.Tree)
		}
		if diff.Note != "2 files, +10 -3; 3 commits, clean" {
			t.Errorf("diff note = %q", diff.Note)
		}
		if !strings.Contains(report.Payload, " -- 3 commits on relay/webshop, tree clean") {
			t.Errorf("payload %q lacks the commit clause", report.Payload)
		}
		if got.RoundBaselineHead != "" || got.RoundBaselineTree != "" {
			t.Errorf("baseline not cleared: head=%q tree=%q", got.RoundBaselineHead, got.RoundBaselineTree)
		}
		if fg.lastRevListFrom != "head-start" || fg.lastRevListTo != "head-end" {
			t.Errorf("rev-list range %q..%q", fg.lastRevListFrom, fg.lastRevListTo)
		}
	})

	t.Run("none and dirty", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-start", dirtyResult: true}
		_, entries := closeRound(t, fg, "head-start")
		diff, report := diffAndReport(t, entries)
		if diff.Commits != 0 || diff.Tree != "dirty" {
			t.Errorf("diff entry facts = (%d, %q), want (0, dirty)", diff.Commits, diff.Tree)
		}
		if !strings.Contains(report.Payload, " -- no commits; changes are uncommitted in the worktree") {
			t.Errorf("payload %q lacks the dirty clause", report.Payload)
		}
	})

	t.Run("no baseline head", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-end", revListCount: 3}
		_, entries := closeRound(t, fg, "")
		diff, report := diffAndReport(t, entries)
		if diff.Tree != "" || diff.Commits != 0 {
			t.Errorf("diff entry facts = (%d, %q), want unknown", diff.Commits, diff.Tree)
		}
		if !strings.Contains(diff.Note, "; commits unknown (no baseline)") {
			t.Errorf("diff note = %q", diff.Note)
		}
		if !strings.Contains(report.Payload, " -- commits unknown (no baseline)") {
			t.Errorf("payload %q", report.Payload)
		}
		if fg.revListCalls != 0 {
			t.Errorf("rev-list called without a baseline head")
		}
	})
}

func TestReconcileUnbreaksSessionlessBuilderAndBackfillsSession(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateBroken

	live := builderAgent(herdr.StatusWorking)
	live.Session = herdr.Session{Value: "late-session"}

	out, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent(), live})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if out.State == store.StateBroken {
		t.Fatal("a located builder must un-break its binding")
	}
	if out.Builder.SessionID != "late-session" {
		t.Fatalf("SessionID = %q, want it backfilled to late-session", out.Builder.SessionID)
	}
}

func TestReconcileLeavesBrokenWhenPaneHoldsADifferentKind(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateBroken

	stranger := builderAgent(herdr.StatusWorking)
	stranger.Name = ""       // a stranger does not carry the name relay gave its builder
	stranger.Kind = "claude" // same pane, different agent

	out, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent(), stranger})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if out.State != store.StateBroken {
		t.Fatalf("state = %q, want broken", out.State)
	}
	if out.Builder.SessionID != "" {
		t.Fatal("a rejected agent must not write identity onto the endpoint")
	}
}

func TestReconcileBreaksWhenRecordedSessionIsGoneAndPaneReused(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBindingWithBuilderSession(t, f, "mine")

	stranger := builderAgent(herdr.StatusWorking)
	stranger.Name = "" // a stranger does not carry the name relay gave its builder
	stranger.Session = herdr.Session{Value: "stranger"}

	out, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent(), stranger})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if out.State != store.StateBroken {
		t.Fatalf("state = %q, want broken: the recorded session is gone", out.State)
	}
	if len(f.prompts) != 0 {
		t.Fatal("relay must not prompt an agent that is not this binding's builder")
	}
}

func TestReconcileRefreshesPaneIDAfterAPaneMove(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBindingWithBuilderSession(t, f, "mine")

	moved := builderAgent(herdr.StatusWorking)
	moved.PaneID = "w9:p1"
	moved.Session = herdr.Session{Value: "mine"}

	out, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent(), moved})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if out.Builder.PaneID != "w9:p1" {
		t.Fatalf("PaneID = %q, want it refreshed to w9:p1", out.Builder.PaneID)
	}
	if out.Builder.SessionID != "mine" {
		t.Fatal("a recorded session must never be overwritten by a refresh")
	}
}

func TestReconcileDoesNotRefreshWhenBuilderIsUnlocatable(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	before := b.Builder

	out, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if out.State != store.StateBroken {
		t.Fatalf("state = %q, want broken", out.State)
	}
	if out.Builder != before {
		t.Fatalf("endpoint = %+v, want it untouched at %+v", out.Builder, before)
	}
}

func TestReconcileRefreshesPlannerEndpoint(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)

	moved := plannerAgent()
	moved.PaneID = "w9:p2" // same session, new pane

	out, err := reconcile(t, rt, b, []herdr.Agent{moved, builderAgent(herdr.StatusWorking)})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if out.Planner.PaneID != "w9:p2" {
		t.Fatalf("planner pane = %q, want w9:p2", out.Planner.PaneID)
	}
}

func TestReconcileAddressesThePaneNotTheForgottenAgentName(t *testing.T) {
	f := &fakeHerdr{readOut: "1. yes\n2. no"}
	rt, b := sentBindingWithBuilderSession(t, f, "mine")

	// herdr forgot the spawned agent's name across a restart, and the pane
	// moved. Only the live pane id is addressable.
	moved := builderAgent(herdr.StatusBlocked)
	moved.PaneID = "w9:p1"
	moved.Session = herdr.Session{Value: "mine"}

	if _, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent(), moved}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.reads) == 0 {
		t.Fatal("expected the blocking dialog to be read")
	}
	for _, r := range f.reads {
		if r.Target != "w9:p1" {
			t.Fatalf("read target = %q, want the located pane w9:p1", r.Target)
		}
	}
}

// TestBindRacesSessionLookupAndReconcileRecovers reproduces #20 end to end: the
// builder is spawned, the post-spawn session lookup loses its race with the
// agent's own registration, herdr flickers and the binding is flagged BROKEN --
// and relay recovers it by itself rather than stranding a working builder.
func TestBindRacesSessionLookupAndReconcileRecovers(t *testing.T) {
	f := &fakeHerdr{
		agents:  []herdr.Agent{plannerAgent()},
		newPane: "w2:p4",
		listErr: errors.New("herdr restarting"), // every call after Bind's own
	}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Builder.SessionID != "" {
		t.Fatal("precondition: this test needs the post-spawn lookup to have lost its race")
	}

	// The flicker passes; the builder was alive throughout and now registers.
	f.listErr = nil
	live := builderAgent(herdr.StatusIdle)
	live.Session = herdr.Session{Value: "registered-late"}
	f.agents = []herdr.Agent{plannerAgent(), live}

	b.State = store.StateBroken // what the flicker left behind

	out, err := reconcile(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if out.State == store.StateBroken {
		t.Fatal("#20: a live builder's binding must not stay broken")
	}
	if out.Builder.SessionID != "registered-late" {
		t.Fatalf("SessionID = %q, want the session backfilled once the agent registered",
			out.Builder.SessionID)
	}
}

func TestQueueReport_RoundClosedTree(t *testing.T) {
	t.Run("ordinary close", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		fg := &fakeGit{
			snapshotTreeID: "tree-end-ordinary",
			diffResult: git.Diff{
				Stat:  git.Stat{FilesChanged: 1, Insertions: 5, Deletions: 2},
				Patch: []byte("diff content"),
			},
		}
		rt.Git = fg
		b.RoundBaselineTree = "tree-start"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		reportFile := rt.Store.ReportPath(b.Name, b.Round)
		if err := os.WriteFile(reportFile, []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath(b.Name, b.Round))

		agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
		got, err := reconcile(t, rt, b, agents)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got.Round != 2 {
			t.Fatalf("round = %d, want 2", got.Round)
		}
		if got.RoundClosedTree != fg.snapshotTreeID {
			t.Fatalf("RoundClosedTree = %q, want %q", got.RoundClosedTree, fg.snapshotTreeID)
		}
	})

	t.Run("retry path", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		fg := &fakeGit{
			snapshotTreeID: "tree-snapshot-should-not-be-used",
		}
		rt.Git = fg
		b.RoundBaselineTree = "tree-start"
		b.RoundClosedTree = "stale-tree-from-round-3"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		// A KindDiff entry already exists for the round
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			return tx.AppendLog(b.Name, store.LogEntry{
				TS:        rt.Now().UTC(),
				Round:     b.Round,
				Direction: store.DirToPlanner,
				Kind:      store.KindDiff,
				Confirmed: true,
			})
		})
		if err != nil {
			t.Fatal(err)
		}

		reportFile := rt.Store.ReportPath(b.Name, b.Round)
		if err := os.WriteFile(reportFile, []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath(b.Name, b.Round))

		agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
		got, err := reconcile(t, rt, b, agents)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got.Round != 2 {
			t.Fatalf("round = %d, want 2", got.Round)
		}
		if got.RoundClosedTree != "" {
			t.Fatalf("RoundClosedTree = %q, want empty", got.RoundClosedTree)
		}
	})

	t.Run("non-git tree", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		rt.Git = nil
		b.RoundBaselineTree = "tree-start"
		b.RoundClosedTree = "stale-tree-from-round-3"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		reportFile := rt.Store.ReportPath(b.Name, b.Round)
		if err := os.WriteFile(reportFile, []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath(b.Name, b.Round))

		agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
		got, err := reconcile(t, rt, b, agents)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got.Round != 2 {
			t.Fatalf("round = %d, want 2 (the round still advances normally)", got.Round)
		}
		if got.RoundClosedTree != "" {
			t.Fatalf("RoundClosedTree = %q, want empty", got.RoundClosedTree)
		}
	})
}

func TestEffectiveStatus(t *testing.T) {
	cases := []struct {
		name      string
		epSession string
		aSession  string
		status    string
		want      string
	}{
		{name: "same session passes idle", epSession: "s1", aSession: "s1", status: herdr.StatusIdle, want: herdr.StatusIdle},
		{name: "same session passes done", epSession: "s1", aSession: "s1", status: herdr.StatusDone, want: herdr.StatusDone},
		{name: "same session passes working", epSession: "s1", aSession: "s1", status: herdr.StatusWorking, want: herdr.StatusWorking},
		{name: "same session passes blocked", epSession: "s1", aSession: "s1", status: herdr.StatusBlocked, want: herdr.StatusBlocked},
		{name: "differing session maps idle to working", epSession: "s1", aSession: "s2", status: herdr.StatusIdle, want: herdr.StatusWorking},
		{name: "differing session maps done to working", epSession: "s1", aSession: "s2", status: herdr.StatusDone, want: herdr.StatusWorking},
		{name: "differing session maps working to working", epSession: "s1", aSession: "s2", status: herdr.StatusWorking, want: herdr.StatusWorking},
		{name: "differing session maps blocked to blocked", epSession: "s1", aSession: "s2", status: herdr.StatusBlocked, want: herdr.StatusBlocked},
		{name: "empty recorded session passes idle", epSession: "", aSession: "s2", status: herdr.StatusIdle, want: herdr.StatusIdle},
		{name: "empty recorded session passes done", epSession: "", aSession: "s2", status: herdr.StatusDone, want: herdr.StatusDone},
		{name: "empty live session passes idle", epSession: "s1", aSession: "", status: herdr.StatusIdle, want: herdr.StatusIdle},
		{name: "empty live session passes done", epSession: "s1", aSession: "", status: herdr.StatusDone, want: herdr.StatusDone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := store.Endpoint{SessionID: tc.epSession}
			a := herdr.Agent{Status: tc.status, Session: herdr.Session{Value: tc.aSession}}
			if got := effectiveStatus(ep, a); got != tc.want {
				t.Errorf("effectiveStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

// closeOnMarkerUnderLock calls closeOnMarker the way Reconcile does: inside
// the store lock, with the binding's current log.
func closeOnMarkerUnderLock(t *testing.T, rt Runtime, b store.Binding) (store.Binding, bool) {
	t.Helper()
	var out store.Binding
	var closed bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		out, closed, err = closeOnMarker(context.Background(), rt, tx, b, entries)
		return err
	})
	if err != nil {
		t.Fatalf("closeOnMarker: %v", err)
	}
	return out, closed
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}

func TestCloseOnMarkerWithReportClosesNormally(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	b.Halt = "stale"
	b.HaltAt = rt.Now()

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want empty after a round close", got.Halt)
	}
	if !got.HaltAt.IsZero() {
		t.Errorf("HaltAt = %v, want zero after a round close", got.HaltAt)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "" {
		t.Errorf("note = %q, want empty on a marked close", pending.Note)
	}
	if !strings.Contains(pending.Payload, "Builder finished round 1. Report: "+rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("payload = %q", pending.Payload)
	}
	if len(f.reads) != 0 {
		t.Errorf("a marked close never reads the terminal: %+v", f.reads)
	}
}

// TestCloseOnMarkerWithoutReportIsNoreport pins §4.1: the builder said it was
// done, so relay closes on that and says the report is missing, instead of
// waiting for idle and scraping a worse artefact. Mutation: fall through to
// scrapeReport -> f.reads is non-empty and the note is "scraped".
func TestCloseOnMarkerWithoutReportIsNoreport(t *testing.T) {
	f := &fakeHerdr{readOut: "some terminal text"}
	rt, b := sentBinding(t, f)
	touch(t, rt.Store.DonePath("webshop", 1))

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("entry must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "noreport" {
		t.Errorf("note = %q, want noreport", pending.Note)
	}
	want := "Builder wrote its completion marker for round 1 but no report at " + rt.Store.ReportPath("webshop", 1) + "."
	if !strings.Contains(pending.Payload, want) {
		t.Errorf("payload = %q, want it to contain %q", pending.Payload, want)
	}
	if len(f.reads) != 0 {
		t.Errorf("no scrape on a marker-only close: %+v", f.reads)
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); err == nil {
		t.Error("relay must not write a report file of its own on a noreport close")
	}
}

func TestCloseOnMarkerAbsentDoesNothing(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if closed || got.Round != 1 {
		t.Fatalf("closed=%v round=%d, want untouched: a report alone is not a close", closed, got.Round)
	}
	after, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("log grew from %d to %d entries; no marker means no I/O", len(before), len(after))
	}
}

// TestReconcileClosesOnMarkerWhileBuilderStillWorking pins §4.2: the marker
// is checked every tick, before herdr's status is consulted. Mutation: move
// the closeOnMarker call inside the idle branch -> this fails because the
// builder is reported working.
func TestReconcileClosesOnMarkerWhileBuilderStillWorking(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2: the marker closes the round regardless of status", got.Round)
	}
	if len(f.prompts) != 0 {
		t.Errorf("no nudge on a marked round: %+v", f.prompts)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found || pending.Note != "" {
		t.Errorf("want a normal report queued: found=%v note=%q err=%v", found, pending.Note, err)
	}
}

// TestReconcileReportWithoutMarkerIsNotAClose pins §4.3: a report on disk is
// not evidence the builder is finished. Mutation: gate on the report instead
// of the marker -> the round advances on the first tick.
func TestReconcileReportWithoutMarkerIsNotAClose(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("half-written"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("round = %d, want 1: a report without a marker must not close the round", got.Round)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing may be queued before the marker or the fallback")
	}
	// Idle past the start grace with no marker: the one nudge, naming both files.
	if len(f.prompts) != 1 {
		t.Fatalf("want exactly one nudge, got %+v", f.prompts)
	}
	if !strings.Contains(f.prompts[0].Text, rt.Store.ReportPath("webshop", 1)) ||
		!strings.Contains(f.prompts[0].Text, rt.Store.DonePath("webshop", 1)) {
		t.Errorf("nudge must name the report and the marker, got %q", f.prompts[0].Text)
	}
}

// TestReconcileQuiescentWithReportClosesUnmarked pins the fallback: after the
// nudge and a still screen, the report that exists is delivered with the
// omission named, never scraped over. Mutation: drop the "unmarked" note ->
// fails; scrape instead of queueing the report -> the body check fails.
func TestReconcileQuiescentWithReportClosesUnmarked(t *testing.T) {
	f := &fakeHerdr{readOut: "still screen"}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("the builder's own words"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	clock.Advance(nudgeGrace + time.Second)
	got, err := reconcile(t, rt, b, agents) // quiescent
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2 after the unmarked close", got.Round)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
	if !strings.Contains(pending.Payload, "never confirmed completion (no 001-done)") ||
		!strings.Contains(pending.Payload, "The diff may be premature.") ||
		!strings.Contains(pending.Payload, rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("payload = %q", pending.Payload)
	}
	body, err := os.ReadFile(rt.Store.ReportPath("webshop", 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "the builder's own words" {
		t.Errorf("the builder's report must be delivered as written, got %q", body)
	}
}

// TestReconcileQuiescentWithoutReportStillScrapes is the regression pin for
// the scrape path: no report and no marker after quiescence is exactly what
// it was before the marker existed.
func TestReconcileQuiescentWithoutReportStillScrapes(t *testing.T) {
	f := &fakeHerdr{readOut: "I did the thing but wrote nothing."}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	clock.Advance(nudgeGrace + time.Second)
	got, err := reconcile(t, rt, b, agents) // scrape
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found || got.Round != 2 {
		t.Fatalf("scrape must close the round: round=%d found=%v err=%v", got.Round, found, err)
	}
	if pending.Note != "scraped" {
		t.Errorf("note = %q, want scraped", pending.Note)
	}
}

// TestReconcileQuiescentOnLimitSwitchesInsteadOfScraping checks the pane
// quiescence decision point (spec §5): a match on the screen switches the
// builder uncounted instead of scraping a report.
func TestReconcileQuiescentOnLimitSwitchesInsteadOfScraping(t *testing.T) {
	f := &fakeHerdr{readOut: "…\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\n"}
	rt, b := sentSwitchable(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	clock.Advance(nudgeGrace + time.Second)
	got, err := reconcile(t, rt, b, agents) // gate, switch
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	if len(f.closed) != 1 || f.closed[0] != "w2:p4" {
		t.Fatalf("closed = %+v, want [w2:p4]", f.closed)
	}
	if len(f.starts) != 1 || f.starts[0].Kind != "claude" {
		t.Fatalf("starts = %+v, want one claude start", f.starts)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want 1: a switch does not close the round", got.Round)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || found {
		t.Errorf("PendingForPlanner: found=%v err=%v, want no pending payload", found, err)
	}
	rl := rateLimitedEntries(loadLedger(t, rt))
	if len(rl) != 1 || rl[0].Subject != "other" {
		t.Errorf("rate_limited entries = %+v", rl)
	}
	if sw := switches(t, rt); len(sw) != 1 {
		t.Errorf("switch entries = %+v, want 1", sw)
	}
}

// TestReconcileQuiescentWithReportOnLimitGatesAndClosesUnmarked checks that
// a report already on disk still wins over the gate (spec §4.4): the gate is
// recorded, but the round closes unmarked instead of switching.
func TestReconcileQuiescentWithReportOnLimitGatesAndClosesUnmarked(t *testing.T) {
	f := &fakeHerdr{readOut: "…\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\n"}
	rt, b := sentSwitchable(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	clock.Advance(nudgeGrace + time.Second)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcile(t, rt, b, agents); err != nil { // gate, close unmarked
		t.Fatalf("second Reconcile: %v", err)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("PendingForPlanner: found=%v err=%v", found, err)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
	if !strings.Contains(pending.Payload, "Provider rate-limited:") {
		t.Errorf("payload = %q, want the rate-limit sentence", pending.Payload)
	}
	rl := rateLimitedEntries(loadLedger(t, rt))
	if len(rl) != 1 {
		t.Errorf("rate_limited entries = %+v, want 1", rl)
	}
	if len(f.closed) != 0 {
		t.Errorf("closed = %+v, want none", f.closed)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none", f.starts)
	}
}
