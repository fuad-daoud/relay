# Headless builders, step 5: `send` -> `startRound` -- one process per round (#99)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #99. **Spec:** `docs/specs/2026-09-12-headless-builders-design.md`
-- read §1, §3.4, §3.5, §4.2, §4.3, §5.2, §6 and §7 step 5 before starting;
section numbers below refer to it. Steps 1-4 are on `main`: the store
fields, `harness.Launch.Print`/`PrintArgs`, `relay.Runner` + `fakeRunner`,
and `bind --headless`. This plan builds on all four.
**Depends on:** nothing open.

**Goal:** `relay send` on a headless binding starts the harness's print form
as a detached process in the binding's tree, with the same prompt the pane
path types, and records the process handle on the endpoint; a second `send`
while that process is alive is refused.

**Architecture:** a new `internal/relay/headless.go` holds the headless
pieces steps 5-7 share: `handleOf(store.Endpoint) ProcHandle`,
`headlessLaunch(...)` (argv = candidate binary + `PrintArgs`), `roundBudget`,
`startRound`, and `ErrBuilderBusy`. `Send` branches twice on
`b.Builder.Headless()`: before the lock it skips the herdr agent lookup (a
headless builder has no agent to find), and inside the lock it replaces the
liveness check (`SameAgent`) with `Runner.Alive` on the recorded handle and
the hand-off (`promptWithRetry`) with `startRound`. Everything after the
hand-off -- the `plan` log entry, drift, `RoundStartedAt`, `State` -- is
shared and unchanged. A `Start` failure records `spawn_failed` in the ledger
exactly as a pane spawn failure does, leaves the round open with `PID 0`,
and puts the binding in `NEEDS YOU`.

**Deviation from the spec text, decided by the planner:** §4.3 says "Send
already ... bumped the round". `Send` does not bump the round today -- the
round advances when the report is queued -- and this plan does not change
that. `startRound` runs against `b.Round` as it stands, exactly where
`promptWithRetry` runs today.

**Not in this step:** the daemon does not yet understand a headless binding
(step 6). A headless binding must still not be created live.

**Tech stack:** Go 1.22. Verification is `make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-t5` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-t5`, cut from `main`. |
| `~/.local/state/relay/headless-t5` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself.

## Global constraints

- Files touched: `internal/relay/headless.go` (new), `headless_test.go`
  (new), `send.go`. Nothing else.
- The pane path of `Send` is byte-for-byte unchanged in behaviour: every
  existing test in `internal/relay` passes **unmodified**.
- The prompt a headless builder receives is `composePrompt(b, planPath,
  reportPath)` -- the same bytes the pane path types. No headless-specific
  wording.
- The log file is never read here. `startRound` only names it.
- `go.mod`/`go.sum` do not change.
- One commit per task, on the worktree's branch.

---

### Task 1: `headless.go` -- `handleOf`, `headlessLaunch`, `roundBudget`, `startRound`

**Files:**
- Create: `internal/relay/headless.go`
- Create: `internal/relay/headless_test.go`

**Interfaces:**
- Consumes: `store.Endpoint{PID, StartedAt, LogPath}`, `Store.BuilderLogPath` (step 1); `harness.Lookup`, `Harness.Binary`, `Harness.Launch`, `Launch.PrintArgs`, `Launch.PromptAt` (step 2); `Runner`, `ProcSpec`, `ProcHandle`, `ErrRunnerUnavailable`, `Runtime.Runner` (step 3); `candidate.ParseRef`, `Set.Lookup`, `recordSpawnFailureLocked` (existing).
- Produces (Task 2 and steps 6-7 call these):

```go
var ErrBuilderBusy = errors.New("builder's previous process is still running; wait for its report, or relay done")
func handleOf(e store.Endpoint) ProcHandle
func roundBudget(b store.Binding) time.Duration
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, budget time.Duration, prompt string) ([]string, error)
func startRound(ctx context.Context, rt Runtime, b store.Binding, prompt string) (store.Binding, error)
```

- [ ] **Step 1: Write the failing tests**

Create `internal/relay/headless_test.go`:

```go
package relay

import (
	"context"
	"errors"
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
```

`ledger.Kind` is a string type; comparing `g.Kind == "spawn_failed"` avoids
importing `internal/ledger` in this file. If the compiler objects
(`mismatched types`), import `github.com/fuad-daoud/relay/internal/ledger`
and compare against `ledger.SpawnFailed` instead.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/relay -run 'TestHandleOf|TestRoundBudget|TestHeadlessLaunch|TestStartRound'`
Expected: compile error -- `undefined: handleOf`, `undefined: startRound`, ...

- [ ] **Step 3: Implement `headless.go`**

Create `internal/relay/headless.go`:

```go
package relay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrBuilderBusy reports a send against a headless binding whose previous
// round's process is still running (headless spec §5.2). One process per
// round is the model; two at once in one tree would race each other's
// edits.
var ErrBuilderBusy = errors.New("builder's previous process is still running; wait for its report, or relay done")

// handleOf is the endpoint's stored process fields as the Runner's handle.
// StartedAt is Unix seconds on the endpoint (store spec §3.1, amended).
func handleOf(e store.Endpoint) ProcHandle {
	return ProcHandle{PID: e.PID, StartedAt: time.Unix(e.StartedAt, 0)}
}

// defaultRoundBudget mirrors the store's default round timeout, for a
// binding that was never saved. Store.Save fills RoundTimeoutMS on every
// real binding, so this is a guard, not a policy.
const defaultRoundBudget = 24 * time.Hour

// roundBudget is the binding's round budget as a duration: what agy's
// --print-timeout gets, so the harness's own default (5m) never cuts a
// round short (headless spec §3.5).
func roundBudget(b store.Binding) time.Duration {
	if b.RoundTimeoutMS <= 0 {
		return defaultRoundBudget
	}
	return time.Duration(b.RoundTimeoutMS) * time.Millisecond
}

// headlessLaunch renders the argv for one headless round: the candidate's
// binary, then its print form with the prompt and the budget filled in
// (headless spec §4.2). Pure. An unknown kind is an error, not a panic:
// Load validated the set, but a binding written by a future relay could
// name a kind this one does not know.
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, budget time.Duration, prompt string) ([]string, error) {
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness kind %q", c.Harness)
	}
	l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)
	if l.PromptAt < 0 {
		return nil, fmt.Errorf("harness %q has no print form", c.Harness)
	}
	return append([]string{h.Binary}, l.PrintArgs(prompt, budget)...), nil
}

// startRound starts the round's process for a headless binding and records
// its handle on the endpoint (headless spec §4.3). The caller holds the
// state lock, has staged the plan, and saves what comes back.
//
// Preconditions:  b.Builder.Headless(); no live process on the endpoint
// (Send checks with Runner.Alive first); rt.Runner non-nil.
// Postconditions: on success PID, StartedAt and LogPath describe the new
// process. On failure the endpoint is returned as it was, PID 0, and the
// candidate's spawn_failed is in the ledger -- the same record a pane spawn
// failure leaves, because it is the same failure: the candidate could not
// be launched. The caller decides the binding's state.
func startRound(ctx context.Context, rt Runtime, b store.Binding, prompt string) (store.Binding, error) {
	if rt.Runner == nil {
		return b, ErrRunnerUnavailable
	}
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	role, _ := harness.RoleByName("builder")
	argv, err := headlessLaunch(c, role, roundBudget(b), prompt)
	if err != nil {
		return b, err
	}
	logPath := rt.Store.BuilderLogPath(b.Name, b.Round)
	h, err := rt.Runner.Start(ctx, ProcSpec{Dir: b.CWD, Argv: argv, LogPath: logPath})
	if err != nil {
		recordSpawnFailureLocked(rt, c.Ref().String(), b.Name, err)
		return b, fmt.Errorf("start headless builder for %q (%s): %w", b.Name, c.Ref().String(), err)
	}
	b.Builder.PID = h.PID
	b.Builder.StartedAt = h.StartedAt.Unix()
	b.Builder.LogPath = logPath
	return b, nil
}
```

`recordSpawnFailureLocked` is used because every caller of `startRound`
holds `Store.WithLock` (Send does; the switch path in step 6 does). The
test `TestStartRoundFailureRecordsSpawnFailedAndLeavesPIDZero` calls it
inside `WithLock` for that reason.

- [ ] **Step 4: Run the tests**

Run: `go test -count=1 ./internal/relay -run 'TestHandleOf|TestRoundBudget|TestHeadlessLaunch|TestStartRound'`
Expected: PASS, all six.

- [ ] **Step 5: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/relay/headless.go internal/relay/headless_test.go
git commit -m "feat(relay): headless startRound -- argv from PrintArgs, handle recorded, spawn_failed on error (#99 step 5)"
```

---

### Task 2: `Send` branches to `startRound`

**Files:**
- Modify: `internal/relay/send.go`
- Modify: `internal/relay/headless_test.go` (tests appended)

**Interfaces:**
- Consumes: Task 1's functions; `Runner.Alive`.
- Produces: no new names. `Send`'s signature is unchanged.

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/headless_test.go`:

```go
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
```

Add `"os"` to the file's imports.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/relay -run 'TestSendHeadless|TestSendPanePath'`
Expected: the four `TestSendHeadless*` tests FAIL --
`TestSendHeadlessStartsTheProcessInsteadOfPrompting` with `ErrBuilderGone`
(Send still looks for a herdr agent). `TestSendPanePathIsUntouchedByHeadless`
passes already.

- [ ] **Step 3: Branch `Send`**

In `internal/relay/send.go`, `Send`'s pre-lock block currently reads:

```go
	if hint, err := rt.Store.Load(name); err == nil {
		baseline = CaptureBaseline(ctx, rt, hint)
		hintRound = hint.Round
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return SendResult{}, fmt.Errorf("list agents: %w", err)
		}
		var ok bool
		builder, ok = FindAgent(agents, hint.Builder)
		if !ok {
			return SendResult{}, fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, hint.Builder.PaneID, hint.BuilderCandidate, ErrBuilderGone)
		}
		locatedBuilder = true
	}
```

Change it to:

```go
	if hint, err := rt.Store.Load(name); err == nil {
		baseline = CaptureBaseline(ctx, rt, hint)
		hintRound = hint.Round
		// A headless builder (#99) is a process relay starts per round; there
		// is no herdr agent to find. Its liveness check is under the lock.
		if !hint.Builder.Headless() {
			agents, err := rt.Herdr.ListAgents(ctx)
			if err != nil {
				return SendResult{}, fmt.Errorf("list agents: %w", err)
			}
			var ok bool
			builder, ok = FindAgent(agents, hint.Builder)
			if !ok {
				return SendResult{}, fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, hint.Builder.PaneID, hint.BuilderCandidate, ErrBuilderGone)
			}
			locatedBuilder = true
		}
	}
```

Inside the lock, the block

```go
		if !locatedBuilder {
			return fmt.Errorf("binding %q: %w", name, ErrBuilderGone)
		}
		if !SameAgent(builder, b.Builder) {
			return fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, b.Builder.PaneID, b.BuilderCandidate, ErrBuilderGone)
		}
```

becomes

```go
		if b.Builder.Headless() {
			// One process per round (headless spec §5.2): a previous round's
			// process still running means the human is early, not that
			// relay should start a second builder in the same tree.
			if b.Builder.PID != 0 {
				if rt.Runner == nil {
					return fmt.Errorf("binding %q: %w", name, ErrRunnerUnavailable)
				}
				alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
				if err != nil {
					return fmt.Errorf("binding %q: check previous process %d: %w", name, b.Builder.PID, err)
				}
				if alive {
					return fmt.Errorf("binding %q (pid %d): %w", name, b.Builder.PID, ErrBuilderBusy)
				}
			}
		} else {
			if !locatedBuilder {
				return fmt.Errorf("binding %q: %w", name, ErrBuilderGone)
			}
			if !SameAgent(builder, b.Builder) {
				return fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, b.Builder.PaneID, b.BuilderCandidate, ErrBuilderGone)
			}
		}
```

And the hand-off

```go
		if err := promptWithRetry(ctx, rt, builder.PaneID, text); err != nil {
			if errors.Is(err, herdr.ErrAgentBlocked) {
				return fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
			}
			return fmt.Errorf("prompt builder: %w", err)
		}
```

becomes

```go
		if b.Builder.Headless() {
			started, err := startRound(ctx, rt, b, text)
			if err != nil {
				// The plan is staged and the round is open; nothing was
				// started. NEEDS YOU says so in status, and the ledger's
				// spawn_failed (written by startRound) gates the candidate
				// for the next pick, as a pane spawn failure would.
				b.State = store.StateNeedsYou
				if saveErr := tx.Save(b); saveErr != nil {
					return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", err, saveErr)
				}
				return err
			}
			b = started
		} else if err := promptWithRetry(ctx, rt, builder.PaneID, text); err != nil {
			if errors.Is(err, herdr.ErrAgentBlocked) {
				return fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
			}
			return fmt.Errorf("prompt builder: %w", err)
		}
```

Nothing after that point changes: the `plan` entry, drift, `RoundStartedAt`,
`State = StateActive` and `tx.Save(b)` run for both shapes.

Update `Send`'s doc comment to:

```go
// Send copies the planner's plan into relay state and hands it to the builder:
// typed into its pane, or -- for a headless binding (#99) -- as the prompt of
// a fresh process started in the binding's tree. It returns a SendResult
// describing the round and any between-rounds drift.
```

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS -- all five new tests and every existing one, unmodified.
If any `TestSend*` test from `send_test.go` fails, the pane path moved: stop
and report.

- [ ] **Step 5: Mutation check, then verify and commit**

Mutation: in `startRound`, comment out the three `b.Builder.PID/StartedAt/LogPath = ...`
assignments. Run `go test ./internal/relay -run 'TestStartRoundRecords|TestSendHeadlessStarts|TestSendHeadlessRefuses'`.
Expected: `TestStartRoundRecordsTheHandleAndTheLogPath` and
`TestSendHeadlessStartsTheProcessInsteadOfPrompting` FAIL, and
`TestSendHeadlessRefusesWhileThePreviousProcessIsAlive` FAILS too (with no
recorded pid the second send is not refused). Revert; re-run: PASS. Say in
the report that this was done and which tests failed.

Run: `make check`
Expected: green.

```bash
git add internal/relay/send.go internal/relay/headless_test.go
git commit -m "feat(relay): send starts a headless round -- Alive gate, startRound, NEEDS YOU on failure (#99 step 5)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), the mutation-check outcome, and
`git diff --stat main..HEAD`. Confirm no existing test was edited. If any step
was impossible as written, say which and why -- do not work around it.
