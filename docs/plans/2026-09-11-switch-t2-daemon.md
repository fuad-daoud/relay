# Switch T2: the triggers and `switchBuilder` in the daemon (#61 step 6)

**Design spec:** `docs/specs/2026-09-11-builder-switching-design.md` -- read §1, §3.5, §4.1–4.4, §5, §6
**Issue:** #61 (step 6)
**Depends on:** Switch T1 (`Policy.SwitchLimit`, `Binding.RoundSwitches`, `Binding.BuilderMissingSince`, `store.KindSwitch`) -- already in this tree.

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
- **A switch happens only when `b.BuilderCandidate != ""` and a round is
  open** (`!b.RoundStartedAt.IsZero()`). Adopted builders and closed
  rounds behave exactly as today. Two tests pin this.
- **The gone trigger waits `switchGrace`** measured from
  `BuilderMissingSince`; a hit clears it. A replacement spawned on a
  flicker orphans a live builder (#20).
- **Only a `RateLimited` gate on the builder's own token triggers the
  gated switch.** `SpawnFailed` gates are ignored.
- **Close before spawn, only for the gated trigger.** The gone trigger
  has no pane to close. `ClosePane` is called from exactly one new place.
- **A switch tick returns without `deliverAndSettle`**, like the halt
  paths in `Reconcile`.
- **Re-resolution uses an omitted token** (`""`): the policy order
  applies even when the binding was bound with an explicit `--builder`.
- No test may execute a `cmd/relay` subcommand that reaches herdr.
  Everything here is `Reconcile` against `fakeHerdr`.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the switch

**Files:**
- Create: `internal/relay/switch.go`, `internal/relay/switch_test.go`
- Modify: `internal/relay/reconcile.go` (`Reconcile` only)
- Modify: `internal/relay/ledger.go` (`mutateLedgerLocked`, `recordSpawnFailureLocked`), `internal/relay/ledger_test.go`
- Modify: `internal/relay/bind.go` (`resolveBuilder` gains `tx`), `internal/relay/add.go`, `internal/relay/fork.go` (callers pass `nil`)

**Interfaces consumed:** `FindAgent`, `haltBinding`, `resolveBuilder` (which
step 1b changes to `(ctx, rt, tx *store.Tx, BindOptions, name, plannerPane)
(store.Endpoint, Resolution, error)`),
`resolveCandidate(set, pol, gates, "", "builder")`, `Gates(rt)`,
`ExplainResolution`, `composePrompt(b, planPath, reportPath)`,
`promptWithRetry(ctx, rt, target, text)`, `rt.Store.PlanPath/ReportPath`,
`rt.Herdr.{ClosePane, Notify}`, `rt.Policy.SwitchLimit()`,
`store.KindSwitch`, `Binding.RoundSwitches`, `Binding.BuilderMissingSince`,
`ledger.Gate`, `ledger.RateLimited`, `slog`. Test helpers: `newRuntime`,
`plannerAgent`, `builderAgent`, `sentBinding`, `reconcile(t, rt, b,
agents)`, `writePlan`, `Send`, `Unavailable`, `recordSpawnFailure`,
`loadLedger`, `candidateSet`, `testTwoProviderJSON`, `orderOf`,
`fakeHerdr.{agents, newPane, starts, prompts, closed, notices, startErr,
closeErr}`.

**Interfaces produced** (spec §3.5, §4.2–4.4):

```go
// internal/relay/switch.go
const switchGrace = 30 * time.Second
func gatedBuilder(rt Runtime, b store.Binding) (ledger.Gate, bool)
func switchEntry(now time.Time, round int, reason string, res Resolution) store.LogEntry
func switchBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reason string, closeOld bool) (store.Binding, error)
```

- [ ] **Step 1: the test scaffold and the first failing tests** (spec §4.1, §5.2)

  Create `internal/relay/switch_test.go`. A fixture that is switchable
  and whose builder's provider can be gated without gating everything:

  ```go
  // sentSwitchable binds webshop to agy/other/m with the three-builder
  // order and hands it round 1, so a rate limit on provider "other" leaves
  // claude and opencode (provider "test") available to switch to.
  func sentSwitchable(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
      t.Helper()
      f.agents = []herdr.Agent{plannerAgent()}
      f.newPane = "w2:p4"
      rt := newRuntime(t, f)
      rt.Candidates = candidateSet(t, testTwoProviderJSON)
      rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
      if _, err := Bind(context.Background(), rt, BindOptions{
          Name: "webshop", Candidate: "agy/other/m", PlannerPane: "w2:p3", CWD: "/repo",
      }); err != nil {
          t.Fatalf("Bind: %v", err)
      }
      f.agents = append(f.agents, builderAgent(herdr.StatusWorking))
      if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it")); err != nil {
          t.Fatalf("Send: %v", err)
      }
      b, err := rt.Store.Load("webshop")
      if err != nil {
          t.Fatalf("Load: %v", err)
      }
      f.prompts, f.starts, f.closed, f.notices = nil, nil, nil, nil
      f.newPane = "w2:p9" // where a replacement would land
      return rt, b
  }

  // at returns rt with its clock moved to baseTime + d.
  func at(rt Runtime, d time.Duration) Runtime {
      rt.Now = func() time.Time { return baseTime.Add(d) }
      return rt
  }

  // switches returns the switch entries in webshop's log.
  func switches(t *testing.T, rt Runtime) []store.LogEntry
  ```

  `gone` is `[]herdr.Agent{plannerAgent()}` (no builder); `present` is
  `[]herdr.Agent{plannerAgent(), builderAgent(herdr.StatusWorking)}`.
  `reconcile` (the existing helper) does not save; chain by passing the
  returned binding to the next call, and save with `rt.Store.Save` when a
  later helper must `Load` it.

  Tests (all fail to compile until step 2; the assertions are the
  contract):

  - `TestGoneStampsMissingAndWaits`: `sentSwitchable`; `reconcile(rt, b,
    gone)` → `State == StateBroken`, `BuilderMissingSince == baseTime`,
    `len(f.starts) == 0`; then `reconcile(at(rt, 29*time.Second), got,
    gone)` → still `Broken`, `BuilderMissingSince == baseTime` (not
    re-stamped), `len(f.starts) == 0`.
  - `TestGoneSwitchesAfterGrace`: same, then `reconcile(at(rt,
    31*time.Second), got, gone)` → `State == StateActive`;
    `len(f.starts) == 1` with `f.starts[0].Kind == "agy"` (agy/other/m
    is first in the order and not gated -- the same candidate again is a
    valid pick for a builder that merely died); `got.Builder.PaneID ==
    "w2:p9"`; `got.BuilderCandidate == "agy/other/m"`; `got.Round == 1`;
    `got.RoundSwitches == 1`; `got.BuilderMissingSince.IsZero()`;
    `got.RoundStartedAt == baseTime.Add(31*time.Second).UTC()`;
    `len(f.prompts) == 1` and `f.prompts[0].Text` contains
    `rt.Store.PlanPath("webshop", 1)`; `len(f.closed) == 0`;
    `len(f.notices) == 1` containing `switched builder to agy/other/m`;
    exactly one `switch` entry with `Confirmed`, `Round == 1`, `Note`
    starting `switched builder (gone for 31s): picked agy/other/m for
    builder: order #1`.
  - `TestGoneThenBackClearsMissing`: miss at +0, hit (`present`) at +10s
    → `State == StateActive`, `BuilderMissingSince.IsZero()`, no starts.
  - `TestGatedSwitchesAtOnce`: `sentSwitchable`; `Unavailable(rt,
    "agy/other/m", time.Time{}, "5h window")`; `reconcile(rt, b, present)`
    → `f.closed == ["w2:p4"]`; `len(f.starts) == 1`, `f.starts[0].Kind ==
    "claude"`; `got.BuilderCandidate == testClaudeRef`; `got.Round == 1`;
    `RoundSwitches == 1`; one `switch` entry whose `Note` starts
    `switched builder (rate-limited: 5h window): picked claude/test/m for
    builder: order #2; skipped agy/other/m (rate-limited until cleared)`;
    one notice.
  - `TestGatedIgnoresSpawnFailedGate`: `sentSwitchable`;
    `recordSpawnFailure(rt, "agy/other/m", "webshop", errors.New("x"))`;
    `reconcile(rt, b, present)` → no starts, no closes, `State ==
    StateActive`.

  Run: `go test ./internal/relay/ -run 'Gone|Gated'`. Expected: FAIL to
  compile.

- [ ] **Step 1b: `resolveBuilder` under a held lock** (spec §4.3 "The lock")

  `Reconcile` runs inside `Store.WithLock`; `recordSpawnFailure` takes that
  same non-reentrant lock via `mutateLedger`. So before `switchBuilder`
  can call `resolveBuilder`, the spawn-failure record needs a lock-free
  variant. (Your round-2 report found this; this step is the fix.)

  `internal/relay/ledger.go`: split `mutateLedger` into

  ```go
  // mutateLedgerLocked is mutateLedger for a caller that already holds the
  // store lock (Reconcile and everything it calls). The lock is not
  // reentrant, so taking it again here would hang the daemon.
  func mutateLedgerLocked(rt Runtime, fn func(ledger.Ledger) ledger.Ledger) error   // load, prune, fn, save -- no WithLock
  func mutateLedger(rt Runtime, fn ...) error                                        // WithLock(func(*store.Tx) error { return mutateLedgerLocked(rt, fn) })
  ```

  and split `recordSpawnFailure` the same way: the entry construction and
  the stderr-on-error rule move into `recordSpawnFailureWith(rt, mutate
  func(Runtime, func(ledger.Ledger) ledger.Ledger) error, token, binding,
  cause)`; `recordSpawnFailure` calls it with `mutateLedger`,
  `recordSpawnFailureLocked` with `mutateLedgerLocked`. Doc comments say
  which lock state each expects.

  `internal/relay/bind.go`: `resolveBuilder(ctx, rt, tx *store.Tx, opts,
  name, plannerPane)`. Doc: `tx` is a witness that the caller holds the
  store lock -- `nil` from the CLI paths, the reconcile transaction from
  the daemon -- and decides which spawn-failure recorder runs; the ledger
  is not written through the `Tx`. The `StartAgent` error branch becomes
  `if tx != nil { recordSpawnFailureLocked(...) } else { recordSpawnFailure(...) }`.
  Update the four callers (`create`, `resume`, `Add`, `Fork`) to pass
  `nil` -- each calls it outside its own `WithLock`, which is why `nil` is
  right there.

  Test in `ledger_test.go`: `TestRecordSpawnFailureLockedUnderHeldLock`:
  run `rt.Store.WithLock(func(*store.Tx) error {
  recordSpawnFailureLocked(rt, testAgyRef, "webshop", errors.New("boom"));
  return nil })` in a goroutine, select on its completion against a 5s
  timer (fail with "deadlocked" on the timer), then assert one
  `spawn_failed` entry. The existing spawn-failure tests keep covering
  the unlocked path.

  Run: `go build ./... && go test -count=1 ./internal/relay/ -run 'SpawnFailure'`.
  Expected: green.

- [ ] **Step 2: `switch.go`** (spec §3.5, §4.2–4.4)

  Create `internal/relay/switch.go`:

  - `switchGrace` with the spec §3.5 comment verbatim.
  - `gatedBuilder`: loop `Gates(rt)`; return the first with `Token ==
    b.BuilderCandidate && Kind == ledger.RateLimited`. Doc: a running
    builder is not a failed spawn, so `SpawnFailed` is ignored.
  - `switchEntry`: exactly spec §4.4.
  - `switchBuilder`: exactly spec §4.3's pseudocode (passing its `tx` to
    `resolveBuilder` -- step 1b), in this order:
    limit check → resolve → `closeOld` close → `resolveBuilder` →
    mutate `b` → `tx.AppendLog(switchEntry)` → prompt → `RoundStartedAt`
    → `Notify` (error only `slog.Warn`ed) → `slog.Info("builder
    switched", "binding", "round", "from", "to", "reason", "switches")`.
    `old := b.BuilderCandidate` is captured before the mutation for the
    log line. The spawn-failure branch is: `b.RoundSwitches++`, `b.State
    = store.StateBroken`, `slog.Warn("builder switch failed", ...)`,
    `return b, nil` -- no halt, no entry; `resolveBuilder` already
    recorded the `spawn_failed`. Every `haltBinding` message is the spec's
    text. The `BindOptions` passed to `resolveBuilder` is `{Candidate:
    res.Token(), CWD: b.CWD}` -- nothing else.

  Doc comment on `switchBuilder`: what it replaces, that the round number
  is kept and the clock restarted, why close precedes spawn (the agent
  name `<name>-builder` must be free), and that the recorded resolution
  is the one made here, not `resolveBuilder`'s always-explicit one (same
  rule as `Add`/`Fork`).

  Run: `go build ./internal/relay/`. Expected: clean.

- [ ] **Step 3: the triggers in `Reconcile`** (spec §4.1)

  In `Reconcile`, replace

  ```go
  builder, ok := FindAgent(agents, b.Builder)
  if !ok {
      b.State = store.StateBroken
      return b, nil
  }
  ```

  with the spec §4.1 block. `now := rt.Now().UTC()`. The gone branch:
  stamp `BuilderMissingSince` if zero, set `Broken`, and when
  `switchable && now.Sub(b.BuilderMissingSince) >= switchGrace` return
  `switchBuilder(ctx, rt, tx, b, fmt.Sprintf("gone for %s",
  now.Sub(b.BuilderMissingSince).Truncate(time.Second)), false)`;
  otherwise `return b, nil`. On a hit: `b.BuilderMissingSince =
  time.Time{}` before the existing `Broken → Active` recovery. After
  `refreshEndpoint` and before the round-cap check: `if switchable { if
  g, ok := gatedBuilder(rt, b); ok { return switchBuilder(ctx, rt, tx, b,
  "rate-limited: "+g.Note, true) } }`. When `g.Note == ""` the reason is
  just `rate-limited`.

  Add a comment above the block naming the two triggers and pointing at
  the spec, and one on the early return: a switch tick delivers nothing,
  like a halt (spec §4.1).

  Run: `go test ./internal/relay/ -run 'Gone|Gated'`. Expected: green.
  Then `go test -count=1 ./internal/relay/` -- every existing reconcile
  test still passes (`seedBound` builders are switchable only after
  `Send`, and no existing test leaves one gone for 30s of fake time or
  gates its provider).

- [ ] **Step 4: the rest of the matrix** (spec §5.3, §6)

  Add to `switch_test.go`:

  - `TestNoOrderHalts`: `sentSwitchable`, then `rt.Policy =
    policy.Policy{}` (no order), gate `other`, tick →
    `State == StateNeedsYou`, no starts, one notice containing `cannot
    switch` and `candidates serve "builder"` (the ambiguity refusal).
  - `TestMaxSwitchesZeroHalts`: `zero := 0; rt.Policy.MaxSwitches =
    &zero`; gate; tick → `NeedsYou`, no starts, no closes, notice
    contains `already switched 0 time(s) this round (max_switches 0)`.
  - `TestExhaustionHalts`: gone at +0, switch at +31s (starts 1), gone
    again at +40s (stamp), switch at +71s (starts 2), gone at +80s, at
    +111s → `NeedsYou`, `len(f.starts) == 2`, `RoundSwitches == 2`,
    `HaltNotifiedRound == 1`; one more tick at +113s → still one halt
    notice (dedup): count notices containing `already switched`.
  - `TestAllGatedHalts`: `Unavailable` on `"agy/other/m"` and on
    `testClaudeRef` (gates `other` and `test` -- everything); tick →
    `NeedsYou`, no starts, no closes (the halt precedes the close),
    notice contains `every candidate serving "builder" is gated`.
  - `TestSpawnFailureWalksOn`: gate `other`; `f.startErr =
    errors.New("agent start: exit 1")`; tick → `State == StateBroken`,
    `RoundSwitches == 1`, `f.closed == ["w2:p4"]`, no `switch` entry,
    ledger has one `spawn_failed` for `testClaudeRef`; then `f.startErr =
    nil`, `f.newPane = "w2:p10"`, tick at +0 with `gone` → stamps missing;
    tick at +31s with `gone` → starts has a second entry with `Kind ==
    "opencode"` (claude is now gated by its spawn failure),
    `RoundSwitches == 2`, `State == StateActive`.
  - `TestAdoptedBuilderIsNeverSwitched`: `sentBinding` variant with
    `BuilderPane` adoption -- copy the adoption setup from `bind_test.go`
    (~line 148), then `Send`, then gone at +0 and +60s → `Broken` both
    times, no starts.
  - `TestNoOpenRoundIsNeverSwitched`: `sentSwitchable`, then set
    `b.RoundStartedAt = time.Time{}` (as `queueReport` does), gone at +0
    and +60s → `Broken`, no starts.
  - `TestSwitchTickDoesNotDeliver`: `sentSwitchable`; queue a planner
    payload the way an existing halt test does (grep `Queue(` in
    `reconcile_test.go` and mirror it); gate; tick → the payload is still
    pending (`rt.Store.PendingForPlanner` found) and the planner was not
    prompted with it (`f.prompts` has exactly the one builder prompt).
  - `TestCloseFailureHalts`: gate; `f.closeErr = errors.New("nope")`;
    tick → `NeedsYou`, no starts, notice contains `could not close its
    pane w2:p4`.

  Run: `go test -count=1 ./internal/relay/`. Expected: green. If a test
  in this list cannot be written as described because a helper does not
  exist, write the helper in `switch_test.go`; if the behaviour differs
  from the spec, stop and report.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add -A internal
  git commit -m "feat(daemon): switch a gone or rate-limited builder mid-round to the next ungated candidate (#61 step 6)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the list of
test functions added, and the exact `Note` of the `switch` entry from
`TestGatedSwitchesAtOnce` and the halt message from `TestExhaustionHalts`
(copy from a `t.Log`).
