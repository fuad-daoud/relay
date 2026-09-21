package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

func seedBound(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []herdr.Agent{plannerAgent()}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
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

	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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

// TestSendArmsSessionCursorForPaneBuilder pins Send's pane branch (#184):
// after the plan lands, the builder's cursor is cut at the located session
// record's current size, so the round's log holds only what the builder
// writes to its own record from here on.
func TestSendArmsSessionCursorForPaneBuilder(t *testing.T) {
	f := &fakeHerdr{}
	// The session id must be on the agent BEFORE Bind, so Bind's own
	// post-spawn lookup (bind.go's best-effort ListAgents) records it on
	// the endpoint -- the same pattern sentBindingWithBuilderSession uses.
	f.agents = []herdr.Agent{
		plannerAgent(),
		{Kind: "agy", Status: herdr.StatusWorking, PaneID: "w2:p4", Session: herdr.Session{Value: "S"}},
	}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	f.prompts = nil

	record := filepath.Join(t.TempDir(), "S.jsonl")
	if err := os.WriteFile(record, []byte(strings.Repeat("x", 42)), 0o644); err != nil {
		t.Fatalf("write record: %v", err)
	}
	rt.Sessions = func(kind, sessionID string) (string, bool) {
		if kind == "agy" && sessionID == "S" {
			return record, true
		}
		return "", false
	}

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Builder.StreamRound != res.Round {
		t.Errorf("StreamRound = %d, want round %d", b.Builder.StreamRound, res.Round)
	}
	if b.Builder.StreamOffset != 42 {
		t.Errorf("StreamOffset = %d, want 42 (the record's size at send time)", b.Builder.StreamOffset)
	}
	if want := rt.Store.BuilderLogPath("webshop", res.Round); b.Builder.LogPath != want {
		t.Errorf("LogPath = %q, want %q", b.Builder.LogPath, want)
	}
}

// The role is selected with --agent at launch (#85); the plan prompt is
// the plan prompt, on round 1 as on every other.
func TestSendPromptCarriesNoPreamble(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f) // an agy candidate, the kind that used to get one
	src := writePlan(t, "x")
	if _, err := Send(context.Background(), rt, "webshop", src, SendOptions{}); err != nil {
		t.Fatalf("round 1 Send: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	if strings.Contains(f.prompts[0].Text, "Activate your") {
		t.Errorf("round 1 prompt must not carry a preamble:\n%s", f.prompts[0].Text)
	}
	wantOrigin := OriginLine("webshop", 1, store.DirToBuilder, store.KindPlan)
	if !strings.HasPrefix(f.prompts[0].Text, wantOrigin) {
		t.Errorf("prompt must start with origin line:\n%s", f.prompts[0].Text)
	}
	if !strings.Contains(f.prompts[0].Text, "Your working tree is: /repo") {
		t.Errorf("prompt must name the working tree (#192):\n%s", f.prompts[0].Text)
	}
}

func TestSendLogsThePlan(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	// seedBound's underlying Bind already wrote the builder bind's pick entry.
	if len(entries) != 2 || entries[0].Kind != store.KindPick || entries[1].Kind != store.KindPlan || entries[1].Direction != store.DirToBuilder {
		t.Fatalf("log = %+v", entries)
	}
	if !entries[1].Confirmed {
		t.Error("an outbound plan is confirmed the moment herdr accepts it")
	}
}

func TestSendStallThenFingerprintOnScreenIsLate(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.stalls = 1
	planPath := rt.Store.PlanPath("webshop", 1)
	f.readOut = "previous output\n" + planPath + "\nsome other line"
	f.prompts = nil
	f.reads = nil

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("res.Round = %d, want 1", res.Round)
	}
	if len(f.prompts) != 0 {
		t.Fatalf("got %d accepted prompts, want 0 (prompt was not re-sent)", len(f.prompts))
	}
	if len(f.reads) != 1 {
		t.Fatalf("got %d reads, want 1", len(f.reads))
	}
	if f.reads[0].Source != "visible" {
		t.Errorf("read source = %q, want visible", f.reads[0].Source)
	}
	if f.reads[0].Lines != lateScanLines {
		t.Errorf("read lines = %d, want %d", f.reads[0].Lines, lateScanLines)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	planEntry := entries[len(entries)-1]
	if planEntry.Kind != store.KindPlan {
		t.Fatalf("last entry kind = %v, want plan", planEntry.Kind)
	}
	if !planEntry.Late {
		t.Error("planEntry.Late = false, want true")
	}
}

func TestSendStallWithoutFingerprintRetries(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.stalls = 1
	f.readOut = "some other screen"
	f.prompts = nil

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err != nil {
		t.Fatalf("Send must retry once past a stall: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("got %d accepted prompts, want 1", len(f.prompts))
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	planEntry := entries[len(entries)-1]
	if planEntry.Late {
		t.Error("planEntry.Late = true, want false")
	}
}

func TestSendRetriesOnceOnStall(t *testing.T) {
	TestSendStallWithoutFingerprintRetries(t)
}

func TestSendStallReadErrorStillRetries(t *testing.T) {
	f := &fakeHerdr{
		stalls:  1,
		readErr: errors.New("cannot read visible screen"),
	}
	rt, _ := seedBound(t, f)
	f.prompts = nil

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("got %d accepted prompts, want 1 (retry should have succeeded)", len(f.prompts))
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	planEntry := entries[len(entries)-1]
	if planEntry.Late {
		t.Error("planEntry.Late = true, want false when read error fell through to retry")
	}
}

func TestSendUnknownBuilderAtDialogIsBlocked(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	f.agents[1].Status = herdr.StatusUnknown
	f.readOut = "Do you want to proceed?\n❯ 1. Yes"
	f.prompts = nil
	f.reads = nil

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{})
	if !errors.Is(err, ErrBuilderBlocked) {
		t.Fatalf("Send err = %v, want ErrBuilderBlocked", err)
	}
	if len(f.prompts) != 0 {
		t.Errorf("prompts = %d, want 0", len(f.prompts))
	}
	if len(f.reads) != 1 {
		t.Fatalf("reads = %d, want 1", len(f.reads))
	}
	if f.reads[0].Source != "visible" || f.reads[0].Lines != dialogScanLines {
		t.Errorf("read = %+v, want visible with %d lines", f.reads[0], dialogScanLines)
	}

	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if after.Round != b.Round {
		t.Errorf("round = %d, want %d (round not advanced)", after.Round, b.Round)
	}
}

func TestSendUnknownBuilderWithoutDialogSends(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.agents[1].Status = herdr.StatusUnknown
	f.readOut = "$ "
	f.prompts = nil

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Errorf("prompts = %d, want 1", len(f.prompts))
	}
}

func TestSendIdleBuilderNeverScansForDialog(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.agents[1].Status = herdr.StatusIdle
	f.readOut = "Do you want to proceed?\n❯ 1. Yes"
	f.prompts = nil
	f.reads = nil

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Errorf("prompts = %d, want 1", len(f.prompts))
	}
	if len(f.reads) != 0 {
		t.Errorf("reads = %d, want 0", len(f.reads))
	}
}

func TestSendGivesUpAfterTwoStalls(t *testing.T) {
	f := &fakeHerdr{stalls: 2}
	rt, _ := seedBound(t, f)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err == nil {
		t.Fatal("two stalls must fail rather than fire a third time")
	}
}

func TestSendSurfacesBlockedBuilder(t *testing.T) {
	f := &fakeHerdr{promptErr: herdr.ErrAgentBlocked}
	rt, _ := seedBound(t, f)

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{})
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

	err := promptWithRetry(context.Background(), rt, "webshop-builder", "text", "")
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

	err := promptWithRetry(context.Background(), rt, "webshop-builder", "text", "")
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
	fg := &fakeGit{snapshotTreeID: "tree-abc123", headCommitID: "head-abc123"}
	rt.Git = fg

	src := writePlan(t, "# test plan")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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
	if b.RoundBaselineHead != "head-abc123" {
		t.Errorf("RoundBaselineHead = %q, want head-abc123", b.RoundBaselineHead)
	}
	if fg.snapshotCalls != 1 {
		t.Errorf("snapshotCalls = %d, want 1", fg.snapshotCalls)
	}
	if !b.FinishPending {
		t.Error("FinishPending = false, want true after Send opens a round")
	}
}

func TestSendHeadFailureLeavesTreeAndClearsHead(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	rt.Git = &fakeGit{snapshotTreeID: "tree-abc123", headCommitErr: errors.New("unborn HEAD")}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# test plan"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "tree-abc123" || b.RoundBaselineHead != "" {
		t.Errorf("baseline = (%q, %q), want (tree-abc123, \"\")", b.RoundBaselineTree, b.RoundBaselineHead)
	}
}

func TestSendBaselineFailureTolerated(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	fg := &fakeGit{snapshotTreeErr: errors.New("git broken")}
	rt.Git = fg

	src := writePlan(t, "# test plan")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
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

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{})
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

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{})
	if !errors.Is(err, ErrBuilderGone) {
		t.Fatalf("err = %v, want ErrBuilderGone", err)
	}
	for _, p := range f.prompts {
		if p.Target == "" {
			t.Fatal("relay addressed the empty target instead of refusing")
		}
	}
}

// TestSendRefusesPaused: a paused binding has no builder to address and its
// worktree is gone; the human resumes it first. No plan is staged.
func TestSendRefusesPaused(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	b := store.Binding{
		Name: "webshop", CWD: "/repo", Worktree: "/wt/webshop", Branch: "relay/webshop",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{Kind: "agy"}, // pause cleared the pane id
		Round:   2, State: store.StatePaused,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save paused: %v", err)
	}

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{})
	if err == nil {
		t.Fatal("Send on a paused binding must be refused")
	}
	if !strings.Contains(err.Error(), "paused") {
		t.Errorf("err = %q, want it to mention paused", err)
	}
	if entries, _ := rt.Store.ReadLog("webshop"); len(entries) != 0 {
		t.Errorf("no plan may be staged: log = %+v", entries)
	}
	if len(f.prompts) != 0 {
		t.Errorf("no prompt may be sent: %+v", f.prompts)
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
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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
	_, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
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

// TestComposePromptNamesPlanReportAndMarkerInOrder pins the handoff contract:
// the builder is told the plan, the report and the completion marker, in that
// order, and told the marker is its last action (spec §3.3).
func TestComposePromptNamesPlanReportAndMarkerInOrder(t *testing.T) {
	b := store.Binding{Name: "webshop", CWD: "/repo/webshop", Round: 3}
	got := composePrompt(b, "/s/003-plan.md", "/s/003-report.md", "/s/003-done")

	wantOrigin := OriginLine("webshop", 3, store.DirToBuilder, store.KindPlan)
	firstLine := strings.SplitN(got, "\n", 2)[0]
	if firstLine != wantOrigin {
		t.Errorf("first line = %q, want origin line %q", firstLine, wantOrigin)
	}
	if !strings.Contains(got, "Your working tree is: /repo/webshop") {
		t.Errorf("prompt must name the working tree (#192), got:\n%s", got)
	}
	if !strings.Contains(got, "create the done marker,\nand do nothing else.") {
		t.Errorf("prompt must state the git-status halt rule, got:\n%s", got)
	}

	plan := strings.Index(got, "/s/003-plan.md")
	report := strings.Index(got, "/s/003-report.md")
	done := strings.Index(got, "/s/003-done")
	if plan < 0 || report < 0 || done < 0 || !(plan < report && report < done) {
		t.Fatalf("paths must appear plan < report < marker, got:\n%s", got)
	}
	if !strings.Contains(got, "as the very last thing you do") {
		t.Errorf("prompt must say the marker is the last action, got:\n%s", got)
	}
	if !strings.HasSuffix(got, "Reply here with only the report path.") {
		t.Errorf("prompt must end with the reply instruction, got:\n%s", got)
	}
}

func TestSendHeadlessTierYoloOverrideAndRoundClose(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	fr := newFakeRunner()
	// Create an agy candidate without extra_args so TierYolo adds --dangerously-skip-permissions cleanly
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)
	rt.Runner = fr
	_, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   "agy/test/m",
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Headless:    true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	src := writePlan(t, "# do yolo")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{Tier: "yolo", AllowYolo: true})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("round = %d, want 1", res.Round)
	}

	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.RoundTier != "yolo" {
		t.Errorf("RoundTier = %q, want %q", stored.RoundTier, "yolo")
	}

	if len(fr.specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(fr.specs))
	}
	hasYoloFlag := false
	for _, arg := range fr.specs[0].Argv {
		if arg == "--dangerously-skip-permissions" {
			hasYoloFlag = true
			break
		}
	}
	if !hasYoloFlag {
		t.Errorf("expected --dangerously-skip-permissions in argv, got %v", fr.specs[0].Argv)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var planEntry *store.LogEntry
	for i := range entries {
		if entries[i].Round == 1 && entries[i].Kind == store.KindPlan {
			planEntry = &entries[i]
			break
		}
	}
	if planEntry == nil {
		t.Fatal("no plan entry found in log")
	}
	if planEntry.Tier != "yolo" {
		t.Errorf("plan entry Tier = %q, want %q", planEntry.Tier, "yolo")
	}

	// Now round close via queueReport
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog("webshop")
		if err != nil {
			return err
		}
		next, err := queueReport(context.Background(), rt, tx, cur, entries, "/dev/null", "done", "test", nil, nil)
		if err != nil {
			return err
		}
		return tx.Save(next)
	})
	if err != nil {
		t.Fatalf("queueReport round close: %v", err)
	}

	closedB, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load after close: %v", err)
	}
	if closedB.RoundTier != "" {
		t.Errorf("RoundTier after round close = %q, want empty", closedB.RoundTier)
	}
}

func TestSendPaneBindingWithTierEditRefused(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	if b.Builder.Headless() {
		t.Fatal("seedBound must produce a pane builder")
	}

	src := writePlan(t, "# plan")
	_, err := Send(context.Background(), rt, "webshop", src, SendOptions{Tier: "edit"})
	if !errors.Is(err, ErrTierPaneFixed) {
		t.Fatalf("Send err = %v, want ErrTierPaneFixed", err)
	}

	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Round != 1 || stored.RoundStartedAt != (time.Time{}) {
		t.Errorf("round must not advance on refusal: round=%d, startedAt=%v", stored.Round, stored.RoundStartedAt)
	}
}

func TestSendHeadlessNoTierDefaultsToHarness(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, f, fr)

	src := writePlan(t, "# do default")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("round = %d, want 1", res.Round)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var planEntry *store.LogEntry
	for i := range entries {
		if entries[i].Round == 1 && entries[i].Kind == store.KindPlan {
			planEntry = &entries[i]
			break
		}
	}
	if planEntry == nil {
		t.Fatal("no plan entry found in log")
	}
	if planEntry.Tier != "harness" {
		t.Errorf("plan entry Tier = %q, want %q", planEntry.Tier, "harness")
	}

	// Verify argv is identical to pre-#141 (contains extra_args from candidate)
	if len(fr.specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(fr.specs))
	}
	planPath := rt.Store.PlanPath("webshop", 1)
	reportPath := rt.Store.ReportPath("webshop", 1)
	donePath := rt.Store.DonePath("webshop", 1)
	b, _ := rt.Store.Load("webshop")
	wantPrompt := composePrompt(b, planPath, reportPath, donePath)
	wantArgv := []string{
		"agy", "-p", wantPrompt, "--model", "m", "--agent", "plan-executor",
		"--output-format", "stream-json", "--print-timeout", "24h0m0s", "--add-dir", "/repo",
		"--dangerously-skip-permissions",
	}
	if !reflect.DeepEqual(fr.specs[0].Argv, wantArgv) {
		t.Errorf("argv =\n%v\nwant =\n%v", fr.specs[0].Argv, wantArgv)
	}
	if !b.FinishPending {
		t.Error("FinishPending = false, want true after Send opens a round")
	}
}

// TestSendDryRunPaneMakesNoWrites pins the dry run's contract for a pane
// binding: it describes the round Send would open and writes nothing -- no
// staged plan, no log entry, no prompt, no change to the binding (#149).
func TestSendDryRunPaneMakesNoWrites(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	src := writePlan(t, "# do the thing")

	bBefore, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	nBefore, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}

	d, err := SendDryRun(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("SendDryRun: %v", err)
	}

	if d.Round != 1 {
		t.Errorf("Round = %d, want 1", d.Round)
	}
	if d.Mode != "pane" {
		t.Errorf("Mode = %q, want pane", d.Mode)
	}
	if !strings.Contains(d.Where, "w2:p4") {
		t.Errorf("Where = %q, want it to name the located pane w2:p4", d.Where)
	}
	if d.PlanPath != rt.Store.PlanPath("webshop", 1) {
		t.Errorf("PlanPath = %q, want %q", d.PlanPath, rt.Store.PlanPath("webshop", 1))
	}
	wantOrigin := OriginLine("webshop", 1, store.DirToBuilder, store.KindPlan)
	if len(d.PromptHead) == 0 || d.PromptHead[0] != wantOrigin {
		t.Errorf("PromptHead = %q, want it to start with %q", d.PromptHead, wantOrigin)
	}

	if len(f.prompts) != 0 {
		t.Fatalf("a dry run must not prompt: %+v", f.prompts)
	}
	if _, statErr := os.Stat(rt.Store.PlanPath("webshop", 1)); statErr == nil {
		t.Error("no plan file may be staged by a dry run")
	}
	nAfter, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(nAfter) != len(nBefore) {
		t.Errorf("log length changed: %d -> %d", len(nBefore), len(nAfter))
	}
	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !store.SameBinding(bBefore, after) {
		t.Error("a dry run changed the binding")
	}
}

// TestSendDryRunHeadlessShowsArgv pins that a dry run of a headless binding
// reports the launch it would use -- the harness binary first -- without
// starting anything.
func TestSendDryRunHeadlessShowsArgv(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, f, fr)

	d, err := SendDryRun(context.Background(), rt, "webshop", writePlan(t, "# do the thing"), SendOptions{})
	if err != nil {
		t.Fatalf("SendDryRun: %v", err)
	}
	if d.Mode != "headless" {
		t.Errorf("Mode = %q, want headless", d.Mode)
	}
	h, ok := harness.Lookup("agy")
	if !ok || h.Binary == "" {
		t.Fatal("no agy harness to name")
	}
	if !strings.HasPrefix(d.Where, h.Binary) {
		t.Errorf("Where = %q, want it to start with the harness binary %q", d.Where, h.Binary)
	}
	if len(fr.specs) != 0 {
		t.Errorf("a dry run must start nothing: %+v", fr.specs)
	}
}

// TestSendDryRunGateNote pins the advisory gate note: a rate-limited candidate
// still dry-runs, but the note says the daemon would switch after the start.
func TestSendDryRunGateNote(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	if _, err := Unavailable(rt, testAgyRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	d, err := SendDryRun(context.Background(), rt, "webshop", writePlan(t, "# x"), SendOptions{})
	if err != nil {
		t.Fatalf("SendDryRun: %v", err)
	}
	if !strings.Contains(d.GateNote, "rate-limited") {
		t.Errorf("GateNote = %q, want it to say rate-limited", d.GateNote)
	}
	if !strings.Contains(d.GateNote, "would switch") {
		t.Errorf("GateNote = %q, want it to say the daemon would switch", d.GateNote)
	}
}

// TestSendDryRunErrorsMatchSend pins §6: for every precondition, the dry run
// returns the identical error Send would, exit-1 text included, and writes
// nothing on the way.
func TestSendDryRunErrorsMatchSend(t *testing.T) {
	type dryRunCase struct {
		name  string
		opts  SendOptions
		setup func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string)
	}

	broken := func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string) {
		f := &fakeHerdr{}
		rt, _ := seedBound(t, f)
		b, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		b.State = store.StateBroken
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		return rt, f, nil, "webshop", writePlan(t, "# x")
	}
	capped := func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string) {
		f := &fakeHerdr{}
		rt, _ := seedBound(t, f)
		b, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		b.Round, b.RoundCap = 5, 3
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		return rt, f, nil, "webshop", writePlan(t, "# x")
	}
	builderGone := func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string) {
		f := &fakeHerdr{}
		rt, _ := seedBound(t, f)
		f.agents = []herdr.Agent{plannerAgent()} // the builder pane is gone
		return rt, f, nil, "webshop", writePlan(t, "# x")
	}
	headlessBusy := func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, _ := seedHeadless(t, f, fr)
		// Prime a live process: unscripted, the fake reports it alive forever.
		if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# one"), SendOptions{}); err != nil {
			t.Fatalf("prime Send: %v", err)
		}
		return rt, f, fr, "webshop", writePlan(t, "# two")
	}
	headlessNoRunner := func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string) {
		f := &fakeHerdr{}
		rt, _ := seedHeadless(t, f, newFakeRunner())
		rt.Runner = nil
		return rt, f, nil, "webshop", writePlan(t, "# x")
	}

	// The missing-plan case's path is fixed once: the error text names the
	// file, and t.TempDir() mints a new directory on every call.
	missingPlan := filepath.Join(t.TempDir(), "nope.md")
	cases := []dryRunCase{
		{"missing plan file", SendOptions{}, func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string) {
			f := &fakeHerdr{}
			rt, _ := seedBound(t, f)
			return rt, f, nil, "webshop", missingPlan
		}},
		{"unknown binding", SendOptions{}, func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string) {
			f := &fakeHerdr{}
			rt, _ := seedBound(t, f)
			return rt, f, nil, "ghost", writePlan(t, "# x")
		}},
		{"broken binding", SendOptions{}, broken},
		{"round cap", SendOptions{}, capped},
		{"pane builder gone", SendOptions{}, builderGone},
		{"headless busy", SendOptions{}, headlessBusy},
		{"headless without runner", SendOptions{}, headlessNoRunner},
		{"tier on a pane binding", SendOptions{Tier: "edit"}, func(t *testing.T) (Runtime, *fakeHerdr, *fakeRunner, string, string) {
			f := &fakeHerdr{}
			rt, _ := seedBound(t, f)
			return rt, f, nil, "webshop", writePlan(t, "# x")
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rtDry, fDry, frDry, nameDry, fileDry := c.setup(t)
			rtSend, _, _, nameSend, fileSend := c.setup(t)

			nBefore, err := rtDry.Store.ReadLog(nameDry)
			if err != nil {
				t.Fatalf("ReadLog: %v", err)
			}
			planPath := rtDry.Store.PlanPath(nameDry, 1)
			_, statBefore := os.Stat(planPath)
			planExisted := statBefore == nil
			promptsBefore := len(fDry.prompts)
			specsBefore := 0
			if frDry != nil {
				specsBefore = len(frDry.specs)
			}

			_, dryErr := SendDryRun(context.Background(), rtDry, nameDry, fileDry, c.opts)
			if dryErr == nil {
				t.Fatal("SendDryRun: want the error Send gives, got nil")
			}
			_, sendErr := Send(context.Background(), rtSend, nameSend, fileSend, c.opts)
			if sendErr == nil {
				t.Fatal("Send: want an error, got nil")
			}
			if dryErr.Error() != sendErr.Error() {
				t.Errorf("dry run error %q != send error %q", dryErr, sendErr)
			}

			// The dry run wrote nothing.
			if len(fDry.prompts) != promptsBefore {
				t.Errorf("dry run prompted: %+v", fDry.prompts[promptsBefore:])
			}
			if frDry != nil && len(frDry.specs) != specsBefore {
				t.Errorf("dry run started a process: %+v", frDry.specs[specsBefore:])
			}
			nAfter, err := rtDry.Store.ReadLog(nameDry)
			if err != nil {
				t.Fatalf("ReadLog: %v", err)
			}
			if len(nAfter) != len(nBefore) {
				t.Errorf("dry run changed the log: %d -> %d", len(nBefore), len(nAfter))
			}
			_, statAfter := os.Stat(planPath)
			if (statAfter == nil) != planExisted {
				t.Error("dry run changed whether the plan file exists")
			}
		})
	}
}

// TestRenderDryRunShape pins the exact rendered shape of a dry run: the seven
// labelled lines, in order, with the 1024-based size.
func TestRenderDryRunShape(t *testing.T) {
	d := DryRun{
		Name:       "api-auth",
		Round:      5,
		Mode:       "headless",
		Candidate:  "agy/google/gemini-3.8-flash-high",
		Where:      "/usr/bin/agy -p",
		PlanPath:   "/home/p/.local/state/relay/api-auth/005-plan.md",
		PlanFrom:   "./plan.md",
		PlanBytes:  4198,
		ReportPath: "/home/p/.local/state/relay/api-auth/005-report.md",
		DonePath:   "/home/p/.local/state/relay/api-auth/005-done",
		Tier:       "yolo",
		PromptHead: []string{
			`relay: round 5 · to builder "api-auth" · from the planner (not the human)`,
			"Your working tree is: /home/p/.worktrees/api-auth",
		},
	}
	want := `would send round 5 to api-auth
  builder   headless agy/google/gemini-3.8-flash-high
  where     /usr/bin/agy -p
  tier      yolo
  plan      /home/p/.local/state/relay/api-auth/005-plan.md  (staged from ./plan.md, 4.1 KiB)
  report    /home/p/.local/state/relay/api-auth/005-report.md
  marker    /home/p/.local/state/relay/api-auth/005-done
  prompt    relay: round 5 · to builder "api-auth" · from the planner (not the human)
            Your working tree is: /home/p/.worktrees/api-auth
`
	got := RenderDryRun(d)
	if got != want {
		t.Errorf("RenderDryRun:\n%s\nwant:\n%s", got, want)
	}

	// The seven labelled lines appear in this order.
	at := -1
	for _, label := range []string{"builder", "where", "tier", "plan", "report", "marker", "prompt"} {
		i := strings.Index(got, "  "+label+" ")
		if i < 0 {
			t.Fatalf("no %q line in:\n%s", label, got)
		}
		if i < at {
			t.Errorf("label %q is out of order", label)
		}
		at = i
	}

	// A gated candidate's note rides on the builder line.
	d.GateNote = "rate-limited until 00:26; the daemon would switch after start"
	if !strings.Contains(RenderDryRun(d), "(rate-limited until 00:26; the daemon would switch after start)") {
		t.Errorf("the gate note must render on the builder line:\n%s", RenderDryRun(d))
	}
}

// TestVerifyPolicyDefault pins #144's trigger: an explicit SendOptions.Verify
// wins, and a plain Send takes policy.json verify.default.
func TestVerifyPolicyDefault(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	rt.Policy.Verify = &policy.VerifyPolicy{Default: true}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !b.RoundVerify {
		t.Errorf("RoundVerify = false, want true from policy verify.default")
	}

	// An explicit --no-verify beats the policy default.
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "again"), SendOptions{Verify: ptr(false)}); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundVerify {
		t.Errorf("RoundVerify = true after Send{Verify: false}, want false")
	}
}
