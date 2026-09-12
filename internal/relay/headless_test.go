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
			"--output-format", "text", "--print-timeout", "2h0m0s", "--dangerously-skip-permissions"}},
		{testClaudeRef, []string{"claude", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor", "--output-format", "text"}},
		{testOpencodeRef, []string{"opencode", "run", "PROMPT", "-m", "test/m", "--agent", "plan-executor"}},
	}
	for _, c := range cases {
		got, err := headlessLaunch(lookup(c.token), role, 2*time.Hour, "PROMPT")
		if err != nil {
			t.Fatalf("%s: %v", c.token, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.token, got, c.want)
		}
	}
	if _, err := headlessLaunch(candidate.Candidate{Harness: "nope"}, role, time.Hour, "x"); err == nil {
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
	if spec.Argv[0] != "agy" || spec.Argv[1] != "-p" || spec.Argv[2] != "the prompt" {
		t.Errorf("argv = %v; want the agy print form with the prompt at index 2", spec.Argv)
	}
	if !containsArg(spec.Argv, "--print-timeout", "24h0m0s") {
		t.Errorf("argv %v lacks the default 24h budget", spec.Argv)
	}
	h := fr.handles[0]
	if got.Builder.PID != h.PID || got.Builder.StartedAt != h.StartedAt.Unix() || got.Builder.LogPath != wantLog {
		t.Errorf("endpoint after start = %+v, want pid %d started %d log %s", got.Builder, h.PID, h.StartedAt.Unix(), wantLog)
	}
	if !got.Builder.Headless() || got.Builder.PaneID != "" {
		t.Errorf("mode or pane changed: %+v", got.Builder)
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

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# do the thing"))
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
	b, _ := rt.Store.Load("webshop")
	wantPrompt := composePrompt(b, planPath, reportPath)
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
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "round one")); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	// Unscripted, the fake reports the process alive forever.
	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "round one again"))
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
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "one")); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	fr.script(fr.handles[0].PID, false) // exited between the two sends
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "one, corrected")); err != nil {
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

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"))
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

func TestSendPanePathIsUntouchedByHeadless(t *testing.T) {
	// A pane binding with a Runner configured never touches it.
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, _ := seedBound(t, f)
	rt.Runner = fr
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x")); err != nil {
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
	e := store.Endpoint{AgentName: "x-builder", Kind: "agy", Mode: store.ModeHeadless, PID: 7, StartedAt: 9, LogPath: "/l"}
	got := clearProcess(e)
	want := store.Endpoint{AgentName: "x-builder", Kind: "agy", Mode: store.ModeHeadless}
	if got != want {
		t.Errorf("clearProcess = %+v, want %+v", got, want)
	}
}

// sentHeadless is seedHeadless plus one Send: round 1 open, one process
// started on fr.
func sentHeadless(t *testing.T, f *fakeHerdr, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := seedHeadless(t, f, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it")); err != nil {
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
		out, err = switchBuilder(context.Background(), rt, tx, b, reason, closeOld)
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

func TestReconcileHeadlessReportFinishesTheRoundAndClearsTheHandle(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after a report", got.Round)
	}
	if got.Builder.PID != 0 || got.Builder.StartedAt != 0 || got.Builder.LogPath != "" {
		t.Errorf("process fields must clear with the round: %+v", got.Builder)
	}
	if !got.Builder.Headless() || got.Builder.AgentName != "webshop-builder" {
		t.Errorf("identity must survive: %+v", got.Builder)
	}
	if len(fr.kills) != 0 {
		t.Errorf("a builder that wrote its report is never killed: %+v", fr.kills)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found || !strings.Contains(pending.Payload, rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("report must be queued: found=%v payload=%q err=%v", found, pending.Payload, err)
	}
	if len(exits(t, rt)) != 0 {
		t.Error("a report is the record; no exit entry")
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
	if err != nil || got.Round != 2 || len(exits(t, rt)) != 0 || len(fr.specs) != 1 {
		t.Errorf("round=%d exits=%d specs=%d err=%v; want the report to finish the round with no exit entry and no switch", got.Round, len(exits(t, rt)), len(fr.specs), err)
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
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it")); err != nil {
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
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 1 || got.Builder.PID != fr.handles[1].PID {
		t.Errorf("bookkeeping: cand=%q switches=%d pid=%d", got.BuilderCandidate, got.RoundSwitches, got.Builder.PID)
	}
	if len(f.closed) != 0 {
		t.Errorf("no pane to close: %+v", f.closed)
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
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x")); err == nil {
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

	if err := Done(context.Background(), rt, "webshop"); err != nil {
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

func TestDoneHeadlessIdleKillsNothing(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, &fakeHerdr{}, fr)
	if err := Done(context.Background(), rt, "webshop"); err != nil {
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

	err := Done(context.Background(), rt, "webshop")
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

func TestDonePaneNeverTouchesTheRunner(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	fr := newFakeRunner()
	rt.Runner = fr
	if err := Done(context.Background(), rt, "webshop"); err != nil {
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
