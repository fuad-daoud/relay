# Plan: record the drift origin when a round closes

Spec: `docs/specs/2026-09-10-between-rounds-drift-design.md`
Issue: #19
Covers: spec step **3**. Nothing else.

Read sections 5.3, 6.1, 8.4 and 10 of the spec before starting.

## What this is

Wave 1 added `DiffResult.EndTree` (the tree `CaptureRoundDiff` snapshots at
report time) and `Binding.RoundClosedTree` (where that value belongs). Both
exist and are unread. This plan connects them, in `queueReport`.

Nothing consumes `RoundClosedTree` when you are done. The send-side wiring is a
separate round. Do not write it.

## Files you may touch

```
internal/relay/reconcile.go
internal/relay/reconcile_test.go
```

Touching any other file means the plan is wrong. Halt and report.

## The change

In `queueReport` (`internal/relay/reconcile.go`), the diff capture is guarded:

```go
if !HasEntry(entries, b.Round, store.DirToPlanner, store.KindDiff) {
        result := CaptureRoundDiff(ctx, rt, b)
        ...
}
```

and further down the function clears `b.RoundBaselineTree = ""` as the round
advances.

Declare a variable **outside** that guard, assign it from the capture **inside**
the guard, and assign it to the binding **unconditionally** where
`RoundBaselineTree` is cleared:

```go
closed := ""

if !HasEntry(entries, b.Round, store.DirToPlanner, store.KindDiff) {
        result := CaptureRoundDiff(ctx, rt, b)
        closed = result.EndTree
        ... existing body, unchanged ...
}

... existing round advance ...
b.RoundBaselineTree = ""
b.RoundClosedTree = closed        // unconditional
```

### Read this before you write it

The unconditional assignment is the entire point of the step, and the obvious
implementation gets it wrong.

`queueReport` has a **retry path**: when a `KindDiff` entry for the round
already exists, the guard is false and `CaptureRoundDiff` never runs. If you
assign `b.RoundClosedTree` only inside the guard, that path leaves the
*previous* round's tree sitting in the field. The next send would then measure
drift against an origin two rounds old and label the result as one round's
worth. It fails silently and produces a plausible wrong answer, which is the
worst failure this feature can have.

Initialising `closed` to `""` and assigning always makes the retry path record
"no origin", which is correct: no snapshot was taken, so there is nothing to
compare against later.

Do not add an `if closed != ""` guard around the assignment. That reintroduces
the bug.

## Tests

Three named cases in `internal/relay/reconcile_test.go`:

1. **ordinary close** -> `RoundClosedTree` is the tree `CaptureRoundDiff`
   snapshotted. Assert against the tree id your fake `Git` returned, not against
   a non-empty check.
2. **retry path** -> with a `KindDiff` entry already present for the round, and
   a non-empty `RoundClosedTree` from a previous round seeded on the binding,
   `RoundClosedTree` is `""` afterwards. Seed it with a recognisable value like
   `"stale-tree-from-round-3"` so a failure message shows what leaked.
3. **non-git tree** (`rt.Git == nil`) -> `RoundClosedTree` is `""`, and the
   round still advances normally.

### Mutation-check case 2 before you call this done

Move the `b.RoundClosedTree = closed` assignment inside the `HasEntry` guard,
confirm case 2 fails, then put it back. Report that you did this and what the
failure said. A test that passes both with and without the logic is not pinning
anything.

## Definition of done

- `make check` is clean. It is stricter than `go test ./...`: it adds
  `gofmt -l .` over the whole tree, `go vet`, and a `go mod tidy` check.
- The three named cases pass.
- Every pre-existing test passes **unmodified**. If one breaks, the change is
  wrong -- halt and report instead of editing it.
- `git diff --stat` touches only the two files listed above.

## Halt and report rather than improvise

If a step is impossible as written or conflicts with the code, stop and say so.
A halt that surfaces a design error is worth more than a green suite that bent a
test to fit. In particular, halt if:

- `queueReport` does not have the `HasEntry` guard this plan describes
- there is no single place where the round advance clears `RoundBaselineTree`
- `DiffResult` has no `EndTree` field (wave 1 did not land; do not add it)
