# Builder identity, refresh and recovery

Status: design approved, not yet implemented.
Issue: [#20](https://github.com/fuad-daoud/relay/issues/20).
Date: 2026-09-08.

## 1. System overview

A relay binding records each side as a `store.Endpoint`: an agent name, a pane
id, a session id and a kind. relay writes that record once, at bind time, and
then trusts it forever. Issue #20 is what happens when the record is wrong.

A builder spawned by relay has its session id looked up immediately after
`StartAgent` returns. The agent has not registered with herdr yet, so the
lookup usually finds nothing and `SessionID` stays empty. From there two
subsystems read the same empty field and reach opposite conclusions:
`Reconcile` decides the binding can never be recovered, and `builderAlive`
decides the builder is alive and a rebind would abandon it. Automatic recovery
is impossible *because* the session is missing, and manual recovery is refused
*because* the session is missing. A healthy builder finished a round and relay
never relayed its report.

This spec fixes the flaw underneath all four symptoms: **an endpoint is not a
record written once, it is a cache of a live agent's identity, and relay never
repairs it.** The fix is one rule with three consequences -- an endpoint is
refreshed from the live agent whenever relay locates it, and every identity
question is answered by one shared predicate.

The change is confined to `internal/relay`. No new state, no schema change, no
migration: `Endpoint` gains no fields and existing `bind.json` files load
unchanged.

## 2. The four defects and how they interlock

| # | Defect | Site |
|---|---|---|
| 1 | Session lookup races the agent's own registration, leaving `SessionID` empty | `bind.go:300` |
| 2 | `Target()` prefers `AgentName`, which herdr forgets across a restart | `send.go:27` |
| 3 | The un-break gate refuses to recover when `SessionID == ""` | `reconcile.go:100` |
| 4 | `builderAlive` accepts a bare pane match as alive, so rebind is refused | `bind.go:74` |

Defects 3 and 4 are the deadlock, and they are a genuine contradiction rather
than two separate oversights. `builderAlive` already reasons that with no
recorded session, a pane match is the best available evidence. `Reconcile`
reasons that the same absence makes the binding unrecoverable. Same field, same
absence, opposite conclusions. Only one of them can be right.

Defect 2 is independent of the deadlock but shares its cause. It was already
fixed once, locally, in the UI: `internal/ui/fetch.go:170` addresses the agent
`FindAgent` just located rather than replaying the stored `AgentName`, with a
comment citing this issue. This spec generalises that precedent to every
addressing site.

Only builders relay **spawned** are exposed. An adopted builder goes through
`endpointOf` (`bind.go:311`), which records no `AgentName` and reads the
session straight off a live agent, so it has neither the racing lookup nor the
forgettable name.

## 3. The shared predicate

One function, in `internal/relay/herdr.go` -- the package that already owns
every herdr assumption relay makes.

```go
// SameAgent reports whether a live agent is the one an endpoint records.
func SameAgent(a herdr.Agent, ep store.Endpoint) bool
```

Its rule, in three arms:

```
session recorded          -> a.Session.Value == ep.SessionID   (exact, no fallback)
no session, kind recorded -> a.PaneID == ep.PaneID && a.Kind == ep.Kind
no session, no kind       -> a.PaneID == ep.PaneID             (best available)
```

The first arm is exact on purpose: a session id is durable identity, so if one
was recorded and no live agent carries it, the agent is gone. Falling back to a
pane match there is how relay currently resolves a dead builder to whatever new
agent occupies its former pane.

The third arm exists for bindings written before `Kind` was recorded. relay
cannot corroborate with a field it never stored, and a stricter rule would
strand exactly the old bindings this issue is about.

`FindAgent` is reimplemented over `SameAgent`. Its signature does not change,
so all eight call sites compile untouched:

```go
func FindAgent(agents []herdr.Agent, ep store.Endpoint) (herdr.Agent, bool)
```

Callers that pass a bare `store.Endpoint{PaneID: x}` -- `Bind`, `resolveBuilder`
and `Fork` locating a planner or an adopted pane -- hit the third arm and keep
their current behaviour exactly.

### 3.1 This makes FindAgent strict, deliberately

`FindAgent` today falls back to a pane match even when a session *was* recorded
and did not match. Routing it through `SameAgent` removes that fallback, which
changes behaviour beyond issue #20 and is intended:

- A binding whose builder died and whose pane was recycled currently resolves
  to the new occupant, and `Reconcile` will read its screen, nudge it and send
  it plans. It now goes `BROKEN` instead.
- `DeliverPending` (`deliver.go:57`) has the same hazard on the planner side: a
  stranger in the planner's old pane can be handed the planner's payload. It
  now reports `PlannerGone` instead.

The cost is that bindings which used to limp along against the wrong agent now
land in `BROKEN` or `ORPHANED`. That is the honest answer, and both states have
documented recovery paths.

## 4. Endpoint refresh

In `internal/relay/reconcile.go`:

```go
// refreshEndpoint updates an endpoint from the live agent it was located by.
func refreshEndpoint(ep store.Endpoint, a herdr.Agent) store.Endpoint
```

- **Precondition:** `SameAgent(a, ep)` held -- the caller located this agent.
  The binding must not be in `StateBroken`; the caller un-breaks first.
- **Postconditions:** `SessionID` is set when it was empty and the agent
  reports one; `PaneID` becomes `a.PaneID`; `Kind` is set when it was empty;
  `AgentName` is untouched. A recorded `SessionID` is **never** overwritten --
  if the agent matched the first arm it carries that same session, and if it
  matched a later arm relay has no business promoting a pane match into
  durable identity.

Refreshing the pane id is what removes `AgentName`'s justification. `AgentName`
was preferred for addressing because a pane id changes when a pane moves
between workspaces; once every located agent refreshes the stored pane id, the
stored pane id is current by construction.

Both endpoints are refreshed. The planner costs no extra herdr call -- it is a
second pass over the agent slice the daemon already fetched -- and it fixes
planner pane-move staleness on the same principle.

## 5. Reconcile after the change

The un-break gate is **deleted, not patched**. Once `FindAgent` *is*
`SameAgent`, the gate asks a question `FindAgent` already answered: if the
agent was found, it matched. That the four lines can be deleted is the proof
that the disagreement between defects 3 and 4 was the whole bug.

Pseudocode for the head of `Reconcile`, replacing `reconcile.go:91-106`:

```
if b.State is Done:            return b

builder, ok := FindAgent(agents, b.Builder)
if not ok:
    b.State = Broken
    return b                          # endpoints are NOT refreshed while broken

if b.State is Broken:
    b.State = Active                  # located means matched; no second check

b.Builder = refreshEndpoint(b.Builder, builder)
if planner, ok := FindAgent(agents, b.Planner); ok:
    b.Planner = refreshEndpoint(b.Planner, planner)

... round cap halt, log read, status switch, deliverAndSettle unchanged ...
```

Placement is load-bearing: the refresh runs after the un-break and before the
round-cap halt, so it happens on every tick where the builder was located and
the binding is not `Done`. A binding whose builder cannot be located returns
early and is not refreshed, which is correct -- relay does not update identity
from an agent it could not find.

`emitMutations` already dispatches `EventStateChanged` on the `BROKEN ->
ACTIVE` transition, so recovery is observable through the existing hook
mechanism. The refresh itself emits nothing: `bind.json` is its own record, and
the round log is round-keyed with no slot for a non-round fact (see
[#19](https://github.com/fuad-daoud/relay/issues/19)).

## 6. builderAlive is deleted

`builderAlive` (`bind.go:74`) becomes, exactly, `_, ok := FindAgent(agents,
b.Builder)`. It is deleted and its two call sites in `resume` call `FindAgent`
directly. Two functions that were allowed to disagree become zero.

`ErrBuilderAlive` keeps its exact meaning and message. Its behaviour after this
change is worth stating, because it is the outcome issue #20 actually wanted:

- Builder session-less and its pane still occupied by an agent of the same kind
  -> still refuses the rebind. This is now **correct**, because the binding
  un-breaks itself on the next tick instead of needing a rebind.
- Builder gone -> `FindAgent` fails, rebind is allowed, exactly as today.

No `--force` override is added. The deadlock is removed by making the two
subsystems agree, not by giving the human a way to overrule a rule that was
wrong.

`resolveBuilder`'s best-effort session lookup (`bind.go:300`) **stays** -- it
usually succeeds and costs one call relay is already making. Its comment must
be corrected: it currently claims an empty `SessionID` means the binding "never
self-heals", which this change makes false. It now costs the binding nothing
beyond one tick.

## 7. Addressing

`Target` keeps its name and every caller it has. It loses only the preference
that broke it:

```go
// Target is the herdr target for an endpoint: its pane id, which Reconcile
// keeps current by refreshing every endpoint it locates.
func Target(ep store.Endpoint) string
```

`AgentName` stops being an address and becomes what it always was in practice --
provenance, recording what relay named the agent at spawn. herdr can forget that
name across a server restart while the pane stays perfectly addressable, which
is how `agent target relay-ui-builder not found` was reported against a live
builder.

**Why the located agent is not threaded through.** `handleBlockedBuilder`,
`screenFingerprint`, `nudgeBuilder` and `scrapeReport` all take a
`store.Binding`, not a `herdr.Agent`. Passing a target down through six
signatures to reach them would buy nothing the refresh has not already bought:
section 5 refreshes `b.Builder` before the status switch runs, so by the time
any of them calls `Target(b.Builder)`, that pane id **is** the located agent's
pane id. The refresh is what makes pane-id addressing correct, and duplicating
it as a parameter would state the same guarantee twice.

Two sites do hold a located agent, and address it directly rather than going
back through the endpoint -- the same shape as `internal/ui/fetch.go:170`:

| Site | Change |
|---|---|
| `reconcile.go:173` read blocking dialog | unchanged; `Target` is now pane-id |
| `reconcile.go:274` fingerprint screen | unchanged |
| `reconcile.go:329` nudge | unchanged |
| `reconcile.go:355` scrape report | unchanged |
| `deliver.go:89` prompt planner | `planner.PaneID`, the agent it just located |
| `send.go:78` prompt builder | the located builder's pane id, section 8 |
| `answer.go:64` send keys | the located builder's pane id, section 8 |

`internal/ui/fetch.go` already does this and needs no change.

## 8. Send and Answer

Neither lists agents, and both call `Target(b.Builder)` inside
`store.WithLock`. They locate the builder **before** taking the lock, the way
`Send` already reads the plan file and captures the baseline pre-lock. That
keeps the critical section free of extra subprocesses -- `send.go` justifies
holding the lock across `Prompt` on the grounds that it costs milliseconds, and
that argument must keep holding.

```
Send(name, file):
    body    = read file                          # unchanged
    hint    = Store.Load(name)                   # already exists, for the baseline
    baseline = CaptureBaseline(hint)             # unchanged
    agents  = Herdr.ListAgents()                 # NEW
    builder, ok = FindAgent(agents, hint.Builder)
    if not ok: return ErrBuilderGone

    WithLock:
        b = tx.Load(name)                        # authoritative
        ... broken / round-cap checks unchanged ...
        if not SameAgent(builder, b.Builder):    # NEW: the hint may be stale
            return ErrBuilderGone
        ... stage plan, compose prompt ...
        promptWithRetry(builder.PaneID, text)
        ... unchanged ...
```

`Send`'s pre-lock `Store.Load` is already best-effort -- today it feeds the
baseline capture and its error is ignored. When it fails there is no endpoint
to locate against, so relay skips the pre-lock resolution entirely and lets the
authoritative in-lock `tx.Load` produce `store.ErrNotFound`. It must not invent
a different error for a binding that may simply not exist.

The in-lock re-assertion matters: the pre-lock load is a hint, and the daemon
can rebind the binding underneath it. On mismatch the send fails rather than
prompting a pane that is no longer this binding's builder.

`Answer` takes the same shape: list and locate before `WithLock`, re-assert
inside it, then `SendKeys(builder.PaneID, keys)`. A blocked agent still
appears in `herdr agent list`, so locating it works while it sits at a dialog.

## 9. Error handling

One new sentinel, in `internal/relay/send.go` beside `ErrBuilderBlocked`:

```go
// ErrBuilderGone reports that a binding's builder could not be located among
// the live agents, so there is nothing to address.
var ErrBuilderGone = errors.New("builder is gone; rebind before sending")
```

Returned by `Send` and `Answer`, wrapped with the binding name, the recorded
pane id and the builder alias so the message names what to rebind.

This is a **new failure mode**. `Send` previously prompted an `AgentName` into
the void and surfaced herdr's raw not-found error; it now fails before staging
the plan. It is recoverable: the human rebinds. `Send` also gains a dependency
on `ListAgents` succeeding, which is a call relay makes on every daemon tick
already.

No other error contracts change. `ErrBuilderAlive`, `store.ErrNotFound` and
`store.ErrCWDTaken` keep their meanings.

## 10. The accepted risk

A session-less endpoint whose pane is recycled to an agent **of the same kind**
will be adopted as the original builder, and relay will relay into it.

This is real and is not buried. It is the price of having any recovery path for
a builder with no recorded session, and the alternative is the deadlock in #20.
It is bounded on both sides: after this change a session-less endpoint exists
only in the window between `bind` and the first healthy tick -- one poll
interval -- and the third arm of `SameAgent` requires the replacement to be the
same agent kind in the same pane.

relay makes no judgements. Where it cannot tell two agents apart it uses the
best available evidence and this spec says, in the code comment on `SameAgent`,
exactly how good that evidence is.

## 11. Testing

Table tests for `SameAgent` across all three arms, including the negative cases
that carry the design: session recorded but absent from the list, and pane
matching with a different kind.

`Reconcile`:

- broken, session-less, pane and kind match -> `Active`, and `SessionID`
  backfilled from the live agent
- broken, session-less, pane matches with a **different** kind -> stays
  `Broken`, endpoint untouched
- session recorded, that session absent, pane reused by another agent ->
  `Broken` (the section 3.1 strictness change)
- pane moved: same session, new pane id -> `PaneID` refreshed, session
  unchanged
- broken and unlocatable -> neither endpoint is refreshed
- planner located -> planner endpoint refreshed on the same tick

`Bind`:

- session-less builder still present -> `ErrBuilderAlive`, as today
- session-less builder whose pane is gone -> rebind allowed
- adopted builder unaffected in every case above

`Send` / `Answer`:

- address the located agent's pane id, not a stale `AgentName`
- builder unlocatable -> `ErrBuilderGone`, and for `Send`, no plan file staged
  and no log entry appended
- binding rebound between the pre-lock load and the lock -> `ErrBuilderGone`

End-to-end regression for #20: the fake's first `ListAgents` omits the
just-spawned agent, so the binding is created with an empty `SessionID`; one
tick then records the session, and a binding forced to `BROKEN` while
session-less recovers on the following tick.

## 12. Ordered implementation steps

Each step is one deliverable and leaves the tree building and green.

1. **`SameAgent` and the `FindAgent` rewrite.** `internal/relay/herdr.go`, plus
   its table test. Done when the new tests pass and the existing suite still
   does -- any failure here is a real behaviour change from section 3.1 and
   must be read, not patched around.
2. **Delete `builderAlive`.** `bind.go`; `resume` calls `FindAgent`. Correct
   `resolveBuilder`'s "never self-heals" comment. Depends on step 1. Done when
   the bind tests in section 11 pass.
3. **`refreshEndpoint` and the Reconcile head.** Add the function, wire both
   endpoints in, delete the un-break gate. Depends on step 1. Done when the
   six `Reconcile` cases pass.
4. **`Target` becomes pane-id addressing.** Drop the `AgentName` preference and
   correct its doc comment to name the refresh as the guarantee. Point
   `deliver.go:89` at the planner it just located. Depends on step 3, which is
   what makes the stored pane id current. Done when a binding whose `AgentName`
   herdr has forgotten is still addressable.
5. **`ErrBuilderGone` and `Send`.** Pre-lock resolution plus the in-lock
   re-assertion. Depends on step 4. Done when the `Send` cases pass, including
   no plan staged on failure.
6. **`Answer`.** Same pre-lock shape as step 5. Depends on step 5. Done when
   `Answer` addresses the pane it located and refuses when the builder is gone.
7. **End-to-end #20 regression.** Depends on steps 1-6.
8. **Document the identity rule** in `docs/design.md`, beside the
   `planner.session_id` row at line 117: what identifies an endpoint, that
   relay refreshes it from the live agent, and the section 10 risk.

## 13. Out of scope

- The between-rounds diff gap, [#19](https://github.com/fuad-daoud/relay/issues/19).
- Any `--force` rebind override. Section 6 explains why it is not needed.
- Persisting or notifying on an endpoint refresh beyond the existing
  `EventStateChanged` hook.
- `relay ui` changes. `fetch.go` already addresses located agents and the list
  scrolling bug is [#18](https://github.com/fuad-daoud/relay/issues/18).
