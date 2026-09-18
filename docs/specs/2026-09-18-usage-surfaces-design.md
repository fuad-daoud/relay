# Usage surfaces: the round line, the binding total, and `relay tab`

**Issue:** #142, slice 2 (closes it). Slice 1 (#185, `9b153cf`) records
`usage` on report and findings entries; this slice prints it.
**Depends on:** #185 (landed). Follow-ups that stay out: #186.
**Amends:** `docs/specs/2026-09-18-round-usage-design.md` §9 ("nothing
prints" is struck); #114's verb freeze takes its **second exception**,
`relay tab`, recorded here the way `statusline` was recorded in
`2026-09-13-statusline-design.md` -- the sums the verb prints exist
regardless (they are on `status --json`), and the verb is the only home
for a cross-binding, seven-day view.

## 1. System overview

Every place a human reads a round now shows what it cost, in the same
vocabulary everywhere: `$0.41` measured, `~$0.41` estimated, `plan` for a
subscription lane, `unknown` with the reason. `relay log` and the ui's
log tab print the round line under each report and findings entry;
`relay status` (text and JSON) carries the newest round's usage and the
binding's running total; the ui's rail card shows the total as a fact;
`relay tab` sums across bindings, including archived ones, by binding,
model or provider, over an optional window.

Two rules from slice 1 hold on every surface: measured and estimated
dollars are never added into one number a reader cannot take apart, and
a `plan` lane or an `unknown` round is never printed as `$0`.

## 2. Rendering rules (`internal/usage`)

All pure. These are the contract; every surface calls them and none
re-implements them.

```go
// Money is the cost word: "$0.41" measured, "~$0.41" estimated, "plan"
// when Plan is set (wins over basis), "unknown" otherwise.
// 0 < usd < 0.005 prints "<$0.01"; everything else two decimals.
func Money(c Cost) string

// Tokens short form: < 1000 as-is, < 1_000_000 "182k", else "2.3M".
func ShortTokens(n int64) string

// Duration short form: "" for 0, "<1m" under a minute, "14m", "1h05m".
func ShortDuration(ms int64) string

// Line is the round line (issue #142's format):
//   claude/anthropic/claude-sonnet-5  14m  in 182k (cache 91%)  out 12k  ~$0.41
// Parts, each separated by two spaces, omitted when empty:
//   1. harness/provider/model with empty parts dropped ("claude", "claude/anthropic")
//   2. ShortDuration(DurationMS)
//   3. "in <ShortTokens(In+CacheRead+CacheWrite)> (cache NN%)" -- the cache
//      parenthetical only when In+CacheRead+CacheWrite > 0; omitted entirely
//      when Samples == 0
//   4. "out <ShortTokens(Out)>" -- omitted when Samples == 0
//   5. Money(Cost), followed by " (<Note>)" when Basis is Unknown and Note != ""
func Line(u Usage) string

// Spend is a binding's (or a group's) total. Measured and Estimated are
// separate sums; Plan and Unknown are round counts, never dollars.
type Spend struct {
	Rounds    int     `json:"rounds"`    // report entries
	Consults  int     `json:"consults"`  // findings entries
	Measured  float64 `json:"measured"`
	Estimated float64 `json:"estimated"`
	Plan      int     `json:"plan"`
	Unknown   int     `json:"unknown"`
	Tokens    Tokens  `json:"tokens"`
}

// Sum folds usages. isConsult marks which entries count as consults.
// Basis routes the dollars: Measured -> Measured, Estimated -> Estimated,
// Unknown -> Unknown++ (dollars ignored). Plan -> Plan++ and the dollars
// are NOT summed (a quota draw is not cash). Tokens always sum.
func Sum(us []Usage, isConsult []bool) Spend

// SpendLine: "4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown".
// Rounds always; "+Nc" only when Consults > 0; every other part only when
// non-zero. Zero rounds and zero consults: "no rounds".
func SpendLine(s Spend) string

// Parts is the round as separable parts for a surface with its own
// separator that names the harness elsewhere (the ui block): model (or
// harness), duration, "in N", "cache NN%", "out N", cost word with an
// unknown note minus any " for <provider>/<model>" suffix.
func Parts(u Usage) []string

// MoneyShort is SpendLine without the rounds part, for the rail card:
// "$1.23 · ~$0.40 · 2 unknown". "" when nothing is non-zero.
func MoneyShort(s Spend) string
```

## 3. Surfaces

### 3.1 `relay log` and the ui log tab -- `relay.LogLine`

```go
// LogLine is the one text form of a log entry, used by `relay log` and the
// ui's log tab. Line one is exactly today's format; when e.Usage != nil a
// second line follows, indented under the kind column:
//   2026-09-18 14:31:07  round 4   to_planner report    /path/004-report.md
//                        ⎿ claude/anthropic/claude-sonnet-5  14m  in 182k (cache 91%)  out 12k  ~$0.41
func LogLine(e store.LogEntry) string
```

The `⎿` is the marker `relay status`'s headless log tail already uses.
`cmd/relay/main.go`'s `cmdLog` and `internal/ui/fetch.go`'s `fetchLog`
both call it and their duplicated `fmt` string is deleted.

### 3.2 `relay status`

`BindingStatus` gains two optional fields, computed where `LastClose`
is:

```go
// LastUsage is the newest report entry's usage; nil when no report entry
// carries one (every binding before #185, and every round that has not
// closed on this build).
LastUsage *usage.Usage `json:"last_usage,omitempty"`
// Spend sums every report and findings entry that carries usage; nil
// when none does.
Spend *usage.Spend `json:"spend,omitempty"`
```

`RenderStatus` prints, after the `last` row and only when non-nil:

```
  usage    claude/anthropic/claude-sonnet-5  14m  in 182k (cache 91%)  out 12k  ~$0.41
  spend    4 rounds +2c · $1.23 · ~$0.40 · 2 unknown
```

`statusline.go` is untouched: the status line is a glance surface and
money is not glance information.

### 3.3 `relay ui`

- **Rail card:** `facts()` appends `dimStyle.Render(usage.MoneyShort(*b.Spend))`
  when `b.Spend != nil` and the string is non-empty. Same line as `dirty`
  / `2 consults` / `switched 2x`, same separator. The compact line is
  unchanged (it carries no facts).
- **Pane block:** two rows after `tree`, mirroring `relay status`'s
  rows in the block's own idiom: `usage    <usage.Parts(LastUsage)
  joined by " · ">` -- model, duration, `in N`, `cache NN%`, `out N`,
  cost word, the note's `for <provider>/<model>` suffix dropped because
  the model is the first part and the `builder` row already names the
  harness -- and `spend    <SpendLine(Spend)>`, each only when non-nil.
  (The second hands-on check found `usage.Line` overflowing the pane;
  `Parts` is what a narrow, `·`-separated row needs.) (Amended 2026-09-18 after the first hands-on
  check: the original placement, a dim string in the header bar next to
  the clock, was not found by the human looking for it. The block is
  where the binding's facts live; the header is for gates and the clock.)
- **Log tab:** through `relay.LogLine`, nothing else.
- **Goldens:** `allStatesRows` gives the `ledger` row a `Spend` (`Rounds
  3, Measured 1.23, Estimated 0.40, Unknown 1`); the `split-*` and
  `stack-*` goldens that include it are regenerated with `-update` and the
  diff is read in the PR.

### 3.4 `relay tab`

```
relay tab [--since 7d|24h|2026-09-01] [--by binding|model|provider] [--json]
```

- **Sources:** every live binding's `log.jsonl` (`Store.List()` then
  `ReadLog`) and every archive's `<name>/log.jsonl` member
  (`Store.ListArchives()`, `Store.ReadArchivedLog(path)`), read-only.
  Only report and findings entries with `Usage != nil` count. A binding
  archived twice under one name, or live and archived, sums into one
  `binding` group.
- **`--since`:** a duration suffix `h`/`d` (`24h`, `7d`) or a date
  `YYYY-MM-DD`, compared against the entry's `TS`; absent means all.
- **`--by`:** `binding` (default), `model` (`usage.Provider + "/" +
  usage.Model`, `unknown` when empty), `provider`.
- **Text:** one row per group sorted by group name, then a `total` row.

```
group                               rounds   in       cache   out     measured   estimated   plan   unknown
round-usage                              1   3.0M       77%   42k                                       1
spaceapi-ingest                          6   1.2M       88%   90k     $0.30      ~$1.10                 2
total                                    7   4.2M       85%   132k    $0.30      ~$1.10                 3
```

  `in` is `In + CacheRead + CacheWrite`, `cache` is `CacheRatio`. Empty
  cells stay empty: `measured` never prints `$0.00` for a group with no
  measured round, `plan` and `unknown` print counts or nothing.
- **`--json`:** `{"since": "<RFC3339 or null>", "by": "binding", "rows":
  [{"group": "...", "spend": {...}}], "total": {...}}`.
- **Pure core:** `relay.TabRows(entries []TabEntry, by string, since
  time.Time) ([]TabRow, usage.Spend)` where `TabEntry{Binding string;
  Entry store.LogEntry}`; `cmd/relay` only collects entries and prints.

## 4. Data structures

```go
// internal/store
type Archive struct {
	Name string    // binding name, from the file name's prefix
	At   time.Time // from the file name's stamp
	Path string
}
func (s *Store) ListArchives() ([]Archive, error)        // .archive/*.tar.gz, sorted by At
func (s *Store) ReadArchivedLog(path string) ([]LogEntry, error) // the <name>/log.jsonl member; nil, nil when absent

// internal/relay
type TabEntry struct {
	Binding string
	Entry   store.LogEntry
}
type TabRow struct {
	Group string      `json:"group"`
	Spend usage.Spend `json:"spend"`
}
type TabReport struct {
	Since *time.Time  `json:"since"`
	By    string      `json:"by"`
	Rows  []TabRow    `json:"rows"`
	Total usage.Spend `json:"total"`
}
func TabRows(entries []TabEntry, by string, since time.Time) ([]TabRow, usage.Spend)
func RenderTab(r TabReport) string
func ParseSince(s string, now time.Time) (time.Time, error)   // "" -> zero; "7d"; "24h"; "2026-09-01"; else ErrBadSince
```

## 5. Errors

`tab`: an unreadable archive is skipped with one stderr line naming it
(`relay tab: skip <path>: <err>`) and the run continues -- a corrupt
tarball must not hide the live bindings. `--by` outside the three values
and an unparsable `--since` are usage errors. Everything else in this
slice is rendering and cannot fail.

## 6. Testing

Pure, no herdr, nothing under `cmd/relay` beyond compiling:

- `internal/usage`: `Money` for each basis and the plan override, the
  `<$0.01` edge; `ShortTokens`/`ShortDuration` boundaries; `Line` for
  measured, estimated, unknown-with-tokens, unknown-with-no-samples,
  adopted builder (no provider/model); `Sum` with every basis mixed and
  plan dollars excluded (mutation: add plan dollars into Measured --
  `TestSumPlanIsNotCash` fails); `SpendLine`/`MoneyShort` part omission.
- `internal/relay`: `LogLine` with and without usage (line one
  byte-identical to today's format); `Status` over a log seeded with two
  report entries and one findings entry -> `LastUsage` is the newer
  report's, `Spend` counts 2 rounds + 1 consult; `TabRows` grouping by all
  three keys, `--since` cut, duplicate binding names merged; `ParseSince`;
  `RenderTab` empty cells.
- `internal/store`: `ListArchives` parses the stamp; `ReadArchivedLog`
  over a tarball the test writes with `tarGzDir`, and one without the
  member.
- `internal/ui`: `facts` with and without `Spend`; header with a selected
  binding's spend; goldens.

## 7. Not in scope

#186: budget caps, cost-aware candidate order, prices refresh, `--by
day`. The status line. Splitting a switched round between builders.
