package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func seedBound(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []herdr.Agent{plannerAgent()}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Alias: "abuilder", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// The spawned agent registers with herdr moments after StartAgent returns,
	// which Bind's own post-spawn lookup is too early to see. Appending it here
	// -- after Bind -- keeps the empty SessionID that lookup produces, which is
	// the #20 condition several tests rely on, while letting Send and Reconcile
	// locate the builder the way they would against a real herdr.
	f.agents = append(f.agents, builderAgent(herdr.StatusWorking))

	return rt, b
}

func writePlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

func TestSendCopiesPlanAndPromptsBuilder(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	src := writePlan(t, "# do the thing")

	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Fatalf("round = %d, want 1", res.Round)
	}

	copied, err := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if err != nil {
		t.Fatalf("plan not copied into state: %v", err)
	}
	if string(copied) != "# do the thing" {
		t.Errorf("copied plan = %q", copied)
	}

	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	text := f.prompts[0].Text
	if !strings.Contains(text, rt.Store.PlanPath("webshop", 1)) {
		t.Error("prompt must name the plan path")
	}
	if !strings.Contains(text, rt.Store.ReportPath("webshop", 1)) {
		t.Error("prompt must name the report path")
	}
	if f.prompts[0].Target != "w2:p4" {
		t.Errorf("target = %q, want pane id w2:p4", f.prompts[0].Target)
	}
}

func TestSendIncludesPreambleOnFirstRoundOnly(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f) // abuilder carries a preamble
	src := writePlan(t, "x")

	if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
		t.Fatalf("round 1 Send: %v", err)
	}
	if !strings.Contains(f.prompts[0].Text, "plan-executor") {
		t.Error("round 1 prompt must carry the abuilder preamble")
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
		t.Fatalf("round 2 Send: %v", err)
	}
	if strings.Contains(f.prompts[1].Text, "plan-executor") {
		t.Error("the preamble must not repeat after round 1")
	}
}

func TestSendIncludesPreambleWhenPending(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	src := writePlan(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Round = 5
	b.PreamblePending = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
		t.Fatalf("round 5 Send: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	if !strings.Contains(f.prompts[0].Text, "plan-executor") {
		t.Error("round 5 prompt with PreamblePending must carry the preamble")
	}

	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.PreamblePending {
		t.Error("PreamblePending must be cleared after successful Send")
	}

	if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
		t.Fatalf("subsequent Send: %v", err)
	}
	if len(f.prompts) != 2 {
		t.Fatalf("got %d prompts, want 2", len(f.prompts))
	}
	if strings.Contains(f.prompts[1].Text, "plan-executor") {
		t.Error("round 5 prompt without PreamblePending must not carry the preamble")
	}
}

func TestSendFailedPromptLeavesPreamblePending(t *testing.T) {
	f := &fakeHerdr{promptErr: errors.New("builder crashed")}
	rt, _ := seedBound(t, f)
	src := writePlan(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Round = 5
	b.PreamblePending = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", src); err == nil {
		t.Fatal("Send must fail when builder prompt fails")
	}

	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !b.PreamblePending {
		t.Error("PreamblePending must remain set if prompt failed")
	}
}

func TestSendLogsThePlan(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != store.KindPlan || entries[0].Direction != store.DirToBuilder {
		t.Fatalf("log = %+v", entries)
	}
	if !entries[0].Confirmed {
		t.Error("an outbound plan is confirmed the moment herdr accepts it")
	}
}

func TestSendRetriesOnceOnStall(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.stalls = 1
	f.prompts = nil

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x")); err != nil {
		t.Fatalf("Send must retry once past a stall: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("got %d accepted prompts, want 1", len(f.prompts))
	}
}

func TestSendGivesUpAfterTwoStalls(t *testing.T) {
	f := &fakeHerdr{stalls: 2}
	rt, _ := seedBound(t, f)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x")); err == nil {
		t.Fatal("two stalls must fail rather than fire a third time")
	}
}

func TestSendSurfacesBlockedBuilder(t *testing.T) {
	f := &fakeHerdr{promptErr: herdr.ErrAgentBlocked}
	rt, _ := seedBound(t, f)

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"))
	if !errors.Is(err, ErrBuilderBlocked) {
		t.Fatalf("got %v, want ErrBuilderBlocked", err)
	}
}

// TestPromptRetryReportsANonStallFailureAsItself keeps the second failure
// honest: the retry can fail for an unrelated reason -- the builder became
// blocked between the two attempts, say -- and calling that a stall sends the
// human looking at the wrong thing.
func TestPromptRetryReportsANonStallFailureAsItself(t *testing.T) {
	f := &fakeHerdr{stalls: 1, promptErr: herdr.ErrAgentBlocked}
	rt, _ := seedBound(t, f)

	err := promptWithRetry(context.Background(), rt, "webshop-builder", "text")
	if err == nil {
		t.Fatal("a failing retry must surface an error")
	}
	if !errors.Is(err, herdr.ErrAgentBlocked) {
		t.Errorf("the real cause must survive the wrap, got %v", err)
	}
	if strings.Contains(err.Error(), "stalled twice") {
		t.Errorf("a non-stall retry failure must not be reported as a stall: %v", err)
	}
	if !strings.Contains(err.Error(), "failed on retry") {
		t.Errorf("error = %v, want it to name the retry", err)
	}
}

// TestPromptRetryReportsASecondStallAsAStall is the other branch: two genuine
// stalls stay labelled as such, and relay never fires a third time.
func TestPromptRetryReportsASecondStallAsAStall(t *testing.T) {
	f := &fakeHerdr{stalls: 2}
	rt, _ := seedBound(t, f)

	err := promptWithRetry(context.Background(), rt, "webshop-builder", "text")
	if err == nil || !strings.Contains(err.Error(), "stalled twice") {
		t.Fatalf("err = %v, want a stalled-twice error", err)
	}
	if len(f.prompts) != 0 {
		t.Errorf("neither attempt was accepted, so nothing may be recorded: %+v", f.prompts)
	}
}

func TestSendCapturesBaselineWithFakeGit(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	fg := &fakeGit{snapshotTreeID: "tree-abc123"}
	rt.Git = fg

	src := writePlan(t, "# test plan")
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Fatalf("round = %d, want 1", res.Round)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "tree-abc123" {
		t.Errorf("RoundBaselineTree = %q, want tree-abc123", b.RoundBaselineTree)
	}
	if fg.snapshotCalls != 1 {
		t.Errorf("snapshotCalls = %d, want 1", fg.snapshotCalls)
	}
}

func TestSendBaselineFailureTolerated(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	fg := &fakeGit{snapshotTreeErr: errors.New("git broken")}
	rt.Git = fg

	src := writePlan(t, "# test plan")
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Fatalf("round = %d, want 1", res.Round)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "" {
		t.Errorf("RoundBaselineTree = %q, want empty", b.RoundBaselineTree)
	}

	// Verify plan was still copied and logged
	copied, err := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if err != nil || string(copied) != "# test plan" {
		t.Fatalf("plan file error: %v, content: %q", err, string(copied))
	}
	log, err := rt.Store.ReadLog("webshop")
	if err != nil || len(log) == 0 || log[len(log)-1].Kind != store.KindPlan {
		t.Fatalf("expected plan log entry, got %v, err: %v", log, err)
	}
}

func TestSendAddressesTheLocatedPane(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(f.prompts))
	}
	if f.prompts[0].Target != "w2:p4" {
		t.Fatalf("target = %q, want the located pane w2:p4", f.prompts[0].Target)
	}
}

func TestSendFailsAndStagesNothingWhenBuilderIsGone(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.agents = []herdr.Agent{plannerAgent()} // the builder pane is gone

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"))
	if !errors.Is(err, ErrBuilderGone) {
		t.Fatalf("err = %v, want ErrBuilderGone", err)
	}
	if len(f.prompts) != 0 {
		t.Fatal("nothing may be prompted when the builder is gone")
	}
	if _, statErr := os.Stat(rt.Store.PlanPath("webshop", 1)); statErr == nil {
		t.Fatal("no plan may be staged when the builder is gone")
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 1 {
		t.Fatalf("round = %d, want 1: a failed send must not advance the round", b.Round)
	}
}

// A binding that appears only after the pre-lock load must never be addressed
// with the zero agent's empty pane id. See round 3, Task 1.
func TestSendRefusesWhenBuilderWasNeverLocated(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	// Stand in for the interleaving: no builder could be located pre-lock,
	// but the binding is present and healthy by the time the lock is held.
	f.agents = []herdr.Agent{plannerAgent()}

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"))
	if !errors.Is(err, ErrBuilderGone) {
		t.Fatalf("err = %v, want ErrBuilderGone", err)
	}
	for _, p := range f.prompts {
		if p.Target == "" {
			t.Fatal("relay addressed the empty target instead of refusing")
		}
	}
}

func TestSendUnchangedTreeBetweenRounds(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	b.Round = 2
	b.RoundClosedTree = "tree-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rt.Git = &fakeGit{snapshotTreeID: "tree-1"}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Drift != "" {
		t.Errorf("got Drift %q, want empty", res.Drift)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			t.Fatalf("unexpected KindDrift entry: %+v", e)
		}
	}
}

func TestSendChangedTreeBetweenRounds(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	b.Round = 2
	b.RoundClosedTree = "tree-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rt.Git = &fakeGit{
		snapshotTreeID: "tree-2",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 1, Insertions: 5, Deletions: 2},
			Patch: []byte("patch content\n"),
		},
	}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 2 {
		t.Fatalf("res.Round = %d, want 2", res.Round)
	}
	if res.Drift == "" {
		t.Fatal("expected non-empty Drift line")
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	var driftEntries []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			driftEntries = append(driftEntries, e)
		}
	}
	if len(driftEntries) != 1 {
		t.Fatalf("got %d KindDrift entries, want 1", len(driftEntries))
	}
	de := driftEntries[0]
	if de.Round != 2 {
		t.Errorf("drift entry Round = %d, want opening round 2", de.Round)
	}
	if !de.Confirmed {
		t.Error("drift entry must have Confirmed == true")
	}
	if de.Direction != store.DirToPlanner {
		t.Errorf("drift entry Direction = %v, want DirToPlanner", de.Direction)
	}
	if de.Path == "" {
		t.Fatal("drift entry Path is empty")
	}
	patch, err := os.ReadFile(de.Path)
	if err != nil {
		t.Fatalf("read drift patch %s: %v", de.Path, err)
	}
	if string(patch) != "patch content\n" {
		t.Errorf("patch = %q, want %q", string(patch), "patch content\n")
	}
}

// TestSendDriftEntryPinsConfirmedDoesNotShadowPendingReport asserts that
// an unconsumed pending report is still returned by Pull after a Send with drift.
// An unconfirmed drift entry would shadow the report in pendingForPlanner.
func TestSendDriftEntryPinsConfirmedDoesNotShadowPendingReport(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f) // queues an unconfirmed report for round 1
	b.Round = 2
	b.RoundClosedTree = "tree-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rt.Git = &fakeGit{
		snapshotTreeID: "tree-2",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 1, Insertions: 5, Deletions: 2},
			Patch: []byte("patch content\n"),
		},
	}

	src := writePlan(t, "plan round 2")
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Drift == "" {
		t.Fatal("expected drift to be detected")
	}

	payload, found, err := Pull(context.Background(), rt, "webshop")
	if err != nil || !found {
		t.Fatalf("Pull: found=%v err=%v", found, err)
	}
	if !strings.Contains(payload, "001-report.md") {
		t.Fatalf("Pull returned payload %q, want pending report", payload)
	}
}

func TestSendRound1NoRoundClosedTreeSilent(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	if b.RoundClosedTree != "" {
		t.Fatalf("expected round 1 RoundClosedTree to be empty, got %q", b.RoundClosedTree)
	}

	rt.Git = &fakeGit{snapshotTreeID: "tree-1"}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Drift != "" {
		t.Errorf("res.Drift = %q, want empty", res.Drift)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			t.Fatalf("unexpected KindDrift entry on round 1: %+v", e)
		}
	}
}

func TestSendFailedPromptPreservesRoundClosedTree(t *testing.T) {
	f := &fakeHerdr{promptErr: errors.New("builder prompt crashed")}
	rt, b := seedBound(t, f)
	b.Round = 2
	b.RoundClosedTree = "tree-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rt.Git = &fakeGit{
		snapshotTreeID: "tree-2",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 1, Insertions: 3, Deletions: 1},
			Patch: []byte("diff\n"),
		},
	}

	src := writePlan(t, "plan")
	_, err := Send(context.Background(), rt, "webshop", src)
	if err == nil {
		t.Fatal("expected Send to fail when prompt fails")
	}

	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if b.RoundClosedTree != "tree-1" {
		t.Errorf("RoundClosedTree = %q, want tree-1 preserved after prompt failure", b.RoundClosedTree)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			t.Fatalf("unexpected KindDrift entry when prompt failed: %+v", e)
		}
	}

	// Subsequent successful send reports the drift
	f.promptErr = nil
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("subsequent Send failed: %v", err)
	}
	if res.Drift == "" {
		t.Fatal("subsequent Send must report the drift")
	}

	entries, err = rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	var driftCount int
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			driftCount++
		}
	}
	if driftCount != 1 {
		t.Fatalf("got %d KindDrift entries, want 1", driftCount)
	}

	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if b.RoundClosedTree != "" {
		t.Errorf("RoundClosedTree = %q, want cleared after successful send", b.RoundClosedTree)
	}
}

func TestSendConcurrentRoundAdvanceSkipsDrift(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	b.Round = 2
	b.RoundClosedTree = "tree-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rt.Git = &fakeGit{
		snapshotTreeID: "tree-2",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 1, Insertions: 5, Deletions: 2},
			Patch: []byte("patch content\n"),
		},
	}

	// f.onList fires inside rt.Herdr.ListAgents, which runs after hintRound is read
	// and before WithLock is taken.
	f.onList = func() {
		cur, err := rt.Store.Load("webshop")
		if err == nil {
			cur.Round = 3
			_ = rt.Store.Save(cur)
		}
	}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 3 {
		t.Fatalf("res.Round = %d, want 3", res.Round)
	}
	if res.Drift != "" {
		t.Errorf("expected Drift to be empty on concurrent advance, got %q", res.Drift)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			t.Fatalf("unexpected KindDrift entry on stale round snapshot: %+v", e)
		}
	}
}

func TestSendSuccessfulSendClearsRoundClosedTree(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	b.Round = 2
	b.RoundClosedTree = "tree-closed"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 2 {
		t.Fatalf("round = %d, want 2", res.Round)
	}

	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if b.RoundClosedTree != "" {
		t.Errorf("RoundClosedTree = %q, want empty after successful send", b.RoundClosedTree)
	}
}
