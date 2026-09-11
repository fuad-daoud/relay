# History T2: `relay policy` peak column and history block; docs (#61 step 7)

**Design spec:** `docs/specs/2026-09-11-availability-history-design.md` -- read §1, §4.2, §5
**Issue:** #61 (step 7, observation half)
**Depends on:** History T1 (`internal/history`, `Runtime.HistoryPath`) -- in this tree (`ls internal/history` must show the package).

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **History never changes a pick.** `resolveCandidate` is untouched; the
  `<- would pick` marker comes from it exactly as before. The peak
  column and history block are display only.
- **Existing `FormatPolicy` expectations do not change** when history is
  empty: the tests in `policy_view_test.go` gain the three new arguments
  and keep their `want` strings byte for byte.
- `time.Local` appears only in `cmdPolicy`; `FormatPolicy` and
  `HourCounts` take the location as a parameter so tests are
  timezone-independent.
- No test may execute a `cmd/relay` subcommand that reaches herdr.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the view and the docs

**Files:**
- Modify: `internal/relay/policy_view.go`, `internal/relay/policy_view_test.go`
- Modify: `cmd/relay/main.go` (`cmdPolicy`)
- Modify: `README.md`, `docs/design.md`

**Interfaces consumed:** `history.History`, `history.Load`,
`history.HourCounts(h, provider, kind, loc) [24]int`, `History.Prune`,
`ledger.RateLimited`, `ledger.SpawnFailed`, `GateKindText`,
`candidate.ParseRef`, `rt.HistoryPath`, `rt.Now`.

**Interfaces produced** (spec §4.2):

```go
func FormatPolicy(set *candidate.Set, pol policy.Policy, gates []ledger.Gate, hist history.History, now time.Time, loc *time.Location) string
func peakText(hist history.History, provider string, now time.Time, loc *time.Location) string     // "" when no limits around now
func formatHistory(hist history.History, loc *time.Location) string                                // "" when hist has no events
```

- [ ] **Step 1: the signature and the unchanged tests** 

  Change `FormatPolicy` to the new signature (the body does not use the
  new arguments yet). Update every caller: the five existing tests pass
  `history.History{}, baseTime, time.UTC`; `cmdPolicy` passes
  `loadHistory(rt), rt.Now(), time.Local` where `loadHistory` is a small
  helper in `cmd/relay/main.go`:

  ```go
  // loadHistory reads the availability history for display, treating an
  // unreadable file as empty after one stderr line -- the same rule Gates
  // applies to the ledger.
  func loadHistory(rt relay.Runtime) history.History
  ```

  (`history.Load(rt.HistoryPath)`, then `.Prune(rt.Now())`; on error
  `fmt.Fprintf(os.Stderr, "relay: could not read history: %v\n", err)`
  and return `history.History{}`.)

  Run: `go build ./... && go test ./internal/relay/ -run FormatPolicy`.
  Expected: green, outputs unchanged.

- [ ] **Step 2: failing tests for the column and the block** (spec §4.2)

  In `policy_view_test.go`:

  - `TestFormatPolicyPeakColumn`: `testCandidatesJSON` (all three on
    provider `test`), `orderOf("builder", testAgyRef, testClaudeRef)`,
    no gates, `now := time.Date(2026, 9, 11, 21, 15, 0, 0, time.UTC)`,
    history with `RateLimited` events for provider `test` at 20:10, 21:40,
    22:05 the same day, one at 21:00 31 days earlier (not counted after
    `Prune` -- pass the pruned history, as `cmdPolicy` does), one
    `SpawnFailed` at 21:30 (not counted: peak counts rate limits only).
    Want the builder block:
    ```
    builder  (order set in ~/.config/relay/policy.json)
      1  agy/test/m       order     limited 3x around 21:00 (30d)  <- would pick
      2  claude/test/m    order     limited 3x around 21:00 (30d)
      3  opencode/test/m  unlisted  limited 3x around 21:00 (30d)
    ```
    (peak text first in the tail; when a row is also picked, two spaces
    then the marker, as the existing gate+marker rule says). Then the
    reviewer/researcher blocks as in the other tests, then `warnings`
    as in `TestFormatPolicyOrderWithGatedFirst`, then:
    ```

    history (30d, local hours)
                 00 01 02 03 04 05 06 07 08 09 10 11 12 13 14 15 16 17 18 19 20 21 22 23
      test       rate-limited   .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  1  1  1  .
      test       spawn failed   .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  1  .  .
    ```
    Build the `want` with a helper that renders the header and the rows
    from a `[24]int` so the test states counts, not spacing -- but the
    helper's format must be the one in step 3, so write it there first
    and copy it.
  - `TestFormatPolicyPeakWrapsMidnight`: one `RateLimited` event at 23:30,
    one at 00:20 next day, `now` 23:50 → `limited 2x around 23:00 (30d)`.
  - `TestFormatPolicyNoHistoryNoBlock`: empty history → output contains
    neither `limited` nor `history (`.
  - `TestFormatPolicyGateAndPeakOrder`: a gate on `testAgyRef` plus one
    limit event at `now` → the agy row tail is
    `limited 1x around 21:00 (30d); spawn failed <untilText>`.

  Run: `go test ./internal/relay/ -run FormatPolicy`. Expected: FAIL.

- [ ] **Step 3: `peakText`, `formatHistory`, wiring** (spec §4.2)

  `peakText`: `c := history.HourCounts(hist, provider, ledger.RateLimited,
  loc)`; `h := now.In(loc).Hour()`; `n := c[(h+23)%24] + c[h] +
  c[(h+1)%24]`; `""` when `n == 0`, else `fmt.Sprintf("limited %dx around
  %02d:00 (30d)", n, h)`.

  `formatHistory`: collect `(provider, kind)` pairs with any event;
  providers sorted; within a provider `RateLimited` then `SpawnFailed`;
  return `""` when none. Header line `"history (30d, local hours)"`, then
  the hour ruler: 13 spaces, then `"00"`…`"23"` joined by single spaces
  (the ruler's cells line up under the counts). Each row:
  `fmt.Sprintf("  %-10s %-13s", provider, GateKindText(kind))` then for
  each hour ` %2s` where the cell is `strconv.Itoa(n)` or `"."`. Check
  the alignment by eye once against the spec's block and adjust the
  ruler indent so `00` sits over the first cell; then copy the exact
  format into the test helper.

  In `FormatPolicy`: the row tail is built as parts -- `peakText` first
  when non-empty, then each gate text -- joined by `; `, then the marker
  appended as today. After the warnings block and before the no-policy
  footer: `if s := formatHistory(hist, loc); s != "" { sb.WriteString("\n" + s) }`.

  Run: `go test ./internal/relay/`. Expected: green. Adjust only the
  formatter, or the test's format helper to match the formatter -- not
  the counts the test states.

- [ ] **Step 4: docs**

  README, under `### Availability`, after the "Mid-round switching"
  subsection:

  ````
  #### History

  Every gate relay records -- a limit you report, a spawn failure it hit
  -- is also kept for 30 days in `~/.local/state/relay/history.json`, by
  provider and local hour. `relay policy` shows it twice: a `limited 3x
  around 21:00 (30d)` note on a candidate whose provider was limited
  within an hour of now, and a `history` block with a 24-hour row per
  provider. It changes nothing about which candidate is picked; it is the
  cue to write a different order, or to `relay unavailable` a provider
  before it bites.
  ````

  In `### Policy`, add the peak column to the `relay policy` example (one
  row). `docs/design.md`: wherever `ledger.json` is listed among state
  files, add `history.json` with one clause.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add -A internal cmd README.md docs/design.md
  git commit -m "feat(relay): relay policy shows a provider's limit history by hour (#61 step 7)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the test
functions added, and the full `FormatPolicy` output for
`TestFormatPolicyPeakColumn` pasted from the test's `want`.
