# Plan: gate times show a date when they're not today

If any step is impossible as written or contradicts the code you find, STOP
and report. Do not bend a test to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so read the "Read first"
ranges in one step (parallel reads), make each file's change in one edit
call, and loop with `go test ./internal/relay/ ./cmd/relay/ -run '<names>' -count=1`.
Run `make check` once at the end.

Read first:
- `internal/relay/ledger.go` lines 280-330 (`GateUntilText`, `gatedNote`)
- `internal/relay/ledger_test.go` lines 265-290 (`TestGateUntilText`)
- `internal/relay/status.go` lines 585-600
- `internal/relay/headless.go` lines 518-530
- `cmd/relay/doctor.go` lines 300-340 (`ledgerChecks`)

## 1. System Overview

A provider gate (`relay unavailable <token> --for 632h`) stores the correct
`Until`, for example `2026-10-19T22:16`. But every surface prints it with
`Format("15:04")`, so `relay candidates`, `relay policy`, `relay status` and
`relay doctor` all say `rate-limited until 22:16`, which reads as later
today. The same goes for a gate's "since" time once it is more than a day
old.

Fix: one formatter that prints the clock time alone when the time falls on
today's local date, and adds the date otherwise. Every **gate** time goes
through it. Non-gate times (pid start, the ui clock, plan/report stamps)
are out of scope; leave them alone.

## 2. File Structure

```
internal/relay/ledger.go        MODIFY  gateClock var; GateTimeText; GateUntilText uses it; gatedNote's since
internal/relay/ledger_test.go   MODIFY  TestGateTimeText; TestGateUntilText pinned via gateClock
internal/relay/status.go        MODIFY  the gates block's since (~line 595)
internal/relay/headless.go      MODIFY  the rate-limit payload's "gated until" (~line 526)
cmd/relay/doctor.go             MODIFY  ledgerChecks: the "wait until" fix and the since (~lines 322, 332)
```

## 3. Data Structures & Type Definitions

`var gateClock = time.Now` (unexported, in `ledger.go`): the "now" that
gate-time formatting compares against. Tests override it and restore it
with `t.Cleanup`.

## 4. Interface Definitions & Component Contracts

### `func GateTimeText(t time.Time) string` (new, exported, `ledger.go`)

Pure except that it reads `gateClock()`. Both `t` and now are converted to
`Local()` first:
- the same local calendar date as now (year, month and day equal):
  `t.Format("15:04")`, e.g. `22:16`;
- a different date in the same year: `t.Format("Jan 2 15:04")`, e.g.
  `Oct 19 22:16`;
- a different year: `t.Format("2006-01-02 15:04")`.

A zero `t` returns "", but callers never pass one (`GateUntilText` handles
zero itself).

### `GateUntilText(until time.Time) string` (signature unchanged)

- A zero value still gives `"until cleared"`.
- Otherwise it returns `"until " + GateTimeText(until)`.

### Call sites switched from `.Local().Format("15:04")` to `GateTimeText(...)`

Only these, which are all gate times:
- `ledger.go` `gatedNote`: `g.Since`;
- `status.go` ~595: `g.Since`;
- `headless.go` ~526: `m.Until`;
- `cmd/relay/doctor.go` ~322: `"wait until " + relay.GateTimeText(g.Until)`;
- `cmd/relay/doctor.go` ~332: `g.Since`.

`grep -n 'Format("15:04")' internal/relay/ledger.go internal/relay/status.go internal/relay/headless.go cmd/relay/doctor.go`
must show only the non-gate uses afterwards (the status pid line ~682).

## 5. High-Level Pseudocode

```
GateTimeText(t):
    lt, ln = t.Local(), gateClock().Local()
    if same Y/M/D: return lt.Format("15:04")
    if same year:  return lt.Format("Jan 2 15:04")
    return lt.Format("2006-01-02 15:04")
```

## 6. Error Handling Strategy

None. Formatting only.

## 7. Ordered Implementation Steps

### Step 1: formatter and call sites

- Deliverable: §3/§4.
- Tests in `ledger_test.go`:
  - `TestGateTimeText`: set `gateClock` to a fixed
    `2026-09-23 14:00` in `time.Local` (restore with `t.Cleanup`). Table:
    - same day 22:16 → `22:16`;
    - `2026-10-19 22:16` → `Oct 19 22:16`;
    - `2026-09-24 00:05` → `Sep 24 00:05` (the next day counts as a
      different date);
    - `2027-01-02 03:04` → `2027-01-02 03:04`.
  - `TestGateUntilText`: pin `gateClock` to the same day as its existing
    `fixed` time, so the existing expected string holds. Add one case for
    the 26-days-out gate that expects `until Oct 19 22:16`.
- Other tests that build their expectation by calling `GateUntilText`
  (`policy_view_test.go`, `candidate_test.go`) stay self-consistent and
  must pass unmodified. If any other test hard-codes a `"until HH:MM"`
  string for a gate that is not today and now fails, STOP and report it
  instead of editing it. (`send_test.go`'s `until 00:26` is a literal
  GateNote fixture, not formatter output, and must stay untouched.)
- Verify: `go test ./internal/relay/ ./cmd/relay/ -count=1` passes.
  Mutation (report it): make `GateTimeText` always return
  `Format("15:04")`. `TestGateTimeText` fails. Restore.

### Step 2: full check

- `make check` passes, and `git diff --stat` shows exactly the five files
  in §2.
- Report: each verify output, the mutation result, and the diff stat.
  Commit on the binding's branch with the message
  `fix(ledger): a gate time not today shows its date`.
