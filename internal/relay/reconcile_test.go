package relay

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/store"
)

func builderAgent(status string) stubAgent {
	return stubAgent{
		Name: "webshop-builder",
		Kind: "agy", Status: status, CWD: "/repo", PaneID: "w2:p4",
		Title: "webshop-builder",
	}
}

// sentBinding puts a binding one Send into round 1, with a working builder.
func sentBinding(t *testing.T, f *fakePanes) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := seedBound(t, f)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
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
func sentBindingWithBuilderSession(t *testing.T, f *fakePanes, sessionID string) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []stubAgent{
		plannerAgent(),
		{Kind: "agy", Status: stubWorking, PaneID: "w2:p4", Session: stubSession{Value: sessionID}},
	}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
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
func reconcile(t *testing.T, rt Runtime, b store.Binding, agents []stubAgent) (store.Binding, error) {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = Reconcile(context.Background(), rt, tx, b)
		return err
	})
	return out, err
}

func TestReconcileQueuesReportWhenBuilderIdleAndMarkerExists(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	b.RoundSwitches = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}

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

func TestReconcileQueuesReportInsideStartGrace(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	clock.Advance(5 * time.Second)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}

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

func TestReconcileIgnoresWorkingBuilder(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	agents := []stubAgent{plannerWith(stubIdle, false), builderAgent(stubWorking)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 || len(f.prompts) != 0 {
		t.Errorf("a working builder must be left alone: round=%d prompts=%+v", got.Round, f.prompts)
	}
}

// TestReconcileSkipsPaused: Reconcile returns immediately for a PAUSED
// binding, exactly as it does for DONE. A stale builder pane left in herdr's
// list must not mark it broken and must not be nudged.
func TestReconcileSkipsPaused(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	b.State = store.StatePaused
	b.Builder = store.Endpoint{Kind: "agy"} // pause cleared the pane id
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save paused: %v", err)
	}

	agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StatePaused {
		t.Errorf("State = %s, want paused", got.State)
	}
	if !got.BuilderMissingSince.IsZero() {
		t.Errorf("BuilderMissingSince = %v, want zero", got.BuilderMissingSince)
	}
	if len(f.prompts) != 0 {
		t.Errorf("a paused binding must not be nudged: %+v", f.prompts)
	}
}

func TestReconcileNeverTreatsUnknownAsDone(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	agents := []stubAgent{plannerWith(stubIdle, false), builderAgent(stubUnknown)}

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

func TestReconcileRecoveredBindingProceedsNormally(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBindingWithBuilderSession(t, f, "builder-sess")
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	b.State = store.StateBroken

	agents := []stubAgent{
		plannerWith(stubWorking, false),
		{Kind: "agy", Status: stubIdle, PaneID: "w2:p4", Session: stubSession{Value: "builder-sess"}},
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
	f := &fakePanes{}
	rt, b := sentBindingWithBuilderSession(t, f, "builder-sess")
	b.State = store.StateBroken

	// A different agent now occupies the same pane: relaying into it would
	// hand plans to a stranger.
	agents := []stubAgent{
		plannerWith(stubWorking, false),
		{Kind: "agy", Status: stubIdle, PaneID: "w2:p4", Session: stubSession{Value: "impostor-sess"}},
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

func TestReconcileDiffCapture(t *testing.T) {
	f := &fakePanes{}
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

	agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}

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
		f := &fakePanes{}
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
		agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}
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

func TestReconcileLeavesBrokenWhenPaneHoldsADifferentKind(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	b.State = store.StateBroken

	stranger := builderAgent(stubWorking)
	stranger.Name = ""       // a stranger does not carry the name relay gave its builder
	stranger.Kind = "claude" // same pane, different agent

	out, err := reconcile(t, rt, b, []stubAgent{plannerAgent(), stranger})
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

// TestQueueReportRecordsRusage: a headless round's report entry gets
// Rusage from rt.Runner.Rusage when the runner has one, and stays nil
// when it does not (#244, #216).
func TestQueueReportRecordsRusage(t *testing.T) {
	setup := func(t *testing.T) (Runtime, store.Binding, *fakeRunner) {
		t.Helper()
		fr := newFakeRunner()
		rt, b := seedHeadless(t, &fakePanes{}, fr)
		b.Builder.PID = 9001
		b.Builder.StartedAt = 1_700_000_000
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte("report body"), 0o644); err != nil {
			t.Fatal(err)
		}
		return rt, b, fr
	}
	closeRound := func(t *testing.T, rt Runtime, b store.Binding) store.LogEntry {
		t.Helper()
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load(b.Name)
			if err != nil {
				return err
			}
			entries, err := tx.ReadLog(b.Name)
			if err != nil {
				return err
			}
			next, err := queueReport(context.Background(), rt, tx, cur, entries, rt.Store.ReportPath(b.Name, b.Round), "done", "test", nil, nil, nil)
			if err != nil {
				return err
			}
			return tx.Save(next)
		})
		if err != nil {
			t.Fatalf("queueReport: %v", err)
		}
		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Round == b.Round && e.Kind == store.KindReport {
				return e
			}
		}
		t.Fatalf("no report entry for round %d in %+v", b.Round, entries)
		return store.LogEntry{}
	}

	t.Run("ok true", func(t *testing.T) {
		rt, b, fr := setup(t)
		fr.setRusage(b.Builder.PID, ProcRusage{CPUMS: 12300, PeakMemBytes: 850 << 20})
		entry := closeRound(t, rt, b)
		if entry.Rusage == nil || entry.Rusage.CPUMS != 12300 || entry.Rusage.PeakMemBytes != 850<<20 {
			t.Errorf("report entry Rusage = %+v, want {12300 %d}", entry.Rusage, int64(850<<20))
		}
	})
	t.Run("ok false", func(t *testing.T) {
		rt, b, _ := setup(t)
		entry := closeRound(t, rt, b)
		if entry.Rusage != nil {
			t.Errorf("report entry Rusage = %+v, want nil", entry.Rusage)
		}
	})
}

func TestQueueReport_RoundClosedTree(t *testing.T) {
	t.Run("ordinary close", func(t *testing.T) {
		f := &fakePanes{}
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

		agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}
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
		f := &fakePanes{}
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

		agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}
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
		f := &fakePanes{}
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

		agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}
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

// closeOnMarkerUnderLock calls closeOnMarker the way Reconcile does: inside
// the store lock, with the binding's current log.
func closeOnMarkerUnderLock(t *testing.T, rt Runtime, b store.Binding) (store.Binding, bool) {
	t.Helper()
	out, closed, _ := closeOnMarkerUnderLockGating(t, rt, b)
	return out, closed
}

// closeOnMarkerUnderLockGating is closeOnMarkerUnderLock plus the gating
// return, for the gate lifecycle tests (#132).
func closeOnMarkerUnderLockGating(t *testing.T, rt Runtime, b store.Binding) (store.Binding, bool, bool) {
	t.Helper()
	var out store.Binding
	var closed, gating bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		out, closed, gating, _, err = closeOnMarker(context.Background(), rt, tx, b, entries, "")
		return err
	})
	if err != nil {
		t.Fatalf("closeOnMarker: %v", err)
	}
	return out, closed, gating
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}

func TestCloseOnMarkerWithReportClosesNormally(t *testing.T) {
	f := &fakePanes{}
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
	f := &fakePanes{readOut: "some terminal text"}
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
	f := &fakePanes{}
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
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubWorking)}

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

// gates returns the gate entries in webshop's log.
func gates(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindGate {
			out = append(out, e)
		}
	}
	return out
}

// TestGateNotConfiguredIsUnchanged pins #132: a binding with no gate closes
// exactly as it did before the gate existed -- no process is started and the
// payload is unchanged.
func TestGateNotConfiguredIsUnchanged(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if gating {
		t.Fatal("gating = true with no gate configured")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if len(fr.specs) != 0 {
		t.Fatalf("Start calls = %d, want 0 with no gate configured", len(fr.specs))
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "" {
		t.Errorf("note = %q, want empty", pending.Note)
	}
	want := "Builder finished round 1. Report: " + rt.Store.ReportPath("webshop", 1)
	if !strings.HasSuffix(pending.Payload, want) {
		t.Errorf("payload = %q, want it to end with %q (unchanged by the gate)", pending.Payload, want)
	}
	if strings.Contains(pending.Payload, "Gate:") {
		t.Errorf("payload = %q, want no gate line with no gate configured", pending.Payload)
	}
}

// TestGatePassClosesWithAnnotation pins #132: a gate that exits 0 closes the
// round with a gate=pass annotation, a Gate record on the entry, and the
// gate's payload line.
func TestGatePassClosesWithAnnotation(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	// First tick starts the gate.
	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if closed || !gating {
		t.Fatalf("closed=%v gating=%v after starting the gate, want gating only", closed, gating)
	}

	// The gate exits 0.
	fr.script(fr.handles[0].PID, false)
	fr.exit(fr.handles[0].PID, 0)

	got, closed, gating = closeOnMarkerUnderLockGating(t, rt, got)
	if gating {
		t.Fatal("gating = true after the gate exited")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if got.GateRun != nil {
		t.Errorf("GateRun = %+v, want nil after the round closes", got.GateRun)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=pass" {
		t.Errorf("note = %q, want gate=pass", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "pass" || pending.Gate.ExitCode != 0 {
		t.Fatalf("Gate = %+v, want Result=pass ExitCode=0", pending.Gate)
	}
	wantLog := rt.Store.GateLogPath("webshop", 1)
	if !strings.Contains(pending.Payload, "Gate: make check -- PASS (exit 0,") ||
		!strings.Contains(pending.Payload, wantLog) {
		t.Errorf("payload = %q", pending.Payload)
	}
}

// TestGateFailAddsTail pins #132: a gate that exits non-zero closes the
// round with gate=fail, and the payload carries the log's last
// gateTailLines non-empty lines, not the first.
func TestGateFailAddsTail(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, _, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if !gating {
		t.Fatal("want gating after starting the gate")
	}

	gateLog := rt.Store.GateLogPath("webshop", 1)
	body := "line one\nline two\nline three\nline four\nline five\nline six\n"
	if err := os.WriteFile(gateLog, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	fr.script(fr.handles[0].PID, false)
	fr.exit(fr.handles[0].PID, 2)

	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, got)
	if gating {
		t.Fatal("gating = true after the gate exited")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=fail" {
		t.Errorf("note = %q, want gate=fail", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "fail" || pending.Gate.ExitCode != 2 {
		t.Fatalf("Gate = %+v, want Result=fail ExitCode=2", pending.Gate)
	}
	if !strings.Contains(pending.Payload, "FAIL (exit 2,") {
		t.Errorf("payload = %q, want it to contain FAIL (exit 2,", pending.Payload)
	}
	for _, want := range []string{"line two", "line three", "line four", "line five", "line six"} {
		if !strings.Contains(pending.Payload, want) {
			t.Errorf("payload missing tail line %q: %q", want, pending.Payload)
		}
	}
	if strings.Contains(pending.Payload, "line one") {
		t.Errorf("payload must carry only the last 5 lines, not the first: %q", pending.Payload)
	}
}

// TestGateTimeoutKills pins #132: a gate that outlives its timeout is
// killed and the round closes with gate=timeout.
func TestGateTimeoutKills(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, _, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if !gating {
		t.Fatal("want gating after starting the gate")
	}
	// fakeRunner.Start stamps StartedAt from a real epoch unrelated to the
	// fake clock, so it is realigned here -- otherwise the elapsed-since-start
	// arithmetic in gateStep would see it as already far past any timeout.
	got.GateRun.StartedAt = clock.Now().Unix()

	fr.script(fr.handles[0].PID, true)
	clock.Advance(rt.Policy.GateTimeout() - time.Second)
	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, got)
	if closed || !gating {
		t.Fatalf("closed=%v gating=%v just under the timeout, want still gating", closed, gating)
	}
	if len(fr.kills) != 0 {
		t.Fatalf("kills = %+v before the timeout elapsed, want none", fr.kills)
	}

	fr.script(fr.handles[0].PID, true)
	clock.Advance(2 * time.Second)
	got, closed, gating = closeOnMarkerUnderLockGating(t, rt, got)
	if gating {
		t.Fatal("gating = true after the timeout")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != fr.handles[0].PID {
		t.Fatalf("kills = %+v, want exactly one kill of pid %d", fr.kills, fr.handles[0].PID)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=timeout" {
		t.Errorf("note = %q, want gate=timeout", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "timeout" {
		t.Fatalf("Gate = %+v, want Result=timeout", pending.Gate)
	}
	if !strings.Contains(pending.Payload, "TIMEOUT after") {
		t.Errorf("payload = %q, want it to contain TIMEOUT after", pending.Payload)
	}
}

// TestGateNoRunnerIsErrorNotHang pins #132: a gate configured on a runtime
// with no Runner cannot hang the round -- it closes this tick with a
// gate=error annotation instead.
func TestGateNoRunnerIsErrorNotHang(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	rt.Runner = nil
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if gating {
		t.Fatal("gating = true with no runner; want an immediate error result")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=error" {
		t.Errorf("note = %q, want gate=error", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "error" || pending.Gate.Note != "no runner" {
		t.Fatalf("Gate = %+v, want Result=error Note=\"no runner\"", pending.Gate)
	}
	if !strings.Contains(pending.Payload, "Gate: make check -- ERROR: no runner.") {
		t.Errorf("payload = %q", pending.Payload)
	}
}

// gateRecordFor finds a binding's report entry for one round and returns its
// gate record, failing the test when the round has no report entry. It is how
// the regate tests inspect a round that closed earlier than the one currently
// in flight: PendingForPlanner only ever hands back the oldest.
func gateRecordFor(t *testing.T, rt Runtime, name string, round int) *store.GateRecord {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%s): %v", name, err)
	}
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport {
			return e.Gate
		}
	}
	t.Fatalf("no report entry for round %d of %s", round, name)
	return nil
}

// failRoundWithGate drives b's current round through the real Reconcile call
// site until its gate exits non-zero: the first tick starts the gate, the
// second (after the fake runner is told the process exited) closes the round
// with gate=fail. It returns the binding Reconcile returned and the failing
// record, and fails the test if the round did not close on the gate.
func failRoundWithGate(t *testing.T, rt Runtime, b store.Binding, fr *fakeRunner, logBody string, agents []stubAgent) (store.Binding, *store.GateRecord) {
	t.Helper()
	round := b.Round
	if err := os.WriteFile(rt.Store.ReportPath(b.Name, round), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath(b.Name, round))

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("reconcile (start gate, round %d): %v", round, err)
	}
	if got.GateRun == nil {
		t.Fatalf("round %d: GateRun is nil; the gate did not start", round)
	}
	pid := got.GateRun.PID
	if err := os.WriteFile(rt.Store.GateLogPath(b.Name, round), []byte(logBody), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.script(pid, false)
	fr.exit(pid, 2)

	got, err = reconcile(t, rt, got, agents)
	if err != nil {
		t.Fatalf("reconcile (close gate, round %d): %v", round, err)
	}
	rec := gateRecordFor(t, rt, b.Name, round)
	if rec == nil || rec.Result != "fail" {
		t.Fatalf("round %d: report gate = %+v, want a fail record", round, rec)
	}
	return got, rec
}

// TestRegateIdenticalSignatureHaltsEarly pins the stall bound (#132 part 2):
// a second identical failure -- same content modulo the clock -- means the
// repair changed nothing that mattered, so the loop ends early.
func TestRegateIdenticalSignatureHaltsEarly(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	b.Gate = "make check"
	b.Regate = 5
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	agents := []stubAgent{plannerAgent(), builderAgent(stubWorking)}

	got, _ := failRoundWithGate(t, rt, b, fr,
		"2026-09-21T14:29:00Z FAIL github.com/example/pkg2 0.02s\n", agents)
	if got.State != store.StateActive {
		t.Fatalf("after the first failure: state = %q, want active (a repair started)", got.State)
	}

	got, _ = failRoundWithGate(t, rt, got, fr,
		"2026-09-21T14:31:07Z FAIL github.com/example/pkg2 9.99s\n", agents)

	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "unchanged after repair") {
		t.Errorf("Halt = %q, want it to mention the unchanged gate output", got.Halt)
	}
	if got.RepairCount != 1 {
		t.Errorf("RepairCount = %d, want 1 (the early halt is not a repair)", got.RepairCount)
	}
}

// TestRegatePassResetsCount pins #132 part 2's reset: a passing gate clears
// the repair bookkeeping, so the next failing gate gets a fresh budget.
func TestRegatePassResetsCount(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	b.Gate = "make check"
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	agents := []stubAgent{plannerAgent(), builderAgent(stubWorking)}

	got, _ := failRoundWithGate(t, rt, b, fr, "FAIL github.com/example/pkg2\n", agents)
	if got.RepairCount != 1 || got.LastGateSig == "" {
		t.Fatalf("after the first failure: repairs=%d sig=%q, want 1 and non-empty", got.RepairCount, got.LastGateSig)
	}

	// Round 2's gate passes.
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 2), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 2))

	got, err := reconcile(t, rt, got, agents)
	if err != nil {
		t.Fatalf("reconcile (start gate, round 2): %v", err)
	}
	if got.GateRun == nil {
		t.Fatal("round 2: GateRun is nil; the gate did not start")
	}
	pid := got.GateRun.PID
	if err := os.WriteFile(rt.Store.GateLogPath("webshop", 2), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.script(pid, false)
	fr.exit(pid, 0)

	got, err = reconcile(t, rt, got, agents)
	if err != nil {
		t.Fatalf("reconcile (close gate, round 2): %v", err)
	}

	if rec := gateRecordFor(t, rt, "webshop", 2); rec == nil || rec.Result != "pass" {
		t.Fatalf("round 2 report gate = %+v, want pass", rec)
	}
	if got.RepairCount != 0 {
		t.Errorf("RepairCount = %d, want 0 after a passing gate", got.RepairCount)
	}
	if got.LastGateSig != "" {
		t.Errorf("LastGateSig = %q, want \"\" after a passing gate", got.LastGateSig)
	}
	if got.Round != 3 {
		t.Errorf("Round = %d, want 3", got.Round)
	}
}

// TestNoRegateUnchanged pins the off switch (#132 part 2): Regate 0 is
// exactly today's behaviour -- the failure is reported, nothing is re-sent.
func TestNoRegateUnchanged(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	agents := []stubAgent{plannerAgent(), builderAgent(stubWorking)}

	got, _ := failRoundWithGate(t, rt, b, fr, "FAIL github.com/example/pkg2\n", agents)

	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2", got.Round)
	}
	if got.RepairCount != 0 || got.LastGateSig != "" {
		t.Errorf("repair bookkeeping moved with regate 0: repairs=%d sig=%q", got.RepairCount, got.LastGateSig)
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", 2)); err == nil {
		t.Error("no round-2 plan may be staged when regate is 0")
	}
	if len(f.prompts) != 0 {
		t.Errorf("prompts = %d, want none with regate 0", len(f.prompts))
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=fail" {
		t.Errorf("note = %q, want gate=fail", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "fail" || pending.Gate.ExitCode != 2 {
		t.Fatalf("Gate = %+v, want Result=fail ExitCode=2", pending.Gate)
	}
}

// roundReportEntry is webshop's queued report entry for round. It is the
// non-e2e form of e2e_test.go's reportEntry, which is behind the e2e tag.
func roundReportEntry(t *testing.T, rt Runtime, round int) store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport {
			return e
		}
	}
	t.Fatalf("no report entry for round %d", round)
	return store.LogEntry{}
}

// TestReportEntryNoSessionIsNil pins #147's never-guess rule: a pane whose
// agent reports no session leaves BuilderSession nil, and the entry's JSON
// carries no builder_session key.
func TestReportEntryNoSessionIsNil(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	agents := []stubAgent{plannerWith(stubWorking, false), builderAgent(stubIdle)}
	if _, err := reconcile(t, rt, b, agents); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	entry := roundReportEntry(t, rt, 1)
	if entry.BuilderSession != nil {
		t.Errorf("BuilderSession = %+v, want nil when no session is known", entry.BuilderSession)
	}
	blob, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "builder_session") {
		t.Errorf("JSON of a session-less entry carries builder_session: %s", blob)
	}
}

// TestVerifyGateLogIsPassed pins #144's gate handoff: the reviewer is told
// where the closed round's gate log is, because seeing the gate's own output
// is the point of running verify after the gate.
func TestVerifyGateLogIsPassed(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	rt.Git = &fakeGit{headCommitID: "head1"}
	rt.NewID = func() string { return verifyConsultID }

	b.RoundVerify = true
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	agents := []stubAgent{plannerWith(stubIdle, false), builderAgent(stubWorking)}

	// First tick starts the gate and holds the round open.
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("round = %d, want 1 while the gate runs", got.Round)
	}

	// The gate exits 0; the next tick closes the round and starts the reviewer.
	fr.script(fr.handles[0].PID, false)
	fr.exit(fr.handles[0].PID, 0)
	got, err = reconcile(t, rt, got, agents)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2 after the gate passed", got.Round)
	}

	question, err := os.ReadFile(rt.Store.AskPath("webshop", 1, verifyConsultID))
	if err != nil {
		t.Fatalf("read ask file: %v", err)
	}
	wantLog := rt.Store.GateLogPath("webshop", 1)
	if !strings.Contains(string(question), "Gate:   "+wantLog) {
		t.Errorf("ask file does not name the gate log %s:\n%s", wantLog, question)
	}
}

// candidateSetWithoutReviewerJSON serves builder and nothing else, so
// resolveCandidate refuses every reviewer.
const candidateSetWithoutReviewerJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"]}
]`

// TestVerifySkippedWhenNoReviewerCandidate pins #144's error handling: a
// round with no reviewer candidate closes normally, logs one "verify skipped:"
// note, starts nothing, and leaves no throwaway worktree behind.
func TestVerifySkippedWhenNoReviewerCandidate(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := sentBinding(t, f)
	rt.Runner = fr
	fg := &fakeGit{headCommitID: "head1"}
	rt.Git = fg
	rt.Candidates = candidateSet(t, candidateSetWithoutReviewerJSON)

	b.RoundVerify = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	agents := []stubAgent{plannerWith(stubIdle, false), builderAgent(stubWorking)}
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2: a missing reviewer is not a round failure", got.Round)
	}
	if len(fr.specs) != 0 {
		t.Errorf("Start calls = %d, want none without a reviewer candidate", len(fr.specs))
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	note := ""
	for _, e := range entries {
		if strings.HasPrefix(e.Note, "verify skipped:") {
			note = e.Note
		}
	}
	if note == "" {
		t.Fatalf("no \"verify skipped:\" note in the log: %+v", entries)
	}

	// Whatever the order, no throwaway worktree is left behind.
	wantWT := rt.Store.VerifyWorktreePath("webshop", 1)
	if len(fg.addDetachedWorktreeCalls) > 0 {
		removed := false
		for _, c := range fg.removeWorktreeCalls {
			if c.Path == wantWT && c.Force {
				removed = true
			}
		}
		if !removed {
			t.Errorf("worktree %s was created but never removed (removals: %+v)", wantWT, fg.removeWorktreeCalls)
		}
	}
}
