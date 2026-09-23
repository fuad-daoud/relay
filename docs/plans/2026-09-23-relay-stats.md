# Plan: `relay stats`, local usage analytics (#322 stage 1)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt and
report if any of the following is false.

- **`relay.TabEntries(rt, cut, warn)` (`internal/relay/tab.go` ~59) returns
  every entry of every live binding's `log.jsonl`, then of every archive, and
  within one log they are in `Seq` order.** It skips an archive older than
  `cut` whole. `relay stats` reuses it unchanged. If it sorts or merges
  across logs, the segment rule in §4 is wrong.
- **The notes have these shapes** (the planner read each one):
  - A pick: `"picked <tok> for <role>: …"` (`candidate.go` `ExplainResolution`).
  - A local switch: `"switched builder (<reason>): picked <tok> for builder: …"`
    (`switch.go` `switchEntry`).
  - A remote switch: `"switched on <server>: <prev> -> <new>"` (`remote.go` ~688).
  - A relaunch: `"relaunched builder (…): picked <tok> for builder: same candidate, not counted"`
    (`headless.go` ~651). This one is a `KindSwitch` entry.
  - A consult's ask: `Note: "<role> <consultID>"` (`ask.go` ~226).
  - A skipped verify: `Note: "verify skipped: <reason>"` (`verify.go` ~188).
  - An `ask --round` consult: `Note` starts with `"round "` (`ask.go` ~451).
- **The switch reasons that exist** are `rate-limited…`
  (`limit.go` ~376, `headless.go` ~497), `exited (code …) without a report`
  (`headless.go` ~690) and `gated while queued` (`queue.go` ~43).
- **`loadHistory(rt)` (`cmd/relay/main.go` ~776)** loads
  `availability.json`, prunes it to 30 days, and turns an unreadable file into
  an empty history after one stderr line.
- **`blockedText(d)` exists in `internal/relay/policy_view.go`** and is what
  `relay policy` prints a blocked duration with.

## 1. System Overview

relay records every round in the append-only `log.jsonl` of its binding,
which is archived with the binding. `relay tab` already sums money over that
record. `relay stats` answers the other questions #322 asks, locally, from
the same record. Nothing leaves the machine. Stage 2 (opt-in telemetry) is
parked by the owner's decision of 2026-09-23 and is out of scope.

It reports:

- **Rounds**: builder rounds per harness/provider/model, and how many
  bindings they came from.
- **Outcomes**: one fixed list of how rounds ended, plus how many reports
  arrived unmarked.
- **Switches**: how many rounds switched builder, which provider each switch
  left, and why.
- **Gate**: pass/fail/timeout/error over the rounds that ran a gate.
- **Consults**: consults per role (reviewer, verify and so on), and per
  harness/provider/model.
- **Blocked**: per provider, rate-limit and spawn-failure events and the
  gates cleared by hand, from the 30-day availability history.

**Why the logs and not `relay.db`.** The logs are the write side and they
are complete. `relay.db` is a projection the daemon ingests from **live**
bindings only. Nothing ingests at archive time, anything older than the
database is missing until `relay db backfill`, and `history`'s open creates
an empty database when none exists. Its ingest also takes a round's last
pick without checking the role (`ingest.builderForRound`), so an `ask`
consult's pick can overwrite a round's `builder_*` columns. `relay tab`
reads the logs for the same reason. Do not fix ingest here; it is reported
separately.

Two limits are stated in the output, not guessed around:

- **needs-you and stalled are live state only.** They are not recorded per
  round, so they are not in the outcome list.
- **The availability history keeps no expiry time.** Only a gate cleared by
  hand (`relay available`) has a measurable span. An expired gate counts as
  an event with no duration.

**Out of scope:**
- any telemetry, upload, install ID or network access
- `relay.db`, `internal/db` and `internal/ingest`
- `relay serve stats`
- a `--by` flag
- any change to `relay tab`'s output
- any change to `TabEntries`

## 2. File Structure

```
internal/relay/
  stats.go        # NEW: report types, BuildStats, note parsers, RenderStats (all pure)
  stats_test.go   # NEW
cmd/relay/
  stats.go        # NEW: cmdStats, flag parsing and gather only
  main.go         # + case "stats" and one usage line
README.md         # + command bullet and a short "Usage stats" subsection
```

No other file changes.

## 3. Data Structures & Type Definitions

All of these go in `internal/relay/stats.go`, with JSON tags as shown. They
are what `relay stats --json` prints.

```
StatsCount       { Key string `json:"key"`; Count int `json:"count"` }

StatsSwitchFrom  { Provider string      `json:"provider"`   // "unknown" when unattributable
                   Count    int         `json:"count"`
                   Reasons  []StatsCount `json:"reasons"` } // fixed SwitchReasons order, zeros included

StatsSwitches    { Total  int               `json:"total"`   // counted switch entries
                   Rounds int               `json:"rounds"`  // counted rounds with >= 1 switch
                   From   []StatsSwitchFrom `json:"from"` }  // sorted by Count desc, then Provider

StatsBlocked     { Provider    string `json:"provider"`
                   RateLimited int    `json:"rate_limited"`  // ledger.RateLimited events in window
                   SpawnFailed int    `json:"spawn_failed"`  // ledger.SpawnFailed events in window
                   Cleared     int    `json:"cleared"`       // history.Cleared events with a usable Since
                   ClearedMS   int64  `json:"cleared_ms"` }  // sum of those spans, clipped to the window

StatsReport      { Since         *time.Time    `json:"since"`   // nil = all recorded
                   Until         time.Time     `json:"until"`   // the now passed in
                   Bindings      int           `json:"bindings"` // segments with >= 1 counted round
                   Rounds        int           `json:"rounds"`
                   Builders      []StatsCount  `json:"builders"`  // key harness/provider/model; Count desc, Key asc
                   Outcomes      []StatsCount  `json:"outcomes"`  // StatsOutcomes order, zeros included
                   Unmarked      int           `json:"unmarked"`
                   Switches      StatsSwitches `json:"switches"`
                   Gate          []StatsCount  `json:"gate"`      // StatsGateResults order, zeros included
                   Consults      []StatsCount  `json:"consults"`  // by role; Count desc, Key asc
                   ConsultModels []StatsCount  `json:"consult_models"` // by harness/provider/model
                   VerifySkipped int           `json:"verify_skipped"`
                   Blocked       []StatsBlocked `json:"blocked"` } // sorted by Provider
```

Fixed lists, exported, in this order:

- `StatsOutcomes = []string{"done", "halted", "blocked", "deferred", "unstructured", "noreport", "stopped", "exited", "open"}`
- `StatsGateResults = []string{"pass", "fail", "timeout", "error"}`
- `SwitchReasons = []string{"rate-limited", "exited", "gated", "remote", "other"}`

Every slice is non-nil (`[]`, never `null`, in JSON).

## 4. Interface Definitions & Component Contracts

Everything in this section is pure. No function reads disk, the clock or
the environment.

### `BuildStats(entries []TabEntry, hist history.History, since, now time.Time) StatsReport`

`since.IsZero()` means no cut, and `Since` stays nil.

**Segments.** Walk `entries` in order. A new segment (one binding's log)
starts at the first entry, and at every entry whose `Binding` differs from
the previous entry's, or whose `Entry.Seq` is `<=` the previous entry's
`Seq`. The second rule separates two archives, or an archive and a live log,
that share a binding name. Round numbers are only unique within a segment.

**Builder token tracking, per segment.** Keep `cur`, the builder token in
effect, which starts as `""`. On each `KindPick` or `KindSwitch` entry:

- If `builderTokenFromNote(note)` is ok, it becomes `cur`.
- Before updating, a counted switch records `from = cur` (see Switches).

**Rounds.** A round is `(segment, Entry.Round)` for `Round >= 1` that has a
`KindPlan` entry with `Direction == DirToBuilder`. Its start is the first
such entry's `TS`. It is **counted** when `since` is zero or its start is not
before `since`. Only counted rounds feed Rounds, Builders, Outcomes,
Unmarked, Switches and Gate.

**Builder key of a round.**

1. If the round's last `KindReport` entry has a non-nil `Usage` with
   `Harness != ""`, use `Harness + "/" + Provider + "/" + Model` with empty
   parts dropped, the same as `tabKey`'s model trim.
2. Else, if `cur` at the round's last entry is not empty, use
   `refKey(cur)`.
3. Else use `"unknown"`.

**Outcome of a round.** `R` is the round's last `KindReport` entry.

- `R` exists and its `Note` contains `"noreport"` gives `noreport`.
- `R` exists otherwise: `R.Outcome` of `done`, `halted`, `blocked` or
  `deferred` gives that value. Any other value, `""` included, gives
  `unstructured`.
  - `Unmarked++` when `R.Note` contains `"unmarked"`.
- No `R`, and the round has a `KindStop` entry, gives `stopped`.
- No `R`, and the round has a `KindExit` entry, gives `exited`.
- Otherwise `open`.

**Gate.** Count `R.Gate.Result` when `R` exists and `R.Gate != nil`, and only
for results that are in `StatsGateResults`. Ignore any other value.

**Switches.** Walk `KindSwitch` entries in counted rounds:

- A note that starts with `"relaunched "` is **not** a switch. It updates
  `cur` and is otherwise ignored.
- A note that starts with `"switched on "`: from is
  `remoteSwitchFrom(note)`, and the reason is `remote`.
- Any other note: from is `cur` before the update, and the reason is
  `switchReason(note)`.
- The from provider is `providerOfToken(from)`, or `"unknown"`.
- `Total++`. The round counts toward `Rounds` once. `From[provider].Count++`,
  and that provider's reason count goes up by one.

**Consults.** Walk all `KindAsk` entries with `Direction == DirToConsult`
whose `TS` is not before `since`. Consults are not tied to counted rounds.

- A note that starts with `"verify skipped:"` gives `VerifySkipped++`, and
  nothing else.
- A note that starts with `"round "` gives role `"session"`.
- Otherwise the role is the note's first space-separated word, or
  `"unknown"` if that is empty.
- Then `Consults[role]++`.

`ConsultModels`: for each `KindFindings` entry with non-nil `Usage` and
`Harness != ""`, whose `TS` is not before `since`, count its
harness/provider/model key, built the same way as builder key rule 1.

**Blocked.** Call `blockedStats(hist, since, now)`.

### `builderTokenFromNote(note string) (string, bool)`

- If the note has the prefix `"switched on "` and contains `" -> "`, return
  the text after the last `" -> "`, trimmed.
- Otherwise find `"picked "`. The token is the text after it, up to the next
  space, and it counts only if the text right after the token is
  `" for builder:"`. Otherwise return `"", false`. A pick for `reviewer`,
  `verify` or any other role is not a builder token.

### `remoteSwitchFrom(note string) (string, bool)`

For `"switched on <server>: <prev> -> <new>"`, return `<prev>`: the text
between the first `": "` after the prefix and the last `" -> "`, trimmed.
Anything else returns `"", false`.

### `switchReason(note string) string`

Take the text after `"switched builder ("`:

- It starts with `rate-limited` gives `rate-limited`.
- It starts with `exited` gives `exited`.
- It starts with `gated` gives `gated`.
- Anything else, or no such prefix, gives `other`.

### `refKey(tok string) string` and `providerOfToken(tok string) string`

- Both call `candidate.ParseRef(tok)`, and a parse error gives `"unknown"`.
- `refKey` returns `Harness/Provider/Model`, with the model cut at its first
  `#`, the same as the effort strip in ingest.
- `providerOfToken` returns `Provider`.

### `blockedStats(h history.History, since, now time.Time) []StatsBlocked`

Walk `h.Events`, and only those with `Provider != ""`:

- `ledger.RateLimited` with `At` not before `since` gives `RateLimited++`.
- `ledger.SpawnFailed` with `At` not before `since` gives `SpawnFailed++`.
- `history.Cleared` with a non-zero `Since` that is not after `At`: clip
  `[Since, At]` to `[since, now]` (zero `since` means no lower clip). If the
  clipped span is positive, `Cleared++` and `ClearedMS += span`.

A provider with every count at zero is omitted. Sort by `Provider`.

### `RenderStats(r StatsReport) string`

The text layout is exact. Labels are left-aligned in an 11-column field. In
every list that uses the fixed order, zero items are omitted.

```
relay stats: since 2026-09-16 00:00 UTC                  # or "relay stats: all recorded rounds"
rounds     42 in 9 bindings
builders   opencode/cline-pass/cline-pass/deepseek-v4.1-flash  18
           claude/anthropic/sonnet  12                     # one per line, "<key>  <n>"
outcomes   done 30, halted 4, unstructured 3, exited 2, open 1; 2 unmarked
switches   7 in 5 of 42 rounds (12%)
           openai  4 (rate-limited 3, exited 1)            # one line per From entry
gate       pass 25, fail 5, error 1 of 31 gated rounds (81% pass)
consults   reviewer 3, verify 2; 1 verify skipped
           claude/anthropic/sonnet  5                      # one line per ConsultModels entry
blocked    openai  rate-limited 3, spawn failed 1, 1 cleared by hand (5h12m)
           (availability history keeps 30 days; an expired gate has no recorded length)
```

The rules:

- The since line is formatted `2006-01-02 15:04 UTC` in UTC.
- `bindings`/`binding` agrees with the count.
- The `; N unmarked` part appears only when the count is above zero.
- The percentages are `round(100*a/b)`.
- An empty section prints one line after its label instead:
  - builders: nothing
  - outcomes: nothing
  - switches: `none`
  - gate: `no gated rounds`
  - consults: `none`
  - blocked: `none`
- The two-line footnote under `blocked` is always printed.
- The `(%)` in the switches line is left out when there are no rounds.
- Durations use `blockedText`.
- If `Rounds == 0`, `Consults` is empty and `Blocked` is empty, the whole
  output is `no rounds\n`.

## 5. High-Level Pseudocode

```
cmdStats(args):
  flags: --since (relay.ParseSince), --json; any positional -> usage error
  rt := newRuntime()
  entries := relay.TabEntries(rt, cut, warn "relay stats: skip <msg>")
  hist := loadHistory(rt)                           # pruned to 30d
  rep := relay.BuildStats(entries, hist, cut, rt.Now().UTC())
  --json ? encode rep, indented : print relay.RenderStats(rep)

BuildStats:
  for each segment (binding change or Seq not increasing):
     track cur builder token through pick/switch notes
     collect rounds that have a plan: start, last report, stop/exit seen, switches
  counted rounds -> builders, outcomes, unmarked, gate, switches (from/reason)
  asks/findings in window -> consults, consult models, verify skipped
  blockedStats(hist)
```

## 6. Error Handling Strategy

The pure functions have no errors: every unparseable note or token becomes
`"unknown"` or `other` and is never dropped silently. `cmdStats` returns these
errors:

- `ParseSince`'s `ErrBadSince`, as `relay tab` does
- a `TabEntries` error, from a live log it cannot read
- a JSON encode error

An unreadable archive is a stderr `skip` line, and an unreadable history is
`loadHistory`'s stderr line with an empty history. Both match `relay tab` and
`relay policy`. Nothing new is logged.

## 7. Ordered Implementation Steps

After every step, run `make check` and `gofmt -l $(git ls-files '*.go')`.
The second must print nothing. All tests go in `internal/relay/stats_test.go`,
in memory, and use `store.LogEntry` literals the way `tab_test.go` does. Add
**no** `cmd/relay` test that runs a subcommand: CI runners have no harness
binary and no network, and the rule is to test in `internal/relay`. No test
opens `relay.db`.

1. **Note parsers.** Add `builderTokenFromNote`, `remoteSwitchFrom`,
   `switchReason`, `refKey` and `providerOfToken`.
   *Verify:* table tests with one row per note shape in the halt list,
   including:
   - a reviewer pick, which gives not ok
   - a relaunch note, which gives its token
   - `"switched on contabo: codex/openai/gpt-5.6-terra:high -> opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high"`,
     which gives the new token (from `builderTokenFromNote`) and the old one
     (from `remoteSwitchFrom`)
   - `refKey("opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high") == "opencode/cline-pass/cline-pass/deepseek-v4.1-flash"`
   - `refKey("bad")` gives `"unknown"`

   *Mutation (required):* drop the `" for builder:"` check and confirm the
   reviewer-pick row fails. Restore it.

2. **Rounds, outcomes, gate and builders.** Add the types, the fixed lists,
   and `BuildStats` without switches, consults or blocked.
   *Verify:* a fixture with one row per outcome in `StatsOutcomes`, plus:
   - a report noted `"unmarked"` gives `Unmarked == 1`, and its outcome is
     still its tail outcome
   - a report noted `"unmarked noreport"` gives `noreport`
   - a round with no plan entry is not counted
   - a round whose plan is before `since` is not counted
   - two segments with the same binding name, each with its own round 1,
     give `Rounds == 2` and `Bindings == 2`
   - the builder key comes from the report's `Usage` when present, else from
     the pick, else `"unknown"`
   - gate counts per result, and an out-of-list result is ignored
   - `Outcomes` and `Gate` hold every key in fixed order, zeros included

   *Mutation (required):* remove the `Seq <= previous` segment rule and
   confirm the two-segment row fails. Restore it.

3. **Switches.** Add them to `BuildStats`.
   *Verify:*
   - A round with a pick of an openai token, then a `rate-limited: …`
     switch, gives `From[openai] = 1 (rate-limited 1)`.
   - A relaunch note is not counted and does not change `Total`.
   - A remote switch attributes to the `<prev>` provider with reason
     `remote`.
   - Two switches in one round give `Total == 2` and `Rounds == 1`.
   - A switch before any pick gives from `"unknown"`.

   *Mutation (required):* count relaunch notes, and confirm the relaunch row
   fails. Restore it.

4. **Consults and blocked.** Add the consult walk and `blockedStats`.
   *Verify:*
   - Roles `reviewer` and `verify` come from ask notes.
   - `"verify skipped: no diff"` counts only toward `VerifySkipped`.
   - A `"round 2 session claude:abc"` note gives role `session`.
   - `ConsultModels` comes from findings usage.
   - In blocked:
     - a Cleared span that straddles `since` is clipped
     - a Cleared with zero `Since` is skipped
     - RateLimited and SpawnFailed counts are windowed
     - a provider with all zeros is omitted

5. **Render.** Add `RenderStats`.
   *Verify:* a golden string for a fixture that covers every section, whose
   output matches §4's layout rules line for line. Also:
   - one golden per empty-section form
   - the all-empty report gives `"no rounds\n"`
   - `--json` shape: `json.Marshal` of an empty `StatsReport` built by
     `BuildStats(nil, history.History{}, zero, now)` contains `"builders":[]`
     and no `null` except `"since":null`

6. **CLI.** Add `cmd/relay/stats.go` with `cmdStats`, mirroring `cmdTab`, and
   `case "stats": return cmdStats(args[1:])` next to `case "tab"` in
   `main.go`. Add this usage line after the `tab` line:
   `  stats     rounds, outcomes, switches, gate and consults across bindings, archived ones included; provider blocks from the last 30d [--since 7d] [--json]`
   *Verify:* `make check` passes. Then `go build -o /tmp/relay-stats ./cmd/relay`
   and run it with `XDG_STATE_HOME` pointing at an empty `t`-style temp dir
   you create by hand; it must print `no rounds`. Do **not** run it against
   the real `~/.local/state/relay`, and do not write any state.

7. **README.**
   - Add a `relay stats [--since 7d] [--json]` bullet to the command-surface
     list, after the `relay tab` bullet.
   - Add a short `### Usage stats` subsection after `### Round usage` that
     covers:
     - that it reads the same logs and archives as `relay tab`, locally only
     - the outcome list and what each one means
     - that needs-you and stalled are live-only, and why they are absent
     - the 30-day blocked window, and that only a gate cleared by hand has a
       length

   *Verify:* `make check` passes.

## Report

- The files touched in each step, and `git diff --stat` against `main`. It
  must match §2 exactly.
- All three required mutation checks: what you broke, and which named test
  failed.
- What you found for each halt condition. Quote any note shape that differs
  from the list.
- The output of step 6's empty-state run.
