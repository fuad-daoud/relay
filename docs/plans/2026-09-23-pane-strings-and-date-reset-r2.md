# Plan (round 2): strings that still describe panes (#352), and codex's "try again at <date>" reset (#311, text half)

This is round 2. Round 1 halted, correctly, before editing anything. Its
finding: `sqlite3` no longer feeds **usage** at all. `opencodeDB`'s only
production caller was the pane path (`readPane`, deleted in #327). It still
matters for **delivery**, though: `newDeliverers` (`cmd/relay/main.go` ~455)
builds the opencode deliverer only when `sqlite3` is on PATH, and that
deliverer confirms a push through it. Decision: the doctor row stays and is
reworded to say what `sqlite3` gates now. The unused `opencodeDB` reader is
**not** deleted in this round; that is out of scope.

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. In
particular, halt and report if:

- `newDeliverers` does **not** gate the opencode deliverer on `sqlite3`, or
  an opencode planner with no deliverer does **not** fall to route `pull`
  (`DeliverPending`, `internal/relay/deliver.go`). The new doctor text in §3
  would then be false.
- A codex limit line with this date form does **not** match codex's limit
  patterns (the harness defaults for the `codex` kind in `limitPatterns`).
  Then `parseReset` is never reached for it, and parsing the date alone fixes
  nothing.

This round has two independent parts. They are batched only because both are
small. Do them in order, with one commit per part.

## 1. System Overview

**Part A (#352).** #303 removed panes, but six user-facing strings and one
comment still describe them. One is actively misleading: a debug log says an
opencode push is "falling back to pane" when the payload actually stays
pending on route `pull`. This part rewords each one and updates the tests
that pin them. It changes only text; no behaviour changes.

**Part B (#311, text half only).** When a builder hits a usage limit,
`parseReset` (`internal/relay/limit.go`) reads the reset time from the
matched line. Today it understands two forms: a duration ("resets in 2h48m",
"try again in 5 min") and a clock time ("resets at 23:30"). Codex's weekly
limit reads:

```
You've hit your usage limit. … or try again at Oct 19th, 2026 7:14 PM.
```

Neither form matches that line, so the gate falls back to the 1h default.
Codex is then retried and fails, costing a builder switch every hour until the
real reset. This part adds a third form, an **absolute date**. It also gives
that form a longer window: **31 days** when the line names a year, because a
full date is hard to misread. The duration and clock forms keep the existing
7-day bound. The structured-event half of #311 (opencode's `retryAfterMs`)
stays blocked on a captured fixture and is out of scope.

## 2. File Structure

```
internal/relay/text.go             MODIFY  UnbindText's two lines (Part A)
internal/relay/text_test.go        MODIFY  the 8 pinned expectations (Part A)
internal/relay/deliver_opencode.go MODIFY  slog message ~line 101 (Part A)
internal/relay/consult.go          MODIFY  silent-consult payload ~line 184 (Part A)
internal/relay/consult_test.go     MODIFY  ~191-196 (Part A)
internal/doctor/doctor.go          MODIFY  sqlite3 check Detail strings ~587, ~592 (Part A)
internal/doctor/doctor_test.go     MODIFY  ~766 (Part A)
cmd/relay/main.go                  MODIFY  cmdPause comment ~1995 and usage text ~2009 (Part A)
internal/relay/send.go             MODIFY  DryRun Mode/Where comments ~416-418 (Part A, comments only)
internal/store/log.go              MODIFY  KindPause comment ~54 (Part A, comment only)
internal/relay/limit.go            MODIFY  dateRe, month table, parseReset date branch, window bounds (Part B)
internal/relay/limit_test.go       MODIFY  TestParseReset rows + a matchLimit test (Part B)
```

Another builder is working on `internal/relay/send.go` in parallel, on the
send path, not these comments. Touch only the two comment lines named here.

## 3. Part A: the exact strings

| Where | Old | New |
|---|---|---|
| `text.go` UnbindText, archived | `archived %s to %s (panes left untouched)` | `archived %s to %s` |
| `text.go` UnbindText, unbound | `unbound %s (panes left untouched)` | `unbound %s` |
| `deliver_opencode.go` ~101, slog message | `"opencode push falling back to pane"` | `"opencode push not confirmed; payload stays pending for relay pull"` (keep the `session` and `reason` attrs) |
| `consult.go` ~184, payload | `Consult %s (%s) wrote no findings: %s. No pane was spawned.` | `Consult %s (%s) wrote no findings: %s.` |
| `consult.go` ~181-183, the comment above it | "A consult never has a pane since #303, so there is never one to name." | drop that sentence and keep the rest |
| `doctor.go` ~587, sqlite3 missing | `not on PATH; opencode pane rounds record usage as unknown` | `not on PATH; relay cannot confirm a push to an opencode planner, so its reports wait for relay pull` (Severity stays warn; Fix unchanged) |
| `doctor.go` ~592, sqlite3 present | `on PATH; opencode pane usage readable` | `on PATH; pushes to an opencode planner can be confirmed` |
| `doctor.go` ~172-174, `WithUsage` doc comment | "sqlite3 on PATH when an opencode candidate is configured (its pane rounds read opencode.db through it)" | "sqlite3 on PATH when an opencode candidate is configured (relay confirms a push to an opencode planner through it)". Do not rename `WithUsage`, `usageOpencode` or the check's `Name` |
| `main.go` ~455-457, `newDeliverers` doc comment | "…so relay falls back to today's pane injection for every opencode planner." | "…so an opencode planner's reports stay pending for relay pull." |
| `main.go` ~1995 comment and ~2009 usage | `releases a binding's worktree and pane between rounds` | `releases a binding's worktree between rounds` |
| `send.go` ~416 comment | `// "pane" \| "headless" \| "remote"` | `// "headless" \| "remote"` (first check `dryRunMode` ~460 can no longer return `"pane"`; if it can, halt) |
| `send.go` ~418 comment | `// pane: "pane w2:p4 (working)"; headless: …` | drop the `pane: …;` clause and keep the headless and remote ones |
| `store/log.go` ~54 comment | `worktree and pane released` | `worktree released` |

The consult payload must still end with a period. Take care not to produce a
double period when `note` already ends with one. If `note` can end with `.`
(read how it is built), trim one trailing `.` from `note` before formatting.

**Do not touch** the strings that mention panes on purpose, to explain the
removal to someone upgrading: `consult.go` ~76 ("pane consults were removed
(#303)"), `reconcile.go` ~120 and ~125, `store.Endpoint.PaneID` and
`ModePane`.

**Tests:**
- `text_test.go`: update all eight expectations to the new strings exactly.
  Do not loosen them to `Contains`.
- `consult_test.go` ~191-196: the test currently strips and checks for the
  "No pane was spawned." suffix. Change it to assert the payload ends with
  `wrote no findings: <note>.` for the fixture's note, and contains no
  `pane`.
- `doctor_test.go` ~766: assert the new Detail, which contains
  `relay pull` and does not contain `pane`. Update the error message in the
  `t.Errorf` to match. If a sibling subtest asserts the "present" Detail,
  update it to the new text too.

*Verify Part A:* `make check` passes. Then this returns only the lines this
plan says to keep (`consult.go` ~76, `reconcile.go` ~120/~125, and
`PaneID`/`ModePane` declarations and their users):
`grep -rn -i "pane" internal cmd --include='*.go' | grep -v _test.go`.
List any other hit in the report with a one-line reason it stays. Do not
reword it unless it is in the table.

## 4. Part B: data and contracts (`internal/relay/limit.go`)

### New package-level values

- `dateRe *regexp.Regexp`: the absolute-date form. It must match, case
  insensitively, on the matched line:
  - a lead-in: `try again at`, `try again on`, `resets at`, `resets on`,
    `reset at`, `reset on` (allow `~` after it, like the others);
  - a month name: an English full or 3-letter abbreviated name, with an
    optional trailing `.` (`Oct`, `Oct.`, `October`, `Sept` also accepted);
  - a day, 1–31, with an optional ordinal suffix (`st`/`nd`/`rd`/`th`);
  - an optional `,` and then an optional 4-digit **year**;
  - an optional time: `,`/`at`/whitespace, then `H:MM`, then an optional
    `am`/`pm`.
  - Capture groups: month, day, year (may be empty), hour (may be empty),
    minute (may be empty), am/pm (may be empty).
- `monthByName map[string]time.Month`: the lower-cased 3-letter prefix maps
  to the month (`"jan"` → January … `"dec"` → December). Look up the first 3
  letters of the captured month, so `sept`/`september` → `sep`.
- `limitWindowShort = 7 * 24 * time.Hour` and
  `limitWindowDated = 31 * 24 * time.Hour`: named constants that replace the
  inline `7*24*time.Hour`.

### Changed functions

`inLimitWindow(t, now time.Time) bool` becomes
`inLimitWindow(t, now time.Time, max time.Duration) bool`. It is true iff
`t.After(now) && !t.After(now.Add(max))`. Every existing caller passes
`limitWindowShort`.

`parseReset(line string, now time.Time) (time.Time, bool)`. The signature is
unchanged, and so is the return: UTC, and ok=false on anything unreadable.
The new order is:

1. duration (unchanged)
2. **date (new)**
3. clock (unchanged)

The date comes before the clock so that a line such as
`resets at Oct 19 7:14 PM` is read as a date. Check that `clockRe` cannot
match such a line first: it needs a digit right after `resets (at )?~?`, and a
month name is not a digit. If it can, keep the date first anyway; that is the
point of the ordering.

Update `parseReset`'s doc comment to list the three forms and both windows.

## 5. Part B pseudocode: the date branch of `parseReset`

```
m = dateRe.FindStringSubmatch(line); if m == nil → fall through to the clock form
month = monthByName[lower(first 3 letters of m.month)]; missing → return false
day   = atoi(m.day); outside 1..31 → return false
hour, minute = 0, 0
if m.hour != "":
    hour = atoi(m.hour); minute = atoi(m.minute)
    apply am/pm exactly as the clock branch does (reuse its rules: 12am → 0,
      12pm → 12, 1-11pm → +12, am/pm with hour outside 1..12 → false,
      no am/pm with hour outside 0..23 → false); minute outside 0..59 → false
loc = now.Location()                          // codex prints the machine's local time
if m.year != "":
    t = time.Date(atoi(m.year), month, day, hour, minute, 0, 0, loc)
    if t.Day() != day → return false          // Feb 30 etc.: time.Date normalised it
    window = limitWindowDated
else:
    t = time.Date(now.Year(), month, day, hour, minute, 0, 0, loc)
    if t.Day() != day → return false
    if !t.After(now): t = same month/day/time in now.Year()+1 (re-check t.Day())
    window = limitWindowShort                 // no year: the vaguer form keeps 7 days
if !inLimitWindow(t, now, window) → return false
return t.UTC(), true
```

A date without a time means 00:00 local on that day. That is the earliest
reading, so it never over-gates.

## 6. Error Handling

`parseReset` never errors. Every unreadable or out-of-window case returns
`ok=false`, and `matchLimit` then uses the fallback exactly as today. A
misread full date can gate a provider for at most 31 days, and
`relay available <provider>` lifts it at any time. That is the accepted
trade-off. No new log lines.

## 7. Ordered Implementation Steps

Run `make check` after each step. All tests are in `internal/relay` and
`internal/doctor`. **Add no test in `cmd/relay`**: CI runners have no harness
binary and no network.

1. **Part A strings and their tests** (§3). Commit:
   `fix: six strings no longer describe panes after #303 (#352)`.
   *Verify:* `make check` passes, and the grep in §3 is accounted for in the
   report.

2. **Part B: `inLimitWindow` takes a window.** Add the two constants, change
   the signature and pass `limitWindowShort` at both existing call sites.
   *Verify:* `make check` passes with no test change. The behaviour is the
   same.

3. **Part B: the date form.** Add `dateRe`, `monthByName`, the date branch and
   the doc comment. Then add `TestParseReset` rows. Use the test's existing
   `now` (or a fixed one in a fixed location such as `time.UTC`) and state it
   in the row:
   - `try again at Oct 19th, 2026 7:14 PM` with now = 2026-09-23 12:00 →
     2026-10-19 19:14 (in now's location, returned as UTC), ok. This is the
     real codex line; use the full sentence from §1 as the input.
   - `resets on October 3, 2026` → 2026-10-03 00:00, ok.
   - `try again at Sept 30th 9am` (no year, 7 days out) → ok.
   - `try again at Nov 1st 9am` (no year, more than 7 days out) → **not ok**:
     the short window applies without a year.
   - `try again at Oct 19th, 2027 7:14 PM` (more than 31 days) → not ok.
   - `try again at Sep 1st, 2026` (in the past) → not ok.
   - `try again at Feb 30th, 2027` → not ok (invalid date).
   - `try again at Oct 19th, 2026 13:14 PM` → not ok (pm with hour 13).
   - The existing duration and clock rows are unchanged.

   Also add one `matchLimit` test. Feed the full codex sentence through
   `matchLimit` with the **codex kind's default patterns** (build them the
   way the existing limit tests do). Assert `Parsed == true` and
   `Until == 2026-10-19 19:14` in the chosen location. That test covers the
   second halt condition.

   *Mutation:* change `limitWindowDated` to `limitWindowShort` in the
   year-present branch. The Oct 19 row and the `matchLimit` test must fail.
   Restore it. Commit:
   `fix(limit): codex's "try again at <date>" sets the gate, up to 31 days when the date names a year (#311)`.

## Report

List the files touched per part and the §3 grep hits you kept, with reasons.
Give the `now`/location the new tests use and the mutation result: what you
changed and which tests failed. State both halt conditions as checked, with
the evidence.
