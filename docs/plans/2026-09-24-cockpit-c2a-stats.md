# Cockpit C2a: `internal/stats` and a new `relevo history --stats`

Spec: `docs/specs/2026-09-24-cockpit-design.md` §5 (Stats) and §3.6 (history display
rules). This round builds the pure stats package and rewires `relevo history --stats`
onto it and the database. The `:stats` TUI view comes later (C2b, after B1). Line
numbers are from `ff04f5d`.

**One round. If a step is impossible as written or contradicts what you find, stop and
report. Do not improvise.**

CI has no harness and no network. `internal/stats` is pure. The `cmd/relevo` test for
`history --stats` runs the verb in-process against a seeded temp DB: it spawns nothing
and reaches no network. Isolation is the package's `TestMain` plus
`historyTabStatsRoot` (`cmd/relevo/history_tab_test.go:16`).

Do not touch `internal/ui/**`, `internal/candidate/**`, `internal/roles/**`,
`internal/policy/**` or `internal/config/**`: parallel rounds own them. **Do not touch**
`internal/relevo/history.go`'s `HistoryLine` / `FormatHistory` / `FormatGroups` either:
a parallel round (A1 round 2) edits them.

## 1. System overview

Today `relevo history --stats` (`cmd/relevo/history.go:355-382`) rebuilds its numbers
from log entries (`relevo.TabEntries` → `BuildStats` → `RenderStats`,
`internal/relevo/stats.go`). The largest lines of its output are `unknown 137` and
`unstructured 250`, and it answers none of the spec's four questions.

After this round:

- **`internal/stats`** computes one `Report` from database rows, the availability
  history and the active gates. The report has six parts:
  - totals;
  - a per-candidate scorecard;
  - spend per day, with this week against last week;
  - reliability (switches, gates, spawn failures, limits by local hour, active gates);
  - repos and features;
  - outcomes.
- **`relevo history --stats`** prints that report as text, or as `--json`. The window
  is `--since`, which now defaults to 30 days.
- **Two data fixes the numbers need:**
  - `db.RoundRow` carries `Switches`.
  - Ingest stops counting daemon relaunches as switches.

The spec's per-actor scorecard needs the actor on each round, which A4 adds. Until
then the scorecard covers every recorded round, and `Inputs.Keep` is the hook A4 will
use.

## 2. File structure

```
internal/stats/stats.go        NEW  Inputs, Report and its parts, Build
internal/stats/render.go       NEW  Render (text)
internal/stats/stats_test.go   NEW
internal/stats/render_test.go  NEW  + testdata/report.golden
internal/db/types.go           RoundRow + Switches (after DurationMS, :152-172)
internal/db/read.go            queryRounds SELECT + scanRoundRow read round.switches (:90-99, :133-260)
internal/db/read_test.go       one assertion that Switches round-trips
internal/ingest/outcome.go     switchesForRound skips relaunch entries (:58-67)
internal/ingest/outcome_test.go  one case
cmd/relevo/history.go          historyStats rewired (:355-382); usage text line for --stats (:18-23)
cmd/relevo/history_tab_test.go the old-output test replaced (see §3)
internal/relevo/stats.go       DELETED (see §3)
internal/relevo/stats_test.go  DELETED (see §3)
```

## 3. Deletions: the closed list

| # | deleted | replaced by |
|---|---|---|
| X1 | `internal/relevo/stats.go`: `BuildStats`, `RenderStats`, `StatsReport` and its types (`StatsCount`, `StatsSwitchFrom`, `StatsSwitches`, `StatsBlocked`), `StatsOutcomes`, `StatsGateResults`, `SwitchReasons`, and every unexported helper used only by them. **Before deleting**, grep every symbol in the file. Anything referenced outside `stats.go` and `stats_test.go` stays, moved to the file of its first user. The research pass found no outside users (`grep BuildStats\|RenderStats` shows only `cmd/relevo/history.go:373,380`). If you find one, keep that symbol and say so in the report. | `internal/stats` |
| X2 | `internal/relevo/stats_test.go`: the tests of X1 | `internal/stats` tests |
| X3 | `cmd/relevo/history_tab_test.go` `TestHistoryStatsOutputIsTheOldStatsOutput` | `TestHistoryStatsNewReport` (§7) |
| X4 | The old `--stats` text and JSON shapes, and `--stats` without `--since` meaning "all recorded rounds" | the §4.4 text, `stats.Report` JSON, and a 30-day default (`--since all` for everything) |

Everything else survives: `history --tab`, `history` rows, the `-q` / `--by` grids,
`TabEntries` (used by `--tab`), `loadHistory`, `formatHistory` in `policy_view.go`,
and ingest's other facts.

## 4. Data structures and contracts

### 4.1 `db.RoundRow.Switches`

- Add `Switches int` to `RoundRow` (`internal/db/types.go:152-172`), after
  `DurationMS`.
- `queryRounds` selects `round.switches` (read.go:90-99), and `scanRoundRow` reads it
  (read.go:133-260). The column is `NOT NULL` (`001_initial.sql`), so a plain int
  works.
- The `history --json` rows gain the key `"Switches"`. That is additive, since
  `RoundRow` is untagged.

### 4.2 `ingest.switchesForRound` (`internal/ingest/outcome.go:58-67`)

Count `KindSwitch` entries for round n **except** relaunch entries, whose `Note` starts
with `relaunched ` or `resumed session ` (`internal/relevo/headless.go:789-795`).
Explain it in a one-line comment. Only new ingests pick this up: live bindings
re-ingest every tick, and archived bindings keep their stored count.

### 4.3 `internal/stats` (pure; imports `db`, `history`, `ledger`, stdlib)

```
type Inputs struct {
    Rows       []db.RoundRow          // already windowed by the caller's query
    Landed     map[string]bool        // binding IDs whose binding reached DONE
    History    history.History        // availability events (rate limits, spawn failures, clears)
    Gates      []ledger.Gate          // active now
    TTFT       func(token string) (ms int64, ok bool)   // nil = no ttft column values
    IsPlan     func(token string) bool                  // nil = nothing is a plan
    Keep       func(db.RoundRow) bool                   // nil = every row; the scorecard's actor filter (A4)
    Since      time.Time              // zero = from the oldest row
    Until      time.Time              // the "now" of the report
    Loc        *time.Location
}

type Report struct {
    Since, Until time.Time
    Totals       Totals
    Scorecard    []ScoreRow
    Spend        Spend
    Reliability  Reliability
    Repos        []GroupRow   // key = RoundRow.Repo, "(none)" when nil
    Features     []GroupRow   // only rows with a Feature; empty when none
    Outcomes     Outcomes
}
type Totals struct {
    Rounds, Bindings, Candidates int
    CostUSD      float64   // known basis, non-plan
    PlanRounds   int
    UnknownCost  int       // rounds with basis unknown or nil cost (non-plan)
    Tokens       int64     // in+cache+write+out
    Halted       int
    MedianMS     int64     // closed rounds with DurationMS
}
type ScoreRow struct {
    Token            string
    Rounds, Closed   int
    Reported, Halted int
    DonePct, HaltPct float64   // of Closed; 0 when Closed == 0
    MedianMS         int64     // closed rounds with DurationMS; 0 when none
    TTFTMS           int64
    HasTTFT          bool
    Plan             bool
    CostPerRound     float64   // mean over rows with known cost; valid when HasCost
    HasCost          bool
    CommitsPerRound  float64   // mean over rows with Commits != nil; valid when HasCommits
    HasCommits       bool
    Few              bool      // Rounds < 5
}
type Spend struct {
    Days       []DayCost   // one per local day Since..Until inclusive, oldest first; zero days included
    ThisWeek   float64     // [Until-7d, Until)
    LastWeek   float64     // [Until-14d, Until-7d)
}
type DayCost struct {
    Day        string              // YYYY-MM-DD in Loc
    USD        float64
    ByProvider map[string]float64  // BuilderProvider, "(none)" when nil
}
type Reliability struct {
    Switches         int     // sum of Switches over rows
    RoundsSwitched   int     // rows with Switches > 0
    SwitchPct        float64 // RoundsSwitched / Rounds * 100
    RateLimits       int     // history RateLimited events in [Since, Until)
    SpawnFailures    int     // history SpawnFailed events in [Since, Until)
    ByHour           []HourRow   // one per provider with any rate limit in the window, sorted
    Active           []ledger.Gate
}
type HourRow struct{ Provider string; Counts [24]int }   // RateLimited events by local hour
type GroupRow struct{ Key string; Rounds, Halted, Landed int; CostUSD float64; RoundsPerLand float64 }
type Outcomes struct {
    ByRound  map[string]int   // db outcome values: reported, halted, exited, switched, done_no_report, open
    ByReport map[string]int   // done, halted, blocked, deferred, "no outcome" (= unstructured)
}

func Build(in Inputs) Report
```

**Rules**

- **Closed:** a row's outcome is one of `reported`, `halted`, `exited`, `switched` or
  `done_no_report` (`internal/db/types.go:192-199`). `open` is not closed.
- **Scorecard rows:**
  - Only rows with a non-nil `BuilderCandidate` and `Keep(row)` true. A nil candidate
    counts in Totals as `(unrecorded)` and never gets a row.
  - Sorted by Rounds desc, then Token asc.
  - `Plan = IsPlan(token)`. A plan row reports `HasCost` false.
- **Cost known:** `CostBasis != nil && *CostBasis != "unknown" && CostUSD != nil`, and
  the candidate is not a plan. This is the same rule as `histq`'s `costKnown`
  (`internal/histq/group.go:187-195`), plus the plan exclusion.
- **Median:** the true median (the mean of the two middles for an even count), as
  `histq`'s unexported `median` (`group.go:207-218`). Copy it into `internal/stats`.
- **Days:** use `Loc`. When `Since` is zero, start at the oldest row's day.
- **ByHour:** count `history` events of kind `RateLimited` with `At` in the window,
  bucketed by `At.In(Loc).Hour()`. Note that `history.HourCounts` takes no window;
  do not use it.
- **Repos / Features:**
  - Group by `Repo` and by `Feature`.
  - `Landed` counts distinct binding IDs in the group that are in `in.Landed`.
  - `RoundsPerLand` = rounds of landed bindings in the group / `Landed`, or 0 when
    `Landed == 0`.
  - Both are sorted by Rounds desc, then Key.
- **`ByReport`:** counts rows whose `ReportOutcome` is non-nil, with `unstructured`
  counted as `no outcome`.

### 4.4 `Render(r Report, name func(token string) string) string`

`name` maps a token to its display name. Pass identity until A1 lands. A1 round 2
switches it to `rt.Candidates.NameOf`.

The text, section by section (widths as shown; values right-aligned in numeric
columns):

```
relevo stats · 2026-08-25 → 2026-09-24 · 142 rounds in 31 bindings · $11.80 + 38 on plan · 96.4M tok · median 14m

candidates                       RNDS  DONE  HALT   MED   TTFT  $/RND  COMMITS
  deepseek-v4.1-flash               96   98%    1%   12m   2.5s  $0.09      1.0
  gemini-3.8-flash-high             22   82%    9%   18m   7.0s   plan      0.9
  glm-5.3-flash *                    4   75%    0%    9m   2.4s  $0.03      1.0
  (* fewer than 5 rounds)            unrecorded: 3 rounds

spend per day (known cost; plan rounds excluded)
  ▁▂▁▃▅▂▁ ▂▄█▃▂▅▃▁▆█▄▇█▇▅█   09-01 … 09-24
  this week $6.10 · last week $3.20 · +91%

reliability
  switches 17 in 12 rounds (8%) · rate limits 11 · spawn failures 1
  limits by local hour  00 01 02 … 23
    google                .  1  .  …  3
  active  openai rate-limited until Oct 19 22:16

repos                            RNDS    COST  HALT  RNDS/LAND
  github.com/fuad-daoud/relevo    118   $9.90     5        2.1
features                         (same columns; the section is omitted when empty)

outcomes
  rounds   reported 128 · halted 7 · switched 4 · exited 1 · no report 2 · open 0
  reports  done 100 · halted 7 · blocked 3 · deferred 1 · no outcome 17
```

- Dollars use `$%.2f`, and `<$0.01` for anything between 0 and 0.01.
- Tokens use the existing short format (`usage.ShortTokens`).
- Durations are minutes (`%dm`), or `%dh%02dm` at an hour and over.
- TTFT is `%.1fs`, or `-` when absent.
- `$/RND` is `-` when there is no cost.
- **The sparkline** has one rune per day from `" ▁▂▃▄▅▆▇█"`, scaled to the window's
  max, with a space for zero days. It is followed by the first and last day as
  `MM-DD`.
- **The week line** reads `+N%` or `-N%`. When last week is 0, it reads `new`
  instead.
- **Empty report:** a report with zero rounds prints only its first line plus
  `no rounds in this window`.
- Every section heading line stays, even for empty sections, so the output keeps its
  shape. `features` is the only omitted section.

### 4.5 `historyStats` (`cmd/relevo/history.go:355-382`)

1. `ParseSince(since, now)`.
   - An empty `since` means `30d`.
   - The literal `all` means a zero cut. Handle `all` here before `ParseSince`, and
     document it in the usage text at 18-23: `--stats [--since D|all]`.
2. `rt, err := newRuntime()`. Then open the DB the same way the normal history path
   does: `openDB(rt.Store.DBPath())`, and close it with `defer`. On an open failure,
   `relevo: no database: <err>`, exit 1, as the normal path does at 243-252.
3. Build the inputs:
   - `rows := rt.DB.Query(db.Filter{Since: cut})`
   - `landed`: IDs from `rt.DB.Bindings(db.Filter{State: "done"})`
   - `hist := loadHistory(rt)`
   - `gates := relevo.Gates(rt)`
   - `ttft`: from `latency.LoadKV(rt.Latency, legacyGatesPath(rt.GatesDir, "latency.json"))`, pruned, then `h.Summary(token)`, exactly as `formatCandidates` (`main.go:901-919`). `ok` is `Summary.N > 0`.
   - `isPlan`: look up `rt.Candidates.Lookup(candidate.ParseRef(token))` and read
     `.Plan`. Any error means false.
4. `rep := stats.Build(...)` with `Loc = time.Local` and `Until = now`.
5. `--json` encodes `rep`, indented two spaces. Otherwise print
   `stats.Render(rep, func(s string) string { return s })`.

## 5. Pseudocode

```
Build(in):
  rows = in.Rows
  totals over all rows; scorecard over rows with candidate && Keep
  per token: closed/reported/halted/durations/costs/commits → ScoreRow
  days = Since..Until by Loc; add known cost per row's StartedAt day (and provider)
  weeks = sums by StartedAt window
  reliability = switches from rows; rate limits/spawn failures/by-hour from History in window; Active = Gates
  repos/features = group rows; landed from in.Landed
  outcomes = counts
```

## 6. Error handling

| case | result |
|---|---|
| `Build` | Total: never errors, never panics on nil pointers. Every nil field of a `RoundRow` is handled explicitly. |
| `historyStats` DB open failure | `relevo: no database: <err>`, exit 1. |
| A bad `--since` | `ParseSince`'s error, exit 2. |
| latency or history read failures | The warning `formatCandidates` prints, and the report continues without that data. |

## 7. Tests

### `internal/stats/stats_test.go`

Build fixtures with helpers like `internal/histq/fixture_test.go:27-34`
(`fxStr`, `fxInt`, `fxFloat` …). Copy what you need; it is a `_test` file in another
package.

- `TestScorecardRates`: closed and open rows, `DonePct` and `HaltPct` against Closed,
  and the median for both even and odd counts.
- `TestScorecardUnrecordedAndKeep`: a nil candidate is excluded from the scorecard and
  counted in Totals; `Keep` drops a row.
- `TestScorecardPlanAndCost`: a plan token has no cost; unknown basis is excluded from
  the mean; `Few` is set under 5.
- `TestSpendDaysAndWeeks`: zero days are present, the provider split sums to USD, and
  the week windows are exact at their boundaries.
- `TestReliabilityWindowAndHours`: history events outside the window are excluded, the
  by-hour buckets use `Loc` (use a fixed-offset location), and `Active` is passed
  through.
- `TestReposFeaturesLanded`: `RoundsPerLand` is right, and zero landed gives 0.
- `TestOutcomes`: unstructured is counted as "no outcome".
- `TestBuildEmpty`: no rows gives a zero report with no panic.

### `internal/stats/render_test.go`

- `TestRenderGolden`: one rich fixture and `testdata/report.golden`, with the repo's
  `-update` flag convention (`internal/ui/golden_test.go:23`). Generate it, **read it**
  and check it against §4.4, and paste it into the report.
- `TestRenderEmpty`.

### `internal/db/read_test.go`

Extend the seeded fixture's assertion, or add `TestQueryReturnsSwitches`: a round
upserted with `Switches: 2` reads back as 2.

### `internal/ingest/outcome_test.go`

`TestSwitchesSkipRelaunch`: a real switch plus a `relaunched builder` entry plus a
`resumed session` entry gives 1. If an existing test asserts that relaunches count,
update it and cite §4.2.

### `cmd/relevo/history_tab_test.go`

`TestHistoryStatsNewReport` replaces X3. Seed the temp DB the way the neighbouring
history tests do, run `history --stats --since all` and `--json` in-process, and
assert:
- the section headings `candidates`, `spend per day`, `reliability`, `repos` and
  `outcomes`;
- that the JSON decodes into `stats.Report`.

This runs no harness.

### Mutation check

In `Build`, count `open` rows as closed. `TestScorecardRates` must fail. Report it,
then revert.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch independent reads and edits as parallel tool calls in one step.
- Read each named range once.
- Iterate on
  `go test ./internal/stats/... ./internal/db/... ./internal/ingest/... ./cmd/relevo/ -run 'History|Stats'`.
- Run `make check` once at the end.

## 9. Ordered steps

**1. db and ingest.**
- Deliverable: §4.1, §4.2 and their tests.
- Verify: `go test ./internal/db/... ./internal/ingest/...`.

**2. `internal/stats` `Build`.**
- Deliverable: §4.3 and `stats_test.go`.
- Verify: `go test ./internal/stats/`.
- Depends on 1.

**3. `Render`.**
- Deliverable: §4.4 and `render_test.go`, with the golden inspected.
- Depends on 2.

**4. Rewire and delete.**
- Deliverable: §4.5, the usage text, X1–X3 deleted with the grep check described in
  X1, and `TestHistoryStatsNewReport`.
- Verify: `go build ./...` and `go test ./cmd/relevo/ -run 'History|Stats'`.
- Depends on 3.

**5. Check and report.**
- Run the mutation check, then `make check`.
- Report:
  - the new functions with their line ranges;
  - every deleted symbol and test with its X item;
  - the golden, pasted;
  - `git diff --stat`, which must touch only the §2 files.
- Depends on 4.
