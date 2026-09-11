# A held payload is delivered when the planner's input is empty or its screen has been quiet

**Issue:** #69

## 1. System overview

When a builder's report is ready and the human is sitting in the planner
pane, `DeliverPending` (`internal/relay/deliver.go`) holds the payload and
fires one `herdr notify`. It then holds for as long as the pane stays
focused: a planner left focused overnight receives nothing until the human
clicks away or runs `relay pull`. The hold is the anti-clobber rule --
`herdr agent prompt` types text and presses Enter, and herdr cannot see the
human's input buffer -- and focus was the only proxy relay had for "a human
may be mid-sentence here".

Two better signals exist now, and relay already uses the second of them on
the builder side:

1. `herdr agent read <pane> --source visible` returns the rendered screen,
   input box included. On an idle claude planner the box is a bare `❯` line;
   with a draft it is `❯ some text`. An empty box has nothing to clobber.
2. Whether the screen changes between ticks says whether the human is
   typing. `builderQuiescent` (`internal/relay/reconcile.go`) already turns
   "unchanged for `nudgeGrace`" into "stopped, not quiet".

This design replaces the bare focus check with a two-signal hold: inject at
once when the input box is known to be empty; otherwise fingerprint the
screen and inject once it has been unchanged for `HeldGrace` (default 60s).
The grace clock runs from the last screen *change*, not from when the hold
began, because injecting mid-keystroke is the worst outcome and screen
motion is the one signal that catches it. Occasionally appending to an
abandoned draft is accepted, on purpose: it beats a payload that never
arrives.

### Scope boundary

In scope: the focused branch of `DeliverPending`; the `inputEmpty` detector
for `claude`; `PlannerScreen`/`PlannerScreenAt` on `store.Binding`;
`Runtime.HeldGrace` and `relay daemon --held-grace`; README and
`docs/design.md` passages that describe the old rule.

Out of scope: per-binding grace or a config file for it; a hold-progress
column in `relay status`; `agy`/`opencode` prompt markers (follow-up, once
their idle screens are captured); anything on the not-focused path, which
is unchanged.

## 2. File structure

```
internal/relay/held.go             DefaultHeldGrace, heldScreenLines, inputEmpty, fingerprint,
                                   plannerHold, clearPlannerScreen
internal/relay/held_test.go        inputEmpty table; fingerprint stability
internal/relay/deliver.go          DeliverPending: returns the binding; focused branch calls plannerHold
internal/relay/deliver_test.go     the eight hold scenarios; existing focused tests carry a draft screen
internal/relay/reconcile.go        screenFingerprint hashes via fingerprint(); deliverAndSettle takes
                                   the returned binding
internal/relay/herdr.go            Runtime.HeldGrace
internal/store/store.go            Binding.PlannerScreen, Binding.PlannerScreenAt
cmd/relay/main.go                  cmdDaemon: --held-grace flag into rt.HeldGrace
README.md                          "The anti-clobber rule" rewritten; daemon flag listed
docs/design.md                     deliver() pseudocode and the "human camps" row updated
```

## 3. Data structures and type definitions

### 3.1 `store.Binding` (modified)

Two fields, placed directly after `BuilderScreenAt`, with a comment in the
same register as the `BuilderScreen` comment:

```
// PlannerScreen is a fingerprint of the planner's visible screen as relay
// last observed it while HOLDING a payload, and PlannerScreenAt is when the
// screen was last seen to change. They exist to tell a human who is typing
// from one who has walked away with the pane focused: the hold ends when the
// screen has been unchanged for HeldGrace.
//
// Both are transient: set only while a payload is held on a focused planner,
// and cleared by every DeliverPending return that is not such a hold.
PlannerScreen   string    `json:"planner_screen,omitempty"`
PlannerScreenAt time.Time `json:"planner_screen_at,omitempty"`
```

Invariant: `PlannerScreen != ""` if and only if the most recent
`DeliverPending` for this binding returned `Held` via the fingerprint path.
`omitempty` keeps every `bind.json` that never holds byte-identical.

### 3.2 `relay.Runtime` (modified)

```
// HeldGrace is how long a focused planner's screen must be unchanged before
// a held payload is injected anyway. Zero means DefaultHeldGrace. Set by
// `relay daemon --held-grace`; daemon-wide like --interval, not per binding.
HeldGrace time.Duration
```

### 3.3 `relay.Delivery` (unchanged shape)

No new fields. New `Reason` strings, all literal:

| outcome | `Reason` |
| --- | --- |
| delivered, detector | `planner focused, input empty` |
| delivered, grace | `planner focused, quiet for <HeldGrace>` (e.g. `quiet for 1m0s`) |
| held, read failed | `planner pane is focused; screen unreadable` |
| held, screen moved or first look | `planner pane is focused; screen changing` |
| held, unchanged but under grace | `planner pane is focused; quiet <n>s of <HeldGrace>` |

`Reason` has no production consumer; the strings are for tests and logs.

### 3.4 Constants (`held.go`)

```
// DefaultHeldGrace is the quiet period after which a held payload is injected
// into a focused planner whose input box relay cannot prove empty.
const DefaultHeldGrace = 60 * time.Second

// heldScreenLines bounds the visible-screen read. The input box is at the
// bottom, and a small window keeps scrolled-off transcript out of the
// fingerprint so old output cannot mask a change in the box.
const heldScreenLines = 40
```

## 4. Interface definitions and component contracts

### 4.1 `inputEmpty` (new, `held.go`)

```
// inputEmpty reports whether the planner's input box, as drawn on its visible
// screen, has nothing typed in it. known is false when relay has no marker
// for this harness kind, or the marker is not on screen; an unknown answer
// must fall through to the grace path, never be read as empty.
func inputEmpty(kind, screen string) (empty, known bool)
```

Rule per kind:

- `claude`: scan the screen's lines from the **last** line upward. The first
  line whose left-trimmed text begins with `❯` (or `>` on older builds) is
  the input box. `empty` is whether the text after the marker is whitespace
  only. Scanning from the bottom matters: transcript above the box can
  contain markdown quotes that begin with `>`. No marker line at all →
  `known = false`.
- any other kind, including `""` → `(false, false)`. Their idle screens have
  not been captured; guessing a marker is worse than none.

Pure. No I/O. Preconditions: none. Postcondition: `empty` is meaningful only
when `known`.

### 4.2 `fingerprint` (new, `held.go`) and `screenFingerprint` (modified)

```
// fingerprint hashes one terminal snapshot so two reads can be compared
// without keeping the text.
func fingerprint(text string) string
```

`sha256` hex, exactly what `screenFingerprint` computes today.
`screenFingerprint` keeps its signature and calls `fingerprint` on the read;
nothing else about it changes.

### 4.3 `plannerHold` (new, `held.go`)

```
// plannerHold decides whether a payload held on a FOCUSED, idle planner may
// be injected now. It reads the planner's visible screen and applies the
// two signals in order: an input box known to be empty injects at once;
// otherwise the screen is fingerprinted and injection waits until it has
// been unchanged for HeldGrace. The returned binding carries the fingerprint
// state and must be persisted whether or not inject is true.
//
// A failed read holds with the fingerprint untouched: a read failure is not
// evidence of anything, and the next tick retries.
func plannerHold(ctx context.Context, rt Runtime, b store.Binding, planner herdr.Agent) (next store.Binding, inject bool, reason string)
```

Dependencies: `rt.Herdr.ReadAgentSource`, `rt.Now`, `rt.HeldGrace`.
Never returns an error.

### 4.4 `clearPlannerScreen` (new, `held.go`)

```
// clearPlannerScreen ends a fingerprint hold. Every DeliverPending return
// that is not a fingerprint hold goes through here, so PlannerScreen is
// never stale.
func clearPlannerScreen(b store.Binding) store.Binding
```

### 4.5 `DeliverPending` (modified)

```
func DeliverPending(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, Delivery, error)
```

The binding is now returned, the way `builderQuiescent` returns it, because
the hold has state to persist. The returned binding differs from the input
only in `PlannerScreen`/`PlannerScreenAt`; `State` remains
`deliverAndSettle`'s to set. On an error return the input binding is
returned unchanged.

Contract per path (in the order the function checks them):

| condition | returned binding | `Delivery` |
| --- | --- | --- |
| planner not found | cleared | `PlannerGone` |
| planner not idle/done | cleared | `Reason: "planner is <status>"` |
| nothing pending | cleared | `Empty` |
| focused, `plannerHold` says hold | as returned by `plannerHold` | `Held`, reason from `plannerHold` |
| focused, `plannerHold` says inject | cleared, after prompt+confirm | `Delivered`, reason from `plannerHold` |
| not focused | cleared, after prompt+confirm | `Delivered` |

The held-notification rule is unchanged: notify once on the transition into
`Held` (`b.State != store.StateHeld`), never per tick.

### 4.6 `deliverAndSettle` (modified)

Takes the binding `DeliverPending` returns and folds `Delivery` into
`State` exactly as today. No other change.

### 4.7 `cmdDaemon` (modified)

```
heldGrace := fs.Duration("held-grace", relay.DefaultHeldGrace,
    "how long a focused planner must be quiet before a held payload is injected anyway")
```

Assigned to `rt.HeldGrace` after `newRuntime()`. Zero or negative from the
flag is passed through; `plannerHold` treats `<= 0` as `DefaultHeldGrace`.

## 5. High-level pseudocode

```
DeliverPending(b, agents):
    planner := FindAgent(agents, b.Planner)
    if not found:                       return clear(b), PlannerGone
    if planner not idle/done:           return clear(b), "planner is <status>"
    pending := tx.PendingForPlanner(b.Name)
    if none:                            return clear(b), Empty

    reason := ""
    if planner.Focused:
        b, inject, reason = plannerHold(b, planner)
        if not inject:
            if b.State != Held: notify("<name>: round N payload ready")
            return b, Held{reason}

    promptWithRetry(planner.PaneID, pending.Payload)
    tx.ConfirmIndex(b.Name, idx)
    mark planner working in agents      # unchanged, #46
    return clear(b), Delivered{reason}

plannerHold(b, planner):
    grace := rt.HeldGrace, or DefaultHeldGrace if <= 0
    screen, err := rt.Herdr.ReadAgentSource(planner.PaneID, "visible", heldScreenLines)
    if err:                             return b, false, "…; screen unreadable"
    empty, known := inputEmpty(planner.Kind, screen)
    if known and empty:                 return b, true, "planner focused, input empty"
    fp := fingerprint(screen); now := rt.Now().UTC()
    if b.PlannerScreen == "" or fp != b.PlannerScreen:
        b.PlannerScreen, b.PlannerScreenAt = fp, now
        return b, false, "…; screen changing"
    quiet := now - b.PlannerScreenAt
    if quiet >= grace:                  return b, true, "planner focused, quiet for <grace>"
    return b, false, "…; quiet <quiet>s of <grace>"
```

State transitions of the hold, per tick, focused and idle with a payload
pending:

```
(no fingerprint) --read ok, input empty-------------------> inject
(no fingerprint) --read ok, not empty/unknown-------------> fingerprint F@t0, hold
(F@t0)           --screen != F-----------------------------> fingerprint F'@t1, hold
(F@t0)           --screen == F, now-t0 <  grace------------> hold
(F@t0)           --screen == F, now-t0 >= grace------------> inject
(any)            --read fails------------------------------> hold, state untouched
(any)            --planner unfocused / busy / gone / empty-> cleared
```

## 6. Error handling strategy

- `ReadAgentSource` failure: **recoverable, silent**. Hold with the
  fingerprint untouched; no error surfaces; retried next tick. Reason names
  it for the log.
- `Notify` failure: unchanged from today -- returned as an error, the tick
  logs and retries.
- `promptWithRetry` / `ConfirmIndex` failure: unchanged -- returned, binding
  returned as-is (the fingerprint is cleared only on a completed delivery,
  so a failed prompt on the grace path re-evaluates next tick rather than
  restarting the clock).
- `inputEmpty` and `fingerprint` cannot fail.

No new error types. Nothing is logged from `internal/relay` beyond what the
daemon already logs per tick.

## 7. Behavioural rules and their rationale

1. **Detector before grace.** An empty box is proof; a quiet screen is
   inference. Proof short-circuits.
2. **Grace clock from the last change, not from the hold.** A human who
   types one character every 50 seconds never gets clobbered; the clock is
   reset by every change. A human who left mid-draft gets the payload
   appended after 60 quiet seconds -- accepted per the issue.
3. **Unknown kind takes the grace path only.** Strictly better than today
   (delivery eventually happens) and never wrong in the dangerous direction.
4. **A read failure is not evidence.** It neither injects nor resets the
   clock.
5. **Fingerprint over the last `heldScreenLines` lines of the `visible`
   source.** `recent-unwrapped` is scrollback and alternate-screen rows never
   reach it; `detection` is herdr's matching buffer, not a stable image.
   Known limitation, recorded in the code comment: a harness that redraws a
   clock inside that window resets the grace every tick. The detector is
   the primary path; if this bites, narrow the hashed region to the lines
   from the marker down rather than widen the design.
6. **`PlannerScreen` is cleared on every non-hold return.** The invariant in
   §3.1 is what makes the field safe to read anywhere later (a status
   column, for instance) without knowing the delivery history.
7. **Notify once, on the transition into Held.** Unchanged. The three-tick
   test pins it against the new branches.

## 8. Testing requirements

All in `internal/relay`, against `fakeHerdr` and a clock the test controls
(`rt.Now = func() time.Time { return now }` with `now` a variable the test
advances). `fakeHerdr.ReadAgentSource` returns `f.readOut` for any target,
so a test sets the planner's screen by assigning `f.readOut` before each
tick and asserts the read used source `visible` via `f.reads`.

`held_test.go`:

1. `TestInputEmpty` table: claude bare `❯` → `(true, true)`; claude
   `❯ draft` → `(false, true)`; claude with a `> quoted` transcript line
   above a bare `❯` → `(true, true)`; claude older `>` marker bare →
   `(true, true)`; claude with no marker → `(false, false)`; `agy` and
   `opencode` and `""` with a bare `❯` → `(_, false)`.
2. `TestFingerprintIsStable`: same text twice equal; different text differs.

`deliver_test.go` (the issue's list, plus the invariant):

3. focused, claude screen with bare `❯` → `Delivered`, `Reason ==
   "planner focused, input empty"`, one prompt, read source `visible` with
   `heldScreenLines` lines.
4. focused, claude `❯ draft` → `Held`, `PlannerScreen != ""`,
   `PlannerScreenAt == now`, no prompt, one notice.
5. same screen, clock advanced past `HeldGrace` → `Delivered`, reason names
   the grace, `PlannerScreen == ""` afterwards.
6. screen changed between ticks → `Held`, `PlannerScreenAt` reset to the
   later tick, `PlannerScreen` is the new fingerprint.
7. unknown kind (`agy`), same screen past grace → `Delivered` (grace path
   alone); before grace → `Held`.
8. `ReadAgentSource` errors → `Held`, `PlannerScreen` unchanged from the
   input binding, `err == nil`.
9. notify fires exactly once across three consecutive held ticks (the
   binding passed back with `State = Held` after the first).
10. a binding carrying `PlannerScreen` with nothing pending → `Empty` and
    the returned binding has both fields zero (the `relay pull` aftermath).
11. a binding carrying `PlannerScreen` whose planner is not focused →
    `Delivered` and both fields zero.
12. `HeldGrace` zero on the runtime → the default applies (advance the clock
    by 59s: held; by 60s: delivered).

Existing `TestDeliverHoldsWhilePlannerPaneFocused` and
`TestDeliverNotifiesOnlyOnceWhileHeld` must set `f.readOut` to a claude
screen with a draft, or they now deliver; `TestDeliverMarksPlannerBusyForTheRestOfTheTick`
and the unfocused tests need only the new return shape.

Mutation checks (CLAUDE.md): remove the `>= grace` comparison → test 5
fails; remove the `known && empty` short circuit → test 3 fails; stop
clearing on the `Empty` path → test 10 fails.

No test executes a `cmd/relay` subcommand. The flag is parsed in
`cmdDaemon` and not tested there; its effect is test 12 on `Runtime`.

## 9. Explicitly out of scope

- Per-binding grace, or a config file for it.
- A "quiet 23s/60s" column in `relay status` or the TUI. Cheap once the
  state exists; not now.
- `agy` / `opencode` markers for `inputEmpty`. Capture with
  `herdr agent read <pane> --source visible | cat -A` first.
- Narrowing the fingerprint region below the marker. Only if the known
  limitation in §7.5 is observed.
