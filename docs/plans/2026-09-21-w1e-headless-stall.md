# Wave 1 batch E: a headless builder that goes silent is labelled stalled -- status/ui label, hook event, never an action (#252; first slice of #135)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. The repo
there has **no tags**, so never run `make check`; run the gate commands in
§7 exactly as written. Run every command in the foreground and read its
exit code; never as a background task. Do not spawn sub-agents for the
edits.

## 1. System overview

On 2026-09-20 an opencode builder on the serve box wrote twelve stream
lines, dispatched a sub-agent whose request never returned, and sat for 36
minutes at 0.3% CPU while relay showed `running` throughout. relay already
owns the one signal that distinguishes "thinking" from "hung" for a
headless builder: the stream file `NNN-builder.jsonl`
(`store.BuilderStreamPath`) grows on every event the harness emits. After
this round the daemon stamps `Binding.StalledSince` when the process is
alive but the stream has been quiet for `policy.json` `stall_after_ms`
(default 15 minutes), clears it when the stream moves again or the process
exits, and fires one `builder_stalled` hook event per episode. `relay status`
and `relay ui` show `stalled <age>` in place of `working`/`running`; the
server ships the stamp in `BindingView` so a remote round shows it on the
client too. relay never acts on it: killing stays the human's decision
(`relay done`, `relay unbind`, or #138's `relay stop` later).

## 2. File structure

```
internal/policy/policy.go              + StallAfterMS *int `json:"stall_after_ms,omitempty"`, DefaultStallAfter = 15m, (p Policy) StallAfter() time.Duration, validation > 0
internal/policy/policy_test.go         + tests
internal/store/types.go                Binding + StalledSince time.Time `json:"stalled_since,omitempty"`
internal/hooks/events.go               + EventBuilderStalled EventType = "builder_stalled"
internal/relay/headless.go             reconcileHeadless alive branch stamps/clears; headlessStatus(ctx, rt, b store.Binding) label; + streamLastActivity(rt, b) time.Time
internal/relay/status.go               headless rows: label; remote rows: label from b.StalledSince
internal/relay/reconcile.go            emitMutations: builder_stalled on zero -> set; queueReport round close clears StalledSince
internal/relay/send.go                 a send clears StalledSince
internal/relay/served.go               ServedView: StalledSince
internal/relay/remote.go               observeRemote: b.StalledSince = view.StalledSince (running and needs_you cases)
internal/remote/proto.go               BindingView + StalledSince time.Time `json:"stalled_since,omitempty"`
internal/relay/headless_test.go        + TestReconcileHeadlessStampsStallWhenStreamQuiet, TestReconcileHeadlessStallClearsWhenStreamMoves, TestStatusHeadlessStalledLabel
internal/relay/reconcile_hooks_test.go + TestBuilderStalledHookFiresOncePerEpisode
internal/relay/served_test.go          + TestServedViewCarriesStalledSince
internal/relay/remote_test.go          + TestObserveRemoteCopiesStalledSince
internal/ui/rail.go                    (only if the rail styles "exited" specially: give "stalled" the same attention style; otherwise no change)
README.md                              policy key, hook event, one paragraph under headless builders
docs/plans/2026-09-21-w1e-headless-stall.md   copy of this plan
```

`headlessStatus`'s signature changes: `grep -rn "headlessStatus(" internal/ --include=*.go`
and update every caller (expected: `internal/relay/status.go` and tests).

## 3. Data structures

```
// internal/policy/policy.go
StallAfterMS *int   // nil = DefaultStallAfter (15 * time.Minute); present value must be > 0 (validate like LimitGateDefaultMS)
func (p Policy) StallAfter() time.Duration

// internal/store/types.go  Binding
StalledSince time.Time   // the stream's last activity time when the daemon judged the builder stalled; zero = not stalled.
                         // Set/cleared only by reconcileHeadless (and copied from the server view for remote bindings); cleared by Send and round close.

// internal/remote/proto.go  BindingView
StalledSince time.Time `json:"stalled_since,omitempty"`   // zero from a pre-stall server
```

## 4. Interfaces

```
// internal/relay/headless.go
func streamLastActivity(rt Runtime, b store.Binding) time.Time
    // st, err := os.Stat(rt.Store.BuilderStreamPath(b.Name, b.Round)); err -> b.RoundStartedAt
    // return the later of st.ModTime() and b.RoundStartedAt   (a round that has not produced a line yet counts from its start)

// reconcileHeadless, the alive branch (today: `if alive { ... return b, nil }` around headless.go:366-380 -- read it; keep every existing side effect there, e.g. drainStream, escape scan):
    last := streamLastActivity(rt, b)
    if now.Sub(last) >= rt.Policy.StallAfter() {
        if b.StalledSince.IsZero() {
            b.StalledSince = last
            slog.Warn("headless builder stalled", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "quiet", now.Sub(last).Truncate(time.Second))
        }
    } else if !b.StalledSince.IsZero() {
        b.StalledSince = time.Time{}
        slog.Info("headless builder resumed", "binding", b.Name, "round", b.Round)
    }
    // every exit path (with or without report) and clearProcess: b.StalledSince = time.Time{}

func headlessStatus(ctx, rt Runtime, b store.Binding) (string, *HeadlessInfo)
    // as today over b.Builder, plus: when the answer would be "working" and !b.StalledSince.IsZero():
    //   "stalled " + shortAge(rt.Now().Sub(b.StalledSince))   -- use the age formatter status.go already uses for "gating <age>" / quiet ages (find it; do not add a second one)
    // HeadlessInfo gains nothing.

// internal/relay/status.go  remote rows (~:311-316)
    if !b.StalledSince.IsZero() && row.BuilderStatus == "running" { row.BuilderStatus = "stalled " + shortAge(...) }

// internal/relay/reconcile.go  emitMutations
    if orig.StalledSince.IsZero() && !next.StalledSince.IsZero() {
        rt.Hooks.Dispatch(ctx, hooks.Event{Type: hooks.EventBuilderStalled, BindingID: next.Name, State: string(next.State), OldState: string(orig.State), Round: next.Round, Timestamp: rt.Now().UTC()})
    }
    // no event on clear; the hook payload has no new fields

// internal/relay/served.go  ServedView: view.StalledSince = b.StalledSince
// internal/relay/remote.go  observeRemote: in the RoundRunning case (and RoundNeedsYou), b.StalledSince = view.StalledSince before Save
```

## 5. Pseudocode

Covered by §4. Order inside the alive branch: existing work first (drain,
scans), then the stall stamp, then `return b, nil`.

## 6. Error handling

- A missing or unreadable stream file is not an error: activity = round
  start. A stream that never appears for 15 minutes on a live process is
  exactly a stall.
- `stall_after_ms <= 0` is a policy load error, worded like the existing
  `limit_gate_default_ms` one.
- Nothing here kills, switches, halts or changes `State`. A stalled binding
  is still `ACTIVE`; `relay wait` keeps waiting.

## 7. Ordered implementation steps

Commit prefix: one feature -> exactly **one** `feat(headless):` commit on
the branch (squash step commits, or `chore:`/`test:` per step and one
`feat:` for the code). Never more than one `feat:`.

### Task 1 -- policy key, store field, event type

**Files:** `internal/policy/policy.go`, `policy_test.go`, `internal/store/types.go`, `internal/hooks/events.go`.

**Tests first:** `TestStallAfterDefaultAndOverride` (nil -> 15m; 60000 ->
1m; 0 -> load error naming `stall_after_ms`). Store round-trip: if
`internal/store` has a Save/Load round-trip test, add `StalledSince` to
it. **Verify:** `go test -count=1 ./internal/policy/ ./internal/store/ ./internal/hooks/`.

### Task 2 -- the daemon stamps and clears; status shows it; the hook fires

**Files:** `internal/relay/headless.go`, `status.go`, `reconcile.go`, `send.go`,
`headless_test.go`, `reconcile_hooks_test.go`.

**Tests first**
- `TestReconcileHeadlessStampsStallWhenStreamQuiet`: build on
  `TestReconcileHeadlessAliveWaits` (~963): a sent headless binding whose
  fake runner says alive; write two lines to `BuilderStreamPath`, then
  `os.Chtimes` it to `now - 20m` (the fake clock's `now`; also set
  `RoundStartedAt` to `now - 30m`); tick -> `StalledSince` equals that
  mtime (second precision), `State` still active, no kill, no switch,
  `fr.specs` unchanged. A second tick keeps the same `StalledSince`.
  **Mutation check:** compare `<` against `StallAfter` instead of `>=`... no:
  set the mtime to `now - 5m` in a second case and assert `StalledSince` is
  zero -- then invert the comparison and both cases must fail.
- `TestReconcileHeadlessStallClearsWhenStreamMoves`: after the stamp,
  `os.Chtimes` to `now` -> next tick clears it. And: process exits with a
  report -> `StalledSince` zero after close.
- `TestStatusHeadlessStalledLabel`: with `StalledSince` set and the runner
  alive, `Status` row `BuilderStatus` starts with `stalled ` and contains an
  age; with it zero -> `working`.
- `TestBuilderStalledHookFiresOncePerEpisode` (`reconcile_hooks_test.go`,
  in the shape of the existing state_changed hook test): the fake hooks
  sink receives exactly one `builder_stalled` across three stalled ticks,
  and none when the stall clears.

**Then** the code per §4. **Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 3 -- the wire and the client

**Files:** `internal/remote/proto.go`, `internal/relay/served.go`, `served_test.go`, `remote.go`, `remote_test.go`, `status.go`.

**Tests first**
- `TestServedViewCarriesStalledSince`: set -> equal in the view; zero -> zero.
- `TestObserveRemoteCopiesStalledSince`: build on
  `TestReconcileRemoteRunningMirrorsLog` (~1190): the fake view carries
  `StalledSince`; after the tick the client binding has it and
  `Status` shows `stalled <age>` for the remote row; a later view with it
  zero clears it.

**Then** the code. **Verify:** `go test -count=1 ./internal/relay/ ./internal/remote/...`.

### Task 4 -- ui, README, plan copy, gate

`internal/ui/rail.go`: `grep -n '"exited' internal/ui/rail.go internal/ui/styles.go`;
if an attention style is applied to `exited` rows, apply it when
`BuilderStatus` starts with `stalled`; otherwise leave the ui alone (the
label flows through `BuilderStatus` already, `rail.go` `whatAge` ~:79).

README: `policy.json` keys table gets `stall_after_ms`; the hooks section
gets `builder_stalled`; the headless section gets one paragraph: what
stalled means, that relay never acts on it, how to end it.

Copy the plan file you were handed to `docs/plans/2026-09-21-w1e-headless-stall.md`
and commit it.

Gate, in the foreground, in this order; stop at the first failure and report it:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
Do not run `make check` or `make e2e` (planner runs them; `make e2e` covers `reconcile.go`).

Final commit message (the one `feat:`):
`feat(headless): a builder whose stream goes quiet is labelled stalled in status/ui and raises builder_stalled; relay never acts on it (#252)`

## Report

Per task: what was done, the test names, the verify result, the mutation
check's outcome (name the failing test). The `headlessStatus(` grep output.
Then the commit shas (exactly one `feat:` on `main..HEAD`) and the gate
output's last lines. If any step was impossible as written, say which and
stop there.
