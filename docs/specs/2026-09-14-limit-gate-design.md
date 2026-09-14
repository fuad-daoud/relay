# Limit gate: the daemon gates a provider on rate-limit text

**Issue:** #140.
**Depends on:** nothing open. Builds on the availability ledger (#61),
builder switching (`switchBuilder`), headless builders (#99) and the
completion marker (#117).
**Consumed by (later):** #135 (stall signals reuse the "literal patterns,
decision points only" rule), #142 (cost line reads the same ledger).
**Status:** draft; plan at `docs/plans/2026-09-14-limit-gate.md`.

## 1. System overview

The ledger gates a provider only when a human runs `relay unavailable`.
Until then a builder that hit its quota looks, to relay, like a builder
that failed: a headless process exits without a report and is switched
(counting toward `max_switches`), a pane builder goes idle and is nudged,
then scraped, and a builder that hangs on the limit runs out its round
budget and halts. The ledger's own history shows the cost: five
`rate_limited` entries in three days, every one typed by hand after reading
the pane, with the reset time guessed.

This design has the daemon read the limit itself, at the moments a round
already stops, and record the gate the human would have recorded:

- **Patterns** -- a short list of literal-ish regexes per harness, shipped
  as defaults in `internal/harness` and extendable per candidate in
  `candidates.json`.
- **Decision points** -- the text is scanned only where the round has
  already stopped from relay's point of view: a headless process that
  exited without the marker, a pane builder that went quiescent without
  the marker, and either mode halting on the round budget. Never on a
  running builder between those points; never on a round that produced the
  marker.
- **On match** -- one `rate_limited` ledger entry with `source: relay`, the
  matched line as its note and a reset time parsed from that line (or a
  policy default), followed by the existing mid-round switch. A switch
  caused by a rate-limit gate no longer counts toward `max_switches`: that
  limit is for builders that fail, not providers that close.

`relay unavailable` and `relay available` stay as the manual override.

### Decisions taken on the issue's open questions

1. **Budget halts are decision points, in both modes.** The issue's "never
   on a running builder" means "not on every tick", not "not at the halt":
   at a budget halt the round has stopped as far as relay is concerned, the
   match runs once on the transition, and a match turns the halt into the
   gate-and-switch that `relay unavailable` performs mid-round today
   (`closeOld=true`). This is the only point that catches the "silent hang,
   quota" case in the history.
2. **The planner's pane is never scanned.** relay reads no planner screen
   at any decision point today and never switches planners; a planner at
   its own limit is the human's to notice. Out of scope.
3. **`source` stays `"relay"`.** The ledger validator admits `relay` and
   `planner`; every automated entry (`spawn_failed`) already says `relay`,
   and `relay policy` already prints the source. No third value.
4. **A second match within the window appends.** Ledger entries are
   append-only and `Gated` shows every live one; a later parse that ends
   later simply outlives the earlier entry. No extend-in-place logic.
5. **Every rate-limit switch is uncounted**, whether the gate came from a
   pattern match or from `relay unavailable`. The trigger is the same
   (`gatedBuilder`), the cause is the same (a provider closed), and
   counting one but not the other would make the halt depend on who
   noticed. The `max_switches` check itself still applies (`0` still
   disables switching; a binding already at the limit stays halted); what
   changes is that a gated switch does not advance the count.

### Scope boundary

One new file in `internal/relay` (`limit.go`: matcher, reset-time parser,
the gate-and-switch helper); one new field on `harness.Harness` and on
`candidate.Candidate`; one new field on `policy.Policy`; one new parameter
on `switchBuilder`; four call sites in `headless.go` and `reconcile.go`;
README rows for the two config keys. `make e2e` runs after, since the pane
decision points sit on the nudge/scrape path -- the e2e shim never prints
limit text, so the existing seven cases must still pass unchanged.

Out of scope, each with its own issue or deliberately dropped: near-limit
warnings (`80% of your usage`); account rotation; scanning the planner's
pane; cost tracking (#142); a general screen-state classifier (#126, #135);
confirm-before-retry (#126).

## 2. File structure

```
internal/harness/harness.go        Harness.LimitPatterns (new field, defaults per kind)
internal/harness/harness_test.go   every known kind has >= 1 pattern; each compiles
internal/candidate/candidate.go    Candidate.LimitPatterns (json:"limit_patterns"); compile-checked in Load
internal/candidate/candidate_test.go  bad regex refused; good one kept
internal/policy/policy.go          Policy.LimitGateDefaultMS (json:"limit_gate_default_ms"); LimitGateDefault()
internal/policy/policy_test.go     nil -> 1h; <= 0 refused
internal/relay/limit.go            limitPatterns, matchLimit, parseReset, gateOnLimit (new)
internal/relay/limit_test.go       matcher and parser over fixtures; gateOnLimit bookkeeping
internal/relay/switch.go           switchBuilder gains `counted bool`; gated callers pass false
internal/relay/headless.go         three decision points call gateOnLimit
internal/relay/reconcile.go        two decision points call gateOnLimit
internal/relay/headless_test.go    exit+match switches uncounted; budget+match switches; exit+report+match gates and closes unmarked
internal/relay/reconcile_test.go   quiescent+match switches; quiescent+report+match gates and closes unmarked
internal/relay/reconcile_blocked_test.go  timeout+match switches, no halt; timeout without match halts as before
internal/relay/switch_test.go      TestGatedSwitchesAtOnce: RoundSwitches stays 0
README.md                          `limit_patterns` (candidates), `limit_gate_default_ms` (policy), one paragraph under switching
```

## 3. Data structures

### 3.1 `harness.Harness.LimitPatterns []string`

Default regexes for the text this harness prints when its provider closes
the session on quota. Compiled with Go's `regexp`; every default must
compile (a test enforces it). Case-insensitivity is written into the
pattern (`(?i)`), not applied by the matcher. Defaults:

| kind | patterns | provenance |
|---|---|---|
| `agy` | `(?i)individual quota reached`, `(?i)RESOURCE_EXHAUSTED`, `(?i)quota exceeded` | first one observed 2026-09-12 in `history.json` ("Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s."); the others from Google API error strings, unverified against a pane |
| `claude` | `(?i)you've hit your .*limit`, `(?i)usage limit reached`, `(?i)rate limit reached`, `(?i)limit .*resets` | Claude Code's own limit banner and API error text; unverified against a pane |
| `opencode` | `(?i)rate.?limit(ed)? (reached\|exceeded)`, `(?i)quota (exceeded\|reached)`, `(?i)insufficient (credits\|quota)`, `(?i)RESOURCE_EXHAUSTED` | OpenRouter 429/402 bodies and the Google strings opencode relays; unverified against a pane |

"Unverified" is recorded in the source comment the way `SubAgents` records
its observation; the first real pane that hits a limit should replace the
comment with the observed line.

### 3.2 `candidate.Candidate.LimitPatterns []string` (`json:"limit_patterns,omitempty"`)

Extra patterns for this candidate, appended after the harness defaults.
Extend-only: a candidate cannot remove a default (a default that
false-positives is a bug to fix in `internal/harness`, not to configure
around). `candidate.Load` compiles each one and refuses the file on the
first that does not compile: `candidates <path>: candidate <i>:
limit_patterns[<j>]: <regexp error>`.

### 3.3 `policy.Policy.LimitGateDefaultMS *int` (`json:"limit_gate_default_ms,omitempty"`)

How long a matched limit gates the provider when no reset time can be
parsed from the matched line. `nil` -> `DefaultLimitGate = 1h`; a present
value must be `> 0` or `Load` refuses with `ErrBadPolicy`. Accessor
`func (p Policy) LimitGateDefault() time.Duration`.

### 3.4 `relay.LimitMatch`

```
type LimitMatch struct {
    Line   string        // the matched line, trimmed, capped at 200 runes
    Until  time.Time     // gate end, UTC
    Parsed bool          // Until came from Line, not from the default
}
```

### 3.5 Ledger entry written on match

```
Kind: RateLimited   Subject: <provider of b.BuilderCandidate>
At: now             Until: match.Until
Note: match.Line    Source: "relay"    Binding: b.Name
```

Committed through `appendEntryLocked` (the caller holds the store lock in
every decision point -- all of them run inside `Reconcile`'s transaction),
so the history mirror is written too. A write failure is printed to stderr
and dropped, exactly like `recordSpawnFailureLocked`: bookkeeping never
masks the switch that follows.

## 4. Contracts

### 4.1 `limitPatterns(rt Runtime, token string) []*regexp.Regexp`

Harness defaults for the token's kind followed by the candidate's own
`limit_patterns`. Empty when the token does not resolve to a configured
candidate (an adopted builder) -- and then no decision point matches
anything. Compiled on each call; decision points fire at most once per
round, so caching buys nothing.

### 4.2 `matchLimit(text string, patterns []*regexp.Regexp, now time.Time, fallback time.Duration) (LimitMatch, bool)`

Pure. Scans `text` line by line from the **last** line backwards and
returns the first (i.e. most recent) line any pattern matches. `Until` is
`parseReset(line, now)` when that succeeds, else `now.Add(fallback)`.
`ok=false` when no line matches or `patterns` is empty. Never errors.

### 4.3 `parseReset(line string, now time.Time) (time.Time, bool)`

Pure. Two forms, tried in this order, on the matched line only:

1. **Duration** -- `(?i)(?:resets?|try again|retry)\s+(?:in|after)\s+~?((?:\d+\s*(?:h|hr|hours?|m|min|minutes?|s|sec|seconds?)\s*)+)`; the
   captured group is summed component by component
   (`2h48m52s`, `5 min`, `30s`). Result `now + d`.
2. **Clock** -- `(?i)resets?\s+(?:at\s+)?~?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?`;
   built in `now.Location()` on `now`'s date; if the result is not after
   `now`, add 24h. `7pm` -> 19:00; `00:26` -> 00:26 next day when now is
   23:13.

Either result must lie in `(now, now+24h]`; anything else is `ok=false`
(garbage in a line that happened to match the limit pattern must not gate
a provider for a week). The returned time is UTC.

### 4.4 `gateOnLimit(ctx, rt Runtime, tx *store.Tx, b store.Binding, text string, closeOld bool) (next store.Binding, m LimitMatch, handled bool, err error)`

The one helper every decision point calls. Preconditions: the round is
open and the caller holds the store lock. It applies the `switchable` guard
itself (`b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()`, the same
one the existing triggers use) and returns `handled=false` without reading
the ledger when it fails -- an adopted builder is never gated by relay, and
no call site has to repeat the check.

Flow:

```
if !switchable: return b, LimitMatch{}, false, nil
patterns := limitPatterns(rt, b.BuilderCandidate)
m, ok := matchLimit(text, patterns, rt.Now(), rt.Policy.LimitGateDefault())
if !ok: return b, LimitMatch{}, false, nil         -- caller continues its own path
appendEntryLocked(rate_limited entry from §3.5)    -- failure printed, dropped
slog.Warn("provider rate-limited", binding, round, provider, until, parsed, line)
if headless: appendLogMarker(log, now, "rate-limited: "+m.Line)
if report exists for this round:
    return b, m, false, nil                        -- gate recorded; caller closes the round as today
next, err := switchBuilder(ctx, rt, tx, b, "rate-limited: "+m.Line, closeOld, counted=false)
return next, m, true, err
```

`handled=true` means the caller returns `next, err` as a switch tick
(no `deliverAndSettle`). `handled=false` with `m.Line != ""` means "the
gate is on the ledger, now close the round as you would have" -- a report
written before the limit is delivered `unmarked` rather than discarded,
with one sentence appended to the payload (`Provider rate-limited:
<line>; gated until <HH:MM local>.`), and the *next* round picks past the
gated provider. `m` is the zero value whenever nothing matched.

### 4.5 `switchBuilder(..., reason string, closeOld, counted bool)`

New trailing parameter. `counted=false` skips `b.RoundSwitches++` on the
success path and on the spawn-failure path (a gated provider's failed
replacement is still not the builder's fault). The `>= limit` check at the
top is unchanged. Callers: the two `gatedBuilder` triggers and
`gateOnLimit` pass `false`; the gone trigger and the headless exit path
pass `true`.

## 5. Decision points

| mode | point | text | `closeOld` | on no match |
|---|---|---|---|---|
| headless | process exited, no report (`headless.go` "Exited without a report") | `logTail(LogPath, 40)` | `false` | as today: exit entry, counted switch |
| headless | process exited, report, no marker | `logTail(LogPath, 40)` | -- (report present, never switches) | as today: `unmarked` close |
| headless | alive past the round budget (`checkRoundTimeout` about to halt) | `logTail(LogPath, 40)` | `true` | as today: halt |
| pane | quiescent after the nudge, no report (`handleIdleBuilder` before `scrapeReport`) | `ReadAgent(Target, scrapeLines)` | `true` | as today: scrape |
| pane | quiescent after the nudge, report, no marker | same read | -- | as today: `unmarked` close |
| pane | working past the round budget (`default:` branch, `checkRoundTimeout` about to halt) | `ReadAgent(Target, scrapeLines)` | `true` | as today: halt |

Rules that hold at every point:

- **Once per transition.** The budget points run the scan only on the tick
  that would notify (`b.HaltNotifiedRound != b.Round`); a binding already
  halted is not rescanned every tick. The quiescent and exit points already
  run once per round by construction.
- **Read failures fall through.** A `ReadAgent` error at a pane point is
  logged at Warn and the point proceeds as if no match (the existing
  behaviour); `logTail` never errors.
- **The exit log entry still gets written** before the headless exit point
  scans, so `relay log` shows the exit and then the switch, in that order.
- **Ordering inside the tick is unchanged**: `gatedBuilder` (a gate already
  on the ledger) still runs before the liveness and idle checks, so a gate
  recorded by this tick's match is acted on by this tick's `switchBuilder`
  and never re-triggers on the next.

## 6. Error handling

| failure | behaviour |
|---|---|
| a default pattern does not compile | `TestLimitPatternsCompile` fails; cannot ship |
| a `candidates.json` pattern does not compile | `candidate.Load` refuses the file (startup error, every subcommand) -- the same rule as any other invalid candidate |
| `limit_gate_default_ms <= 0` | `policy.Load` refuses with `ErrBadPolicy` |
| ledger/history write fails on match | stderr line, switch proceeds (§4.4) |
| screen read fails at a pane point | Warn, no match, existing path |
| reset time parses outside `(now, now+24h]` | treated as unparsed: default window, `Parsed=false` |
| replacement spawn fails after a match | `switchBuilder`'s existing path: `spawn_failed` recorded, binding BROKEN, next tick walks on; `RoundSwitches` not advanced |
| every candidate gated after a match | `switchBuilder` halts with "cannot switch" -- the binding is NEEDS YOU with the ledger showing why |

A false positive (a builder that printed limit-shaped text and then died
for another reason) costs one gate and one switch, both visible in
`relay policy` and `relay log`, and `relay available <provider>` undoes the
gate. That bound is why the scan runs only where the round already stopped.

## 7. Observability

- `slog.Warn("provider rate-limited", ...)` with `binding`, `round`,
  `provider`, `until`, `parsed`, `line` on every match.
- Headless log marker `relay: rate-limited: <line>` before the existing
  `switched to ...` marker.
- The switch log entry's note reads `switched builder (rate-limited: <line>): ...`,
  the same shape the manual gate already produces.
- `relay policy` shows the gate with `source relay` and the matched line as
  the note; `relay status`'s gated block likewise.

## 8. Testing

All pure or fake-backed, in the packages named in §2; no test under
`cmd/relay`; nothing reaches herdr.

- `matchLimit`: last matching line wins; empty patterns never match; the
  agy fixture from `history.json` matches and parses `2h48m52s`; a
  `resets 7pm` line parses to today/tomorrow 19:00 in the fixture's zone;
  a `resets ~00:26` line at 23:13 parses to next-day 00:26; a line with
  no time uses the fallback with `Parsed=false`; `try again in 400h` falls
  back (out of range).
- `gateOnLimit`: match -> one `rate_limited` entry with `source relay`,
  `binding` set, `until` as parsed; no match -> ledger untouched, `handled`
  false; match with report present -> entry written, `handled` false, no
  switch.
- Headless: exit + limit text in the log -> switch, `RoundSwitches == 0`,
  ledger gated, log marker present; exit without limit text -> today's
  counted switch (existing test unchanged); budget + limit text -> Kill,
  switch, no halt notice; budget without -> halt as today; exit + report +
  limit text -> `unmarked` close, ledger gated, no switch.
- Pane: quiescent + limit text on screen -> ClosePane, switch, no scrape;
  quiescent + report + limit text -> `unmarked` close, gated; timeout +
  limit text -> switch, `notices` carries the switch not a halt; timeout
  without -> halt (existing test).
- Counting: `TestGatedSwitchesAtOnce` and
  `TestReconcileHeadlessGatedKillsAndSwitches` now expect
  `RoundSwitches == 0`; `TestMaxSwitchesZeroHalts` still halts.
- Mutations named in the plan: drop the `counted` guard (the two
  RoundSwitches tests fail); scan before the report check in `gateOnLimit`
  (the report-present tests fail); return the first rather than the last
  matching line (the last-line test fails).
