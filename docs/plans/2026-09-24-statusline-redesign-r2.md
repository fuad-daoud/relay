# Plan r2: pin roundFacts' round match with a test case

**If a step is impossible as written or contradicts the code, stop and report.**

## 1. Overview

`roundFacts` (`internal/relevo/status.go`, after `isPayloadKind`) matches the
closing report by `e.Round == targetRound && !e.TS.Before(start)`. The planner
removed the `e.Round == targetRound` condition and `TestRoundFacts` in
`internal/relevo/status_test.go` still passed, so nothing pins it. The case
that needs it: a late report for an older round, logged *after* the newer
round's plan. That report must not close the newer round.

## 2. Files

`internal/relevo/status_test.go` only (`TestRoundFacts` table). No production change.
Do not edit `docs/plans/2026-09-24-statusline-redesign.md`, and commit this file too.

## 3. Step

Add case 8 to the `TestRoundFacts` table. Entries in order:
plan r1 (T0), plan r2 (T1), report r1 with a non-nil usage (T2 > T1).
Expected: start = T1, end = zero, usage = nil.

Verification (mutation, do it and state the result in the report):
1. Run `go test ./internal/relevo -run TestRoundFacts -count=1`. It passes.
2. Temporarily delete `e.Round == targetRound && ` from the report loop in
   `roundFacts`. Rerun: case 8 must fail. Quote the failure line.
3. Restore it (`git checkout internal/relevo/status.go`) and rerun: it passes.

## 4. Working efficiently

One read of the `TestRoundFacts` table and one edit. Run the focused command
above. Then `gofmt -l internal/relevo/` must print nothing, and
`go vet ./internal/relevo` must pass. Do not run `make check` (the planner runs
it). Make one commit: `test(statusline): pin roundFacts' round match against a late older report`.
