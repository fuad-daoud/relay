# Live usage: exact token split + running-round figure -- implementation plan

**Goal:** Every surface that prints a round shows `in / cache / write / out`
as four exact fields, and `relay ui`, `relay status` and `relay statusline`
show a `live` figure for a round that is still running.

**Spec:** `docs/specs/2026-09-20-live-usage-design.md` -- read it first; this
plan argues from it and repeats nothing it already settles. Issue #234.

**Architecture:** Pure renderers in `internal/usage` change shape (four
token cells, a `tok` cell, `LiveParts`/`LiveShort`). The production
`usage.Reader` gains `Peek` -- `Read` minus the wait for the exit trailer --
backed by a per-stream cache that parses only appended bytes. `relay.Status`
calls `Peek` for every binding with an open round and puts the fold on a new
`BindingStatus.LiveUsage`. Header, rail card, statusline and status text
render it. Nothing is written to disk.

## Rules for the builder

- **Stop rather than improvise.** If a step is impossible as written, or the
  code contradicts what a step assumes, halt and report which step and why.
  Do not bend a test to pass. A halt that surfaces a design error is the
  right outcome.
- **`make check` green at the end of every task**, not just the last. It
  runs `gofmt -l .`, `go vet`, `go mod tidy` check and `go test ./...`.
  `gofmt` covers the whole tree: a misformatted line anywhere fails it.
- **One commit per task**, message in the repo's style
  (`feat(usage): …`, `feat(relay): …`, `feat(ui): …`, `docs: …`), each ending
  with `(#234)`. Do not squash tasks together.
- **Golden files**: `internal/ui` goldens regenerate with
  `go test ./internal/ui -run TestGolden -update`; then read the diff of the
  `.golden` files and confirm every changed line is one this plan predicts.
  A golden that changed in a way the plan does not predict is a halt.
- **No CLI test in `cmd/relay` may reach herdr** -- CI runners have no
  `herdr` binary. Nothing in this plan adds a `cmd/relay` test.
- **Mutation check where the plan says so**: revert the named condition,
  confirm the named test fails, restore it. Report the result per task.
- **Do not run `make e2e`**; the planner runs it after pull.

## Global constraints (from the spec)

- `in` = `Tokens.In` (uncached input); `cache` = `Tokens.CacheRead` with
  `(NN%)` = `CacheRatio()`; `write` = `Tokens.CacheWrite`, printed as `0`
  when zero; `out` = `Tokens.Out`. `tok` = `ShortTokens(Tokens.Total())`.
- Measured and estimated dollars never share one number. `plan` and
  `unknown` are never `$0`. The live figure is never summed into `Spend`
  and never written to `log.jsonl`.
- The word `live` precedes every live figure. No live figure when the
  reader has no samples (surface shows nothing, never `live unknown`).
- `liveDeadline = 500 * time.Millisecond` per binding in `Status`.
- Live read caches by `(size, mtime)` and parses appended bytes only.

---

## Task 1 -- renderers: four token cells, `tok`, `LiveParts`, `LiveShort`

**Files**
- Modify `internal/usage/format.go` (`Line` :58, `Parts` :94)
- Modify `internal/usage/spend.go` (`SpendLine` :75, `MoneyShort` :90)
- Modify `internal/usage/format_test.go` (`TestLine` :47, `TestParts` :102)
- Modify `internal/usage/spend_test.go` (`TestSpendLine` :55, `TestMoneyShort` :74)
- Modify `internal/relay/logline_test.go` (`TestLogLineWithUsageAddsSecondLine` :24) -- expected string only
- Modify `internal/relay/status_test.go` (`TestStatusLastUsageAndSpend` :1500) -- expected strings only
- Regenerate `internal/ui/testdata/*.golden` (the `usage`/`spend` rows and the cards' facts line)
- Modify `internal/ui/split_test.go` (`TestPaneHeadUsageAndSpendRows` :361) -- expected string only

**Produces** (exact signatures later tasks call)

```go
// internal/usage/format.go
func tokenCells(t Tokens, samples int) []string   // unexported
func Parts(u Usage) []string                      // shape unchanged, cells replaced
func Line(u Usage) string                         // unchanged: Parts joined by "  "
func LiveParts(u Usage) []string                  // new
func LiveShort(u Usage) string                    // new

// internal/usage/spend.go
func SpendLine(s Spend) string                    // gains trailing tok cell
func MoneyShort(s Spend) string                   // gains trailing tok cell
```

**Pseudocode**

```
tokenCells(t, samples):
  if samples == 0: return nil
  prompt := t.In + t.CacheRead + t.CacheWrite
  cache := "cache " + ShortTokens(t.CacheRead)
  if prompt > 0: cache += fmt(" (%.0f%%)", t.CacheRatio()*100)
  return ["in "+ShortTokens(t.In), cache, "write "+ShortTokens(t.CacheWrite), "out "+ShortTokens(t.Out)]

Parts(u):
  parts := [model-or-harness]           // exactly as today
  if ShortDuration(u.DurationMS) != "": parts += it
  parts += tokenCells(u.Tokens, u.Samples)...
  parts += money-word (with ": note" under Unknown, exactly as today)
  return parts

Line(u):
  today's loop over Harness/Provider/Model joined by "/", then duration,
  then tokenCells(...)..., then money -- joined by two spaces.
  (Line's identity cell stays harness/provider/model; Parts' stays model|harness.)

LiveParts(u):  return append(["live"], Parts(u)...)

LiveShort(u):
  if u.Samples == 0: return ""
  return "live " + Money(u.Cost) + " · " + ShortTokens(u.Tokens.Total()) + " tok"

SpendLine(s):  today's string; if s.Tokens.Total() > 0 append " · " + ShortTokens(total) + " tok"
MoneyShort(s): moneyParts(s) joined by " · "; if s.Tokens.Total() > 0 append the same tok cell.
               (When moneyParts is empty and tokens > 0, return just the tok cell.)
```

**Tests** (table-driven, expected strings verbatim from spec §3)

| case | expect |
|---|---|
| `Line` claude measured: In 2100, CacheRead 166000, CacheWrite 14000, Out 12000, 14m, $0.41, Samples 1 | `claude/anthropic/claude-sonnet-5  14m  in 2k  cache 166k (91%)  write 14k  out 12k  $0.41` |
| `Line` opencode, CacheWrite 0: In 410000, CacheRead 3100000, Out 7000, 5m, $0.16 | `opencode/cline-pass/glm-5.3-flash  5m  in 410k  cache 3.1M (88%)  write 0  out 7k  $0.16` |
| `Line` unknown, Samples 0, Note "agy keeps no usage record", 6m | `agy/google/gemini-3-pro  6m  unknown: agy keeps no usage record` |
| `Line` plan: In 1200, CacheRead 88000, CacheWrite 4000, Out 6000, 9m, Plan true | `claude/anthropic/opus  9m  in 1k  cache 88k (94%)  write 4k  out 6k  plan` |
| `LiveParts` opencode measured: Model glm-5.3-flash, 4m, In 1800, CacheRead 91000, CacheWrite 3100, Out 8200, $0.04 | joined by ` · `: `live · glm-5.3-flash · 4m · in 2k · cache 91k (95%) · write 3k · out 8k · $0.04` |
| `LiveParts` claude estimated: Model claude-sonnet-5, 2m, In 900, CacheRead 40000, CacheWrite 1200, Out 2300, ~$0.02 | `live · claude-sonnet-5 · 2m · in 900 · cache 40k (95%) · write 1k · out 2k · ~$0.02` |
| `LiveShort` Samples 1, Cost ~$0.04, Tokens.Total 103000 | `live ~$0.04 · 103k tok` |
| `LiveShort` Samples 0 | `` |
| `SpendLine` Rounds 4, Consults 2, Measured 1.23, Estimated 0.40, Plan 1, Unknown 2, Tokens total 2.1M | `4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown · 2.1M tok` |
| `SpendLine` Rounds 1, Measured 0.41, Tokens zero | `1 round · $0.41` (no tok cell) |
| `MoneyShort` Measured 1.51, Tokens total 2.1M | `$1.51 · 2.1M tok` |
| `MoneyShort` Unknown 2, Tokens total 206000 | `2 unknown · 206k tok` |

Every expected string above uses `ShortTokens` exactly as it is today
(whole thousands below 1M: 1800 -> `2k`) and `CacheRatio` rounded with
`%.0f`; the arithmetic was checked when the plan was written
(166000/182100 = 91%, 3100000/3510000 = 88%, 88000/93200 = 94%,
91000/95900 = 95%, 40000/42100 = 95%). If a computed value disagrees with a
row, halt and report the row rather than changing either.

Existing tests in `internal/relay` and `internal/ui` that assert the old
`in <prompt> (cache NN%)` string will fail: update their expected strings to
the new cells for the same inputs (compute from the fixture's `Tokens`; do
not change the fixtures). Regenerate ui goldens and verify every changed
golden line is a `usage`, `spend` or facts line.

**Verify:** `make check` green. Mutation: make `tokenCells` omit the
`write` cell -> the opencode `Line` case fails on `write 0`.

**Commit:** `feat(usage): four token cells on the round line, tok on spend, LiveParts/LiveShort (#234)`

---

## Task 2 -- `Reader.Peek` without the trailer wait

**Files**
- Modify `internal/usage/source.go` (`Reader` :38, `reader.Read` :51, `readStream` :120)
- Modify `internal/usage/source_test.go`
- Modify `internal/relay/fake_test.go` (`fakeUsage` :585) -- add `Peek`

**Produces**

```go
// internal/usage/source.go
type Reader interface {
	Read(ctx context.Context, src Source) (samples []Sample, note string)
	Peek(ctx context.Context, src Source) (samples []Sample, note string)
}
```

`fakeUsage` gains fields `peekSamples []Sample`, `peekNote string`,
`peeks []Source`, and `Peek` records the source and returns them (it never
blocks; `block` applies to `Read` only).

**Pseudocode**

```
reader.Peek(ctx, src):
  switch src.Mode:
    headless: return r.readStream(ctx, src, /*wait=*/false)
    pane:     return r.readPane(ctx, src)        // already bounded by the window
  default:    return nil, "no reader for mode " + mode

readStream(ctx, src, wait bool):
  stat; if missing: return nil, "no stream"
  closed := false
  if wait: closed = waitClosed(ctx, path) else closed = streamClosed(path)
  ... parse exactly as today ...
  if len == 0: return nil, "no usage events"
  if !closed: return samples, "stream still open"
  return samples, ""

reader.Read(ctx, src): headless -> readStream(ctx, src, true)   // unchanged behaviour
```

Do not add the cache in this task; that is Task 3. Peek re-parses from
byte 0 here and that is fine for the tests.

**Tests** (`internal/usage/source_test.go`)
- `TestPeekOpenStreamReturnsPartial`: write two claude `assistant` events
  with distinct `message.id` and no trailer; `Peek` with a 2s ctx returns
  2 samples, note `stream still open`, and returns in under 200ms (assert
  elapsed < `trailerPoll`; the point is it did not poll). Then append the
  trailer line (`ExitTrailerForTest()` + `0`) and a `result` event; `Read`
  returns 1 sample (the result) and note ``.
- `TestPeekMissingStream`: `Peek` on a path that does not exist -> nil,
  `no stream`.
- `TestPeekPaneDelegates`: pane Source for agy -> nil, `agy keeps no usage
  record` (same as Read).
- Existing `TestReadHeadlessWaitsForTrailer` and
  `TestReadHeadlessTimesOutOnOpenStream` still pass untouched -- they pin
  that `Read` still waits.

**Verify:** `make check`. Mutation: make `Peek` call `readStream(..., true)`
-> `TestPeekOpenStreamReturnsPartial` fails on elapsed time (or hangs to
its ctx and fails on the note).

**Commit:** `feat(usage): Reader.Peek reads an open stream without waiting for the trailer (#234)`

---

## Task 3 -- resumable stream parsers and the per-stream cache

**Files**
- Create `internal/usage/carry.go` -- `streamCarry` interface, `streamCache`, cache map
- Modify `internal/usage/claude.go` (`claudeStream` :80), `agy.go` (`agyStream` :38),
  `opencode.go` (`opencodeStream` :39), `codex.go` (`codexStream` :27) -- each
  becomes a thin wrapper over its carry
- Modify `internal/usage/source.go` (`reader` :42, `New` :49, `readStream`)
- Create `internal/usage/carry_test.go`

**Produces**

```go
// internal/usage/carry.go
type streamCarry interface {
	feed(line []byte)      // one JSON line; ignores what it cannot parse
	samples() []Sample     // the fold so far; result-style events replace step samples
}
func newCarry(harness, provider, model string) (streamCarry, bool)  // false: no reader for harness

type streamCache struct {
	size   int64
	mtime  time.Time
	offset int64        // bytes fed so far (whole lines only)
	carry  streamCarry
	tail   []byte       // an incomplete trailing line, kept for the next feed
}

// on reader:
type reader struct {
	exec  Exec
	home  string
	mu    *sync.Mutex
	cache map[string]*streamCache   // by StreamPath
}
func New(exec Exec, home string) Reader   // allocates mu and cache; returns the value as today
```

`reader` stays a value type; `mu` and `cache` are pointers so every copy
shares them.

**Pseudocode**

```
each harness carry:
  fields = what the current stream function keeps in locals
    claude:   model, result *Sample, assistant []Sample, seen map[id]bool
    agy:      model, result *Sample, steps []Sample
    opencode: out []Sample
    codex:    out []Sample (model already effort-stripped by the caller, as today)
  feed(line): the body of today's scanLines callback
  samples():  the tail of today's function after the loop (result wins; fill model)

claudeStream(r, provider) etc.: c := newCarry(...); scanLines(r, c.feed); return c.samples()
  -- existing tests for the four stream functions pass unchanged; this is the pin
     that the carry did not drift.

readStream(ctx, src, wait):
  stat path; missing -> nil, "no stream"
  closed := wait ? waitClosed : streamClosed
  samples := r.parseCached(src)                // replaces the open+parse
  notes as today

reader.parseCached(src):
  lock mu
  e := cache[path]
  info := stat
  if e == nil || info.Size() < e.offset:      // new, or truncated/reused
      c, ok := newCarry(harness, provider, model); if !ok: return nil, "no reader for "+harness
      e = &streamCache{carry: c}; cache[path] = e
  if info.Size() == e.size && info.ModTime().Equal(e.mtime): return e.carry.samples()
  open; seek e.offset; read to EOF
  buf := e.tail + read
  split buf on '\n': for every complete line, if len>0 && line[0]=='{': e.carry.feed(line)
  e.tail = the bytes after the last '\n'
  e.offset = info.Size() - len(e.tail)
  e.size, e.mtime = info.Size(), info.ModTime()
  return e.carry.samples()
```

Line-size guard: `scanLines` caps a line at `maxLine`; the incremental
splitter must apply the same cap (drop a `tail` that exceeds `maxLine`
and note nothing -- a pathological line is skipped, as `scanLines` skips
it today).

**Tests** (`internal/usage/carry_test.go`)
- `TestParseCachedFeedsOnlyAppendedBytes`: write 3 opencode `step_finish`
  lines; `Peek` -> 3 samples. Append 2 more; wrap the file open in a
  counting hook (simplest: assert `e.offset` advanced by exactly the
  appended byte count and `len(samples)==5`). Read the cache entry through
  an unexported accessor in the test (same package).
- `TestParseCachedPartialLineWaits`: append half a JSON line (no `\n`);
  `Peek` -> still 3 samples, `tail` non-empty; append the rest + `\n` ->
  4 samples.
- `TestParseCachedTruncatedFileResets`: after 5 samples, truncate the file
  to 0 and write 1 line -> 1 sample.
- `TestParseCachedUnchangedFileIsNoRead`: two `Peek`s with no write; the
  second must not open the file (assert via `e.offset`/`e.size` unchanged
  and -- cheaper -- by removing read permission between calls on a
  non-root test; skip that assertion if `os.Getuid()==0`).
- `TestClaudeCarryDedupesAcrossFeeds`: two feeds of the same `message.id`
  in separate `Peek`s -> 1 sample.
- Existing `TestClaudeStream*`, `TestAgyStream*`, `TestOpencodeStream*`,
  `TestCodexStream*` pass with no edits.

**Verify:** `make check`. Mutation: make `parseCached` always seek to 0 ->
`TestParseCachedFeedsOnlyAppendedBytes` fails (offset check) and
`TestClaudeCarryDedupesAcrossFeeds` still passes -- so also make the
carry's `seen` map fresh per feed -> that test fails. Report both.

**Commit:** `feat(usage): resumable stream carries and a per-stream cache behind Peek (#234)`

---

## Task 4 -- `Status` fills `LiveUsage`; text `usage` row shows it

**Files**
- Modify `internal/relay/status.go` (`BindingStatus` :25-66, `statusRow` around :318-334, `RenderStatus` :583)
- Modify `internal/relay/usage.go` -- add `liveDeadline` const and `peekUsage`
- Modify `internal/relay/status_test.go`

**Produces**

```go
// status.go
type BindingStatus struct {
	// ... existing ...
	// LiveUsage is what the open round has consumed so far, read from the
	// harness's record on this call (#234). nil when no round is open or
	// nothing is readable yet. Never recorded, never summed into Spend.
	LiveUsage *usage.Usage `json:"live_usage,omitempty"`
}

// usage.go
const liveDeadline = 500 * time.Millisecond
func peekUsage(ctx context.Context, rt Runtime, b store.Binding, now time.Time) *usage.Usage
```

**Pseudocode**

```
peekUsage(ctx, rt, b, now):
  if rt.Usage == nil || b.RoundStartedAt.IsZero() || b.Builder.Kind == "": return nil
  src := roundSource(rt, b, b.RoundStartedAt, now)
  pctx, cancel := WithTimeout(ctx, liveDeadline); defer cancel
  samples, note := rt.Usage.Peek(pctx, src)
  if len(samples) == 0: return nil
  u := usage.Fold(samples, rt.Prices, src.Plan, note)
  u.Harness = src.Harness; if u.Provider == "": u.Provider = src.Provider
  if u.Model == "": u.Model = src.Model         // mirror recordUsage's fallbacks; read recordUsage :74 and copy its post-Fold fixups exactly
  u.DurationMS = now.Sub(b.RoundStartedAt).Milliseconds()
  return &u

statusRow: after row.Spend is set (:333):
  row.LiveUsage = peekUsage(ctx, rt, b, rt.Now())

RenderStatus text, replace the usage row block (:583):
  if b.LiveUsage != nil:  "  usage    " + strings.Join(usage.LiveParts(*b.LiveUsage), "  ")
  else if b.LastUsage != nil: "  usage    " + usage.Line(*b.LastUsage)     // as today
  spend row unchanged
```

`rt.Now` may be nil in some tests -- check how `statusRow` already obtains
"now" (search for `rt.Now` / `time.Now()` in status.go) and use the same
source.

**Tests** (`internal/relay/status_test.go`)
- `TestStatusLiveUsageOnOpenRound`: binding with `RoundStartedAt` set, a
  headless builder, `fakeUsage{peekSamples: [one opencode sample with
  cost]}`; `Status` -> `LiveUsage != nil`, `Cost.Basis == measured`,
  `DurationMS > 0`, `Spend` unchanged from what the log entries give
  (assert `Spend` equals the value with `LiveUsage` ignored -- construct
  the expected Spend from the fixture entries). `fake.peeks[0].Start ==
  RoundStartedAt`.
- `TestStatusNoLiveUsageWhenRoundClosed`: `RoundStartedAt` zero ->
  `LiveUsage == nil` and `fake.peeks` empty.
- `TestStatusNoLiveUsageWhenPeekEmpty`: Peek returns nil, `no usage events`
  -> `LiveUsage == nil`.
- `TestStatusTextUsageRowPrefersLive`: `RenderStatus` on a report with both
  `LiveUsage` and `LastUsage` -> the `usage` row starts with `live  ` and
  contains no second usage row.
- `TestStatusLiveUsageJSON`: marshal a `BindingStatus` with `LiveUsage` ->
  key `live_usage` present; with nil -> absent.

**Verify:** `make check`. Mutation: remove the `RoundStartedAt.IsZero()`
guard -> `TestStatusNoLiveUsageWhenRoundClosed` fails on `peeks` non-empty.

**Commit:** `feat(relay): status reads the open round's usage live via Peek (#234)`

---

## Task 5 -- statusline trailing usage segment

**Files**
- Modify `internal/relay/statusline.go` (`RenderStatusLine`, the `mid` build at :70-74)
- Modify `internal/relay/statusline_test.go`

**Pseudocode**

```
after  mid += " · " + waiting(b):
  switch:
    b.LiveUsage != nil && usage.LiveShort(*b.LiveUsage) != "":  mid += " · " + LiveShort
    b.Spend != nil && usage.MoneyShort(*b.Spend) != "":          mid += " · " + MoneyShort
```

Nothing else moves: the segment is inside `mid`, so `truncate(mid, midW)`
already cuts it first.

**Tests**
- `TestRenderStatusLineLiveSegment`: row with `LiveUsage` (opencode,
  measured $0.02, total 41000 tokens) at 120 columns -> line contains
  `plan sent · live $0.02 · 41k tok` and ends with `ACTIVE`.
- `TestRenderStatusLineSpendSegment`: row with `Spend{Measured 1.51,
  Tokens total 2.1M}` and no `LiveUsage` -> contains `· $1.51 · 2.1M tok`.
- `TestRenderStatusLineLiveWinsOverSpend`: both set -> contains `live `,
  does not contain `$1.51`.
- `TestRenderStatusLineNarrowDropsUsageFirst`: the live row at 60 columns
  -> `ACTIVE` still present at the end, `tok` absent.
- Existing tests pass unchanged (they build rows with neither field).

**Verify:** `make check`. Mutation: put the segment before `waiting(b)` ->
`TestRenderStatusLineNarrowDropsUsageFirst` fails (`plan sent` truncated
instead of `tok`).

**Commit:** `feat(relay): statusline shows live or spend usage at the end of the middle cell (#234)`

---

## Task 6 -- ui header and rail card

**Files**
- Modify `internal/ui/pane.go` (usage/spend rows :79-90)
- Modify `internal/ui/rail.go` (`facts` :93-111)
- Modify `internal/ui/golden_test.go` fixtures -- add one binding with
  `LiveUsage` set (and `Spend` set) so the goldens pin both
- Modify `internal/ui/split_test.go` -- add `TestPaneHeadLiveUsageRow`
- Regenerate goldens

**Pseudocode**

```
pane.go header rows:
  if b.LiveUsage != nil:
      parts := usage.LiveParts(*b.LiveUsage)
      styled: parts[0] ("live") accentStyle; last (money) fgStyle; the rest dimStyle
      rows += label("usage") + join(styled, sep)
  else if b.LastUsage != nil:  exactly today's block
  if b.Spend != nil: rows += label("spend") + usage.SpendLine(*b.Spend)    // unchanged

rail.go facts, after the forked-from fact:
  if b.Spend != nil && MoneyShort != "": out += dim(MoneyShort)            // unchanged
  if b.LiveUsage != nil && LiveShort != "": out += dim(LiveShort)          // new, last
```

**Tests**
- `TestPaneHeadLiveUsageRow` (split_test.go, next to
  `TestPaneHeadUsageAndSpendRows`): binding with `LiveUsage` and
  `LastUsage` both set -> exactly one `usage` row, it contains `live`, it
  does not contain the `LastUsage` model's duration; `spend` row present.
- Golden: add to the `split-all-states` fixture one ACTIVE binding with
  `LiveUsage{Model "glm-5.3-flash", DurationMS 4*60_000, Tokens{1800,
  91000, 3100, 8200}, Cost{0.04, Measured}, Samples 3}` and
  `Spend{Rounds 1, Measured 0.16, Tokens total 3.5M}`; regenerate; confirm
  the card's facts line reads `$0.16 · 3.5M tok · live $0.04 · 104k tok`
  (1800+91000+3100+8200 = 104100 -> `104k`) and the header has the live
  row.

**Verify:** `make check`; inspect the golden diff line by line.

**Commit:** `feat(ui): header usage row and rail card show the live figure (#234)`

---

## Task 7 -- `relay tab` columns, README

**Files**
- Modify `internal/relay/tab.go` (`RenderTab` :116-160)
- Modify `internal/relay/tab_test.go` -- whichever test asserts the header
  row or a rendered line; if none does, add `TestRenderTabColumns`
- Modify `README.md` `### Round usage` (:1102-1150)

**Pseudocode**

```
RenderTab header:  group  rounds  in  cache  write  out  measured  estimated  plan  unknown
row cells:         ShortTokens(s.Tokens.In), ShortTokens(s.Tokens.CacheRead),
                   ShortTokens(s.Tokens.CacheWrite), ShortTokens(s.Tokens.Out)
                   -- no percentage cell; drop the `prompt`/`cache` locals
widths:            in/cache/write/out each %7s
```

README `### Round usage`: replace the `relay log` example line with
`⎿ opencode/cline-pass/glm-5.3-flash  14m  in 2k  cache 166k (91%)  write 14k  out 12k  $0.41`;
replace the `spend` example with `4 rounds +2c · $1.23 · ~$0.40 · 2 unknown · 2.1M tok`;
add one paragraph after "Where you see it" beginning "While a round is
running," that states: `relay status` and `relay ui` show a `live` figure
read from the harness's record on each refresh, `relay statusline` appends
`live $0.02 · 41k tok` to the row, `status --json` carries it as
`live_usage`, it is estimated (`~$`) unless the harness reports dollars per
step (opencode), it is never recorded and never added to `spend`, and agy
in a pane has none. Update the `relay tab` sentence to name the four
token columns.

**Tests**
- `TestRenderTabColumns`: one row with `Tokens{In 2100, CacheRead 166000,
  CacheWrite 14000, Out 12000}` -> header contains `write`, the row
  contains `2k`, `166k`, `14k`, `12k` in that order and no `%`.

**Verify:** `make check`.

**Commit:** `feat(relay): tab prints in/cache/write/out; README round usage (#234)`

---

## Report

At the end, report per task: commit sha, `make check` result, the
mutation-check outcome. If any golden line changed in a way this plan does not predict,
say which and stop there.
