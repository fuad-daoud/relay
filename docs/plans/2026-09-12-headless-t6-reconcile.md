# Headless builders, step 6: the daemon's headless branch -- report, alive, exited, idle (#99)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #99. **Spec:** `docs/specs/2026-09-12-headless-builders-design.md`
-- read §1, §3.8, §4.4, §4.5, §5.1, §5.4, §6 and §7 step 6 before starting;
section numbers below refer to it. Steps 1-5 are on `main`: the store
fields, `PrintArgs`, `Runner`/`fakeRunner`, `bind --headless`, and
`send` → `startRound` (`internal/relay/headless.go`: `handleOf`,
`roundBudget`, `headlessLaunch`, `startRound`, `ErrBuilderBusy`). This plan
extends `headless.go` and branches `Reconcile` and `switchBuilder`.
**Depends on:** nothing open.

**Goal:** the daemon follows a headless round to its end: a report file
finishes the round; a live process is left alone (and only its budget can
halt it); a process that exited without a report is logged with its exit
code and log tail and the builder is switched, up to `max_switches`; a
binding with no round open is idle, never broken; a stray process with no
round is killed. A mid-round switch on a headless binding starts a new
process rather than opening a pane, and kills the old process when the
planner gated its provider.

**Architecture:** `Reconcile` gains one early branch, right after the `DONE`
gate: `if b.Builder.Headless() { return reconcileHeadless(...) }`. The pane
path below it is untouched. `reconcileHeadless` (in `headless.go`) mirrors
the pane path's order -- planner refresh, round-cap halt, then the §5.1
decision tree -- and reuses `queueReport`, `checkRoundTimeout`,
`haltBinding`, `switchBuilder`, `gatedBuilder` and `deliverAndSettle`
unchanged. `switchBuilder` learns two things: the replacement inherits the
mode (`Headless: b.Builder.Headless()` in the `BindOptions` it passes to
`resolveBuilder`), and the hand-off is `startRound` instead of
`promptWithRetry`; its `closeOld` step becomes `Runner.Kill` for a headless
old builder. `logTail` reads the last N lines of a file for the exit entry's
payload; nothing is parsed out of it.

**Decisions taken by the planner where the spec left room:**

- §5.1's `b.BuilderScreen = logTail(...)` is **not done**. `BuilderScreen`
  is a fingerprint hash in the pane path, with `BuilderScreenAt` driving the
  nudge-grace clock; writing prose into it would change its meaning for
  `status`. Step 7's status line reads `logTail(LogPath, 3)` straight from
  the file, which is what §4.8 says it shows anyway.
- After an exit without a report, `LogPath` is **kept** (PID goes to 0) so
  `status` and the exit entry can still point at the log until the round
  closes; the report path and the idle path clear all three fields.
- A round that is open with `PID == 0` and no report (a `send` whose
  `Start` failed and already set `NEEDS YOU`) is left as it is: nothing to
  observe, the human has been told.
- `Alive` returning an error is treated as alive for that tick (spec §5.1),
  logged with `slog.Warn`.

**Tech stack:** Go 1.22. Verification is `make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-t6` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-t6`, cut from `main`. |
| `~/.local/state/relay/headless-t6` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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

- Files touched: `internal/relay/headless.go`, `headless_test.go`,
  `reconcile.go` (one early branch), `switch.go`. Nothing else.
- The pane path is byte-for-byte unchanged in behaviour: every existing
  test in `internal/relay` passes **unmodified** -- `reconcile_test.go` and
  `switch_test.go` in particular.
- relay never kills a process for running long: the budget path halts
  (`NEEDS YOU`) and leaves the process alone (spec §5.1, §6). The only
  kills are: a stray process with no round open, and the gated-switch
  `closeOld`.
- The log is never parsed. `logTail` returns text for humans; the exit
  code comes from `Runner.ExitCode` only.
- `go.mod`/`go.sum` do not change.
- One commit per task, on the worktree's branch.

---

### Task 1: `logTail`, `clearProcess`, and `switchBuilder` learns headless

**Files:**
- Modify: `internal/relay/headless.go` (`logTail`, `logTailLines`, `clearProcess`)
- Modify: `internal/relay/switch.go` (`switchBuilder`)
- Modify: `internal/relay/headless_test.go` (tests appended)

**Interfaces:**
- Consumes: `handleOf`, `startRound` (step 5); `Runner.Kill`; `resolveBuilder` with `BindOptions.Headless` (step 4).
- Produces:

```go
const logTailLines = 20
func logTail(path string, n int) string            // last n lines, "" if absent; never parsed
func clearProcess(e store.Endpoint) store.Endpoint // PID, StartedAt, LogPath zeroed; Mode/Kind/AgentName kept
```

`switchBuilder`'s signature is unchanged.

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/headless_test.go`:

```go
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
```

Add `"os"`, `"path/filepath"` and `"strings"` to the file's imports where
missing.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/relay -run 'TestLogTail|TestClearProcess|TestSwitchBuilderHeadless'`
Expected: compile error -- `undefined: logTail`, `undefined: clearProcess`.

- [ ] **Step 3: `logTail` and `clearProcess`**

Append to `internal/relay/headless.go` (add `"os"` and `"strings"` to its
imports):

```go
// logTailLines is how much of a builder log the exit entry carries: enough
// to see why it died, not enough to flood the round log (spec §3.8).
const logTailLines = 20

// logTail is the last n lines of the file at path, without a trailing
// newline, or "" when the file is absent or empty. It is text for humans --
// the exit entry's payload, the status snippet -- and is never parsed
// (spec §1: relay reads no builder output for meaning).
func logTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || n <= 0 {
		return ""
	}
	s := strings.TrimRight(string(data), "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// clearProcess is the endpoint between rounds: no pid, no start time, no
// log. Identity -- name, kind, mode -- is untouched.
func clearProcess(e store.Endpoint) store.Endpoint {
	e.PID, e.StartedAt, e.LogPath = 0, 0, ""
	return e
}
```

- [ ] **Step 4: `switchBuilder`**

In `internal/relay/switch.go`, the `closeOld` block currently reads:

```go
	if closeOld {
		if err := rt.Herdr.ClosePane(ctx, b.Builder.PaneID); err != nil {
			return haltBinding(ctx, rt, b, fmt.Sprintf(
				"%s: builder %s; could not close its pane %s to replace it: %v",
				b.Name, reason, b.Builder.PaneID, err))
		}
	}
```

Replace it with:

```go
	if closeOld {
		if b.Builder.Headless() {
			// The one place besides done/unbind where relay stops a process
			// it started (#99): the planner gated the provider while the
			// round's process was still running.
			if b.Builder.PID != 0 && rt.Runner != nil {
				if err := rt.Runner.Kill(ctx, handleOf(b.Builder)); err != nil {
					return haltBinding(ctx, rt, b, fmt.Sprintf(
						"%s: builder %s; could not stop its process %d to replace it: %v",
						b.Name, reason, b.Builder.PID, err))
				}
			}
		} else if err := rt.Herdr.ClosePane(ctx, b.Builder.PaneID); err != nil {
			return haltBinding(ctx, rt, b, fmt.Sprintf(
				"%s: builder %s; could not close its pane %s to replace it: %v",
				b.Name, reason, b.Builder.PaneID, err))
		}
	}
```

The `resolveBuilder` call

```go
	ep, _, err := resolveBuilder(ctx, rt, tx, BindOptions{Candidate: res.Token(), CWD: b.CWD}, b.Name, b.Planner.PaneID)
```

becomes

```go
	// The replacement inherits the mode (spec §5.4): a headless binding gets
	// a headless endpoint, which startRound below fills in.
	ep, _, err := resolveBuilder(ctx, rt, tx, BindOptions{Candidate: res.Token(), CWD: b.CWD, Headless: b.Builder.Headless()}, b.Name, b.Planner.PaneID)
```

And the hand-off

```go
	text := composePrompt(b, rt.Store.PlanPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round))
	if err := promptWithRetry(ctx, rt, ep.PaneID, text); err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: switched builder to %s but could not hand it round %d: %v",
			b.Name, res.Token(), b.Round, err))
	}
```

becomes

```go
	text := composePrompt(b, rt.Store.PlanPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round))
	if b.Builder.Headless() {
		started, err := startRound(ctx, rt, b, text)
		if err != nil {
			return haltBinding(ctx, rt, b, fmt.Sprintf(
				"%s: switched builder to %s but could not start round %d: %v",
				b.Name, res.Token(), b.Round, err))
		}
		b = started
	} else if err := promptWithRetry(ctx, rt, ep.PaneID, text); err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: switched builder to %s but could not hand it round %d: %v",
			b.Name, res.Token(), b.Round, err))
	}
```

Note `b.Builder = ep` and `b.BuilderCandidate = res.Token()` are already
assigned above this point in the existing code, so `startRound` sees the
new candidate and a headless endpoint with `PID 0`. Update the function's
doc comment to mention: "For a headless binding the replacement is a new
process started on the same round's prompt; closeOld kills the old process
instead of closing a pane."

- [ ] **Step 5: Run the tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS -- the five new tests and every existing one (all of
`switch_test.go` unmodified).

- [ ] **Step 6: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/relay/headless.go internal/relay/headless_test.go internal/relay/switch.go
git commit -m "feat(relay): headless switch -- replacement inherits the mode, startRound hands off, closeOld kills (#99 step 6)"
```

---

### Task 2: `reconcileHeadless` and the branch in `Reconcile`

**Files:**
- Modify: `internal/relay/headless.go` (`reconcileHeadless`, `exitEntry`)
- Modify: `internal/relay/reconcile.go` (one early branch)
- Modify: `internal/relay/headless_test.go` (tests appended)

**Interfaces:**
- Consumes: `queueReport`, `checkRoundTimeout`, `haltBinding`, `switchBuilder`, `gatedBuilder`, `deliverAndSettle`, `refreshEndpoint`, `HasEntry`, `FindAgent` (existing); `handleOf`, `logTail`, `clearProcess`, `logTailLines` (above); `store.KindExit` (step 1).
- Produces:

```go
func reconcileHeadless(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error)
func exitEntry(now time.Time, round int, logPath, codeText string) store.LogEntry
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/headless_test.go`:

```go
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
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/relay -run 'TestReconcileHeadless|TestReconcilePanePath'`
Expected: compile OK (nothing new is referenced), then most
`TestReconcileHeadless*` tests FAIL --
`TestReconcileHeadlessIdleIsNotBroken` with `state = broken` (the pane path
cannot find an agent). `TestReconcilePanePathUntouchedByHeadless` passes
already.

- [ ] **Step 3: `exitEntry` and `reconcileHeadless`**

Append to `internal/relay/headless.go` (add `"log/slog"`, `"strconv"` and
`"github.com/fuad-daoud/relay/internal/herdr"` to its imports):

```go
// exitEntry is the log record of a headless builder that exited without a
// report (spec §3.8): relay → log only, Confirmed, never a pending payload.
// codeText is the exit code, or "unknown" when the supervisor's trailer is
// missing (killed, or the log unreadable). The payload is the log's last
// logTailLines lines, for the human; relay reads nothing out of it.
func exitEntry(now time.Time, round int, logPath, codeText string) store.LogEntry {
	return store.LogEntry{
		TS:        now,
		Round:     round,
		Direction: store.DirToPlanner,
		Kind:      store.KindExit,
		Path:      logPath,
		Note:      fmt.Sprintf("builder exited (code %s) without a report", codeText),
		Payload:   logTail(logPath, logTailLines),
		Confirmed: true,
	}
}

// reconcileHeadless is one tick of a headless binding (spec §5.1). Reconcile
// hands off here right after the DONE gate; the pane path never runs for a
// headless endpoint and this never runs for a pane one.
//
// Order mirrors the pane path: refresh the planner, halt on the round cap,
// then decide. A report file finishes the round whatever the process did
// (the report is the contract). Otherwise: a gated candidate is switched
// with its process killed; a live process is left alone, its budget the
// only thing that can halt it; a process that exited is logged with its
// code and log tail and the builder is switched, up to max_switches. No
// round open means idle -- never BROKEN -- and a process lingering with no
// round is a stray relay stops.
func reconcileHeadless(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	if planner, ok := FindAgent(agents, b.Planner); ok {
		b.Planner = refreshEndpoint(b.Planner, planner)
	}
	if b.Round > b.RoundCap {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: hit the round cap of %d", b.Name, b.RoundCap))
	}

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}
	roundOpen := HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
		!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport)

	if !roundOpen {
		// Idle is normal (spec §5.1): between rounds there is no process.
		// One with no round is a stray -- a send whose round closed some
		// other way -- and relay stops it rather than let it edit a tree
		// nobody is watching.
		if b.Builder.PID != 0 {
			if rt.Runner != nil {
				if err := rt.Runner.Kill(ctx, handleOf(b.Builder)); err != nil {
					slog.Warn("stray headless process not killed", "binding", b.Name, "pid", b.Builder.PID, "err", err)
				} else {
					slog.Warn("killed stray headless process", "binding", b.Name, "pid", b.Builder.PID)
				}
			}
			b.Builder = clearProcess(b.Builder)
		}
		if b.State == store.StateBroken {
			b.State = store.StateActive
		}
		return deliverAndSettle(ctx, rt, tx, b, agents)
	}

	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if _, err := os.Stat(reportPath); err == nil {
		// The report is the contract (spec §1): the process is done with
		// whatever it exits as, and it exits on its own. Never Kill here.
		payload := fmt.Sprintf("Builder finished round %d. Report: %s", b.Round, reportPath)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, "")
		if err != nil {
			return b, err
		}
		next.Builder = clearProcess(next.Builder)
		return deliverAndSettle(ctx, rt, tx, next, agents)
	}

	if b.Builder.PID == 0 {
		// Round open, no process, no report: a send whose Start failed.
		// Send already put the binding in NEEDS YOU and the ledger has the
		// spawn_failed; nothing to observe until the human acts.
		return b, nil
	}
	if rt.Runner == nil {
		slog.Warn("headless binding but no Runner configured", "binding", b.Name)
		return b, nil
	}

	switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
	if switchable {
		if g, gated := gatedBuilder(rt, b); gated {
			reason := "rate-limited"
			if g.Note != "" {
				reason = "rate-limited: " + g.Note
			}
			return switchBuilder(ctx, rt, tx, b, reason, true)
		}
	}

	alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
	if err != nil {
		// An OS hiccup is not evidence the builder stopped: treat it as
		// alive this tick and say so (spec §6).
		slog.Warn("headless liveness check failed; treating as alive", "binding", b.Name, "pid", b.Builder.PID, "err", err)
		alive = true
	}
	if alive {
		next, halted, err := checkRoundTimeout(ctx, rt, b)
		if halted {
			return next, err // a halt does not deliver, as in the pane path
		}
		if err != nil {
			return b, err
		}
		return deliverAndSettle(ctx, rt, tx, next, agents)
	}

	// Exited without a report.
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), b.Builder.LogPath); ok {
		codeText = strconv.Itoa(code)
	}
	now := rt.Now().UTC()
	if err := tx.AppendLog(b.Name, exitEntry(now, b.Round, b.Builder.LogPath, codeText)); err != nil {
		return b, err
	}
	slog.Info("headless builder exited without a report", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText)
	b.Builder.PID, b.Builder.StartedAt = 0, 0 // LogPath stays: status and the entry point at it
	if !switchable {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: builder exited (code %s) without a report; see %s", b.Name, codeText, b.Builder.LogPath))
	}
	return switchBuilder(ctx, rt, tx, b, fmt.Sprintf("exited (code %s) without a report", codeText), false)
}
```

On the `!switchable` line: a headless binding always has a
`BuilderCandidate` (it cannot adopt a pane), and `RoundStartedAt` is
stamped by every `send`, so this branch is defensive; it exists so a
binding that somehow reaches here does not loop through `switchBuilder`
with nothing to resolve.

Why the exit path does not re-log on a later tick: after it runs, either
`switchBuilder` started a replacement (`PID != 0` again) or halted the
binding at the switch limit -- and in the halt case `PID == 0` with the
round still open, which the `b.Builder.PID == 0` return above catches on
the next tick. `TestReconcileHeadlessExitHaltsAfterMaxSwitches` pins that.

- [ ] **Step 4: The branch in `Reconcile`**

In `internal/relay/reconcile.go`, immediately after

```go
	if b.State == store.StateDone {
		return b, nil
	}
```

insert:

```go
	// A headless builder (#99) is a process, not an agent herdr lists;
	// everything below this line looks for a pane. Spec §5.1 is its own
	// tick.
	if b.Builder.Headless() {
		return reconcileHeadless(ctx, rt, tx, b, agents)
	}
```

Nothing else in `Reconcile` changes.

- [ ] **Step 5: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS -- every new test and every existing one, unmodified. If
anything in `reconcile_test.go` or `switch_test.go` fails, the pane path
moved: stop and report.

- [ ] **Step 6: Mutation checks, then verify and commit**

Two mutations, one at a time, each reverted before the next:

1. In `reconcileHeadless`, replace the whole "Exited without a report"
   block (from `codeText := "unknown"` to the final `return switchBuilder`)
   with `return b, nil`. Run
   `go test ./internal/relay -run 'TestReconcileHeadlessExit'`.
   Expected: `TestReconcileHeadlessExitWithoutReportLogsAndSwitches`,
   `TestReconcileHeadlessExitUnknownCode` and
   `TestReconcileHeadlessExitHaltsAfterMaxSwitches` all FAIL. Revert.
2. In `Reconcile`, delete the `if b.Builder.Headless() { ... }` branch.
   Run `go test ./internal/relay -run 'TestReconcileHeadlessIdleIsNotBroken'`.
   Expected: FAIL with `state = broken`. Revert.

Run `go test -count=1 ./internal/relay` once more: PASS. Say in the report
that both were done and which tests failed under each.

Run: `make check`
Expected: green.

```bash
git add internal/relay/headless.go internal/relay/headless_test.go internal/relay/reconcile.go
git commit -m "feat(relay): daemon headless branch -- report finishes, alive waits, exit logs and switches, idle is not broken (#99 step 6)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), both mutation-check outcomes, and
`git diff --stat main..HEAD`. Confirm `reconcile_test.go` and
`switch_test.go` were not edited. If any step was impossible as written, say
which and why -- do not work around it.
