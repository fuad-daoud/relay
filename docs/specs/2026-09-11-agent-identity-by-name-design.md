# Agent identity by name; a sub-agent's status is not the builder's

**Issue:** #66
**Amends:** `SameAgent`'s doc comment (`internal/relay/herdr.go`), the
identity rules from `docs/specs/2026-09-08-builder-identity-recovery-design.md`

## 1. System overview

herdr's agy integration reports whichever agy session is in the foreground of
a pane. While a builder waits on a sub-agent, `herdr agent list` returns the
sub-agent's session id and the sub-agent's status for the builder's pane; when
the sub-agent finishes, the parent's come back. Observed twice on 2026-09-11,
deterministically, on the first sub-agent dispatch of each round.

relay's `SameAgent` treats a recorded session id as exact identity and does
not fall back to anything else. So every agy round now flips to `NEEDS YOU`
with `builder … gone` and the builder's own pane listed as a foreign agent,
the `status` line advises a rebind-and-resend that would double-submit the
plan, and recovery needs `--assume-dead` on a builder that is alive. If the
binding is not broken at that moment, `handleIdleBuilder` reads the
sub-agent's `idle`/`done` as the round finishing and nudges -- which last time
made the builder kill its sub-agents.

This design does two things.

1. **Identity by the name relay chose.** Every agent relay starts is named by
   relay -- `<binding>-builder`, `<binding>-<role>-<id>` -- and that name is
   unique by construction and stable for the agent's life: it survives a pane
   moved between workspaces (#21) and a session that flips. `herdr.Agent`
   gains the `name` field it has never parsed, and `SameAgent` matches on it
   first. Session stays the rule for endpoints relay did not name (the
   planner, an adopted pane), exactly as before: a session mismatch means gone,
   with no pane fallback.
2. **A foreign foreground session means busy, not done.** When the located
   agent carries a session other than the recorded one, its `idle` or `done`
   is a sub-agent's and says nothing about the builder. Those two statuses
   are read as `working`. `blocked` is left alone: a sub-agent stuck on a
   dialog blocks the parent too, and the dialog capture is exactly what a
   human wants then.

Neither change is a judgement about the agent's work. Both are readings of
herdr data that account for a documented quirk of one integration, in the
way screen-fingerprint quiescence already accounts for a builder that is
quiet because its own sub-agents are running.

### Why there is still no pane fallback

Round 1 of the plan tried falling back from a session mismatch to pane id
+ kind for nameless endpoints. `TestBindRebindIdentityRule/rebind succeeds
when old pane is reused by unrelated agent` showed the cost: a dead
builder's pane taken by an unrelated agent reads as "alive" and the
documented #21 recovery is refused. An adopted pane that runs sub-agents
is the only case the fallback would have served, and relay cannot name an
adopted pane. The 2026-09-08 rule stands for nameless endpoints.

### Scope boundary

In scope: `herdr.Agent.Name`, `SameAgent`, the effective-status rule and its
use in `Reconcile`, `reconcileConsults` and `Status`, `DiagnoseBuilder`'s
notion of "identified", test fixtures that now need names.

Out of scope, unchanged:

- Why agy/herdr report the foreground session. Upstream's question.
- herdr's `idle` for a builder waiting on sub-agents when the session does
  **not** flip (a harness that runs sub-agents in-process). The screen
  fingerprint path covers that as before.
- `--assume-dead` itself. It stays for the case it was built for: a
  session-less builder whose pane is genuinely gone with a round open.

## 2. File structure

```
internal/herdr/types.go            Agent.Name
internal/herdr/types_test.go       ParseAgentList carries name (extend an existing parse test)
internal/relay/herdr.go            SameAgent by name, then session with pane+kind fallback; rewritten comment
internal/relay/herdr_test.go       identity matrix
internal/relay/reconcile.go        effectiveStatus; used after FindAgent for the builder
internal/relay/reconcile_test.go   sub-agent idle does not nudge / scrape; blocked still captured
internal/relay/consult.go          effectiveStatus for the consult agent
internal/relay/consult_test.go     sub-agent idle does not finish or nudge a consult
internal/relay/status.go           BuilderStatus / PlannerStatus via effectiveStatus
internal/relay/status_test.go      status shows working under a sub-agent session
internal/relay/diagnose.go         Identified replaces SessionIdentified (name or session)
internal/relay/diagnose_test.go    named-but-sessionless builder is identified
internal/relay/bind.go             resume gate uses Identified
internal/relay/*_test.go           builderAgent/consultAgent fixtures gain Name
```

## 3. Data structures and type definitions

### 3.1 `herdr.Agent` (modified)

```
type Agent struct {
    Name        string  `json:"name"`      // the name given at `herdr agent start`; empty for agents herdr did not start
    Kind        string  `json:"agent"`
    …unchanged…
}
```

### 3.2 `relay.BuilderDiagnosis` (modified)

```
type BuilderDiagnosis struct {
    RoundOpen  bool
    Identified bool   // the builder can be located by name or by session; replaces SessionIdentified
}
```

`Identified = b.Builder.AgentName != "" || b.Builder.SessionID != ""`. The
`movedPaneWarning` text changes from "never session-identified" to "cannot
be identified by name or session".

## 4. Interface definitions and component contracts

### 4.1 `relay.SameAgent` (signature unchanged)

```
func SameAgent(a herdr.Agent, ep store.Endpoint) bool
```

Rules, in order; the first that applies decides:

| recorded on `ep` | live `a` | result |
| --- | --- | --- |
| `AgentName != ""` | `a.Name != ""` | `a.Name == ep.AgentName` |
| `AgentName != ""` | `a.Name == ""` | fall through to the session rule (herdr version without names, or an agent restarted by hand in the pane) |
| `SessionID != ""` | `a.Session.Value == ep.SessionID` | true |
| `SessionID != ""` | session differs or empty | false (unchanged: no pane fallback) |
| neither | — | pane, and kind if recorded (unchanged) |

Postcondition: a builder whose pane reports a sub-agent's session is
located; a builder whose pane moved is located (by name, or by session as
before); a session-less endpoint's pane hosting a different kind is not.

### 4.2 `relay.effectiveStatus` (new, `reconcile.go`)

```
func effectiveStatus(ep store.Endpoint, a herdr.Agent) string
```

Returns `a.Status`, except: when `ep.SessionID != ""` and `a.Session.Value
!= ""` and they differ, `StatusIdle` and `StatusDone` become
`StatusWorking`. Every other status passes through, `StatusBlocked`
included. Pure; documented with the reasoning in §1.

### 4.3 `relay.refreshEndpoint` (unchanged, restated)

Backfills `SessionID` only when empty. It must **not** be changed to
overwrite on mismatch: that would record a sub-agent's session and make the
parent look foreign. The comment gains a sentence saying so.

### 4.4 Consumers of the status

| site | before | after |
| --- | --- | --- |
| `Reconcile`, the `switch builder.Status` | raw | `switch effectiveStatus(b.Builder, builder)` |
| `reconcileConsults`, `idle :=` and the blocked check | raw | via `effectiveStatus(c.Endpoint, agent)` |
| `Status`, `row.BuilderStatus` / `row.PlannerStatus` | raw | via `effectiveStatus` -- what relay acts on is what it shows |
| `DeliverPending`'s planner idle/focus check | raw | unchanged: the planner is claude, reports one session, never flips; leave it |

### 4.5 `DiagnoseBuilder`, `Detail`, `Bind`'s resume gate

`SessionIdentified` → `Identified` everywhere. `bind.go:146` becomes
`!d.Identified && d.RoundOpen && !opts.AssumeDead`. A builder relay started
is always identified from the moment it is recorded, so `--assume-dead` is
never demanded for one; it remains demanded for a session-less adopted
pane with a round open.

## 5. High-level pseudocode

### 5.1 `SameAgent`

```
if ep.AgentName != "" and a.Name != "":  return a.Name == ep.AgentName
if ep.SessionID != "":
    return a.Session.Value == ep.SessionID
if ep.Kind != "":  return a.PaneID == ep.PaneID and a.Kind == ep.Kind
return a.PaneID == ep.PaneID
```

### 5.2 `Reconcile`, builder branch

```
builder, ok := FindAgent(agents, b.Builder)          -- now finds it under a sub-agent
…
b.Builder = refreshEndpoint(b.Builder, builder)      -- session recorded once, never replaced
switch effectiveStatus(b.Builder, builder):
    idle/done  -> handleIdleBuilder                  -- only when the foreground is the recorded session
    blocked    -> handleBlockedBuilder               -- sub-agent dialogs included
    default    -> checkRoundTimeout
```

## 6. Error handling strategy

No new errors. One removed failure mode: `ErrBuilderUnverified` can no
longer be raised for a builder relay started, because such a builder is
always `Identified`.

Observability: `relay status` shows `working` for a builder under a
sub-agent session instead of `idle`/`done`, and stops listing the builder's
own pane as foreign. Nothing new is logged.

## 7. Behavioural rules and their rationale

### 7.1 Name before session

Session was chosen in the 2026-09-08 spec because it survives a pane move.
Name survives that and the session flip, and relay assigned it, so it is
the one identity relay can vouch for. It is also what a human sees in
`herdr agent list`.

### 7.2 Override only idle and done, never blocked

A sub-agent's `idle` means "the sub-agent is idle", which tells relay
nothing about the parent -- the parent is, by construction, waiting on it.
A sub-agent's `blocked` means a dialog is up in the builder's pane and
nothing will progress until a human answers it. That is the situation
`handleBlockedBuilder` exists for.

### 7.3 The recorded session is the first one seen

`refreshEndpoint` records the first session herdr reports and keeps it. In
practice that is the parent's: a builder is prompted and ticks for ~40 s
before its first sub-agent dispatch, and the daemon ticks every 2 s. If a
sub-agent's session were recorded first, name matching still locates the
builder; the only cost is that the parent's own `idle` would be read as
`working` until the next sub-agent run flips it back. Accepted rather than
engineered: relay cannot tell the two sessions apart from the outside.

### 7.4 `Status` shows the effective status

Showing raw `idle` while relay acts on `working` would send a human to
answer "why is relay not nudging an idle builder". One value, shown and
acted on.

## 8. Testing requirements

`internal/relay`:

1. `SameAgent` matrix (`herdr_test.go`): name match wins over a differing
   session and pane; name mismatch loses despite matching session and pane;
   named endpoint vs nameless agent falls to session; session match; session
   mismatch → false regardless of pane; session-less rules unchanged.
2. `effectiveStatus`: same session passes every status; differing session
   turns idle and done into working, leaves working and blocked; empty
   recorded or live session passes through.
3. `Reconcile` with a sent round, builder agent carrying a *different*
   session and `StatusIdle` past `startGrace`: no nudge, no report scrape,
   binding stays `active`, `b.Builder.SessionID` unchanged. Through
   `Daemon.Tick`, not a direct call.
4. Same setup with `StatusBlocked` and a dialog in `readOut`: the question
   is queued as today.
5. `Reconcile` with a builder agent whose session differs but whose name
   matches, in a *different pane id*: located, `PaneID` refreshed, not
   broken.
6. `reconcileConsults`: consult agent with a differing session and
   `StatusIdle`, no findings: not nudged, not finished, still running.
7. `Status`: builder under a differing session and `StatusDone` renders
   `working`, and `Foreign` is empty.
8. `DiagnoseBuilder`: `AgentName` set, `SessionID` empty → `Identified`;
   `Bind --resume --builder` on such a binding with a round open does not
   demand `--assume-dead` (extend the existing gate test).
9. `herdr.ParseAgentList` carries `name` (extend the existing parse test's
   fixture with a `"name"` field).

Fixtures: `builderAgent` and `consultAgent` gain `Name` matching what
`Bind`/`Ask` record (`webshop-builder`, `webshop-reviewer-7f2a3c1d`);
`agentAt` in `foreign_test.go` stays nameless.

Mutation checks for the report: remove the name rule from `SameAgent`
(test 1's "name wins over session" case and test 5 must fail); make
`effectiveStatus` return `a.Status` unconditionally (test 3 must fail);
make it also override `blocked` (test 4 must fail).

## 9. Explicitly out of scope

- Any change to `DeliverPending`'s planner checks.
- Recording more than one session per endpoint.
- The nudge itself, `startGrace`, or the screen-fingerprint path.
- #64's name-length validation, though it touches the same names.
- A pane fallback for nameless endpoints (tried in round 1, withdrawn).
