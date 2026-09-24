# ui-pane-round r3: port the #428 fix onto the cockpit rewrite (#429)

## 1. Why

Rounds 1-2 fixed #428 on `c47b705c` (commits `cd097f91`, `08920178`). Since
then `main` took #429 (`26dd5d6e`), which deleted `internal/ui/model.go` and
moved the pane into `internal/ui/round_pane.go` (`roundPane`) and
`internal/ui/view_round.go`. The same bug lives on there:
`round_pane.go:100` (`round: r.Round - 1,` in `pointDetailAt`) and
`round_pane.go:190` (`p.detail.round = r.Round - 1` in `invalidate`). PR #431
conflicts. This round rebuilds the branch on `origin/main` as ONE commit with
the same fix in the new structure.

The `internal/relevo` half (`BindingStatus.PlanRound`, its `statusRow` loop,
`TestStatusPlanRound`) is unchanged; it only has to be re-applied.

**Stop rather than improvise.** If a step is impossible as written or
contradicts the code (a patch that does not apply cleanly, a line that is not
where this plan says), halt and report. Do not bend a test to fit.

## 2. Change

### 2.1 Rebase point

1. `git fetch origin && git reset --hard origin/main` on branch
   `relevo/ui-pane-round` (the old tip `08920178` stays reachable by hash).
2. `git diff c47b705c 08920178 -- internal/relevo/ | git apply --3way`.
   It must apply with no conflict; if not, halt.

### 2.2 `internal/ui/view_round.go`

1. Directly after `row` (~line 78-85) add the function round 1 added to the
   old `model.go` -- copy it verbatim from `git show 08920178:internal/ui/model.go`
   (`paneRound(r relevo.BindingStatus) int`, with its doc comment): returns
   `r.PlanRound` when > 0, else `r.Round - 1`.
2. `newRoundView`'s doc comment (~line 27-28): "today's pointDetailAt rule
   (Round-1)" becomes "pointDetailAt's rule (paneRound)".

### 2.3 `internal/ui/round_pane.go`

1. `pointDetailAt` (~line 100): `round:    r.Round - 1,` becomes
   `round:    paneRound(*r),`. Its doc comment (~line 78-79): "round
   (row.Round - 1)" becomes "round (paneRound)".
2. `invalidate` (~line 190): `p.detail.round = r.Round - 1` becomes
   `p.detail.round = paneRound(*r)`.

`detail.rounds` stays `r.Round` at both. Nothing else in `internal/ui` changes.

### 2.4 Tests (`internal/ui`)

1. **`pane_test.go`**, `paneModel` (~line 21): `round: b.Round - 1` becomes
   `round: paneRound(b)`.
2. **`invalidation_test.go`**, `TestStatusMsgNewerTSClearsFileCachesPreservesTerminal`
   (~line 43-97). Port, do not delete. Keep the `Round: 4` row with no
   `PlanRound` and its `== 3` assertion; change that message to
   "fallback Round-1 when PlanRound is 0: got %d". Then, at the end of the
   test, send a second `statusMsg` to `got` the same way the test sends the
   first (`got.Update(statusMsg{...}, testEnv(plannerSource{rt}, rep2, 140, 40))`),
   whose row is `{Name: name, Round: 4, PlanRound: 4, Display: "ACTIVE",
   Last: &relevo.LastEvent{TS: newTS.Add(10 * time.Second), Round: 4}}`, and
   assert the resulting `roundView`'s `pane.detail.round == 4`.
3. **`model_test.go`**: add, after `TestPointDetailAtMarksViewed` (~line 498-520):
   - `TestPaneRound`: copy verbatim from `git show 08920178:internal/ui/model_test.go`.
   - `TestPointDetailAtOpensOnPlanRound`: same cases as round 2's test in
     `08920178` (read it there), rebuilt in this file's idiom -- save the
     binding to a `store.New(t.TempDir())`, then
     `rv := newTestRound(t, rt, relevo.Report{Bindings: []relevo.BindingStatus{<row>}}, <name>, 0)`
     and assert `rv.pane.detail.round` / `rv.pane.detail.rounds`:
     `"inflight"` row `{Round: 1, PlanRound: 1, Display: "ACTIVE"}` -> 1 / 1;
     `"idle"` row `{Round: 3, PlanRound: 2, Display: "ACTIVE"}` -> 2 / 3.
     One `newTestRound` per case.

No test executes a `cmd/relevo` subcommand.

### 2.5 Mutations (each reverted, `git diff` clean of it afterwards)

1. `paneRound` returns `r.Round - 1` unconditionally -> `TestPaneRound` fails.
2. `pointDetailAt` back to `r.Round - 1` -> `TestPointDetailAtOpensOnPlanRound` fails.
3. `invalidate` back to `r.Round - 1` -> the invalidation test's `== 4` fails.
4. Drop the `row.PlanRound` loop in `statusRow` -> `TestStatusPlanRound` fails.

## 3. Working efficiently

- After §2.1, read `internal/ui/view_round.go` (1-100), `internal/ui/round_pane.go`
  (70-200), `internal/ui/pane_test.go` (1-30), `internal/ui/invalidation_test.go`
  (1-100), `internal/ui/model_test.go` (480-525), and
  `git show 08920178:internal/ui/model_test.go | tail -60` in one parallel step.
- One edit call per file.
- **Focused:** `go test -count=1 ./internal/ui/ && go test -count=1 ./internal/relevo/ -run TestStatus`.
- **Once at the end:** `make check`.

## 4. Steps

1. §2.1. Verify: `git log --oneline -1` is `origin/main`'s tip; `git diff --stat`
   shows only `internal/relevo/status.go` and `internal/relevo/status_test.go`.
2. §2.2-2.4. Verify with the focused command.
3. §2.5, all four, each reverted.
4. `make check` passes. Then exactly one commit on top of `origin/main`:

       fix(ui): the pane opens on the round in flight, not the one before it (#428)

   Do not push. The report gives `git log --oneline origin/main..HEAD` (one
   line), `git diff --stat origin/main` (expected: the two `internal/relevo`
   files and the five `internal/ui` files named above), and each mutation
   with the test that failed.
