package relay

import (
	"context"
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
	f := &fakeHerdr{readOut: "I implemented the guard clause but could not write the file."}
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
	if len(f.reads) != 1 || f.reads[0].Source != "recent-unwrapped" {
		t.Errorf("scrape reads = %+v, want one recent-unwrapped read", f.reads)
	}
}

// TestReconcileWaitsOutNudgeGraceBeforeScraping is the regression test for a
// scrape that raced the builder: relay nudged on one tick and scraped on the
// next, two seconds later, then advanced the round -- so the builder's real
// report was written to an abandoned round's path and never relayed.
func TestReconcileWaitsOutNudgeGraceBeforeScraping(t *testing.T) {
	f := &fakeHerdr{readOut: "half a screen of output"}
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
	if len(f.reads) != 0 {
		t.Errorf("the terminal must not be scraped inside the grace, got %+v", f.reads)
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

func TestReconcileStaysBrokenWithoutRecordedSession(t *testing.T) {
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
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want still broken: there is no session id to trust", got.State)
	}
	if len(f.prompts) != 0 {
		t.Error("nothing may be relayed without a recorded session id")
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
