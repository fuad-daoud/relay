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
		Name: "webshop", Alias: "abuilder", PlannerPane: "w2:p3", CWD: "/repo",
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

func TestReconcileQueuesReportWhenBuilderIdleAndFileExists(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
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
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0].Text, rt.Store.ReportPath("webshop", 1)) {
		t.Fatalf("expected one nudge naming the report path, got %+v", f.prompts)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("a nudge must not queue anything for the planner")
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
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after a report", got.Round)
	}
	if len(f.prompts) != 0 {
		t.Fatalf("expected zero prompts when report file is present, got %+v", f.prompts)
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
		Name: "webshop", Alias: "abuilder", PlannerPane: "w2:p3", CWD: "/repo",
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
