# Between-rounds drift capture

- Date: 2026-09-10
- Issue: [#19](https://github.com/fuad-daoud/relay/issues/19)
- Status: approved, not yet planned

## 1. System overview

`CaptureBaseline` runs in `Send` and `CaptureRoundDiff` runs when a round's
report is queued, so a captured patch covers exactly **Send -> report**.
Anything that changes the working tree *after* a round closes and *before* the
next `Send` is recorded nowhere. The per-round diff exists so the planner can
audit what a builder actually did; a gap between rounds means the trail is a
history of the relayed windows, not of the tree -- and nothing says so.

`relay diff` reads as if it were the latter. That is the defect.

This design closes the gap by keeping a value relay already computes and throws
away. `CaptureRoundDiff` snapshots the tree at report time to produce the round
diff, then discards that tree id. Retaining it gives an origin to compare the
next round's baseline against, so the between-rounds window becomes a patch on
the same footing as a round's own.

Cost at round close is **zero new git calls**. Cost at send is one string
comparison, and a `DiffTrees` only when that comparison shows the tree moved.

### Scope boundary

relay reports that the tree moved and what moved. It does not attribute,
prevent, or adjudicate, and drift never changes a binding's `State` or
`Display`. This is the line [#52](https://github.com/fuad-daoud/relay/issues/52)
drew for foreign agents, held in the same place: `herdr agent list` observes
*who was in the tree*, drift observes *what changed in it*, and correlating the
two is the planner's judgement to make. The two features are deliberately not
wired together.

## 2. Constraints the design is derived from

Each of these rules out an otherwise obvious design.

1. **`SnapshotTree` captures content, not history.** It copies the repo index to
   a temp index, runs `add -A`, and writes a tree (`internal/git/client.go:100`).
   A planner that *commits* or *amends* the builder's work therefore produces no
   drift, because the content did not move. Only real edits, merges, and
   checkouts register. The signal is already narrow without any filtering, and
   no filtering may be added -- a filter would be a judgement.

2. **A snapshot is expensive; a tree-id comparison is free.** Snapshotting is a
   temp-index copy plus `add -A` over the whole tree. Eager per-tick detection
   would pay that for every idle binding on every poll, so detection is **lazy**,
   at the next `Send`. That is also the moment the planner can act on it.

3. **A round advance must never be blocked by a failed diff.** `CaptureBaseline`
   and `CaptureRoundDiff` both document that they never return an error
   (`internal/relay/capture.go:59,78`). Drift capture inherits that posture
   exactly: a send must not fail because a patch did.

4. **`Confirmed: true` is what makes a log entry a record rather than a
   delivery.** `pendingForPlanner` returns the newest entry matching
   `Direction == DirToPlanner && !Confirmed` (`internal/store/log.go:203`). An
   unconfirmed drift entry would shadow a pending report in the `held` / `relay
   pull` path. The existing `KindDiff` entry already sets `Confirmed: true` for
   this reason; drift must too.

5. **The planner runs `relay send` itself.** It does not need delivery
   machinery to learn about drift -- it is standing in the command. Printing on
   stdout costs nothing and cannot interact with anti-clobber.

6. **`queueReport` is the only round advance for a live binding**
   (`internal/relay/reconcile.go:423`; `fork.go:203` constructs a new binding
   rather than advancing one). There is exactly one place to record the close
   tree.

## 3. File structure

```
internal/relay/
  drift.go              NEW  DriftResult, CaptureDrift, DriftSummary, DriftLine,
                             ReadDrift
  drift_test.go         NEW
  capture.go            MOD  DiffResult gains EndTree; CaptureRoundDiff fills it
  reconcile.go          MOD  queueReport stores b.RoundClosedTree
  send.go               MOD  SendResult; drift capture inside the locked block
  bind.go               MOD  resume clears RoundClosedTree on rebind
internal/store/
  types.go              MOD  Binding.RoundClosedTree
  log.go                MOD  KindDrift
  store.go              MOD  DriftPath
cmd/relay/
  main.go               MOD  cmdSend prints the drift line; cmdDiff --drift
docs/
  design.md             MOD  the trail's coverage, stated plainly
  README.md             MOD  relay diff --drift
```

No new package. Drift is the same operation as a round diff against a different
origin, and it belongs beside it.

## 4. Data structures and type definitions

### 4.1 `store.Binding.RoundClosedTree` (new field)

| field | type | json | description |
| --- | --- | --- | --- |
| `RoundClosedTree` | `string` | `round_closed_tree,omitempty` | The git tree object the PREVIOUS round ended at, written by `queueReport` from the snapshot `CaptureRoundDiff` already took, and consumed (then cleared) by the next successful `Send`. Empty means no drift origin exists, and the next send says nothing. |

`omitempty`, so every existing `bind.json` on disk stays byte-identical until a
round closes under the new code. A binding created before this feature simply
produces no drift line for its next send, and self-heals one round later.

Deliberately **not** derived from `RoundBaselineTree`: the two describe
different instants, and the round advance clears one while setting the other.

### 4.2 `store.KindDrift` (new constant)

```
KindDrift Kind = "drift"
```

Added beside `KindPlan`, `KindReport`, `KindQuestion`, `KindDiff`
(`internal/store/log.go:32`). Every existing reader compares `Kind` for
equality rather than switching exhaustively -- `cmd/relay/main.go:681`,
`internal/ui/list.go:93`, `internal/ui/fetch.go:93` -- so `KindDrift` is inert
in all of them and no reader needs a change. In particular `fetch.go` filters
pending to `KindReport || KindQuestion`, which is why drift never reaches the
TUI's pending row without a deliberate later change.

### 4.3 `relay.DiffResult` (modified)

Gains one field:

| field | type | description |
| --- | --- | --- |
| `EndTree` | `string` | The tree snapshotted at round close. Non-empty whenever the snapshot itself succeeded -- including when the subsequent `DiffTrees` failed, because a successful snapshot is a valid drift origin regardless of what the comparison did. |

`EndTree` is populated on **every** return path after the snapshot succeeds, and
empty on every return path before it. Note the consequence: `CaptureRoundDiff`
returns early on `b.RoundBaselineTree == ""` *before* snapshotting, so a round
with no baseline also yields no close tree. See 8.4.

### 4.4 `relay.DriftResult` (new)

What one between-rounds capture attempt produced. A zero value means "nothing to
say", which is the normal outcome.

| field | type | description |
| --- | --- | --- |
| `Available` | `bool` | a comparison actually ran, or was short-circuited by equal trees |
| `Path` | `string` | patch file; `""` when the trees matched or the body was truncated |
| `Stat` | `git.Stat` | exact whenever `Available` |
| `Truncated` | `bool` | patch omitted because it exceeded the client's cap |
| `Reason` | `string` | why `Available` is false; `""` when it is true |

Field-for-field identical to `DiffResult` minus `EndTree`, and that is
intentional rather than an invitation to merge them. **Do not unify the two
types or their renderers.** The labels differ (`Drift:` vs `Diff:`), the empty
case differs ("no drift" vs "no file changes"), and a shared renderer taking a
label plus two booleans is harder to read than the duplication it removes.

### 4.5 `relay.SendResult` (new)

`Send` returns this instead of a bare `int`.

| field | type | description |
| --- | --- | --- |
| `Round` | `int` | the round the plan was filed under, as before |
| `Drift` | `string` | the line for stdout, or `""` when there is nothing to say |

One production caller (`cmd/relay/main.go:604`); the rest are tests.

### 4.6 `store.Store.DriftPath` (new method)

```
DriftPath(name string, round int) string
```

`roundFile(name, round, "drift", ".patch")`, yielding `NNN-drift.patch` beside
the round's `NNN-diff.patch`. Keyed to the round **about to open**, not the one
that closed -- see 8.2.

## 5. Interface definitions and component contracts

### 5.1 `internal/relay/drift.go`

```
func CaptureDrift(ctx context.Context, rt Runtime, b store.Binding, baseline string) DriftResult
```

Single responsibility: compare the tree a round closed at against the tree the
next round is opening at, and persist the patch.

- **Preconditions:** none. `baseline` is the value `CaptureBaseline` returned
  for this send; `b` is the binding as loaded under the state lock.
- **Postconditions:**
  - `Available: false`, `Reason: ""` when `rt.Git == nil`, `b.RoundClosedTree`
    is empty, or `baseline` is empty. Nothing is written and no git call runs.
  - `Available: true` with an empty `Stat` when `baseline == b.RoundClosedTree`.
    This is a positive finding -- relay looked and nothing moved -- and it
    short-circuits before any git call.
  - `Available: false` with a `Reason` when `DiffTrees` failed for a reason
    other than `git.ErrNotRepo`. `ErrNotRepo` yields `Available: false` with an
    empty `Reason`, matching `CaptureRoundDiff`.
  - `Available: true` with `Truncated` and an exact `Stat`, and no `Path`, when
    the body exceeded the cap.
  - `Available: true` with `Path` naming an existing file otherwise.
- **Errors:** none, ever. Every failure lands in `Reason`.
- **Depends on:** `rt.Git`, `rt.Store.DriftPath`.

```
func DriftSummary(res DriftResult) string
```

The `Note` for the log entry. `"unavailable: <reason>"` / `"unavailable"` /
`"no drift"` / `"truncated"` / `"3 files, +40 -2"`.

```
func DriftLine(res DriftResult) string
```

The stdout line. Returns `""` when there is nothing worth telling the planner --
`rt.Git` off, not a repository, **or an empty `Stat`**. That last case is what
keeps a quiet send quiet.

When there is something to say:

```
drift: 3 files, +40 -2 between round 4's report and this send
       /home/u/.local/state/relay/webshop/005-drift.patch
```

The round named in the prose is `b.Round - 1` (the round that closed); the file
is keyed to `b.Round` (the round opening). Both appear so the asymmetry in 8.2
is visible rather than surprising.

```
func ReadDrift(rt Runtime, name string, round int) ([]byte, bool, error)
```

Mirrors `ReadDiff` exactly, against `DriftPath`. Errors: `store.ErrNotFound` for
an unknown binding; a wrapped read error.

### 5.2 `internal/relay/capture.go` (modified)

`CaptureRoundDiff`'s contract gains one postcondition:

> `EndTree` is the snapshotted tree whenever the snapshot succeeded, and empty
> otherwise. It is set independently of `Available`.

No behaviour change otherwise.

### 5.3 `internal/relay/reconcile.go` (modified)

`queueReport`'s postconditions gain:

> `RoundClosedTree` is the tree the closing round ended at, or `""` when no
> snapshot was taken -- including the retry path where a `KindDiff` entry for
> the round already exists.

### 5.4 `internal/relay/send.go` (modified)

`Send`'s postconditions gain:

> On success, `RoundClosedTree` is cleared, and a `KindDrift` entry exists for
> the opening round when the tree moved or the comparison failed. On any
> failure -- including a failed prompt -- `RoundClosedTree` is untouched, so the
> drift is reported by the next send that succeeds.

### 5.5 `internal/relay/bind.go` (modified)

`resume`'s postcondition block currently reads "Round, CWD, Name,
`RoundBaselineTree` and the round log are untouched." It gains, inside the
`if rebinding` branch:

> `RoundClosedTree` is cleared.

A tree that changed hands says nothing about a builder that no longer exists.
One clear site covers all three recovery routes -- an ordinary rebind,
`--assume-dead`, and BROKEN recovery -- because every one of them reaches
`resume` with `rebinding == true`.

### 5.6 `cmd/relay/main.go` (modified)

`cmdSend`: print `res.Drift` **before** the existing `sent round %d` line when
it is non-empty, so the audit note is not buried under the confirmation.

`cmdDiff`: gains `--drift`, which switches both the patch source
(`ReadDrift`), the `--stat` lookup kind (`KindDrift`), and the default round --
see 8.2. `--drift` composes with `--stat` and with `--round`.

## 6. High-level pseudocode

### 6.1 Round close, in `queueReport`

```
closed := ""

if no KindDiff entry exists for b.Round:
    result := CaptureRoundDiff(ctx, rt, b)
    closed = result.EndTree
    append KindDiff entry
    payload += DiffLine(result)

... existing report queueing ...

b.Round++
b.RoundBaselineTree = ""
b.RoundClosedTree = closed        // UNCONDITIONAL
```

`closed` is declared **outside** the guard and assigned unconditionally at the
bottom. This is the whole subtlety of the step. On the retry path the guard is
false, `CaptureRoundDiff` never runs, and a conditional assignment would leave
the *previous* round's tree in place -- drift silently measured against the
wrong origin. Initialising to `""` and assigning always makes the retry path
report nothing, which is correct.

### 6.2 Send

```
pre-lock:
    hint, err := rt.Store.Load(name)
    if err == nil:
        baseline  = CaptureBaseline(ctx, rt, hint)
        hintRound = hint.Round            // staleness token, nothing else
        ... existing agent location ...

under the lock, immediately after the KindPlan entry is appended:

    driftLine := ""
    if b.Round == hintRound:
        res := CaptureDrift(ctx, rt, b, baseline)
        if (res.Available and not res.Stat.Empty()) or res.Reason != "":
            append LogEntry{
                TS: now, Round: b.Round,
                Direction: DirToPlanner, Kind: KindDrift,
                Path: res.Path, Note: DriftSummary(res),
                Confirmed: true,
            }
            driftLine = DriftLine(res)

    round = b.Round
    b.RoundBaselineTree = baseline
    b.RoundClosedTree   = ""
    ... existing tail ...

after the lock:
    return SendResult{Round: round, Drift: driftLine}
```

Three placement decisions, each load-bearing:

**`b.Round == hintRound` is the staleness guard.** `baseline` was snapshotted
before the lock was taken. If a round advanced in between, that snapshot belongs
to the previous round and comparing it proves nothing. Skipping outright is the
same pattern `locatedBuilder` and `SameAgent` already apply a few lines above,
and it costs at most one missed drift report on a genuine race. Only `hintRound`
travels from the pre-lock read -- `b.RoundClosedTree` is read fresh under the
lock, so it can never be stale.

**Capture runs after `promptWithRetry` succeeds**, beside the `KindPlan` append.
The existing code appends the plan entry only after the prompt lands, for the
same reason: a send that did not happen must not consume the drift record or
leave a `KindDrift` entry for a round the builder never received.

**Only a real finding is logged.** Equal trees produce no entry and no line, so
the common case adds nothing to the log. A failure is logged, because "we could
not tell" is worth recording.

### 6.3 Reading it back

```
relay diff --drift                  -> patch for the current round
relay diff --drift --stat           -> the KindDrift entry's Note
relay diff --drift --round 5        -> patch for round 5
relay diff                          -> unchanged in every respect
```

## 7. Error handling strategy

**No new error category.** Drift capture introduces no error path a caller must
handle: `CaptureDrift` returns no error by construction, and every failure is a
`Reason` string that renders into the log Note and the stdout line.

**Failures are reported, not swallowed.** A `DiffTrees` that fails produces
`drift: unavailable (<reason>)` on stdout and a matching Note. The planner is
told that relay could not look, which is different from being told nothing
moved, and the two must never render alike.

**`git.ErrNotRepo` is silence, not a failure.** A binding on a non-git tree is a
supported configuration, and it already produces no round diff. It produces no
drift line either, with no `Reason`.

**Recoverability.** Every drift failure is non-recoverable and inconsequential:
relay drops the observation and the send proceeds. There is no retry, and no
state records that a capture was attempted -- `RoundClosedTree` is cleared on a
successful send whether or not the comparison worked, because keeping it would
compare the *next* round against a two-round-old origin and label the result as
one round's drift.

**Observability.** The `KindDrift` log entry is the durable record; stdout is
the immediate one. No hook event fires -- `state_changed` would need a
definition of "new drift" and per-binding dedupe state, and drift is not a
state.

## 8. Behavioural rules and their rationale

### 8.1 A commit is not drift

Because `SnapshotTree` writes a tree from `add -A` over the working tree, a
planner committing, amending, or rebasing the builder's work leaves the content
identical and produces no drift. A merge that brings in new content, a checkout
that changes files, and a builder that kept editing after it reported all do
produce drift. Document this in `docs/design.md`: the narrowness is a property
worth relying on, and a reader who does not know it will misread a quiet send.

### 8.2 Drift is keyed to the round that opens, not the one that closed

The window sits between round N's report and round N+1's send, so it belongs to
neither cleanly. It is keyed to **N+1** for two reasons: it is discovered at
N+1's send, and round N's log entries are complete and immutable by the time it
is known. Appending to a closed round would make "the round is finished" a
statement with an exception in it.

The visible consequence is that `relay diff --drift` defaults to `b.Round`, the
currently open round, while `relay diff` defaults to `b.Round - 1`, the newest
completed one. Both defaults name the newest thing of their kind. `DriftLine`
prints both round numbers so the offset is never inferred.

### 8.3 Drift never changes state

No `State`, no `Display`, no notification, no hook. A drifted tree is an
observation. Promoting it would make relay assert that drift is a problem, and
in the most common case -- the planner merging main into the builder's worktree
between rounds -- it is routine.

### 8.4 Known limitation: a round with no baseline yields no close tree

`CaptureRoundDiff` returns before snapshotting when `RoundBaselineTree` is
empty, so a round that produced no diff also records no drift origin, and the
following send is silent. Accepted rather than fixed: adding an independent
snapshot in `queueReport` would put a fresh `add -A` on the round-advance path
purely to cover a case that self-heals after one round with a baseline. The
"silent when absent" posture makes this indistinguishable from every other
absent-origin case, which is the point.

### 8.5 A fork carries no drift origin

`Fork` constructs a fresh `store.Binding` literal (`internal/relay/fork.go:196`)
and copies neither `RoundBaselineTree` nor `RoundClosedTree`. That stays true.
A fork's builder runs in a **different worktree**, so a tree id measured in the
source names nothing there, and carrying it would produce a first-send drift
report describing the difference between two directories rather than a change
over time. The fork's first send is silent; its second is normal.

## 9. Testing requirements

Unit, table-driven where the shape allows.

**`CaptureRoundDiff` / `EndTree`** --
- successful snapshot and successful diff -> `EndTree` set, `Available` true
- successful snapshot, `DiffTrees` fails -> `EndTree` **still set**,
  `Available` false with a `Reason` (this is the pairing a naive implementation
  gets wrong)
- `rt.Git == nil` -> `EndTree` empty
- `RoundBaselineTree == ""` -> `EndTree` empty, and no snapshot was attempted
- snapshot returns `ErrNotRepo` -> `EndTree` empty

**`queueReport` / `RoundClosedTree`** --
- ordinary close -> stores the tree `CaptureRoundDiff` snapshotted
- **retry path**, a `KindDiff` entry already exists for the round -> stores
  `""`, not the previous round's value. This is the mutation-testable guard for
  6.1: move the assignment inside the `HasEntry` block and this named test must
  fail.
- non-git tree -> stores `""`, and the round still advances

**`CaptureDrift`** --
- equal trees -> `Available` true, empty `Stat`, **no git call made**
- differing trees -> `Available` true, exact `Stat`, patch written at
  `DriftPath(name, round)` and readable
- `b.RoundClosedTree == ""` -> `Available` false, no `Reason`, no git call
- `baseline == ""` -> same
- `rt.Git == nil` -> same
- `DiffTrees` returns `ErrNotRepo` -> `Available` false, empty `Reason`
- `DiffTrees` returns any other error -> `Available` false, `Reason` set
- truncated body -> `Available` true, `Truncated` true, `Path` empty, `Stat`
  exact

**`Send` integration** --
- unchanged tree between rounds -> no `KindDrift` entry, `SendResult.Drift`
  empty, stdout unchanged from today
- changed tree between rounds -> one `KindDrift` entry keyed to the **opening**
  round, `Confirmed: true`, `Path` readable
- **the drift entry does not shadow a pending report**: queue a report, drift
  the tree, send, then assert `Pull` returns the report. This is the
  mutation-testable guard for constraint 4: flip `Confirmed` to false and this
  named test must fail.
- round 1, no close tree -> silent
- **failed prompt** -> `RoundClosedTree` still set afterwards, no `KindDrift`
  entry written, and the *next* successful send reports the drift
- concurrent round advance between the pre-lock load and the lock
  (`b.Round != hintRound`) -> skipped, no entry, send otherwise normal
- successful send -> `RoundClosedTree` cleared

**`resume`** -- a rebind clears `RoundClosedTree`; a planner-only resume
(`rebinding == false`) leaves it alone.

**`Fork`** -- a forked binding has an empty `RoundClosedTree`, and its first
send emits no drift line.

**`cmdDiff --drift`** -- defaults to the current round; `--round N` overrides;
`--stat` prints the `KindDrift` Note; a round with no drift errors with a
message naming `--drift`; plain `relay diff` output is byte-identical to today.

**`DriftLine` / `DriftSummary`** -- golden strings for each result shape,
including the empty-`Stat` case returning `""` from `DriftLine`.

## 10. Ordered implementation steps

Each step is independently verifiable and leaves the tree green (`make check`).

**Step 1 -- `EndTree` on `DiffResult`.**
Deliverable: the field, populated on every post-snapshot return path in
`CaptureRoundDiff`, plus its contract comment.
Depends on: nothing.
Implements: 4.3, 5.2.
Verify: the five `EndTree` cases in section 9 pass; every existing
`capture_test.go` case passes **unmodified**. Nothing reads the field yet.

**Step 2 -- store surface.**
Deliverable: `Binding.RoundClosedTree` with its doc comment, `KindDrift`,
`Store.DriftPath`.
Depends on: nothing (parallel with step 1, different package).
Implements: 4.1, 4.2, 4.6.
Verify: an existing `bind.json` round-trips byte-identically through load/save;
`DriftPath` yields `005-drift.patch`. No reader changes are needed (4.2); if you
find a `Kind` switch this spec missed, halt and report rather than extending it.

**Step 3 -- record the close tree.**
Deliverable: `queueReport` stores `RoundClosedTree`, unconditionally, from
step 1's `EndTree`.
Depends on: steps 1 and 2.
Implements: 5.3, 6.1.
Verify: the three `queueReport` cases in section 9, including the retry-path
mutation guard. Still nothing reads the field.

**Step 4 -- `CaptureDrift` and its renderers.**
Deliverable: `internal/relay/drift.go` plus `drift_test.go` -- `DriftResult`,
`CaptureDrift`, `DriftSummary`, `DriftLine`, `ReadDrift`.
Depends on: step 2.
Implements: 4.4, 5.1.
Verify: the eight `CaptureDrift` cases and the renderer goldens pass. Assert
explicitly that the equal-trees case makes no git call -- a fake `Git` that
fails the test if `DiffTrees` is reached. Nothing calls this yet.

**Step 5 -- wire drift into `Send`.**
Deliverable: `SendResult`, the locked-block capture, `RoundClosedTree` cleared
on success.
Depends on: steps 3 and 4.
Implements: 4.5, 5.4, 6.2.
Verify: the full `Send` integration list in section 9, both mutation guards
included. `cmd/relay/main.go:604` updated for the new return type; the existing
`sent round %d` line is byte-identical when there is no drift.

**Step 6 -- clear on rebind.**
Deliverable: `b.RoundClosedTree = ""` inside `resume`'s `if rebinding` block,
and the postcondition comment.
Depends on: step 2.
Implements: 5.5, 8.5.
Verify: the `resume` and `Fork` cases in section 9.

**Step 7 -- `relay diff --drift`.**
Deliverable: the flag, the round default, the `--stat` kind switch.
Depends on: steps 4 and 5.
Implements: 5.6, 6.3, 8.2.
Verify: the five `cmdDiff` cases in section 9.

**Step 8 -- documentation.**
Deliverable: `docs/design.md` states what the diff trail covers and that a
commit is not drift (8.1, 8.3); `relay diff --help` and the README document
`--drift`.
Depends on: steps 5 and 7.
Verify: a reader of `docs/design.md` can say, without reading code, which of
{builder kept working, planner committed, planner merged main} produce a drift
line.

## 11. Explicitly out of scope

- **Attributing drift to an actor.** relay cannot observe writes. #52's
  foreign-agent rows say who was in the tree; joining that to a drift patch is
  the planner's judgement, and relay must not make it.
- **Eager, per-tick drift detection.** Constraint 2. A binding that has drifted
  shows nothing in `relay status` until the next send.
- **Any hook event or notification for drift.** Section 7, and 8.3.
- **Surfacing drift in the TUI.** `relay diff --drift` only. The detail screen
  can gain a row later; it needs its own design for how an entry belonging to an
  *open* round renders beside entries belonging to closed ones.
- **Annotating a round's own diff as unreliably attributed** -- #40's second
  open question. That requires observing *during* a round, which is daemon work
  and a different design.
- **Backfilling drift for existing bindings.** There is no origin to backfill
  from. Existing bindings self-heal after one round closes.
- **Merging `DriftResult` with `DiffResult`.** Section 4.4, stated there because
  it is the refactor a reader of this spec is most likely to attempt.
