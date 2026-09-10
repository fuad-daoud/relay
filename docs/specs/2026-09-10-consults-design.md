# Consults: read-only, one-shot agent roles beside the builder

- Date: 2026-09-10
- Issues: #36 (consults); unblocks #37 (triggers)
- Status: approved, not yet planned

## 1. System overview

A binding today is exactly one planner and one builder, and that pair is baked
into the schema rather than expressed as roles. Everything else the planner
needs -- a codebase search, a second opinion on a diff -- it does itself, in its
own context window, at planner prices.

This design adds one primitive, the **consult**: an ephemeral, read-only,
one-shot agent that runs in its own relay-spawned pane and hands findings back
through a file path. A consult has no round counter, no diff baseline, no
worktree of its own, no screen fingerprint and no persistent session. It is
spawned by `relay ask`, watched by the daemon's existing per-binding tick, and
delivered to the planner through the delivery path reports already use.

Exactly one role ships here: `reviewer`. It is read-only, one-shot, and the
artifact it consumes -- `NNN-diff.patch` -- is already captured by
`CaptureRoundDiff`. It exercises the whole primitive without touching the
builder loop. `explorer` and `explorer-light` are deliberately deferred.

### Scope boundary

relay makes no judgements here either. The planner decides what to ask, which
role to ask, and what to do with the answer. relay spawns a pane, types a
prompt it was given, checks whether a file appeared, and delivers a path. The
one gate is `does the findings file exist`, which is a fact.

**"Read-only" is a property of the role's configuration, not a guarantee relay
enforces.** relay cannot observe writes -- `herdr agent list` reports a kind, a
status, a cwd and a title, and nothing more. The prompt instructs the consult
not to modify the tree; that is an instruction, not a sandbox. This vocabulary
matches `2026-09-10-foreign-agent-detection-design.md` §7.2 and must not drift
into claiming enforcement.

### Two claims in #36 that this design rejects

Both were checked against the source before being set aside.

**#36 says `ErrCWDTaken` must be relaxed to key on writers, and calls it "the
single blocker".** It is not a blocker under this design. `ErrCWDTaken` is
raised from exactly three places -- `Bind` (`internal/relay/bind.go:220`), `Add`
(`internal/relay/add.go:146`) and `Store.assertCWDFree`
(`internal/store/store.go:299`) -- and all three are reached only when a
*binding* is created. A consult creates no binding, so it never reaches
`FindByCWD`. The rule is relaxed only under the pseudo-binding model, which §7.3
rejects. **The cwd rule is unchanged by this work.**

**#36 says endpoints and log directions must become role-tagged.** The
combinatorial pressure it fears -- a field per role on `Binding` -- is created
by modelling consults as endpoint positions. Modelling them as records with
their own `Role` field removes it. `Direction` likewise needs no
generalisation for findings: findings genuinely travel *to the planner*, and
#36's own requirement (that they be written `DirToPlanner` so the pending scan
picks them up) is an argument for leaving the enum alone. See §7.4.

### The blocker #36 missed

`knownEndpoints` (`internal/relay/foreign.go:58`) returns `b.Planner` and
`b.Builder` and nothing else. `ForeignAgents` flags every live agent inside a
bound tree matching no known endpoint. A `reviewer` consult sitting in the
builder's tree therefore raises a foreign-agent row on `relay status` for its
entire life. This is a one-line fix (§4.5) but it is invisible until the first
consult is spawned, and it is the actual prerequisite that must land.

## 2. File structure

```
internal/store/types.go        (modified) Consult, ConsultState; Binding gains
                                          Consults and ConsultCap
internal/store/log.go          (modified) KindAsk, KindFindings, DirToConsult;
                                          pendingForPlanner FIFO; confirmEntry
                                          replaces confirmLatest
internal/store/store.go        (modified) AskPath, FindingsPath helpers
internal/alias/alias.go        (modified) Spec gains Role and Tree

internal/relay/ask.go          (new)      Ask: validate, spawn, record, log
internal/relay/consult.go      (new)      reconcileConsults, finishConsult,
                                          consult prompt templates
internal/relay/reap.go         (new)      Reap: close reapable panes, drop records
internal/relay/reconcile.go    (modified) call reconcileConsults in Reconcile
internal/relay/foreign.go      (modified) knownEndpoints includes consults
internal/relay/pull.go         (modified) confirm by identity, not "latest"
internal/relay/deliver.go      (modified) confirm by identity, not "latest"
internal/relay/status.go       (modified) running-consult count per binding
internal/herdr/client.go       (modified) ClosePane

internal/ui/fetch.go           (modified) surface KindFindings
cmd/relay/main.go              (modified) ask, reap subcommands; help text

internal/harness/agents/reviewer.claude.md    (new)
internal/harness/agents/reviewer.opencode.md  (new)

docs/design.md                 (modified) amend "Relay never kills a pane"
CLAUDE.md                      (modified) same amendment
README.md                      (modified) reviewer role install notes
```

## 3. Data structures and type definitions

### 3.1 `store.ConsultState` (new)

```go
type ConsultState string

const (
    ConsultRunning ConsultState = "running" // spawned; no findings yet
    ConsultDone    ConsultState = "done"    // findings queued to the planner
    ConsultSilent  ConsultState = "silent"  // gave up; "no findings" reported
)
```

`done` and `silent` are both terminal and both **reapable**. The record is not
dropped when the consult finishes, because `relay reap` needs the pane id to
close it: `Binding.Consults` is the reap worklist as well as the watch list.
This is a direct consequence of choosing `relay reap` over auto-close (§7.1).

### 3.2 `store.Consult` (new)

| Field | Type | Constraint | Purpose |
| --- | --- | --- | --- |
| `ID` | `string` | 8 lowercase hex, generated at spawn | identity in the log, the filenames and `relay reap` |
| `Role` | `string` | non-empty; resolves in the alias table at spawn | recorded as a name, not a resolved spec, so the record stays truthful about intent if the spec changes under it |
| `Endpoint` | `store.Endpoint` | `PaneID` required; `SessionID` best effort | reuses the existing shape so `FindAgent`/`SameAgent`/`refreshEndpoint` work unmodified |
| `Round` | `int` | >= 1 | the owning binding's round at spawn; audit and filename only |
| `AskPath` | `string` | absolute | where relay staged the question body, mirroring `Send` and `NNN-plan.md` |
| `FindingsPath` | `string` | absolute | where the consult was told to write; its existence is the entire completion gate |
| `State` | `ConsultState` | one of the three above | |
| `SpawnedAt` | `time.Time` | UTC, non-zero | timeout origin |
| `NudgedAt` | `time.Time` | zero means not yet nudged | one nudge only |
| `Note` | `string` | may be empty | why a `silent` consult gave up; empty for `running` and `done` |

`Round` is borrowed for filenames and audit. It is the **only** relationship
between a consult and the builder's round: a consult never advances a round,
never stamps `RoundStartedAt`, and never touches a diff baseline.

### 3.3 `store.Binding` (modified)

```go
Consults   []Consult `json:"consults,omitempty"`
ConsultCap int       `json:"consult_cap,omitempty"` // 0 means defaultConsultCap (8)
```

`omitempty` on both means every `bind.json` already on disk stays
byte-identical until its first consult. There is no migration.

`ConsultCap` bounds **running** consults per binding, not lifetime ones. It
exists for the reason `RoundCap` does, and specifically for the case #36 names:
twelve idle opencode panes is roughly 9.6 GB, which is not a workspace.

### 3.4 `store.Kind` and `store.Direction` (modified)

```go
KindAsk      Kind = "ask"       // planner -> consult, the staged question
KindFindings Kind = "findings"  // consult -> planner, the findings path

DirToConsult Direction = "to_consult"
```

`DirToConsult` is additive and safe: every consumer of `Direction` tests
equality against a specific value (`deliver.go:29`, `fork.go:102`,
`reconcile.go:191`/`254`/`396`/`473`, `ui/fetch.go:93`). There is no exhaustive
switch anywhere in the tree. The alternative -- logging an outbound consult
prompt as `DirToBuilder` -- would redefine what a persisted enum value means,
which is a larger change than adding a value.

### 3.5 `alias.Spec` (modified)

```go
type Spec struct {
    Name     string   `json:"name"`
    Kind     string   `json:"kind"`
    Args     []string `json:"args"`
    Preamble string   `json:"preamble,omitempty"`
    Role     string   `json:"role,omitempty"` // "" or "builder"; or "consult"
    Tree     string   `json:"tree,omitempty"` // "" or "binding"; or "none"
}
```

Both default to today's behaviour, so the three existing aliases parse
unchanged and `alias_test.go` keeps passing.

`Tree: "none"` is forward-declared for a future treeless `explorer-light`. **No
treeless role ships in this spec, and `Ask` refuses `Tree: "none"` with
`ErrTreelessUnsupported`.** It is declared now because the alternative is
`explorer-light` arriving and finding `Tree` absent, and because a field with an
explicit refusal is honest where a silently-ignored one is not.

### 3.6 Path helpers (`internal/store/store.go`)

```
AskPath(name string, round int, id string)      -> <dir>/NNN-<id>-ask.md
FindingsPath(name string, round int, id string) -> <dir>/NNN-<id>-findings.md
```

Siblings of `roundFile`, which takes no id. Deliberately **not**
`NNN-question.md`: that name belongs to the blocked-dialog capture
(`QuestionPath`), and a consult being asked a question is a different event
from a builder being blocked on one.

## 4. Interface definitions and component contracts

### 4.1 `internal/relay/ask.go` (new)

```go
type AskOptions struct {
    Role        string // consult role to spawn; required
    File        string // question file; required
    Name        string // binding; from --name, positional, or CWD
    PlannerPane string // $HERDR_PANE_ID; required
    NewTab      bool
    WorkspaceID string
}

type AskResult struct {
    Consult store.Consult
    Binding string
}

func Ask(ctx context.Context, rt Runtime, opts AskOptions) (AskResult, error)
```

Single responsibility: spawn one consult and record it. It does not wait for
findings and does not deliver anything.

- **Preconditions:** `opts.PlannerPane` names a live agent pane; `opts.File` is
  readable; `opts.Role` resolves to a spec with `Role == "consult"` and
  `Tree != "none"`; the named binding exists and is not `broken` or `done`;
  running consults on it are below `ConsultCap`.
- **Postconditions:** on success a `ConsultRunning` record exists on the
  binding, the question is staged at `AskPath`, one `DirToConsult`/`KindAsk`
  entry is appended, and a live pane is running the role. On a *validation*
  failure nothing has been spawned. On a *spawn* failure after the pane exists,
  a `ConsultSilent` record is written so the pane is reapable (§6.2).
- **Errors:** `ErrNotAConsultRole`, `ErrTreelessUnsupported`, `ErrConsultCap`,
  `alias.ErrUnknownAlias`, `store.ErrNotFound`, wrapped herdr failures.
- **Dependencies:** `rt.Herdr` (SplitPane/CreateTab/StartAgent/Prompt),
  `rt.Store`, `rt.Aliases`, `rt.Now`, and a random source for `ID`.

### 4.2 `internal/relay/consult.go` (new)

```go
func reconcileConsults(ctx context.Context, rt Runtime, tx *store.Tx,
    b store.Binding, agents []herdr.Agent) (store.Binding, error)
```

Single responsibility: advance every running consult on one binding by one
tick. Called from `Reconcile` after the builder steps, inside the same
transaction, over the **same `agents` snapshot**, so a tick costs zero
additional herdr calls regardless of consult count.

- **Preconditions:** caller holds the state lock and passes its `tx`.
- **Postconditions:** every `ConsultRunning` record is either unchanged,
  nudged, or moved to `ConsultDone`/`ConsultSilent` with exactly one
  `DirToPlanner`/`KindFindings` entry queued for the transition. Terminal
  records are never revisited.
- **Errors:** wrapped store failures only. A herdr prompt failure while nudging
  is swallowed and retried next tick; it is not evidence the consult stopped.

```go
func finishConsult(ctx context.Context, rt Runtime, tx *store.Tx,
    b store.Binding, i int, state store.ConsultState, note string) (store.Binding, error)
```

Queues the findings entry through the existing `Queue`, then sets the record's
terminal state. Going through `Queue` is what makes consults inherit the
anti-clobber rule, `held`, notifications and `relay pull` with no new code.

Constants, not configuration:

```go
const (
    consultTimeout = 10 * time.Minute // spawned -> gave up, while still working
    consultGrace   = 60 * time.Second // nudged -> gave up, while idle
)
```

A consult runs a minute or two. Anything tunable here is a knob nobody turns,
and every knob is a thing `relay doctor` eventually has to explain.

### 4.3 `internal/relay/reap.go` (new)

```go
type ReapOptions struct {
    Name   string // binding; empty with All
    All    bool   // every binding
    DryRun bool
}

type ReapResult struct {
    Binding string
    Closed  []store.Consult // panes closed and records dropped
    Failed  []store.Consult // close failed; record kept
}

func Reap(ctx context.Context, rt Runtime, opts ReapOptions) ([]ReapResult, error)
```

Single responsibility: close panes for terminal consults and drop their
records. `ConsultRunning` records are never touched. A failed close keeps the
record so a retry is possible; it never fails the whole reap.

- **Errors:** `ErrUnknownConsult`, `store.ErrNotFound`, wrapped store failures.

### 4.4 `internal/herdr/client.go` (modified)

```go
func (c *Client) ClosePane(ctx context.Context, paneID string) error
```

The only pane-destroying call relay makes. It is reachable **only** from
`Reap`.

### 4.5 `internal/relay/foreign.go` (modified)

```go
func knownEndpoints(bindings []store.Binding) []store.Endpoint {
    eps := make([]store.Endpoint, 0, len(bindings)*2)
    for _, b := range bindings {
        eps = append(eps, b.Planner, b.Builder)
        for _, c := range b.Consults {
            eps = append(eps, c.Endpoint)
        }
    }
    return eps
}
```

This is consistent with `2026-09-10-foreign-agent-detection-design.md` §7.1
("foreign means referenced by no binding at all") and does **not** contradict
its §7.2. §7.2 concerns sub-agent panes relay never spawned and holds no
endpoint for, which stay foreign and stay reported. A consult is spawned by
relay and its endpoint is recorded, so it is referenced.

### 4.6 `internal/store/log.go` (modified)

```go
func (s *Store) PendingForPlanner(name string) (LogEntry, bool, error) // now OLDEST
func (tx *Tx) ConfirmEntry(name string, ts time.Time, kind Kind) error // replaces ConfirmLatest
```

`confirmLatest` is removed rather than kept alongside. "Latest" is ambiguous the
moment two payloads are pending, and leaving an ambiguous function reachable is
how the two callers drift apart.

## 5. High-level pseudocode

### 5.1 `relay ask`, per invocation

```
body   = read(opts.File)                       # before the lock, as Send does
spec   = rt.Aliases.Lookup(opts.Role)
if spec.Role != "consult":     return ErrNotAConsultRole
if spec.Tree == "none":        return ErrTreelessUnsupported
name   = resolveBinding(rt, opts.Name, positional)

WithLock(tx):
    b = tx.Load(name)
    if b.State in {broken, done}:                  return refusal
    if running(b.Consults) >= cap(b):              return ErrConsultCap

    id       = 8 hex from rt.Rand
    askPath  = AskPath(name, b.Round, id);   write(askPath, body)
    findings = FindingsPath(name, b.Round, id)
    agent    = name + "-" + spec.Name + "-" + id

    pane = opts.NewTab ? CreateTab(opts.WorkspaceID, b.CWD, agent)
                       : SplitPane(b.Planner.PaneID, splitDirection, b.CWD)

    c = Consult{ID: id, Role: spec.Name, Round: b.Round,
                AskPath: askPath, FindingsPath: findings,
                Endpoint: {AgentName: agent, PaneID: pane, Kind: spec.Kind},
                State: ConsultRunning, SpawnedAt: now}

    if StartAgent(agent, spec.Kind, pane, spec.Args) fails:
        c.State, c.Note = ConsultSilent, "start failed: " + brief(err)
        b.Consults = append(b.Consults, c); tx.Save(b)     # pane stays reapable
        return the error

    c.Endpoint.SessionID = best-effort lookup, as resolveBuilder does

    prompt = spec.Preamble + consultPrompt(askPath, findings)
    if promptWithRetry(pane, prompt) fails:
        c.State, c.Note = ConsultSilent, "prompt failed: " + brief(err)
        b.Consults = append(b.Consults, c); tx.Save(b)
        return the error

    tx.AppendLog(name, {Round: b.Round, Direction: DirToConsult, Kind: KindAsk,
                        Path: askPath, Note: spec.Name + " " + id, Confirmed: true})
    b.Consults = append(b.Consults, c)
    tx.Save(b)

print id, agent name, pane id, findings path
```

`consultPrompt`:

```
Read: <askPath>

Write your findings to: <findingsPath>

Reply here with only that path. Do not modify any file in this repository.
```

The last line is an instruction to the model, not an enforced constraint
(§1).

### 5.2 `reconcileConsults`, per binding per tick

```
for i := range b.Consults:
    c = &b.Consults[i]
    if c.State != ConsultRunning: continue          # terminal; awaiting reap

    agent, live = FindAgent(agents, c.Endpoint)

    if !live:
        if exists(c.FindingsPath):                  # wrote, then the pane died
            finish(i, ConsultDone, "")
        else:
            finish(i, ConsultSilent, "consult pane is gone")
        continue

    c.Endpoint = refreshEndpoint(c.Endpoint, agent)

    if exists(c.FindingsPath) and agent.Status in {idle, done}:
        finish(i, ConsultDone, ""); continue

    if agent.Status == blocked:
        finish(i, ConsultSilent,
               "blocked on a prompt in pane " + agent.PaneID); continue

    if agent.Status in {idle, done}:
        if c.NudgedAt.IsZero():
            promptWithRetry(agent.PaneID, consultNudge(c.FindingsPath))
            c.NudgedAt = now
        else if now - c.NudgedAt >= consultGrace:
            finish(i, ConsultSilent, "went idle without writing findings")
        continue

    # still working
    if now - c.SpawnedAt >= consultTimeout:
        finish(i, ConsultSilent, "no findings after " + consultTimeout)
```

Payloads queued by `finish`:

```
done:    "Findings from <role> consult <id>: <findingsPath>"
silent:  "Consult <id> (<role>) wrote no findings: <note>. Pane <p> is still open."
```

Both are **pointers, not content**, matching `handleIdleBuilder`'s
`"Builder finished round %d. Report: %s"`. relay hands the planner paths and
never inlines an artifact it did not author.

### 5.3 `relay reap`

```
for each selected binding, WithLock(tx):
    b = tx.Load(name)
    keep = []
    for c in b.Consults:
        if c.State == ConsultRunning: keep = append(keep, c); continue
        if opts.DryRun:               report c; keep = append(keep, c); continue
        if ClosePane(c.Endpoint.PaneID) fails:
            record as Failed; keep = append(keep, c); continue
        record as Closed              # dropped by omission from keep
    b.Consults = keep
    tx.Save(b)
```

## 6. Error handling strategy

### 6.1 Categories

| Error | Raised by | Recoverable | Recovery |
| --- | --- | --- | --- |
| `ErrNotAConsultRole` | `Ask` | yes | name a consult role |
| `ErrTreelessUnsupported` | `Ask` | yes | not implemented in this spec |
| `ErrConsultCap` | `Ask` | yes | `relay reap`, then retry |
| `ErrUnknownConsult` | `Reap` | yes | `relay status` for live ids |
| `alias.ErrUnknownAlias` | `Ask` | yes | reused, not redefined |
| wrapped herdr failure | `Ask` | partly | pane may exist; see 6.2 |

Every relay-defined error above is a **precondition** failure raised before
anything is spawned, matching `Bind`'s existing discipline of checking the cwd
before starting an agent so a refusal cannot strand a pane.

### 6.2 A spawn failure never strands a pane

If `StartAgent` or the first `Prompt` fails after `SplitPane` has already made a
pane, `Ask` still records the consult, in `ConsultSilent`, with the failure as
its `Note`. The pane is then reapable.

`resolveBuilder` (`bind.go:300`) does the opposite: it returns the error and
leaves the pane stranded with nothing pointing at it, which is the situation
`CLAUDE.md` warns about. Consults fan out, so inheriting that behaviour would
multiply the leak. Fixing `resolveBuilder` is **out of scope** here but worth
its own issue.

### 6.3 Accepted risk: a crash between spawn and save

A crash between `SplitPane` and `tx.Save` strands a pane with no record. The
window is milliseconds and the cost is a leaked pane, not lost work. Closing it
would require a two-phase write for a failure mode `herdr pane close` already
resolves by hand. `docs/design.md` names risks of this class rather than
engineering them away; this follows that precedent.

### 6.4 The consult failure path stays dumb

Builders get a nudge, screen-fingerprint quiescence and a scrape fallback
because a builder runs for hours. A consult runs for a minute or two, so it
gets **one nudge and then reports that it wrote nothing**. Specifically not
inherited: `screenFingerprint`, `builderQuiescent`, `scrapeReport`, and
`relay answer`.

A **blocked** consult is reported to the planner and abandoned, not negotiated
with. Its pane stays open, so a human can answer the dialog by hand and the
planner can re-ask. `relay answer` remains builder-only: a consult is one-shot,
and a one-shot agent that needs a conversation has already failed its contract.

### 6.5 Observability

- `relay status` gains a per-binding running-consult count, rendered as a
  column only when non-zero.
- `relay log` needs no change: `KindAsk` and `KindFindings` print through the
  existing formatter (`cmd/relay/main.go:843`).
- `internal/ui/fetch.go:93` filters `DirToPlanner && (KindReport ||
  KindQuestion)` and must learn `KindFindings`, or findings never surface in the
  TUI.
- No new hooks. `state_changed.d/` fires on binding state, and a consult never
  changes binding state.

## 7. Behavioural rules and their rationale

### 7.1 relay closes a pane only in `relay reap`

`docs/design.md:288` currently states "Relay never kills a pane", with no
exception, and `internal/herdr/client.go` has no close method at all. (#36
asserts the doc already carves out spawned builders. It does not; this is the
first carve-out.)

Auto-closing on success was considered and rejected. It would make relay's
first pane kill conditional on an outcome check, and a consult that wrote a
findings file and then crashed would lose the terminal that explains why.
`relay reap` keeps destruction explicit and human-triggered while still solving
the memory problem, at the cost of one command.

Consequence: terminal consult records persist until reaped (§3.1). `relay
status` showing a growing reapable count is the intended nudge.

### 7.2 A consult is a record, not a binding

Modelling a consult as a `Binding` with a role marker would maximise reuse of
`Reconcile`, but every consumer -- `Status`, `ui`, `gc`, `FindByCWD`, `doctor`,
`Fork` -- would need to learn to filter them, and `Round`, `RoundBaselineTree`,
`BuilderScreen` and the drift fields would all be permanently dead on them.
That is the shape #36 warns against in its own "if a role needs to remember, it
is a builder" rule.

### 7.3 The cwd rule is unchanged

See §1. Consults never create bindings and never reach `FindByCWD`, so
`ErrCWDTaken` cannot fire for one. Multiple read-only consults may share a tree
with a live builder freely, which is the outcome #36 wanted; it simply does not
require the rule change #36 proposed.

### 7.4 Rejected: role-keyed endpoints and a role-keyed `Direction`

`Consult` carries its own `Endpoint` and `Role`, so no field-per-role pressure
exists on `Binding`. `Direction` stays closed for inbound traffic because
findings *are* planner-bound. A migration nothing needs is pure risk against a
format users have live state in.

Revisit when a role appears that is neither planner-bound nor its own record --
most plausibly a writer role, which is out of scope for a different reason (a
second writer on one tree is the one failure mode `docs/design.md` calls out as
destroying work rather than stalling).

### 7.5 FIFO, and why it is a fix rather than a preference

Consults make multiple simultaneously-pending planner payloads normal.
`pendingForPlanner` returns the **newest** unconfirmed entry and `confirmLatest`
confirms the **newest**, so the queue is LIFO. Fan out three consults and the
builder's round report, queued first, is delivered last, behind findings that
arrived after it.

Arrival order is the only order relay can defend without judging content, so
delivery becomes FIFO and confirmation becomes identity-based
(`ConfirmEntry(name, ts, kind)`). This is worth landing as its own step: it is
independently testable, and it is arguably a latent bug today, since a report
and a blocked-dialog question can already be pending together.

### 7.6 A consult does not advance the round

`Ask` does not increment `Round`, does not stamp `RoundStartedAt`, and does not
capture or clear a diff baseline. `Round` on a `Consult` is a label. A consult
that finishes between rounds must not make the builder's next round look
started, and a consult that runs long must not consume the builder's round
timeout.

## 8. Role definition

### 8.1 `reviewer.{claude,opencode}.md` (new)

Read-only reviewer of a diff, spawned in its own relay pane, handing findings
back through a file path. Distinct from the `researcher` role shipped by
`2026-09-10-foreign-agent-detection-design.md` §8.2, which is dispatched by
`plan-executor` and returns findings in-band to its parent. Same read-only
posture, different contract; deliberately not the same definition.

Contents:

- Read-only tool set; explicit prohibition on edits, creates, deletes and any
  tree-modifying command.
- Instruction to write findings to the path named in the prompt and to reply
  with only that path.
- Instruction to cite file and line references rather than summarising.
- Instruction to state plainly when the diff does not contain a problem, rather
  than manufacturing one. A reviewer that always finds something is not pinning
  anything.
- A `model:` frontmatter pin.

**Model pins.** Worked examples, not a supported set, chosen so neither
introduces a provider the user does not already need -- the same framing §8.2
uses:

| file | pin | provider already assumed by |
| --- | --- | --- |
| `reviewer.claude.md` | `opus` | `cbuilder` alias (`sonnet`) |
| `reviewer.opencode.md` | `openrouter/z-ai/glm-5.3-flash` | `builder` alias |

A reviewer is the one role where paying for stronger reasoning is the point, so
the claude pin is deliberately above `cbuilder`'s.

### 8.2 The shipped alias entry

```json
{ "name": "reviewer", "kind": "claude",
  "args": ["--agent", "reviewer", "--model", "opus"],
  "role": "consult", "tree": "binding" }
```

Whether this ships in `DefaultTable` at all is governed by the open decision in
#24 §2 ("does relay ship a table at all"). If defaults survive that issue, this
entry joins them; if they do not, it ships as a documented example only. This
spec does not pre-empt that decision, and the implementer must check #24's
resolution before adding to `DefaultTable`.

## 9. Testing requirements

Verification is `make check`, not `go test ./...` (`CLAUDE.md`): it adds
`gofmt -l .` over the tree, `go vet`, and a `go mod tidy` check.

1. **FIFO delivery** (step 1, no consult code required): three pending
   planner-bound entries; assert delivery order is arrival order and that
   `ConfirmEntry` confirms the entry that was delivered, not the newest. Include
   the report-behind-findings case from §7.5 explicitly.
2. **`Pull` and `DeliverPending` agree**: both must claim the same entry, since
   they race under one lock.
3. **Lifecycle branches**, using a fake herdr and the injectable `rt.Now`, one
   test each: findings-then-idle; idle-no-findings-nudge; nudge-then-grace;
   blocked; pane-gone-with-findings; pane-gone-without-findings; timeout while
   working.
4. **Terminal records are inert**: a `done` consult is not re-queued on the next
   tick, and `reconcileConsults` over a binding of terminal records issues no
   herdr calls.
5. **Foreign-agent regression**: a live agent matching a consult endpoint inside
   a bound tree is **not** reported by `ForeignAgents`, while an unrecorded
   agent in the same tree still is.
6. **Refusals**: `ErrConsultCap` at the cap, `ErrNotAConsultRole` for a builder
   alias, `ErrTreelessUnsupported` for `tree: "none"`, and `Bind` refusing a
   consult alias.
7. **Spawn-failure reapability**: a `StartAgent` failure leaves a
   `ConsultSilent` record whose pane `Reap` closes.
8. **`Reap` never touches running consults**, and a failed `ClosePane` keeps the
   record.
9. **Schema compatibility**: a `bind.json` written before this change loads,
   round-trips, and re-serialises without `consults` or `consult_cap` keys.

**Mutation test** (`CLAUDE.md`): delete the `c.State != ConsultRunning` guard at
the top of `reconcileConsults` and confirm a *named* test from (4) fails. That
guard is the single line whose absence turns one delivery into one per tick,
and a test that passes with and without it is pinning nothing.

## 10. Ordered implementation steps

Each step is one focused session with its own verification.

**Step 1 -- FIFO pending queue.** Depends on nothing. Change
`pendingForPlanner` to return the oldest unconfirmed planner-bound entry;
replace `confirmLatest` with `ConfirmEntry(name, ts, kind)`; update
`DeliverPending` and `Pull` to confirm the entry they claimed.
*Verify:* tests (1) and (2); `make check` clean; no behaviour change with a
single pending payload.

**Step 2 -- consult schema.** Depends on 1 (log constants land together). Add
`Consult`, `ConsultState`, `Binding.Consults`, `Binding.ConsultCap`, `KindAsk`,
`KindFindings`, `DirToConsult`, `AskPath`, `FindingsPath`. No behaviour.
*Verify:* test (9); `make check` clean.

**Step 3 -- role-aware alias table.** Depends on 2. Add `Spec.Role` and
`Spec.Tree`; teach `Bind`/`Add` to refuse a consult alias.
*Verify:* existing `alias_test.go` unchanged and passing; test (6)'s `Bind`
refusal.

**Step 4 -- `relay ask`.** Depends on 3. `internal/relay/ask.go`, the `ask`
subcommand via `resolveBinding`, and help text. Spawns, records, logs, returns.
*Verify:* tests (6) and (7); a real `relay ask` against a bound tree produces a
live pane, a staged `NNN-<id>-ask.md`, and a `running` record.

**Step 5 -- `knownEndpoints` fix.** Depends on 2 only, and may land in parallel
with 4. One line plus a regression test.
*Verify:* test (5). Land this before step 6 or every manual test of the daemon
is noisy with false foreign rows.

**Step 6 -- consult lifecycle in the daemon.** Depends on 4 and 5.
`internal/relay/consult.go`; call it from `Reconcile`; add `KindFindings` to
`ui/fetch.go:93`.
*Verify:* tests (3) and (4); the mutation test; confirm a tick over N consults
still makes exactly one `ListAgents` call.

**Step 7 -- `relay reap`.** Depends on 6. `ClosePane` on the herdr client,
`internal/relay/reap.go`, the `reap` subcommand with `--all` and `--dry-run`.
*Verify:* test (8); `--dry-run` closes nothing.

**Step 8 -- ship the `reviewer` role.** Depends on 7. The two role definitions,
the alias entry subject to §8.2, `relay status` consult count, README notes, and
the `docs/design.md:288` / `CLAUDE.md` amendment about pane closing.
*Verify:* end to end -- run a builder round, `relay ask --role reviewer` against
its captured `NNN-diff.patch`, receive findings in the planner, `relay reap`.

## 11. Explicitly out of scope

- **Triggers** (#37): planner-declared edges firing consults on artifact
  appearance. This design is its prerequisite and deliberately does not
  anticipate it.
- **`explorer` and `explorer-light`**: `Tree: "none"` is declared and refused.
- **Any writer role.** A second writer on one tree is the failure mode
  `docs/design.md` calls out as destroying work rather than stalling.
- **Long-lived or stateful consults.** If a role needs to remember, it is a
  builder.
- **Fan-in or joins**: a consult that should run only after two others finish.
- **`relay answer` for consults** (§6.4).
- **Relay judging findings.** The gate is `does the file exist`, and it stays
  that way.
- **Fixing `resolveBuilder`'s stranded pane on spawn failure** (§6.2). Real, but
  it belongs to the builder path and deserves its own issue.
