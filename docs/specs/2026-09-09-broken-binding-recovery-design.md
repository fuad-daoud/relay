# Telling the three broken bindings apart

Status: design approved, not yet implemented.
Issues: [#41](https://github.com/fuad-daoud/relay/issues/41),
[#21](https://github.com/fuad-daoud/relay/issues/21).
Date: 2026-09-09.

## 1. System overview

`StateBroken` is documented as "builder pane is gone"
(`internal/store/types.go:14`) and covers three situations relay does not
distinguish. In one of them the correct action is nothing at all; in another the
obvious action -- `relay bind --resume --builder` -- spawns a replacement and
orphans a builder that is still running the round. They render identically, as
`NEEDS YOU`.

This spec does not add display states and does not try to locate a moved pane.
It derives two facts relay already stores, and spends them in two places:

1. **`relay status` says which broken case it is** and what to do about it
   (#41, options 1 and 3).
2. **`relay bind --resume --builder` refuses** when relay cannot tell a dead
   builder from a moved one, until the human says so explicitly (#21,
   direction 2).

Both consumers read the same two facts from one pure function, which is the
whole of the shared foundation.

## 2. Constraints the design is derived from

1. **relay cannot invent identity a harness does not report.** The moved-pane
   case (#41 row 3) is not detectable. This spec labels it and refuses to act
   on a guess; it does not resolve it. Corroborating a moved pane by cwd and
   kind was considered and rejected in #21 as a guess relay should not make.

2. **relay does not act on the user's own initiative.** No auto-rebind, no
   auto-resend, no reaping. Naming the situation is the whole ask.

3. **The three-state display collapse is good design.** `docs/design.md`
   defends it. `displayState` (`internal/relay/status.go:137`) is not touched,
   and no fourth display word is added.

4. **`relay` is driven by agents as often as by humans.** A planner agent runs
   `relay bind --resume`. An interactive y/N prompt could hang a non-TTY caller,
   so the refusal is an error plus an opt-in flag, never a prompt.

5. **The refusal must land before anything is spawned.** `resume` carries an
   `// IRREVERSIBLE: a pane may now exist. Never closed by relay.` line
   (`internal/relay/bind.go`). Every new check goes above it.

## 3. The two facts

Both are read from the stored `Binding` alone -- no herdr call, no log read.

### 3.1 RoundOpen: was work in flight when the builder went away

`RoundOpen = !b.RoundStartedAt.IsZero()`

`RoundStartedAt` is stamped in exactly one place, `Send` at handoff
(`internal/relay/send.go:122`), and zeroed in exactly one place, `queueReport`
once the round's report has been logged and `b.Round` incremented
(`internal/relay/reconcile.go:430`). So a zero value means "the current round
has not been handed to the builder", which is precisely "no work is
outstanding".

This is a refinement of what #41 proposed. The issue suggested walking the round
log for "a report for round N exists". `RoundStartedAt` answers the same
question without a log read, and `reconcile.go:269` already uses
`RoundStartedAt.IsZero()` for exactly this meaning on the nudge path, so the
semantics are established in the codebase rather than invented here.

Note the off-by-one that the wording must respect: `queueReport` increments
`b.Round` *after* logging the report, so when `RoundOpen` is false the report
that was delivered belongs to round `b.Round - 1`.

### 3.2 SessionIdentified: can relay tell dead from moved

`SessionIdentified = b.Builder.SessionID != ""`

`refreshEndpoint` backfills `SessionID` the first time the endpoint is located
and the agent reports one, and never overwrites a recorded one
(`internal/relay/reconcile.go`). So an empty `SessionID` means herdr has never
reported a session for this builder, and `SameAgent` has been matching on pane
plus kind -- the one field a workspace move invalidates.

When this is false, "not found" is genuinely ambiguous: the builder may be dead,
or may be alive in a pane whose id changed.

### 3.3 They are orthogonal

A builder that was never session-identified may be mid-round or idle, and a
session-identified one may be either too. The four combinations are all
reachable, so the detail line carries both facts rather than choosing one of
#41's three rows.

## 4. Which harnesses are affected

Verified live on 2026-09-09 (herdr integration version 10):

| harness | reports a session | when |
| --- | --- | --- |
| `claude` | yes | at spawn |
| `agy` | yes | first turn |
| `opencode` | yes | first turn |

`opencode` was unverified in both issues; it is in the same population as `agy`.
A freshly spawned, idle opencode pane carries no `agent_session`; after one
prompt it carries `ses_...` from `source: herdr:opencode`; and moving that pane
between workspaces changed `pane_id` while the session was unchanged.

The mechanism is that both halves of the opencode integration are turn-driven.
`plugins/herdr-agent-state.js` deliberately reports nothing on `session.created`
(server-global, so an attached client may own it) and suppresses the same id on
`session.updated`; the first call carrying a session id is `chat.message` or
`session.status`. `herdr-tui-session.js` polls the route every 100ms but only
reports on a `session` route, and a fresh opencode sits on the splash screen.

**Consequence for scope.** Both of relay's shipped *builder* aliases are
late-session: `builder` is opencode and `abuilder` is agy. `claude`, the immune
harness, is the planner. So the ambiguous window is not an agy quirk -- it is
every builder harness relay ships. The window itself is narrow, closing at the
builder's first turn, which in practice is shortly after `bind` sends the round.

## 5. File structure

```
internal/relay/
  diagnose.go         NEW. The two facts and the detail sentence. Pure.
  diagnose_test.go    NEW. Table test over the six composed sentences.
  status.go           MODIFIED. BindingStatus.Detail, populated + rendered.
  bind.go             MODIFIED. ErrBuilderUnverified, AssumeDead, the gate.
  herdr.go            MODIFIED. SameAgent doc comment corrected (section 9).
cmd/relay/
  main.go             MODIFIED. --assume-dead flag, wired to BindOptions.
```

No new packages. No store schema change. No migration.

## 6. Interfaces

### 6.1 `internal/relay/diagnose.go`

```
// BuilderDiagnosis explains a broken binding: what was at stake when the
// builder went away, and whether relay can trust that it is really gone.
// Both fields are derived from the stored binding alone.
type BuilderDiagnosis struct {
    RoundOpen         bool
    SessionIdentified bool
}

// DiagnoseBuilder derives the diagnosis. Pure; no herdr call, no log read.
DiagnoseBuilder(b store.Binding) BuilderDiagnosis

// Detail renders the sentence for `relay status`. It is total: every
// combination yields a non-empty sentence, so a caller never has to handle
// an empty return as a special case.
(BuilderDiagnosis) Detail(round int) string
```

`Detail` takes the round so it can name the delivered report's round; it does
not take the whole binding, which keeps it a pure formatting function.

### 6.2 Detail text

The sentence is composed of two clauses rather than enumerated over the 2x2,
because the two facts are independent (3.3) and enumeration would duplicate
wording. Exact strings, so status tests can assert them. `N` is `b.Round`.

**Clause A -- what was at stake.** Note this is a three-way split, not two:
`RoundOpen` is also false for a binding that was bound but never sent, where
naming a delivered report would be wrong.

| Condition | Clause A |
| --- | --- |
| `RoundOpen` | `round N was open -- that work is unaccounted for` |
| `!RoundOpen && round > 1` | `round N-1 report delivered; nothing outstanding` |
| `!RoundOpen && round <= 1` | `no round has been sent yet; nothing outstanding` |

`N-1` is written as the computed integer, not the literal text `N-1`.

**Clause B -- whether relay can trust that the builder is gone.**

| Condition | Clause B |
| --- | --- |
| `SessionIdentified` | *(empty)* |
| `!SessionIdentified` | `the builder was never session-identified, so it may be alive in a moved pane -- verify before rebinding` |

**Composition.** Join a non-empty Clause B to Clause A with `, and `. When
Clause B is empty and `RoundOpen` is true, append `; rebind and resend the
round` instead -- that is the recovery, and it is only safe to advise when the
builder is positively identifiable. When Clause B is empty and `RoundOpen` is
false, append ` -- unless you want another round` when `round > 1`, or ` --
unless you want to send one` when `round <= 1`; a binding that was never sent
has no *another* round to want.

The `round <= 1 && !SessionIdentified` case is the exact window #21 describes: a
builder bound but not yet sent its first round, whose session has therefore
never been reported. Its sentence carries Clause B, which is the warning that
matters most there.

### 6.3 `internal/relay/status.go`

```
BindingStatus.Detail string `json:"detail,omitempty"`
```

Additive and `omitempty`, so an existing `--json` consumer is unaffected.

Populated in `statusRow` only when `b.State == store.StateBroken`. `orphaned`
and `needs_you` are unambiguous and stay untouched; `displayState` is not
modified.

`RenderStatus` prints, after the builder line and before `last`:

```
  detail   <text>
```

only when `row.Detail` is non-empty. `Detail` itself is total (6.1); the
emptiness being tested here is that `statusRow` never populated the field for a
non-broken binding. Those render byte-identically to today.

### 6.4 `internal/relay/bind.go`

```
// ErrBuilderUnverified reports that relay cannot distinguish a dead builder
// from one whose pane moved, because no session was ever recorded for it.
ErrBuilderUnverified error

BindOptions.AssumeDead bool
```

Message:

```
builder for %q was never session-identified, so relay cannot tell a dead
builder from a moved pane. Check %s is really gone, then re-run with
--assume-dead.
```

where `%s` is `b.Builder.PaneID`.

### 6.5 `cmd/relay/main.go`

```
assumeDead := fs.Bool("assume-dead", false,
    "confirm a builder relay cannot verify is gone is really gone")
```

wired to `BindOptions.AssumeDead`, beside the existing `resume` flag.

## 7. The gate

In `resume`, inside the existing `if rebinding` block, immediately after the
`ErrBuilderAlive` check and above the `// IRREVERSIBLE` line:

```
if _, alive := FindAgent(agents, b.Builder); alive {
    return store.Binding{}, ErrBuilderAlive
}
// NEW:
if b.Builder.SessionID == "" && !opts.AssumeDead {
    return store.Binding{}, fmt.Errorf(...ErrBuilderUnverified...)
}
```

The gate keys on the live evidence -- `FindAgent` missed, and no session is
recorded -- rather than on `b.State == store.StateBroken`. Keying on the stored
state would make the gate's behaviour depend on whether the daemon had ticked
since the pane went away, which would make it intermittent and untestable.

Note the two checks are exclusive by construction: the first returns when the
builder *was* found, so the second is only reached when it was not.

`--assume-dead` has no effect on any path other than this one. In particular it
never overrides `ErrBuilderAlive`: a builder relay can positively see is alive
is still refused, which is #20's guarantee and is not weakened here.

## 8. Error handling

| Condition | Result | Recoverable |
| --- | --- | --- |
| builder located, alive | `ErrBuilderAlive` | yes, by not rebinding |
| builder missing, session recorded | proceeds as today | n/a |
| builder missing, no session, no flag | `ErrBuilderUnverified` | yes, by verifying then passing `--assume-dead` |
| builder missing, no session, flag set | proceeds | n/a |

Both errors are returned before any pane is spawned, so neither leaves a
partially-created builder. Neither is wrapped in a way that hides it from
`errors.Is`.

`Detail` never errors and never panics on a zero-value binding.

## 9. Documentation correction

`SameAgent`'s doc comment (`internal/relay/herdr.go`) states the session-less
exposure lasts "for agy lasting until the agent has begun a conversation". That
is now known to be incomplete: section 4 verified opencode behaves identically.
The comment is the one a future reader will trust when reasoning about this
exact risk, so it is corrected to name both harnesses.

## 10. Testing

**`diagnose_test.go`** -- table over the six sentences 6.2 composes (three
Clause A conditions x two Clause B), asserting exact strings. Plus: a
zero-value binding does not panic, and `round == 1` never renders `round 0`.

**`status_test.go`** -- a broken binding renders the `detail` line; a non-broken
binding renders byte-identically to today; `detail` is absent from `--json`
when empty.

**`bind_test.go`** -- resume refuses with `ErrBuilderUnverified` when the
builder is missing and session-less; proceeds with `AssumeDead`; is unaffected
when a session is recorded; and still returns `ErrBuilderAlive` for a live
builder even with `AssumeDead` set.

## 11. Explicitly out of scope

- **A fourth display word.** `displayState` is unchanged. Splitting the stored
  state and giving the benign case its own word (#41 option 2) was considered
  and deferred; it touches `Reconcile`, the UI and the `--json` contract, and
  the detail line should first show which cases actually occur.
- **Detail for `orphaned` or `needs_you`.** Only `broken` is overloaded.
- **Locating a moved pane.** Not possible without identity the harness does not
  report.
- **Anything in #21 beyond direction 2.** Directions 3 and 4 stay open.
