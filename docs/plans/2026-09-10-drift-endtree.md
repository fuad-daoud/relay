# Plan: retain the round-close tree on DiffResult

Spec: `docs/specs/2026-09-10-between-rounds-drift-design.md`
Issue: #19
Covers: spec step **1**. Nothing else.

Read sections 4.3, 5.2, 8.4 and 10 of the spec before starting.

## What this is

`CaptureRoundDiff` snapshots the working tree at report time to produce the
round's diff, compares it against the round's baseline, and then **throws the
snapshotted tree id away**. That discarded value is the origin the next round
needs in order to detect work done between rounds.

This plan retains it. One field, populated on the right return paths. Nothing
reads it when you are done -- that is expected, and you must not wire it up.

## Files you may touch

```
internal/relay/capture.go
internal/relay/capture_test.go
```

Touching any other file means the plan is wrong. Halt and report.

## Step 1 -- add the field

Add to `relay.DiffResult`:

```go
EndTree string // tree snapshotted at round close; see below
```

Document it in the same voice as the existing fields:

> The tree snapshotted at round close. Non-empty whenever the snapshot itself
> succeeded -- including when the subsequent `DiffTrees` failed, because a
> successful snapshot is a valid drift origin regardless of what the comparison
> did.

## Step 2 -- populate it on every post-snapshot return path

In `CaptureRoundDiff`, `end` is the value to keep. The rule is exact:

- **Every return before the snapshot succeeds leaves `EndTree` empty.** That is
  the `rt.Git == nil` return, the `b.RoundBaselineTree == ""` return, and both
  error returns from `SnapshotTree` itself (`git.ErrNotRepo` and any other
  error).
- **Every return after the snapshot succeeds carries `EndTree: end`.** That is
  the two `DiffTrees` error returns, the empty-stat return, the truncated
  return, the `os.WriteFile` failure return, and the success return.

Read that second bullet again. The trap is the `DiffTrees` failure paths: a
natural implementation sets `EndTree` only alongside `Available: true`, which
loses the origin in exactly the case where relay most wants it. `EndTree` is set
**independently of `Available`**.

## Step 3 -- extend the contract comment

`CaptureRoundDiff`'s postcondition block gains:

> `EndTree` is the snapshotted tree whenever the snapshot succeeded, and empty
> otherwise. It is set independently of `Available`.

Do not change any other sentence in that comment, and do not change any
behaviour. This step adds a field and fills it in. Nothing else about the
function moves.

## Step 4 -- tests

Five named cases, table-driven if the existing file's shape allows:

1. successful snapshot, successful diff -> `EndTree` set, `Available` true
2. **successful snapshot, `DiffTrees` fails -> `EndTree` still set,
   `Available` false with a `Reason`** -- this is the pairing a naive
   implementation gets wrong, so name the test so a reader knows what it pins
3. `rt.Git == nil` -> `EndTree` empty
4. `b.RoundBaselineTree == ""` -> `EndTree` empty, **and no snapshot was
   attempted** (assert this against the fake `Git`, do not just check the field)
5. `SnapshotTree` returns `git.ErrNotRepo` -> `EndTree` empty

`Git` is an interface (`internal/relay/herdr.go:32`), so a fake that records
calls and returns programmed errors is straightforward. Look for one already in
the package's tests before writing a new one.

### Mutation-check case 2 before you call this done

Make `EndTree` be set only on the success path, confirm the case-2 test fails,
then put it back. A test that passes both with and without the logic is not
pinning anything. Report in your write-up that you did this and what failed.

## Definition of done

- `make check` is clean. It is stricter than `go test ./...`: it adds
  `gofmt -l .` over the whole tree, `go vet`, and a `go mod tidy` check.
- The five named cases pass.
- Every pre-existing case in `capture_test.go` passes **unmodified**. Adding a
  field must not change any existing assertion. If one breaks, the change is
  wrong -- halt and report instead of editing the test.
- `git diff --stat` touches only the two files listed above.

## Halt and report rather than improvise

Per the project's working agreement: if a step is impossible as written, or
conflicts with what is actually in the code, stop and say so. A halt that
surfaces a design error is worth more than a green suite that bent a test to
fit. In particular, halt if:

- `CaptureRoundDiff` does not have the return structure this plan describes
- the `Git` interface cannot be faked from the test package
- adding the field breaks an existing assertion
