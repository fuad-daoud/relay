# `relay ask` outside the state lock (#58)

**Design spec:** `docs/specs/2026-09-11-ask-outside-lock-design.md`
**Issue:** #58

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Prerequisites -- check these FIRST

Your worktree must contain the #63 fix (PR #65), which makes the daemon's save
gate see consult transitions. Without it, step 6's daemon-level test cannot
pass and you will chase a ghost. Verify:

```bash
grep -q 'append(\[\]store.Consult(nil), b.Consults...)' internal/relay/consult.go
test -f internal/relay/daemon_consult_test.go
```

If either check fails, **stop immediately and report it**. Do not implement the
fix yourself; it is a merged change your base should already carry.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan. A halt that surfaces a design error
is worth more than a green suite that worked around one.

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
- No herdr call may run while `Store.WithLock` is held in `Ask`. This is the
  invariant the whole change exists for; step 5's test pins it.
- relay makes no judgements. Phase 3 writes what happened; it never closes a
  pane because a record was late.
- Every new exported symbol and every new constant gets a doc comment that
  explains the rationale, in the house style already in these files.
- Existing `bind.json` files carry no `spawning` records; nothing about their
  serialisation changes.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: restructure `Ask` into reserve / spawn / record

**Files:**
- Modify: `internal/store/types.go`
- Modify: `internal/relay/consult.go`, `internal/relay/consult_test.go`
- Modify: `internal/relay/reap.go`, `internal/relay/reap_test.go`
- Modify: `internal/relay/ask.go`, `internal/relay/ask_test.go`
- Modify: `internal/relay/fake_test.go`
- Modify: `internal/relay/status.go` (comment only)
- Modify: `internal/store/store.go` (comment only)
- Modify: `cmd/relay/main.go` (`cmdReap` output)
- Modify: `docs/specs/2026-09-10-consults-design.md` (§6.3 pointer)

**Interfaces produced** (spec §3, §4):
- `store.ConsultSpawning ConsultState = "spawning"`
- `relay.consultSpawnTimeout = 5 * time.Minute`
- `relay.ReapResult.Dropped []store.Consult`
- `fakeHerdr.onSplit func()`, `fakeHerdr.startErr error`

Steps are ordered so every step leaves the suite green.

- [ ] **Step 1: the `spawning` state and the cap count** (spec §3.1, §4.5)

  In `internal/store/types.go` add `ConsultSpawning` before `ConsultRunning`
  and rewrite the `ConsultState` doc comment to say: `spawning` and `running`
  are non-terminal and both occupy a cap slot; only `done` and `silent` are
  reapable; a finished record is kept because it is the reap worklist.

  In `internal/relay/ask.go`, `runningConsults` counts `ConsultSpawning` as
  well as `ConsultRunning`. Update its comment: a reservation occupies a slot
  the cap cares about, or two concurrent asks would both see it free.

  In `internal/relay/status.go`, the `Consults` field comment (line ~50) says
  "still running"; make it "reserved or running".

  Test (`ask_test.go`): `TestAskCountsAReservationAgainstTheCap` -- seed a
  binding with `ConsultCap: 1` and one `spawning` consult; `Ask` returns
  `ErrConsultCap` and `f.splits == 0 && len(f.starts) == 0`.

  Verify: `go test ./internal/relay/ -run Cap` green.

- [ ] **Step 2: the daemon skips a fresh reservation and expires a stale one**
  (spec §4.2, §4.3, §5.2)

  In `internal/relay/consult.go`:
  - Add `consultSpawnTimeout = 5 * time.Minute` to the const block, with a
    comment stating both bounds: it must exceed `Ask`'s worst-case spawn
    phase (five herdr calls at the client's 30 s timeout) so a slow live
    spawn is never expired from under its owner, and it is how long a crashed
    `Ask` holds a cap slot. Say it follows the client timeout, as
    `lockAcquireLimit`'s comment does.
  - In `reconcileConsults`, after the terminal-state `continue` and **before**
    `FindAgent`, add the `spawning` branch from spec §5.2. Comment why
    `FindAgent` is not called for it.
  - In `finishConsult`, the `silent` payload: when `c.Endpoint.PaneID == ""`
    end with `No pane was spawned.` instead of the "Pane %s is open until the
    next `relay reap`" sentence.

  Tests (`consult_test.go`), seeding a `spawning` record by hand via
  `rt.Store.Load`/`Save` the way `seedReapable` does:
  - `TestReconcileSkipsAFreshReservation` -- `tickConsults` once; state still
    `spawning`, no log entry queued, `len(f.prompts) == 0`.
  - `TestReconcileExpiresAStaleReservation` -- move the clock past
    `consultSpawnTimeout`; state `silent`, `Note` mentions "spawn did not
    complete", exactly one `KindFindings` entry whose `Payload` does not
    contain the substring `pane ` (lowercase, trailing space -- the phrase
    that would name one).
  - `TestTickPersistsAnExpiredReservation` in `daemon_consult_test.go` --
    same seed, through `NewDaemon(rt, time.Second).Tick` twice; on-disk
    state `silent`, exactly one findings entry. This is the test that
    depends on PR #65.

  Verify: `go test ./internal/relay/ -run 'Reservation|Consult'` green.

- [ ] **Step 3: `Reap` drops a terminal record that has no pane** (spec §3.3,
  §4.4, §5.3)

  In `internal/relay/reap.go` add `Dropped []store.Consult` to `ReapResult`
  with the comment from spec §3.3. In the loop, after the running-state
  `continue`, add: `ConsultSpawning` → keep and continue (it is
  non-terminal); then if `c.Endpoint.PaneID == ""` → append to `Dropped`
  and, unless dry-run, do not keep it; no `ClosePane` call.

  In `cmd/relay/main.go` `cmdReap`, print one line per `Dropped` entry:
  `dropped <role> consult <id> on <binding> (no pane was spawned)`; under
  `--dry-run`, `would drop …`. Follow the existing verb pattern for `Closed`.

  Tests (`reap_test.go`): extend `seedReapable` with a `spawning` record
  (`dddddddd`, no pane) and a `silent` record with no pane (`eeeeeeee`).
  Update the existing three tests' counts accordingly, and add
  `TestReapDropsATerminalRecordWithNoPane`: `eeeeeeee` is in `Dropped`, not
  in `f.closed`, gone from the saved binding; `dddddddd` is kept. Add a
  dry-run assertion that `eeeeeeee` is listed in `Dropped` and still saved.

  Verify: `go test ./internal/relay/ -run Reap` green.

- [ ] **Step 4: fake hooks** (spec §4.6)

  In `internal/relay/fake_test.go` add to `fakeHerdr`:
  - `onSplit func()` -- called at the top of both `SplitPane` and
    `CreateTab`, before any other logic. Comment: it fires at the first
    herdr call `Ask` makes after releasing the lock, which is where a test
    proves the lock is free and where it can rewrite the reservation to
    simulate a slow spawn.
  - `startErr error` -- when set, `StartAgent` records the call then returns
    it. Comment: the strand-on-start path had no test because the fake could
    not fail a start.

  No test in this step; step 5 uses both. Verify: `go vet ./...` clean.

- [ ] **Step 5: restructure `Ask`** (spec §4.1, §5.1, §7.1-§7.4)

  Rewrite the body of `Ask` in `internal/relay/ask.go` after the validation
  block as the three phases in spec §5.1. Concretely:

  - Phase 1 `WithLock`: load, refuse broken/done, refuse at cap, build the
    `Consult` with `State: ConsultSpawning`, `Endpoint{AgentName, Kind}`
    (no pane), `SpawnedAt: now`; write the question to `AskPath`; append and
    save. Return on error -- nothing was spawned.
  - Phase 2, **no lock**: `consultPane`; if it fails, outcome is `silent`
    with note `"split failed: " + brief(err)` and an empty pane. Otherwise
    set `PaneID`, then `StartAgent`, then `promptWithRetry`; the first
    failure sets outcome `silent` with `"start failed: …"` or
    `"prompt failed: …"`; success sets `running`. **Delete** the
    `ListAgents` backfill (spec §7.4) -- do not move it. Put the
    `// IRREVERSIBLE:` comment from `bind.go:157` here in the same form.
  - Phase 3 `WithLock`: load; find the consult by `ID`; overwrite it if
    found, append if not (spec §7.3 says why unconditionally); on `running`
    append the `KindAsk` log entry exactly as today; save. A save failure
    returns `strandError(spawnErr, saveErr)` when `spawnErr` is non-nil, and
    `fmt.Errorf("consult %s is running in pane %s but could not be recorded: %w", id, pane, saveErr)`
    when the spawn succeeded -- `strandError` formats its cause with `%w`
    and a nil cause would print `%!w(<nil>)`. Either way the message names
    the pane, because a human now has to close it.
  - Return `AskResult{Consult: c, Binding: name}, spawnErr`.

  Update the `Ask` doc comment's Postconditions to the five cases in spec
  §4.1, and add one line under Errors for a store error from either phase.

  Delete the `strand` closure; its job is now phase 3.

  Tests (`ask_test.go`):
  - `TestAskHoldsNoLockWhileSpawning` -- `f.onSplit` starts a goroutine that
    calls `rt.Store.WithLock(func(*store.Tx) error { return nil })` and
    signals a channel; select on it against `time.After(2 * time.Second)`;
    timeout → `t.Fatal("state lock is held during SplitPane")`. Comment why
    a hang is the signal (spec §7.1).
  - `TestAskReservesBeforeSpawning` -- `f.onSplit` calls `rt.Store.Load`
    inside the same goroutine-with-deadline pattern (`Store.Load` takes the
    lock, so this doubles as a lock-free check) and asserts one consult with
    id `7f2a3c1d`, state `spawning`, empty `PaneID`.
  - `TestAskRecordsSilentWithNoPaneWhenTheSplitFails` -- `f.newPane = ""`;
    `Ask` errors; record `silent`, `PaneID == ""`, `len(f.starts) == 0`.
  - `TestAskRecordsAReapableConsultWhenTheStartFails` -- `f.startErr` set;
    record `silent`, `PaneID == "w2:p9"`, `Note` starts `start failed`.
  - Keep `TestAskRecordsAReapableConsultWhenTheSpawnFails` (prompt failure)
    and add an assertion that `Note` starts `prompt failed`.
  - `TestAskUpsertsAnExpiredReservation` -- `f.onSplit` rewrites the
    reservation to `silent` with note `spawn did not complete` (via
    `rt.Store.Load`/`Save` in the deadline goroutine, then wait for it);
    after `Ask`, the record is `running` with `PaneID == "w2:p9"` and there
    is still exactly one consult.
  - `TestAskReappendsAReapedReservation` -- `f.onSplit` saves the binding
    with `Consults = nil`; after `Ask`, exactly one consult, `running`.
  - `TestAskSpawnsRecordsAndStagesTheQuestion` -- keep, and add: the
    `KindAsk` log entry exists with `Confirmed: true`; and `f.listCalls`
    did not increase during `Ask` (the backfill is gone).
  - `TestAskLogsTheQuestionOutbound` -- keep; confirm it still passes with
    the entry now written in phase 3.

  Verify: `go test ./internal/relay/ -run Ask` green, then the full suite.

- [ ] **Step 6: comments and docs** (spec §7.5)

  - `internal/store/store.go`, the `lockAcquireLimit` comment: append one
    sentence -- `Ask` spawns its consult between two short critical sections
    on purpose, so its herdr calls do not count here; keep it that way.
  - `docs/specs/2026-09-10-consults-design.md` §6.3: append a paragraph:
    superseded for the crash-between-phases case by
    `docs/specs/2026-09-11-ask-outside-lock-design.md` §6 -- a crashed `Ask`
    now leaves a `spawning` record the daemon expires, and a pane that got
    a record late is still recorded.

  Verify: `make check` (or its constituents) green.

- [ ] **Step 7: mutation checks -- put the results in your report**

  Do each, run the named test, revert, and record which test failed:
  1. Remove the `ConsultSpawning` `continue` in `reconcileConsults`. Expect
     `TestReconcileSkipsAFreshReservation` to fail (a fresh reservation
     would be reported "pane is gone").
  2. Move the `consultPane` call inside phase 1's `WithLock`. Expect
     `TestAskHoldsNoLockWhileSpawning` to fail.
  3. Make `runningConsults` count only `ConsultRunning`. Expect
     `TestAskCountsAReservationAgainstTheCap` to fail.
  4. Make phase 3 skip the append when the record is not found. Expect
     `TestAskReappendsAReapedReservation` to fail.

  If any mutation does **not** fail its named test, the test is not pinning
  the behaviour: fix the test, do not weaken the code, and say so.

- [ ] **Step 8: commit**

  One commit. Message subject:
  `fix(relay): spawn a consult outside the state lock (#58)`. Body: what the
  hold was, what the three phases are, that `spawning` is a state, and that
  the `ListAgents` backfill is deleted rather than moved. Do not push.

## Report

Your report (`relay done` is the planner's call, not yours) must contain:

- `git diff --stat` against your base.
- The `make check` (or constituents) output tail.
- The step 7 mutation table: mutation → test that failed.
- Anything you stopped on, or any place the plan and the code disagreed.
