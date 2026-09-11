# Availability ledger: what relay ran into, what the planner declared

**Issue:** #61, step 1 of 7
**Depends on:** #80 (candidates; landed in #81–#83)
**Amends:** nothing. Adds one state file and reads it from `status`, `doctor`,
`candidates`, and the four spawn paths.

## 1. System overview

#61 wants relay to pick which candidate runs a role. Its hard gates --
"unavailable if the binary is missing, the role definition is missing, a
spawn failed recently, or the provider is rate-limited and the cooldown has
not expired" -- need somewhere to look. Two of those facts `doctor` probes
live. The other two happen at a moment: a `StartAgent` call fails, or a
builder prints "usage limit reached" and stops. If nobody writes them down
when they happen, nothing can read them later.

The **ledger** is that record: a list of events, per candidate or per
provider, each with a timestamp and an optional expiry. This step makes
relay *keep* it and *show* it. Nothing reads it to decide anything --
refusing a gated candidate is #61 step 4, with the rest of the hard gates.

Two constraints from the brainstorm shape every rule below:

1. **Relay records only what it observed or was told.** A `spawn_failed`
   entry is written by relay when `StartAgent` returns an error -- a fact.
   A `rate_limited` entry is written by the planner through
   `relay unavailable`, because relay cannot tell a rate-limited builder
   from any other idle one without reading its screen, and reading screen
   content for meaning is a judgement relay does not make. Screen-text
   detection may come in step 7; it is not here.
2. **A rate limit gates a provider, not a model.** Quotas are enforced per
   subscription or key. `relay unavailable claude/anthropic/sonnet` takes a
   token because that is what the planner has in hand, but the entry's
   subject is `anthropic`, and every candidate whose `provider` is
   `anthropic` shows as gated. A `spawn_failed` gates only the token that
   failed.

`spawn_failed` expires on its own after a fixed ten minutes: a spawn
failure is nearly always transient (a pane race, a binary mid-upgrade). A
`rate_limited` entry expires only when `--for` said so or when the planner
runs `relay available`: relay does not know any provider's reset schedule,
and a guessed default that expires early sends the next round straight back
into the limit.

### Scope boundary

In scope: the `ledger` package; appends from the four spawn paths;
`relay unavailable` and `relay available`; the `Gated` view; rendering in
`status`, `candidates`, `doctor`; the advisory line at spawn time.

Out of scope, unchanged:

- Refusing, scoring, or reordering candidates. `resolveCandidate` does not
  read the ledger. A planner who binds a gated candidate gets one line on
  stderr and a running builder.
- `policy.json`, per-provider cooldowns, `max_switches` (step 2).
- Peak-hour history and quality counters (step 7). The ledger's `at`
  timestamps are that step's raw material; nothing aggregates them here.
- The reconciler, held delivery, consults, reap. None of them read or write
  the ledger.
- `doctor`'s binary and role probes. They stay live probes and are not
  ledger entries: a fresh probe beats a record of one.

## 2. File structure

```
internal/ledger/ledger.go            Entry, Kind, Ledger, Load, Save, Append, Clear, Prune, Gated   (new package)
internal/ledger/ledger_test.go       round-trip, prune, provider gating, clear
internal/store/store.go              LedgerPath(); ledger lives beside .lock under the same flock
internal/relay/herdr.go              Runtime.LedgerPath, set from store.LedgerPath() by newRuntime
internal/relay/ledger.go             recordSpawnFailure, Unavailable, Available, gatedNote  (thin wrappers over ledger under WithLock)
internal/relay/ledger_test.go        spawn paths append; Unavailable resolves provider; Available clears
internal/relay/bind.go               resolveBuilder: append spawn_failed on StartAgent error; advisory note
internal/relay/ask.go                phase 2: append spawn_failed on StartAgent error; advisory note
internal/relay/status.go             Report.Gated; RenderStatus prints the candidates block
internal/relay/candidates_list.go    FormatCandidates takes gates; trailing "unavailable:" column
internal/relay/status_test.go, candidates_list_test.go
cmd/relay/main.go                    cmdUnavailable, cmdAvailable, dispatch, help
cmd/relay/doctor.go                  ledger rows per gated candidate
cmd/relay/doctor_test.go             ledgerChecks as a pure function
README.md, CLAUDE.md                 "Availability" section; dispatch paragraph gains `relay unavailable`
```

## 3. Data structures and type definitions

### 3.1 `ledger.Kind`

```
type Kind string
const (
    SpawnFailed Kind = "spawn_failed"   // subject is a candidate token
    RateLimited Kind = "rate_limited"   // subject is a provider
)
```

A closed set. Each kind fixes what its `Subject` means; a new kind is a
spec change, not a config knob.

### 3.2 `ledger.Entry`

```
type Entry struct {
    Kind    Kind      `json:"kind"`
    Subject string    `json:"subject"`            // token (SpawnFailed) or provider (RateLimited)
    At      time.Time `json:"at"`
    Until   time.Time `json:"until,omitempty"`    // zero = until cleared
    Note    string    `json:"note,omitempty"`     // the StartAgent error, or --reason
    Source  string    `json:"source"`             // "relay" | "planner"
    Binding string    `json:"binding,omitempty"`  // which binding hit it; absent for planner entries
}

func (e Entry) Expired(now time.Time) bool   // !Until.IsZero() && !now.Before(Until)
```

| field | required | constraint |
|---|---|---|
| `Kind` | yes | one of the two constants |
| `Subject` | yes | non-empty; a parseable `candidate.Ref` string for `SpawnFailed`; a single segment for `RateLimited` |
| `At` | yes | non-zero |
| `Until` | no | zero or after `At` |
| `Source` | yes | `relay` or `planner` |

### 3.3 `ledger.Ledger`

```
type Ledger struct {
    Entries []Entry `json:"entries"`
}

func Load(path string) (Ledger, error)             // missing file = empty ledger, nil error
func Save(path string, l Ledger) error             // write-then-rename, like bind.json
func (l Ledger) Prune(now time.Time) Ledger        // drops Expired entries; pure
func (l Ledger) Append(e Entry) Ledger             // pure; no dedupe -- two spawn failures are two events
func (l Ledger) Clear(kind Kind, subject string) Ledger   // pure; drops every entry matching both
```

The file is `$XDG_STATE_HOME/relay/ledger.json` (`store.LedgerPath()`),
beside `.lock`. Every mutation runs inside `store.WithLock`, so a `bind`
appending a failure and a planner running `relay unavailable` in another
pane serialise on the flock that already serialises `bind.json` writes.
Readers (`status`, `candidates`, `doctor`) load without the lock, as
`Store.List` does: a torn read is impossible because `Save` renames.

### 3.4 `ledger.Gate`

```
type Gate struct {
    Token  string     // the gated candidate, canonical ref
    Kind   Kind
    Since  time.Time  // Entry.At
    Until  time.Time  // zero = until cleared
    Note   string
    Source string
}

func Gated(l Ledger, refs []string, providerOf func(string) string, now time.Time) []Gate
```

Pure. `refs` are the configured candidate tokens (`Set.Refs()`);
`providerOf` maps a token to its provider (`ParseRef(...).Provider`). For
each live (non-expired) entry: a `SpawnFailed` entry yields one gate for
its subject if the subject is in `refs`; a `RateLimited` entry yields one
gate for **every** ref whose provider equals the subject. Output is sorted
by token, then by `Since`. A candidate can carry several gates; the
renderer shows them all.

### 3.5 `relay.Report` (modified)

```
Gated []ledger.Gate `json:"gated,omitempty"`
```

Populated by `Status` from the ledger and `rt.Candidates`. Absent from JSON
when nothing is gated, so a consumer that never learned the field sees
the document it always did.

### 3.6 `relay.Runtime` (modified)

```
LedgerPath string   // rt.Store.LedgerPath(); set by newRuntime, and by tests to a temp path
```

### 3.7 `relay.AskResult` (modified)

```
Candidate string   // canonical token the consult was started from, for the CLI's gatedNote
```

`Bind`, `Add` and `Fork` already expose the token through
`Binding.BuilderCandidate`; `ask` did not, and the note in §4.4 needs it.

## 4. Interface definitions and component contracts

### 4.1 `relay.recordSpawnFailure` (new, `internal/relay/ledger.go`)

```
func recordSpawnFailure(rt Runtime, token, binding string, cause error)
```

Appends `{SpawnFailed, token, now, now+SpawnFailedCooldown, cause.Error(),
"relay", binding}` under `WithLock`, pruning first. **Never returns an
error**: a failed bookkeeping write must not mask the spawn error the
caller is about to return. A write failure is printed to stderr as
`relay: could not record spawn failure: <err>` and dropped.
`SpawnFailedCooldown = 10 * time.Minute`, a package constant with a
comment saying why it is not config (step 2 may move it).

Call sites, both on the `StartAgent` error path and nowhere else:
- `bind.go` `resolveBuilder`, before `return store.Endpoint{}, "",
  fmt.Errorf("start builder ...")`. Covers `bind`, `add`, `fork`.
- `ask.go` phase 2, in the `StartAgent` error branch, before the consult is
  marked `ConsultSilent`.

A `builderPane` / `consultPane` (split) failure is **not** a spawn failure:
no agent was asked to start, and the pane split says nothing about the
candidate.

### 4.2 `relay.Unavailable`

```
func Unavailable(rt Runtime, token string, until time.Time, reason string) (provider string, err error)
```

Pre: `token` parses and is in `rt.Candidates` (so a typo is refused with
`candidate.ErrUnknownCandidate`, not recorded). Post: one `RateLimited`
entry for `ParseRef(token).Provider` with `Source: "planner"`, `Note:
reason`, `Until: until` (zero when the caller passed none). Returns the
provider so the CLI can say what it gated. Under `WithLock`, pruning first.

### 4.3 `relay.Available`

```
func Available(rt Runtime, subject string) (provider string, removed int, err error)
```

`subject` is a token or a bare provider: if it parses as a ref, its
provider is used; otherwise it is taken as a provider name. Clears every
`RateLimited` entry for that provider and returns how many. Zero removed is
not an error -- the CLI prints `nothing was gating <provider>`. Under
`WithLock`.

### 4.4 `relay.gatedNote`

```
func gatedNote(rt Runtime, token string) string
```

Pure over a fresh `Load` (no lock). Returns `""` when the token has no live
gate; otherwise one line:

```
note: <token> is gated: <kind> since HH:MM (until HH:MM | until cleared)[: <note>]; proceeding
```

Multiple gates join with `; `. Called by `cmdBind`, `cmdAdd`, `cmdFork`,
`cmdAsk` **after** the spawn succeeds (they know the canonical token from
the result) and printed to stderr. Advisory only: the spawn already
happened. A planner that wants a refusal waits for step 4.

### 4.5 `relay.Status` (modified)

After assembling bindings: `l, _ := ledger.Load(rt.LedgerPath)` (a load
error is logged to stderr and treated as empty -- status must not fail on
bookkeeping), then `rep.Gated = ledger.Gated(l.Prune(now), rt.Candidates.Refs(), providerOf, now)`.

### 4.6 `relay.RenderStatus` (modified)

After the bindings (and before the `done` footer), when `len(r.Gated) > 0`:

```
candidates
  agy/google/gemini-3.8-flash-high        spawn failed  14:02  until 14:12  herdr: agent start: exit 1  (cand-a)
  claude/anthropic/opus                   rate-limited  15:30  until cleared  5-hour window hit
  claude/anthropic/sonnet                 rate-limited  15:30  until cleared  5-hour window hit
```

Times are local `HH:MM`; `until cleared` for a zero `Until`; the binding in
parentheses when present. With zero bindings and some gates, the block
prints after `no bindings`. Absent entirely when nothing is gated.

### 4.7 `relay.FormatCandidates` (signature change)

```
func FormatCandidates(set *candidate.Set, gates []ledger.Gate) string
```

A gated row gains a trailing `   unavailable: <kind> until HH:MM|cleared`.
`cmdCandidates` computes gates the same way `Status` does.

### 4.8 `doctor` rows

```
func ledgerChecks(gates []ledger.Gate) []doctor.Check
```

Pure, in `cmd/relay/doctor.go`, tested there. One `SevWarn` row per gate,
`Group` = the token's harness, `Name` = `ledger`, `Detail` = `<token>:
<kind> since HH:MM (<until>)`, `Fix` = `relay available <provider>` for
`RateLimited`, `wait until HH:MM` for `SpawnFailed`. `cmdDoctor` appends
them after `doctor.Run`. They do not count as failures (`rep.Failures()`
is unchanged), so a gated provider does not fail the exit code.

### 4.9 CLI

| command | behaviour |
|---|---|
| `relay unavailable <token> [--for D] [--reason S]` | `Unavailable`; prints `gated <provider> (<n> candidates) until HH:MM|cleared` |
| `relay available <provider\|token>` | `Available`; prints `cleared <provider> (<n> entries)` or `nothing was gating <provider>` |
| `relay status` | §4.6 |
| `relay candidates` | §4.7 |
| `relay doctor` | §4.8 |
| `bind`/`add`/`fork`/`ask` | §4.4 note after a successful spawn |

`--for` is a Go duration (`2h`, `90m`). A zero or negative `--for` is
refused. Neither new command reaches herdr; both are testable through
`internal/relay` on a temp ledger path.

## 5. High-level pseudocode

### 5.1 `resolveBuilder`, spawn path (bind/add/fork)

```
… resolveCandidate, Launch, builderPane as today …
if err := StartAgent(...); err != nil:
    recordSpawnFailure(rt, c.Ref().String(), name, err)     -- never fails the caller
    return Endpoint{}, "", fmt.Errorf("start builder %q: %w", agentName, err)
```

### 5.2 `relay unavailable`

```
parse flags; token := positional
until := zero; if --for set: until = now + D (D > 0 else usage error)
provider, err := Unavailable(rt, token, until, reason)
print "gated <provider> (<count of rt.Candidates with that provider> candidates) until …"
```

### 5.3 `Gated`

```
live := l.Prune(now).Entries
for e in live:
    switch e.Kind:
      SpawnFailed: if e.Subject in refs: gates += Gate{Token: e.Subject, …}
      RateLimited: for ref in refs: if providerOf(ref) == e.Subject: gates += Gate{Token: ref, …}
sort gates by (Token, Since)
```

### 5.4 `Prune` on every write

```
WithLock:
    l := Load(path); l = l.Prune(now); l = l.Append(e) | l.Clear(...); Save(path, l)
```

So the file holds live entries plus whatever expired since the last touch;
there is no daemon tick for it.

## 6. Error handling strategy

| error | where | recoverable |
|---|---|---|
| `candidate.ErrUnknownCandidate` | `Unavailable` with a token not in the set | yes: fix the token |
| `candidate.ErrBadRef` | `Unavailable` with a malformed token | yes |
| `ledger.ErrBadEntry` (new) | `Load` on an entry violating §3.2 | yes: the message names the index; the file is hand-editable |
| usage error | `--for` ≤ 0; missing positional | yes |

A `Load` error in a **reader** (`status`, `candidates`, `doctor`,
`gatedNote`) is printed to stderr once and treated as an empty ledger: a
malformed bookkeeping file must not take `status` down. A `Load` error in
a **writer** (`Unavailable`, `Available`) is returned: the planner asked
for a change and should know it did not happen. `recordSpawnFailure`
is the exception in the writer set -- it swallows and prints, because its
caller is already returning the real error.

No new `log.jsonl` events. The ledger is its own audit trail.

## 7. Ordered implementation steps

Each step is one plan / one builder session; `make check` green at every
step; a builder that finds a step impossible as written halts.

1. **`ledger` package.** §3.1–3.4: `Kind`, `Entry`, `Expired`, `Ledger`,
   `Load`, `Save`, `Prune`, `Append`, `Clear`, `Gated`, `ErrBadEntry`.
   `store.LedgerPath()`. Tests: round-trip through a temp file; missing
   file is empty; `Prune` drops exactly the expired; `Gated` gates every
   candidate of a rate-limited provider and only the failed token for a
   spawn failure; sorted output; `Clear` scoped to kind+subject.

2. **Runtime + writers.** §3.6, §4.1–4.3: `Runtime.LedgerPath`,
   `recordSpawnFailure` wired into `resolveBuilder` and `ask` phase 2,
   `Unavailable`, `Available`. Tests in `internal/relay`: `fakeHerdr.startErr`
   on bind, add, fork, ask each leave one `spawn_failed` entry with the
   right token and binding and the original error still returned; a split
   failure leaves none; `Unavailable` records the provider and refuses an
   unknown token; `Available` by token and by provider.

3. **Readers.** §3.5, §4.4–4.8: `Status.Gated`, `RenderStatus` block,
   `FormatCandidates` column, `ledgerChecks`, `gatedNote`. Renderer tests
   on fixed inputs; `ledgerChecks` in `cmd/relay/doctor_test.go` as a pure
   function.

4. **CLI + docs.** §4.9: `cmdUnavailable`, `cmdAvailable`, the note after
   spawn in the four commands, help text. README gains an "Availability"
   section under "Candidates"; CLAUDE.md's dispatch paragraph says to run
   `relay unavailable <token>` when a builder reports a usage limit, before
   moving down the list. No `cmd/relay` test reaches herdr.

Planner verification across the change: `make check`; `git diff --stat`
against §2; mutation -- make `Gated` ignore the provider match and confirm
the provider-gating test fails; on this machine, `relay unavailable
claude/anthropic/sonnet --reason test` then `relay status`, `relay
candidates`, `relay doctor`, then `relay available anthropic`.
