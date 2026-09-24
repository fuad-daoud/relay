# Plan: status line redesign (round clock, round tokens, no money, server)

Spec: `docs/specs/2026-09-24-statusline-redesign-design.md` (in this tree). Read it
first; it is short. This plan is self-contained: every change names its file,
function and line range as of commit ff04f5d1.

**If a step is impossible as written or contradicts the code, stop and report.
Do not bend a test to fit.** In particular: if a report entry's `Round` is not
the same number as the plan entry of the round it closes (checked in step 2),
halt and report rather than invent a matching rule.

## 1. System overview

`relevo status --line` prints one row per binding for Claude Code's status line
(`RenderStatusLine`, `internal/relevo/statusline.go`). Today the right-hand
clock is "time since the last relayed message", so it keeps ticking after a
report arrives. Every open binding shows `ACTIVE`, the row shows dollar figures,
and closed rows show tokens summed over every round. This change:
(a) adds four log-derived fields to `BindingStatus` (round start, round end,
that round's usage, remote server), (b) re-renders the row from them: a round
clock that freezes at the report, this round's tokens only, no money, no
`live` word, no `ACTIVE`, and `harness@server` for remote builders. Nothing
else that reads `BindingStatus` changes.

## 2. File structure

```
internal/relevo/status.go          MODIFY  BindingStatus gains 4 fields; buildRow fills them; new pure func roundFacts
internal/relevo/statusline.go      MODIFY  RenderStatusLine re-rendered; age() replaced by roundClock(); tokens cell; ansiActive removed
internal/relevo/statusline_test.go MODIFY  port 9 tests + fixture, add 3 tests
internal/relevo/status_test.go     MODIFY  add TestRoundFacts (pure) — or a new file roundfacts_test.go, your choice
README.md                          MODIFY  "Status line" section, one sentence (line ~1269)
docs/specs/2026-09-24-statusline-redesign-design.md  ALREADY PRESENT, commit it
docs/plans/2026-09-24-statusline-redesign.md         ALREADY PRESENT (this file), commit it
```

No other file changes. In particular: `internal/usage/*`, `internal/ui/*` and
`cmd/relevo/*` are untouched. `usage.LiveShort` and `usage.MoneyShort` stay,
because `internal/ui/rail.go:126,133` uses them.

## 3. Data structures

### `BindingStatus` (internal/relevo/status.go, struct at lines 72–229)

Insert directly after the `LiveUsage` field (line 147), with a doc comment
each:

| Field | Type | JSON tag | Contract |
|---|---|---|---|
| `RoundStart` | `time.Time` | `json:"round_start,omitzero"` | TS of the earliest `KindPlan` + `DirToBuilder` entry whose `Round` equals the round of the newest such entry. Zero when the log has no such entry. |
| `RoundEnd` | `time.Time` | `json:"round_end,omitzero"` | TS of the newest `KindReport` + `DirToPlanner` entry with that same `Round` and `TS >= RoundStart`. Zero when none (the round is open). |
| `RoundUsage` | `*usage.Usage` | `json:"round_usage,omitempty"` | Copy of the `Usage` on the entry that set `RoundEnd`. Nil when `RoundEnd` is zero or that entry's `Usage` is nil. Never taken from any other entry. |
| `Server` | `string` | `json:"server,omitempty"` | `b.Builder.Server` when `b.Builder.Remote()`; "" otherwise. |

(`omitzero` is supported: go.mod is go 1.25 and `internal/history/history.go:41`
uses it already.)

## 4. Interfaces / function contracts

### `roundFacts` (new, internal/relevo/status.go, place after `isPayloadKind`, ~line 555)

```
func roundFacts(entries []store.LogEntry) (start, end time.Time, u *usage.Usage)
```
- Pure; no I/O. `entries` is in log order (oldest first), as `rt.Store.ReadLog` returns.
- Postconditions: exactly the three contracts in §3 for RoundStart/RoundEnd/RoundUsage.
- `KindFindings`, `KindQuestion`, `KindAnswer`, `KindDiff`, drift, pick,
  switch and exit entries never affect the result.
- Returned `u` is a fresh copy (not a pointer into `entries`).

### `buildRow` (internal/relevo/status.go)

- Remote branch at lines 378–386 (`} else if b.Builder.Remote() {`): add
  `row.Server = b.Builder.Server` as its first statement.
- After the `LastClose` loop (ends ~line 458) and before the usage loop
  (`var usages []usage.Usage`, ~line 459): set
  `row.RoundStart, row.RoundEnd, row.RoundUsage = roundFacts(entries)`.
- Nothing else in `buildRow` changes. `LastUsage`, `Spend` and `LiveUsage`
  keep their current meaning.

### `roundClock` (new, internal/relevo/statusline.go, replaces `age` at lines ~150–155)

```
func roundClock(b BindingStatus, now time.Time) string
```
- `b.RoundStart` zero → `"--"`.
- `b.RoundEnd` non-zero → `AgeText(b.RoundEnd.Sub(b.RoundStart))`. `now` is ignored, so the clock is frozen.
- else → `AgeText(now.Sub(b.RoundStart))`.

### `roundTokens` (new, internal/relevo/statusline.go)

```
func roundTokens(b BindingStatus) string
```
- Round open (`b.RoundEnd` zero): if `b.LiveUsage != nil && b.LiveUsage.Samples > 0 && Tokens.Total() > 0`
  → `usage.ShortTokens(total) + " tok"`, else "".
- Round closed: if `b.RoundUsage != nil && Tokens.Total() > 0` → same format, else "".
- Never reads `Spend` or `LastUsage`. Never produces a `$`, `~$`, `unknown` or `live`.

### `RenderStatusLine` (internal/relevo/statusline.go, lines 49–107)

Signature unchanged. New row assembly (pseudocode):

```
harness := harnessSegment(b.BuilderCandidate)       // as today, only when candidate != ""
if b.Server != "" { harness += "@" + b.Server }
mid := "r<Round>" [+ " · " + harness] + " · " + waiting(b)
if t := roundTokens(b); t != "" { mid += " · " + t }

clock := roundClock(b, now)
if b.Display == "ACTIVE" or b.Display == "":
    rawRight = clock;  colouredRight = clock
else:
    word := b.Display, coloured ansiNeedsYou…ansiReset when "NEEDS YOU", plain otherwise
    rawRight = clock + " · " + b.Display
    colouredRight = clock + " · " + word
// dot, width math, padding, truncation, the midW < 8 unpadded branch: unchanged
```

## 5. Deletions (closed list)

Only these go. Everything not listed survives.

1. `statusline.go` lines 76–81: the `switch` that appends `usage.LiveShort(...)` or `usage.MoneyShort(...)` to `mid`. `roundTokens` replaces it.
2. `statusline.go` line 24: `ansiActive = "\x1b[38;5;42m"`, and lines 85–86: the `case "ACTIVE":` arm that colours the word. The word ACTIVE is no longer printed.
3. `statusline.go` `func age(b BindingStatus, now time.Time) string` (~lines 150–155). `roundClock` replaces it; delete `age` if nothing else references it (grep first; if something outside statusline.go does, halt and report).

Tests: none are deleted. The tests that asserted deleted behaviour are ported (§7, step 4).

## 6. Error handling

There are no new error paths. `roundFacts` is total: any log, including an
empty one, gives a defined result. `buildRow` already returns the
`ReadLog` error, and the new code adds no failure mode. The status line keeps
its rule that it prints nothing on error.

## 7. Working efficiently

Each tool call costs a full round trip, so:
- Batch independent reads as parallel tool calls in one step. The only files to
  read are the four in §2 (and only the line ranges named).
- Don't re-search for what this plan already located. The line numbers are from ff04f5d1.
- Make all of a file's changes in one edit, or a few, not line by line.
- Focused loop: `go test ./internal/relevo -run 'StatusLine|RoundFacts|RoundClock|AgeText|Waiting|PlannerStatus|Status' -count=1`.
  Fix every error it reports before the next run.
- Full check once, at the end: `make check` (gofmt over tracked files, go vet,
  go mod tidy check, full tests). Do not run `make e2e`.
- CI has no harness binary and no network. Every new test here is a pure
  function test in `internal/relevo`. Add no test in `cmd/relevo`.

## 8. Ordered steps

### Step 1: `BindingStatus` fields and `roundFacts`
- File: `internal/relevo/status.go`. Add the four fields (§3) and `roundFacts` (§4).
- Verify: `go build ./...` passes.

### Step 2: fill the fields in `buildRow`
- Depends on step 1. Make the two insertions in §4 "buildRow".
- First confirm the round-number premise: `internal/relevo/send.go` ~line 414
  writes the plan entry with `Round: b.Round`, and `internal/relevo/reconcile.go`
  ~line 499 writes the report entry with `Round: b.Round`, before
  `b.Round++` at reconcile.go:530. If the report's round is not the plan's
  round, halt.
- Verify: `go build ./...`.

### Step 3: re-render the row
- Depends on step 2. File `internal/relevo/statusline.go`: make the deletions
  §5.1–5.3, add `roundClock` and `roundTokens`, and rewrite the mid/right
  assembly in `RenderStatusLine` as the §4 pseudocode shows. Update the
  function's doc comment to cite the new spec. Update the package doc comment
  on line 1 so it names both specs.
- Verify: `go vet ./internal/relevo`.

### Step 4: port the existing tests (`internal/relevo/statusline_test.go`)
Each port changes the assertion; no test is deleted. `baseTime` is the file's
`now`.
- `statuslineFixture` (~line 384): add `RoundStart` to each binding.
  `api`: `now-12m`, open. `client`: `now-4m`, open. `docs`: `RoundStart now-5m`,
  `RoundEnd now-5m+23s`, so its clock is a frozen `23s`.
- `TestRenderStatusLineIgnoresBookkeepingLast` (190): add `RoundStart: now-12m`.
  Change the suffix assertion to `" 12m"` and assert the line does not contain `ACTIVE`.
- `TestRenderStatusLineLiveSegment` (223): add `RoundStart`. Expect
  `"plan sent · 41k tok"`, assert no `$` and no `live`, and the suffix is `" 12m"`.
- `TestRenderStatusLineSpendSegment` (244): rename it to
  `TestRenderStatusLineClosedRoundTokens`. Build a closed row: `RoundStart`/`RoundEnd` set,
  `RoundUsage` with 2.1M tokens, and `Spend` with a different total (for example 9M).
  Expect `"· 2.1M tok"`, and assert no `9.0M`/`9M` and no `$`.
- `TestRenderStatusLineLiveWinsOverSpend` (259): add `RoundStart`, open round.
  Expect `41k tok`, assert no `2.1M` and no `$`.
- `TestRenderStatusLineNarrowDropsUsageFirst` (282): add `RoundStart`. Suffix is
  `" 12m"`, and the `tok` assertion is unchanged.
- `TestRenderStatusLineAt80` (422): line 0 suffix is `" 12m"` and it does not contain `ACTIVE`.
  Lines 1 and 2 are unchanged. Width assertions are unchanged.
- `TestRenderStatusLineTruncatesAt40` (459): the suffixes become `" 12m"`,
  `" 4m · NEEDS YOU"`, `" 23s · PAUSED"`.
- `TestRenderStatusLineUnpaddedWhenTooNarrow` (493): want
  `"○ api  r3 · agy · plan sent · 12m"`.
- `TestRenderStatusLineColours` (506): replace the ACTIVE-colour assertion with
  one that line 0 contains no `\x1b[38;5;42m` and no `ACTIVE`. Keep the rest unchanged.
- Verify: the focused test command passes.

### Step 5: new tests
- `TestRoundFacts` (table-driven, pure; in `status_test.go` or a new
  `roundfacts_test.go`). Cases:
  1. empty log → zero, zero, nil.
  2. plan r1 → start = its TS, end zero, nil.
  3. plan r1, report r1 with usage → end = report TS, usage equals that usage.
  4. plan r1, report r1 (usage U1), plan r2 → start = r2 plan TS, end zero, nil (r1 must not leak).
  5. plan r2, later plan r2 (nudge or switch), report r2 → start = the *first* r2 plan TS.
  6. plan r1, report r1 (U1), plan r2, report r2 with nil usage → end = r2 report TS, usage nil (no borrowing U1).
  7. plan r1, findings entry with usage, question and answer entries → end zero, usage nil.
- `TestRoundClock` (statusline_test.go): open → `now-start`, and advancing `now`
  advances it. Closed → `end-start`, and two different `now` values give the
  same string. Zero start → `"--"`.
- `TestRenderStatusLineRemoteServer`: `BuilderCandidate "opencode/cline-pass/glm-5.3-flash"`, `Server "contabo"` → the line contains
  `"r1 · opencode@contabo · plan sent"`. The same row with `Server ""` contains `"r1 · opencode · plan sent"`.
- Mutation check. Do this once and state the result in the report:
  - Change `roundFacts` to take the *latest* plan TS: case 5 must fail.
  - Make `roundClock` ignore `RoundEnd`: `TestRoundClock` must fail.
  - Revert both.
- Verify: the focused test command passes.

### Step 6: README
- `README.md` ~line 1269: "the right-hand `age · STATE` cell" becomes "the
  right-hand round clock (with a state word only when it is not ACTIVE)".
  Add one sentence after line 1256: "Each row shows the round's harness
  (`harness@server` for a remote builder), what it is waiting on, this round's
  tokens, and the round's length: ticking while it runs, frozen once the report is in."
- Verify: by reading it back.

### Step 7: full check and commit
- Run `make check`. It must pass.
- `git diff --stat` must touch only the files in §2.
- Make one commit on this branch with every change plus the spec and this plan:
  `feat(statusline): round clock freezes at the report; round tokens, no money, no ACTIVE; harness@server`.
- The report lists: every ported test and how its assertion changed, the new
  tests, the mutation-check results, and any deviation from this plan.
