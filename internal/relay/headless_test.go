package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// seedHeadless binds webshop headless on the agy test candidate, with fr as
// the runtime's Runner. No herdr agent is added for the builder: there is
// none.
func seedHeadless(t *testing.T, f *fakeHerdr, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []herdr.Agent{plannerAgent()}
	rt := newRuntime(t, f)
	rt.Runner = fr
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	f.listCalls = 0
	return rt, b
}

func TestHandleOfConvertsUnixSeconds(t *testing.T) {
	h := handleOf(store.Endpoint{PID: 42, StartedAt: 1_789_000_000})
	if h.PID != 42 || !h.StartedAt.Equal(time.Unix(1_789_000_000, 0)) {
		t.Errorf("handleOf = %+v", h)
	}
	if z := handleOf(store.Endpoint{}); z.PID != 0 || !z.StartedAt.Equal(time.Unix(0, 0)) {
		t.Errorf("zero endpoint: %+v", z)
	}
}

func TestRoundBudgetIsTheBindingsRoundTimeout(t *testing.T) {
	if got := roundBudget(store.Binding{RoundTimeoutMS: 90 * 60 * 1000}); got != 90*time.Minute {
		t.Errorf("roundBudget = %s, want 1h30m", got)
	}
	// Store.Save fills RoundTimeoutMS, so zero is only ever a binding that was
	// never saved; the default matches the store's 24h.
	if got := roundBudget(store.Binding{}); got != 24*time.Hour {
		t.Errorf("roundBudget(zero) = %s, want 24h", got)
	}
}

func TestHeadlessLaunchPerKind(t *testing.T) {
	role, _ := harness.RoleByName("builder")
	set := candidateSet(t, testCandidatesJSON)
	lookup := func(token string) candidate.Candidate {
		ref, err := candidate.ParseRef(token)
		if err != nil {
			t.Fatal(err)
		}
		c, err := set.Lookup(ref)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		token string
		want  []string
	}{
		{testAgyRef, []string{"agy", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor",
			"--output-format", "stream-json", "--print-timeout", "2h0m0s", "--add-dir", "/repo", "--dangerously-skip-permissions"}},
		{testClaudeRef, []string{"claude", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"}},
		{testOpencodeRef, []string{"opencode", "run", "PROMPT", "-m", "test/m", "--agent", "plan-executor", "--format", "json"}},
	}
	for _, c := range cases {
		got, err := headlessLaunch(lookup(c.token), role, harness.TierHarness, 2*time.Hour, "PROMPT", "/repo", "/s")
		if err != nil {
			t.Fatalf("%s: %v", c.token, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.token, got, c.want)
		}
	}
	if _, err := headlessLaunch(candidate.Candidate{Harness: "nope"}, role, harness.TierHarness, time.Hour, "x", "/repo", "/s"); err == nil {
		t.Error("unknown harness kind must be an error, not a panic or an empty argv")
	}
}

func TestStartRoundRecordsTheHandleAndTheLogPath(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr)

	got, err := startRound(context.Background(), rt, b, "the prompt")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	spec := fr.specs[0]
	wantLog := rt.Store.BuilderLogPath("webshop", 1)
	if spec.Dir != "/repo" || spec.LogPath != wantLog {
		t.Errorf("spec Dir/LogPath = %q/%q, want /repo/%q", spec.Dir, spec.LogPath, wantLog)
	}
	if want := rt.Store.BuilderStreamPath("webshop", 1); spec.StreamPath != want {
		t.Errorf("spec StreamPath = %q, want %q", spec.StreamPath, want)
	}
	if got.Builder.StreamRound != 1 || got.Builder.StreamOffset != 0 {
		t.Errorf("cursor after a fresh start = round %d offset %d; want 1, 0", got.Builder.StreamRound, got.Builder.StreamOffset)
	}
	if spec.Argv[0] != "agy" || spec.Argv[1] != "-p" || spec.Argv[2] != "the prompt" {
		t.Errorf("argv = %v; want the agy print form with the prompt at index 2", spec.Argv)
	}
	if !containsArg(spec.Argv, "--print-timeout", "24h0m0s") {
		t.Errorf("argv %v lacks the default 24h budget", spec.Argv)
	}
	if !containsArg(spec.Argv, "--add-dir", "/repo") {
		t.Errorf("argv %v lacks --add-dir pinned to the binding's CWD (#192)", spec.Argv)
	}
	h := fr.handles[0]
	if got.Builder.PID != h.PID || got.Builder.StartedAt != h.StartedAt.Unix() || got.Builder.LogPath != wantLog {
		t.Errorf("endpoint after start = %+v, want pid %d started %d log %s", got.Builder, h.PID, h.StartedAt.Unix(), wantLog)
	}
	if !got.Builder.Headless() || got.Builder.PaneID != "" {
		t.Errorf("mode or pane changed: %+v", got.Builder)
	}
}

func TestStartRoundOnTheSameRoundKeepsTheCursor(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr)
	b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 512 // a switch mid-round: the file already has 512 bytes rendered
	got, err := startRound(context.Background(), rt, b, "again")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if got.Builder.StreamRound != b.Round || got.Builder.StreamOffset != 512 {
		t.Errorf("cursor = round %d offset %d; a same-round start must keep it at %d/512", got.Builder.StreamRound, got.Builder.StreamOffset, b.Round)
	}
}

func TestStartRoundOnALaterRoundMovesTheCursor(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr)
	b.Round = 2
	b.Builder.StreamRound, b.Builder.StreamOffset = 1, 512
	got, err := startRound(context.Background(), rt, b, "round two")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if got.Builder.StreamRound != 2 || got.Builder.StreamOffset != 0 {
		t.Errorf("cursor = round %d offset %d; want 2, 0", got.Builder.StreamRound, got.Builder.StreamOffset)
	}
	if fr.specs[0].StreamPath != rt.Store.BuilderStreamPath("webshop", 2) {
		t.Errorf("StreamPath = %q, want round 2's", fr.specs[0].StreamPath)
	}
}

func TestReconcileHeadlessExitReadsTheTrailerFromTheStream(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)
	if _, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.exitPaths) == 0 || fr.exitPaths[0] != rt.Store.BuilderStreamPath("webshop", 1) {
		t.Errorf("ExitCode was asked about %v; want the round-1 stream %s", fr.exitPaths, rt.Store.BuilderStreamPath("webshop", 1))
	}
}

// containsArg reports whether argv has flag immediately followed by value.
func containsArg(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func TestStartRoundFailureRecordsSpawnFailedAndLeavesPIDZero(t *testing.T) {
	fr := newFakeRunner()
	fr.startErr = errors.New("agy: not found on PATH")
	rt, b := seedHeadless(t, &fakeHerdr{}, fr)

	var got store.Binding
	err := rt.Store.WithLock(func(_ *store.Tx) error {
		var err error
		got, err = startRound(context.Background(), rt, b, "p")
		return err
	})
	if err == nil || !errors.Is(err, fr.startErr) {
		t.Fatalf("err = %v, want the Start error wrapped", err)
	}
	if got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("a failed start must leave the endpoint idle: %+v", got.Builder)
	}
	var gated bool
	for _, g := range Gates(rt) {
		if g.Token == testAgyRef && g.Kind == "spawn_failed" {
			gated = true
		}
	}
	if !gated {
		t.Errorf("spawn_failed must be in the ledger for %s: %+v", testAgyRef, Gates(rt))
	}
}

func TestStartRoundWithoutARunnerIsErrRunnerUnavailable(t *testing.T) {
	rt, b := seedHeadless(t, &fakeHerdr{}, newFakeRunner())
	rt.Runner = nil
	if _, err := startRound(context.Background(), rt, b, "p"); !errors.Is(err, ErrRunnerUnavailable) {
		t.Errorf("err = %v, want ErrRunnerUnavailable", err)
	}
}

func TestSendHeadlessStartsTheProcessInsteadOfPrompting(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, f, fr)

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# do the thing"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("round = %d, want 1", res.Round)
	}
	if len(f.prompts) != 0 {
		t.Fatalf("a headless send must not Prompt: %+v", f.prompts)
	}
	if f.listCalls != 0 {
		t.Errorf("a headless send has no agent to look up: listCalls = %d", f.listCalls)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	spec := fr.specs[0]
	planPath := rt.Store.PlanPath("webshop", 1)
	reportPath := rt.Store.ReportPath("webshop", 1)
	donePath := rt.Store.DonePath("webshop", 1)
	b, _ := rt.Store.Load("webshop")
	wantPrompt := composePrompt(b, planPath, reportPath, donePath)
	if spec.Argv[2] != wantPrompt {
		t.Errorf("prompt handed to the process:\n%q\nwant the pane path's composePrompt:\n%q", spec.Argv[2], wantPrompt)
	}
	if spec.Dir != "/repo" || spec.LogPath != rt.Store.BuilderLogPath("webshop", 1) {
		t.Errorf("spec = %+v", spec)
	}

	// The endpoint carries the handle; the round is stamped as today.
	if b.Builder.PID != fr.handles[0].PID || b.Builder.LogPath != spec.LogPath {
		t.Errorf("stored endpoint = %+v", b.Builder)
	}
	if b.RoundStartedAt.IsZero() || b.State != store.StateActive {
		t.Errorf("round not stamped: startedAt=%v state=%s", b.RoundStartedAt, b.State)
	}
	entries, _ := rt.Store.ReadLog("webshop")
	var plans int
	for _, e := range entries {
		if e.Kind == store.KindPlan && e.Round == 1 && e.Path == planPath {
			plans++
		}
	}
	if plans != 1 {
		t.Errorf("want exactly one plan entry for round 1, log = %+v", entries)
	}
}

func TestSendHeadlessRefusesWhileThePreviousProcessIsAlive(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, &fakeHerdr{}, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "round one"), SendOptions{}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	// Unscripted, the fake reports the process alive forever.
	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "round one again"), SendOptions{})
	if !errors.Is(err, ErrBuilderBusy) {
		t.Fatalf("second Send: err = %v, want ErrBuilderBusy", err)
	}
	if len(fr.specs) != 1 {
		t.Errorf("a refused send must start nothing: specs = %d", len(fr.specs))
	}
	plan, _ := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if string(plan) != "round one" {
		t.Errorf("a refused send must not restage the plan: %q", plan)
	}
}

func TestSendHeadlessStartsAgainOnceThePreviousProcessExited(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, &fakeHerdr{}, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "one"), SendOptions{}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	fr.script(fr.handles[0].PID, false) // exited between the two sends
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "one, corrected"), SendOptions{}); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if len(fr.specs) != 2 || len(fr.handles) != 2 || fr.handles[1].PID == fr.handles[0].PID {
		t.Fatalf("want a second, distinct process: specs=%d handles=%+v", len(fr.specs), fr.handles)
	}
	b, _ := rt.Store.Load("webshop")
	if b.Builder.PID != fr.handles[1].PID {
		t.Errorf("endpoint pid = %d, want the new process %d", b.Builder.PID, fr.handles[1].PID)
	}
}

func TestSendHeadlessStartFailureGoesNeedsYou(t *testing.T) {
	fr := newFakeRunner()
	fr.startErr = errors.New("agy: not found on PATH")
	rt, _ := seedHeadless(t, &fakeHerdr{}, fr)

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{})
	if err == nil || !errors.Is(err, fr.startErr) {
		t.Fatalf("err = %v, want the Start error wrapped", err)
	}
	b, _ := rt.Store.Load("webshop")
	if b.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", b.State)
	}
	if b.Builder.PID != 0 {
		t.Errorf("pid = %d, want 0 after a failed start", b.Builder.PID)
	}
	if !strings.HasPrefix(b.Halt, "builder spawn failed: ") {
		t.Errorf("Halt = %q, want prefix %q", b.Halt, "builder spawn failed: ")
	}
	if !strings.Contains(b.Halt, "not found on PATH") {
		t.Errorf("Halt = %q, want it to contain %q", b.Halt, "not found on PATH")
	}
	if b.HaltAt.IsZero() {
		t.Error("HaltAt is zero, want set")
	}
	entries, _ := rt.Store.ReadLog("webshop")
	for _, e := range entries {
		if e.Kind == store.KindPlan {
			t.Errorf("no plan entry may be logged for a round that never started: %+v", e)
		}
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", 1)); err != nil {
		t.Errorf("the plan stays staged so the human can retry: %v", err)
	}
}

// TestSendClearsAStaleHalt pins Send's success path: a binding that was
// halted for an earlier round must not carry that halt into a round it just
// successfully handed over.
func TestSendClearsAStaleHalt(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	b.Halt = "round 1 has run past 1s"
	b.HaltAt = rt.Now()
	b.State = store.StateNeedsYou
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want empty", got.Halt)
	}
	if !got.HaltAt.IsZero() {
		t.Errorf("HaltAt = %v, want zero", got.HaltAt)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active", got.State)
	}
}

func TestSendPanePathIsUntouchedByHeadless(t *testing.T) {
	// A pane binding with a Runner configured never touches it.
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, _ := seedBound(t, f)
	rt.Runner = fr
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("pane send started a process: %+v", fr.specs)
	}
	if len(f.prompts) != 1 {
		t.Errorf("pane send must still Prompt once: %d", len(f.prompts))
	}
}

func TestLogTailReturnsTheLastLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "003-builder.log")
	if err := os.WriteFile(p, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(p, 2); got != "c\nd" {
		t.Errorf("logTail(2) = %q, want \"c\\nd\"", got)
	}
	if got := logTail(p, 10); got != "a\nb\nc\nd" {
		t.Errorf("logTail(10) = %q, want the whole file without the trailing newline", got)
	}
	if got := logTail(filepath.Join(dir, "absent.log"), 3); got != "" {
		t.Errorf("logTail(absent) = %q, want empty", got)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(p, 3); got != "" {
		t.Errorf("logTail(empty) = %q, want empty", got)
	}
}

func TestClearProcessKeepsIdentity(t *testing.T) {
	e := store.Endpoint{AgentName: "x-builder", Kind: "agy", Mode: store.ModeHeadless, PID: 7, StartedAt: 9, LogPath: "/l", StreamRound: 3, StreamOffset: 99}
	got := clearProcess(e)
	want := store.Endpoint{AgentName: "x-builder", Kind: "agy", Mode: store.ModeHeadless, StreamRound: 3, StreamOffset: 99}
	if got != want {
		t.Errorf("clearProcess = %+v, want %+v", got, want)
	}
}

// streamWrite appends raw to webshop's round-1 stream file, creating it.
func streamWrite(t *testing.T, rt Runtime, raw string) {
	t.Helper()
	p := rt.Store.BuilderStreamPath("webshop", 1)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// readLog is webshop's round-1 builder log, "" when absent.
func readLog(t *testing.T, rt Runtime) string {
	t.Helper()
	data, err := os.ReadFile(rt.Store.BuilderLogPath("webshop", 1))
	if err != nil {
		return ""
	}
	return string(data)
}

const (
	agyToolActive = `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"go test ./..."}}}}` + "\n"
	agyToolDone   = `{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"run_command"}}` + "\n"
	agyResult     = `{"event":"result","result":{"status":"SUCCESS","response":"all done","denied_actions":[]}}` + "\n"
)

func TestDrainStreamRendersNewLinesInOrderAndAdvances(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr) // round 1 open, cursor at 1/0
	streamWrite(t, rt, agyToolActive+agyToolDone)

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "● run_command go test ./...\n  ⎿ ok\n"; readLog(t, rt) != want {
		t.Errorf("log = %q, want %q", readLog(t, rt), want)
	}
	if got.Builder.StreamOffset != int64(len(agyToolActive+agyToolDone)) {
		t.Errorf("offset = %d, want the whole file %d", got.Builder.StreamOffset, len(agyToolActive+agyToolDone))
	}

	// Nothing new: nothing appended.
	again, err := reconcile(t, rt, got, []herdr.Agent{plannerAgent()})
	if err != nil || readLog(t, rt) != "● run_command go test ./...\n  ⎿ ok\n" || again.Builder.StreamOffset != got.Builder.StreamOffset {
		t.Errorf("a tick with no new stream data must change nothing: log=%q offset=%d err=%v", readLog(t, rt), again.Builder.StreamOffset, err)
	}

	// More arrives: appended after, in order.
	streamWrite(t, rt, agyResult)
	got, err = reconcile(t, rt, again, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "● run_command go test ./...\n  ⎿ ok\nall done\n"; readLog(t, rt) != want {
		t.Errorf("log = %q, want %q", readLog(t, rt), want)
	}
}

func TestDrainStreamWaitsForAPartialLine(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	whole := strings.TrimSuffix(agyToolActive, "\n")
	streamWrite(t, rt, whole[:40]) // mid-event, no newline yet

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "" || got.Builder.StreamOffset != 0 {
		t.Errorf("a partial line must not be rendered or consumed: log=%q offset=%d", readLog(t, rt), got.Builder.StreamOffset)
	}
	streamWrite(t, rt, whole[40:]+"\n")
	got, err = reconcile(t, rt, got, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "● run_command go test ./...\n" || got.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Errorf("completed line: log=%q offset=%d", readLog(t, rt), got.Builder.StreamOffset)
	}
}

func TestDrainStreamCursorSurvivesAReload(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	streamWrite(t, rt, agyToolActive)
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.Save(got) }); err != nil {
		t.Fatal(err)
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Builder.StreamRound != 1 || loaded.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Fatalf("cursor after reload = %d/%d", loaded.Builder.StreamRound, loaded.Builder.StreamOffset)
	}
	// A daemon restarted from that state renders nothing twice.
	if _, err := reconcile(t, rt, loaded, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatal(err)
	}
	if readLog(t, rt) != "● run_command go test ./...\n" {
		t.Errorf("log after reload tick = %q; the line was rendered twice", readLog(t, rt))
	}
}

func TestDrainStreamCursorPastEndRendersFromTheStart(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	streamWrite(t, rt, agyToolActive)
	b.Builder.StreamOffset = 10_000 // a state file rewritten by hand
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "● run_command go test ./...\n" || got.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Errorf("log=%q offset=%d; want rendered from 0 and the cursor at EOF", readLog(t, rt), got.Builder.StreamOffset)
	}
}

func TestDrainStreamNoiseAdvancesWithoutWriting(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	noise := `{"event":"init","init":{}}` + "\n"
	streamWrite(t, rt, noise)
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, statErr := os.Stat(rt.Store.BuilderLogPath("webshop", 1)); statErr == nil {
		t.Error("all-noise input must not create the log")
	}
	if got.Builder.StreamOffset != int64(len(noise)) {
		t.Errorf("offset = %d, want %d", got.Builder.StreamOffset, len(noise))
	}
}

func TestReconcileHeadlessExitEntryCarriesTheRenderedResult(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	// stderr went straight to the log; stdout and the trailer to the stream,
	// all before this tick (the process is gone).
	if err := os.MkdirAll(filepath.Dir(b.Builder.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Builder.LogPath, []byte("jetski: starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	streamWrite(t, rt, agyToolActive+agyToolDone+agyResult+"\nrelay-exit:0\n")

	if _, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("exit entries = %d, want 1", len(ex))
	}
	want := "jetski: starting\n● run_command go test ./...\n  ⎿ ok\nall done\nrelay-exit:0"
	if ex[0].Payload != want {
		t.Errorf("payload = %q, want the drained log %q", ex[0].Payload, want)
	}
	if !strings.Contains(ex[0].Note, "(code 0)") {
		t.Errorf("note = %q; the code must still come from the trailer", ex[0].Note)
	}
}

func TestDrainStreamKeepsGoingAfterAMarkerClose(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	streamWrite(t, rt, agyToolActive)

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 {
		t.Fatalf("round=%d pid=%d; want the marker to close round 1", got.Round, got.Builder.PID)
	}
	if got.Builder.StreamRound != 1 {
		t.Fatalf("StreamRound = %d after the close; the cursor must stay on round 1's file", got.Builder.StreamRound)
	}
	// The builder flushes its result after relay saw the marker.
	streamWrite(t, rt, agyResult+"\nrelay-exit:0\n")
	if _, err := reconcile(t, rt, got, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "● run_command go test ./...\nall done\nrelay-exit:0\n"; readLog(t, rt) != want {
		t.Errorf("round 1 log after the close = %q, want %q", readLog(t, rt), want)
	}
}

func TestDrainStreamIsANoopBeforeAnyRound(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr) // bound, never sent: StreamRound 0
	got := drainStream(rt, b)
	if !reflect.DeepEqual(got, b) {
		t.Errorf("drainStream changed a binding with no stream: %+v", got.Builder)
	}
}

func TestDrainStreamDoesNotAdvanceOnAWriteFailure(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	if err := os.MkdirAll(rt.Store.BuilderLogPath("webshop", 1), 0o755); err != nil {
		t.Fatal(err)
	}
	streamWrite(t, rt, agyToolActive)
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Builder.StreamOffset != 0 {
		t.Errorf("StreamOffset = %d on write failure; want 0 (cursor must not advance)", got.Builder.StreamOffset)
	}
}

// sentHeadless is seedHeadless plus one Send: round 1 open, one process
// started on fr.
func sentHeadless(t *testing.T, f *fakeHerdr, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := seedHeadless(t, f, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// switchHeadless runs switchBuilder on b inside the lock, the way Reconcile does.
func switchHeadless(t *testing.T, rt Runtime, b store.Binding, reason string, closeOld bool) (store.Binding, error) {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = switchBuilder(context.Background(), rt, tx, b, reason, closeOld, true)
		return err
	})
	return out, err
}

func TestSwitchBuilderHeadlessStartsAProcessNotAPane(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	// Order claude first so the switch lands on a different candidate.
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	oldPID := b.Builder.PID

	got, err := switchHeadless(t, rt, b, "exited (code 3) without a report", false)
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 || len(f.prompts) != 0 || len(f.closed) != 0 {
		t.Fatalf("a headless switch must touch no pane: tabs=%d starts=%d prompts=%d closed=%d", len(f.tabs), len(f.starts), len(f.prompts), len(f.closed))
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a second Start on claude, specs = %+v", fr.specs)
	}
	if !got.Builder.Headless() || got.Builder.PID != fr.handles[1].PID || got.Builder.PID == oldPID {
		t.Errorf("new endpoint = %+v, want headless with the new pid %d", got.Builder, fr.handles[1].PID)
	}
	if got.Builder.LogPath != rt.Store.BuilderLogPath("webshop", 1) {
		t.Errorf("LogPath = %q, want round 1's log", got.Builder.LogPath)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 1 || got.Round != 1 || got.State != store.StateActive {
		t.Errorf("bookkeeping: cand=%q switches=%d round=%d state=%s", got.BuilderCandidate, got.RoundSwitches, got.Round, got.State)
	}
	if len(fr.kills) != 0 {
		t.Errorf("closeOld=false must not kill: %+v", fr.kills)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "switched builder to "+testClaudeRef) {
		t.Errorf("notices = %+v", f.notices)
	}
	// The prompt handed to the new process is the same round's prompt.
	if !strings.Contains(fr.specs[1].Argv[2], rt.Store.PlanPath("webshop", 1)) {
		t.Errorf("new process prompt lacks the round's plan path: %q", fr.specs[1].Argv[2])
	}
}

func TestSwitchBuilderHeadlessCloseOldKillsTheProcess(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	old := handleOf(b.Builder)

	if _, err := switchHeadless(t, rt, b, "rate-limited", true); err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != old {
		t.Errorf("kills = %+v, want the old handle %+v", fr.kills, old)
	}
	if len(f.closed) != 0 {
		t.Errorf("no pane to close: %+v", f.closed)
	}
}

func TestSwitchBuilderHeadlessStartFailureHalts(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.startErr = errors.New("claude: not found")

	got, err := switchHeadless(t, rt, b, "exited", false)
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you when the replacement cannot start", got.State)
	}
	if got.Builder.PID != 0 {
		t.Errorf("pid = %d, want 0", got.Builder.PID)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "could not start") {
		t.Errorf("notices = %+v, want one saying the round could not be started", f.notices)
	}
}

func TestSwitchBuilderHeadlessMarksTheLog(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)

	logPath := rt.Store.BuilderLogPath("webshop", b.Round)
	if data, err := os.ReadFile(logPath); err == nil {
		if strings.Contains(string(data), "--- relay") {
			t.Fatalf("log already contains relay marker before switch: %s", string(data))
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error reading log before switch: %v", err)
	}

	if _, err := switchHeadless(t, rt, b, "rate-limited", true); err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantSuffix := ": switched to " + testClaudeRef + " (rate-limited) ---"
	if !strings.HasSuffix(lines[len(lines)-1], wantSuffix) {
		t.Errorf("last line = %q, want suffix %q", lines[len(lines)-1], wantSuffix)
	}
	if !strings.HasPrefix(lines[len(lines)-1], "--- relay ") {
		t.Errorf("last line = %q, want prefix --- relay ", lines[len(lines)-1])
	}
}

func TestSwitchBuilderPaneWritesNoLog(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)

	got, err := reconcile(t, rt, b, gone())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, err = reconcile(t, at(rt, 31*time.Second), got, gone())
	if err != nil {
		t.Fatalf("Reconcile at +31s: %v", err)
	}
	if got.RoundSwitches != 1 {
		t.Fatalf("RoundSwitches = %d, want 1", got.RoundSwitches)
	}
	logPath := rt.Store.BuilderLogPath(got.Name, got.Round)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("pane switch created a log file: %s", logPath)
	}
}

func TestSwitchBuilderHeadlessMarkerSurvivesStartFailure(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.startErr = errors.New("claude: not found")

	got, err := switchHeadless(t, rt, b, "exited", false)
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}

	logPath := rt.Store.BuilderLogPath("webshop", b.Round)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantSuffix := ": switched to " + testClaudeRef + " (exited) ---"
	if !strings.HasSuffix(lines[len(lines)-1], wantSuffix) {
		t.Errorf("last line = %q, want suffix %q", lines[len(lines)-1], wantSuffix)
	}
	if !strings.HasPrefix(lines[len(lines)-1], "--- relay ") {
		t.Errorf("last line = %q, want prefix --- relay ", lines[len(lines)-1])
	}
}

// exits returns the exit entries in webshop's log.
func exits(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindExit {
			out = append(out, e)
		}
	}
	return out
}

func TestReconcileHeadlessIdleIsNotBroken(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := seedHeadless(t, f, fr) // bound, nothing sent: no round open

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active: no process between rounds is normal, not broken", got.State)
	}
	if !got.BuilderMissingSince.IsZero() {
		t.Errorf("BuilderMissingSince must stay zero for a headless binding: %s", got.BuilderMissingSince)
	}
	if len(f.starts) != 0 || len(fr.specs) != 0 || len(fr.kills) != 0 || len(f.notices) != 0 {
		t.Errorf("an idle tick must do nothing: starts=%d specs=%d kills=%d notices=%v", len(f.starts), len(fr.specs), len(fr.kills), f.notices)
	}
	// A second idle tick, well after any grace, still does not switch.
	got, err = reconcile(t, at(rt, 5*time.Minute), got, []herdr.Agent{plannerAgent()})
	if err != nil || got.State != store.StateActive || len(fr.specs) != 0 {
		t.Errorf("later idle tick: state=%s specs=%d err=%v", got.State, len(fr.specs), err)
	}
}

func TestReconcileHeadlessReportWinsEvenIfTheProcessExitedNonZero(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 1)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	pending, found, perr := rt.Store.PendingForPlanner("webshop")
	if err != nil || got.Round != 2 || len(exits(t, rt)) != 0 || len(fr.specs) != 1 || perr != nil || !found {
		t.Fatalf("round=%d exits=%d specs=%d err=%v found=%v; want the report to finish the round with no exit entry and no switch", got.Round, len(exits(t, rt)), len(fr.specs), err, found)
	}
	if pending.Note != "unmarked" || !strings.Contains(pending.Payload, "exited (code 1)") {
		t.Errorf("note=%q payload=%q, want an unmarked close naming the exit code", pending.Note, pending.Payload)
	}
}

func TestReconcileHeadlessAliveWaits(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)

	got, err := reconcile(t, at(rt, 10*time.Minute), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 || got.State != store.StateActive || got.Builder.PID != b.Builder.PID {
		t.Errorf("a live process is left alone: round=%d state=%s pid=%d", got.Round, got.State, got.Builder.PID)
	}
	if len(fr.kills) != 0 || len(fr.specs) != 1 || len(exits(t, rt)) != 0 || len(f.notices) != 0 {
		t.Errorf("nothing else may happen while it runs: kills=%d specs=%d exits=%d notices=%v", len(fr.kills), len(fr.specs), len(exits(t, rt)), f.notices)
	}
}

func TestReconcileHeadlessBudgetHaltsButNeverKills(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	b.RoundTimeoutMS = 1000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, 2*time.Second), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you past the budget", got.State)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "run past") {
		t.Errorf("notices = %+v, want the budget halt", f.notices)
	}
	if len(fr.kills) != 0 || got.Builder.PID != b.Builder.PID {
		t.Errorf("the budget never kills: kills=%+v pid=%d", fr.kills, got.Builder.PID)
	}
}

func TestReconcileHeadlessExitWithoutReportLogsAndSwitches(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 3)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("starting\nboom: out of tokens\nrelay-exit:3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("exit entries = %d, want 1", len(ex))
	}
	if ex[0].Note != "builder exited (code 3) without a report" {
		t.Errorf("note = %q", ex[0].Note)
	}
	if !strings.Contains(ex[0].Payload, "boom: out of tokens") || ex[0].Path != logPath {
		t.Errorf("payload/path = %q / %q, want the log tail and the log path", ex[0].Payload, ex[0].Path)
	}
	if !ex[0].Confirmed || ex[0].Direction != store.DirToPlanner || ex[0].Round != 1 {
		t.Errorf("exit entry shape = %+v", ex[0])
	}
	// Then the switch, exactly as "gone" does today.
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "switched builder (exited (code 3) without a report): picked "+testClaudeRef) {
		t.Errorf("switch entries = %+v", sw)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a second Start on claude: %+v", fr.specs)
	}
	if got.Builder.PID != fr.handles[1].PID || got.Builder.PID == oldPID {
		t.Errorf("pid = %d, want the replacement's %d", got.Builder.PID, fr.handles[1].PID)
	}
	if got.Round != 1 || got.RoundSwitches != 1 || got.State != store.StateActive || got.BuilderCandidate != testClaudeRef {
		t.Errorf("bookkeeping: round=%d switches=%d state=%s cand=%q", got.Round, got.RoundSwitches, got.State, got.BuilderCandidate)
	}
	if len(fr.kills) != 0 {
		t.Errorf("an exited process is not killed: %+v", fr.kills)
	}
	if !got.BuilderMissingSince.IsZero() {
		t.Errorf("BuilderMissingSince is a pane concept: %s", got.BuilderMissingSince)
	}
}

func TestReconcileHeadlessExitUnknownCode(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false) // exited; no exit() set: killed before the trailer, say

	if _, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 || ex[0].Note != "builder exited (code unknown) without a report" {
		t.Errorf("exit entries = %+v", ex)
	}
}

func TestReconcileHeadlessExitHaltsAfterMaxSwitches(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 2)
	b.RoundSwitches = rt.Policy.SwitchLimit()
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you at the switch limit", got.State)
	}
	if len(fr.specs) != 1 {
		t.Errorf("no replacement may start past the limit: specs = %d", len(fr.specs))
	}
	if len(exits(t, rt)) != 1 {
		t.Error("the exit is still logged")
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "already switched") {
		t.Errorf("notices = %+v", f.notices)
	}
	if got.Builder.PID != 0 {
		t.Errorf("pid = %d, want 0 after the exit", got.Builder.PID)
	}
	// Second tick: nothing repeats.
	if _, err := reconcile(t, rt, got, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatal(err)
	}
	if len(exits(t, rt)) != 1 || len(f.notices) != 1 {
		t.Errorf("a halted exit must not re-log or re-notify: exits=%d notices=%d", len(exits(t, rt)), len(f.notices))
	}
}

func TestReconcileHeadlessGatedKillsAndSwitches(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	f.agents = []herdr.Agent{plannerAgent()}
	rt := newRuntime(t, f)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/other/m", PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, _ := rt.Store.Load("webshop")
	old := handleOf(b.Builder)
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != old {
		t.Errorf("kills = %+v, want the gated builder's process %+v", fr.kills, old)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a claude replacement: %+v", fr.specs)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 0 || got.Builder.PID != fr.handles[1].PID {
		t.Errorf("bookkeeping: cand=%q switches=%d (want 0; a gated switch is uncounted) pid=%d", got.BuilderCandidate, got.RoundSwitches, got.Builder.PID)
	}
	if len(f.closed) != 0 {
		t.Errorf("no pane to close: %+v", f.closed)
	}
}

func TestReconcileHeadlessExitOnLimitGatesAndSwitchesUncounted(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, f, fr)
	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 1)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "starting\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\nrelay-exit:1\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("exit entries = %d, want 1 (the exit is still logged)", len(ex))
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "switched builder (rate-limited: Individual quota reached") {
		t.Errorf("switch entries = %+v", sw)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	rl := rateLimitedEntries(loadLedger(t, rt))
	if len(rl) != 1 || rl[0].Source != "relay" || rl[0].Subject != "other" {
		t.Errorf("rate_limited entries = %+v", rl)
	}
	if len(fr.kills) != 0 {
		t.Errorf("kills = %+v, want none: the process already exited", fr.kills)
	}
}

func TestReconcileHeadlessBudgetOnLimitKillsAndSwitches(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, f, fr)
	b.RoundTimeoutMS = 1000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	old := handleOf(b.Builder)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "working\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, 2*time.Second), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != old {
		t.Errorf("kills = %+v, want the old process %+v", fr.kills, old)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a claude replacement: %+v", fr.specs)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
	var switchNotices, timeoutNotices int
	for _, n := range f.notices {
		if strings.Contains(n, "switched builder") {
			switchNotices++
		}
		if strings.Contains(n, "run past") {
			timeoutNotices++
		}
	}
	if switchNotices != 1 || timeoutNotices != 0 {
		t.Errorf("notices = %+v, want exactly one switch notice and no timeout notice", f.notices)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
	}
}

func TestReconcileHeadlessBudgetWithoutLimitStillHalts(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, f, fr)
	b.RoundTimeoutMS = 1000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("working hard\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, 2*time.Second), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "run past") {
		t.Errorf("notices = %+v, want the budget halt", f.notices)
	}
	if len(fr.kills) != 0 || got.Builder.PID != b.Builder.PID {
		t.Errorf("the budget never kills: kills=%+v pid=%d", fr.kills, got.Builder.PID)
	}
	if l := loadLedger(t, rt); len(l.Entries) != 0 {
		t.Errorf("ledger entries = %+v, want none", l.Entries)
	}
}

func TestReconcileHeadlessExitWithReportOnLimitGatesAndClosesUnmarked(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, f, fr)
	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 1)
	reportPath := rt.Store.ReportPath("webshop", 1)
	if err := os.WriteFile(reportPath, []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "starting\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\nrelay-exit:1\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("PendingForPlanner: found=%v err=%v", found, err)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
	if !strings.Contains(pending.Payload, "Provider rate-limited: Individual quota reached") {
		t.Errorf("payload = %q, want it to contain the rate-limit sentence", pending.Payload)
	}
	rl := rateLimitedEntries(loadLedger(t, rt))
	if len(rl) != 1 {
		t.Errorf("rate_limited entries = %+v, want 1", rl)
	}
	if sw := switches(t, rt); len(sw) != 0 {
		t.Errorf("switch entries = %+v, want none", sw)
	}
	if len(fr.specs) != 1 {
		t.Errorf("fr.specs = %+v, want 1: no replacement spawned", fr.specs)
	}
}

func TestReconcileHeadlessStrayProcessIsKilled(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr) // no round open
	b.Builder.PID = 999
	b.Builder.StartedAt = 1_700_000_000
	b.Builder.LogPath = rt.Store.BuilderLogPath("webshop", 1)
	fr.script(999, true)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != 999 {
		t.Errorf("kills = %+v, want the stray 999", fr.kills)
	}
	if got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("process fields must clear: %+v", got.Builder)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

func TestReconcileHeadlessAliveErrorIsTreatedAsAlive(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	fr.aliveErr = errors.New("ps: permission denied")

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile must not fail the tick on an OS hiccup: %v", err)
	}
	if got.Round != 1 || got.State != store.StateActive || got.Builder.PID != b.Builder.PID {
		t.Errorf("round=%d state=%s pid=%d; want the round left open", got.Round, got.State, got.Builder.PID)
	}
	if len(exits(t, rt)) != 0 || len(fr.specs) != 1 {
		t.Errorf("no exit, no switch on an OS hiccup: exits=%d specs=%d", len(exits(t, rt)), len(fr.specs))
	}
}

func TestReconcileHeadlessOpenRoundWithNoProcessIsLeftAlone(t *testing.T) {
	// A send whose Start failed: round open, PID 0, NEEDS YOU already set.
	fr := newFakeRunner()
	fr.startErr = errors.New("agy: not found")
	rt, _ := seedHeadless(t, &fakeHerdr{}, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err == nil {
		t.Fatal("Send should have failed")
	}
	b, _ := rt.Store.Load("webshop")
	// Stage a plan entry by hand so the round reads as open the way a
	// half-started round would; the failed Send logged none.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("webshop", store.LogEntry{TS: rt.Now(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Path: rt.Store.PlanPath("webshop", 1), Confirmed: true})
	}); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou || got.Builder.PID != 0 || len(fr.kills) != 0 || len(exits(t, rt)) != 0 {
		t.Errorf("state=%s pid=%d kills=%d exits=%d; want NEEDS YOU left as it is", got.State, got.Builder.PID, len(fr.kills), len(exits(t, rt)))
	}
}

func TestReconcilePanePathUntouchedByHeadless(t *testing.T) {
	// The existing pane tests are the real pin; this one adds a Runner to a
	// pane binding and checks it is never consulted.
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fr := newFakeRunner()
	rt.Runner = fr
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)})
	if err != nil || got.Round != 2 {
		t.Fatalf("pane report path: round=%d err=%v", got.Round, err)
	}
	if len(fr.specs) != 0 || len(fr.kills) != 0 {
		t.Errorf("pane path touched the Runner: specs=%d kills=%d", len(fr.specs), len(fr.kills))
	}
}

func TestDoneHeadlessStopsTheLiveProcess(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	h := handleOf(b.Builder)

	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != h {
		t.Errorf("kills = %+v, want the round's process %+v", fr.kills, h)
	}
	got, _ := rt.Store.Load("webshop")
	if got.State != store.StateDone || got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("after done: state=%s builder=%+v; want done with process fields cleared", got.State, got.Builder)
	}
}

func TestDoneHeadlessMarksTheLog(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	logPath := b.Builder.LogPath

	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantLast := fmt.Sprintf("--- relay %s: stopped: done ---", rt.Now().Local().Format("15:04:05"))
	if lines[len(lines)-1] != wantLast {
		t.Errorf("last line = %q, want %q", lines[len(lines)-1], wantLast)
	}
}

func TestStopProcessMarksUnbind(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	logPath := b.Builder.LogPath

	pid, err := stopProcess(context.Background(), rt, b.Builder, "unbind")
	if err != nil {
		t.Fatalf("stopProcess: %v", err)
	}
	if pid != b.Builder.PID {
		t.Errorf("pid = %d, want %d", pid, b.Builder.PID)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantLast := fmt.Sprintf("--- relay %s: stopped: unbind ---", rt.Now().Local().Format("15:04:05"))
	if lines[len(lines)-1] != wantLast {
		t.Errorf("last line = %q, want %q", lines[len(lines)-1], wantLast)
	}
}

func TestStopProcessKillFailureWritesNoMarker(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	fr.killErr = errors.New("boom")

	_, err := stopProcess(context.Background(), rt, b.Builder, "done")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want boom", err)
	}
	data, err := os.ReadFile(b.Builder.LogPath)
	if err == nil {
		if strings.Contains(string(data), "--- relay") {
			t.Errorf("log contains relay marker after failed kill: %s", string(data))
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error reading log: %v", err)
	}
}

func TestStopProcessIdleWritesNoMarker(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr)

	pid, err := stopProcess(context.Background(), rt, b.Builder, "done")
	if err != nil {
		t.Fatalf("stopProcess: %v", err)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
	if b.Builder.LogPath != "" {
		if _, err := os.Stat(b.Builder.LogPath); !os.IsNotExist(err) {
			t.Errorf("log file should not exist for idle endpoint: %v", err)
		}
	}
	if _, err := os.Stat(rt.Store.BuilderLogPath(b.Name, b.Round)); !os.IsNotExist(err) {
		t.Errorf("log file should not exist for idle endpoint: %v", err)
	}
}

func TestDoneHeadlessIdleKillsNothing(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, &fakeHerdr{}, fr)
	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if len(fr.kills) != 0 {
		t.Errorf("no process, no kill: %+v", fr.kills)
	}
}

func TestDoneHeadlessKillFailureStillMarksDone(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := sentHeadless(t, &fakeHerdr{}, fr)
	fr.killErr = errors.New("SIGTERM: operation not permitted")

	_, err := Done(context.Background(), rt, "webshop")
	if !errors.Is(err, ErrStopFailed) || !strings.Contains(err.Error(), "marked done") {
		t.Fatalf("err = %v, want ErrStopFailed saying the binding is still marked done", err)
	}
	got, _ := rt.Store.Load("webshop")
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done even when the kill failed", got.State)
	}
	if got.Builder.PID == 0 {
		t.Error("a process relay could not stop must stay recorded, so the human can find it")
	}
}

func TestDoneHeadlessKillFailureKeepsWorktree(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	fg := &fakeGit{}
	rt.Git = fg
	fr.killErr = errors.New("SIGTERM: operation not permitted")

	wt := t.TempDir()
	b.Worktree = wt
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, "webshop")
	if !errors.Is(err, ErrStopFailed) {
		t.Fatalf("err = %v, want ErrStopFailed", err)
	}
	if res.WorktreeKept != wt {
		t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
	}
	if res.KeptReason != "builder process still running" {
		t.Errorf("KeptReason = %q, want 'builder process still running'", res.KeptReason)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree calls = %d, want 0", len(fg.removeWorktreeCalls))
	}
}

func TestDoneHeadlessStopReleasesWorktree(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	fg := &fakeGit{dirtyResult: false}
	rt.Git = fg

	wt := t.TempDir()
	b.Worktree = wt
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, "webshop")
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeRemoved != wt {
		t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
	}
}

func TestDonePaneNeverTouchesTheRunner(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	fr := newFakeRunner()
	rt.Runner = fr
	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatal(err)
	}
	if len(fr.kills) != 0 {
		t.Errorf("pane done killed something: %+v", fr.kills)
	}
}

func TestUnbindHeadlessStopsTheProcessAndSaysSo(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	pid := b.Builder.PID

	res, err := Unbind(context.Background(), rt, "webshop", false)
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != pid {
		t.Errorf("kills = %+v, want pid %d", fr.kills, pid)
	}
	if res.ProcessStopped != pid || res.ProcessErr != "" {
		t.Errorf("result = %+v, want ProcessStopped=%d", res, pid)
	}
	if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding still loads: %v", err)
	}
	text := UnbindText("webshop", res)
	if !strings.Contains(text, fmt.Sprintf("stopped builder process %d", pid)) {
		t.Errorf("UnbindText = %q, want the stopped line", text)
	}
}

func TestUnbindHeadlessKillFailureIsReportedNotFatal(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	fr.killErr = errors.New("SIGTERM: operation not permitted")

	res, err := Unbind(context.Background(), rt, "webshop", true)
	if err != nil {
		t.Fatalf("Unbind must still succeed: %v", err)
	}
	if res.ProcessStopped != 0 || !strings.Contains(res.ProcessErr, "operation not permitted") {
		t.Errorf("result = %+v, want ProcessErr set and ProcessStopped 0", res)
	}
	text := UnbindText("webshop", res)
	if !strings.Contains(text, fmt.Sprintf("could not stop builder process (pid %d", b.Builder.PID)) {
		t.Errorf("UnbindText = %q, want the failure line", text)
	}
	if res.ArchivedTo == "" {
		t.Error("the archive still happens")
	}
}

func TestAnswerRefusesAHeadlessBuilderBeforeAskingHerdr(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)

	err := Answer(context.Background(), rt, "webshop", AnswerInput{Keys: "enter"})
	if !errors.Is(err, ErrHeadlessNoDialog) {
		t.Fatalf("err = %v, want ErrHeadlessNoDialog", err)
	}
	if !strings.Contains(err.Error(), b.Builder.LogPath) {
		t.Errorf("the refusal must point at the log: %v", err)
	}
	if len(f.keys) != 0 || f.listCalls != 0 {
		t.Errorf("nothing may reach herdr: keys=%d listCalls=%d", len(f.keys), f.listCalls)
	}

	// Between rounds there is no log to point at; the refusal still stands.
	rt2, _ := seedHeadless(t, &fakeHerdr{}, newFakeRunner())
	err = Answer(context.Background(), rt2, "webshop", AnswerInput{Keys: "enter"})
	if !errors.Is(err, ErrHeadlessNoDialog) || !strings.Contains(err.Error(), "no round is running") {
		t.Errorf("idle headless: err = %v", err)
	}
}

func TestStatusHeadlessWorkingShowsPidAndLogTail(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}, fr)
	if err := os.MkdirAll(filepath.Dir(b.Builder.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Builder.LogPath, []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.BuilderPane != "headless" || row.BuilderKind != "agy" || row.BuilderStatus != "working" {
		t.Errorf("row = pane %q kind %q status %q; want headless/agy/working", row.BuilderPane, row.BuilderKind, row.BuilderStatus)
	}
	if row.Headless == nil {
		t.Fatal("Headless info missing")
	}
	if row.Headless.PID != b.Builder.PID || row.Headless.LogPath != b.Builder.LogPath || row.Headless.ExitCode != "" {
		t.Errorf("info = %+v", *row.Headless)
	}
	if !row.Headless.StartedAt.Equal(time.Unix(b.Builder.StartedAt, 0)) {
		t.Errorf("StartedAt = %s, want %s", row.Headless.StartedAt, time.Unix(b.Builder.StartedAt, 0))
	}
	if !reflect.DeepEqual(row.Headless.Tail, []string{"l2", "l3", "l4"}) {
		t.Errorf("Tail = %q, want the last three lines", row.Headless.Tail)
	}

	text := RenderStatus(rep)
	for _, want := range []string{"  builder  headless       agy      working", fmt.Sprintf("pid %d since", b.Builder.PID), "`" + testAgyRef + "`", "  log      l2\n  log      l3\n  log      l4\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("RenderStatus lacks %q:\n%s", want, text)
		}
	}
}

func TestStatusHeadlessIdle(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}, fr)
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.BuilderStatus != "idle" || row.Headless == nil || row.Headless.PID != 0 || len(row.Headless.Tail) != 0 {
		t.Errorf("row = %q %+v; want idle with no pid and no tail", row.BuilderStatus, row.Headless)
	}
	text := RenderStatus(rep)
	if strings.Contains(text, "pid ") || strings.Contains(text, "  log ") {
		t.Errorf("idle must show no pid and no log lines:\n%s", text)
	}
	if !strings.Contains(text, "  builder  headless       agy      idle") {
		t.Errorf("RenderStatus:\n%s", text)
	}
}

func TestStatusHeadlessExited(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.BuilderStatus != "exited 3" || row.Headless.ExitCode != "3" {
		t.Errorf("status = %q info = %+v; want exited 3", row.BuilderStatus, row.Headless)
	}

	// No trailer: exited, code unknown.
	fr2 := newFakeRunner()
	rt2, b2 := sentHeadless(t, &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}, fr2)
	fr2.script(b2.Builder.PID, false)
	rep2, _ := Status(context.Background(), rt2)
	if rep2.Bindings[0].BuilderStatus != "exited" || rep2.Bindings[0].Headless.ExitCode != "unknown" {
		t.Errorf("no trailer: status = %q info = %+v", rep2.Bindings[0].BuilderStatus, rep2.Bindings[0].Headless)
	}
}

func TestStatusHeadlessWithoutRunnerIsUnknown(t *testing.T) {
	rt, _ := sentHeadless(t, &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}, newFakeRunner())
	rt.Runner = nil
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].BuilderStatus != "unknown" {
		t.Errorf("status = %q, want unknown when no Runner can answer", rep.Bindings[0].BuilderStatus)
	}
}

func TestStatusPaneRowHasNoHeadlessInfo(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	rt.Runner = newFakeRunner()
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Bindings[0].Headless != nil || rep.Bindings[0].BuilderPane != "w2:p4" {
		t.Errorf("pane row = %+v", rep.Bindings[0])
	}
}

// TestReconcileHeadlessAliveWithReportButNoMarkerWaits is the headless half
// of the fix: a running process that has written a report is still running.
// Mutation: stat the report instead of the marker -> round 2, PID cleared.
func TestReconcileHeadlessAliveWithReportButNoMarkerWaits(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 || got.Builder.PID != b.Builder.PID {
		t.Errorf("round=%d pid=%d, want round 1 and the same pid: the process is still running", got.Round, got.Builder.PID)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing is queued while the process runs without a marker")
	}
	if len(fr.kills) != 0 || len(exits(t, rt)) != 0 {
		t.Errorf("kills=%d exits=%d, want none", len(fr.kills), len(exits(t, rt)))
	}
}

// TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked: exit is a
// hard fact, so the report is trusted with the omission noted (spec §4.4).
func TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("round=%d builder=%+v, want round 2 with process fields cleared", got.Round, got.Builder)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
	if !strings.Contains(pending.Payload, "exited (code 0) after writing its report but never confirmed completion (no 001-done)") ||
		!strings.Contains(pending.Payload, rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("payload = %q", pending.Payload)
	}
	if len(exits(t, rt)) != 0 || len(fr.specs) != 1 {
		t.Errorf("exits=%d specs=%d, want no exit entry and no switch: the report is the record", len(exits(t, rt)), len(fr.specs))
	}
}

// TestReconcileHeadlessMarkerClosesAndClearsTheHandle: the marker closes the
// round the same way for a process as for a pane, and the handle goes with it.
func TestReconcileHeadlessMarkerClosesAndClearsTheHandle(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 || got.Builder.StartedAt != 0 || got.Builder.LogPath != "" {
		t.Errorf("round=%d builder=%+v, want round 2 with process fields cleared", got.Round, got.Builder)
	}
	if !got.Builder.Headless() || got.Builder.AgentName != "webshop-builder" {
		t.Errorf("identity must survive: %+v", got.Builder)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found || pending.Note != "" {
		t.Errorf("want a normal report queued: found=%v note=%q err=%v", found, pending.Note, err)
	}
	if len(fr.kills) != 0 {
		t.Errorf("a builder that wrote its marker is never killed: %+v", fr.kills)
	}
}

// escapeFixture seeds webshop headless with a Repo and a fake Git configured
// so a round that leaves the worktree's tree unchanged while the repo is
// dirty is detected as a worktree escape (#192): fg.snapshotTreeID is
// captured as the round's baseline by Send and compared against again at
// close, so leaving it alone between the two is what makes treeUnchanged
// hold.
func escapeFixture(t *testing.T, f *fakeHerdr, fr *fakeRunner, fg *fakeGit, repo string) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedHeadless(t, f, fr)
	rt.Git = fg
	b.Repo = repo
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree == "" {
		t.Fatalf("baseline tree not captured; fixture assumes rt.Git is wired before Send")
	}
	return rt, b
}

// TestHeadlessMarkerCloseEscapedNote: the tree is unchanged and the source
// repo is dirty, but the builder still produced a report and its marker --
// the round closes normally, annotated "escaped" rather than halted (#192).
func TestHeadlessMarkerCloseEscapedNote(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	fg := &fakeGit{snapshotTreeID: "tree-1", dirtyResult: true}
	rt, b := escapeFixture(t, f, fr, fg, "/original/repo")

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2: an escape note still closes the round", got.Round)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != escapeNote {
		t.Errorf("note = %q, want %q", pending.Note, escapeNote)
	}
}

// TestHeadlessExitNoReportEscapedHalts: the tree is unchanged, the source
// repo is dirty, and the process exited without a report at all -- nothing
// suggests the builder ever touched its own tree, so relay halts NEEDS YOU
// rather than dispatching a replacement into the same broken setup (#192).
func TestHeadlessExitNoReportEscapedHalts(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	fg := &fakeGit{snapshotTreeID: "tree-1", dirtyResult: true}
	rt, b := escapeFixture(t, f, fr, fg, "/original/repo")
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you: an escape halts rather than switches", got.State)
	}
	if !strings.Contains(got.Halt, "worked outside its tree") {
		t.Errorf("Halt = %q, want it to name the escape", got.Halt)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0: an escape halt is not a switch", got.RoundSwitches)
	}
	if len(switches(t, rt)) != 0 {
		t.Errorf("switch entries = %+v, want none", switches(t, rt))
	}
	if len(fr.specs) != 1 {
		t.Errorf("specs = %+v, want no second Start", fr.specs)
	}
}

// TestHeadlessExitNoReportNoRepoSwitches pins that a binding with no Repo
// (an old bind.json, a --cwd bind, or an adopted one) is untouched by #192:
// escapeCheck's precondition on b.Repo keeps the existing switch-on-exit
// behaviour exactly as it was.
func TestHeadlessExitNoReportNoRepoSwitches(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active after an ordinary switch", got.State)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 1 {
		t.Errorf("bookkeeping: cand=%q switches=%d, want %q / 1", got.BuilderCandidate, got.RoundSwitches, testClaudeRef)
	}
	if len(switches(t, rt)) != 1 {
		t.Errorf("switch entries = %+v, want 1", switches(t, rt))
	}
}

// twoBuilderJSON serves "builder" from exactly two candidates, so excluding
// both is reachable in one round -- testCandidatesJSON's third (unlisted)
// candidate would otherwise still be pickable and no halt would ever fire.
const twoBuilderJSON = `[
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
]`

// TestReconcileHeadlessRoundExclusionThenAllGatedHalts is #191's end-to-end
// pin: A exits without a report and is excluded in favour of B; B then also
// exits without a report, and with every candidate serving "builder" now
// excluded, relay halts instead of dispatching a third pick into the same
// broken round. A report that eventually appears still closes the round
// normally, and finishRound clears the exclusion with the switch count.
func TestReconcileHeadlessRoundExclusionThenAllGatedHalts(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	fr := newFakeRunner()
	rt := newRuntime(t, f)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, twoBuilderJSON)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	f.listCalls = 0
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Stage 1: A (agy) exits without a report -> switches to B (claude),
	// RoundExcluded == [A].
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile (A exits): %v", err)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Fatalf("candidate after switch = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testAgyRef {
		t.Fatalf("RoundExcluded = %v, want [%s]", got.RoundExcluded, testAgyRef)
	}

	// Stage 2: B (claude) also exits without a report -> every candidate
	// serving builder is now excluded, so relay halts instead of switching.
	fr.script(got.Builder.PID, false)
	fr.exit(got.Builder.PID, 4)
	got, err = reconcile(t, rt, got, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile (B exits): %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "exited without a report") {
		t.Errorf("Halt = %q, want it to mention \"exited without a report\"", got.Halt)
	}
	if len(got.RoundExcluded) != 2 {
		t.Errorf("RoundExcluded = %v, want both candidates excluded", got.RoundExcluded)
	}

	// Stage 3: a report and marker eventually appear for round 1 -- the
	// marker still closes the round, and finishRound clears RoundExcluded
	// alongside RoundSwitches.
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	got, err = reconcile(t, rt, got, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile (marker closes): %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2", got.Round)
	}
	if got.RoundExcluded != nil {
		t.Errorf("RoundExcluded = %v, want nil after finishRound", got.RoundExcluded)
	}
}

func TestAppendLogMarker(t *testing.T) {
	t.Run("appends in order", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "builder.log")
		if err := os.WriteFile(p, []byte("first line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t1 := time.Date(2026, 9, 14, 10, 15, 30, 0, time.UTC)
		t2 := time.Date(2026, 9, 14, 10, 16, 45, 0, time.UTC)
		appendLogMarker(p, t1, "stopped: done")
		appendLogMarker(p, t2, "switched to x/y/z (why)")

		want := fmt.Sprintf("first line\n--- relay %s: stopped: done ---\n--- relay %s: switched to x/y/z (why) ---\n",
			t1.Local().Format("15:04:05"),
			t2.Local().Format("15:04:05"),
		)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("got %q, want %q", string(got), want)
		}
	})

	t.Run("creates the file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "builder.log")
		t1 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
		appendLogMarker(p, t1, "stopped: done")

		want := fmt.Sprintf("--- relay %s: stopped: done ---\n", t1.Local().Format("15:04:05"))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("got %q, want %q", string(got), want)
		}
	})

	t.Run("empty path is a no-op and unwritable path does not panic", func(t *testing.T) {
		appendLogMarker("", time.Now(), "stopped: done")

		dir := t.TempDir()
		appendLogMarker(dir, time.Now(), "stopped: done")
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			t.Errorf("%s is no longer a directory", dir)
		}
	})
}

func TestReconcileHeadlessExitPermissionBlockedHalts(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	fr := newFakeRunner()
	rt := newRuntime(t, f)
	rt.Runner = fr
	_, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testClaudeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Headless:    true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)

	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 1)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	denialText := "tool use was rejected: Bash command not allowed"
	if err := os.WriteFile(logPath, []byte("starting\n"+denialText+"\nrelay-exit:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "permission denial") || !strings.Contains(got.Halt, `"`+denialText+`"`) {
		t.Errorf("Halt = %q, want it to contain 'permission denial' and %q", got.Halt, `"`+denialText+`"`)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("got %d exit entries, want 1", len(ex))
	}
	wantSuffix := "; permission-blocked: " + denialText
	if !strings.HasSuffix(ex[0].Note, wantSuffix) {
		t.Errorf("exit entry Note = %q, want suffix %q", ex[0].Note, wantSuffix)
	}
	if got.RoundSwitches != b.RoundSwitches {
		t.Errorf("RoundSwitches = %d, want %d (unchanged)", got.RoundSwitches, b.RoundSwitches)
	}
	if l := loadLedger(t, rt); len(l.Entries) != 0 {
		t.Errorf("ledger entries = %+v, want none", l.Entries)
	}
	if len(fr.specs) != 1 {
		t.Errorf("specs = %d, want 1 (no new process started)", len(fr.specs))
	}
}

// TestGateHeadlessCallSiteHolds pins #132: the headless marker path holds
// the round while a configured gate runs -- no "exited without a report"
// handling (no KindExit, no switch) -- exactly as the pane call site does.
func TestGateHeadlessCallSiteHolds(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := seedHeadless(t, f, fr)
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("Round = %d, want 1: the gate holds the round open", got.Round)
	}
	if got.GateRun == nil {
		t.Fatal("GateRun must be set once the gate starts")
	}
	if len(exits(t, rt)) != 0 {
		t.Errorf("no KindExit while the gate runs: %+v", exits(t, rt))
	}
	if len(switches(t, rt)) != 0 {
		t.Errorf("no switch while the gate runs: %+v", switches(t, rt))
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
}
