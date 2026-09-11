# Policy order: relay picks the first ungated candidate in the planner's order

**Issue:** #61, step 2 of 7 (re-cut; see §1 "Why this is not #61's step 2 as written")
**Depends on:** #80 (candidates, landed in #81–#83); #61 step 1 (ledger, landed in #86)
**Amends:** `docs/specs/2026-09-11-candidates-design.md` §4.3 (the resolve rule);
`docs/specs/2026-09-11-availability-ledger-design.md` §1 "Scope boundary" first
bullet ("`resolveCandidate` does not read the ledger" -- it now does); README
"Choosing a candidate" and "Availability"; CLAUDE.md "Dispatching work to
builders"

## 1. System overview

Today the harness order lives in one paragraph of `CLAUDE.md` addressed to
the planner, and the planner walks it by hand: bind `agy`, see it is
rate-limited in `relay status`, run `relay unavailable`, bind `claude`
instead, naming each token explicitly because with three builder candidates
`resolveCandidate` refuses an omitted `--builder`.

This design moves the order into relay and lets relay walk it. A new
`~/.config/relay/policy.json` holds `order[role]`, the planner's preferred
candidate order per role. `resolveCandidate` reads it together with the
ledger's gates: an omitted token now means **the first candidate in
`order[role]` that is not gated**, and relay refuses only when every
candidate serving the role is gated. An explicit token still bypasses
everything, with the advisory note step 1 added.

Two of #61's principles bind this step:

1. **Every pick is explained.** Each resolution writes one `pick` entry to
   the binding's `log.jsonl` naming what was picked, why (order position,
   sole candidate, unlisted fallback, explicit bypass), and every gated
   candidate it passed over. `bind`/`add`/`fork`/`ask` print the same line
   to stderr. `relay policy` shows, per role, what the resolver would pick
   right now and why, computed by the resolver itself so the two cannot
   disagree.
2. **The planner's order wins when it is set.** Relay orders only where the
   planner has ordered. A role with no `order` entry and two or more
   candidates still refuses, as today. That refusal is the seam #61 step 3's
   scorer fills; nothing here pre-empts it with an arbitrary default.

### Why this is not #61's step 2 as written

#61 step 2 lists candidate metadata (cost, strengths) and a `policy.json`
with `weights`, `providers[].peak`, `order[role]`, `floor`, `max_switches`,
`cooldown`, plus a loader, daemon hot-reload and `relay policy set`. All but
`order` have no reader until steps 3–7, and `cooldown` duplicates the
ledger's per-entry `Until` and `--for`. This step ships only `order[role]`
and wires it into the four spawn paths -- which is #61 step 4 in its
degenerate, order-only form. Step 3 (scorer) then slots in *behind* the
order for roles the planner has not ordered, which is the precedence #61
already specifies. Each deferred knob is added by the step that reads it.

### Scope boundary

In scope: the `policy` package; `Runtime.Policy`; `resolveCandidate` reading
policy and gates and returning a `Resolution`; the `pick` log entry; the
stderr line; `relay policy`; `doctor` policy rows; docs.

Out of scope, unchanged:

- `weights`, `floor`, `providers[].peak`, `max_switches`, cost and strength
  metadata, the scorer, `relay policy explain` (#61 steps 3, 5, 7).
- `cooldown`: dropped. The ledger's `Until` is the cooldown.
- `relay policy set`: not added. The file is the interface; a setter earns
  its place when there is a second key.
- Daemon hot-reload: the daemon does not resolve candidates until step 6
  (mid-round switching). Every CLI invocation loads the file fresh.
- The remaining hard gates (`binary missing on PATH`, `role definition
  missing`): still `doctor`'s live probes. A missing binary fails
  `StartAgent`, which records `spawn_failed`, which gates the candidate for
  the next resolution -- self-correcting one bind late.
- Ledger writers and readers, `status`, `candidates`: unchanged.

## 2. File structure

```
internal/policy/policy.go               Policy, Load, Order, ErrBadPolicy          (new package)
internal/policy/policy_test.go          load matrix: missing file, bad JSON, unknown role, bad token, duplicate
internal/store/log.go                   KindPick
internal/relay/herdr.go                 Runtime.Policy
internal/relay/candidate.go             Resolution, How, Skip, resolveCandidate (new signature), ErrAllGated,
                                        ExplainResolution, pickEntry, CandidateKind (passes policy + gates)
internal/relay/candidate_test.go        resolver table incl. gates, order, unlisted, all-gated, no-order refusal
internal/relay/bind.go                  BindResolved; Bind wraps it; resolveBuilder returns Resolution; pick entry
internal/relay/add.go                   AddResult.Resolution; pick entry
internal/relay/fork.go                  ForkResult.Resolution; InheritedFrom; pick entry
internal/relay/ask.go                   AskResult.Resolution; pick entry
internal/relay/bind_test.go, add_test.go, fork_test.go, ask_test.go   pick entry present; gated skip; bypass
internal/relay/policy_view.go           PolicyWarnings, FormatPolicy
internal/relay/policy_view_test.go      fixed-input rendering; warnings matrix
cmd/relay/main.go                       newRuntime loads policy.json; cmdPolicy; dispatch + help;
                                        cmdBind/cmdAdd/cmdFork/cmdAsk print ExplainResolution
cmd/relay/doctor.go                     policyChecks
cmd/relay/doctor_test.go                policyChecks as a pure function
README.md                               "Policy" section; "Choosing a candidate" and "Availability" rewritten
CLAUDE.md                               "Dispatching work to builders" rewritten
docs/design.md                          Candidates (config) paragraph mentions policy.json
```

## 3. Data structures and type definitions

### 3.1 `policy.Policy`

```
type Policy struct {
    Order map[string][]string `json:"order,omitempty"`   // role -> candidate tokens, most preferred first
}

func Load(path string) (Policy, error)
func (p Policy) OrderFor(role string) []string // nil when the role has no entry; returns a copy
```

The file:

```json
{
  "order": {
    "builder": ["agy/google/gemini-3.8-flash-high",
                "claude/anthropic/sonnet",
                "opencode/openrouter/z-ai/glm-5.3-flash"]
  }
}
```

| field | required | constraint |
|---|---|---|
| `order` | no | object; absent or empty means no role is ordered |
| `order.<role>` | -- | key must be in `harness.RoleNames()`; value a non-nil array of strings |
| `order.<role>[i]` | -- | must satisfy `candidate.ParseRef`; no duplicates within one list |

`Load` on a missing file returns the zero `Policy` and no error. It validates
**only the file's own shape** (the rows above). It does not open
`candidates.json`: an `order` entry that names a token not configured, or
one that does not serve the role, is *tolerated* by `Load`, skipped by the
resolver, and warned about by `relay policy` and `doctor` (§4.6). Removing
a candidate must never make every subcommand refuse to start.

### 3.2 `store.KindPick`

```
KindPick Kind = "pick"   // relay -> log only: which candidate was resolved for a spawn and why
```

Added to the `Kind` constants beside `KindFork`. A `pick` entry is always
`Confirmed: true` and `Direction: DirToPlanner`: an unconfirmed
`DirToPlanner` entry is relay's pending-delivery record, and a pick is never
a payload. No reader switches exhaustively on `Kind`
(`ui/fetch.go` and `main.go` test equality against specific kinds), so the
new value is additive.

### 3.3 `relay.Resolution`

```
type How string
const (
    HowExplicit How = "explicit"   // token named by the planner (or inherited by fork); gates not consulted
    HowSole     How = "sole"       // the only candidate serving the role, ungated
    HowOrder    How = "order"      // taken from order[role]; Position is its 1-based index in that list
    HowUnlisted How = "unlisted"   // serves the role, not in order[role]; reached after every listed one was gated
)

type Skip struct {
    Token string
    Kind  ledger.Kind
    Until time.Time     // zero = until cleared
}

type Resolution struct {
    Candidate     candidate.Candidate
    How           How
    Position      int      // 1-based index in order[role] when How == HowOrder; 0 otherwise
    Skipped       []Skip   // gated candidates passed over, in the order they were passed; nil for HowExplicit
    Gates         []Skip   // for HowExplicit only: live gates on the named candidate, so the bypass is recorded
    InheritedFrom string   // fork: the source binding whose BuilderCandidate was inherited; "" otherwise
}

func (r Resolution) Token() string   // Candidate.Ref().String()
```

### 3.4 `relay.Runtime` (modified)

```
Policy policy.Policy   // loaded by newRuntime from ~/.config/relay/policy.json; zero value in tests that do not set it
```

A value, not a pointer: the zero `Policy` is a valid "nothing ordered" and
every existing test that builds a `Runtime` literal keeps compiling.

### 3.5 Result structs (modified)

```
AddResult.Resolution  Resolution
ForkResult.Resolution Resolution
AskResult.Resolution  Resolution      // AskResult.Candidate stays; it equals Resolution.Token()
```

`Bind` returns `store.Binding` at 48 call sites, so its signature is kept.
The implementation moves to `BindResolved` (§4.3) which returns the
`Resolution` too; `Bind` calls it and drops the second value. Only
`cmdBind` calls `BindResolved`.

## 4. Interface definitions and component contracts

### 4.1 `policy.Load`

```
func Load(path string) (Policy, error)
```

Pre: `path` composed by the caller through `userConfigRoot()`. Post: a
`Policy` satisfying §3.1, or an error wrapping `ErrBadPolicy` whose message
names the path, the role and the index (`policy.json: order.builder[2]:
duplicate token "claude/anthropic/sonnet"`). Missing file → zero value, nil.
Loaded at `newRuntime` after `candidate.Load`; a load error is fatal for
every subcommand, as for `candidates.json`.

### 4.2 `relay.resolveCandidate` (new signature)

```
func resolveCandidate(set *candidate.Set, pol policy.Policy, gates []ledger.Gate, token, role string) (Resolution, error)
```

Pure. `gates` is `Gates(rt)` (already pruned to live entries and projected
onto configured tokens). The rule, replacing candidates-design §4.3:

| `token` | `order[role]` | serving `role` | result |
|---|---|---|---|
| set | -- | -- | `ParseRef`, `Lookup`, must `Serves(role)` -- errors as today. `HowExplicit`; `Gates` = live gates on that token. Gates never refuse. |
| `""` | -- | 0, set empty | `ErrNoCandidates`, as today |
| `""` | -- | 0, set non-empty | `ErrRoleNotServed`, as today |
| `""` | -- | 1 | ungated → `HowSole`; gated → `ErrAllGated` |
| `""` | absent / empty | ≥2 | `ErrAmbiguousCandidate`; message gains `, or set order.<role> in ~/.config/relay/policy.json` |
| `""` | present | ≥2 | walk the ranked list (below); first ungated → `HowOrder` with `Position`, or `HowUnlisted`; `Skipped` = every gated one before it. None ungated → `ErrAllGated` |

**Ranked list** for `(set, pol, role)`, also used by `FormatPolicy`:

```
ranked := []
for i, tok in pol.OrderFor(role):
    c, ok := set.Lookup(tok); if !ok or !c.Serves(role): continue     -- tolerated; §4.6 warns
    ranked += (c, HowOrder, i+1)
for c in set.ForRole(role):           -- ref-sorted, as today
    if c not already in ranked: ranked += (c, HowUnlisted, 0)
```

A token is **gated** when any `Gate` in `gates` has `Token == tok`. One
`Skip` per gate, so a token with two gates contributes two `Skip`s.

Post: the returned `Resolution.Candidate` serves `role`. Unit-tested in
`internal/relay` with `gates` built by hand, never through a subcommand (CI
has no herdr).

### 4.3 Spawn paths

`resolveBuilder` returns `(store.Endpoint, Resolution, error)`; the adopt
branch returns a zero `Resolution` (`How == ""`), which every caller treats
as "no pick to record". Its `resolveCandidate` call passes `rt.Policy,
Gates(rt)`. The token written to `Binding.BuilderCandidate` is
`res.Token()` (`""` for adoption, as today).

```
func BindResolved(ctx, rt, opts) (store.Binding, Resolution, error)   // the implementation
func Bind(ctx, rt, opts) (store.Binding, error)                       // = BindResolved, resolution dropped
```

`Add` and `Fork` keep their early `resolveCandidate` call (a refused add or
fork must leave no worktree) and **that** is the `Resolution` they record
and return. They hand `resolveBuilder` the resolved token explicitly, so
`resolveBuilder`'s own resolution is always `HowExplicit` and is discarded
-- recording it would log every `add` as a policy bypass. The spawned
candidate is the same one either way.
`Fork` sets `res.InheritedFrom = opts.Source` when `opts.Candidate == ""`
and the source's `BuilderCandidate` was used; an inherited token is
`HowExplicit` (the planner's earlier choice), so a fork of a gated builder is
logged as a bypass rather than silently continuing.

`Ask` passes `rt.Policy, Gates(rt)`; `AskResult.Resolution` is set on every
return that today sets `AskResult.Candidate`, including the stranded
(`spawnErr != nil`) return.

`CandidateKind` (the bind preflight) passes `rt.Policy, Gates(rt)` and
returns `res.Candidate.Harness`.

### 4.4 The pick entry

```
func pickEntry(now time.Time, round int, role string, res Resolution) store.LogEntry
```

Pure: `{TS: now.UTC(), Round: round, Direction: DirToPlanner, Kind: KindPick,
Confirmed: true, Note: ExplainResolution(role, res)}`. Appended:

- `BindResolved`, fresh path: `rt.Store.Save(b)` becomes one `WithLock`
  doing `tx.Save(b)` then `tx.AppendLog(name, pickEntry(..., 1, "builder", res))`.
  Resume path: appended inside the existing `WithLock`, after `tx.Save`,
  at the binding's current round. Adoption (`res.How == ""`): no entry.
- `Add`: after the binding is saved, round 1.
- `Fork`: in the same transaction as `forkEntry`, after it, at `b.Round`.
- `Ask`: in the same transaction as the `KindAsk` entry, before it, at
  `consult.Round`, only when the consult reached `ConsultRunning` (a
  stranded consult has nothing to explain -- the ledger already holds the
  failure).

The pick entry is written **after** the spawn succeeded, never before: a
pick that did not lead to a running agent is not a pick.

### 4.5 `relay.ExplainResolution`

```
func ExplainResolution(role string, res Resolution) string
```

Pure; one line, no trailing newline; shared by the log entry, stderr and
tests. `skipText(s Skip)` is `<token> (<GateKindText> <GateUntilText>)`.

| `How` | text |
|---|---|
| `sole` | `picked <tok> for <role>: sole candidate` |
| `order` | `picked <tok> for <role>: order #<Position>` |
| `unlisted` | `picked <tok> for <role>: unlisted, after order` |
| `explicit`, no `InheritedFrom` | `picked <tok> for <role>: explicit, policy bypassed` |
| `explicit`, `InheritedFrom` | `picked <tok> for <role>: explicit, inherited from <src>, policy bypassed` |

When `Skipped` is non-empty: `; skipped <skipText>, <skipText>`. When
`How == explicit` and `Gates` is non-empty: `; gated: <kind> <until>, ...`.

`cmdBind`, `cmdAdd`, `cmdFork`, `cmdAsk` print `ExplainResolution` to stderr
after a successful spawn for every `How` except `explicit` and `""`. The
explicit case keeps today's `GatedNote` line and prints nothing else, so an
explicit ungated bind stays as quiet as it is now.

### 4.6 `relay.PolicyWarnings`

```
type PolicyWarning struct {
    Role  string
    Index int      // index into order[role]; -1 for an unlisted-candidate warning
    Token string
    Text  string   // the rendered line
}

func PolicyWarnings(set *candidate.Set, pol policy.Policy) []PolicyWarning
```

Pure. For each role in `harness.RoleNames()` with an `order` entry, in list
order, then unlisted:

```
order.<role>[<i>] "<tok>" is not a configured candidate
order.<role>[<i>] "<tok>" does not serve <role> (its roles: [...])
<role>: <tok> serves the role but is not in order.<role>
```

A role with no `order` entry produces no warnings: nothing is unlisted
relative to an order that does not exist. Both `FormatPolicy` and
`policyChecks` render from this one function.

### 4.7 `relay.FormatPolicy`

```
func FormatPolicy(set *candidate.Set, pol policy.Policy, gates []ledger.Gate) string
```

Pure. For each role in `harness.RoleNames()`:

```
builder  (order set in ~/.config/relay/policy.json)
  1  agy/google/gemini-3.8-flash-high        order      rate-limited until 20:28
  2  claude/anthropic/sonnet                 order      <- would pick
  3  opencode/openrouter/z-ai/glm-5.3-flash  unlisted
reviewer  (no order set)
  1  claude/anthropic/opus                   sole       <- would pick
researcher  (no order set)
  no candidate serves this role
```

Rows are the ranked list of §4.2 (for a role with no order: `ForRole` in
ref order, tag blank). The tag is `sole` whenever exactly one candidate
serves the role, order or not, matching the resolver's `HowSole`. The third
column is the gate text for each live gate on the row, joined by `; `. The
marker comes from calling `resolveCandidate(set, pol, gates, "", role)`:
success marks that row `<- would pick`; `ErrAmbiguousCandidate` appends a
row `  would refuse: N candidates serve <role> and no order is set`;
`ErrAllGated` appends `  would refuse: every candidate serving <role> is
gated`; `ErrRoleNotServed` prints `no candidate serves this role`;
`ErrNoCandidates` prints the `ErrNoCandidates` text once and stops.

After the roles, when `PolicyWarnings` is non-empty:

```
warnings
  order.builder[3] "claude/anthropic/haiku" is not a configured candidate
```

When `pol.Order` is empty, the last line is `no policy configured; write
~/.config/relay/policy.json (see README "Policy")`. `relay policy` is this
formatter over `rt.Candidates, rt.Policy, Gates(rt)`; no flags; exit 0
always -- it is a listing, not a check. Not tested through the subcommand.

### 4.8 `doctor` rows

```
func policyChecks(warnings []relay.PolicyWarning) []doctor.Check
```

Pure, in `cmd/relay/doctor.go` beside `ledgerChecks`. One `SevWarn` row per
warning: `Group: ""`, `Name: "policy"`, `Detail: w.Text`, `Fix: "edit
~/.config/relay/policy.json"`. Appended by `cmdDoctor` after `ledgerChecks`.
Not failures: a degraded order is not a broken machine.

### 4.9 CLI

| command | change |
|---|---|
| `relay policy` | new; §4.7 |
| `bind`/`add`/`fork`/`ask` | stderr line per §4.5, after the existing `GatedNote` |
| `bind --builder`, `add --builder`, `fork --builder`, `ask --candidate` | help: "omit to take the first ungated candidate in policy.json order[<role>]" |
| `relay doctor` | §4.8 |
| `relay help` | `policy` listed under the same heading as `candidates` |

## 5. High-level pseudocode

### 5.1 `resolveCandidate`, omitted token

```
if set.Len() == 0: return ErrNoCandidates
serving := set.ForRole(role)
if len(serving) == 0: return ErrRoleNotServed
gatesOf := func(tok) []Skip  -- every Gate with Token == tok, as Skip

if len(serving) == 1:
    g := gatesOf(serving[0].Ref().String())
    if len(g) == 0: return Resolution{serving[0], HowSole}
    return ErrAllGated{serving[0]: g}

if len(pol.OrderFor(role)) == 0: return ErrAmbiguousCandidate (+ policy hint)

ranked := rankedList(set, pol, role)          -- §4.2
skipped := []
for r in ranked:
    g := gatesOf(r.Token)
    if len(g) == 0: return Resolution{r.Candidate, r.How, r.Position, skipped}
    skipped += g
return ErrAllGated{skipped}
```

### 5.2 `BindResolved`, fresh path

```
… preconditions, planner lookup, CWD check as today …
builder, res, err := resolveBuilder(ctx, rt, opts, name, planner.PaneID)
b := Binding{…, BuilderCandidate: res.Token(), Round: 1, …}
err := rt.Store.WithLock(func(tx):
    tx.Save(b)
    if res.How != "": tx.AppendLog(name, pickEntry(rt.Now(), 1, "builder", res))
)
on err: "bind failed after starting builder in pane %s (close it yourself)" as today
stored := rt.Store.Load(name)
return stored, res, nil
```

### 5.3 `cmdBind`

```
b, res, err := relay.BindResolved(...)
print the binding line as today
if n := GatedNote(rt, b.BuilderCandidate); n != "": eprint(n)
if res.How != "" && res.How != HowExplicit: eprint(ExplainResolution("builder", res))
noteConsultRolesTooLong(...)
```

`cmdAdd`, `cmdFork`, `cmdAsk` mirror this with their result's `Resolution`
(`"reviewer"`/`opts.Role` for ask).

### 5.4 `FormatPolicy`, one role

```
ranked := rankedList(set, pol, role)  -- or ForRole tagged sole/blank when no order
res, err := resolveCandidate(set, pol, gates, "", role)
header := role + ("  (order set in …)" | "  (no order set)")
for i, r in ranked:
    marker := "<- would pick" if err == nil && r.Token == res.Token() else ""
    row(i+1, r.Token, r.How-or-"sole"-or-"", gateText(r.Token), marker)
switch err:
    ErrAmbiguousCandidate: row "would refuse: N candidates serve role and no order is set"
    ErrAllGated:           row "would refuse: every candidate serving role is gated"
    ErrRoleNotServed:      row "no candidate serves this role"
```

## 6. Error handling strategy

| error | where | recoverable |
|---|---|---|
| `policy.ErrBadPolicy` (new) | `Load`: bad JSON, unknown role key, bad token, duplicate token | yes: the message names path, role, index; fatal at `newRuntime` like a bad `candidates.json` |
| `relay.ErrAllGated` (new) | omitted token; every candidate serving the role is gated | yes: `--builder <tok>` bypasses, `relay available <provider>` clears |
| `relay.ErrAmbiguousCandidate` | unchanged trigger (≥2, no order); message gains the policy hint | yes: set `order.<role>` or name one |
| `ErrNoCandidates`, `ErrRoleNotServed`, `ErrUnknownRole`, `ErrNoBuilderCandidate`, `candidate.ErrBadRef`, `candidate.ErrUnknownCandidate` | unchanged | unchanged |

`ErrAllGated` text:

```
every candidate serving "builder" is gated: agy/google/gemini-3.8-flash-high (rate-limited until 20:28), claude/anthropic/sonnet (spawn failed until 19:12); name one with --builder to bypass, or clear a gate with relay available <provider>
```

All resolver errors are raised before any lock, pane or worktree exists, as
today. A pick-entry append failure after a successful spawn is returned as
the bind/add/fork/ask error with the "close it yourself" wording the save
failure already uses -- the binding was saved in the same transaction, so a
failed append rolls nothing back but must not be silent. Cross-file
inconsistency between `policy.json` and `candidates.json` is never an error
anywhere: it is a warning in `relay policy` and `doctor` (§4.6).

`Gates(rt)` already treats a ledger read error as an empty ledger with one
stderr line; the resolver inherits that: a corrupt ledger means no gates,
never a refusal.

No `slog` in these paths: they run in the CLI, not the daemon. The `pick`
entry is the record.

## 7. Ordered implementation steps

Each step is one plan / one builder session; `make check` green at every
step; a builder that finds a step impossible as written halts.

1. **`policy` package + `Runtime.Policy`.** §3.1, §3.4, §4.1. `Load`,
   `OrderFor`, `ErrBadPolicy`. `newRuntime` loads
   `filepath.Join(configDir, "relay", "policy.json")`. Tests: missing file
   → zero value; each validation branch with its message substring;
   `OrderFor` on an unknown role is nil. No caller reads `Policy` yet.
   Verify: `go build ./...`; `relay status` on a machine with no
   `policy.json` is unchanged.

2. **Resolver.** §3.3, §4.2, §5.1, `ErrAllGated`, `ExplainResolution`
   (§4.5). `resolveCandidate` takes the new signature and returns
   `Resolution`; the four existing call sites (`resolveBuilder`, `Add`,
   `Fork`, `Ask`) and `CandidateKind` pass `rt.Policy, Gates(rt)` and read
   `.Candidate` -- nothing is recorded yet. Tests in `candidate_test.go`:
   every row of the §4.2 table with hand-built `gates`; ordering of
   listed-then-unlisted; a listed token that is unconfigured or does not
   serve the role is skipped without error; two gates on one token yield
   two `Skip`s; `ExplainResolution` per `How`. Depends on 1. Mutation: make
   the walk ignore `gates` -- the "skips a gated first entry" test must
   fail; put unlisted before listed -- the ordering test must fail.

3. **Pick record.** §3.2, §3.5, §4.3, §4.4, §4.9 stderr lines.
   `store.KindPick`; `resolveBuilder` returns `Resolution`; `BindResolved`
   + `Bind` wrapper; `AddResult`/`ForkResult`/`AskResult.Resolution`;
   `Fork.InheritedFrom`; `pickEntry` appended per §4.4; `cmdBind`/`cmdAdd`/
   `cmdFork`/`cmdAsk` print per §5.3. Tests in `internal/relay` with the
   fake herdr: bind on a two-candidate machine with an order and a gated
   first entry spawns the second and leaves exactly one `pick` entry with
   `Confirmed: true` and the expected `Note`; adoption leaves none; an
   explicit gated token spawns and logs `policy bypassed; gated:`; fork
   with no token logs `inherited from <src>`; ask logs at the consult's
   round before the `ask` entry; a stranded ask logs no pick. Depends on 2.
   No `cmd/relay` test reaches herdr.

4. **`relay policy` + `doctor`.** §4.6–4.8, §5.4. `PolicyWarnings`,
   `FormatPolicy`, `cmdPolicy`, dispatch, help, `policyChecks`. Tests:
   `FormatPolicy` on a fixed set/policy/gates for each of: order with a
   gated first row, no order with one candidate, no order with two, all
   gated, role unserved, empty policy; `PolicyWarnings` matrix;
   `policyChecks` in `cmd/relay/doctor_test.go` as a pure function.
   Depends on 2 (not 3).

5. **Docs.** README: new "Policy" subsection under "Candidates" (file
   format, the rule, `relay policy`, the warnings); "Choosing a candidate"
   rewritten to the new rule; "Availability" loses "it does not (yet) act
   on it" and the "proceed -- refusing is a later step" sentence, and says
   an omitted token skips gated candidates while an explicit one proceeds
   with the note. CLAUDE.md "Dispatching work to builders": the order
   lives in `~/.config/relay/policy.json`, omit `--builder`, `relay policy`
   shows what would be picked, keep running `relay unavailable` when a
   builder reports a limit so the next pick skips it. `docs/design.md`
   "Candidates (config)" paragraph gains one sentence on `policy.json`.
   Depends on 3 and 4.

Planner verification across the change, not the builder's: `make check`
constituents; `git diff --stat` against §2; the two mutations in step 2;
then on this machine with a temp `XDG_STATE_HOME` and the real
`candidates.json`: write `policy.json` with the three builders in
CLAUDE.md's order, `relay unavailable agy/google/gemini-3.8-flash-high
--reason test`, `relay policy` shows agy gated and sonnet `<- would pick`,
`relay bind` with no `--builder` starts sonnet and prints `picked … order
#2; skipped agy/…`, `relay log` shows the `pick` entry, `relay unavailable
claude/anthropic/sonnet` + `relay unavailable opencode/…` then `relay bind`
refuses with `ErrAllGated`, `relay bind --builder agy/…` proceeds with the
note; `relay available` for each provider.
