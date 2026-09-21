# Dashboard: every builder run relay ever made, filtered, grouped, in `relay ui`

Status: design, 2026-09-21. Follows `docs/specs/2026-09-20-persistence-design.md`
(the db, `db.Query(Filter)`, `RoundRow`, the ui `all` scope). Issue #172's
use 3 ("a retrospective across bindings"); #186's "`tab --by day`" falls out.

## 1. Purpose

`relay.db` holds every round relay has run: which builder ran it, how it
ended, what it changed, what it cost. `relay history` prints it as lines
and `relay ui`'s `all` scope shows one binding at a time. Neither answers
"which model halts most on this repo", "what did last week cost", "every
round codex ran, newest first" without piping through `jq`. This spec adds
a **dashboard screen** to `relay ui`: a filter line that matches on any
column, a grid of rounds, an optional regroup by binding / repo / feature /
builder / harness / provider / model / day / outcome with sums, and a row of
totals for the current filter. Selecting a round opens the existing detail
pane for it. The same filter grammar drives `relay history -q`.

## 2. Decisions

1. **A row is a round** (one builder execution); `by:<axis>` collapses
   rows into aggregate groups that expand back into their rounds.
2. **One query line** (`/`) with `key:value` tokens and bare words; the
   parser lives in `internal/histq` and is shared with `relay history -q`.
3. **A screen inside `relay ui`** (`d` toggles; `relay ui --dashboard`
   starts there), implemented as its own model in `internal/ui/dash` that
   the fleet model hosts. No new verb (#114 freeze).
4. **Aggregation in Go over one query**: `db.Query(filter)` once per
   refresh; grouping, numeric ops, substring matches and tiles are pure
   functions over `[]db.RoundRow`.
5. `db.RoundRow` grows the columns the grid and the sums need (tokens,
   duration, server, mode, report outcome); `history --json` grows with it.

## 3. Grammar (`internal/histq`)

```
query   := token*                          (whitespace separated)
token   := key ":" value                   equality; value bare or "double quoted"
         | numkey op number                op in > < >= <=   (also "=" as equality)
         | word                            substring, case-insensitive, on binding | repo | feature
key     := binding repo feature planner harness provider model candidate outcome
           report state gate basis server mode since until archived by
numkey  := cost tokens commits duration round
values  : outcome   reported|halted|exited|switched|done_no_report|open
          report    done|halted|blocked|deferred|unstructured
          gate      pass|fail|timeout|error
          basis     measured|estimated|unknown
          mode      pane|headless|remote
          archived  true|false
          since/until   24h | 7d | 2w | YYYY-MM-DD   (relay.ParseSince forms)
          by        none|binding|repo|feature|builder|harness|provider|model|day|outcome
          duration  minutes; tokens = in+cache+write+out; cost in USD
```

Mapping: every `key:value` except `by`, `report`, `gate`, `basis`, `server`,
`mode`, and the numeric/`word` tokens maps 1:1 onto `db.Filter` (`repo` sets
`Filter.Repo`; `here` is the CLI's `--here`, not a token). The rest are
applied in Go by `Query.Apply`. `by` is not a filter; it is the regroup
axis and is carried on the same line so one string reproduces a view.
Unknown key, bad enum value, bad number, unbalanced quote: `ErrQuery` with
the token and position; the caller keeps its previous query.

## 4. Data (`internal/histq`, `internal/db`)

```
// internal/db -- RoundRow gains
InTokens, CacheTokens, WriteTokens, OutTokens *int64
DurationMS *int64          // closed_at - started_at when both set
ReportOutcome *string
BuilderMode, Server *string
// Query's SELECT and scan add these columns; Bindings/others unchanged.

// internal/histq
type Query struct {
    Filter   db.Filter          // the db half
    Words    []string           // substring terms
    Nums     []NumCond          // {Key, Op, Value float64}
    Report, Gate, Basis, Server, Mode string
    By       Axis
    Raw      string             // the text as typed, normalised
}
type Axis string                // none|binding|repo|feature|builder|harness|provider|model|day|outcome
type NumCond struct{ Key string; Op string; Value float64 }

func Parse(s string) (Query, error)
func (q Query) String() string                        // canonical text (round-trips through Parse)
func (q Query) Apply(rows []db.RoundRow) []db.RoundRow // the in-Go half, order preserved
func Group(rows []db.RoundRow, by Axis) []GroupRow     // by none -> nil
func Totals(rows []db.RoundRow) Tiles                  // named Totals: the type is Tiles

type GroupRow struct {
    Key        string        // the axis value ("-" when null)
    Rounds, Reported, Halted, Exited, Switched, DoneNoReport, Open int
    Commits    int
    Tokens     int64         // sum of the four
    CostUSD    float64       // sum where basis != unknown
    Unknown    int           // rounds with unknown basis
    Last       time.Time     // newest StartedAt
    Rows       []db.RoundRow // the group's rounds, newest first
}
type Tiles struct {
    Rounds int; CostUSD float64; Unknown int; Tokens int64
    Halted, Exited int; MedianDurationMS int64; Bindings, Builders int
}
```

`Group` orders groups by `CostUSD` desc then `Rounds` desc then `Key`;
`day` keys are `YYYY-MM-DD` in the caller's location and sort newest
first. `builder` keys are the candidate token with any `#effort` suffix
kept (it is what the user picked).

## 5. CLI mirror: `relay history -q "<query>" [--by <axis>] [--json]`

`-q` is parsed with `histq.Parse`; the existing flags still work and are
merged (a flag and a token for the same key: the flag wins, and a note on
stderr says so). `--by` (or `by:` in the query) prints group rows:

```
builder                                     rounds  reported  halted  commits   tokens     cost   last
agy/antigravity/claude-sonnet-4-6               31        27       3       58    22.1M    $9.10   2026-09-20
```
`--json` with a `by` prints `[]GroupRow` (without `Rows` unless
`--rows`). This is the `tab --by day` #186 asked for, on the db instead of
the log files.

## 6. The screen (`internal/ui/dash`)

```
 relay · dashboard    harness:agy since:30d by:builder             / filter  b regroup  s sort  d fleet  r refresh
 rounds 57   cost $14.20 (3 unknown)   tokens 41.2M   halted 4 · exited 2   median 23m   bindings 12 · builders 3
 builder                              rounds  reported  halted  commits   tokens    cost        last
▸agy/antigravity/claude-sonnet-4-6        31        27       3       58    22.1M   $9.10  2026-09-20
 agy/google/gemini-3.8-flash-high         26        25       1       40    19.1M   $5.10  2026-09-19
   2026-09-20 22:01  persist   r5  reported   +1  clean  pass   1.2M   $0.42   27m         <- expanded rounds
```

- `d` from the fleet screen enters; `d` or `esc` returns, filter intact.
  `relay ui --dashboard` starts here. The dashboard is a `screen` value in
  the fleet model; `dash.Model` owns its state and receives the fleet's
  `Runtime`, size, and tick.
- `/` opens the query input (bubbles `textinput`), prefilled with the
  current query; `enter` applies (parse error -> red inline message under
  the input, previous query kept), `esc` cancels.
- `b` cycles `by` through the axes; `enter` on a group row toggles its
  expansion; `enter` on a round row switches to the fleet screen pointed
  at that binding with `detail.round` = that round (the `all` scope is
  turned on if the binding is not live). `[`/`]` there work as before.
- `s` cycles the sort column for the visible level (round rows: started,
  cost, tokens, duration, commits; group rows: cost, rounds, halted, last);
  `S` flips direction.
- `r` re-queries; the fleet's status tick also re-queries at most every
  10 s while the dashboard is visible.
- Row rendering: outcome coloured as the rail colours states (halted =
  attention colour, open = live); archived rounds dim; `unknown` cost
  renders `?`; long tokens `41.2M`; the grid scrolls, header fixed; below
  110 columns drop `commits`, `tree`, `gate`, then `tokens`.
- Prefs: `Dashboard string` (the query text), `DashboardSort string`.

## 7. Errors

| case | behaviour |
|---|---|
| no db (`rt.DB == nil`) | `d` shows the same notice as `a` and stays on the fleet screen |
| `db.Query` error | the tiles row shows `query failed: <err>`, last good rows stay |
| parse error | inline under the input, previous query kept, nothing re-queried |
| a round row whose binding is gone from the db between refreshes | `enter` shows a notice, no screen change |

## 8. Testing

Pure, no herdr. `histq`: a table over every key, both value forms, each
op, `word`, `by`, quoting, and eight bad inputs; `String()` round-trip;
`Apply` over literal rows (mutation: drop the `word` branch -> its test
fails); `Group` per axis with the sums checked by hand (mutation: sum
`CostUSD` over unknown-basis rows too -> `TestGroupCostSkipsUnknown`
fails); `Totals` including median over odd/even counts. `db`: the new
columns round-trip through `Query`. `cmd/relay`: `-q` merge rule and the
group formatter. `dash`: goldens for flat, grouped, expanded, narrow width,
parse error, empty result; `enter` on a round row yields the fleet screen
pointed at the right binding/round.

## 9. Out of scope

Column menus (a later layer over the same query). Charts. Editing or
acting on rounds from the dashboard (no `done`, no `gc`). Planner
transcript search. Per-server views (#216). Persisting anything but the
query and sort in prefs.

## 10. Rounds

1. `docs/plans/2026-09-21-dashboard-r1-histq.md` -- `RoundRow` columns,
   `internal/histq`, `relay history -q/--by`.
2. `docs/plans/2026-09-21-dashboard-r2-screen.md` -- `internal/ui/dash`,
   the `d` screen, `--dashboard`, prefs, goldens.
