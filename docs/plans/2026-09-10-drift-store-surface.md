# Plan: drift store surface, and clearing the origin on rebind

Spec: `docs/specs/2026-09-10-between-rounds-drift-design.md`
Issue: #19
Covers: spec steps **2** and **6**. Nothing else.

Read sections 4.1, 4.2, 4.6, 5.5, 8.5 and 10 of the spec before starting.

## What this is

relay's diff trail covers exactly `Send -> report`. Work done after a round
closes and before the next send is recorded nowhere. The full feature closes
that gap; this plan lays only its foundation -- three additions to
`internal/store` and one line in `internal/relay/bind.go`.

Nothing reads any of it when you are done. That is expected. Do not wire it up.

## Files you may touch

```
internal/store/types.go        Binding.RoundClosedTree
internal/store/log.go          KindDrift
internal/store/store.go        DriftPath
internal/relay/bind.go         resume clears RoundClosedTree
plus the matching _test.go files
```

Touching any other file means the plan is wrong. Halt and report.

## Step 1 -- `Binding.RoundClosedTree`

Add to `store.Binding` (`internal/store/types.go`):

```go
RoundClosedTree string `json:"round_closed_tree,omitempty"`
```

Place it immediately after `RoundBaselineTree` and give it a doc comment in the
same voice as its neighbour. The comment must say:

- it is the git tree the PREVIOUS round ended at
- it is written by `queueReport` and consumed, then cleared, by the next
  successful `Send`
- empty means no drift origin exists and the next send says nothing
- it is deliberately not derived from `RoundBaselineTree`: the two describe
  different instants, and the round advance clears one while setting the other

`omitempty` is required, not stylistic. It keeps every `bind.json` already on
disk byte-identical until a round closes under the new code.

**Verify:** a `Binding` with an empty `RoundClosedTree` marshals to JSON with no
`round_closed_tree` key. Write this as a named test.

## Step 2 -- `KindDrift`

Add to the `Kind` block in `internal/store/log.go` (currently line 32), after
`KindDiff`:

```go
KindDrift Kind = "drift"
```

No reader needs changing. Every existing site compares `Kind` for equality
rather than switching exhaustively -- `cmd/relay/main.go:681`,
`internal/ui/list.go:93`, `internal/ui/fetch.go:93` -- so the new value is inert
in all of them. This has been checked; you do not need to re-check it.

**If you find a `Kind` switch that would silently mishandle an unknown value,
halt and report it.** Do not extend it: that would be a design decision, and it
belongs in the spec, not in this round.

## Step 3 -- `Store.DriftPath`

Add beside `DiffPath` in `internal/store/store.go`:

```go
// DriftPath is where the patch for the window between the previous round's
// report and this round's send is stored.
func (s *Store) DriftPath(name string, round int) string {
	return s.roundFile(name, round, "drift", ".patch")
}
```

Note the round it is keyed to is the round **about to open**, not the one that
closed. Section 8.2 of the spec explains why; the doc comment should not
re-argue it, but must not contradict it either.

**Verify:** `DriftPath("webshop", 5)` ends in `005-drift.patch` and sits in the
same directory as `DiffPath("webshop", 5)`.

## Step 4 -- clear the origin on rebind

In `internal/relay/bind.go`, inside `resume`'s locked block, in the
`if rebinding { ... }` branch that already clears `BuilderScreen` and
`BuilderScreenAt`, add:

```go
b.RoundClosedTree = ""
```

Then update `resume`'s postcondition comment. It currently reads:

> Round, CWD, Name, RoundBaselineTree and the round log are untouched.

It must now also state that `RoundClosedTree` is cleared when a builder was
supplied. Say why in one line: a tree that changed hands says nothing about a
builder that no longer exists.

This one line covers all three recovery routes -- an ordinary rebind,
`--assume-dead`, and BROKEN recovery -- because every one of them reaches
`resume` with `rebinding == true`. Do not add a second clear site.

**Verify, as two named tests:**

1. A rebind (`opts.Alias` or `opts.BuilderPane` set) clears `RoundClosedTree`.
2. A planner-only resume (`rebinding == false`) leaves it **untouched**. This is
   the one that catches a clear written outside the `if rebinding` block.

## Definition of done

- `make check` is clean. It is stricter than `go test ./...`: it adds
  `gofmt -l .` over the whole tree, `go vet`, and a `go mod tidy` check.
- The four named tests above pass.
- Every pre-existing test passes **unmodified**. If you needed to edit an
  existing test, the change is wrong -- halt and report instead.
- `git diff --stat` touches only the files listed above.

## Halt and report rather than improvise

Per the project's working agreement: if a step is impossible as written, or
conflicts with what is actually in the code, stop and say so. A halt that
surfaces a design error is worth more than a green suite that bent a test to
fit. In particular, halt if:

- a `Kind` switch exists that this plan says does not
- `resume` has no `if rebinding` branch matching the description above
- `roundFile` has a different signature than `DriftPath` assumes
