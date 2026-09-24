# ui-pane-round r2: pin pointDetailAt's round (#428)

## 1. The gap

Round 1 made both `internal/ui/model.go` call sites use `paneRound`. Reverting
`pointDetailAt`'s `round: paneRound(*r),` (~line 217) back to
`round: r.Round - 1,` leaves `go test ./internal/ui/` green: nothing pins the
site that opens the pane -- the one that showed "round 0 left no log".
This round adds that test. No production code changes.

**Stop rather than improvise.** If a step is impossible as written or
contradicts the code, halt and report.

## 2. Change

**`internal/ui/model_test.go`**: add `TestPointDetailAtOpensOnPlanRound`
directly after `TestPointDetailAtMarksViewed` (~line 667-690), built the same
way as that test (a `store.New(t.TempDir())` with a saved binding, a
`plannerSource`, `newModel`, `m.report` set by hand, then
`m, _ = m.pointDetailAt(...)`). Two bindings in one report:

- `"inflight"`: stored `Round: 1`; row `{Name: "inflight", Round: 1,
  PlanRound: 1, Display: "ACTIVE"}`. After `pointDetailAt("inflight")`:
  `m.detail.round == 1` and `m.detail.rounds == 1`.
- `"idle"`: stored `Round: 3`; row `{Name: "idle", Round: 3, PlanRound: 2,
  Display: "ACTIVE"}`. After `pointDetailAt("idle")`:
  `m.detail.round == 2` and `m.detail.rounds == 3`.

Save both bindings to the store (so `MarkViewed` has a record).

**Mutation:** change `pointDetailAt`'s `round: paneRound(*r),` to
`round: r.Round - 1,`. The new test must fail on the `"inflight"` case.
Revert, and confirm `git diff` shows only `model_test.go`.

## 3. Working efficiently

- Read `internal/ui/model_test.go` lines 1-40 (imports) and 660-710 in one step.
- **Focused:** `go test -count=1 ./internal/ui/ -run 'TestPointDetailAt|TestPaneRound'`.
- **Once at the end:** `go test -count=1 ./internal/ui/` then `make check`.

## 4. Steps

1. §2 test. Verify with the focused command.
2. The mutation, reverted.
3. `make check` passes. Then one commit on top of round 1's:

       test(ui): pin the round pointDetailAt opens on (#428)

   Do not push. The report names the mutation and the failure it produced.
