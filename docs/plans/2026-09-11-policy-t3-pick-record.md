# Policy T3: the `pick` log entry, `Resolution` on results, the stderr line (#61 step 2)

**Design spec:** `docs/specs/2026-09-11-policy-order-design.md` -- read §3.2, §3.5, §4.3, §4.4, §4.5 (last paragraph), §4.9, §5.2, §5.3, §6
**Issue:** #61 (step 2)
**Depends on:** T2 (`Resolution`, `How*`, `resolveCandidate` new signature, `ExplainResolution`) -- already in this tree.

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **A `pick` entry is always `Confirmed: true` and `Direction:
  DirToPlanner`** (spec §3.2). An unconfirmed `DirToPlanner` entry is the
  pending-delivery record; a pick that is not confirmed would be
  delivered to the planner as a payload. Step 4's tests assert it.
- **The pick entry is written after the spawn succeeded, never before**
  (spec §4.4). A resolution that did not lead to a running agent writes
  nothing.
- **Adoption writes nothing.** `resolveBuilder`'s adopt branch returns the
  zero `Resolution` (`How == ""`) and every writer skips on it.
- **`Bind`'s signature does not change.** Its body moves to
  `BindResolved`; `Bind` wraps it. 48 call sites keep compiling untouched.
- The stderr line is printed for every `How` **except** `HowExplicit` and
  `""`. The explicit case keeps today's `GatedNote` and nothing else.
- No test may execute a `cmd/relay` subcommand that reaches herdr.
  Everything here is in `internal/relay` against `fakeHerdr`.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the pick record

**Files:**
- Modify: `internal/store/log.go` (`KindPick`)
- Modify: `internal/relay/candidate.go` (`pickEntry`)
- Modify: `internal/relay/bind.go` (`BindResolved`, `Bind`, `resume`, `create`, `resolveBuilder`)
- Modify: `internal/relay/add.go`, `internal/relay/fork.go` (incl. `writeFork`), `internal/relay/ask.go`
- Modify: `internal/relay/fork_test.go` (the one direct `writeFork` caller)
- Create: `internal/relay/pick_test.go`
- Modify: `cmd/relay/main.go` (`cmdBind`, `cmdAdd`, `cmdFork`, `cmdAsk`, flag help)

**Interfaces consumed** (T2): `Resolution`, `Resolution.Token()`, `How`,
`HowExplicit`, `resolveCandidate(set, pol, gates, token, role)`,
`ExplainResolution(role, res)`, `Gates(rt)`; `store.Tx.{Save, AppendLog}`,
`store.Store.{WithLock, ReadLog}`; `recordSpawnFailure` (tests use it to
gate a token the honest way).

**Interfaces produced** (spec §3.2, §3.5, §4.3, §4.4):

```go
// internal/store/log.go
KindPick Kind = "pick"

// internal/relay/candidate.go
func pickEntry(now time.Time, round int, role string, res Resolution) store.LogEntry

// internal/relay/bind.go
func BindResolved(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, Resolution, error)
func Bind(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, error)   // unchanged signature
func resolveBuilder(ctx, rt, opts, name, plannerPane string) (store.Endpoint, Resolution, error)

// results
AddResult.Resolution  Resolution
ForkResult.Resolution Resolution
AskResult.Resolution  Resolution
```

- [ ] **Step 1: `KindPick`, `pickEntry`, `resolveBuilder` returns `Resolution`** (spec §3.2, §4.3, §4.4)

  `internal/store/log.go`: add to the `Kind` const block, after `KindFork`:

  ```go
  KindPick Kind = "pick" // relay -> log only: which candidate a spawn resolved to and why (#61 step 2)
  ```

  `internal/relay/candidate.go`:

  ```go
  // pickEntry is the log record of one resolution. Confirmed and bound for
  // the planner so it is never mistaken for an undelivered payload; the
  // note is ExplainResolution, so `relay log` reads exactly what bind
  // printed (spec §3.2, §4.4).
  func pickEntry(now time.Time, round int, role string, res Resolution) store.LogEntry {
      return store.LogEntry{
          TS: now.UTC(), Round: round, Direction: store.DirToPlanner,
          Kind: store.KindPick, Confirmed: true, Note: ExplainResolution(role, res),
      }
  }
  ```

  `bind.go` `resolveBuilder`: return type `(store.Endpoint, Resolution,
  error)`. The adopt branch returns `endpointOf(found), Resolution{}, nil`.
  The spawn branch keeps `res, err := resolveCandidate(...)` (T2 named it;
  keep `c := res.Candidate`) and returns `ep, res, nil` at the end; every
  error return is `store.Endpoint{}, Resolution{}, err`. Update the doc
  comment: the second value is the resolution, zero when adopting.

  Callers: in `create` and `resume` (bind.go), `builder, token, err :=
  resolveBuilder(...)` becomes `builder, res, err := ...` and every use of
  `token` becomes `res.Token()`. In `resume`, the `var ( builder; token )`
  block becomes `var ( builder store.Endpoint; res Resolution )`. In
  `add.go` and `fork.go`, `builder, _, err :=` becomes `builder, res, err
  :=` (used in step 2).

  Run: `go build ./...`. Expected: clean.

- [ ] **Step 2: write the entry on each path** (spec §4.4, §5.2)

  **bind, fresh (`create`)**: replace `rt.Store.Save(b)` with

  ```go
  err = rt.Store.WithLock(func(tx *store.Tx) error {
      if err := tx.Save(b); err != nil {
          return err
      }
      if res.How == "" {
          return nil
      }
      return tx.AppendLog(name, pickEntry(rt.Now(), 1, "builder", res))
  })
  ```

  keeping the existing "bind failed after starting builder in pane %s
  (close it yourself)" wrapping. (`rt.Now` is always set in production and
  in `newRuntime`-built test runtimes; guard `now := time.Now(); if rt.Now
  != nil { now = rt.Now() }` only if you find a test runtime without it --
  say so in the report.)

  **bind, resume (`resume`)**: inside the existing `WithLock`, after
  `tx.Save(b)` succeeds and before `out = b`:

  ```go
  if rebinding && res.How != "" {
      if err := tx.AppendLog(opts.Name, pickEntry(rt.Now(), b.Round, "builder", res)); err != nil {
          return err
      }
  }
  ```

  **`BindResolved` / `Bind`**: rename the current `Bind` to
  `BindResolved` returning `(store.Binding, Resolution, error)`; `create`
  and `resume` return the triple (their `res`). Add:

  ```go
  // Bind is BindResolved without the resolution, for the callers that only
  // need the binding. cmdBind uses BindResolved to print why relay picked
  // what it did.
  func Bind(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, error) {
      b, _, err := BindResolved(ctx, rt, opts)
      return b, err
  }
  ```

  Move `Bind`'s doc comment ("ties the calling planner pane...") to
  `BindResolved` and add: the second value is how the builder was chosen,
  zero when a pane was adopted.

  **add**: `AddResult` gains `Resolution Resolution` (comment: how the
  builder was chosen, for the pick line). In the `WithLock` that saves,
  after `tx.Save(b)`: `if res.How != "" { return tx.AppendLog(b.Name,
  pickEntry(rt.Now(), 1, "builder", res)) }`. Set `Resolution: res` in
  the returned `AddResult`.

  **fork**: `ForkResult` gains `Resolution Resolution`. After
  `resolveBuilder` succeeds: `if opts.Candidate == "" { res.InheritedFrom
  = src.Name }`. `writeFork` gains a parameter `pick store.LogEntry`
  appended right after `forkEntry` (same `tx.AppendLog(b.Name, ...)`
  shape, same error return). `Fork` passes `pickEntry(now, b.Round,
  "builder", res)` (`now` is already computed there). Update the one
  test caller in `fork_test.go` (~line 559) to pass
  `pickEntry(time.Now().UTC(), b.Round, "builder", Resolution{How:
  HowExplicit, Candidate: <the opencode candidate from the set>})` -- look
  at what `b` is in that test and mirror it. Set `Resolution: res` in the
  returned `ForkResult`.

  **ask**: `AskResult` gains `Resolution Resolution`; set `Resolution:
  res` in all three `AskResult{...}` literals (the two error returns
  included -- spec §4.3). In phase 3, inside `if consult.State ==
  store.ConsultRunning {`, **before** the existing `entry := store.LogEntry{...
  KindAsk ...}`:

  ```go
  if err := tx.AppendLog(b.Name, pickEntry(rt.Now(), consult.Round, role.Name, res)); err != nil {
      return err
  }
  ```

  Run: `go build ./... && go test -count=1 ./internal/relay/`. Expected:
  green -- existing tests do not count log entries by kind, except any
  that assert an exact entry list; if one does, it is a test that seeds a
  bind through `Bind` and then reads `ReadLog`, and its expectation gains
  one leading `pick` entry. Report which, if any.

- [ ] **Step 3: the failing tests** (spec §4.4, §7 step 3)

  Create `internal/relay/pick_test.go`. Helpers:

  ```go
  // picks returns the pick entries in a binding's log, in order.
  func picks(t *testing.T, rt Runtime, name string) []store.LogEntry
  // kinds returns the kinds in a binding's log, in order, for position checks.
  func kinds(t *testing.T, rt Runtime, name string) []store.Kind
  ```

  both over `rt.Store.ReadLog(name)`. A gate the honest way:
  `recordSpawnFailure(rt, testAgyRef, "earlier", errors.New("agent start:
  exit 1"))` writes a `spawn_failed` for agy at `baseTime`, `Until
  baseTime.Add(SpawnFailedCooldown)`; `untilText :=
  GateUntilText(baseTime.Add(SpawnFailedCooldown))`.

  - `TestBindPicksFirstUngatedInOrder`: `newRuntime`, `rt.Policy =
    orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)` (T2's
    helper), gate agy as above, `Bind` with `Candidate: ""`. Want: no
    error; `f.starts[0].Kind == "claude"`; `b.BuilderCandidate ==
    testClaudeRef`; exactly one pick entry with `Confirmed == true`,
    `Direction == store.DirToPlanner`, `Round == 1`, `Note ==
    "picked claude/test/m for builder: order #2; skipped agy/test/m (spawn
    failed " + untilText + ")"`.
  - `TestBindResolvedReturnsTheResolution`: same setup through
    `BindResolved`; `res.How == HowOrder`, `res.Position == 2`,
    `len(res.Skipped) == 1`.
  - `TestBindRefusesWhenEveryCandidateIsGated`: `rt.Policy` as above;
    `Unavailable(rt, testClaudeRef, time.Time{}, "quota")` gates provider
    `test`, i.e. all three; `Bind` with `Candidate: ""` →
    `errors.Is(err, ErrAllGated)`, `len(f.starts) == 0`, and the binding
    does not exist (`rt.Store.Load("webshop")` is `store.ErrNotFound`).
  - `TestBindExplicitGatedBypassesAndLogsIt`: gate agy; `Bind` with
    `Candidate: testAgyRef` → no error; one pick entry whose `Note` is
    `"picked agy/test/m for builder: explicit, policy bypassed; gated:
    spawn failed " + untilText`.
  - `TestBindAdoptionWritesNoPick`: mirror the existing adoption test at
    `bind_test.go` ~line 148 (`BuilderPane: "w2:p8"` with a builder agent
    in that pane); after `Bind`, `len(picks(...)) == 0`.
  - `TestResumeRebindLogsPickAtCurrentRound`: mirror
    `TestResumeAllowsRebindWhenSessionlessBuilderPaneIsGone` (bind_test.go
    ~line 1077); after the resume, exactly one pick entry with `Round ==
    b.Round` and `Note` starting `picked agy/test/m for builder: explicit`.
  - `TestAddLogsPick`: `newForkRuntime`, `rt.Policy = orderOf("builder",
    testAgyRef)`, `addRepo`, `Add` with `Candidate: ""` → `res.Resolution.How
    == HowOrder`; one pick entry in the new binding's log, `Round == 1`.
  - `TestForkInheritedLogsSource`: `newForkRuntime`, `seedFourRoundBinding(t,
    rt, "source", srcCWD)` (its `BuilderCandidate` is `testOpencodeRef`),
    `Fork` with `Candidate: ""`, `Round: 2`, `NewName: "alt"` → `res.Resolution.InheritedFrom
    == "source"`; `kinds(t, rt, "alt")` has `KindFork` immediately followed
    by `KindPick`; the pick `Note` is `"picked opencode/test/m for builder:
    explicit, inherited from source, policy bypassed"`, `Round == 3`.
  - `TestAskLogsPickBeforeAsk`: `seedForAsk`, `writeQuestion`, `Ask` with
    `Role: "reviewer"` → in `kinds(t, rt, "webshop")` the last two entries
    are `KindPick` then `KindAsk`; the pick's `Round == res.Consult.Round`
    and `Note == "picked claude/test/m for reviewer: sole candidate"`.
  - `TestStrandedAskLogsNoPick`: `seedForAsk`, `f.startErr =
    errors.New("agent start: exit 1")`, `Ask` → error; `len(picks(...)) ==
    0`; `res.Resolution.How == HowSole` (the result still says what was
    tried).

  Run: `go test ./internal/relay/ -run 'Pick|Resolved|EveryCandidateIsGated|Bypasses|Inherited|StrandedAsk'`.
  Expected: green already for most -- step 2 did the work; a red one here
  is a real gap. Fix the code, not the test.

- [ ] **Step 4: the CLI** (spec §4.5 last paragraph, §4.9, §5.3)

  `cmd/relay/main.go`. A helper next to `noteConsultRolesTooLong`:

  ```go
  // notePick prints why relay chose the candidate it spawned. Silent for
  // an explicit token (the planner already knows) and for adoption
  // (nothing was chosen); the gated note, if any, is printed separately.
  func notePick(role string, res relay.Resolution) {
      if res.How == "" || res.How == relay.HowExplicit {
          return
      }
      fmt.Fprintln(os.Stderr, relay.ExplainResolution(role, res))
  }
  ```

  - `cmdBind`: `b, res, err := relay.BindResolved(...)`. Call
    `notePick("builder", res)` after the `GatedNote` line in the bound
    branch, and also in the `*resume && *builderAlias != ""` branch before
    its `return nil` (a rebind resolves too).
  - `cmdAdd`, `cmdFork`: `notePick("builder", res.Resolution)` after the
    `GatedNote` line.
  - `cmdAsk`: `notePick(*role, res.Resolution)` after the `GatedNote` line.
  - Flag help: `bind --builder`: `candidate harness/provider/model to
    spawn, or a pane id to adopt; omit to take the first ungated candidate
    in policy.json order[builder]`; `add --builder`: same minus adopt;
    `fork --builder`: `candidate harness/provider/model to spawn (default:
    inherits source; else the first ungated in policy.json order[builder])`;
    `ask --candidate`: `candidate harness/provider/model; omit to take the
    first ungated in policy.json order[<role>]`.

  Run: `go build ./... && go vet ./...`. Expected: clean. No `cmd/relay`
  test for this: it reaches herdr.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "feat(relay): log a pick entry per resolution; BindResolved; bind/add/fork/ask say why (#61 step 2)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the list of
test functions added, any existing test whose expected log gained a `pick`
entry (step 2), and whether any test runtime lacked `rt.Now` (step 2).
