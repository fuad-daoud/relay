# Dashboard round 1: `RoundRow` columns, the `histq` query language, `relay history -q / --by`

Spec: `docs/specs/2026-09-21-dashboard-design.md` §3 (grammar), §4 (data),
§5 (CLI). Issue #172 (use 3), #186 (`tab --by day`).

This plan stands alone: everything you need is in this file and in the
tree. If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.


**Resent after a halt.** The worktree already holds Tasks 1 and 2 as
uncommitted work from the halted first attempt (`RoundRow` columns,
`internal/histq/histq.go` + tests, `ParseSince` moved). Verify them (`go
test ./internal/db/ ./internal/histq/`), do not redo them, and continue
from Task 3. The one halt was `func Tiles` colliding with `type Tiles`; the
function is now `Totals`.

## 1. System overview

`relay.db` holds every round; `db.Query(db.Filter)` returns `[]db.RoundRow`
and `relay history` prints one line per row with one flag per filter. This
round adds the query language and the aggregate functions that the
dashboard screen (round 2) and the CLI share: `internal/histq` parses
`harness:agy since:30d cost>1 by:builder auth` into a `db.Filter` plus the
in-Go conditions, applies them, groups rows by an axis with sums, and
computes totals. `db.RoundRow` gains the columns the sums need. `relay
history -q "<query>" [--by <axis>]` is the CLI face of it.

## 2. File structure

```
internal/db/types.go            RoundRow + InTokens, CacheTokens, WriteTokens, OutTokens *int64; DurationMS *int64; ReportOutcome, BuilderMode, Server *string
internal/db/read.go             Query's SELECT/scan add the columns (round.in_tokens ... round.report_outcome, round.builder_mode, binding.server; duration computed in Go from started/closed)
internal/db/read_test.go        + TestQueryRowCarriesTokensDurationAndMode
internal/histq/histq.go         Query, Axis, NumCond, Parse, (Query).String, ErrQuery
internal/histq/apply.go         (Query).Apply
internal/histq/group.go         GroupRow, Group, Tiles
internal/histq/*_test.go
internal/relay/history.go       FormatGroups(groups []histq.GroupRow, by histq.Axis, loc) string; HistoryOptions.Query string merged into Filter
internal/relay/history_test.go  + tests
cmd/relay/history.go            -q, --by, --rows flags; merge rule; group output
cmd/relay/history_test.go       + pure tests of the merge note and group formatting
README.md                       `### relay history` gains the query grammar and --by
```

`internal/histq` imports `internal/db` and the standard library only. It
must not import `internal/relay` (which will import it). Check with
`go list -deps ./internal/histq | grep relay/internal/` -> only `internal/db`
(and whatever `internal/db` itself pulls).

`ParseSince` lives in `internal/relay/tab.go`. `histq` cannot import it;
move the function body to `internal/histq` as `ParseSince(s string, now
time.Time) (time.Time, error)` and make `relay.ParseSince` a one-line
wrapper that calls it, so every existing caller and test is unchanged.

## 3. Data structures

```
// internal/db/types.go additions to RoundRow
InTokens, CacheTokens, WriteTokens, OutTokens *int64
DurationMS   *int64      // *closed_at - *started_at in ms; nil when closed_at is null
ReportOutcome *string
BuilderMode, Server *string

// internal/histq
type Axis string
const (AxisNone Axis = "none"; AxisBinding = "binding"; AxisRepo = "repo"; AxisFeature = "feature";
       AxisBuilder = "builder"; AxisHarness = "harness"; AxisProvider = "provider"; AxisModel = "model";
       AxisDay = "day"; AxisOutcome = "outcome")
func ParseAxis(s string) (Axis, bool)

type NumCond struct{ Key, Op string; Value float64 }   // Key in cost|tokens|commits|duration|round; Op in > < >= <= =
type Query struct {
    Filter db.Filter
    Words  []string
    Nums   []NumCond
    Report, Gate, Basis, Server, Mode string
    By     Axis          // AxisNone when absent
    Raw    string
    Now    time.Time     // the clock since/until were resolved against; zero = time.Now at Parse
}
type ErrQuery struct{ Token string; Pos int; Reason string }   // Error(): `query: <token> at <pos>: <reason>`

type GroupRow struct {
    Key string
    Rounds, Reported, Halted, Exited, Switched, DoneNoReport, Open int
    Commits int
    Tokens  int64
    CostUSD float64
    Unknown int
    Last    time.Time
    Rows    []db.RoundRow
}
type Tiles struct {
    Rounds int; CostUSD float64; Unknown int; Tokens int64
    Halted, Exited int; MedianDurationMS int64; Bindings, Builders int
}
```

## 4. Interfaces

```
func Parse(s string) (Query, error)                 // ParseAt(s, time.Now())
func ParseAt(s string, now time.Time) (Query, error)
    tokens: split on whitespace outside double quotes; a quoted value may contain spaces; a backslash escapes a quote
    key:value -> see the table below; key op number -> NumCond; anything else -> Words
    duplicate key: last wins; `by` twice: last wins
    since/until: ParseSince(value, now) -> Filter.Since / Filter.Until
    archived:true|false -> Filter.Archived = &bool
    outcome/report/gate/basis/mode: validated against the spec's enums; bad -> ErrQuery
    round:N -> Filter.Round (exact); round>N etc -> NumCond
    Filter.Newest = true always
func (q Query) String() string
    canonical: filter keys in the fixed order binding repo feature planner harness provider model candidate outcome
    report state gate basis server mode round since until archived, then nums in input order, then words, then by;
    values quoted when they contain a space; since/until printed as typed (keep the raw value on the Query for this: add `Since, Until string` raw fields)
func (q Query) Apply(rows []db.RoundRow) []db.RoundRow
    keeps a row when every Word is a case-insensitive substring of BindingName, *Repo or *Feature (any of the three),
    every NumCond holds (cost: *CostUSD, nil fails; tokens: sum of the four, nil counts 0; commits: *Commits, nil fails;
    duration: *DurationMS/60000, nil fails; round: Number),
    Report/Gate/Basis/Server/Mode equal the row's column when set (nil column fails)
func Group(rows []db.RoundRow, by Axis) []GroupRow
    AxisNone -> nil
    key per axis: binding BindingName; repo *Repo ("-" when nil); feature *Feature ("-"); builder *BuilderCandidate ("-");
    harness/provider/model the column ("-"); day StartedAt.In(loc).Format("2006-01-02") -- Group takes loc *time.Location as a third argument;
    outcome Outcome
    sums: Rounds; one counter per outcome value; Commits (nil = 0); Tokens; CostUSD over rows whose *CostBasis != "unknown" and CostUSD != nil;
    Unknown = rows with nil CostUSD or basis unknown; Last = max StartedAt; Rows newest first
    order: CostUSD desc, then Rounds desc, then Key asc; AxisDay: Key desc
func Totals(rows []db.RoundRow) Tiles          // NOT named Tiles: the type is Tiles and Go forbids a type and a func of one name
    Bindings = distinct BindingID; Builders = distinct *BuilderCandidate (non-nil); Median over rows with DurationMS != nil (0 when none; even count -> mean of the two middles)

// internal/relay
func FormatGroups(groups []histq.GroupRow, by histq.Axis, loc *time.Location) string
    header: <axis>  rounds  reported  halted  commits  tokens  cost  last   (axis column padded to 40, "…" truncation)
    tokens via usage.ShortTokens-style "41.2M"; cost "$9.10" plus " (3 unknown)" when Unknown > 0; last as 2006-01-02
    "no rounds" when empty
HistoryOptions.Query string   // -q text; Filter() parses it, then applies the flag overrides
```

Merge rule in `HistoryOptions.Filter`: parse `-q` first; for each explicit
flag that is non-zero, set the `db.Filter` field from the flag and, if the
query also set it to a different value, append a note
`note: --<flag> overrides <key>:<value> from -q` to a returned `[]string`
the CLI prints to stderr. `--by` overrides `by:` the same way.

## 5. Pseudocode

```
cmdHistory:
    parse flags (+ q := fs.String("q"), by := fs.String("by"), rows := fs.Bool("rows"))
    opts := HistoryOptions{..., Query: *q}
    f, notes := opts.Filter(ctx, rt, now); print notes to stderr
    rows := rt.DB.Query(f); rows = query.Apply(rows)          -- the histq.Query is kept on HistoryOptions after Filter()
    axis := ParseAxis(*by) if set else query.By
    if axis != none:
        groups := histq.Group(rows, axis, time.Local)
        --json: encode groups; unless --rows, blank each group's Rows first
        else: print FormatGroups
    else: as today (FormatHistory / json rows)
```

## 6. Error handling

- `ErrQuery` -> exit 2 with the message; nothing printed to stdout.
- `--by` with an unknown axis -> exit 2 listing the ten axes.
- A `-q` key the CLI has no flag for (`report`, `gate`, `basis`, `server`,
  `mode`, numeric ops, words) is fine -- it is applied in Go.
- Nothing here writes to the db.

## 7. Ordered implementation steps

### Task 1 -- `RoundRow` columns

**Files:** `internal/db/types.go`, `read.go`, `read_test.go`.

**Test:** `TestQueryRowCarriesTokensDurationAndMode`: seed one round with
tokens, closed_at 27 min after started_at, `report_outcome done`,
`builder_mode remote`, binding `server contabo` -> the `RoundRow` has every
new field; a round with `closed_at` null has `DurationMS == nil`.

**Verify:** `go test ./internal/db/`.

### Task 2 -- `histq`: parse and String

**Files:** `internal/histq/histq.go`, `histq_test.go`; move `ParseSince` (see §2).

**Tests**
- `TestParseEveryKey`: one token per key -> the right `Filter`/field.
- `TestParseNumericOps`: `cost>1`, `tokens>=1000000`, `commits<3`, `duration<=30`, `round=2`, `round>2`.
- `TestParseWordsAndQuotes`: `auth "api v2" harness:agy` -> Words `[auth, api v2]`.
- `TestParseSinceUntil`: `since:7d until:2026-09-01` against a fixed now.
- `TestParseBy`, `TestParseLastDuplicateWins`.
- `TestParseErrors`: unknown key, bad outcome, bad number, unbalanced quote, bad since, bad axis, bad archived, empty value -> `ErrQuery` naming the token.
- `TestStringRoundTrip`: `Parse(q.String())` equals `q` for six queries; canonical order pinned by one exact string.
- `TestParseSinceMovedKeepsRelayWrapper`: `relay.ParseSince` still exists and agrees with `histq.ParseSince` (put this one in `internal/relay`).

**Verify:** `go test ./internal/histq/ ./internal/relay/ -run 'Parse'`; `go list -deps ./internal/histq | grep relay/internal/` shows no `internal/relay`.

### Task 3 -- Apply, Group, Totals

**Files:** `internal/histq/apply.go`, `group.go`, tests.

Fixture: a literal `[]db.RoundRow` of ten rows across three bindings, two
repos, two features, three builders (`agy/...`, `claude/...`, `opencode/...#high`),
outcomes reported×6 halted×2 exited×1 open×1, two days, two unknown-basis
rows, one nil-duration row, costs and tokens chosen so every sum is
checkable by hand in the test.

**Tests**
- `TestApplyWordMatchesAnyOfThree` (mutation: drop the `Repo` branch -> fails), `TestApplyNumericConds` (each key, nil handling), `TestApplyEnumConds`.
- `TestGroupByBuilderSums` (every counter and sum asserted), `TestGroupCostSkipsUnknown` (mutation: sum unknown rows too -> fails), `TestGroupByDayNewestFirst`, `TestGroupOrderCostThenRoundsThenKey`, `TestGroupNoneIsNil`, one small test per remaining axis asserting the key set.
- `TestTotalsCounts`, `TestTotalsMedianOddEven` (over `Totals`).

**Verify:** `go test ./internal/histq/`.

### Task 4 -- CLI and README

**Files:** `internal/relay/history.go` (+test), `cmd/relay/history.go` (+test), `README.md`.

**Tests**
- `internal/relay`: `TestHistoryOptionsQueryMergesWithFlagNote` (query `harness:agy`, flag `--harness codex` -> Filter.Harness codex, one note), `TestFormatGroupsColumns` (exact header and one row).
- `cmd/relay`: pure tests only (CI has no herdr): the `--by` validation helper and the JSON shape with/without `--rows` over a literal `[]GroupRow`.

README `### relay history`: the grammar table from spec §3 (compact), three examples:
`relay history -q "harness:agy outcome:halted since:30d"`,
`relay history -q "auth cost>1" --by builder`,
`relay history --by day --since 14d`.

**Verify** (planner's machine, after the round -- say in the report that you left it):
`relay history -q "since:30d by:builder"`, `relay history -q "cost>0.05" --by day --json | jq length`, `relay history -q "provider:cline-pass"`.

### Task 5 -- full check and commit

Run: `gofmt -l .` (nothing), `go vet ./...`, `go test -race -count=1 ./...`,
`go mod tidy && git diff --exit-code go.mod go.sum`, then `make check`.
Known: `scripts/plugin-build_test.sh` case 3 fails on a clone without tags
(#242) -- if that is the **only** failure and `git tag` prints nothing, say
so and treat the check as passed.

**Commit** (one for the round):
`feat(histq): a query language over the round history, grouped sums, relay history -q/--by (#172, #186)`

## Report

Per task: what was done, the test names, the verify result, the two
mutation checks' outcomes (name the failing test). Then the commit sha and
the `make check` result. If any step was impossible as written, say which
and stop there.
