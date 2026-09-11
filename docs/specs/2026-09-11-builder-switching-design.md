# Mid-round builder switching: the daemon replaces a gone or gated builder

**Issue:** #61, step 6 of 7 (taken before steps 3, 5 and 7 -- see §1 "Why step 6 next")
**Depends on:** #61 step 2 (policy order, resolver, pick entries; landed in #88)
**Amends:** `docs/specs/2026-09-11-availability-ledger-design.md` §1 "Scope
boundary" ("The reconciler ... none of them read or write the ledger" -- the
reconciler now reads it); CLAUDE.md "Working with builders" second bullet
(relay now closes one more kind of pane); README "Availability"

## 1. System overview

When a builder hits a usage limit half-way through a round, today's sequence
is: the builder goes idle, the daemon nudges it, waits, scrapes its screen
as the round's "report", and the planner reads a quota message where a
report should be. The planner then runs `relay unavailable`, then `relay
bind --resume --assume-dead --builder <other>`, then `relay send` again.
When a builder's pane simply disappears mid-round, the binding turns
`BROKEN` and waits for a human.

This design makes the daemon do the middle part. On each tick, for a
binding whose round is open and whose builder relay itself spawned, two
conditions trigger a **switch**:

1. **Gone.** The builder cannot be located for `switchGrace` (30s). The
   grace is what separates a dead pane from a detection flicker (#20).
2. **Gated.** The ledger holds a live `rate_limited` entry for the
   builder's provider -- i.e. the planner ran `relay unavailable` on it.
   `relay unavailable` is thereby the verb that says "switch now"; no new
   command is added, and relay still reads no screen text for meaning.

A switch is: re-resolve `builder` with the policy order and the ledger's
gates (step 2's rule, with an omitted token), close the replaced pane when
it is still open, spawn the pick beside the planner in the **same
working tree**, hand it the **same round's** plan, and write a `switch`
entry with the pick's explanation. The round number does not change. The
new builder inherits the partial diff in the tree, which `CaptureRoundDiff`
already handles. After `max_switches` in one round, or when the resolver
refuses, the binding goes `NEEDS YOU` with the reason.

### Why step 6 next

#61 orders the scorer (step 3), the TUI tab (step 5) and the history (step
7) before switching. After step 2 the scorer has no non-zero input: the
order is the rule, the gates are hard, and cost/fitness/peak/quality have no
data until step 7. Switching, by contrast, needs only what step 2 built --
"re-score" is "re-resolve" -- and fixes a failure hit this afternoon. Steps
3, 5 and 7 are reconsidered after this lands (§1 of the next spec says
whether they still earn their place).

### Scope boundary

In scope: the two triggers; `switchBuilder`; `Policy.MaxSwitches`;
`Binding.RoundSwitches`, `Binding.BuilderMissingSince`; the `switch` log
entry; `status` showing the switch count; `relay unavailable` naming the
bindings the daemon will switch; docs.

Out of scope, unchanged:

- Detecting a rate limit from the builder's screen. The planner (or a
  later step) says so with `relay unavailable`.
- Adopted builders (`BuilderCandidate == ""`): relay did not spawn them
  and does not replace them. They turn `BROKEN` as today.
- A builder gone **between** rounds (no round open): `BROKEN` as today;
  the planner's next `send` fails with `ErrBuilderGone` and the planner
  rebinds. Nothing to resend, so nothing to switch to.
- Consults. A gated reviewer finishes or is reaped; it is never switched.
- Where the replacement lands: always a split beside the planner, even if
  the original was in its own tab (`--tab`). Tab placement is not recorded
  on the binding.
- `spawn_failed` gates do not trigger a switch: the builder is running.
- Scoring, `relay policy explain`, the TUI tab, history (#61 steps 3, 5, 7).

## 2. File structure

```
internal/policy/policy.go               Policy.MaxSwitches (*int), Policy.SwitchLimit(), validation
internal/policy/policy_test.go          absent → 2; 0 → 0; negative → ErrBadPolicy
internal/store/types.go                 Binding.RoundSwitches, Binding.BuilderMissingSince
internal/store/log.go                   KindSwitch
internal/relay/reconcile.go             gone/gated triggers in Reconcile; queueReport resets RoundSwitches
internal/relay/switch.go                switchGrace, switchBuilder, gatedBuilder, switchEntry   (new)
internal/relay/bind.go                  resolveBuilder takes tx *store.Tx (nil = caller holds no lock); callers pass nil
internal/relay/ledger.go                recordSpawnFailureLocked, mutateLedgerLocked
internal/relay/switch_test.go           the trigger and switch matrix against fakeHerdr           (new)
internal/relay/status.go                BindingStatus.Switches; RenderStatus "switched Nx"
internal/relay/status_test.go
internal/relay/ledger.go                BindingsOnProvider (pure) for the CLI note
internal/relay/ledger_test.go
cmd/relay/main.go                       cmdUnavailable prints "the daemon will switch: ..."
README.md                               "Mid-round switching" under "Availability"; policy.json gains max_switches
CLAUDE.md                               "Working with builders": relay closes a replaced builder's pane
docs/design.md                          log.jsonl kinds gain "switch"; failure handling row
```

## 3. Data structures and type definitions

### 3.1 `policy.Policy` (extended)

```
type Policy struct {
    Order       map[string][]string `json:"order,omitempty"`
    MaxSwitches *int                `json:"max_switches,omitempty"`   // nil = default
}

const DefaultMaxSwitches = 2

func (p Policy) SwitchLimit() int   // *MaxSwitches, or DefaultMaxSwitches when nil
```

| field | required | constraint |
|---|---|---|
| `max_switches` | no | integer ≥ 0; `0` disables switching (every trigger halts instead); absent = 2; negative → `ErrBadPolicy` |

A pointer so "absent" and "0" stay distinct: absent is the default, `0`
is the planner turning the feature off.

### 3.2 `store.Binding` (extended)

```
// RoundSwitches counts builder switches in the current round. Reset when
// the round advances (queueReport). Compared against Policy.SwitchLimit().
RoundSwitches int `json:"round_switches,omitempty"`

// BuilderMissingSince is when the daemon first failed to locate the
// builder during the current absence; zero while it is located. Stamped
// on the first miss, cleared on any hit, so a flicker never accumulates.
BuilderMissingSince time.Time `json:"builder_missing_since,omitempty"`
```

### 3.3 `store.KindSwitch`

```
KindSwitch Kind = "switch"   // relay -> log only: the builder was replaced mid-round, and why
```

Always `Confirmed: true`, `Direction: DirToPlanner` (the same reasoning as
`KindPick`: never a pending payload). `Note` is
`switched builder (<reason>): <ExplainResolution("builder", res)>`, where
`<reason>` is `gone for 31s` or `rate-limited: <ledger note>`.

### 3.4 `relay.BindingStatus` (extended)

```
Switches int `json:"switches,omitempty"`   // Binding.RoundSwitches
```

Rendered on the builder line after the candidate: `switched 1x`. Absent
when zero.

### 3.5 Constants (`internal/relay/switch.go`)

```
// switchGrace is how long a builder must be unlocatable before the daemon
// replaces it. Measured from Binding.BuilderMissingSince. It is the same
// 30s as startGrace and for the same reason: herdr's view lags reality, and
// a replacement spawned on a flicker orphans a live builder (#20).
const switchGrace = 30 * time.Second
```

## 4. Interface definitions and component contracts

### 4.1 Triggers in `Reconcile`

Between the consult pass and the existing `FindAgent` result handling:

```
builder, ok := FindAgent(agents, b.Builder)
switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
if !ok:
    if b.BuilderMissingSince.IsZero(): b.BuilderMissingSince = now
    b.State = StateBroken
    if switchable && now.Sub(b.BuilderMissingSince) >= switchGrace:
        return switchBuilder(ctx, rt, tx, b, "gone for "+dur, /*closeOld*/ false)
    return b, nil                                  -- as today
b.BuilderMissingSince = time.Time{}
… existing Broken→Active recovery, refreshEndpoint …
if switchable:
    if g, gated := gatedBuilder(rt, b); gated:
        return switchBuilder(ctx, rt, tx, b, "rate-limited: "+g.Note, /*closeOld*/ true)
… round cap, status switch, deliverAndSettle as today …
```

`switchBuilder` returns without `deliverAndSettle`, like the halt paths:
the tick that replaces a builder does nothing else to that binding.

### 4.2 `relay.gatedBuilder`

```
func gatedBuilder(rt Runtime, b store.Binding) (ledger.Gate, bool)
```

Pure over `Gates(rt)`: the first gate with `Token == b.BuilderCandidate`
and `Kind == ledger.RateLimited`. `SpawnFailed` gates are ignored -- a
running builder is not a failed spawn.

### 4.3 `relay.switchBuilder`

```
func switchBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reason string, closeOld bool) (store.Binding, error)
```

Pre: `b.BuilderCandidate != ""`, a round is open, the lock is held.
Post (success): `b.Builder` is the new endpoint, `b.BuilderCandidate` the
pick, `b.RoundSwitches` incremented, `b.BuilderMissingSince` zero,
`b.RoundStartedAt = now`, `b.State = StateActive`, `BuilderScreen*`
cleared, one `switch` entry appended, the new builder prompted with the
current round's plan, the planner notified once.

```
limit := rt.Policy.SwitchLimit()
if b.RoundSwitches >= limit:
    return haltBinding(ctx, rt, b, fmt.Sprintf("%s: builder %s (%s); already switched %d time(s) this round (max_switches %d)", b.Name, reason, b.BuilderCandidate, b.RoundSwitches, limit))
res, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), "", "builder")
if err != nil:
    return haltBinding(ctx, rt, b, fmt.Sprintf("%s: builder %s (%s); cannot switch: %v", b.Name, reason, b.BuilderCandidate, err))
if closeOld:
    if err := rt.Herdr.ClosePane(ctx, b.Builder.PaneID); err != nil:
        return haltBinding(ctx, rt, b, fmt.Sprintf("%s: builder %s; could not close its pane %s to replace it: %v", b.Name, reason, b.Builder.PaneID, err))
    -- the agent name <name>-builder is free only once the old agent is gone
ep, _, err := resolveBuilder(ctx, rt, tx, BindOptions{Candidate: res.Token(), CWD: b.CWD}, b.Name, b.Planner.PaneID)
if err != nil:
    -- resolveBuilder already recorded spawn_failed for the pick, which gates
    -- it for the next resolution. Count the attempt, leave the binding for
    -- the next tick: with the old pane closed (or gone) the gone trigger
    -- fires again after switchGrace and walks to the next candidate.
    b.RoundSwitches++
    b.State = StateBroken
    slog.Warn("builder switch failed", "binding", b.Name, "round", b.Round, "pick", res.Token(), "err", err)
    return b, nil
b.Builder = ep; b.BuilderCandidate = res.Token(); b.RoundSwitches++
b.BuilderMissingSince = time.Time{}; b.BuilderScreen = ""; b.BuilderScreenAt = time.Time{}
b.State = StateActive
tx.AppendLog(b.Name, switchEntry(now, b.Round, reason, res))
text := composePrompt(b, PlanPath(b.Name, b.Round), ReportPath(b.Name, b.Round))
if err := promptWithRetry(ctx, rt, ep.PaneID, text); err != nil:
    return haltBinding(ctx, rt, b, fmt.Sprintf("%s: switched builder to %s but could not hand it round %d: %v", b.Name, res.Token(), b.Round, err))
b.RoundStartedAt = now
rt.Herdr.Notify(ctx, fmt.Sprintf("%s: switched builder to %s (%s)", b.Name, res.Token(), reason))   -- error logged, not returned
slog.Info("builder switched", "binding", b.Name, "round", b.Round, "from", old, "to", res.Token(), "reason", reason, "switches", b.RoundSwitches)
return b, nil
```

Why `resolveBuilder`: it already does launch rendering, the split beside
the planner, `StartAgent`, `recordSpawnFailure` on error, and the
best-effort session lookup.

**The lock.** `Reconcile` runs inside `Store.WithLock`, and
`recordSpawnFailure` takes that same non-reentrant lock through
`mutateLedger` -- so `switchBuilder` calling today's `resolveBuilder` would
deadlock the daemon on the first failed replacement spawn (found by the T2
builder, goroutine dump in its round-2 report). `resolveBuilder` therefore
gains a `tx *store.Tx` parameter: `nil` means the caller holds no lock and
the spawn-failure record locks as today; non-nil means the caller holds it
and the record is written through `recordSpawnFailureLocked`, which is the
same append without the `WithLock`. `create`, `resume`, `Add` and `Fork`
pass `nil` (they call it before their own `WithLock`); `switchBuilder`
passes its `tx`. The `Tx` is a witness that the lock is held, not something
the ledger writes through -- the ledger file stays outside the store's
transaction. Its own `Resolution` is `explicit` (we hand it
the token) and is discarded; the recorded one is `res`, exactly as `Add`
and `Fork` do (policy-order spec §4.3).

Why `RoundStartedAt = now`: the new builder gets the same `startGrace`
before a nudge and the same round budget as a fresh handoff. The round
*number* is unchanged (#61); the *clock* restarts.

Why close before spawn: herdr agent names are unique and the replacement
is `<name>-builder` again. With the old agent alive, `StartAgent` would
refuse the name. For the gone trigger there is nothing to close.

### 4.4 `relay.switchEntry`

```
func switchEntry(now time.Time, round int, reason string, res Resolution) store.LogEntry
```

Pure: `{TS: now.UTC(), Round: round, Direction: DirToPlanner, Kind: KindSwitch, Confirmed: true, Note: "switched builder (" + reason + "): " + ExplainResolution("builder", res)}`.

### 4.5 `queueReport` (modified)

Alongside `b.HaltNotifiedRound = 0`: `b.RoundSwitches = 0`.

### 4.6 `relay.BindingsOnProvider`

```
func BindingsOnProvider(bindings []store.Binding, provider string) []string
```

Pure. Names of bindings that are `StateActive`, have an open round
(`RoundStartedAt` non-zero), a non-empty `BuilderCandidate` whose
`ParseRef(...).Provider == provider`. Sorted. `cmdUnavailable` prints, after
its existing line, `the daemon will switch: a, b` when non-empty.

### 4.7 `status`

`Status` copies `RoundSwitches` into `BindingStatus.Switches`. `RenderStatus`
appends `   switched 1x` to the builder line when non-zero.

## 5. High-level pseudocode

### 5.1 One tick, rate-limited builder

```
planner: relay unavailable claude/anthropic/sonnet --reason "5h window"
   → ledger: rate_limited anthropic; CLI: "gated anthropic (2 candidates) until cleared" / "the daemon will switch: webshop"
daemon tick:
   Reconcile(webshop): builder located; switchable; gatedBuilder → yes
   switchBuilder(reason "rate-limited: 5h window", closeOld true):
       RoundSwitches 0 < 2
       resolve "" → order walk skips sonnet (gated) → opencode (order #3)
       ClosePane(wM:p5)
       resolveBuilder(opencode) → split beside planner, StartAgent webshop-builder
       switch entry: "switched builder (rate-limited: 5h window): picked opencode/... for builder: order #3; skipped claude/anthropic/sonnet (rate-limited until cleared)"
       prompt round 2's plan; RoundStartedAt = now
       Notify "webshop: switched builder to opencode/... (rate-limited: 5h window)"
```

### 5.2 One tick, builder gone

```
tick 1: FindAgent miss → BuilderMissingSince = now, Broken
tick k (≥30s later): still missing → switchBuilder(reason "gone for 31s", closeOld false) → as above without ClosePane
tick k' (builder reappeared before 30s): hit → BuilderMissingSince cleared, Broken→Active as today
```

### 5.3 Exhaustion

```
switch #1 ok; switch #2 ok; third trigger: RoundSwitches 2 >= limit 2 → haltBinding NEEDS YOU
   "webshop: builder gone for 31s (opencode/...); already switched 2 time(s) this round (max_switches 2)"
```

## 6. Error handling strategy

| condition | outcome | recoverable |
|---|---|---|
| `RoundSwitches >= SwitchLimit()` | `NEEDS YOU`, notified once per round | yes: fix the cause, `bind --resume` |
| resolver refuses (`ErrAllGated`, `ErrAmbiguousCandidate`, ...) | `NEEDS YOU` with the resolver's text | yes: `relay available`, set the order, or `bind --resume --builder` |
| `ClosePane` fails | `NEEDS YOU` | yes: close it by hand, rebind |
| spawn fails | `spawn_failed` recorded (by `resolveBuilder`), `RoundSwitches++`, `BROKEN`; the next tick's gone trigger walks on | automatic, bounded by `max_switches` |
| prompt fails after spawn | `NEEDS YOU` naming the new pane; the builder is running with no plan | yes: `relay send` again, or rebind |
| `Notify` fails | logged, switch stands | -- |
| ledger unreadable | `Gates` returns empty (existing rule): no gated trigger this tick | -- |

`haltBinding`'s once-per-round notification dedup applies to every halt
above. A switch never runs on an adopted builder, a closed round, or a
`DONE` binding (the `DONE` return precedes it).

The `switch` entry is the audit trail; `relay log` shows it like any entry.
`slog` lines at `Info` (switched) and `Warn` (switch failed) follow #78's
convention.

## 7. Ordered implementation steps

1. **Policy `max_switches`, binding fields, `KindSwitch`, `queueReport`
   reset.** §3.1–3.3, §4.5. Tests: `policy` load matrix rows (absent → 2,
   `0` → 0, `-1` → `ErrBadPolicy`, `"two"` → `ErrBadPolicy`);
   `store` round-trip of the two fields; `queueReport` zeroes
   `RoundSwitches` (extend an existing reconcile test that closes a round).

2. **`switch.go` and the triggers.** §3.5, §4.1–4.4, §5. Tests in
   `switch_test.go` against `fakeHerdr` (`seedBound` + `Send` to open a
   round, `Reconcile` driven directly with a hand-built agents slice and a
   fixed clock):
   - gone: miss stamps `BuilderMissingSince`, state `BROKEN`, no spawn;
     miss at +29s: still no spawn; miss at +31s: new agent started, same
     round, `switch` entry with `gone for 31s`, prompt text contains the
     round's plan path, `RoundStartedAt` reset, `RoundSwitches == 1`,
     `notices` has one line, nothing closed;
   - gone then reappears at +10s: `BuilderMissingSince` cleared, `ACTIVE`;
   - gated: `Unavailable` on the builder's token, then one tick: old pane
     closed, new agent started from the next ungated in order, `switch`
     entry `rate-limited: <note>`;
   - gated with explicit-bound builder: same (the order applies on switch);
   - `max_switches` 0: gated tick halts, nothing spawned, `NEEDS YOU`;
   - two switches then a third trigger: `NEEDS YOU`, notified once;
   - everything gated: `NEEDS YOU` with `ErrAllGated` text, nothing spawned;
   - spawn failure: `RoundSwitches == 1`, `BROKEN`, one `spawn_failed` in
     the ledger, no `switch` entry; a later tick past the grace switches to
     the next candidate;
   - adopted builder (`BuilderCandidate == ""`) gone for 60s: `BROKEN`, no
     spawn; no round open: `BROKEN`, no spawn;
   - a switch tick does not deliver a pending payload (mirror the halt
     tests).
   Mutations for the planner: drop the grace comparison (the +29s test
   fails); drop `closeOld` (the gated test's `closed` assertion fails);
   count switches from 1 instead of 0 (the exhaustion test fails).

3. **Status, CLI note, docs.** §3.4, §4.6, §4.7. `BindingsOnProvider`
   test; `RenderStatus` fixture with `Switches: 1`; `cmdUnavailable` line
   (no subcommand test); README "Mid-round switching" (what triggers it,
   what it does, `max_switches`, what `NEEDS YOU` means, the pane relay now
   closes); CLAUDE.md bullet; `docs/design.md` `log.jsonl` kinds and the
   failure-handling section.

Planner verification across the change: check constituents; `git diff
--stat` against §2; the three mutations; on this machine with a temp
`XDG_STATE_HOME`, a real daemon (`relay daemon --interval 2s` against the
temp state), a real bind + send, then `relay unavailable` on the builder's
token and watch `relay status` / `relay log` show the switch and the old
pane close; then `herdr pane close` on the replacement and `unbind`.
