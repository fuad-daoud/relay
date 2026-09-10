# Plan: CaptureDrift and its renderers

Spec: `docs/specs/2026-09-10-between-rounds-drift-design.md`
Issue: #19
Covers: spec step **4**. Nothing else.

Read sections 4.4, 5.1, 6.3 and 10 of the spec before starting.

## What this is

A new file holding the comparison that closes the between-rounds gap: given the
tree a round ended at and the tree the next round is opening at, produce a patch
and the two strings that describe it.

Nothing calls any of this when you are done. `Send` is wired in a separate
round. Do not touch `send.go`.

## Files you may touch

```
internal/relay/drift.go        (new)
internal/relay/drift_test.go   (new)
```

Touching any other file means the plan is wrong. Halt and report.
In particular `capture.go` is finished -- read it as your model, do not edit it.

## Step 1 -- `DriftResult`

```go
type DriftResult struct {
        Available bool     // a comparison ran, or was short-circuited by equal trees
        Path      string   // patch file; "" when the trees matched or the body was truncated
        Stat      git.Stat // exact whenever Available
        Truncated bool     // patch omitted because it exceeded the cap
        Reason    string   // why Available is false; "" when it is true
}
```

This is `DiffResult` minus `EndTree`, and that duplication is deliberate.
**Do not unify the two types, and do not factor their renderers into a shared
helper.** The labels differ, the empty case differs, and a shared renderer
taking a label plus two booleans is harder to read than what it replaces. If you
think you see a clean way to merge them, that is the trap -- leave them apart.

## Step 2 -- `CaptureDrift`

```go
func CaptureDrift(ctx context.Context, rt Runtime, b store.Binding, baseline string) DriftResult
```

Model it on `CaptureRoundDiff` in `capture.go`. It **never returns an error**;
every failure lands in `Reason`. Contract, in evaluation order:

1. `rt.Git == nil`, or `b.RoundClosedTree == ""`, or `baseline == ""`
   -> `DriftResult{Available: false}`. No `Reason`. **No git call.**
2. `baseline == b.RoundClosedTree` -> `DriftResult{Available: true}` with a zero
   `Stat`. This is a positive finding: relay looked and nothing moved. It
   short-circuits **before any git call** -- comparing two strings is free and
   the whole design depends on the common case costing nothing.
3. `DiffTrees` returns `git.ErrNotRepo` -> `Available: false`, empty `Reason`.
4. `DiffTrees` returns any other error -> `Available: false`, `Reason: brief(err)`.
5. empty stat -> `Available: true`, `Stat` set, no `Path`.
6. truncated -> `Available: true`, `Stat` exact, `Truncated: true`, no `Path`.
7. otherwise write the patch to `rt.Store.DriftPath(b.Name, b.Round)` and return
   `Available: true` with `Path` set. A `WriteFile` failure is
   `Available: false, Reason: brief(err)`.

The diff direction is `DiffTrees(ctx, b.CWD, b.RoundClosedTree, baseline)` --
from where the last round ended, to where this one starts. Getting the arguments
backwards inverts every patch, so assert the order in a test.

`b.Round` is the round **about to open**, which is what `DriftPath` should be
keyed to. Do not subtract one.

## Step 3 -- `DriftSummary` and `DriftLine`

`DriftSummary(res DriftResult) string` -- the `Note` for the log entry. Mirror
`DiffSummary`'s shape: `"unavailable: <reason>"`, `"unavailable"`, `"no drift"`
for an empty stat, `"truncated"`, and `"3 files, +40 -2"` otherwise. Reuse the
existing `formatFiles` and `brief` helpers in the package; do not copy them.

`DriftLine(res DriftResult) string` -- the line printed on stdout. Returns `""`
when there is nothing worth telling the planner: `rt.Git` off, not a repository,
**or an empty `Stat`**. That last clause is what keeps a quiet send quiet, and it
is the difference from `DiffLine`, which prints "no file changes". Get it wrong
and every send grows a noise line.

When there is something to say, both round numbers appear -- the round that
closed and the file keyed to the round opening -- so the offset in spec 8.2 is
visible rather than surprising:

```
drift: 3 files, +40 -2 between round 4's report and this send
       /home/u/.local/state/relay/webshop/005-drift.patch
```

`DriftLine` takes only a `DriftResult`, so the round number must reach it. Add a
`round int` parameter -- `DriftLine(res DriftResult, round int) string`, where
`round` is the opening round and the prose names `round-1`. Say so in the doc
comment.

## Step 4 -- `ReadDrift`

```go
func ReadDrift(rt Runtime, name string, round int) ([]byte, bool, error)
```

A mirror of `ReadDiff` against `DriftPath`. Same error contract:
`store.ErrNotFound` for an unknown binding, a wrapped read error otherwise,
`(nil, false, nil)` when the file simply is not there.

## Tests

Eight `CaptureDrift` cases, one per numbered branch above plus the equal-trees
one, table-driven if it reads well. Two need explicit call assertions against
the fake `Git`:

- `b.RoundClosedTree == ""` -> **zero** `DiffTrees` calls
- equal trees -> **zero** `DiffTrees` calls

Plus: a case asserting `DiffTrees` received `(b.RoundClosedTree, baseline)` in
that order; renderer goldens for every `DriftResult` shape including the
empty-`Stat` case returning `""` from `DriftLine`; and `ReadDrift` for present,
absent, and unknown-binding.

### Mutation-check the equal-trees short-circuit

Remove the `baseline == b.RoundClosedTree` early return, confirm the
zero-`DiffTrees`-calls case fails, then put it back. Report what failed.

## Definition of done

- `make check` is clean. It is stricter than `go test ./...`: it adds
  `gofmt -l .` over the whole tree, `go vet`, and a `go mod tidy` check.
- All cases above pass.
- No existing file is modified. `git diff --stat` shows two new files only.

## Halt and report rather than improvise

If a step is impossible as written or conflicts with the code, stop and say so.
In particular, halt if:

- `Store.DriftPath` does not exist (wave 1 did not land; do not add it)
- `Binding.RoundClosedTree` does not exist (same)
- the `Git` interface cannot be faked from the test package
- you conclude `DriftResult` and `DiffResult` should be one type -- say why and
  stop, rather than merging them
