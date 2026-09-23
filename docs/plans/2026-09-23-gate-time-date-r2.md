# Plan, round 2: gate times show a date when not today: pin the clock in fixed-clock tests

If any step is impossible as written or contradicts the code you find, STOP
and report. Do not bend a test to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so read the "Read first"
ranges in one step (parallel reads), make each file's change in one edit
call, and loop with focused `go test` runs. Run `make check` once at the end.

## Where the tree is

Round 1 left **uncommitted**, correct work in this worktree, and it stays:
- `internal/relay/ledger.go`: `var gateClock = time.Now`, `GateTimeText`,
  `GateUntilText` delegating to it, and `gatedNote`'s since;
- `internal/relay/ledger_test.go`: `TestGateTimeText`, and
  `TestGateUntilText` pinned with the Oct 19 case;
- `internal/relay/status.go` ~595, `internal/relay/headless.go` ~526, and
  `cmd/relay/doctor.go` ~322/332, all switched to `GateTimeText`.

Start with `git status`. Those five paths must be the only changes; if they
aren't, STOP.

Round 1 halted, correctly, because two groups of tests fail. They render
against their **own fixed clock** (a past date), while the formatter
compares against the wall clock, so a gate that is "today" for the test is
printed with a date:
- A: `internal/relay/status_test.go` `TestRenderStatusGatedBlock` (a gate
  on 2026-09-11; the expectation is `until 18:10`);
- B: `internal/ui` `TestGoldenViews` (split-all-states, stack-all-states,
  stack-detail) and `TestHeaderGatesAndClock`, where `railNow` is
  2026-09-17 and the goldens say `codex gated until 15:30`.

The expected output in those tests is right: relative to the test's clock,
the gate *is* today. What's missing is a way for a test to put the
formatter on its clock. In production every surface renders at wall-clock
now, so `gateClock = time.Now` stays the default. Nothing in these packages
calls `t.Parallel()`, so swapping a package clock in a test does not race.

Read first:
- `internal/relay/ledger.go` lines 295-340 (`gateClock`, `GateTimeText`,
  `GateUntilText`)
- `internal/relay/ledger_test.go` lines 270-320 (how round 1 pins `gateClock`)
- `internal/relay/status_test.go` lines 170-212 (`TestRenderStatusGatedBlock`)
- `internal/ui/golden_test.go` lines 120-135 and 195-215, and
  `internal/ui/split_test.go` lines 470-485
- `internal/ui/rail_test.go` lines 10-25 (`railNow`)

## Design

New exported function in `internal/relay/ledger.go`, right after `gateClock`:

```
// SetGateClock replaces the clock gate times are formatted against and
// returns a func that restores the previous one. For tests that render at a
// fixed time, in this package and others (internal/ui); production never
// calls it. Not safe to use from parallel tests.
func SetGateClock(now func() time.Time) (restore func())
```

Contract: save the current `gateClock`, set it to `now`, and return a
closure that puts the saved value back.

In round 1's `ledger_test.go`, switch any direct assignment to `gateClock` to
`t.Cleanup(SetGateClock(...))`. Behaviour stays the same.

## Steps

### Step 1: the hook, and pin the two test groups

- Deliverable: `SetGateClock`, plus these test edits. They are **only** the
  clock pin; expectations and goldens do not change:
  - `internal/relay/status_test.go` `TestRenderStatusGatedBlock`: as its
    first statement after `now` is defined, add
    `t.Cleanup(SetGateClock(func() time.Time { return now }))`.
  - `internal/ui/golden_test.go` `TestGoldenViews`: as its first
    statement, add
    `t.Cleanup(relay.SetGateClock(func() time.Time { return railNow }))`.
    Add the `relay` import if the file lacks it.
  - `internal/ui/split_test.go` `TestHeaderGatesAndClock`: the same first
    statement.
- **Do not** edit any `.golden` file, `railNow`, or any expected string. If
  a golden or an expectation still fails after the pin, STOP and report the
  diff.
- Verify:
  `go test ./internal/relay/ -run 'TestGate|TestRenderStatusGatedBlock' -count=1`
  and `go test ./internal/ui/ -count=1` both pass.
- Mutation (report it): remove the pin from `TestHeaderGatesAndClock`.
  It fails with `until Sep 17 15:30`, which shows the date logic is live
  and the pin is what the test relies on. Restore.

### Step 2: full check and commit

- `make check` passes. If any **other** test fails because it renders a gate
  against a fixed past clock, apply the same one-line pin to it (the clock
  that test already uses), and list each such test in the report. Any
  other kind of failure: STOP and report.
- `git diff --stat` shows round 1's five files, plus `status_test.go`,
  `internal/ui/golden_test.go`, `internal/ui/split_test.go`, and any test
  files pinned under the rule above.
- Report: each verify output, the mutation result, and the diff stat.
  Commit everything (round 1's work and this round's) as one commit with
  the message `fix(ledger): a gate time not today shows its date`.

## Round 1's contract, for reference (unchanged)

`GateTimeText(t)`, with `t` and `gateClock()` both in `Local()`:
- same Y/M/D → `15:04`;
- same year → `Jan 2 15:04`;
- otherwise → `2006-01-02 15:04`.

`GateUntilText(zero)` → `until cleared`; otherwise `"until " + GateTimeText(until)`.

Gate "since" and "until" times at these call sites go through
`GateTimeText`: `gatedNote`, status `writeGatedBlock`, the headless
rate-limit payload, and doctor `ledgerChecks`. Non-gate times (pid start,
the ui clock, plan/report stamps) are out of scope.
