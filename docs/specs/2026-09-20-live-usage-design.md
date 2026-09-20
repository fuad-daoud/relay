# Live usage: the exact token split on every surface, and a figure for the running round

**Issue:** #234.
**Depends on:** #185 (the record), the usage-surfaces PR (the printers,
`relay tab`), #122 (`relay statusline`). All landed.
**Amends:** `docs/specs/2026-09-18-usage-surfaces-design.md` §2 (the
round line's token cells) and §3 (every surface gains the live row);
`docs/specs/2026-09-13-statusline-design.md` §3.3 (the middle cell gains
a trailing usage segment). Neither spec's invariants change: measured and
estimated dollars never share a number, and `plan`/`unknown` are never `$0`.
**Follow-ups that stay out:** #186.

## 1. System overview

#142 records what a round consumed and prints it, but two things a human
wants to read are not there. The round line collapses the prompt into one
number -- `in 3.5M · cache 88% · out 7k` -- where `in` is uncached input
plus cache reads plus cache writes, and `cache_write` appears on no text
surface at all. And every surface is silent about a round that is still
running: usage is read once, at round close, after the stream's exit
trailer, so an ACTIVE binding in `relay ui` has no `usage` row in its
header, no figure on its rail card, and nothing in `relay statusline`.

This design does two things. It replaces the collapsed prompt cell with
the four fields `Tokens` already records -- `in`, `cache`, `write`,
`out` -- on every surface that prints a round, and adds total tokens to
the two surfaces that print only money. And it gives `relay.Status` a
**live** figure for a binding whose round is open: the same readers,
pointed at the same record, asked once per refresh what is on disk so
far, and rendered with the word `live` so no reader mistakes a partial
figure for a closed one. At close the recorded figure replaces it, exactly
as today; the live figure is never written to `log.jsonl` and never folded
into `spend`.

Nothing here is a new verb. `relay tab` changes columns; `status --json`
gains one field.

### What the readers already give us (verified against the code, 2026-09-20)

| harness | headless stream, mid-round | pane, mid-round |
|---|---|---|
| claude | one sample per `assistant` event deduped by `message.id`, tokens only; `result` (with `total_cost_usd`) arrives at close and then wins | `~/.claude/projects/<slug>/**/*.jsonl` is appended live; samples inside `[start, now]` |
| agy | one sample per `step_update`/`agent_response`, tokens only; `result` at close | none -- `unknown`, as today |
| opencode | one sample per `step_finish`, **with `cost`** -- live dollars are measured | `opencode.db` is written per message; samples inside the window |
| codex | one sample per `turn.completed`, tokens only | none today (#230 scope) |

`readStream` already returns partial samples for an open stream with the
note `stream still open`; it only *waits* for the trailer because
`waitClosed` is called first. The live path is that read without the
wait.

## 2. Vocabulary

- **`in` is uncached input.** `Tokens.In` as recorded. Today's line
  printed the whole prompt under `in`; that overload ends here. The word
  keeps its place at the front of the token cells so a reader's eye lands
  where it did.
- **`cache` is `Tokens.CacheRead`**, followed in parentheses by its share
  of the prompt (`CacheRatio`), as today's percentage was.
- **`write` is `Tokens.CacheWrite`.** Printed whenever `Samples > 0`, as
  `0` when nothing was written, so a reader learns the field exists.
- **`out` is `Tokens.Out`**, thinking included, as today.
- **`tok` is `Tokens.Total()`** in short form -- the one number a rail
  card or a statusline has room for.
- **Short form is `ShortTokens` as it stands**: whole thousands below 1M
  (`1800` -> `2k`), one decimal above. The exact counts are in `--json`
  and `log.jsonl`; the text surfaces show the split, not the digits.
- **`live` is a partial figure.** It is a `usage.Usage` folded from what
  is on disk now, with `Cost.Basis` as `Fold` computes it (`measured`
  when every sample had dollars -- opencode; `estimated` otherwise;
  `unknown` when a price is missing). It is not recorded and not summed.
  The word `live` precedes it on every surface. When the reader finds
  nothing yet (`no usage events`, `no stream`), there is no live figure:
  the surface shows nothing rather than `live unknown`.
- **`spend` is closed rounds only**, as today. A surface that shows both
  puts them in separate cells (`spend` row vs `usage` row; `$1.51` vs
  `live ~$0.04` on a card) and never adds them.

## 3. Rendering rules (`internal/usage`)

All pure; every surface calls these and none re-implements them. Existing
signatures keep their names; two functions are new.

```go
// tokenCells is the four token cells in order, or nil when Samples == 0:
//   "in 2k", "cache 166k (91%)", "write 14k", "out 12k"
// The percentage is omitted when the prompt total is 0.
func tokenCells(t Tokens, samples int) []string

// Parts: [model|harness] [duration] tokenCells... money
// Unchanged shape; the three old token parts become the four cells.
func Parts(u Usage) []string

// Line: Parts joined by two spaces, as today.
func Line(u Usage) string

// LiveParts is Parts with "live" prepended and DurationMS taken as given
// (the caller sets it to now - start). Cost word as Money renders it.
func LiveParts(u Usage) []string

// SpendLine: "4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown · 2.1M tok"
// The tok cell is last and omitted when Tokens.Total() == 0.
func SpendLine(s Spend) string

// MoneyShort: SpendLine without the rounds head, for the rail card and
// the statusline: "$1.51 · 2.1M tok".
func MoneyShort(s Spend) string

// LiveShort is the card/statusline form of a live figure:
// "live ~$0.04 · 103k tok". "" when u.Samples == 0.
func LiveShort(u Usage) string
```

Worked examples (golden-tested):

| input | `Line` |
|---|---|
| closed claude headless, measured | `claude/anthropic/claude-sonnet-5  14m  in 2k  cache 166k (91%)  write 14k  out 12k  $0.41` |
| closed opencode, no cache writes | `opencode/cline-pass/glm-5.3-flash  5m  in 410k  cache 3.1M (88%)  write 0  out 7k  $0.16` |
| unknown, no samples | `agy/google/gemini-3-pro  6m  unknown (agy keeps no usage record)` -- `Line` parenthesises the note, as it always has; `Parts` (the ui header) uses `unknown: note` |
| plan lane | `claude/anthropic/opus  9m  in 1k  cache 88k (94%)  write 4k  out 6k  plan` |

| input | `LiveParts` joined by ` · ` |
|---|---|
| opencode mid-round, 3 steps with cost | `live · glm-5.3-flash · 4m · in 2k · cache 91k (95%) · write 3k · out 8k · $0.04` |
| claude mid-round, assistant events only | `live · claude-sonnet-5 · 2m · in 900 · cache 40k (95%) · write 1k · out 2k · ~$0.02` |

## 4. The live read (`internal/usage`, `internal/relay`)

### 4.1 `Reader.Peek`

```go
type Reader interface {
	Read(ctx context.Context, src Source) (samples []Sample, note string)
	// Peek is Read without waiting for the record to close. It returns
	// what is on disk now. For a headless Source it never calls
	// waitClosed; for a pane Source it is Read with End = src.End (the
	// caller passes now). It never errors and never blocks past ctx.
	Peek(ctx context.Context, src Source) (samples []Sample, note string)
}
```

`Peek` on the production reader is `readStream` with the trailer wait
skipped and `readPane` as-is. The fake reader in `internal/relay`'s tests
gains the method.

### 4.2 Cache

`Status` runs on every `relay ui` tick (2s, `defaultInterval`) and on
every `relay statusline` invocation, across every binding. A headless
stream grows to tens of MB over a long round; re-parsing it each tick is
not acceptable. The production reader keeps, per `StreamPath`:

```go
type streamCache struct {
	size    int64
	mtime   time.Time
	offset  int64     // bytes already parsed
	samples []Sample  // folded so far
	carry   streamCarry // per-harness parser state: model seen, ids deduped, pending result
}

// streamCarry is what a resumable stream parser needs between calls.
// Each harness's parser implements it; the cache holds it opaquely.
type streamCarry interface {
	feed(line []byte)
	samples() []Sample
}
```

Entries are keyed by `StreamPath + "\x00" + Harness`: a mid-round builder
switch reuses the round's path with a different harness and must never
inherit the previous carry. On `Peek`: stat the file; if `(size, mtime)`
match, return the cached samples; otherwise seek to `offset` (bytes
consumed from disk, the held `tail` included -- the tail is prepended
from memory, never re-read), parse every complete appended line, record
the new `(size, offset, mtime)`. A file that shrank (truncated, or a new
round reusing the path) resets the entry. The
per-harness stream parsers gain a resumable form -- the same line loop,
with the accumulator passed in rather than created -- so `claudeStream`
and the cache do not drift. The map lives on the `reader` value held by
`Runtime`; it is not persisted.

`relay statusline` is a fresh process per invocation, so this cache does
not help it: each call parses every open stream from byte 0. That is
bounded by `liveDeadline` (§6) and by how Claude Code drives the
statusline (once per assistant message, not on a timer). If a long round
makes the statusline visibly late, persisting the cache entry beside the
stream (`NNN-usage.carry`) is the follow-up; it is not built until
measured.

The pane readers are not cached in this slice: `claudeProject` already
skips files whose modtime predates the round, and `opencode.db` is one
query. If a pane read shows up in a profile, that is a follow-up.

### 4.3 `Status`

For each binding with `!b.RoundStartedAt.IsZero()` and a builder
(`b.Builder.Kind != ""`):

```
src  := roundSource(rt, b, b.RoundStartedAt, now)
ctx  := WithTimeout(ctx, liveDeadline)          // 500ms; see §6
samples, note := rt.Usage.Peek(ctx, src)
if len(samples) == 0 { row.LiveUsage = nil; continue }
u := usage.Fold(samples, rt.Prices, src.Plan, note)
u.Harness, u.Provider (fallbacks), u.DurationMS = now - RoundStartedAt
row.LiveUsage = &u
```

`recordUsage` at round close is unchanged and keeps using `Read`, so the
recorded figure still waits for the trailer and still gets claude's
`total_cost_usd`.

A binding on `--cwd` (shared worktree) gets no pane live figure, for the
same reason its rounds are `unknown (shared cwd)` today: the `Source`
carries `Worktree: ""` and `readPane` returns nothing.

## 5. Surfaces

### 5.1 `relay ui` header (`internal/ui/pane.go`)

Rows `usage` and `spend`, in this order, each only when its value exists:

```
usage    live · glm-5.3-flash · 4m · in 2k · cache 91k (95%) · write 3k · out 8k · $0.04
spend    no rounds
```

`usage` shows `LiveUsage` when set, else `LastUsage` (the closed round's
`Parts`). The word `live` is rendered in the accent style; the cost word
stays `fgStyle`, the rest dim, as today. `spend` is `SpendLine(*Spend)`
and appears only when `Spend != nil`; a first-round binding shows no
spend row rather than `no rounds`.

### 5.2 `relay ui` rail card (`internal/ui/rail.go`, `facts`)

The facts line appends, after `forked from`:

- `MoneyShort(*Spend)` when `Spend != nil` -- now `$1.51 · 2.1M tok`
- `LiveShort(*LiveUsage)` when `LiveUsage != nil` -- `live ~$0.04 · 103k tok`

Both may appear on one card (a fourth round running on a binding with
three closed). They are separate facts separated by ` · `; the card never
sums them. The card's width rule is unchanged: facts that do not fit are
cut by the existing truncation, live last.

### 5.3 `relay log` and the ui log tab (`internal/relay/logline.go`)

The round line under each report/findings entry is `Line(u)`, so it
picks up the four cells with no change of its own. Golden files update.

### 5.4 `relay status` (`internal/relay/status.go`)

Text: the `usage` row shows `LiveParts` joined by two spaces when
`LiveUsage != nil`, else `Line(*LastUsage)`; `spend` row as today with
the `tok` cell. JSON: `BindingStatus` gains

```go
// LiveUsage is what the open round has consumed so far, read from the
// harness's record on this call (#234). nil when no round is open or
// nothing is readable yet. Never recorded, never summed into Spend.
LiveUsage *usage.Usage `json:"live_usage,omitempty"`
```

`last_usage` and `spend` keep their shape.

### 5.5 `relay statusline` (`internal/relay/statusline.go`)

The middle cell gains one trailing segment, the last thing before the
right cell so `truncate(mid, midW)` drops it first:

| condition | segment |
|---|---|
| `LiveUsage != nil` | ` · ` + `LiveShort(*LiveUsage)` |
| else `Spend != nil` and `MoneyShort` non-empty | ` · ` + `MoneyShort(*Spend)` |
| else | nothing |

```
○ beszel   r1 · opencode · plan sent · live $0.02 · 41k tok           3s · ACTIVE
● career   r3 · claude · report in · $1.51 · 2.1M tok                  4m · NEEDS YOU
```

The statusline spec's rule that the two renderers never say different
things holds: both call `LiveShort`/`MoneyShort`.

### 5.6 `relay tab` (`internal/relay/tab.go`)

Columns `in  cache  out` become `in  cache  write  out`, each
`ShortTokens` of the group's summed field; the `cache` column loses its
percentage (a sum across models has no meaningful ratio). Header widths
adjust; `--json` is `Spend` and is unchanged.

## 6. Errors

- **Slow or absent record.** `liveDeadline` is 500ms per binding. On
  timeout `Peek` returns what it has; `Status` sets `LiveUsage` only when
  samples exist. A binding whose read is slow shows nothing live and
  `Status` still returns within its budget. No note is surfaced: a live
  figure that is not there is not an error.
- **Unpriced model mid-round.** `Fold` gives `unknown` with `no price for
  …`; `LiveParts` renders `live · … · unknown: no price for cline-pass/glm-5.3-flash`.
  Tokens still print. Same rule as the closed line.
- **Stream reset.** A shrunk file resets the cache entry; a new round
  writes a new `NNN-builder.jsonl` so the path changes and the old entry
  is simply never hit again. Entries are not evicted: the cache grows by
  one entry per round a process has peeked -- a round's samples, a few KB
  -- and lives as long as the process (`relay ui`, the daemon). Eviction
  is a follow-up if a long-lived daemon ever shows it in a profile.
- **Reader nil** (`rt.Usage == nil`, tests only): no live figure.

## 7. Testing

`internal/usage`:
- `tokenCells`, `Parts`, `Line`, `LiveParts`, `SpendLine`, `MoneyShort`,
  `LiveShort`: table tests with the §3 examples verbatim.
- `Peek` on an open stream: write N events without a trailer, assert N
  samples and note `stream still open`; then append the trailer and
  assert `Read` returns the result sample. Mutation: remove the
  wait-skip and the `Peek` test hangs against its deadline.
- Cache: write 3 events, `Peek`, write 2 more, `Peek`; assert the second
  call parsed only the appended bytes (a counting reader) and returned 5
  samples. Truncate the file; assert a reset.

`internal/relay`:
- `Status` with a fake reader whose `Peek` returns samples: `LiveUsage`
  set on a binding with `RoundStartedAt` non-zero, nil on one with it
  zero, nil when `Peek` returns no samples. Mutation: skip the
  `RoundStartedAt` check and the closed-binding case fails.
- `RenderStatusLine`: a row with `LiveUsage`, a row with `Spend` only,
  a row with neither; a narrow `columns` where the segment is the part
  truncated and `ACTIVE` survives.
- `recordUsage` unchanged: existing tests pass untouched, pinning that
  the close path still calls `Read`.

`internal/ui`: golden files for header and card with `LiveUsage`,
`Spend`, both, neither.

`cmd/relay`: no new CLI test reaches herdr; `tab`'s column change is a
golden update in `internal/relay`.

`make e2e` after the change, as CLAUDE.md asks for anything near the
report path: it asserts the scraped round's note exactly, and `Status`
is what it reads that note through.

## 8. Not in scope

- Budget caps, cost-aware ordering, prices refresh, `tab --by day` (#186).
- A pane live figure for codex (no pane reader, #230) or agy (no record).
- Caching pane reads (§4.2).
- Per-owner usage for remote builders (#216).
- Writing the live figure anywhere on disk.
