# Plan: remote builder parity, round 1b (store format amendment, then commit)

**If a step is impossible as written or contradicts the code, stop and report.**

## 1. Context

Round 1 (`docs/plans/2026-09-24-remote-live-r1.md`, in this tree) is implemented
but uncommitted in this worktree. Every test it added passes. The full suite
fails in exactly one place: `internal/store` `TestBindingShapeMatchesFormat`.
The new key path `builder.remote_live` needs a `BindingFormat` bump and a
regenerated `internal/store/testdata/binding-shape.golden`. Round 1's §2 did
not authorise either. This round does.

## 2. Decision (planner's; do not revisit)

`BindingFormat` becomes 3, and `recordFormat` does not change: no binding is
ever *written* at format 3. Reasons:
- `store.go:548` refuses to *load* a binding whose format is newer than the
  binary knows. Stamping 3 whenever `RemoteLive` is set would make every older
  relevo unable to read a remote binding while its round runs.
- `RemoteLive` is a poll cache: observeRemote re-fetches it every tick and
  clears it in every non-running state. An older relevo that drops the field
  on rewrite loses nothing.

## 3. Changes (closed list)

1. `internal/store/format.go`: `const BindingFormat = 2` becomes `3`. Extend its
   doc comment with: "Format 3 adds `builder.remote_live`, which recordFormat
   never stamps: it is a poll cache re-fetched on every tick, so an older relevo
   that drops it on rewrite loses nothing, while stamping it would lock older
   relevo out of loading a running remote binding (store.go Load refuses a
   newer format)." `recordFormat` is not edited, but add one sentence to its
   comment: "RemoteLive (format 3) never raises the record's format; see BindingFormat."
2. `internal/store/testdata/binding-shape.golden`: regenerate with
   `go test ./internal/store -run TestBindingShapeMatchesFormat -count=1 -update`.
   The golden diff must be exactly the new `builder.remote_live...` key paths
   plus the format number. If it shows anything else, halt.
3. If any other store test asserts the literal `BindingFormat == 2` (for
   example an ErrNewerFormat message test that uses `Know: 2`), port its
   expectation to 3 and name it in the report. If a test asserts that a record
   is *written* at format 2 or 1, it must keep passing unchanged. If it fails,
   halt.

Add one test in `internal/store` (next to the existing format tests):
`TestRemoteLiveDoesNotRaiseRecordFormat`. Save a binding with an empty Role and
`Builder.RemoteLive` set, then read the raw stored JSON (or Load it and check
`Format`, whichever the existing format tests do). Assert the stored format is
the format-1 encoding (absent / 0), not 3.

## 4. Working efficiently and verification

- Read `internal/store/format.go` and the store format tests once, then edit.
- Run `go test ./internal/store -count=1`, then the full check by its parts:
  `gofmt -l $(git ls-files '*.go')` (it must print nothing), `go vet ./...`, and
  `go test -race -count=1 ./...`. All must pass.
- `git diff --stat` must show round 1's files, the two §3 store files, the new
  test, and the docs (the spec plus the r1 and r1b plans). Nothing else.
- Make one commit containing everything, round 1's work included:
  `feat(remote): a running remote round shows the server's live facts (tokens, diff, progress, pid, tail)`.
- The report lists the golden diff summary, any ported test, and confirms the
  new test fails if `recordFormat` is made to return 3 for a binding with
  RemoteLive (a mutation you run once, then revert).
