# Availability history: what the ledger observed, kept, by provider and hour

**Issue:** #61, step 7 (the observation half only; quality counters are not built -- see §1)
**Depends on:** #61 step 1 (ledger, #86), step 2 (`relay policy`, #88)
**Amends:** `docs/specs/2026-09-11-availability-ledger-design.md` §1 "Scope
boundary" ("Peak-hour history ... nothing aggregates them here" -- this
does); README "Policy" and "Availability"

## 1. System overview

The ledger records when a provider was rate-limited or a spawn failed, but
it is a *current-state* file: `Prune` drops every expired entry on every
write, and `relay available` clears the rest. Nothing remembers that
Google was limited at 19:08 and 19:19 today, so nothing can tell a planner
at 19:00 tomorrow that binding agy now is a bad bet.

This design keeps the observations. Every ledger append is mirrored into
`~/.local/state/relay/history.json`, an append-only record pruned to a
30-day window, and `relay policy` reads it two ways:

- a **peak** column per candidate row: `limited 3x around 21:00 (30d)` --
  how often the row's provider was rate-limited within an hour of now, in
  the window;
- a **history** block: per provider and kind, a 24-cell row of counts by
  local hour, so the planner can see a provider's busy hours at a glance.

Nothing *decides* on it. The order and the gates remain the rule; this is
the "look before you bind" input #61 wanted, without a scorer. If the
histogram ever shows a pattern the order should react to, that is the
moment to design the scorer (#61 step 3) with a real factor -- not before.

### What is deliberately not here

- **Quality counters** (halts, rejected reports, redone rounds): they
  need a definition of "rejected" and "redone" that relay does not have
  and the planner has not asked for. Out until a real question needs them.
- **Predictive gating**: history never gates or reorders. A provider that
  was limited at this hour five days running is still tried first if the
  order says so; the column is the planner's cue to write a different
  order or `relay unavailable` pre-emptively.
- **Latency history**: the ledger does not observe latency.
- Anything in the daemon. The daemon writes history only through the
  ledger writers it already calls (`recordSpawnFailureLocked`).

## 2. File structure

```
internal/history/history.go        Event, History, Load, Save, Prune, Append, FromEntry, HourCounts, RetainWindow   (new)
internal/history/history_test.go   round-trip, prune window, FromEntry both kinds, HourCounts in a fixed location
internal/store/store.go            HistoryPath()
internal/relay/herdr.go            Runtime.HistoryPath
internal/relay/ledger.go           appendEntryLocked mirrors into history; Unavailable and recordSpawnFailure* use it
internal/relay/ledger_test.go      both writers leave a history event; Available leaves none; a corrupt history does not fail the ledger write
internal/relay/policy_view.go      peak column; history block; FormatPolicy takes history
internal/relay/policy_view_test.go
cmd/relay/main.go                  newRuntime sets HistoryPath; cmdPolicy loads history
README.md, docs/design.md          "History" under "Availability"; state files list
```

## 3. Data structures and type definitions

### 3.1 `history.Event`

```
type Event struct {
    At       time.Time   `json:"at"`
    Kind     ledger.Kind `json:"kind"`                 // spawn_failed | rate_limited
    Provider string      `json:"provider"`             // always set
    Token    string      `json:"token,omitempty"`      // the candidate, for spawn_failed; "" for rate_limited
    Source   string      `json:"source"`               // relay | planner
    Binding  string      `json:"binding,omitempty"`
    Note     string      `json:"note,omitempty"`
}
```

### 3.2 `history.History`

```
type History struct { Events []Event `json:"events"` }

const RetainWindow = 30 * 24 * time.Hour

func Load(path string) (History, error)                     // missing = empty, nil
func Save(path string, h History) error                     // write-then-rename, like ledger.Save
func (h History) Prune(now time.Time) History               // drops At < now-RetainWindow; pure
func (h History) Append(e Event) History                    // pure; no dedupe
func FromEntry(e ledger.Entry, providerOf func(token string) string) Event
func HourCounts(h History, provider string, kind ledger.Kind, loc *time.Location) [24]int
```

`FromEntry`: `RateLimited` → `Provider = e.Subject`, `Token = ""`;
`SpawnFailed` → `Token = e.Subject`, `Provider = providerOf(e.Subject)`.
`At`, `Source`, `Binding`, `Note` copied. `HourCounts` buckets
`e.At.In(loc).Hour()` for events matching provider and kind. `loc` is a
parameter so tests are timezone-independent; production passes
`time.Local`.

### 3.3 `relay.Runtime` (modified)

```
HistoryPath string   // rt.Store.HistoryPath(); tests set a temp path
```

## 4. Interface definitions and component contracts

### 4.1 `relay.appendEntryLocked`

```
func appendEntryLocked(rt Runtime, e ledger.Entry) error
```

Caller holds the store lock. Loads, prunes and appends `e` to the ledger,
saves; then loads, prunes and appends `FromEntry(e, providerOf)` to the
history, saves. **A history failure does not fail the call**: the ledger
write already happened and is the one that gates; the history error is
printed to stderr (`relay: could not record history: …`) and dropped.
`providerOf` is the same closure `Gates` uses.

`Unavailable` becomes `rt.Store.WithLock(func(*store.Tx) error { return
appendEntryLocked(rt, entry) })`. `recordSpawnFailureWith(rt, mutate, …)`
is replaced by `recordSpawnFailureWith(rt, locked bool, …)`: the entry is
built as today and committed through `appendEntryLocked`, wrapped in
`WithLock` when `!locked`. `recordSpawnFailure` / `recordSpawnFailureLocked`
keep their signatures. `mutateLedger` / `mutateLedgerLocked` stay for
`Available` (a clear is not an observation).

### 4.2 `relay.FormatPolicy` (signature change)

```
func FormatPolicy(set *candidate.Set, pol policy.Policy, gates []ledger.Gate, hist history.History, now time.Time, loc *time.Location) string
```

Peak column, per row, from `HourCounts(hist, provider, RateLimited, loc)`:
`n := c[(h+23)%24] + c[h] + c[(h+1)%24]` where `h = now.In(loc).Hour()`.
When `n > 0` the row's tail begins with `limited <n>x around <HH>:00 (30d)`,
before any gate text, all joined by `; `, then the marker as today.

History block, after warnings and before the no-policy footer, only when
`hist` has any events:

```
history (30d, local hours)
             00 01 02 03 04 05 06 07 08 09 10 11 12 13 14 15 16 17 18 19 20 21 22 23
  anthropic  rate-limited   .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  1  .  .  .  2  1  .  .  .
  google     spawn failed   .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  .  1  .  .  .  .
```

One row per `(provider, kind)` with a non-zero count, providers sorted,
`rate_limited` before `spawn_failed`. Cells are `%2d` or ` .` for zero,
space-separated. `cmdPolicy` loads history with the same
error-tolerant rule as `Gates` (stderr once, empty) and passes
`rt.Now()`, `time.Local`.

### 4.3 `store.HistoryPath`

`filepath.Join(s.root, "history.json")`, beside `ledger.json`.

## 5. High-level pseudocode

```
relay unavailable claude/anthropic/sonnet --reason "5h window"
   WithLock: ledger += {rate_limited anthropic}; history += {at, rate_limited, anthropic, planner, note}
daemon switch, replacement spawn fails
   (lock held) ledger += {spawn_failed tok}; history += {at, spawn_failed, provider(tok), tok, relay, binding}
relay policy
   hist := history.Load(HistoryPath).Prune(now)     -- read-only; the file is pruned on write, so a stale event may show until the next write
   rows gain the peak column; block rendered when len(hist.Events) > 0
```

## 6. Error handling strategy

| condition | outcome |
|---|---|
| history file unreadable on write | stderr line; ledger write stands |
| history file unreadable on read (`relay policy`) | stderr line; treated as empty |
| history `Save` fails | stderr line; ledger write stands |
| `FromEntry` on a `spawn_failed` whose token no longer parses | `Provider == ""`; the event is kept and counted under no provider (never shown) |

No new log kinds, no daemon changes beyond the writer it already calls.

## 7. Ordered implementation steps

1. **`history` package, `HistoryPath`, `Runtime.HistoryPath`, writers.**
   §3, §4.1, §4.3. Tests: round-trip; `Prune` keeps an event at exactly
   `now-RetainWindow` and drops one a second older; `FromEntry` both
   kinds; `HourCounts` with events at 21:30 and 21:59 in a fixed `loc`
   → bucket 21 == 2; in `internal/relay`: `Unavailable` leaves one ledger
   entry and one history event with the provider and note; a bind spawn
   failure leaves one of each with the token and provider; `Available`
   leaves the history untouched; a history path under a regular file
   makes `Unavailable` still succeed and print the stderr line.

2. **`relay policy` peak column and history block, docs.** §4.2. Tests:
   the existing `FormatPolicy` tests pass `history.History{}`, `baseTime`,
   `time.UTC` and are unchanged in output; a new test with three
   `rate_limited` events on provider `test` at hours 20, 21 and 22 (UTC)
   and `now` at 21:15 UTC renders `limited 3x around 21:00 (30d)` on the
   `test` rows only, and the history block with `1` in cells 20, 21, 22;
   an event 31 days old is not counted. README: "History" paragraph under
   "Availability" and the `relay policy` example gains the column;
   `docs/design.md` state-files list gains `history.json`.

Planner verification: check constituents; the writers' mutation (drop
the history append -- the `Unavailable` history test fails); on this
machine `relay policy` shows today's real gates as history once the next
ledger write lands.
