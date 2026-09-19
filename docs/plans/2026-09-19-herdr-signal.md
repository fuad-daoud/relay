# relay's state in herdr's own sidebar and toasts; blue means "all rounds finished" (#129, #182 splits 1–2)

Closes #129. Closes #182 splits 1 and 2 (split 3 is a herdr follow-up, not a
dependency). Design in this file; no separate spec.

Decisions taken by the planner (the issues left them open):

- **Tokens are written by the daemon only**, from the stored binding, once per
  tick when the computed set changed (and unconditionally every 60 s so a
  herdr server restart -- which drops token metadata -- heals within a
  minute). No CLI verb writes tokens; a `relay send` shows up on the next
  tick. This replaces "on every transition, daemon and CLI" from #129 with
  one code path.
- **No `--state-label` this round** and **no rolled-up token on the planner
  pane for headless builders**: headless and remote builders have no pane and
  get no tokens. The sidebar rules on `relay_state` already colour NEEDS YOU.
- **DONE clears the tokens** (as does the binding vanishing from the store):
  a done builder's pane is the human's again.
- **A delivered report gets no toast** (#182 supersedes that row of #129's
  table): the one `done`-sounding moment is "all rounds finished".
- **Sound classes:** halt / blocked dialog → `request`; held report → `done`;
  mid-round switch → `none`; all rounds finished → `done`.
- Broken/orphaned bindings still do not toast (they never did; #129 listed
  them, but they have no dedup key today -- a follow-up, not this round).

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`
(the planner runs it). Do not add a CLI verb or flag. Do not add a module
dependency. Do not touch `internal/ui` beyond the one fake method §2 lists.
Run every command in the foreground; dispatch no sub-agents. Every exported
signature you change is named in §4; change no other.

**Commits.** ONE `feat(herdr):` commit for the whole feature (squash your
step commits before the gate). Subject:
`feat(herdr): sidebar tokens, notification classes, and one "all rounds finished" toast (#129, #182)`.

**Commit the plan with the work.** Copy this plan file to
`docs/plans/2026-09-19-herdr-signal.md` in your worktree and include it.

**Before step 1**: `git status --short` and `git branch --show-current`; you
must be on `relay/<binding-name>` under `~/.local/state/relay/.worktrees/`
with a clean tree. Otherwise halt.

## 1. System overview

Today `Herdr.Notify(ctx, message)` runs `herdr notification show <message>`
with no body and no sound, from three call sites (`deliver.go:95` held
report, `reconcile.go:278` `haltBinding`, `switch.go:204` mid-round switch),
and relay never calls `herdr pane report-metadata`. So in herdr's sidebar a
builder blocked at a dialog looks like one that is working, and every relay
toast sounds the same. Separately (#182), a pane planner's tab turns blue
after *every* round because the daemon types each report in with
`agent prompt`, which ends a planner turn per round; blue should mean "the
planner is finished and nothing is running on its behalf".

This plan does four things:

1. **`herdr.Client` learns two verbs** -- `Notify(ctx, title, body, sound)`
   and `ReportMetadata(ctx, paneID, meta)` -- and the `relay.Herdr`
   interface, its stub, and its fakes follow.
2. **Every existing toast picks a class** (title, body, sound), and a builder
   blocked at a dialog gets its own `request` toast, once per round, where
   the question entry is written.
3. **The daemon tags each pane builder's pane** with `relay=<name>`,
   `relay_round=NNN`, `relay_state=active|held|needs-you|done` after every
   tick, through one pure function (`PaneTokens`) and one per-daemon
   dedup map; tokens are cleared when the binding is DONE or gone.
4. **One "all rounds finished" toast per planner** (#182 split 2): a pure
   predicate over a planner's bindings fires `done` once when the planner is
   idle and no binding of it has a round in flight, a held or pending
   payload, or a switch about to happen -- keyed on a new per-binding
   `FinishPending` flag that `Send` sets and the toast clears, never on
   `State` (the `HaltNotifiedRound` lesson). Plus the planner-side
   instruction (#182 split 1): after `relay send`, wait inside the turn.

## 2. File structure

```
internal/herdr/
  client.go            EDIT  Sound type + consts; PaneMetadata; Notify(title, body, sound); ReportMetadata; MetadataSource
  client_test.go       EDIT  TestClientNotifyArgv, TestClientReportMetadataArgv (argv-capture script pattern at :161)
internal/relay/
  herdr.go             EDIT  Herdr interface: Notify signature, ReportMetadata
  fake_test.go         EDIT  fakeHerdr: Notify records title (notices), bodies, sounds; ReportMetadata records metadata calls
  deliver.go           EDIT  held toast: title/body/SoundDone
  reconcile.go         EDIT  haltBinding: SoundRequest + body; handleBlockedBuilder: its own request toast
  switch.go            EDIT  switch toast: SoundNone + body
  send.go              EDIT  b.FinishPending = true where RoundStartedAt is stamped (send.go:278)
  tokens.go            NEW   PaneTokens, tokenState, TokenFingerprint, syncPaneMetadata (daemon side)
  tokens_test.go       NEW   pure tests + daemon-driven tests
  finished.go          NEW   roundInFlight, FinishedGroup, notifyFinished (daemon side)
  finished_test.go     NEW   pure tests + daemon-driven tests
  daemon.go            EDIT  Daemon gains applied map + metaAt; Tick calls syncPaneMetadata and notifyFinished after the binding loop
  daemon_test.go       EDIT  TestTickAppliesTokensAndNotifiesFinished (or split as §8 says)
  reconcile_test.go    EDIT  blocked-dialog toast test (next to the existing handleBlockedBuilder tests)
  deliver_test.go      EDIT  only if an assertion on notices needs the new fields (keep changes minimal)
internal/store/
  types.go             EDIT  Binding.FinishPending
internal/serve/
  herdr.go             EDIT  stubHerdr: new Notify signature (logs title+body+sound), ReportMetadata no-op nil
internal/e2e/
  fakes_test.go        EDIT  fakeHerdr: new Notify signature, ReportMetadata recorder (compile only)
internal/ui/
  fake_test.go         EDIT  fakeHerdr: new Notify signature, ReportMetadata read-only violation (compile only)
internal/harness/agents/
  architect.claude.md  EDIT  "Handing off": the wait-inside-the-turn bullet (§7)
  architect.opencode.md EDIT same bullet
  architect.agy.md     EDIT  same bullet
README.md              EDIT  §7: sidebar snippet under "### herdr lifecycle integrations"; planner-loop paragraph under "## The planner: architect"; one line under "## Display states"
docs/plans/2026-09-19-herdr-signal.md  NEW  copy of this plan
```

Grep before you start: `grep -rn "\.Notify(" --include=*.go .` must list
exactly the sites §2 names (three production, plus the fakes/stub). If
another appears, halt.

## 3. Data structures & type definitions

### `internal/herdr/client.go`

```
// Sound is herdr's notification sound class (`notification show --sound`).
type Sound string

const (
    SoundNone    Sound = "none"
    SoundDone    Sound = "done"
    SoundRequest Sound = "request"
)

// MetadataSource is the one `--source` relay ever reports under: herdr caps
// a pane at 32 distinct sources, so relay is one source, never one per
// binding (#129).
const MetadataSource = "relay"

// maxTokenValue is herdr's cap on a token value (80 chars, verified 0.9.1).
const maxTokenValue = 80

// PaneMetadata is one `herdr pane report-metadata` call: display-only.
type PaneMetadata struct {
    Tokens      map[string]string // --token NAME=VALUE, emitted sorted by NAME; values truncated to maxTokenValue
    ClearTokens []string          // --clear-token NAME, emitted in slice order
    Seq         int64             // --seq; a later write with a lower seq is ignored by herdr
}
```

### `internal/store/types.go` -- `Binding`

Add next to `HaltNotifiedRound`:

```
// FinishPending is true from the moment Send opens a round until the
// daemon has raised the one "all rounds finished" toast for this
// binding's planner (#182). It is deliberately NOT derived from State,
// for the same reason HaltNotifiedRound is not: a later step in the same
// tick may rewrite State. A binding written before this field existed
// has FinishPending == false and never toasts until its next Send.
FinishPending bool `json:"finish_pending,omitempty"`
```

### `internal/relay/daemon.go` -- `Daemon`

```
type Daemon struct {
    rt       Runtime
    interval time.Duration
    refresh  func(Runtime) Runtime
    // applied is what the daemon last reported to herdr per binding
    // (#129): the pane it wrote to, the token fingerprint, and when. In
    // memory only: a daemon restart re-applies everything on its first
    // tick, and metadataRefresh bounds how stale a herdr restart can leave
    // a pane.
    applied map[string]appliedMeta
}

type appliedMeta struct {
    pane string
    fp   string
    at   time.Time
}

// metadataRefresh is how often an unchanged token set is re-reported anyway.
const metadataRefresh = 60 * time.Second
```

`NewDaemon` initialises `applied` to an empty map.

### `internal/relay/tokens.go`

```
// Token names relay reports (#129). README's sidebar snippet uses them.
const (
    TokenName  = "relay"
    TokenRound = "relay_round"
    TokenState = "relay_state"
)
```

### `internal/relay/finished.go`

```
// FinishedResult is what one planner's bindings say about the "all rounds
// finished" toast.
type FinishedResult struct {
    Fire  bool     // the toast should be raised now
    Names []string // the bindings whose FinishPending it clears, sorted
}
```

## 4. Interface definitions & component contracts

### `internal/herdr/client.go`

```
// Notify shows a toast: `herdr notification show <title> [--body <body>] --sound <sound>`.
// --body is omitted when body == ""; an empty sound is sent as SoundNone.
func (c *Client) Notify(ctx context.Context, title, body string, sound Sound) error

// ReportMetadata runs `herdr pane report-metadata <paneID> --source relay
// [--token k=v ...] [--clear-token k ...] --seq <n>`. Tokens are emitted in
// sorted key order so the argv is deterministic; a value longer than
// maxTokenValue is truncated (rune-safe). A call with no tokens and no
// clears is a no-op that returns nil without running herdr.
func (c *Client) ReportMetadata(ctx context.Context, paneID string, m PaneMetadata) error
```

Both go through the existing `c.run`. Errors propagate as `run` returns
them (envelope-decoded).

### `internal/relay/herdr.go` -- `Herdr`

Replace `Notify(ctx context.Context, message string) error` with

```
Notify(ctx context.Context, title, body string, sound herdr.Sound) error
ReportMetadata(ctx context.Context, paneID string, m herdr.PaneMetadata) error
```

Implementations to update: `*herdr.Client` (above), `serve.stubHerdr`
(Notify: `slog.Info("notify", "title", title, "body", body, "sound", sound)`;
ReportMetadata: return nil), `internal/relay` `fakeHerdr`, `internal/e2e`
`fakeHerdr`, `internal/ui` `fakeHerdr` (both new methods are read-only
violations there, same shape as its `Notify`).

`internal/relay/fake_test.go` `fakeHerdr`:

```
notices  []string           // titles, so every existing `f.notices[0]` assertion still reads the message text
bodies   []string           // parallel to notices
sounds   []herdr.Sound      // parallel to notices
metadata []metadataCall     // every ReportMetadata call, in order

type metadataCall struct {
    Pane string
    Meta herdr.PaneMetadata
}
```

`Notify` appends to all three slices; `ReportMetadata` appends one
`metadataCall` and returns nil (add `metaErr error` returned when set, for
the daemon-tolerates-failure test).

### `internal/relay/tokens.go`

```
// tokenState maps a stored state onto the relay_state token: the display
// state lower-cased with spaces as hyphens, so the README rules read like
// `relay status` does. Total: every store.State maps.
func tokenState(s store.State) string
//   held -> "held"; needs_you, broken, orphaned -> "needs-you"; done -> "done"; anything else -> "active"

// PaneTokens is the token set for a binding, or (nil, false) when the
// binding has no pane to tag: a headless or remote builder, a served
// binding (b.Serve != nil), or an empty Builder.PaneID.
func PaneTokens(b store.Binding) (tokens map[string]string, ok bool)
//   tokens = {relay: b.Name, relay_round: fmt.Sprintf("%03d", b.Round), relay_state: tokenState(b.State)}

// TokenFingerprint is a stable string of a token set, for the dedup map.
func TokenFingerprint(tokens map[string]string) string
//   keys sorted, "k=v" joined by "\x1f"

// clearTokens is the ReportMetadata that removes relay's three tokens.
func clearTokens(seq int64) herdr.PaneMetadata

// syncPaneMetadata reports tokens for every binding whose set changed (or
// whose last report is older than metadataRefresh), clears tokens for a
// DONE binding and for every name in applied that bindings no longer
// contains, and updates applied. Errors are slog.Warn per binding and never
// fail the tick. Seq is rt.Now().UnixMilli().
func syncPaneMetadata(ctx context.Context, rt Runtime, applied map[string]appliedMeta, bindings []store.Binding)
```

### `internal/relay/finished.go`

```
// roundInFlight reports whether something is still running on the
// planner's behalf for this binding (#182): an open round on an active
// builder, a held or undelivered payload, or a broken builder the daemon
// is about to switch. A NEEDS YOU, orphaned, non-switchable broken or DONE
// binding is not in flight: the human already has its notification.
func roundInFlight(b store.Binding, pending bool) bool
//   b.State == StateHeld                                        -> true
//   pending                                                     -> true
//   b.State == StateActive && !b.RoundStartedAt.IsZero()        -> true
//   b.State == StateBroken && switchable(b)                     -> true   (switchable from waiting.go)
//   otherwise                                                   -> false

// FinishedGroup decides the toast for one planner's bindings. Pure.
// inFlight is roundInFlight with the pending lookup bound by the caller.
func FinishedGroup(bs []store.Binding, plannerIdle bool, inFlight func(store.Binding) bool) FinishedResult
//   Fire iff plannerIdle && no b has inFlight(b) && at least one b has FinishPending
//   Names = sorted names of every b with FinishPending (only meaningful when Fire)

// notifyFinished groups bindings by Planner.PaneID, evaluates FinishedGroup
// per group (plannerIdle: FindAgent(agents, b.Planner) found with Status
// idle or done; a planner not found is never idle), and for each firing
// group raises ONE toast then clears FinishPending on its Names under one
// store lock, re-loading each binding first (a concurrent unbind is
// ErrNotFound and skipped). Toast: title "<names joined by ', '>: all rounds
// finished", body one line per name "<name> round <Round-1> <display state
// lower-cased>", herdr.SoundDone. A Notify error is slog.Warn and the flags
// are NOT cleared (retry next tick).
func notifyFinished(ctx context.Context, rt Runtime, bindings []store.Binding, agents []herdr.Agent)
```

The `pending` lookup is `rt.Store.PendingForPlanner(name)` (the locking
form; `notifyFinished` runs outside the per-binding lock so it must not
hold a Tx while calling it -- do the evaluation with `Store` methods, then
take `WithLock` only for the clear).

### Call-site contracts

| site | title | body | sound |
|---|---|---|---|
| `haltBinding` (reconcile.go) | `message` unchanged | `fmt.Sprintf("round %d", b.Round)` | `SoundRequest` |
| `handleBlockedBuilder` (reconcile.go), right after `Queue` succeeds | `fmt.Sprintf("%s: builder blocked at a dialog", b.Name)` | `fmt.Sprintf("round %d: %s", b.Round, capLine(dialog, 100))` (capLine from waiting.go; "dialog captured" when it is empty) | `SoundRequest` |
| `DeliverPending` held (deliver.go) | `msg` unchanged | `"held: you are in the planner pane; " + pending.Note` (note "" -> `"held: you are in the planner pane"`) | `SoundDone` |
| `switchBuilder` (switch.go) | unchanged | `reason` | `SoundNone` |

A `handleBlockedBuilder` Notify error is `slog.Warn`, not a returned error:
the question is already queued and the state change must land.

### `Daemon.Tick`

After the existing per-binding loop (and only when it ran, i.e.
`len(bindings) > 0` already returned early above):

```
fresh, err := d.rt.Store.List()   // post-reconcile view; on error slog.Warn and return nil
syncPaneMetadata(ctx, d.rt, d.applied, fresh)
notifyFinished(ctx, d.rt, fresh, agents)
```

`agents` here is the tick's own copy, including the `StatusWorking` marks
`DeliverPending` made this tick -- that is what keeps a planner that was
just prompted from counting as idle on the same tick.

## 5. High-level pseudocode

```
syncPaneMetadata(ctx, rt, applied, bindings):
    now := rt.Now(); seq := now.UnixMilli()
    seen := set{}
    for b in bindings:
        seen.add(b.Name)
        tokens, ok := PaneTokens(b)
        if !ok: continue                       // nothing to tag, nothing remembered
        if b.State == StateDone:
            if prev, had := applied[b.Name]; had:
                ReportMetadata(prev.pane, clearTokens(seq)) (warn on error)
                delete(applied, b.Name)
            continue
        fp := TokenFingerprint(tokens)
        prev, had := applied[b.Name]
        if had && prev.pane == b.Builder.PaneID && prev.fp == fp && now.Sub(prev.at) < metadataRefresh:
            continue
        if had && prev.pane != b.Builder.PaneID:     // builder switched panes
            ReportMetadata(prev.pane, clearTokens(seq)) (warn on error, continue anyway)
        err := ReportMetadata(b.Builder.PaneID, PaneMetadata{Tokens: tokens, Seq: seq})
        if err: slog.Warn("report metadata", ...); continue     // not remembered: retried next tick
        applied[b.Name] = {pane: b.Builder.PaneID, fp: fp, at: now}
    for name, prev in applied:
        if !seen.has(name):
            ReportMetadata(prev.pane, clearTokens(seq)) (warn on error)
            delete(applied, name)

notifyFinished(ctx, rt, bindings, agents):
    groups := map[plannerPane][]Binding   (skip bindings with Planner.PaneID == "")
    for pane, bs in groups:
        planner, found := FindAgent(agents, bs[0].Planner)
        idle := found && (planner.Status == StatusIdle || planner.Status == StatusDone)
        res := FinishedGroup(bs, idle, func(b) bool {
            _, pending, _ := rt.Store.PendingForPlanner(b.Name)
            return roundInFlight(b, pending)
        })
        if !res.Fire: continue
        err := rt.Herdr.Notify(ctx, title(res.Names), body(bs, res.Names), SoundDone)
        if err: slog.Warn(...); continue
        rt.Store.WithLock(tx):
            for name in res.Names:
                b, err := tx.Load(name); if ErrNotFound: continue; if err: return err
                b.FinishPending = false
                tx.Save(b)
        slog.Info("all rounds finished", "planner", pane, "bindings", res.Names)
```

Lifecycle of `FinishPending`, end to end, pane builder:

```
send round 1     -> FinishPending = true, RoundStartedAt set      (in flight)
builder reports  -> queueReport: RoundStartedAt zero; payload pending (in flight: pending)
planner idle     -> DeliverPending prompts; agents[planner] = working  (not idle this tick)
planner turn     -> sends round 2 (in flight) ... or stops
planner stops    -> next tick: idle, not in flight, FinishPending -> toast, flag cleared
relay done       -> State done: tokens cleared; a later tick has FinishPending false -> silence
```

## 6. Error handling strategy

- Every herdr call added by this plan is display-only: **no new error ever
  fails a tick or a verb**. `syncPaneMetadata` and `notifyFinished` warn
  and move on; a failed token report is not remembered, so it retries; a
  failed finished toast keeps `FinishPending`, so it retries.
- The three existing call sites keep their existing error behaviour
  (`haltBinding` and the held delivery return the error; `switchBuilder`
  warns) -- only the arguments change.
- `handleBlockedBuilder`'s new toast warns on error (see §4).
- `ReportMetadata` with an empty `PaneMetadata` returns nil without a
  herdr call, so a clear on a binding that never had tokens is free.

## 7. Documentation

**README.md**

1. Under `### herdr lifecycle integrations` (or immediately after it, as a
   new `### Sidebar tokens` subsection), add the snippet from #129:

   > relay tags each pane builder's pane with three display tokens --
   > `relay` (binding), `relay_round` (`007`), `relay_state`
   > (`active|held|needs-you|done`) -- refreshed by the daemon within a tick
   > of any change and cleared when the binding is done or unbound.
   > Headless and remote builders have no pane and no tokens. Show them in
   > herdr's sidebar with:
   >
   > ```toml
   > [ui.sidebar.agents]
   > rows = [
   >   ["state_icon", "workspace", "tab"],
   >   [{ token = "$relay", bold = true }, { token = "$relay_round", dim = true },
   >    { token = "$relay_state", rules = [
   >        { equals = "needs-you", fg = "#f55", bold = true },
   >        { equals = "held",      fg = "#fc0" },
   >        { equals = "done",      dim = true } ] }],
   > ]
   > ```
   >
   > Toasts carry a sound class: `request` when a builder is blocked at a
   > dialog or a binding halts, `done` when a held report is ready and once
   > when all of a planner's rounds have finished, `none` for a mid-round
   > switch.

2. Under `## The planner: architect`, after the `claude --agent architect`
   example block, add:

   > **Wait inside the turn.** After `relay send`, the planner runs
   > `relay wait <name> --timeout 9m` (looping while it exits 124) and then
   > `relay pull <name>`, and ends its turn only when no binding has a round
   > in flight. herdr then badges the planner's tab once, when the planner
   > is actually finished, instead of after every round the daemon typed a
   > report into it (#182). The daemon's typed delivery remains the fallback
   > for a planner that is idle when a report lands.

3. Under `## Display states`, one line: `The daemon raises one
   "all rounds finished" toast (sound done) per planner when the planner is
   idle and none of its bindings has a round open, a payload pending or a
   switch due.`

**`internal/harness/agents/architect.{claude,opencode,agy}.md`**, "Handing
off" list, add one bullet after "Headless by default":

> - **Wait inside the turn.** After `relay send <name>`, run
>   `relay wait <name> --timeout 9m` in a loop while it exits 124, then
>   `relay pull <name>`. End your turn only when no binding you drive has a
>   round in flight: exit 3 (`NEEDS YOU`) means ask the human; exit 4 means
>   the binding is done. A turn that ends with a round open badges the
>   planner tab for nothing.

Keep the three copies textually identical for this bullet.

## 8. Tests

`internal/herdr/client_test.go` (argv-capture script, pattern at `:161`):
- `TestClientNotifyArgv`: `Notify(ctx, "t", "b", SoundRequest)` -> argv
  `notification show t --body b --sound request`; `Notify(ctx, "t", "", "")`
  -> `notification show t --sound none`.
- `TestClientReportMetadataArgv`: tokens `{relay_state: x, relay: y}`,
  clear `[a]`, seq 7 -> `pane report-metadata P --source relay --token
  relay=y --token relay_state=x --clear-token a --seq 7` (sorted tokens);
  a 100-char value arrives truncated to 80; empty metadata -> no script
  run (args file absent).

`internal/relay/tokens_test.go`:
- `TestTokenStateIsTotal`: every `store.State` constant plus `""` maps to
  one of the four values.
- `TestPaneTokensSkipsPanelessBuilders`: headless, remote, `Serve != nil`,
  empty PaneID -> ok false; a pane builder -> the three tokens with
  zero-padded round.
- `TestSyncReportsOnceThenOnChange`: two bindings, fake herdr; first call
  -> two metadata calls; second call same clock -> none; change one
  binding's State -> exactly one call, for that binding, with the new
  `relay_state`; advance clock past `metadataRefresh` -> both re-reported.
  **Mutation check (run and report):** drop the fingerprint comparison so
  every call reports; this test fails on the "none" step.
- `TestSyncClearsOnDoneAndOnVanish`: applied has A and B; bindings arrive
  with A DONE and B absent -> two clear calls (`ClearTokens` = the three
  names, no `Tokens`) on the remembered panes; applied is empty.
- `TestSyncFailureIsRetried`: `metaErr` set -> nothing remembered; error
  cleared -> reported on the next call.
- `TestSyncClearsOldPaneOnSwitch`: applied[A].pane = p1, binding A now on
  p2 -> clear on p1 then tokens on p2.

`internal/relay/finished_test.go`:
- `TestRoundInFlight`: table over the §4 rows, including `Broken` with and
  without `switchable`, `NeedsYou` with an open round (false), `Done`
  (false), pending true on a `NeedsYou` binding (true).
- `TestFinishedGroup`: planner idle + nothing in flight + one pending flag
  -> Fire with that name; planner working -> no fire; one binding in flight
  -> no fire; no FinishPending anywhere -> no fire even when idle.
  **Mutation check (run and report):** remove the "no binding in flight"
  clause; the in-flight case fails.
- `TestNotifyFinishedFiresOncePerTransition`: two bindings on one planner,
  fake herdr with the planner `idle`; both `FinishPending`, no rounds open
  -> exactly one Notify with sound `done`, title containing both names, and
  both flags false in the store; a second call -> no Notify. Then `Send`
  one of them (or set `FinishPending` + `RoundStartedAt` by hand and save)
  -> no Notify while the planner is `working`; planner idle and round
  closed (`RoundStartedAt` zero) -> one more Notify naming only that one.
- `TestNotifyFinishedWaitsForPendingPayload`: FinishPending, round closed,
  but a `Queue`d unconfirmed report entry -> no Notify; `ConfirmIndex` it ->
  Notify.

`internal/relay/reconcile_test.go`:
- `TestBlockedDialogToastsOnce`: builder `blocked`, fake `ReadAgentSource`
  returns a dialog; first tick -> one notice with sound `request`, title
  containing "blocked at a dialog", body containing the dialog's first
  line; second tick -> still one.
- Existing halt tests: add to ONE of them (e.g. the round-cap halt test)
  the assertion `f.sounds[0] == herdr.SoundRequest`; to
  `deliver_test.go`'s held test `f.sounds[0] == herdr.SoundDone`; to
  `switch_test.go:154`'s test `f.sounds[0] == herdr.SoundNone`.

`internal/relay/send_test.go`: in an existing successful-send test, assert
`FinishPending` is true afterwards (pane) and, in a headless send test, the
same.

`internal/relay/daemon_test.go`:
- `TestTickSyncsMetadataAndFinishedAfterReconcile`: one pane binding, one
  tick -> `f.metadata` has one call for its pane with the three tokens;
  `d.applied` has the name. (The finished path is covered in
  `finished_test.go`; here only that `Tick` calls both -- assert the
  metadata side and, with `FinishPending` set and the planner idle, one
  `done` notice.)

CI note: none of these reach herdr; `cmd/relay` gets no test.

## 9. Ordered implementation steps

1. `internal/herdr/client.go` types and the two methods; `client_test.go`
   two tests. `go test ./internal/herdr`.
2. `internal/relay/herdr.go` interface; update `serve.stubHerdr`, the
   three fakes (`internal/relay`, `internal/e2e`, `internal/ui`); adapt the
   three existing call sites per the §4 table and add the
   `handleBlockedBuilder` toast. `go build ./... && go vet ./...` clean;
   `go test ./internal/relay ./internal/serve ./internal/ui ./internal/e2e`
   green (the fake records titles, so no existing assertion changes).
   Add the three `f.sounds[0]` assertions and `TestBlockedDialogToastsOnce`.
3. `internal/store/types.go` `FinishPending`; `send.go` sets it; the
   `send_test.go` assertions.
4. `tokens.go` + `tokens_test.go` (pure + sync); run the mutation check.
5. `finished.go` + `finished_test.go`; run the mutation check.
6. `daemon.go` wiring + `daemon_test.go`.
7. Docs per §7 (README three places, three architect copies).
8. Copy the plan to `docs/plans/2026-09-19-herdr-signal.md`. Squash to the
   one `feat(herdr):` commit. Gate, exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Report the tail verbatim, both mutation outcomes, and
   `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block (`status`, `halted_at`,
`changed_paths`, `commands_run`, `not_done`). State that the daemon-path
changes are not live until the planner runs `make service`, and that
`relay agent install --force` is owed after merge for the architect edit.
