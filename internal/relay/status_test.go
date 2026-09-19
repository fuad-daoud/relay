package relay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

func TestStatusReportsLiveAgentState(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(rep.Bindings))
	}

	got := rep.Bindings[0]
	if got.PlannerStatus != herdr.StatusWorking || got.BuilderStatus != herdr.StatusWorking {
		t.Errorf("status must come from the live agent list, got %+v", got)
	}
	if got.Display != "ACTIVE" {
		t.Errorf("display = %q, want ACTIVE", got.Display)
	}
	if got.BuilderCandidate != testAgyRef {
		t.Errorf("candidate = %q", got.BuilderCandidate)
	}
}

// TestStatusRemoteRow checks that a remote binding's status row names the
// server as its pane and shows the last RemoteStatus the daemon recorded
// (#100 step 3), and that RenderStatus prints both.
func TestStatusRemoteRow(t *testing.T) {
	f := &fakeHerdr{}
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Builder.RemoteStatus = "running"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Herdr: f, Now: func() time.Time { return baseTime }}
	f.agents = []herdr.Agent{plannerAgent()}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(rep.Bindings))
	}
	got := rep.Bindings[0]
	if got.BuilderPane != "zen" {
		t.Fatalf("BuilderPane = %q, want the server name zen", got.BuilderPane)
	}
	if got.BuilderStatus != "running" {
		t.Fatalf("BuilderStatus = %q, want the recorded RemoteStatus", got.BuilderStatus)
	}

	text := RenderStatus(rep)
	if !strings.Contains(text, "zen") || !strings.Contains(text, "running") {
		t.Errorf("rendered status must show the server and its status, got %q", text)
	}
}

// TestStatusRemoteRowUnknownStatus checks the "" -> "unknown" fallback for a
// remote binding the daemon has never ticked.
func TestStatusRemoteRowUnknownStatus(t *testing.T) {
	f := &fakeHerdr{}
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Herdr: f, Now: func() time.Time { return baseTime }}
	f.agents = []herdr.Agent{plannerAgent()}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].BuilderStatus; got != "unknown" {
		t.Fatalf("BuilderStatus with no recorded RemoteStatus = %q, want unknown", got)
	}
}

func TestStatusMarksMissingAgentsAsGone(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = nil

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].BuilderStatus != "gone" {
		t.Errorf("builder status = %q, want gone", rep.Bindings[0].BuilderStatus)
	}
}

func TestStatusSurfacesHeldPending(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	b.State = store.StateHeld
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true), builderAgent(herdr.StatusIdle)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.Display != "HELD" || got.Pending == nil {
		t.Fatalf("held binding must show its pending payload, got %+v", got)
	}

	text := RenderStatus(rep)
	if !strings.Contains(text, "HELD") || !strings.Contains(text, "webshop") {
		t.Errorf("rendered status = %q", text)
	}
	if !strings.Contains(text, "pending") {
		t.Errorf("rendered status must still show a pending line, got %q", text)
	}
}

// heldStatusBinding saves a HELD binding whose planner clock started 23s
// before the runtime's fixed clock, with the grace the test supplies.
func heldStatusBinding(t *testing.T, f *fakeHerdr, screen string, grace time.Duration) Runtime {
	t.Helper()
	rt, b := queuedBinding(t, f)
	b.State = store.StateHeld
	b.PlannerScreen = screen
	if screen != "" {
		b.PlannerScreenAt = baseTime.Add(-23 * time.Second)
	}
	b.HeldGrace = grace
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true), builderAgent(herdr.StatusIdle)}
	return rt
}

func TestStatusShowsHoldClockAgainstGrace(t *testing.T) {
	f := &fakeHerdr{}
	rt := heldStatusBinding(t, f, "fp", time.Minute)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	hold := rep.Bindings[0].Pending.Hold
	if hold == nil || hold.QuietMS != 23000 || hold.GraceMS != 60000 {
		t.Fatalf("Hold = %+v, want quiet 23000ms of 60000ms", hold)
	}
	text := RenderStatus(rep)
	if !strings.Contains(text, "pending  report round 1 -> planner, held: quiet 23s of 1m0s") {
		t.Errorf("rendered status = %q", text)
	}
}

func TestStatusShowsHoldClockWithoutGrace(t *testing.T) {
	f := &fakeHerdr{}
	rt := heldStatusBinding(t, f, "fp", 0)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	hold := rep.Bindings[0].Pending.Hold
	if hold == nil || hold.QuietMS != 23000 || hold.GraceMS != 0 {
		t.Fatalf("Hold = %+v, want quiet 23000ms with no grace", hold)
	}
	text := RenderStatus(rep)
	if !strings.Contains(text, "held: quiet 23s\n") {
		t.Errorf("a binding held before HeldGrace existed shows the quiet time alone, got %q", text)
	}
}

func TestStatusShowsHoldWaitingForScreen(t *testing.T) {
	f := &fakeHerdr{}
	rt := heldStatusBinding(t, f, "", time.Minute)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].Pending.Hold != nil {
		t.Fatalf("Hold = %+v, want nil: the clock has not started", rep.Bindings[0].Pending.Hold)
	}
	text := RenderStatus(rep)
	if !strings.Contains(text, "held: waiting for the planner's screen") {
		t.Errorf("rendered status = %q", text)
	}
}

func TestStatusPendingLineUnchangedWhenNotHeld(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].Pending == nil || rep.Bindings[0].Pending.Hold != nil {
		t.Fatalf("Pending = %+v, want a pending with no hold", rep.Bindings[0].Pending)
	}
	text := RenderStatus(rep)
	if !strings.Contains(text, "pending  report round 1 -> planner\n") || strings.Contains(text, "held:") {
		t.Errorf("an active binding's pending line must not mention a hold, got %q", text)
	}
}

func TestStatusShowsNudgeClock(t *testing.T) {
	f := &fakeHerdr{readOut: "half a screen of output"}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	// One tick nudges and takes the fingerprint at baseTime; the daemon
	// would persist it, so Status must see the saved binding.
	b, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("nudge Reconcile: %v", err)
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = agents
	clock.Advance(23 * time.Second)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.Nudge == nil {
		t.Fatalf("a nudged round must carry a Nudge, got %+v", row)
	}
	if !row.Nudge.At.Equal(baseTime) || row.Nudge.QuietMS != 23000 || row.Nudge.GraceMS != 60000 {
		t.Fatalf("Nudge = %+v, want at baseTime, quiet 23000ms of 60000ms", row.Nudge)
	}
	if row.Last == nil || row.Last.Note != "nudge" {
		t.Fatalf("Last = %+v, want the nudge entry with its note", row.Last)
	}

	text := RenderStatus(rep)
	if !strings.Contains(text, "  nudge    "+baseTime.Local().Format("15:04:05")+"  quiet 23s of 1m0s\n") {
		t.Errorf("rendered status = %q", text)
	}
	if !strings.Contains(text, "plan to_builder round 1 (nudge)\n") {
		t.Errorf("the last line must say it was a nudge, got %q", text)
	}
}

func TestStatusHasNoNudgeLineWhenNotNudged(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].Nudge != nil {
		t.Fatalf("Nudge = %+v, want nil on an un-nudged round", rep.Bindings[0].Nudge)
	}
	text := RenderStatus(rep)
	if strings.Contains(text, "  nudge") || strings.Contains(text, "(") {
		t.Errorf("no nudge line and no note on an ordinary round, got %q", text)
	}
}

// TestStatusJSONCarriesStructuredFields guards the statusline interface: Last
// and Pending must serialise as JSON objects with typed fields, not as
// rendered prose the consumer would have to regex apart.
func TestStatusJSONCarriesStructuredFields(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	b.State = store.StateHeld
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true), builderAgent(herdr.StatusIdle)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	bindings, ok := decoded["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("bindings = %#v", decoded["bindings"])
	}
	row, ok := bindings[0].(map[string]any)
	if !ok {
		t.Fatalf("row is not a JSON object: %#v", bindings[0])
	}

	pending, ok := row["pending"].(map[string]any)
	if !ok {
		t.Fatalf("pending must be a JSON object, not prose, got %#v", row["pending"])
	}
	if _, ok := pending["round"].(float64); !ok {
		t.Errorf("pending.round missing or not numeric: %#v", pending)
	}
	if _, ok := pending["kind"].(string); !ok {
		t.Errorf("pending.kind missing or not a string: %#v", pending)
	}

	last, ok := row["last"].(map[string]any)
	if !ok {
		t.Fatalf("last must be a JSON object, not prose, got %#v", row["last"])
	}
	if _, ok := last["round"].(float64); !ok {
		t.Errorf("last.round missing or not numeric: %#v", last)
	}
	if _, ok := last["direction"].(string); !ok {
		t.Errorf("last.direction missing or not a string: %#v", last)
	}
}

// TestStatusRowBranch: the worktree branch reaches the row; a --cwd
// binding (no branch) leaves it empty.
func TestStatusRowBranch(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.Branch = "relay/webshop"
	row, err := statusRow(context.Background(), rt, b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.Branch != "relay/webshop" {
		t.Errorf("Branch = %q", row.Branch)
	}
	b.Branch = ""
	row, _ = statusRow(context.Background(), rt, b, nil, nil)
	if row.Branch != "" {
		t.Errorf("--cwd binding Branch = %q, want empty", row.Branch)
	}
}

// TestStatusRowWaiting: a needs_you binding whose round has a captured
// question carries Waiting{Cause: "blocked", Hint: "relay answer ..."};
// an active binding carries nil.
func TestStatusRowWaiting(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateNeedsYou
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	qPath := rt.Store.QuestionPath(b.Name, 2)
	if err := os.WriteFile(qPath, []byte("Do you want to proceed?"), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		TS: rt.Now().UTC(), Round: 2, Direction: store.DirToPlanner, Kind: store.KindQuestion,
		Path: qPath,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	row, err := statusRow(context.Background(), rt, b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.Waiting == nil || row.Waiting.Cause != "blocked" {
		t.Fatalf("Waiting = %+v, want cause blocked", row.Waiting)
	}
	if row.Waiting.Hint != "relay answer --name "+b.Name {
		t.Errorf("Hint = %q", row.Waiting.Hint)
	}
	b.State = store.StateActive
	row, _ = statusRow(context.Background(), rt, b, nil, nil)
	if row.Waiting != nil {
		t.Errorf("active row Waiting = %+v, want nil", row.Waiting)
	}
}

func TestDoneStopsRelaying(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)

	if _, err := Done(context.Background(), rt, b.Name); err != nil {
		t.Fatalf("Done: %v", err)
	}

	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done", got.State)
	}
}

func TestDoneReleasesCleanWorktree(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{dirtyResult: false}
	rt.Git = fg

	b.Worktree = t.TempDir()
	b.Branch = "relay/webshop"
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeRemoved != b.Worktree {
		t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, b.Worktree)
	}
	if res.Branch != "relay/webshop" {
		t.Errorf("Branch = %q, want relay/webshop", res.Branch)
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
	}
	call := fg.removeWorktreeCalls[0]
	if call.Dir != b.CWD || call.Path != b.Worktree || call.Force != false {
		t.Errorf("RemoveWorktreeCall = %+v, want Dir=%q, Path=%q, Force=false", call, b.CWD, b.Worktree)
	}
	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done", got.State)
	}
}

func TestDoneKeepsDirtyWorktree(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{dirtyResult: true}
	rt.Git = fg

	b.Worktree = t.TempDir()
	b.Branch = "relay/webshop"
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeKept != b.Worktree {
		t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, b.Worktree)
	}
	if res.KeptReason != "uncommitted changes" {
		t.Errorf("KeptReason = %q, want 'uncommitted changes'", res.KeptReason)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree calls = %d, want 0", len(fg.removeWorktreeCalls))
	}
}

func TestDoneKeepsWorktreeWhileRoundOpen(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{}
	rt.Git = fg

	b.Worktree = t.TempDir()
	b.Branch = "relay/webshop"
	b.RoundStartedAt = rt.Now()
	b.Round = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeKept != b.Worktree {
		t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, b.Worktree)
	}
	wantReason := "round 1 open; the builder may still write"
	if res.KeptReason != wantReason {
		t.Errorf("KeptReason = %q, want %q", res.KeptReason, wantReason)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree calls = %d, want 0", len(fg.removeWorktreeCalls))
	}
}

func TestDoneNoWorktreeIsZeroResult(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{}
	rt.Git = fg

	b.Worktree = ""
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	want := DoneResult{Branch: b.Branch}
	if res != want {
		t.Errorf("res = %+v, want %+v", res, want)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
}

func TestDoneReportsGoneWorktree(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{}
	rt.Git = fg

	b.Worktree = filepath.Join(t.TempDir(), "missing")
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeGone != b.Worktree {
		t.Errorf("WorktreeGone = %q, want %q", res.WorktreeGone, b.Worktree)
	}
}

func TestStatusJSONForkProvenance(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	// 1. Ordinary binding: forked_from and forked_at_round must be omitted from JSON
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	humanBefore := RenderStatus(rep)

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	row := decoded["bindings"].([]any)[0].(map[string]any)
	if _, ok := row["forked_from"]; ok {
		t.Errorf("ordinary binding must omit forked_from: %+v", row)
	}
	if _, ok := row["forked_at_round"]; ok {
		t.Errorf("ordinary binding must omit forked_at_round: %+v", row)
	}

	// 2. Forked binding: forked_from and forked_at_round must be present in JSON
	b.ForkedFrom = "source"
	b.ForkedAtRound = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	repFork, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status fork: %v", err)
	}
	humanAfter := RenderStatus(repFork)

	// RenderStatus human output must be byte-identical
	if humanBefore != humanAfter {
		t.Errorf("RenderStatus human output changed for fork:\nbefore:\n%s\nafter:\n%s", humanBefore, humanAfter)
	}

	rawFork, err := json.Marshal(repFork)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decodedFork map[string]any
	if err := json.Unmarshal(rawFork, &decodedFork); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	rowFork := decodedFork["bindings"].([]any)[0].(map[string]any)
	if got, ok := rowFork["forked_from"].(string); !ok || got != "source" {
		t.Errorf("forked_from = %v, want 'source'", rowFork["forked_from"])
	}
	if got, ok := rowFork["forked_at_round"].(float64); !ok || got != 2 {
		t.Errorf("forked_at_round = %v, want 2", rowFork["forked_at_round"])
	}
}

func TestStatusDetailsBrokenBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateBroken
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The builder is gone; only the planner is live.
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]

	if got.Display != "NEEDS YOU" {
		t.Errorf("display = %q, want NEEDS YOU (the collapse must not change)", got.Display)
	}
	want := DiagnoseBuilder(b).Detail(b.Round)
	if got.Detail != want {
		t.Errorf("detail =\n  %q\nwant\n  %q", got.Detail, want)
	}
	// sentBinding names the builder, so it is identified; no moved-pane warning.
	if strings.Contains(got.Detail, "moved pane") {
		t.Errorf("detail must not warn about a moved pane when named, got %q", got.Detail)
	}

	// Sibling case: an adopted pane (nameless and session-less) still gets the warning.
	b.Builder.AgentName = ""
	b.Builder.SessionID = ""
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	repAdopted, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	gotAdopted := repAdopted.Bindings[0]
	wantAdopted := DiagnoseBuilder(b).Detail(b.Round)
	if gotAdopted.Detail != wantAdopted {
		t.Errorf("adopted detail =\n  %q\nwant\n  %q", gotAdopted.Detail, wantAdopted)
	}
	if !strings.Contains(gotAdopted.Detail, "moved pane") {
		t.Errorf("adopted detail must warn about a moved pane, got %q", gotAdopted.Detail)
	}
}

func TestStatusOmitsDetailForHealthyBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.Detail != "" {
		t.Errorf("detail = %q, want empty for a healthy binding", got.Detail)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "detail") {
		t.Errorf("detail must be omitempty, got %s", raw)
	}
}

// orphaned also collapses into NEEDS YOU but is not overloaded, so it gets no
// detail. This pins the scope decision.
func TestStatusOmitsDetailForOrphanedBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateOrphaned
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = nil

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if d := rep.Bindings[0].Detail; d != "" {
		t.Errorf("detail = %q, want empty for orphaned", d)
	}
}

func TestRenderStatusShowsDetailLine(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "doctor", CWD: "/repo", Workspace: "wM", Round: 3,
		Display:          "NEEDS YOU",
		BuilderCandidate: testAgyRef,
		PlannerPane:      "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
		BuilderPane: "wM:pV", BuilderKind: "agy", BuilderStatus: "gone",
		Detail: "round 2 report delivered; nothing outstanding -- unless you want another round",
	}}})

	if !strings.Contains(out, "  detail   round 2 report delivered") {
		t.Errorf("detail line missing from:\n%s", out)
	}
	// It must sit between the builder line and pending, where a human about to
	// rebind is already looking.
	builderAt := strings.Index(out, "  builder ")
	detailAt := strings.Index(out, "  detail ")
	pendingAt := strings.Index(out, "  pending ")
	if !(builderAt < detailAt && detailAt < pendingAt) {
		t.Errorf("detail must follow builder and precede pending, got:\n%s", out)
	}
}

func TestRenderStatusOmitsEmptyDetail(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "ok", CWD: "/repo", Round: 1, Display: "ACTIVE",
		PlannerPane: "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
		BuilderPane: "wM:p2", BuilderKind: "agy", BuilderStatus: "working",
		BuilderCandidate: testAgyRef,
	}}})

	if strings.Contains(out, "detail") {
		t.Errorf("no detail line may appear for a healthy binding:\n%s", out)
	}
}

func TestStatusReportsForeignAgentInBoundTree(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	stranger := herdr.Agent{
		PaneID: "w9:p9", Kind: "claude", Status: herdr.StatusIdle,
		CWD: b.CWD, Title: "plan-executor",
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking), stranger}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0].Foreign
	if len(got) != 1 {
		t.Fatalf("got %d foreign agents, want 1: %+v", len(got), got)
	}
	if got[0].PaneID != "w9:p9" || got[0].Title != "plan-executor" {
		t.Errorf("foreign = %+v", got[0])
	}
}

func TestStatusReportsNoForeignAgentsForHealthyBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].Foreign; got != nil {
		t.Errorf("foreign = %+v, want nil", got)
	}
}

func TestStatusForeignDoesNotChangeDisplay(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		builderAgent(herdr.StatusWorking),
		{PaneID: "w9:p9", Kind: "claude", Status: herdr.StatusIdle, CWD: b.CWD},
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].Display != "ACTIVE" {
		t.Errorf("display = %q, want ACTIVE: a foreign agent is an observation, not a state",
			rep.Bindings[0].Display)
	}
}

func TestStatusJSONOmitsForeignWhenEmpty(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "foreign") {
		t.Errorf("empty Foreign must be omitted from JSON, got %s", raw)
	}
}

func TestRenderStatusShowsForeignLine(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "claude", Status: "idle", CWD: "/repo", Title: "plan-executor"},
		},
	}}})
	if !strings.Contains(out, "foreign") {
		t.Errorf("missing foreign line:\n%s", out)
	}
	if !strings.Contains(out, "w9:p9") || !strings.Contains(out, "plan-executor") {
		t.Errorf("foreign line missing pane or title:\n%s", out)
	}
}

func TestRenderStatusOmitsForeignLineWhenNone(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
	}}})
	if strings.Contains(out, "  foreign ") {
		t.Errorf("unexpected foreign line:\n%s", out)
	}
}

func TestRenderStatusForeignShowsRelativeCWDWhenNested(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "opencode", Status: "working", CWD: "/repo/internal/ui", Title: "researcher"},
		},
	}}})
	if !strings.Contains(out, "internal/ui") {
		t.Errorf("nested foreign agent must show its location:\n%s", out)
	}
	if strings.Contains(out, "/repo/internal/ui") {
		t.Errorf("location must be relative to the binding cwd, not absolute:\n%s", out)
	}
}

func TestRenderStatusForeignOmitsCWDAtTreeRoot(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "claude", Status: "idle", CWD: "/repo", Title: "plan-executor"},
		},
	}}})
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "  foreign ") {
			continue
		}
		if strings.Contains(line, "/repo") {
			t.Errorf("an agent at the tree root must not repeat the cwd: %q", line)
		}
	}
}

func TestRenderStatusShowsEveryForeignAgent(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p1", Kind: "claude", Status: "idle", CWD: "/repo"},
			{PaneID: "w9:p2", Kind: "agy", Status: "working", CWD: "/repo"},
		},
	}}})
	if n := strings.Count(out, "  foreign "); n != 2 {
		t.Errorf("got %d foreign lines, want 2:\n%s", n, out)
	}
}

func TestStatusCountsOnlyRunningConsults(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Consults = []store.Consult{
		{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultRunning},
		{ID: "bbbbbbbb", Role: "reviewer", State: store.ConsultRunning},
		{ID: "cccccccc", Role: "reviewer", State: store.ConsultDone},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings", len(rep.Bindings))
	}
	if rep.Bindings[0].Consults != 2 {
		t.Errorf("Consults = %d, want 2 running (the done one awaits reap)", rep.Bindings[0].Consults)
	}
}

func TestRenderStatusOmitsTheConsultCountWhenZero(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Consults: 0,
	}}}

	if out := RenderStatus(r); strings.Contains(out, "+0c") {
		t.Errorf("rendered a zero consult count:\n%s", out)
	}
}

func TestRenderStatusShowsSwitches(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Switches: 1,
	}}}

	if out := RenderStatus(r); !strings.Contains(out, "switched 1x") {
		t.Errorf("RenderStatus output missing switched 1x:\n%s", out)
	}
}

func TestRenderStatusOmitsSwitchedWhenZero(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Switches: 0,
	}}}

	if out := RenderStatus(r); strings.Contains(out, "switched") {
		t.Errorf("rendered a zero switch count:\n%s", out)
	}
}

func TestHideDoneRemovesOnlyDoneRows(t *testing.T) {
	in := Report{
		Bindings: []BindingStatus{
			{Name: "first", State: string(store.StateActive)},
			{Name: "second", State: string(store.StateDone)},
			{Name: "third", State: string(store.StateBroken)},
		},
	}

	got := HideDone(in)

	if len(in.Bindings) != 3 {
		t.Fatalf("HideDone modified input report: len = %d, want 3", len(in.Bindings))
	}
	if got.DoneHidden != 1 {
		t.Errorf("DoneHidden = %d, want 1", got.DoneHidden)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("got %d bindings, want 2", len(got.Bindings))
	}
	if got.Bindings[0].Name != "first" || got.Bindings[1].Name != "third" {
		t.Errorf("bindings = %+v, want first and third in original order", got.Bindings)
	}
}

func TestRenderStatusFooterCountsHidden(t *testing.T) {
	cases := []struct {
		hidden    int
		wantSub   string
		wantNoSub string
	}{
		{hidden: 3, wantSub: "3 done · relay gc to clear"},
		{hidden: 1, wantSub: "1 done · relay gc to clear"},
		{hidden: 0, wantNoSub: "done ·"},
	}

	for _, tc := range cases {
		r := Report{
			Bindings: []BindingStatus{
				{Name: "live", CWD: "/repo", Workspace: "w1", Round: 1, Display: "ACTIVE"},
			},
			DoneHidden: tc.hidden,
		}
		out := RenderStatus(r)
		if tc.wantSub != "" && !strings.Contains(out, tc.wantSub) {
			t.Errorf("hidden=%d: RenderStatus output missing %q:\n%s", tc.hidden, tc.wantSub, out)
		}
		if tc.wantNoSub != "" && strings.Contains(out, tc.wantNoSub) {
			t.Errorf("hidden=%d: RenderStatus output should not contain %q:\n%s", tc.hidden, tc.wantNoSub, out)
		}
	}
}

func TestRenderStatusFooterOnlyWhenEverythingIsDone(t *testing.T) {
	r := Report{
		Bindings:   nil,
		DoneHidden: 2,
	}
	out := RenderStatus(r)
	if !strings.Contains(out, "2 done · relay gc to clear") {
		t.Errorf("RenderStatus output missing footer:\n%s", out)
	}
	if strings.Contains(out, "no bindings") {
		t.Errorf("RenderStatus output should not contain 'no bindings':\n%s", out)
	}
}

func TestStatusShowsWorkingUnderASubAgentSession(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBindingWithBuilderSession(t, f, "parent-session")

	// Builder agent with same name and pane, but different session and StatusDone.
	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		{
			Name:    "webshop-builder",
			Kind:    "agy",
			Status:  herdr.StatusDone,
			CWD:     b.CWD,
			PaneID:  b.Builder.PaneID,
			Session: herdr.Session{Value: "child-session"},
		},
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(rep.Bindings))
	}
	got := rep.Bindings[0]
	if got.BuilderStatus != "working" {
		t.Errorf("BuilderStatus = %q, want working", got.BuilderStatus)
	}
	if len(got.Foreign) != 0 {
		t.Errorf("Foreign = %+v, want empty", got.Foreign)
	}
}

func TestRenderStatusGatedBlock(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	r := Report{
		Bindings: []BindingStatus{{
			Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
			PlannerPane: "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
			BuilderPane: "wM:p2", BuilderKind: "agy", BuilderStatus: "working",
			BuilderCandidate: testAgyRef,
		}},
		Gated: []ledger.Gate{
			{Token: testAgyRef, Kind: ledger.SpawnFailed, Since: now, Until: now.Add(10 * time.Minute)},
			{Token: testClaudeRef, Kind: ledger.RateLimited, Since: now, Until: time.Time{}},
		},
	}

	out := RenderStatus(r)
	if !strings.Contains(out, "candidates\n") {
		t.Fatalf("missing candidates block:\n%s", out)
	}
	rest := out[strings.Index(out, "candidates\n")+len("candidates\n"):]
	lines := strings.SplitN(rest, "\n", 3)
	if len(lines) < 2 {
		t.Fatalf("expected two gate rows, got:\n%s", rest)
	}
	wantUntil := "until " + now.Add(10*time.Minute).Local().Format("15:04")
	if !strings.Contains(lines[0], testAgyRef) || !strings.Contains(lines[0], "spawn failed") || !strings.Contains(lines[0], wantUntil) {
		t.Errorf("row 0 = %q", lines[0])
	}
	if !strings.Contains(lines[1], testClaudeRef) || !strings.Contains(lines[1], "rate-limited") || !strings.Contains(lines[1], "until cleared") {
		t.Errorf("row 1 = %q", lines[1])
	}
}

func TestRenderStatusNoGatesIsUnchanged(t *testing.T) {
	r := Report{
		Bindings: []BindingStatus{{
			Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
			PlannerPane: "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
			BuilderPane: "wM:p2", BuilderKind: "agy", BuilderStatus: "working",
			BuilderCandidate: testAgyRef,
		}},
		Gated: nil,
	}

	out := RenderStatus(r)
	if strings.Contains(out, "candidates") {
		t.Errorf("no gates must render no candidates block:\n%s", out)
	}
}

func TestRenderStatusGatesWithNoBindings(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	r := Report{
		Gated: []ledger.Gate{
			{Token: testAgyRef, Kind: ledger.SpawnFailed, Since: now, Until: now.Add(10 * time.Minute)},
		},
	}

	out := RenderStatus(r)
	if !strings.HasPrefix(out, "no bindings\ncandidates\n") {
		t.Errorf("out = %q, want it to start with %q", out, "no bindings\ncandidates\n")
	}
}

func TestStatusPopulatesGated(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	if _, err := Unavailable(rt, testAgyRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Gated) == 0 {
		t.Fatalf("rep.Gated = %+v, want non-empty", rep.Gated)
	}

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"gated"`) {
		t.Errorf("expected \"gated\" key present, got %s", raw)
	}

	rawEmpty, err := json.Marshal(Report{})
	if err != nil {
		t.Fatalf("Marshal empty: %v", err)
	}
	if strings.Contains(string(rawEmpty), "gated") {
		t.Errorf("empty report must omit gated, got %s", rawEmpty)
	}
}

// TestHideDoneKeepsGated pins that hiding DONE bindings does not drop the
// machine-wide gated block: cmdStatus applies HideDone between Status and
// RenderStatus, so a gate lost here never reaches the terminal.
func TestHideDoneKeepsGated(t *testing.T) {
	in := Report{
		Bindings: []BindingStatus{{Name: "old", State: string(store.StateDone)}},
		Gated:    []ledger.Gate{{Token: "agy/test/m", Kind: ledger.RateLimited}},
	}
	out := HideDone(in)
	if out.DoneHidden != 1 || len(out.Bindings) != 0 {
		t.Fatalf("HideDone = %+v, want one hidden and no rows", out)
	}
	if len(out.Gated) != 1 || out.Gated[0].Token != "agy/test/m" {
		t.Fatalf("Gated = %+v, want the gate carried through", out.Gated)
	}
}

func TestRenderStatusCoverageRowPerKind(t *testing.T) {
	base := func(kind, vis string) BindingStatus {
		return BindingStatus{
			Name: "b", CWD: "/repo", Round: 1, Display: "ACTIVE",
			PlannerPane: "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
			BuilderPane: "wM:p2", BuilderKind: kind, BuilderStatus: "working",
			BuilderCandidate: testAgyRef,
			SubAgents:        vis,
			Foreign: []ForeignAgent{{
				PaneID: "wM:p9", Kind: "claude", Status: "working", CWD: "/repo", Title: "researcher",
			}},
			Detail: "something to say",
		}
	}

	t.Run("claude prints no coverage row", func(t *testing.T) {
		out := RenderStatus(Report{Bindings: []BindingStatus{base("claude", "separate")}})
		if strings.Contains(out, "coverage") {
			t.Errorf("claude binding must not print a coverage row:\n%s", out)
		}
	})

	for _, tc := range []struct{ kind, vis, want string }{
		{"agy", "foreground", "  coverage sub-agents hidden: agy runs them in the builder pane; no foreign rows above does not mean the tree is clear\n"},
		{"opencode", "hidden", "  coverage sub-agents hidden: opencode runs them in-process, herdr lists only the pane; no foreign rows above does not mean the tree is clear\n"},
		{"gemini", "", "  coverage sub-agents unverified for kind \"gemini\"; no foreign rows above does not mean the tree is clear\n"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			out := RenderStatus(Report{Bindings: []BindingStatus{base(tc.kind, tc.vis)}})
			if !strings.Contains(out, tc.want) {
				t.Fatalf("coverage row missing or reworded; want %q in:\n%s", tc.want, out)
			}
			// It sits after the last foreign row and before detail: the
			// reader has just scanned the foreign rows and this is the
			// sentence about what that scan proved.
			foreignAt := strings.Index(out, "  foreign ")
			coverageAt := strings.Index(out, "  coverage ")
			detailAt := strings.Index(out, "  detail ")
			if !(foreignAt < coverageAt && coverageAt < detailAt) {
				t.Errorf("coverage must follow foreign and precede detail, got:\n%s", out)
			}
		})
	}
}

func TestStatusRowCopiesHarnessSubAgents(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{"claude", "separate"},
		{"agy", "foreground"},
		{"opencode", "hidden"},
		{"gemini", ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := &fakeHerdr{}
			rt, b := sentBinding(t, f)
			b.Builder.Kind = tc.kind
			if err := rt.Store.Save(b); err != nil {
				t.Fatal(err)
			}
			rep, err := Status(context.Background(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if got := rep.Bindings[0].SubAgents; got != tc.want {
				t.Errorf("SubAgents = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStatusLastPayloadSkipsBookkeepingKinds pins that LastPayload walks the
// log for the last plan/report/question/answer entry, skipping relay's own
// bookkeeping kinds (drift, diff, ...) that Last does not skip.
func TestStatusLastPayloadSkipsBookkeepingKinds(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	// Log now reads: plan (from sentBinding), then drift.
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindDrift,
	}); err != nil {
		t.Fatalf("AppendLog drift: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.Last == nil || got.Last.Kind != store.KindDrift {
		t.Fatalf("Last = %+v, want KindDrift", got.Last)
	}
	if got.LastPayload == nil || got.LastPayload.Kind != store.KindPlan {
		t.Fatalf("LastPayload = %+v, want KindPlan", got.LastPayload)
	}

	// Log now reads: plan, drift, diff, report (with a note).
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindDiff,
	}); err != nil {
		t.Fatalf("AppendLog diff: %v", err)
	}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindReport, Note: "unmarked",
	}); err != nil {
		t.Fatalf("AppendLog report: %v", err)
	}

	rep, err = Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got = rep.Bindings[0]
	if got.Last == nil || got.Last.Kind != store.KindReport {
		t.Fatalf("Last = %+v, want KindReport", got.Last)
	}
	if got.LastPayload == nil || got.LastPayload.Kind != store.KindReport {
		t.Fatalf("LastPayload = %+v, want KindReport", got.LastPayload)
	}
	if got.LastPayload.Note != "unmarked" {
		t.Errorf("LastPayload.Note = %q, want %q", got.LastPayload.Note, "unmarked")
	}

	// A fresh binding with no Send has no log entries at all.
	f2 := &fakeHerdr{}
	rt2, _ := seedBound(t, f2)
	f2.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep2, err := Status(context.Background(), rt2)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	// NOTE (deviation from the plan text): the plan says this binding's Last
	// should also be nil. It is not: seedBound's Bind call passes an explicit
	// Candidate, and create() (bind.go) unconditionally logs a KindPick entry
	// for HowExplicit resolutions, regardless of Send. That logging is
	// existing, out-of-scope behaviour this plan does not touch. LastPayload
	// is unaffected -- KindPick is not a payload kind -- so the assertion
	// that matters to this plan (LastPayload nil on an unsent binding) still
	// holds and is checked below.
	got2 := rep2.Bindings[0]
	if got2.Last == nil || got2.Last.Kind != store.KindPick {
		t.Errorf("Last = %+v, want the KindPick entry Bind logs for an explicit candidate", got2.Last)
	}
	if got2.LastPayload != nil {
		t.Errorf("LastPayload = %+v, want nil", got2.LastPayload)
	}
}

func TestBindingStatusSubAgentsJSON(t *testing.T) {
	withValue, _ := json.Marshal(BindingStatus{Name: "b", SubAgents: "hidden"})
	if !strings.Contains(string(withValue), `"sub_agents":"hidden"`) {
		t.Errorf("sub_agents missing from %s", withValue)
	}
	empty, _ := json.Marshal(BindingStatus{Name: "b"})
	if strings.Contains(string(empty), "sub_agents") {
		t.Errorf("empty SubAgents must be omitted, got %s", empty)
	}
}

// seedClosedRound appends a diff entry for round 1 with the given facts and
// returns the binding as the daemon leaves it after queueReport: round 2,
// not yet sent (RoundStartedAt zero).
func seedClosedRound(t *testing.T, tree string, commits int) (Runtime, store.Binding) {
	t.Helper()
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: 1, Direction: store.DirToPlanner, Kind: store.KindDiff, Confirmed: true,
		Commits: commits, Tree: tree,
	}); err != nil {
		t.Fatalf("AppendLog diff: %v", err)
	}
	b.Round = 2
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return rt, b
}

func TestStatusLastCloseAndDirty(t *testing.T) {
	t.Run("dirty close, next round not sent", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "dirty", 0)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Round != 1 || got.LastClose.Tree != "dirty" || got.LastClose.Commits != 0 {
			t.Fatalf("LastClose = %+v, want round 1, dirty, 0 commits", got.LastClose)
		}
		if !got.Dirty {
			t.Error("Dirty = false, want true")
		}
	})

	t.Run("clean close", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "clean", 3)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "clean" || got.LastClose.Commits != 3 {
			t.Fatalf("LastClose = %+v, want clean, 3 commits", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true, want false")
		}
	})

	t.Run("dirty close but the next round is sent", func(t *testing.T) {
		rt, b := seedClosedRound(t, "dirty", 0)
		b.RoundStartedAt = time.Now().UTC()
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "dirty" {
			t.Fatalf("LastClose = %+v, want dirty", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true while a round is running, want false")
		}
	})

	t.Run("pre-field diff entry", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "", 0)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "" {
			t.Fatalf("LastClose = %+v, want present with unknown tree", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true for an unknown tree, want false")
		}
	})

	t.Run("no diff entry", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, _ := sentBinding(t, f)
		f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got := rep.Bindings[0]; got.LastClose != nil || got.Dirty {
			t.Errorf("LastClose = %+v, Dirty = %v; want nil, false", got.LastClose, got.Dirty)
		}
	})
}

func TestRenderStatusShowsDirty(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2, Dirty: true, Consults: 1,
	}}}
	out := RenderStatus(r)
	if !strings.Contains(out, "round 2   ACTIVE dirty +1c") {
		t.Errorf("RenderStatus output missing 'dirty' after the display word:\n%s", out)
	}
}

func TestRenderStatusOmitsDirtyWhenClean(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2,
		LastClose: &CloseInfo{Round: 1, Tree: "dirty"}, // the raw fact, without the rule applied
	}}}
	if out := RenderStatus(r); strings.Contains(out, "dirty") {
		t.Errorf("rendered dirty from LastClose instead of Dirty:\n%s", out)
	}
}

func seedUsageLog(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedClosedRound(t, "clean", 1)
	mk := func(usd float64, basis usage.Basis) *usage.Usage {
		return &usage.Usage{Harness: "agy", Provider: "test", Model: "m", DurationMS: 60_000,
			Tokens: usage.Tokens{In: 100, Out: 10}, Cost: usage.Cost{USD: usd, Basis: basis}, Samples: 1}
	}
	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "r1", Confirmed: true, Usage: mk(0.10, usage.Measured)},
		{Round: 1, Direction: store.DirToPlanner, Kind: store.KindFindings, Payload: "f1", Confirmed: true, Usage: mk(0.02, usage.Estimated)},
		{Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "r2", Confirmed: true, Usage: mk(0.30, usage.Measured)},
	}
	for _, e := range entries {
		if err := rt.Store.AppendLog(b.Name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return rt, b
}

func TestStatusLastUsageAndSpend(t *testing.T) {
	rt, _ := seedUsageLog(t)
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.LastUsage == nil || got.LastUsage.Cost.USD != 0.30 {
		t.Fatalf("LastUsage = %+v, want the round-2 report's", got.LastUsage)
	}
	if got.Spend == nil {
		t.Fatal("Spend = nil")
	}
	if got.Spend.Rounds != 2 || got.Spend.Consults != 1 {
		t.Errorf("rounds/consults = %d/%d, want 2/1", got.Spend.Rounds, got.Spend.Consults)
	}
	if got.Spend.Measured < 0.399 || got.Spend.Measured > 0.401 || got.Spend.Estimated != 0.02 {
		t.Errorf("measured/estimated = %v/%v, want 0.40/0.02", got.Spend.Measured, got.Spend.Estimated)
	}
	text := RenderStatus(rep)
	if !strings.Contains(text, "  usage    "+usage.Line(*got.LastUsage)) {
		t.Errorf("text lacks the usage row:\n%s", text)
	}
	if !strings.Contains(text, "  spend    2 rounds +1c · $0.40 · ~$0.02") {
		t.Errorf("text lacks the spend row:\n%s", text)
	}
}

func TestStatusNoUsageNoRows(t *testing.T) {
	rt, _ := seedClosedRound(t, "clean", 1)
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].LastUsage != nil || rep.Bindings[0].Spend != nil {
		t.Errorf("no usage entries: both must be nil, got %+v / %+v", rep.Bindings[0].LastUsage, rep.Bindings[0].Spend)
	}
	text := RenderStatus(rep)
	if strings.Contains(text, "  usage ") || strings.Contains(text, "  spend ") {
		t.Errorf("no rows without usage:\n%s", text)
	}
	raw, _ := json.Marshal(rep.Bindings[0])
	if strings.Contains(string(raw), "last_usage") || strings.Contains(string(raw), "spend") {
		t.Errorf("json must omit both when nil: %s", raw)
	}
}

func TestRenderStatusOutcome(t *testing.T) {
	t.Run("prints outcome when halted", func(t *testing.T) {
		ts := time.Date(2026, 9, 18, 15, 4, 5, 0, time.UTC)
		rep := Report{
			Bindings: []BindingStatus{
				{
					Name: "b1", Round: 1, State: "active", Display: "ACTIVE",
					Last: &LastEvent{
						TS: ts, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
						Note: "noreport", Outcome: "halted",
					},
				},
			},
		}
		text := RenderStatus(rep)
		want := "last     " + ts.Local().Format("15:04:05") + " report to_planner round 1 (noreport) halted\n"
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in status text:\n%s", want, text)
		}
	})

	t.Run("prints nothing for done outcome", func(t *testing.T) {
		ts := time.Date(2026, 9, 18, 15, 4, 5, 0, time.UTC)
		rep := Report{
			Bindings: []BindingStatus{
				{
					Name: "b1", Round: 1, State: "active", Display: "ACTIVE",
					Last: &LastEvent{
						TS: ts, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
						Outcome: "done",
					},
				},
			},
		}
		text := RenderStatus(rep)
		want := "last     " + ts.Local().Format("15:04:05") + " report to_planner round 1\n"
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in status text:\n%s", want, text)
		}
		if strings.Contains(text, "done") {
			t.Errorf("status text should not print 'done' outcome:\n%s", text)
		}
	})
}
