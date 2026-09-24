# ui: the pane opens on the round in flight, not the one before it (#428)

## 1. The defect

`relevo ui` opens a binding's detail pane at `row.Round - 1`
(`internal/ui/model.go`, `pointDetailAt` ~line 205 and `maybeInvalidate`
~line 316). `BindingStatus.Round` is `b.Round`, which is the *next, unsent*
round once a round has closed (`finishRound` does `b.Round++`,
`internal/relevo/reconcile.go:530`) but *is* the running round while one is in
flight. So on an in-flight round N the pane shows round N-1, and
`fetchTerminal` (`internal/ui/fetch.go` ~360) takes its past-round branch
(`round != b.Round`): round 1 reads "terminal is live; round 0 left no log",
round N>1 shows round N-1's finished log instead of the live tail.

`relevo show` already solves this (`showLive`, `internal/relevo/show.go`
~113-125): the rounds count is the highest round with a `plan` log entry. The
fix carries that same number on the status row and has the pane open on it.

**Stop rather than improvise.** If a step is impossible as written or
contradicts the code, halt and report. Do not bend a test to fit.

## 2. Change

### 2.1 `internal/relevo/status.go`

1. **`BindingStatus`** (struct at the top of the file): directly after the
   `Round` field add

   - `PlanRound int` with tag `json:"plan_round,omitempty"`.
   - Doc comment: the highest round with a `plan` log entry -- the round in
     flight while one is, the last round sent once it has closed; 0 before
     any plan. Unlike `Round`, it never names an unsent round. Same rule as
     `showLive`'s rounds count (#428).

2. **`statusRow`**: in the block that already walks `entries` (after
   `entries, err := rt.Store.ReadLog(b.Name)`, ~line 428), set
   `row.PlanRound` to the maximum `e.Round` over entries with
   `e.Kind == store.KindPlan`. Fold it into one of the existing loops over
   `entries` or add one short loop; no new store read.

### 2.2 `internal/ui/model.go`

1. Add an unexported function next to `row` (~line 145):

   `paneRound(r relevo.BindingStatus) int` -- returns `r.PlanRound` when it
   is > 0, else `r.Round - 1` (a binding with no plan yet, or a row from an
   older `relevo serve` whose JSON lacks `plan_round`). Doc comment names
   #428 and says why `Round - 1` alone is wrong mid-round.

2. `pointDetailAt` (~line 205): `round: r.Round - 1` becomes
   `round: paneRound(*r)`. Update the function's doc comment (~line 183-184),
   which says "round (row.Round - 1)", to "round (paneRound)".

3. `maybeInvalidate` (~line 316): `m.detail.round = r.Round - 1` becomes
   `m.detail.round = paneRound(*r)`.

`detail.rounds` stays `r.Round` at both sites. Nothing else in `internal/ui`
changes.

### 2.3 Tests

1. **`internal/relevo/status_test.go`** -- add `TestStatusPlanRound`:
   - `rt, _ := sentBinding(t)` (round 1 sent, in flight; `Send` logs a plan
     entry for round 1). `Status` -> `rep.Bindings[0]`: `Round == 1`,
     `PlanRound == 1`.
   - `rt, _ := seedClosedRound(t, "clean", 1)` (round 1 closed, `b.Round`
     now 2). `PlanRound == 1`, `Round == 2`.
   - `rt, _ := seedBound(t)` (bound, nothing sent): `PlanRound == 0`.
   Check the fixtures' return shapes in `internal/relevo/fixture_test.go`
   before writing; `seedBound` returns `(Runtime, <something>)`.

2. **`internal/ui/invalidation_test.go`** (~line 81-112, the test whose row is
   `Round: 4`): this test asserts surviving behaviour, so port it, do not
   delete. Keep the existing row (no `PlanRound`) and its assertion
   `detail.round == 3` -- it now pins the fallback; change its message to say
   "fallback Round-1 when PlanRound is 0". Then add a second `statusMsg` (a
   later `Last.TS`, so it invalidates again) whose row has `Round: 4,
   PlanRound: 4` and assert `detail.round == 4`.

3. **`internal/ui/pane_test.go`**, `paneModel` (~line 20): `round: b.Round - 1`
   becomes `round: paneRound(b)`. Run the `internal/ui` tests, golden files
   included; if a golden diff appears, stop and report it rather than
   regenerating (no fixture there sets `PlanRound`, so none is expected).

4. Add a unit test `TestPaneRound` in `internal/ui/model_test.go` if that
   file exists, else in `internal/ui/invalidation_test.go`: table of
   `{Round:1, PlanRound:1} -> 1`, `{Round:5, PlanRound:5} -> 5`,
   `{Round:5, PlanRound:4} -> 4`, `{Round:1, PlanRound:0} -> 0`,
   `{Round:3, PlanRound:0} -> 2`.

No test in this round executes a `cmd/relevo` subcommand.

### 2.4 Mutations

- In `paneRound`, return `r.Round - 1` unconditionally: `TestPaneRound` and
  the new half of the invalidation test fail. Revert.
- In `statusRow`, drop the `PlanRound` assignment: `TestStatusPlanRound`
  fails. Revert.

## 3. Working efficiently

- Read `internal/relevo/status.go` (struct + `statusRow`),
  `internal/relevo/fixture_test.go`, `internal/ui/model.go`,
  `internal/ui/invalidation_test.go`, `internal/ui/pane_test.go` in one
  parallel step. Every location is named above; do not search for others.
- One edit call per file.
- **Focused:** `go test -count=1 ./internal/relevo/ -run 'TestStatus' && go test -count=1 ./internal/ui/`.
- **Once at the end:** `make check`.

## 4. Steps

1. §2.1 + §2.3.1. Verify: `go test -count=1 ./internal/relevo/ -run TestStatusPlanRound`.
2. §2.2 + §2.3.2-4. Verify: `go test -count=1 ./internal/ui/`.
3. §2.4 mutations, each reverted.
4. `make check` passes. Then one commit:

       fix(ui): the pane opens on the round in flight, not the one before it (#428)

   Do not push. The report lists both mutations and the tests that failed for
   each, and `git diff --stat` (expected: the five files above, plus
   `model_test.go` only if it already existed).
