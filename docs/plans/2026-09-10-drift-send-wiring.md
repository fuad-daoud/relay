# Plan: wire drift into Send, and expose it through relay diff

Spec: `docs/specs/2026-09-10-between-rounds-drift-design.md`
Issue: #19
Covers: spec steps **5** and **7**. Nothing else.

Read sections 4.5, 5.4, 5.6, 6.2, 6.3 and 8.2 of the spec before starting.

## What this is

Everything underneath is built and unread. `Binding.RoundClosedTree` is
recorded when a round closes; `CaptureDrift`, `DriftSummary`, `DriftLine` and
`ReadDrift` all exist and pass their tests. This plan connects them to `Send`
and to `relay diff`, which is what finally makes the feature do something.

Both steps touch `cmd/relay/main.go`, which is why they are one round rather
than two.

## Files you may touch

```
internal/relay/send.go
internal/relay/send_test.go
cmd/relay/main.go
cmd/relay/main_test.go
```

Touching any other file means the plan is wrong. Halt and report.
Do not modify `drift.go`, `capture.go` or `reconcile.go` -- they are finished.

## Step 1 -- `SendResult`

`Send` currently returns `(int, error)`. Replace the `int`:

```go
// SendResult is what one successful Send produced.
type SendResult struct {
        Round int    // the round the plan was filed under
        Drift string // the drift line for stdout, or "" when there is nothing to say
}
```

Every error return becomes `SendResult{}, err`. There is one production caller
(`cmd/relay/main.go`, in `cmdSend`); the rest are tests.

## Step 2 -- capture the staleness token before the lock

`Send` loads the binding once before taking the lock, to snapshot the baseline:

```go
if hint, err := rt.Store.Load(name); err == nil {
        baseline = CaptureBaseline(ctx, rt, hint)
        ...
}
```

`hint` is scoped to that `if`, so declare a variable beside `baseline` and
record `hint.Round` into it inside the block.

**Only the round travels.** Do not carry `hint.RoundClosedTree` out --
`b.RoundClosedTree` is read fresh under the lock, where it cannot be stale.

## Step 3 -- capture drift inside the locked block

Place this **immediately after the `KindPlan` log entry is appended**, and
before `round = b.Round`:

```go
driftLine := ""
if b.Round == hintRound {
        res := CaptureDrift(ctx, rt, b, baseline)
        if (res.Available && !res.Stat.Empty()) || res.Reason != "" {
                driftEntry := store.LogEntry{
                        TS: rt.Now().UTC(), Round: b.Round,
                        Direction: store.DirToPlanner, Kind: store.KindDrift,
                        Path: res.Path, Note: DriftSummary(res),
                        Confirmed: true,
                }
                if err := tx.AppendLog(name, driftEntry); err != nil {
                        return err
                }
                driftLine = DriftLine(res, b.Round)
        }
}
```

Then, beside the existing `b.RoundBaselineTree = baseline`, add
`b.RoundClosedTree = ""`. Assign `driftLine` to a variable declared outside the
`WithLock` closure so it survives, the same way `round` already does.

### Three things here are load-bearing. Read before writing.

**`Confirmed: true` is not decoration.** `pendingForPlanner`
(`internal/store/log.go`) returns the newest entry matching
`Direction == DirToPlanner && !Confirmed`. An unconfirmed drift entry would
shadow a pending report and the planner would pull drift instead of the report
it was waiting for. The existing `KindDiff` entry sets it for the same reason.

**Placement after the prompt is not arbitrary.** `promptWithRetry` can fail and
abort the transaction. Capturing before it would consume `RoundClosedTree` and
log a drift entry for a round the builder never received. A send that did not
happen must leave the drift to be reported by the next one that does.

**`b.Round == hintRound` is a staleness guard, not a formality.** `baseline` was
snapshotted before the lock. If a round advanced in between, that snapshot
belongs to the previous round and comparing it proves nothing. Skipping outright
is the same pattern the surrounding code already applies with `locatedBuilder`
and `SameAgent`.

**Only a real finding is logged.** Equal trees produce no entry and no line, so
an ordinary send adds nothing to the log. A failure is logged, because "relay
could not look" differs from "nothing moved" and the two must never render
alike.

## Step 4 -- print it

In `cmdSend`, print `res.Drift` when non-empty **before** the existing
`sent round %d to %s's builder` line, so the audit note is not buried under the
confirmation. When there is no drift, output must be byte-identical to today.

## Step 5 -- `relay diff --drift`

Add a `--drift` bool to `cmdDiff`. It switches three things:

1. the patch source: `relay.ReadDrift` instead of `relay.ReadDiff`
2. the `--stat` lookup kind: `store.KindDrift` instead of `store.KindDiff`
3. **the default round**: `b.Round` instead of `b.Round - 1`

Point 3 is the one to get right. Drift is recorded when a round *opens*, so the
newest drift belongs to the currently open round; a round diff is recorded when
a round *closes*, so the newest one belongs to `b.Round - 1`. Both defaults name
the newest thing of their kind, and they are different numbers. Spec 8.2.

`--drift` composes with `--stat` and with an explicit `--round`. The
"no drift recorded for round N" error should name `--drift` so a user who typed
it on a round with none knows which lookup missed.

Keep `relay diff` with no `--drift` byte-identical to today, including its
error strings.

## Tests

In `send_test.go`:

1. unchanged tree between rounds -> no `KindDrift` entry, `Drift` empty
2. changed tree -> exactly one `KindDrift` entry, keyed to the **opening**
   round, `Confirmed` true, `Path` readable, `Drift` non-empty
3. **the drift entry does not shadow a pending report** -- queue a report, drift
   the tree, send, then assert `Pull` returns the *report*. Name it so a reader
   knows it pins `Confirmed`.
4. round 1 with no `RoundClosedTree` -> silent
5. **failed prompt** -> `RoundClosedTree` still set afterwards, no `KindDrift`
   entry, and a subsequent successful send reports the drift
6. concurrent round advance (`b.Round != hintRound`) -> skipped, send otherwise
   normal
7. successful send -> `RoundClosedTree` cleared

In `main_test.go`: `--drift` defaults to the current round; `--round` overrides;
`--stat` prints the `KindDrift` Note; a round with no drift errors mentioning
`--drift`.

### Mutation-check two of them before you call this done

- Flip `Confirmed` to `false` on the drift entry -> case 3 must fail.
- Remove the `b.Round == hintRound` guard -> case 6 must fail.

Report both, and what each failure said. A test that passes with and without the
logic is not pinning anything.

## Definition of done

- `make check` clean (`gofmt -l .`, `go vet`, `go mod tidy` check, full tests).
- All cases above pass.
- Every pre-existing test passes, **except** where the `SendResult` signature
  forces a mechanical edit at the call site. Those edits must change only how
  the return value is destructured -- never an assertion. If an assertion
  breaks, halt and report.
- `git diff --stat` touches only the four files listed.

## Halt and report rather than improvise

Halt if:
- there is no single place in `Send` where `RoundBaselineTree` is assigned
- `cmdDiff` does not have the `--stat` / round-defaulting shape described
- pinning case 3 requires changing anything in `internal/store`
