# `relay ask` spawns outside the state lock

**Issue:** #58
**Depends on:** #63 / PR #65 (the daemon's save gate must see consult transitions)
**Amends:** `docs/specs/2026-09-10-consults-design.md` §5.1, §6.3

## 1. System overview

`Ask` today holds `Store.WithLock` across its whole spawn sequence: split or
create a pane, start the agent, list agents for a session-id backfill, and
prompt with one retry. Each herdr call is bounded by the client's 30 s
timeout, so the worst-case hold is ~150 s (the reviewer consult in #62
measured the realistic worst at ~125 s). `lockAcquireLimit` is 90 s, and its
comment says the limit must exceed the longest hold. A slow herdr therefore
turns every concurrent relay caller -- the daemon tick first among them --
into a lock-acquire failure, at exactly the moment a human wants `relay
status` to answer.

This design restructures `Ask` into three phases: **reserve** under the lock,
**spawn** with no lock held, **record** under the lock. The lock is held only
across file writes in phases 1 and 3; no herdr call runs inside it. The cap
is enforced by the reservation, so two concurrent asks cannot both observe a
free slot -- the TOCTOU the issue names is closed by writing the slot before
releasing.

The reservation is a new, non-terminal consult state, `spawning`. It is
explicit rather than inferred from an empty pane id, because `Reap` and
`reconcileConsults` both branch on state and a sniffed zero value is a rule
nobody can grep for. A crashed `Ask` leaves a `spawning` record behind; the
daemon expires it after `consultSpawnTimeout` so it does not hold a cap slot
forever, and a record that never got a pane is dropped by `Reap` without a
close call.

`Bind` is the precedent: `resolveBuilder` spawns before the lock and takes it
only to write (`bind.go:151-159`). `Ask` could not copy that directly because
consults have no `ErrCWDTaken` backstop; the reservation is what stands in
for it.

### Scope boundary

In scope: `Ask`'s lock structure, the `spawning` state, the daemon's handling
of it, `Reap`'s handling of a terminal record with no pane, the
`lockAcquireLimit` comment, and tests that pin the lock is not held during
the spawn.

Out of scope, unchanged by this design:

- The herdr client's 30 s timeout.
- `Reap` closing panes under the lock (consults spec §7.7, accepted).
- `Bind`/`resolveBuilder` stranding a pane on failure (consults spec §6.2
  names it as its own issue).
- `relay answer` for consults; #59's blocked-pane lifecycle.

## 2. File structure

```
internal/store/types.go            ConsultSpawning added; ConsultState doc updated
internal/relay/ask.go              Ask restructured into reserve / spawn / record
internal/relay/ask_test.go         lock-not-held test; reservation and record tests
internal/relay/consult.go          consultSpawnTimeout; spawning branch in reconcileConsults;
                                   finishConsult message for a record with no pane
internal/relay/consult_test.go     spawning is skipped, then expired, tests
internal/relay/reap.go             ReapResult.Dropped; terminal-without-pane branch
internal/relay/reap_test.go        drop-without-close test
internal/relay/status.go           doc comment on Consults count (no logic change)
internal/relay/fake_test.go        onSplit hook; startErr injection
internal/store/store.go            lockAcquireLimit comment names Ask as deliberately outside
cmd/relay/main.go                  reap output for Dropped
docs/specs/2026-09-10-consults-design.md   §6.3 gains a pointer to this document
```

## 3. Data structures and type definitions

### 3.1 `store.ConsultState` (modified)

```
ConsultSpawning ConsultState = "spawning"   // slot reserved; no pane yet
ConsultRunning  ConsultState = "running"    // spawned; no findings yet
ConsultDone     ConsultState = "done"       // findings queued to the planner
ConsultSilent   ConsultState = "silent"     // gave up; "no findings" reported
```

`spawning` and `running` are both **non-terminal** and both count against
`ConsultCap`. Only `done` and `silent` are reapable. The type's doc comment
must say all four things.

### 3.2 `store.Consult` (unchanged shape)

No new field. A `spawning` record has:

| field | value while spawning |
| --- | --- |
| `ID`, `Role`, `Round`, `AskPath`, `FindingsPath` | set, as today |
| `Endpoint.AgentName`, `Endpoint.Kind` | set -- both are known before any pane exists |
| `Endpoint.PaneID`, `Endpoint.SessionID` | empty |
| `State` | `spawning` |
| `SpawnedAt` | reservation time; the spawn deadline is measured from it |
| `NudgedAt`, `Note` | zero / empty |

Existing `bind.json` files carry no `spawning` records and are unaffected.

### 3.3 `relay.ReapResult` (modified)

```
type ReapResult struct {
    Binding string
    Closed  []store.Consult   // pane closed, record dropped
    Failed  []store.Consult   // close failed, record kept for a retry
    Dropped []store.Consult   // record dropped; no pane ever existed to close
}
```

`Dropped` is separate from `Closed` because the CLI prints a pane id for
`Closed`, and "closed pane " with nothing after it is the message #59
already objects to.

### 3.4 Constants (`internal/relay/consult.go`)

```
consultSpawnTimeout = 5 * time.Minute
```

Must exceed `Ask`'s worst-case spawn phase (~150 s, five herdr calls at 30 s)
with margin, so a slow-but-live spawn is never expired from under the `Ask`
that owns it. Five minutes is also how long a crashed `Ask` holds a cap slot.
The comment must state both bounds and that the value follows the herdr
client timeout.

## 4. Interface definitions and component contracts

### 4.1 `relay.Ask` (signature unchanged)

```
func Ask(ctx context.Context, rt Runtime, opts AskOptions) (AskResult, error)
```

**Preconditions:** as today.

**Postconditions, restated for three phases:**

- On a validation failure (before phase 1 writes): nothing on disk changed,
  nothing spawned.
- On a reservation failure (phase 1 returns an error): no record written, no
  pane exists. `ErrConsultCap` is raised here and only here.
- On success: exactly one record for the id exists, `State == running`,
  `Endpoint.PaneID` set, the ask logged with `Confirmed: true`.
- On a spawn failure after a pane exists: exactly one record for the id,
  `State == silent`, `Endpoint.PaneID` set, `Note` names the failure; the
  error is returned. `Reap` can close it.
- On a spawn failure before a pane exists (`SplitPane`/`CreateTab` itself
  failed): exactly one record for the id, `State == silent`,
  `Endpoint.PaneID` empty, `Note` names the failure; the error is returned.
  `Reap` drops it without a close.

**Invariant, pinned by test:** no `rt.Herdr` method is called while the
store lock is held. The `ListAgents` session-id backfill is removed rather
than moved: it is best-effort, `Reconcile`'s `refreshEndpoint` fills the id
on the first tick that sees the agent, and dropping it removes a 30 s bound
for no loss.

**Errors:** as today, plus a wrapped store error from either locked phase.

### 4.2 `reconcileConsults` (signature unchanged)

New branch, evaluated before the running-consult logic:

- `State == spawning` and `now - SpawnedAt < consultSpawnTimeout` → skip.
  Not "pane is gone": there is no pane to be gone.
- `State == spawning` and the deadline has passed → `finishConsult(silent,
  "spawn did not complete within " + consultSpawnTimeout)`. The queued
  message must not name a pane.

`FindAgent` is never called for a `spawning` record: `SameAgent` with an
empty pane id and a set kind can only match an agent whose pane id is
empty, which does not exist, but the intent should not rest on that.

### 4.3 `finishConsult` (signature unchanged)

The `silent` message gains a branch: when `c.Endpoint.PaneID == ""`, the
payload ends `No pane was spawned.` instead of naming a pane that is open
until the next reap.

### 4.4 `Reap` (signature unchanged)

Per consult:

| state | pane id | action |
| --- | --- | --- |
| `spawning` or `running` | any | keep; never touched |
| `done` or `silent` | set | `ClosePane`; `Closed` on success, `Failed` on error (as today) |
| `done` or `silent` | empty | drop the record; append to `Dropped`; no herdr call |

`--dry-run` reports `Dropped` the same way it reports `Closed`: listed, not
acted on.

### 4.5 `runningConsults` (signature unchanged)

Counts `spawning` **and** `running`. Rename is not required; the doc comment
must say a reservation occupies a slot. `relay status`'s `+Nc` inherits this
count, which is correct: a slot that is reserved is not free.

### 4.6 `fakeHerdr` (test double)

- `onSplit func()` -- runs at the top of `SplitPane` and `CreateTab`. This is
  the hook the lock-not-held test uses.
- `startErr error` -- when set, `StartAgent` returns it. Today the fake
  cannot fail a start, so the `strand("start failed", …)` path has no test.

## 5. High-level pseudocode

### 5.1 `Ask`

```
validate opts (planner pane, file readable, role is a consult, tree != none)
   -- unchanged, no lock

── phase 1: reserve ─────────────────────────────── lock held, no herdr calls
WithLock:
    b := load(name)
    refuse if b is broken or done
    refuse with ErrConsultCap if runningConsults(b) >= cap      -- counts spawning too
    c := Consult{ID, Role, Round, AskPath, FindingsPath,
                 Endpoint{AgentName, Kind},                     -- no pane yet
                 State: spawning, SpawnedAt: now}
    write question body to c.AskPath
    b.Consults = append(b.Consults, c)
    save(b)
on error → return (nothing spawned)

── phase 2: spawn ──────────────────────────────────────── no lock held
pane, err := consultPane(...)                    -- SplitPane or CreateTab
if err:
    outcome = silent, note "split failed: …", pane ""      -- no pane exists
else:
    c.Endpoint.PaneID = pane
    err = StartAgent(agentName, kind, pane, args)
    if err: outcome = silent, note "start failed: …"
    else:
        err = promptWithRetry(pane, preamble + consultPrompt)
        if err: outcome = silent, note "prompt failed: …"
        else:   outcome = running
-- IRREVERSIBLE from the first successful call: a pane may exist. Phase 3
-- must run regardless of err, and must not be skipped on ctx cancellation.

── phase 3: record ──────────────────────────────── lock held, no herdr calls
WithLock:
    b := load(name)
    i := index of consult with c.ID in b.Consults
    if not found:                       -- expired and reaped while we spawned
        append c to b.Consults          -- the pane is real; the record must be too
    else:
        b.Consults[i] = c               -- upsert: overwrite whatever expiry wrote
    if outcome == running:
        appendLog(KindAsk, DirToConsult, Path: AskPath, Confirmed: true)
    save(b)
if phase 3 fails: return strandError(spawnErr, saveErr)  -- as today
return AskResult{c}, spawnErr
```

Upsert-by-id is the one rule for phase 3. It covers the normal case (record
still `spawning`), the slow case (daemon expired it to `silent` while the
spawn was in flight), and the pathological case (expired **and** reaped).
In the last case the cap may transiently exceed its limit by one; that is
the honest count of live panes, and the alternative -- closing a pane
because a record was late -- is a judgement relay does not make.

### 5.2 `reconcileConsults`, per running-or-spawning consult

```
if state is terminal: continue                              -- unchanged
if state == spawning:
    if now - SpawnedAt >= consultSpawnTimeout:
        finishConsult(silent, "spawn did not complete within 5m0s")
    continue
… existing running-consult logic, unchanged …
```

### 5.3 `Reap`, per consult

```
if state is spawning or running: keep; continue
if Endpoint.PaneID == "":
    Dropped += c; (dry-run: keep) ; continue                 -- no herdr call
… existing close logic, unchanged …
```

## 6. Error handling strategy

| category | example | recoverable | who sees it |
| --- | --- | --- | --- |
| validation | builder alias, tree none, unknown alias | yes, fix the ask | CLI error, nothing on disk |
| reservation | `ErrConsultCap`, binding done/broken, store write failure | yes | CLI error, nothing on disk |
| spawn, no pane | `SplitPane` failed | yes, re-ask | CLI error; `silent` record with no pane; `reap` drops it |
| spawn, pane exists | `StartAgent`/`Prompt` failed | yes, `reap` then re-ask | CLI error; `silent` record with pane; `reap` closes it |
| record | phase 3 save failed | no -- pane exists, record may be stale | `strandError` names both; human closes the pane |
| expiry | `Ask` crashed between phases | yes, automatic after 5 min | planner notified via the findings queue; cap slot freed |

A `spawning` record that is never expired is the one new leak class. It
cannot happen while the daemon runs: `reconcileConsults` evaluates the
deadline every tick, before the DONE gate and before the builder is
located (consults spec §7.6), so a done binding or a gone builder does not
shield it.

**Observability:** unchanged. The reservation is visible as `+Nc` in
`status` and as a `spawning` record in `bind.json`. No new log entry: the
ask is logged once, on success, as today.

## 7. Behavioural rules and their rationale

### 7.1 No herdr call under the lock, pinned by test

The invariant is the whole point of the change, so it gets a test that
fails if anyone moves a call back. `fakeHerdr.onSplit` attempts
`rt.Store.WithLock` in a goroutine with a short deadline; if the goroutine
does not return, the lock was held during the spawn. This is the same
"mutexes are not reentrant" fact the consults spec §7.7 warns about, used
on purpose.

### 7.2 The reservation is an explicit state

Rejected: `running` with an empty `Endpoint`. `reconcileConsults` would have
to sniff the empty pane id to avoid reporting "pane is gone" on the very
next tick, `Reap` would have to sniff it to avoid calling `ClosePane("")`,
and the cap count would be right by accident. A fourth state costs one
constant and reads as what it is.

#59 argues against a fourth state for `blocked`. The argument there is that
`blocked` would be a terminal state with a special reap rule -- a policy
decision. `spawning` is a transient state with the existing rules
(non-terminal: keep), and it exists to make a lock boundary safe, not to
encode a judgement.

### 7.3 Upsert, never conditional write, in phase 3

A phase 3 that only fills a record it finds in `spawning` would, on the
expired-and-reaped path, leave a live pane with no record -- the exact leak
the strand path exists to prevent. Writing the truth unconditionally is
simpler and cannot strand.

### 7.4 The session-id backfill is deleted, not moved

It was inside the lock; moving it to phase 2 would keep a 30 s call whose
only effect `Reconcile` reproduces on its next tick. `Bind` keeps its copy
because a builder is matched by session across a workspace move (#21); a
consult lives minutes and is matched by pane id plus kind for its whole
life.

### 7.5 `lockAcquireLimit` stays at 90 s

The comment's own reasoning holds: the limit is not the adjustable side.
After this change the longest hold is once again `Reconcile`'s scrape path,
and the comment gains one sentence saying `Ask` spawns outside the lock on
purpose, so the next person to add a herdr call to `Ask` reads why not.

## 8. Testing requirements

All in `internal/relay`, using the existing fakes.

1. **Lock not held during spawn** -- `onSplit` takes the lock with a 2 s
   deadline; a timeout is a failure. Also asserts the store lock is not
   held during `StartAgent` (via `startErr` being set and a Load succeeding
   inside a goroutine -- or a second hook if the builder prefers).
2. **Reservation is written before the spawn** -- `onSplit` loads the
   binding and finds one `spawning` consult with the expected id and no
   pane id.
3. **Cap counts a reservation** -- seed a `spawning` record; `Ask` at cap
   returns `ErrConsultCap` and makes no herdr calls.
4. **Success records running with pane and logs the ask** -- existing
   `TestAskSpawnsRecordsAndStagesTheQuestion` adapted; must assert the
   log entry exists and `Confirmed: true`.
5. **Split failure records silent with no pane** -- `f.newPane = ""`;
   record is `silent`, `PaneID == ""`, no `StartAgent` call.
6. **Start failure records silent with pane** -- `f.startErr` set.
7. **Prompt failure records silent with pane** -- existing test, kept.
8. **Expired reservation upserted by a late phase 3** -- seed the daemon's
   expiry (state `silent`, note "spawn did not complete…") by hand between
   phases using `onSplit`; after `Ask` returns, the record is `running`
   with a pane.
9. **Reaped reservation re-appended by a late phase 3** -- `onSplit`
   removes the record lock-free (consults spec §7.7's test note explains
   why not via `Store`); after `Ask`, the record exists and is `running`.
10. **Daemon skips a fresh reservation** -- tick with a `spawning` record
    younger than the deadline: unchanged, nothing queued, no prompt.
11. **Daemon expires a stale reservation** -- clock past the deadline: state
    `silent`, one findings entry queued whose payload does not contain
    "pane", and -- through `Daemon.Tick`, not `tickConsults` -- persisted.
12. **Reap drops a terminal record with no pane** -- no `ClosePane` call,
    record gone, listed under `Dropped`; dry-run lists and keeps it.
13. **Reap keeps a spawning record** -- extend `seedReapable`.
14. **Mutation check for the builder's report:** remove the `spawning`
    skip in `reconcileConsults` and name the test that fails (10 must);
    move `SplitPane` back inside phase 1 and name the test that fails (1
    must).

## 9. Explicitly out of scope

- Making `Reap` close panes outside the lock (§7.7 of the consults spec).
- A `--force` or blocked-aware reap (#59).
- Validating derived agent-name length (#64) -- adjacent, and `Ask` will
  be edited by both; #64 lands second and rebases.
- Any change to `Bind`'s lock structure or `resolveBuilder`'s strand
  behaviour.
