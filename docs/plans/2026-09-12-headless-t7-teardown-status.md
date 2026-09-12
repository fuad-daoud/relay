# Headless builders, step 7: `done`/`unbind` stop the process; `answer` refuses; `status` and `ui` show the log (#99)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #99. **Spec:** `docs/specs/2026-09-12-headless-builders-design.md`
-- read §1 (scope boundary), §4.6, §4.7, §4.8, §6 and §7 step 7 before
starting; section numbers below refer to it. Steps 1-6 are on `main`; this
plan uses `handleOf`, `clearProcess`, `logTail` from
`internal/relay/headless.go`, `Runner.Alive/ExitCode/Kill`, `fakeRunner`,
and the `seedHeadless`/`sentHeadless` fixtures in `headless_test.go`.
**Depends on:** nothing open.

**Goal:** the human-facing edges of a headless binding behave: `relay done`
and `relay unbind` stop a live round's process (the one place besides the
gated switch where relay stops a process it started); `relay answer` on a
headless binding is refused with a pointer at the log; `relay status` shows
`headless`, the process state, the pid and the log's last lines; the
`relay ui` terminal tab shows the log file instead of a pane.

**Architecture:** four small, independent changes behind `b.Builder.Headless()`.
`Done` (`status.go`) kills inside its lock and clears the process fields
before saving `DONE`; a kill failure is returned *after* the state is
saved, wrapped in `ErrStopFailed`, so the CLI reports it and the binding is
still done. `Unbind` (`bind.go`) kills first, then tears down as today, and
reports the outcome in two new `UnbindResult` fields that `UnbindText`
renders as one extra line. `Answer` refuses before it lists agents.
`statusRow` gains a `ctx` and, for a headless endpoint, fills a new
`BindingStatus.Headless *HeadlessInfo` from `Runner.Alive`/`ExitCode` and
`logTail`; `RenderStatus` prints the §4.8 line and up to three `log` lines.
`ui.fetchTerminal` reads the log file for a headless binding and never
calls herdr.

**Decisions taken by the planner where the spec left room:**

- `Done` keeps its `error`-only signature. Success is silent (the `DoneText`
  line is unchanged, so `pick`'s result screen is unchanged); a kill
  failure comes back as an error *after* `DONE` is saved, so the human sees
  it and the binding is still done. "Reported, not fatal" (§4.6) holds for
  the state, not the exit code.
- `Unbind` reports the stop in `UnbindResult.ProcessStopped` / `ProcessErr`
  and `UnbindText` prints it; existing `UnbindText` output is unchanged
  when both are zero.
- Status liveness is a live `ps` (via `Runner.Alive`) per headless row, the
  same cost class as the herdr list the pane rows already pay. `Runner`
  nil (a runtime without one) shows `unknown`.

**Tech stack:** Go 1.22. Verification is `make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-t7` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-t7`, cut from `main`. |
| `~/.local/state/relay/headless-t7` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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

- Files touched: `internal/relay/status.go`, `bind.go`, `answer.go`,
  `text.go`, `headless.go`, `headless_test.go`, `text_test.go`;
  `internal/ui/fetch.go`, `fetch_test.go`. Nothing else.
- Pane behaviour and output are byte-for-byte unchanged: every existing test
  in `internal/relay`, `internal/ui`, `internal/pick` and `cmd/relay` passes
  **unmodified**. `RenderStatus`'s pane `builder` line and `UnbindText`'s
  existing lines do not change.
- The log is never parsed. `status` and `ui` show it; the exit code comes
  from `Runner.ExitCode` only.
- `go.mod`/`go.sum` do not change.
- One commit per task, on the worktree's branch.

---

### Task 1: `Done` and `Unbind` stop a live process

**Files:**
- Modify: `internal/relay/status.go` (`Done`)
- Modify: `internal/relay/bind.go` (`UnbindResult`, `Unbind`)
- Modify: `internal/relay/text.go` (`UnbindText`)
- Modify: `internal/relay/headless.go` (`ErrStopFailed`, `stopProcess`)
- Modify: `internal/relay/headless_test.go`, `internal/relay/text_test.go` (tests appended)

**Interfaces:**
- Consumes: `handleOf`, `clearProcess`, `Runner.Kill`.
- Produces:

```go
var ErrStopFailed = errors.New("could not stop the builder process")
// stopProcess kills a headless endpoint's live process, if any. Returns the
// pid it addressed (0 when there was none) and Kill's error.
func stopProcess(ctx context.Context, rt Runtime, e store.Endpoint) (pid int, err error)
// UnbindResult gains:
ProcessStopped int    // pid relay stopped, or 0
ProcessErr     string // why the stop failed; "" when it did not
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/headless_test.go`:

```go
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
```

Add `"fmt"` to the file's imports if missing.

Append to `internal/relay/text_test.go`:

```go
func TestUnbindTextProcessLines(t *testing.T) {
	got := UnbindText("x", UnbindResult{ProcessStopped: 4242})
	if got != "unbound x (panes left untouched)\nstopped builder process 4242" {
		t.Errorf("stopped: %q", got)
	}
	got = UnbindText("x", UnbindResult{ArchivedTo: "/a/x.tgz", ProcessErr: "pid 4242: SIGTERM: operation not permitted"})
	if got != "archived x to /a/x.tgz (panes left untouched)\ncould not stop builder process (pid 4242: SIGTERM: operation not permitted); check for it yourself" {
		t.Errorf("failed: %q", got)
	}
	// No process, no line: existing output is unchanged.
	if got := UnbindText("x", UnbindResult{}); got != "unbound x (panes left untouched)" {
		t.Errorf("plain: %q", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/relay -run 'TestDoneHeadless|TestDonePane|TestUnbindHeadless|TestUnbindTextProcess'`
Expected: compile error -- `undefined: ErrStopFailed`, `unknown field ProcessStopped`.

- [ ] **Step 3: `ErrStopFailed` and `stopProcess`**

Append to `internal/relay/headless.go`:

```go
// ErrStopFailed reports that relay marked a binding done or unbound but
// could not stop its headless process (spec §4.6). The state change stands;
// the pid stays on the endpoint (done) or in the result (unbind) so the
// human can find the process.
var ErrStopFailed = errors.New("could not stop the builder process")

// stopProcess kills a headless endpoint's live process, if it has one. It
// returns the pid it addressed -- 0 when there was nothing to stop -- and
// Kill's error. A pane endpoint, or a headless one between rounds, is a
// no-op. Runner nil with a pid recorded is an error: relay cannot say the
// process is stopped.
func stopProcess(ctx context.Context, rt Runtime, e store.Endpoint) (int, error) {
	if !e.Headless() || e.PID == 0 {
		return 0, nil
	}
	if rt.Runner == nil {
		return e.PID, ErrRunnerUnavailable
	}
	if err := rt.Runner.Kill(ctx, handleOf(e)); err != nil {
		return e.PID, err
	}
	return e.PID, nil
}
```

- [ ] **Step 4: `Done`**

In `internal/relay/status.go`, `Done`'s lock body currently reads:

```go
		oldState := b.State
		b.State = store.StateDone

		if err := tx.Save(b); err != nil {
			return err
		}
```

Change it to:

```go
		oldState := b.State
		b.State = store.StateDone

		// A headless round's process is stopped here (#99, spec §4.6): the
		// planner has declared the work finished, so a builder still
		// editing the tree is now the wrong thing. Failure is reported
		// after DONE is saved -- the state change stands either way -- and
		// the pid stays on the endpoint so the human can find it.
		pid, stopErr := stopProcess(ctx, rt, b.Builder)
		if stopErr == nil {
			b.Builder = clearProcess(b.Builder)
		}

		if err := tx.Save(b); err != nil {
			return err
		}
```

and at the end of the lock body, replace the final `return nil` with:

```go
		if stopErr != nil {
			return fmt.Errorf("%s marked done, but its builder process %d is still running: %v: %w", b.Name, pid, stopErr, ErrStopFailed)
		}
		return nil
```

`fmt` is already imported by `status.go`. `pid` and `stopErr` are used only
on that path; if the compiler complains `pid declared and not used`, the
`return` line above did not land -- fix that rather than blanking `pid`.

- [ ] **Step 5: `Unbind` and `UnbindText`**

In `internal/relay/bind.go`, add to `UnbindResult` after `WorktreeGone`:

```go
	// ProcessStopped is the headless builder process relay stopped, or 0
	// (#99). ProcessErr is why a stop failed; "" when it did not. Both
	// zero for a pane binding and for a headless one between rounds.
	ProcessStopped int
	ProcessErr     string
```

In `Unbind`, after the `rt.Store.Load` and before `worktreeTeardown`, insert:

```go
	var res UnbindResult
	// Stop a live headless round before touching its tree (#99, spec §4.6):
	// a builder still writing would dirty the worktree relay is about to
	// judge clean or not. A failed stop is reported, never fatal -- the
	// unbind is the human's decision and it proceeds.
	if pid, err := stopProcess(ctx, rt, b.Builder); err != nil {
		res.ProcessErr = fmt.Sprintf("pid %d: %v", pid, err)
	} else if pid != 0 {
		res.ProcessStopped = pid
	}
```

and remove the existing `var res UnbindResult` line that follows (there
must be exactly one declaration).

In `internal/relay/text.go`, `UnbindText`: before the final
`return strings.Join(lines, "\n")`, add:

```go
	switch {
	case res.ProcessStopped != 0:
		lines = append(lines, fmt.Sprintf("stopped builder process %d", res.ProcessStopped))
	case res.ProcessErr != "":
		lines = append(lines, fmt.Sprintf("could not stop builder process (%s); check for it yourself", res.ProcessErr))
	}
```

Update `UnbindText`'s doc comment to "one line for the binding, then at
most one for its worktree, then at most one for a headless process".

- [ ] **Step 6: Run the tests**

Run: `go test -count=1 ./internal/relay ./internal/pick`
Expected: PASS -- the seven new tests and every existing one, including
`TestUnbindText`, `TestDoneText` and the `pick` package (whose result
texts are unchanged for the pane fixtures).

- [ ] **Step 7: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/relay/status.go internal/relay/bind.go internal/relay/text.go internal/relay/headless.go internal/relay/headless_test.go internal/relay/text_test.go
git commit -m "feat(relay): done/unbind stop a headless round's process; failure reported, not fatal (#99 step 7)"
```

---

### Task 2: `Answer` refuses a headless builder

**Files:**
- Modify: `internal/relay/answer.go`
- Modify: `internal/relay/headless_test.go` (test appended)

**Interfaces:**
- Produces: `var ErrHeadlessNoDialog = errors.New("builder is headless and takes no dialogs")`.

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/headless_test.go`:

```go
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
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/relay -run TestAnswerRefusesAHeadless`
Expected: compile error, `undefined: ErrHeadlessNoDialog`.

- [ ] **Step 3: Implement**

In `internal/relay/answer.go`, add near the other sentinels (after
`ErrBuilderNotBlocked` in `send.go` is where they live -- put this one at
the top of `answer.go` instead, so the file that refuses owns the error):

```go
// ErrHeadlessNoDialog: a headless builder (#99) runs with stdin closed and
// no terminal; there is no dialog to answer. What it printed is in its log.
var ErrHeadlessNoDialog = errors.New("builder is headless and takes no dialogs")
```

Add `"errors"` to `answer.go`'s imports if it is not already there.

In `Answer`, the pre-lock block begins:

```go
	if hint, err := rt.Store.Load(name); err == nil {
		agents, err := rt.Herdr.ListAgents(ctx)
```

Insert the refusal as the first thing inside that `if`:

```go
	if hint, err := rt.Store.Load(name); err == nil {
		if hint.Builder.Headless() {
			where := "no round is running"
			if hint.Builder.LogPath != "" {
				where = "read " + hint.Builder.LogPath
			}
			return fmt.Errorf("%s's builder is headless and takes no dialogs; %s: %w", name, where, ErrHeadlessNoDialog)
		}
		agents, err := rt.Herdr.ListAgents(ctx)
```

- [ ] **Step 4: Run the tests, verify, commit**

Run: `go test -count=1 ./internal/relay`
Expected: PASS.

Run: `make check`
Expected: green.

```bash
git add internal/relay/answer.go internal/relay/headless_test.go
git commit -m "feat(relay): answer refuses a headless builder, pointing at its log (#99 step 7)"
```

---

### Task 3: `status` -- the headless builder line and the log tail

**Files:**
- Modify: `internal/relay/status.go` (`HeadlessInfo`, `BindingStatus.Headless`, `statusRow`, `RenderStatus`)
- Modify: `internal/relay/headless.go` (`headlessStatus`, `statusTailLines`)
- Modify: `internal/relay/headless_test.go` (tests appended)

**Interfaces:**
- Consumes: `Runner.Alive`, `Runner.ExitCode`, `logTail`, `handleOf`.
- Produces:

```go
// HeadlessInfo is the process half of a headless builder's status row.
type HeadlessInfo struct {
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at"`        // zero when idle
	LogPath   string    `json:"log_path,omitempty"`
	ExitCode  string    `json:"exit_code,omitempty"` // "3", or "unknown", when exited; "" otherwise
	Tail      []string  `json:"tail,omitempty"`      // last statusTailLines of the log
}
// BindingStatus gains:
Headless *HeadlessInfo `json:"headless,omitempty"` // nil for a pane builder
const statusTailLines = 3
// headlessStatus is the builder status word and the info for a headless endpoint.
func headlessStatus(ctx context.Context, rt Runtime, e store.Endpoint) (status string, info *HeadlessInfo)
```

`statusRow` gains `ctx context.Context` as its first parameter.

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/headless_test.go`:

```go
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
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/relay -run 'TestStatusHeadless|TestStatusPaneRow'`
Expected: compile error -- `row.Headless undefined`.

- [ ] **Step 3: `headlessStatus`**

Append to `internal/relay/headless.go` (add `"strconv"` to imports if it is
not there from step 6):

```go
// statusTailLines is how much of the log `relay status` shows under a
// headless builder line (spec §4.8).
const statusTailLines = 3

// headlessStatus is what `relay status` says about a headless endpoint: the
// status word that sits where a pane's herdr status sits, and the process
// details. Idle between rounds; otherwise a live Alive check -- the same
// cost class as the herdr list pane rows pay -- then, for an exited
// process, the trailer's code. No Runner means relay cannot say.
func headlessStatus(ctx context.Context, rt Runtime, e store.Endpoint) (string, *HeadlessInfo) {
	info := &HeadlessInfo{PID: e.PID, LogPath: e.LogPath}
	if e.StartedAt != 0 {
		info.StartedAt = time.Unix(e.StartedAt, 0)
	}
	if e.LogPath != "" {
		if tail := logTail(e.LogPath, statusTailLines); tail != "" {
			info.Tail = strings.Split(tail, "\n")
		}
	}
	if e.PID == 0 {
		return "idle", info
	}
	if rt.Runner == nil {
		return "unknown", info
	}
	alive, err := rt.Runner.Alive(ctx, handleOf(e))
	if err != nil {
		return "unknown", info
	}
	if alive {
		return "working", info
	}
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(e), e.LogPath); ok {
		info.ExitCode = strconv.Itoa(code)
		return "exited " + info.ExitCode, info
	}
	info.ExitCode = "unknown"
	return "exited", info
}
```

- [ ] **Step 4: `BindingStatus`, `statusRow`, `RenderStatus`**

In `internal/relay/status.go`:

Add the type after `BindingStatus`'s declaration block (anywhere at file
scope):

```go
// HeadlessInfo is the process half of a headless builder's status row
// (#99, spec §4.8). Nil on a pane row.
type HeadlessInfo struct {
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at"` // zero when idle
	LogPath   string    `json:"log_path,omitempty"`
	// ExitCode is "3", or "unknown" when the process is gone without a
	// trailer; "" while running or idle.
	ExitCode string `json:"exit_code,omitempty"`
	// Tail is the log's last few lines, for the human. Never parsed.
	Tail []string `json:"tail,omitempty"`
}
```

Add to `BindingStatus`, after `BuilderStatus`:

```go
	// Headless is set for a headless builder (#99): its process state and
	// log. BuilderPane reads "headless" and BuilderStatus is one of idle,
	// working, exited N, exited, unknown. Nil for a pane builder.
	Headless *HeadlessInfo `json:"headless,omitempty"`
```

Change `statusRow`'s signature to
`func statusRow(ctx context.Context, rt Runtime, b store.Binding, agents []herdr.Agent, known []store.Endpoint) (BindingStatus, error)`
and its one call site in `Status` to `statusRow(ctx, rt, b, agents, known)`.
(If a test calls `statusRow` directly, pass `context.Background()`; do not
otherwise edit it.)

In `statusRow`, replace

```go
	if a, ok := FindAgent(agents, b.Builder); ok {
		row.BuilderStatus = effectiveStatus(b.Builder, a)
		row.BuilderPane = a.PaneID
	}
```

with

```go
	if b.Builder.Headless() {
		row.BuilderPane = "headless"
		row.BuilderStatus, row.Headless = headlessStatus(ctx, rt, b.Builder)
	} else if a, ok := FindAgent(agents, b.Builder); ok {
		row.BuilderStatus = effectiveStatus(b.Builder, a)
		row.BuilderPane = a.PaneID
	}
```

In `RenderStatus`, replace

```go
		fmt.Fprintf(&sb, "  builder  %-14s %-8s %-9s `%s`",
			b.BuilderPane, b.BuilderKind, b.BuilderStatus, b.BuilderCandidate)
		if b.Switches > 0 {
			fmt.Fprintf(&sb, "   switched %dx", b.Switches)
		}
		fmt.Fprint(&sb, "\n")
```

with

```go
		if b.Headless != nil {
			// spec §4.8: builder  headless  <kind>  <status>  [pid P since HH:MM]  `<token>`
			fmt.Fprintf(&sb, "  builder  %-14s %-8s %-9s", "headless", b.BuilderKind, b.BuilderStatus)
			if b.Headless.PID != 0 {
				fmt.Fprintf(&sb, " pid %d since %s ", b.Headless.PID, b.Headless.StartedAt.Local().Format("15:04"))
			} else {
				fmt.Fprint(&sb, " ")
			}
			fmt.Fprintf(&sb, "`%s`", b.BuilderCandidate)
		} else {
			fmt.Fprintf(&sb, "  builder  %-14s %-8s %-9s `%s`",
				b.BuilderPane, b.BuilderKind, b.BuilderStatus, b.BuilderCandidate)
		}
		if b.Switches > 0 {
			fmt.Fprintf(&sb, "   switched %dx", b.Switches)
		}
		fmt.Fprint(&sb, "\n")
		if b.Headless != nil {
			for _, line := range b.Headless.Tail {
				fmt.Fprintf(&sb, "  log      %s\n", line)
			}
		}
```

The pane branch is the existing `Fprintf` verbatim, so pane output does not
change. Note the headless line for `idle` renders as
`  builder  headless       agy      idle      ` + "`agy/test/m`" -- the
`%-9s` pads `idle` to nine columns and the single space follows, which is
what the test's `"  builder  headless       agy      idle"` prefix checks.

- [ ] **Step 5: Run the tests**

Run: `go test -count=1 ./internal/relay ./internal/pick ./internal/ui ./cmd/relay`
Expected: PASS everywhere. `pick`'s `renderRow` uses `BuilderCandidate` and
`BuilderStatus` and needs no change; a headless row reads
`webshop  ACTIVE  r1  builder agy/test/m working`.

- [ ] **Step 6: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/relay/status.go internal/relay/headless.go internal/relay/headless_test.go
git commit -m "feat(relay): status shows a headless builder -- state, pid, log tail (#99 step 7)"
```

---

### Task 4: `relay ui` terminal tab reads the log

**Files:**
- Modify: `internal/ui/fetch.go` (`fetchTerminal`)
- Modify: `internal/ui/fetch_test.go` (tests appended)

**Interfaces:**
- Consumes: `store.Endpoint.Headless()`, `Endpoint.LogPath`.
- Produces: no new names.

- [ ] **Step 1: Write the failing tests**

Append to `internal/ui/fetch_test.go`:

```go
func TestFetchTerminalHeadlessReadsTheLogNotHerdr(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	logPath := filepath.Join(t.TempDir(), "002-builder.log")
	if err := os.WriteFile(logPath, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newTestBinding("webshop")
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless, PID: 4242, StartedAt: 1_700_000_000, LogPath: logPath}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	msg := fetchTerminal(context.Background(), rt, "webshop", 3)()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil || tMsg.content.empty != "" {
		t.Fatalf("content = %+v, want a body", tMsg.content)
	}
	if tMsg.content.body != "c\nd\ne" {
		t.Errorf("body = %q, want the last three lines", tMsg.content.body)
	}
	if fh.readCalls != 0 {
		t.Errorf("a headless builder has no pane to read: readCalls = %d", fh.readCalls)
	}
}

func TestFetchTerminalHeadlessIdleAndMissingLog(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	b := newTestBinding("webshop")
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tMsg := fetchTerminal(context.Background(), rt, "webshop", 24)().(tabMsg)
	if tMsg.content.empty != "headless builder; no round is running, so there is no log yet" {
		t.Errorf("idle: empty = %q", tMsg.content.empty)
	}

	b.Builder.PID = 4242
	b.Builder.LogPath = filepath.Join(t.TempDir(), "absent.log")
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tMsg = fetchTerminal(context.Background(), rt, "webshop", 24)().(tabMsg)
	if !strings.HasPrefix(tMsg.content.empty, "log not written yet: ") || !strings.Contains(tMsg.content.empty, b.Builder.LogPath) {
		t.Errorf("missing log: empty = %q", tMsg.content.empty)
	}
	if fh.readCalls != 0 {
		t.Errorf("readCalls = %d, want 0", fh.readCalls)
	}
}
```

Add `"os"`, `"path/filepath"` and `"strings"` to the test file's imports
where missing.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/ui -run 'TestFetchTerminalHeadless'`
Expected: FAIL -- the first with `builder gone` in `empty` (the pane path
cannot find an agent), the second likewise.

- [ ] **Step 3: Implement**

In `internal/ui/fetch.go`, `fetchTerminal`, right after the `rt.Store.Load`
error check (before `rt.Herdr.ListAgents`), insert:

```go
		// A headless builder (#99) has no pane; its output is the round's
		// log file. Shown, never parsed.
		if b.Builder.Headless() {
			if b.Builder.LogPath == "" {
				return tabMsg{
					name: name,
					t:    tabTerminal,
					content: tabContent{
						loaded: true,
						empty:  "headless builder; no round is running, so there is no log yet",
					},
				}
			}
			data, err := os.ReadFile(b.Builder.LogPath)
			if err != nil {
				return tabMsg{
					name: name,
					t:    tabTerminal,
					content: tabContent{
						loaded: true,
						empty:  "log not written yet: " + b.Builder.LogPath,
					},
				}
			}
			body := strings.TrimRight(string(data), "\n")
			if all := strings.Split(body, "\n"); len(all) > lines {
				body = strings.Join(all[len(all)-lines:], "\n")
			}
			return tabMsg{
				name: name,
				t:    tabTerminal,
				content: tabContent{
					loaded: true,
					body:   body,
				},
			}
		}
```

Add `"os"` and `"strings"` to `fetch.go`'s imports if missing. Update
`fetchTerminal`'s doc comment to "resolves the builder agent and reads its
recent output, or -- for a headless builder -- the tail of its round log."

- [ ] **Step 4: Run the tests**

Run: `go test -count=1 ./internal/ui`
Expected: PASS -- both new tests and every existing one
(`TestFetchTerminalBuilderAbsent`, `...Present`, `...AddressesLocatedAgent`
unmodified).

- [ ] **Step 5: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/ui/fetch.go internal/ui/fetch_test.go
git commit -m "feat(ui): terminal tab shows a headless builder's log (#99 step 7)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), and `git diff --stat main..HEAD`. Confirm no
existing test was edited. If any step was impossible as written, say which
and why -- do not work around it.
