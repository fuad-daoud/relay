# Round usage: tokens and cost per round, with provenance

**Issue:** #142 (this is slice 1 of it: the record. Slice 2, the surfaces --
`relay log` line, `status --json`, `relay ui` -- follows once this has
landed; `relay tab` is a new verb and waits for the #114 freeze to lift,
~2026-10-12. #181 merged 2026-09-18 as 080a28c, so nothing here is gated
on it.)
**Depends on:** #168 (landed): every headless round leaves
`NNN-builder.jsonl`, the raw stream, on disk.
**Amends:** nothing. Adds one optional field to `store.LogEntry`, one file
under `~/.config/relay/`, one package.

## 1. System overview

relay picks the harness and model for every round and records neither what
the round consumed nor what it cost. This design records both, per round,
on the round's `report` log entry, filled at round close by a reader that
knows each harness's own record shape. Every figure carries its provenance:
`measured` (the harness reported dollars), `estimated` (relay multiplied
harness-reported tokens by a price table) or `unknown` (no record, no
price, or no way to read). `unknown` is a legitimate answer, printed as
such; nothing is ever inferred from output length, and a subscription lane
is never called free.

Nothing in this slice prints. The field lands in `log.jsonl` and is
readable there; the printers come with slice 2.

### Verified sources (2026-09-18, this machine)

| harness | headless -- `NNN-builder.jsonl`, already on disk | pane |
|---|---|---|
| claude | `result` event: `usage{input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens}` and `total_cost_usd`; each `assistant` event: `message.id`, `message.model`, `message.usage`; `system`/`init` event: `model` | `~/.claude/projects/<slug>/**/*.jsonl` (session files and `<session>/subagents/agent-*.jsonl`): per record `type`, `timestamp` (RFC3339), `cwd`, `message.id`, `message.model`, `message.usage` as above. **The same `message.id` is written once per content block** (76 lines, 46 ids in one session) -- dedupe by id. |
| agy | `result.usage{input_tokens, output_tokens, thinking_tokens, cache_read_tokens}`; `init.model`; no dollars | **nothing on disk.** 281 conversations under `~/.gemini/antigravity-cli/brain/`, none carry usage; `/usage` is interactive only. `unknown`, by necessity. |
| opencode | `step_finish.part.{cost, tokens{input, output, reasoning, cache{read, write}}}` per step; no model field in the stream | `~/.local/share/opencode/opencode.db` (SQLite): `session.directory` is the worktree path; `message.time_created` epoch-ms; `message.data` JSON with `role`, `cost`, `tokens` (same shape), `modelID`, `providerID`. `opencode session list --format json` needs the background server and returned `[]` for a directory holding 21 sessions, so the CLI is not the reader; `sqlite3` is. |

Slug rule for claude's project directory, verified against five entries:
every byte of the absolute cwd that is not `[A-Za-z0-9]` becomes `-`
(`/home/fuad/.claude/projects` -> `-home-fuad--claude-projects`).

## 2. Vocabulary

- **`Basis` is provenance of the dollar figure, not truth of the bill.**
  `measured`: the harness itself reported dollars (claude's
  `total_cost_usd`, opencode's `cost`). `estimated`: relay multiplied
  harness-reported tokens by `prices.json`. `unknown`: no source, no
  tokens, or a model absent from the table. Tokens may be present under
  `unknown` (agy with no price row).
- **`plan` is a candidate flag, not a basis.** `"plan": true` on a
  candidate in `candidates.json` sets `Cost.Plan = true`; dollars are still
  recorded as the source gave them, and any printer shows `plan`, never
  `$0` and never "free".
- **`Out` includes thinking/reasoning tokens.** Every provider bills them as
  output; the readers fold `thinking_tokens` / `reasoning` into `Out`.
- **Model is the record's, falling back to the candidate's.** A sample
  names the model the record names (`claude-opus-5`); when the record has
  none (opencode's stream) the binding's `BuilderCandidate` supplies
  provider and model. Price lookup tries `provider/<record model>` then
  `provider/<candidate model>`.
- **The round is the unit; the builder that closed it is measured.** The
  window is `[Binding.RoundStartedAt, close]`. A switched round keeps its
  `switch` entry and is not split; for pane readers the window plus the
  worktree directory already covers both builders of one harness.
- **Consults get their own record**, on their `findings` entry, read by the
  same reader from the consult's pane. No new `Kind`.

## 3. File structure

```
internal/usage/
  usage.go              Tokens, Cost, Basis, Usage, Sample; Fold(samples, prices, plan) Usage
  source.go             Source; Reader interface; Exec interface; New(exec, home) Reader; dispatch by (harness, mode)
  claude.go             claudeStream(r io.Reader) []Sample; claudeProject(fsys, slugDir, window) []Sample; ProjectSlug(cwd) string
  agy.go                agyStream(r io.Reader) []Sample; pane -> nil, note "agy keeps no usage record"
  opencode.go           opencodeStream(r io.Reader) []Sample; opencodeDB(ctx, exec, dbPath, worktree, window) []Sample; the one SQL string
  prices.go             Prices; LoadPrices(path) (Prices, error); Estimate(provider, model, Tokens) (float64, bool); embedded default
  prices_default.json   shipped table with as_of and source
  testdata/
    claude-stream.jsonl       headless capture (scrubbed)
    claude-project/           a slug dir: one session file + subagents/agent-x.jsonl, duplicate message ids, one record outside the window
    agy-stream.jsonl
    opencode-stream.jsonl
    opencode-db.json          `sqlite3 -json` output for the query, three rows, one outside the window
internal/store/log.go   LogEntry.Usage *usage.Usage `json:"usage,omitempty"`
internal/relay/herdr.go Runtime.Usage usage.Reader; Runtime.Prices usage.Prices
internal/relay/usage.go roundSource(b, now) usage.Source; recordUsage(ctx, rt, b, window) *usage.Usage  (never errors)
internal/relay/reconcile.go   queueReport attaches recordUsage to the report entry
internal/relay/consult.go     findings entry attaches recordUsage for the consult
internal/doctor/doctor.go     "usage" group: sqlite3 on PATH when an opencode candidate exists; prices.json parses; as_of age
cmd/relay/main.go       wires usage.New(execRunner, home) and LoadPrices(filepath.Join(configDir, "relay", "prices.json"))
```

`internal/usage` knows harness record shapes and nothing else -- no
rounds, no bindings, no store -- the same boundary `internal/transcript`
keeps. The relay side is `internal/relay/usage.go`: one function that
builds a `Source` from a binding and one that never returns an error.

## 4. Data structures

```go
package usage

type Basis string
const (
    Measured  Basis = "measured"
    Estimated Basis = "estimated"
    Unknown   Basis = "unknown"
)

// Tokens are counts as the provider bills them. Out includes thinking.
type Tokens struct {
    In         int64 `json:"in"`
    CacheRead  int64 `json:"cache_read"`
    CacheWrite int64 `json:"cache_write"`
    Out        int64 `json:"out"`
}
func (t Tokens) Add(o Tokens) Tokens
func (t Tokens) Total() int64          // In + CacheRead + CacheWrite + Out
func (t Tokens) CacheRatio() float64   // CacheRead / (In + CacheRead + CacheWrite); 0 when the denominator is 0

type Cost struct {
    USD   float64 `json:"usd"`
    Basis Basis   `json:"basis"`
    Plan  bool    `json:"plan,omitempty"`
}

// Usage is what one round consumed. The zero value is Basis "" and is
// never written; a reader that finds nothing returns Unknown with a Note.
type Usage struct {
    Harness  string        `json:"harness"`
    Provider string        `json:"provider"`
    Model    string        `json:"model"`             // the model most Out tokens went to; "" when unknown
    DurationMS int64        `json:"duration_ms"`       // End - Start in milliseconds; 0 when Start is zero
    Tokens   Tokens        `json:"tokens"`
    Cost     Cost          `json:"cost"`
    Samples  int           `json:"samples"`           // records folded; 0 with Basis unknown means nothing was found
    Note     string        `json:"note,omitempty"`    // why unknown, or "n models" when >1 model was seen
}

// Sample is one billed message as a reader found it. HasCost says whether
// USD came from the record (measured) or is zero because it was absent.
type Sample struct {
    Provider string
    Model    string
    Tokens   Tokens
    USD      float64
    HasCost  bool
}

type Mode string
const (
    ModePane     Mode = "pane"
    ModeHeadless Mode = "headless"
)

// Source is everything a reader needs, built by internal/relay.
type Source struct {
    Harness  string    // "claude" | "agy" | "opencode"
    Mode     Mode
    Provider string    // from BuilderCandidate; "" for an adopted builder
    Model    string    // from BuilderCandidate; "" for an adopted builder
    Plan     bool      // candidate's plan flag
    StreamPath string  // headless: NNN-builder.jsonl
    Worktree string    // pane: the binding's worktree; "" for a --cwd binding
    Start, End time.Time
}

type Prices struct {
    AsOf   string                  `json:"as_of"`   // YYYY-MM-DD
    Source string                  `json:"source"`
    Models map[string]ModelPrice   `json:"models"`  // key "provider/model"
}
type ModelPrice struct {           // USD per million tokens
    In, CacheRead, CacheWrite, Out float64
}
```

Constraints: every `Tokens` field >= 0 (a negative count from a record is
treated as 0 and noted). `Cost.USD` is >= 0 and is meaningful only when
`Basis != Unknown`. `DurationMS` is `End - Start`, 0 when `Start` is zero.
`Prices.Models` keys are the same `provider/model` form `candidate.Ref`
prints, minus the harness.

`store.LogEntry` gains exactly one field:

```go
Usage *usage.Usage `json:"usage,omitempty"`   // report and findings entries only (#142)
```

`internal/store` importing `internal/usage` is a new edge; `usage` imports
nothing from relay, so there is no cycle.

## 5. Interfaces

```go
// Reader turns a Source into samples. Never errors: an unreadable source
// is zero samples and a note. Runtime.Usage holds one; tests hold a fake.
type Reader interface {
    Read(ctx context.Context, src Source) (samples []Sample, note string)
}

// Exec runs a binary and returns its stdout. Satisfied by a thin
// exec.CommandContext wrapper in cmd/relay and by a fake in tests. Used
// only for sqlite3.
type Exec interface {
    Run(ctx context.Context, bin string, args ...string) ([]byte, error)
}

func New(exec Exec, home string) Reader
//   home is os.UserHomeDir(); claude's project root is home/.claude/projects,
//   opencode's db is home/.local/share/opencode/opencode.db.

// Fold sums samples into a Usage. Tokens always sum. Cost, in order:
//   no samples                -> Basis Unknown, Note as given
//   every sample HasCost      -> Basis Measured, USD = sum of the records
//   otherwise                 -> each sample without HasCost is estimated
//                                from prices; if every one resolves, Basis
//                                Estimated and USD = measured + estimated;
//                                if any does not, Basis Unknown, USD 0,
//                                Note "no price for <provider>/<model>"
// Model is the model with the most Out tokens; Note gains "n models" when
// more than one is seen. Plan is copied through.
func Fold(samples []Sample, prices Prices, plan bool, note string) Usage

func LoadPrices(path string) (Prices, error)
//   missing file -> the embedded default, nil error
//   present      -> embedded default with the file's models overlaid (a file
//                   row replaces a default row); as_of and source from the file
//   malformed    -> ErrBadPrices, and the caller (cmd/relay) prints it and
//                   continues with the default -- a bad price file must
//                   never stop a round from closing

func (p Prices) Estimate(provider, model string, t Tokens) (usd float64, ok bool)
//   ok false when provider/model has no row. Never returns 0 for a
//   missing row: that is what ok is for.

func ProjectSlug(cwd string) string   // claude's directory rule, §1
```

Per-harness readers are unexported; their contracts are the fixtures.
Each takes the narrowest input (an `io.Reader`, an `fs.FS`, a `[]byte` of
`sqlite3 -json` output) so the tests need no filesystem beyond `testdata`.

Reading rules per cell:

- **every headless stream:** the reader first waits for the stream's
  `relay-exit:` trailer, polling every 200 ms under the caller's 5 s
  deadline. A round closes on the builder's done marker, which the
  harness writes *before* its final event (measured 2026-09-18: done at
  14:34:43, `result` at 14:34:44); without the wait the claude reader
  falls through to its killed-round fallback on every ordinary round.
  On timeout the stream is read as it is and the note gains
  `stream still open`.
- **claude stream:** take the `result` event's `usage` as one sample with
  `HasCost = total_cost_usd present`; model from the last `assistant`
  event's `message.model`, else `init.model`. If there is no `result`
  event (the round was killed, or the wait timed out), fall back to the
  `assistant` events deduped by `message.id`, `HasCost false`.
- **claude project:** walk every `*.jsonl` under the slug dir (subagents
  included); keep records with `type == "assistant"`, `timestamp` inside
  the window and `cwd == Worktree`; dedupe by `message.id`, first wins;
  one sample per id, `HasCost false`.
- **agy stream:** the `result.usage` as one sample; model from
  `init.model`; `Out = output + thinking`; `CacheRead = cache_read`.
  No `result` -> sum `step_update` steps of `step_type == agent_response`.
- **agy pane:** zero samples, note `agy keeps no usage record`.
- **opencode stream:** one sample per `step_finish` part, `HasCost true`,
  model from `Source.Model`/`Provider` (the stream has none).
- **opencode db:** `sqlite3 -readonly -json <db> "<sql>"` where the SQL
  selects `m.data` from `message m join session s on s.id = m.session_id`
  with `s.directory = '<worktree>'` and `m.time_created between <start ms>
  and <end ms>`; the path is embedded with `'` doubled, nothing else is
  interpolated. Each row's `data` with `role == "assistant"` is one sample,
  `HasCost true`, provider/model from `providerID`/`modelID`. `sqlite3`
  missing on PATH -> zero samples, note `sqlite3 not on PATH`; db missing
  -> note `no opencode store`.
- **pane with `Worktree == ""`** (a `--cwd` binding shares the planner's
  directory, and the planner's own spend would be counted): zero samples,
  note `shared cwd`.

## 6. Flow

```
queueReport(ctx, rt, tx, b, entries, path, payload, note):
    window := (b.RoundStartedAt, rt.Now())        // read BEFORE the reset below
    ... diff entry as today ...
    entry := report LogEntry as today
    entry.Usage = recordUsage(ctx, rt, b, window)  // *usage.Usage, never nil, never errors
    Queue(entry)
    ... reset as today (RoundStartedAt = zero, etc.) ...

recordUsage(ctx, rt, b, window):
    if rt.Usage == nil: return &Usage{Basis: Unknown, Note: "no reader"}
    src := roundSource(b, window)
        Harness  = b.Builder.Kind
        Mode     = headless if b.Builder.Headless() else pane
        Provider, Model, Plan = from rt.Candidates lookup of b.BuilderCandidate; "" / false when absent
        StreamPath = rt.Store.BuilderStreamPath(b.Name, b.Round) when headless
        Worktree = b.Worktree
    samples, note := rt.Usage.Read(ctx, src)
    u := Fold(samples, rt.Prices, src.Plan, note)
    u.Harness, u.Provider (when empty), u.DurationMS = src...
    return &u

consult close (consult.go, where the findings entry is built):
    same, with the consult's Endpoint, its SpawnedAt as Start, and the
    consult's candidate for provider/model.
```

Timeout: `recordUsage` wraps `ctx` with a 5 s deadline. A reader that
overruns returns what it has with note `timed out`; the round closes
regardless. The claude project walk reads only files whose mtime is
>= `Start` (a file untouched since before the round cannot hold a record
inside it).

## 7. Error handling

There is exactly one error surface -- `LoadPrices` at startup -- and it is
non-fatal. Every other failure becomes `Basis: unknown` plus a `Note`:

| condition | basis | note |
|---|---|---|
| reader nil (tests, old wiring) | unknown | `no reader` |
| stream file missing / unreadable | unknown | `no stream` |
| stream has no usage events | unknown | `no usage events` |
| claude slug dir missing | unknown | `no claude project dir` |
| agy pane | unknown | `agy keeps no usage record` |
| sqlite3 absent / db absent / query failed | unknown | `sqlite3 not on PATH` / `no opencode store` / `sqlite3: <first line of stderr>` |
| `--cwd` binding, pane | unknown | `shared cwd` |
| tokens found, model unpriced | unknown | `no price for <provider>/<model>` |
| reader timeout | as folded | `timed out` appended |

Logging: `recordUsage` does not log; the `Note` is the log. `relay doctor`
is where a human learns that `sqlite3` is missing or `prices.json` is
stale (`as_of` older than 90 days -> warn), before a round says `unknown`.

## 8. Testing

All pure, no herdr, no harness binaries, nothing under `cmd/relay`:

- `internal/usage`: each reader over its fixture; `Fold` over hand-built
  samples for each basis rule; `Estimate` with a missing row returns
  `ok == false` (mutation: make it return 0, true -- `TestEstimateMissingRow`
  must fail); `LoadPrices` overlay; `ProjectSlug` against the five verified
  entries; `opencodeDB` with a fake `Exec` that records argv (asserts
  `-readonly`, the doubled quote, the ms bounds).
- `internal/relay`: `queueReport` with a fake reader -> the report entry
  carries `Usage` with the window's duration; `RoundStartedAt` still
  resets; a reader that returns nothing -> `Basis unknown`, round still
  closes (mutation: make the reader return an error path that panics --
  no such path exists, which is the point).
- `internal/store`: round-trip of an entry with and without `Usage`; an
  entry without it marshals byte-identically to today.
- `internal/doctor`: the three usage checks with a fake `Env`.
- Real check after: one headless claude round, compare `usage` in
  `log.jsonl` against the `result` event by hand; one opencode pane round
  against `sqlite3` by hand.

## 9. Not in scope

Printing anything (slice 2). Budget enforcement. Choosing candidates by
cost (#61's scorer). Keeping prices current automatically. Splitting a
switched round between its builders. agy pane usage (no record exists to
read). Reading claude's transcript by session id (relay does not know
it; the window plus directory is exact enough for a worktree binding).
Excluding a consult's spend from the builder's round when both are pane
claude in the same worktree: the consult's window lies inside the round's,
so the round over-counts by the consult, and the findings entry carries the
consult's own figure for whoever wants to subtract it.
