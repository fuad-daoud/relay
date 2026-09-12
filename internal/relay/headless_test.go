package relay

import (
	"context"
	"errors"
	"os"
	"reflect"
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
