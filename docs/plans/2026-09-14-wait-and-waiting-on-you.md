# Wait and waiting-on-you: `relay wait` and the blind-turn lines (#127, #128)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-14-wait-and-waiting-on-you-design.md`. Section
numbers below (§) refer to it.
**Issues:** #127, #128. Closes both.
**Depends on:** nothing open.

**Goal:** A binding that is NEEDS YOU can say what it is waiting on and
since when: `haltBinding` records its reason in two new fields (`Halt`,
`HaltAt`), and a pure classifier `WaitingOn` turns a binding plus its log
into a `Waiting` value (blocked / halted / broken / orphaned) with a
one-line reason and the verb that resolves it. Two readers: `relay wait`
polls the store (never herdr) and exits 0/2/3/4/124 the moment a round
closes or needs a human; every mutating verb prints one stderr line per
*other* binding that is waiting on a human, exit code unchanged.

**Architecture:** `store.Binding` gains `Halt string` and `HaltAt
time.Time` (omitempty), written in `haltBinding` and in `Send`'s headless
spawn-failure branch, cleared at round close, `bind --resume --rebind` and
a successful `Send`. Two new files in `internal/relay`: `waiting.go`
(`Waiting`, `WaitingOn`, `questionFirstLine`, `WaitingLine`,
`WaitingOnYou`) and `wait.go` (`WaitResult`, the `Wait*` exit constants,
`WaitOptions`, `DefaultWaitRound`, `WaitOutcome`, `Wait`). `cmd/relay`
gains `cmdWait` and `warnWaitingOnYou`, the latter called from seven
verbs. No herdr call is added anywhere; no prompt text changes.

**Tech stack:** Go 1.22. Verification is the `make check` constituent set
(runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/wait-guard` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/wait-guard`, cut from `main`. |
| `~/.local/state/relay/wait-guard` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Resuming a round another builder started

The branch may already carry commits from an earlier builder on this same
round. Before Task 1, run `git log --oneline main..HEAD`. A task whose
commit message is already there is **done**: run its Step 4 command to
confirm it is green, tick it, and continue with the next task. Do not redo
it, do not amend it. An untracked or modified file from an unfinished task
is yours to finish or replace as that task's steps say.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` or `make e2e` yourself. **No test is added under
`cmd/relay`** (CI runners have no `herdr`; CLAUDE.md). Every new test is a
pure function or a store-backed function in `internal/relay`, using the
existing `fakeHerdr`, `newRuntime`, `seedBound`, `sentBinding`,
`sentSwitchable`, `seedHeadless`, `withClock`/`fakeClock`, `at` and
`reconcile` helpers in `fake_test.go`, `reconcile_test.go`,
`switch_test.go` and `headless_test.go`.

## Global constraints

- Neither `waiting.go` nor `wait.go` imports `internal/herdr` or touches
  `rt.Herdr`. `Wait` and `WaitingOnYou` read the store (`Load`, `List`,
  `ReadLog`, `QuestionPath`) and nothing else (§1 scope, §4.4, §4.7).
- `WaitingOn` checks `blocked` before `halted` (§4.1) and returns `!ok`
  for a switchable `broken` binding (§1 decision 5).
- `WaitOutcome` checks the report entry **before** `State == done` and
  before `WaitingOn` (§4.6).
- `DefaultWaitRound` is the newest `to_builder`/`plan` entry's round,
  excluding nudges (`Note == nudgeNote`), else `b.Round` (§4.5).
- `Halt`/`HaltAt` are written inside `haltBinding`'s existing
  `HaltNotifiedRound != b.Round` guard, never outside it (§4.10).
- `warnWaitingOnYou` never changes a verb's return value or exit code
  (§4.9).
- `relay wait`'s exit code is `res.Code` exactly; `0` is a `nil` return,
  every other code is `exitCodeErr{code}` (§4.8).
- The line format is exactly §4.3's: `waiting on you: <name> round <N>
  <cause>[ <age>] -- <line>  (<hint>)` -- two spaces before the
  parenthesised hint.
- `internal/relay/e2e_test.go` is not edited. `builderPrompt` and every
  payload string in `send.go`, `reconcile.go`, `deliver.go` are not
  edited.
- One commit per task, on the worktree's branch.

---

### Task 1: `Binding.Halt` / `HaltAt`, written by `haltBinding`, cleared at round close

**Files:**
- Modify: `internal/store/types.go` (two fields after `HaltNotifiedRound`, with the doc comment from §3.1)
- Modify: `internal/relay/reconcile.go` (`haltBinding`; the close block that zeroes `HaltNotifiedRound`)
- Modify: `internal/relay/reconcile_blocked_test.go` (`TestReconcileFlagsRoundTimeout`)
- Modify: `internal/relay/switch_test.go` (`TestExhaustionHalts`)
- Modify: `internal/relay/headless_test.go` (`TestCloseOnMarkerWithReportClosesNormally`)

**Interfaces:**
- Produces: `store.Binding.Halt string` (`json:"halt,omitempty"`), `store.Binding.HaltAt time.Time` (`json:"halt_at,omitempty"`).
- `haltBinding` keeps its signature. Inside the `HaltNotifiedRound != b.Round` block, after the notify: `b.Halt = strings.TrimPrefix(message, b.Name+": ")`; `b.HaltAt = rt.Now().UTC()`.
- The close block in `queueReport` (the one setting `b.HaltNotifiedRound = 0`) also sets `b.Halt = ""` and `b.HaltAt = time.Time{}`.

- [ ] **Step 1: Write the assertions.**
  - `TestReconcileFlagsRoundTimeout`: after the first tick, `got.Halt == "round 1 has run past 30m0s"` and `got.HaltAt.Equal(rt.Now().UTC())`; after the second tick, `HaltAt` is unchanged (compare with the value from the first tick -- the fixed clock makes this trivially equal, so also assert via `at(rt, time.Minute)` for the second tick that `HaltAt` did **not** move to the later clock).
  - `TestExhaustionHalts`: on the final halted binding, `strings.Contains(got.Halt, "max_switches")` and `!got.HaltAt.IsZero()`.
  - `TestCloseOnMarkerWithReportClosesNormally`: before the closing tick set `b.Halt = "stale"`, `b.HaltAt = rt.Now()`; after the close assert `got.Halt == ""` and `got.HaltAt.IsZero()`.

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'TestReconcileFlagsRoundTimeout|TestExhaustionHalts|TestCloseOnMarkerWithReportClosesNormally'`: compile error, `Halt` undefined.

- [ ] **Step 3: Implement** the fields, the write, the clear.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/store ./internal/relay`. Expected: PASS. Also confirm `go test -count=1 ./internal/store -run TestBindingJSON` (or whatever test round-trips a binding; if none compares raw bytes, skip) still passes -- omitempty keeps old files byte-identical.

- [ ] **Step 5: Commit**

```bash
git add internal/store/types.go internal/relay/reconcile.go internal/relay/reconcile_blocked_test.go internal/relay/switch_test.go internal/relay/headless_test.go
git commit -m "feat(relay): a halt records its reason and time on the binding (#127, #128)"
```

---

### Task 2: `Send` writes the halt on spawn failure and clears it on success; `bind --resume --rebind` clears it

**Files:**
- Modify: `internal/relay/send.go` (the headless `startRound` failure branch; the success block that sets `b.State = store.StateActive`)
- Modify: `internal/relay/bind.go` (the `rebinding` block that zeroes `HaltNotifiedRound`)
- Modify: `internal/relay/headless_test.go` (`TestSendHeadlessStartFailureGoesNeedsYou`; `TestSendHeadlessStartsAgainOnceThePreviousProcessExited` or a new sibling)
- Modify: `internal/relay/bind_test.go` (`TestBindRebindWithGoneBuilder`)

**Interfaces:**
- `Send`, failure branch: `b.Halt = "builder spawn failed: " + err.Error()`; `b.HaltAt = rt.Now().UTC()` before the `tx.Save(b)` that records NEEDS YOU.
- `Send`, success: alongside `b.State = store.StateActive`, `b.Halt = ""`, `b.HaltAt = time.Time{}`.
- `Bind`, `rebinding` block: same two zeroings next to `b.HaltNotifiedRound = 0`.

- [ ] **Step 1: Write the assertions.**
  - `TestSendHeadlessStartFailureGoesNeedsYou`: `strings.HasPrefix(b.Halt, "builder spawn failed: ")` and `strings.Contains(b.Halt, "not found on PATH")`; `!b.HaltAt.IsZero()`.
  - New `TestSendClearsAStaleHalt`: `seedBound` (pane path is fine), set `b.Halt = "round 1 has run past 1s"`, `b.HaltAt = rt.Now()`, `b.State = store.StateNeedsYou`, save; `Send`; reload; `Halt == ""`, `HaltAt.IsZero()`, `State == active`.
  - In `TestBindRebindWithGoneBuilder`: seed `Halt`/`HaltAt` on the binding before `Bind(... Resume, Rebind)`; assert both are zero after.

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'TestSendHeadlessStartFailureGoesNeedsYou|TestSendClearsAStaleHalt|Rebind'`.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/send.go internal/relay/bind.go internal/relay/headless_test.go internal/relay/bind_test.go
git commit -m "feat(relay): send and rebind keep the halt record honest (#127, #128)"
```

---

### Task 3: `Waiting`, `WaitingOn`, `questionFirstLine`, `WaitingLine`

**Files:**
- Create: `internal/relay/waiting.go`
- Create: `internal/relay/waiting_test.go`

**Interfaces:**
- Produces `type Waiting struct { Name string; Round int; Cause string; Line string; Since time.Time; Hint string }` (§3.2), with a doc comment per field.
- Produces `func WaitingOn(b store.Binding, entries []store.LogEntry, questionOf func(name string, round int) string) (Waiting, bool)` exactly per the §4.1 table. Switchable is `b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()` -- the same expression `Reconcile` uses; write it once as an unexported `switchable(b)` helper in `waiting.go` and leave `reconcile.go` alone (it stays inline there; do not refactor it).
- Produces `func questionFirstLine(rt Runtime) func(name string, round int) string` (§4.2): reads `rt.Store.QuestionPath(name, round)`, returns the first non-blank line trimmed, `""` on any error.
- Produces `func WaitingLine(w Waiting, now time.Time) string` (§4.3), using `AgeText`.
- Unexported `capLine(s string, n int) string`: first non-blank line, `strings.TrimSpace`, truncated to `n` runes with a trailing `…` when longer. `WaitingOn` applies `capLine(_, 120)` to every `Line` it produces.

- [ ] **Step 1: Write `TestWaitingOn`** as a table over `store.Binding` + `[]store.LogEntry` + a `map[string]string` question source, covering every row of §8's first bullet: active/held/done -> `!ok`; needs_you + question entry -> blocked, line from the map, `Since` = entry TS, hint `relay answer --name api`; needs_you + question entry + `Halt` -> blocked (order); needs_you + `Halt` only -> halted, `Since == HaltAt`, hint `relay status --name api`; needs_you with neither -> cause `needs you`, line `no reason recorded (binding predates the halt record)`, zero `Since`; broken + switchable (`BuilderCandidate: "agy/x/y"`, `RoundStartedAt` set) -> `!ok`; broken + `BuilderCandidate == ""` + `Builder.AgentName == "b"` -> broken, line `== DiagnoseBuilder(b).Detail(b.Round)`, hint `relay bind --resume --name api --rebind`, `Since == BuilderMissingSince`; broken + unidentified -> hint `relay status --name api`; broken + candidate set but `RoundStartedAt` zero -> broken; orphaned -> orphaned, `planner pane is gone`, hint `relay bind --resume --name api`; question map entry of 200 `x` runes -> `Line` is 120 runes ending in `…`; question map entry `"\n\n  second  \n"` -> `Line == "second"`; question map returns `""` -> `Line == "dialog captured at " + <the path the test computes from a real store's QuestionPath>` (pass a `rt.Store` into the test for the path, or assert `strings.HasPrefix(line, "dialog captured at ")` and `strings.HasSuffix(line, "001-question.md")`).

- [ ] **Step 2: Write `TestQuestionFirstLine`**: over a `newRuntime` store, write a question file with a blank first line and `Do you want to proceed?` second; expect that; a missing file -> `""`.

- [ ] **Step 3: Write `TestWaitingLine`**: the three exact strings from §4.3 (construct `now` so the ages are `23m` and `1h 5m`); a zero `Since` yields no age and a single space between cause and `--`.

- [ ] **Step 4: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'TestWaitingOn|TestQuestionFirstLine|TestWaitingLine'`: compile error.

- [ ] **Step 5: Implement `waiting.go`.** File header comment: `// Package relay waiting: who is waiting on a human, and why, per docs/specs/2026-09-14-wait-and-waiting-on-you-design.md §4.1-4.4.`

- [ ] **Step 6: Run** `go test -race -count=1 ./internal/relay -run 'TestWaitingOn|TestQuestionFirstLine|TestWaitingLine'`. Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/relay/waiting.go internal/relay/waiting_test.go
git commit -m "feat(relay): WaitingOn classifies a NEEDS YOU binding into one line (#127, #128)"
```

---

### Task 4: `WaitingOnYou`

**Files:**
- Modify: `internal/relay/waiting.go`
- Modify: `internal/relay/waiting_test.go`

**Interfaces:**
- Produces `func WaitingOnYou(rt Runtime, except string) ([]string, error)` (§4.4): `rt.Store.List()`; skip `b.Name == except` and `b.State == store.StateDone`; `ReadLog`; `WaitingOn(b, entries, questionFirstLine(rt))`; `WaitingLine(w, rt.Now().UTC())`. Order = `List()` order. A `List` or `ReadLog` error is returned as is.

- [ ] **Step 1: Write `TestWaitingOnYou`**: over one `newRuntime` store, save four bindings by hand (`rt.Store.Save`): `a` needs_you with a question entry appended via `AppendLog` and a question file written; `b` active; `c` done; `d` needs_you with `Halt` set. Call `WaitingOnYou(rt, "d")`. Expect exactly one line, starting `waiting on you: a round `, containing the question's first line and `(relay answer --name a)`. Call again with `except == ""`: two lines, `a` then `d`.

- [ ] **Step 2: Run to verify it fails.**

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay -run TestWaitingOnYou`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/waiting.go internal/relay/waiting_test.go
git commit -m "feat(relay): WaitingOnYou lists the other bindings waiting on a human (#128)"
```

---

### Task 5: `WaitResult`, `DefaultWaitRound`, `WaitOutcome`

**Files:**
- Create: `internal/relay/wait.go`
- Create: `internal/relay/wait_test.go`

**Interfaces:**
- Produces `type WaitResult struct { Code int; Line string; Done bool }` and the constants `WaitClosed = 0`, `WaitUnmarked = 2`, `WaitNeedsYou = 3`, `WaitGone = 4`, `WaitTimeout = 124` (§3.3), each with a one-line doc comment.
- Produces `func DefaultWaitRound(b store.Binding, entries []store.LogEntry) int` (§4.5).
- Produces `func WaitOutcome(b store.Binding, entries []store.LogEntry, round int, questionOf func(name string, round int) string) WaitResult` (§4.6). "Report entry for round" is `e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport`; take the **last** such entry if several exist.

- [ ] **Step 1: Write `TestDefaultWaitRound`**: no entries, `Round: 1` -> 1; plan entry round 3, `Round: 3` -> 3; plan entry round 3 and report entry round 3, `Round: 4` -> 3; plus a plan entry round 4 with `Note: nudgeNote`, `Round: 4` -> 3 (a nudge is not a send).

- [ ] **Step 2: Write `TestWaitOutcome`** as a table per §8: report note `""` -> `{0, path, true}`; `unmarked` -> `{2, path, true}`; `scraped` -> `{2, path, true}`; `noreport` -> `{2, "-", true}`; DONE, no report -> `{4, "", true}`; needs_you with `Halt` -> `{3, <Halt>, true}`; active, open round, no report -> `Done == false`; DONE **with** a report for the asked round -> `{0, path, true}`; broken (unswitchable) with a report for round 1 asked for round 1 -> `{0, path, true}`; the same binding asked for round 2 (no report, `Round: 2`) -> `{3, ..., true}`.

- [ ] **Step 3: Run to verify they fail.**

- [ ] **Step 4: Implement `wait.go`.** Header comment names the spec §3.3-4.7.

- [ ] **Step 5: Run** `go test -race -count=1 ./internal/relay -run 'TestDefaultWaitRound|TestWaitOutcome'`. Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/wait.go internal/relay/wait_test.go
git commit -m "feat(relay): WaitOutcome classifies a round into wait's exit codes (#127)"
```

---

### Task 6: `Wait`, the loop

**Files:**
- Modify: `internal/relay/wait.go`
- Modify: `internal/relay/wait_test.go`

**Interfaces:**
- Produces `type WaitOptions struct { Names []string; Round int; Timeout, Interval time.Duration }` (§3.4).
- Produces `func Wait(ctx context.Context, rt Runtime, opts WaitOptions) (name string, res WaitResult, err error)` (§4.7). Validation: `len(Names) == 0`, `Timeout <= 0` or `Interval <= 0` -> `fmt.Errorf("wait: ...")`. Entry: `Load` each name (any error, including `ErrNotFound`, is returned); compute `rounds[name]` once. Loop: the pass, then `select { case <-ctx.Done(): return "", WaitResult{}, ctx.Err(); case <-time.After(opts.Interval): }`; the deadline is `start := rt.Now()` and `rt.Now().Sub(start) >= opts.Timeout` checked **after** each pass and before the sleep, so the first pass always runs.

- [ ] **Step 1: Write the tests.** All use `Interval: time.Millisecond` unless stated, `context.WithTimeout(context.Background(), 5*time.Second)` as a guard, and a `sentBinding` (`webshop`, round 1 open) as the base.
  - `TestWaitReturnsAtOnceWhenAlreadyClosed`: append a report entry for round 1 (`Confirmed: true`, note `""`), `Interval: time.Hour`; `Wait` returns `("webshop", {0, path, true}, nil)` well inside the 5s guard -- if the loop slept first, the guard fires and the test fails with `context deadline exceeded`.
  - `TestWaitAnyReturnsTheFirstThatCloses`: two bindings (`seedBound` twice with different names/cwds, or `sentBinding` + `Add`-free manual `Save`), report entry only on the second; `Names: [first, second]` -> name is the second.
  - `TestWaitGoneWhenUnboundMidWait`: `rt.Now` replaced by a closure counting calls; on its 4th call it runs `rt.Store.Delete("webshop")` (or `os.RemoveAll(rt.Store.Dir("webshop"))`) before returning the fixed time. Expect `("webshop", {4, "", true}, nil)`.
  - `TestWaitTimesOut`: `rt.Now` closure that advances one minute per call; `Timeout: 5 * time.Minute`; expect `("", {124, "", true}, nil)`.
  - `TestWaitNamesUnknownBindingIsAnError`: `Names: ["nope"]` -> `errors.Is(err, store.ErrNotFound)`.
  - `TestWaitDefaultRoundIsTheNewestPlanned`: close round 1 (report entry), leave `b.Round = 2` with no plan entry for 2; `Round: 0` -> exit 0 for round 1's report (not a timeout).

- [ ] **Step 2: Run to verify they fail.**

- [ ] **Step 3: Implement `Wait`.**

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay -run TestWait`. Expected: PASS, and the whole package still green: `go test -race -count=1 ./internal/relay`.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/wait.go internal/relay/wait_test.go
git commit -m "feat(relay): Wait polls the store until a round closes or needs a human (#127)"
```

---

### Task 7: `relay wait` verb

**Files:**
- Modify: `cmd/relay/main.go` (`usage` -- one line after `log`; the `run` switch -- `case "wait"`; new `cmdWait` after `cmdLog`)

**Interfaces:**
- `cmdWait(args []string) error` per §4.8. Flags: `-name string`, `-any bool` ("wait on every named binding; the first to close or need you wins, its name printed first"), `-round int` (default 0, "round to wait on (default: the newest round sent; an earlier round answers from the log)"), `-timeout time.Duration` (default `10*time.Minute`). Resolution: if `*any`: `fs.Args()` must be non-empty and `--name` empty, else usage error; names = `fs.Args()`. Else: `resolveBinding(rt, *name, fs.Args())` (the cwd fallback, like `send`). `*timeout <= 0` or `*round < 0` -> usage error. Call `relay.Wait(ctx, rt, relay.WaitOptions{Names, Round: *round, Timeout: *timeout, Interval: time.Second})` with `ctx` from `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` as `cmdUI` does. Output: if `*any` and `name != ""`, `fmt.Println(name)`; if `res.Line != ""`, `fmt.Println(res.Line)`. Return `nil` when `res.Code == 0`, else `exitCodeErr{res.Code}`.
- Usage line: `  wait      block until a round closes or needs you; exit 0 closed, 2 unmarked, 3 needs you, 4 done/unbound, 124 timeout`.

- [ ] **Step 1: Implement.** No test under `cmd/relay` (the verb never reaches herdr, but the package rule stands).

- [ ] **Step 2: Build and smoke** -- `go build ./... && go run ./cmd/relay wait -h` prints the four flags; `go run ./cmd/relay wait` with no binding for the cwd prints the `no binding for ...` error and exits 1; `go vet ./cmd/relay` clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/relay/main.go
git commit -m "feat(relay): relay wait blocks until a round closes or needs a human (#127)"
```

---

### Task 8: `warnWaitingOnYou` in the seven mutating verbs

**Files:**
- Modify: `cmd/relay/main.go` (new helper after `resolveBinding`; one call in each of `cmdBind`, `cmdAdd`, `cmdFork`, `cmdSend`, `cmdAnswer`, `cmdDone`, `cmdUnbind`)

**Interfaces:**
- `func warnWaitingOnYou(rt relay.Runtime, except string)` per §4.9: `lines, err := relay.WaitingOnYou(rt, except)`; on error `fmt.Fprintf(os.Stderr, "relay: waiting-on-you check: %v\n", err)` and return; else `fmt.Fprintln(os.Stderr, l)` per line.
- Call placement: immediately before each verb's final successful `return nil`, after its own stdout output. `except` is the acted-on binding's name: `opts.Name`/`res.Name` for bind/add/fork, `target` for send, the answered name for answer, the name for done and unbind. Where a verb has several success returns (e.g. `--pick` paths), the call goes before each; a `--dry-run` or help path is not a success and gets none.

- [ ] **Step 1: Implement.**

- [ ] **Step 2: Build and vet** -- `go build ./... && go vet ./cmd/relay`. Read each of the seven functions once more and confirm no early `return nil` was missed and none precedes a failing path.

- [ ] **Step 3: Commit**

```bash
git add cmd/relay/main.go
git commit -m "feat(relay): mutating verbs name the other bindings waiting on you (#128)"
```

---

### Task 9: README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Command surface** (after the `relay log NAME` bullet): add
  `- \`relay wait [NAME|--name N] [--any N1 N2 ...] [--round R] [--timeout D]\` — block until the round closes or the binding needs you, reading relay's own state only (never herdr). Exit 0: closed on the marker, stdout is the report path. 2: closed without it (\`unmarked\`, \`scraped\`, \`noreport\` — verify before trusting), report path or \`-\`. 3: needs you, stdout is one line saying what it is waiting on. 4: the binding is DONE or was unbound. 124: \`--timeout\` (default 10m) elapsed. \`--any\` waits on several and prints the winner's name first. A pane planner that does not want the report typed afterwards runs \`relay wait N && relay pull N\`.`

- [ ] **Step 2: "Running several builders at once"** (after the paragraph ending `every verb that acts on a binding takes \`--name\`.`): add one paragraph: every mutating verb (`bind`, `add`, `fork`, `send`, `answer`, `done`, `unbind`) ends by listing, on stderr, every *other* binding that is waiting on a human — a blocked dialog, a halt, a dead builder, a lost planner — with how long and the verb that resolves it, e.g. `waiting on you: api round 4 blocked 23m -- Do you want to proceed? > 1. Yes  (relay answer --name api)`. The exit code is unchanged; it is a reminder, not a refusal.

- [ ] **Step 3: Quick start** (the block around line 165 that shows `relay pull`): add a line `relay wait                        # block until the round closes or needs you` before `relay pull`, if the block is a sequence; otherwise skip.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: relay wait and the waiting-on-you lines (#127, #128)"
```

---

### Task 10: mutation checks

Run each mutation, confirm the named test fails, revert with `git checkout -- <file>`, and record all five in the report.

- [ ] **Mutation 1:** in `WaitingOn`, check `b.Halt != ""` before the question entry. Expected failure: the `TestWaitingOn` blocked-with-Halt case.
- [ ] **Mutation 2:** in `WaitingOn`, drop the `!switchable(b)` guard on `broken`. Expected failure: the `TestWaitingOn` broken+switchable case.
- [ ] **Mutation 3:** in `WaitOutcome`, move the `State == done` check above the report lookup. Expected failure: the `TestWaitOutcome` DONE-with-report case.
- [ ] **Mutation 4:** in `Wait`, replace `DefaultWaitRound(b, entries)` with `b.Round`. Expected failure: `TestWaitDefaultRoundIsTheNewestPlanned` (times out to 124 instead of 0) and the `TestDefaultWaitRound` after-close case if you mutate the function itself instead.
- [ ] **Mutation 5:** in `haltBinding`, move the `Halt`/`HaltAt` writes outside the `HaltNotifiedRound` guard. Expected failure: `TestReconcileFlagsRoundTimeout` on the unchanged-`HaltAt` assertion.

---

### Task 11: full constituent set

- [ ] **Step 1: Run the four constituents** from "Running commands", in order. Expected: green. `gofmt -l .` prints nothing.

- [ ] **Step 2: `git diff --stat main..HEAD`** and compare with §2's file list plus the test files named in this plan. Anything outside it goes in the report.

- [ ] **Step 3: No commit** (nothing to commit unless a constituent found something; if `gofmt` reformatted a file, commit that as `style: gofmt (#127)`).

---

## Report

Your report is `NNN-report.md` in the drop directory, then the empty
`NNN-done`. It lists: each task's commit hash; the Task 10 mutation checks
and which tests failed under each; the final constituent-set output; the
`git diff --stat`; and any step where you stopped, with the reason.
