# Served builders, part 1 of 3: a per-server cap and a queued round state (#285)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it. A halt that surfaces a design error
is worth more than a green suite that bent a test to fit.

You are a headless builder on a server-side worktree of this repo. The
worktree has **no `origin`**: never fetch, pull or rebase. Never run `make
check` here (the repo has no tags on this box and the planner runs it); run
the gate commands in §7 exactly as written. Every command in the foreground;
no sub-agents for edits. Do not touch any file outside this worktree.

Vocabulary: a round that was accepted by the server but has no builder
process yet is **queued**. Not "parked", not "pending", not "waiting" --
`queued` is the word in every identifier, log note, JSON value and message.

## 1. System overview

`relay serve` starts a headless builder for every round the moment it is
accepted: `handleStartRound` (`internal/serve/rounds.go`) calls
`relay.Send`, which calls `startRound` inside the owner's store lock. On a
4-core box five builders ran at once and the box thrashed (#285). This part
adds a cap and a queue:

- `policy.json` gains a `serve` section with `max_builders` (default
  `max(1, runtime.NumCPU()-1)`) and a `scope` sub-section (defined and
  validated here, **used only by part 3**).
- `relay.Send` gains `SendOptions.Defer`: stage the round, append the plan
  log entry, stamp everything except `RoundStartedAt`, set
  `Binding.QueuedAt`, and return **without spawning**.
- `relay.Admit` (new) starts a queued round: spawn, stamp
  `RoundStartedAt`, zero `QueuedAt`, log a `queue` entry.
- `serve.(*Server).admit` (new) is the **only** place a served builder
  starts. It counts running builders across all owners, and admits queued
  rounds oldest-first while the count is below the cap. `handleStartRound`
  calls it right after `Send(Defer)`, and `Tick` calls it after every
  owner has been reconciled. Both already hold `s.mu`, so the count is a
  fact, not a race.
- `remote.RoundState` gains `queued`; `GET /v1/bindings/{name}` fills a
  `queue` object (position, ahead, running, cap, since); `whoami` reports
  the census. The client's rendering of these is **part 2** -- here the
  client must merely not break when it sees `queued`.
- A queued round has no process and no clocks: timeout, stall, explore and
  progress all key on `RoundStartedAt` or a live pid, and both stay zero.
- On a server, the #244 "builder lost to a daemon restart" branch
  **re-queues** the round at the head of the queue instead of relaunching
  it (a box reboot must not relaunch every builder past the cap). The
  local daemon keeps today's relaunch.
- Hook events `round_queued` and `round_admitted`; the server gets a hooks
  dispatcher (today `serve.runtimeAt` sets none, so nothing fires there).

Everything is server-side or additive. `relay daemon` (local) never sets
`Defer`, never has a `QueuedAt`, and is untouched in behaviour.

## 2. Files

```
internal/policy/policy.go             Policy.Serve; ServePolicy; ScopePolicy; validation in Load; MaxBuildersOrDefault
internal/policy/policy_test.go        Load accepts/rejects serve.* per §3.1; MaxBuildersOrDefault
internal/store/types.go               Binding.QueuedAt
internal/store/log.go                 KindQueue
internal/remote/proto.go              RoundQueued; FeatureQueue; QueueView; BindingView.Queue; BuildersView; WhoAmI.Builders
internal/relay/send.go                SendOptions.Defer; the deferred branch
internal/relay/queue.go        (new)  Admit; ErrNotQueued; queueNote helpers
internal/relay/queue_test.go   (new)
internal/relay/served.go              RoundStateOf: queued
internal/relay/served_test.go         TestRoundStateOfQueued (add to the existing file or create it)
internal/relay/headless.go            queued early return; served re-queue in the lost-to-restart branch
internal/relay/headless_test.go       re-queue test (existing file; add)
internal/relay/status.go              Done refuses a queued served binding
internal/relay/logline.go             LogLine renders KindQueue as its note
internal/relay/remote.go              observeRemote: RoundQueued case (minimal, see §4.9)
internal/serve/serve.go               Config.MaxBuilders; Config.Hooks; runtimeAt passes Hooks
internal/serve/admit.go        (new)  cap; census; admit; queuedRound
internal/serve/admit_test.go   (new)
internal/serve/rounds.go              handleStartRound: Send(Defer) + admit + Queue on the 201
internal/serve/daemon.go              Tick: admit after the owner loop
internal/serve/bindings.go            handleGetBinding fills Queue; handleDone refuses queued
internal/serve/admin.go               GCAbandoned skips queued
internal/serve/routes.go              handleWhoAmI: Builders, FeatureQueue
internal/serve/serve_test.go          scriptRunner: per-pid liveness (prerequisite for admit_test)
internal/hooks/events.go              EventRoundQueued, EventRoundAdmitted
internal/ingest/facts.go              KindQueue passes through wherever kinds are switched on (no facts)
internal/ingest/outcome.go            same
cmd/relay/serve.go                    --max-builders; Config.MaxBuilders/Hooks; startup log line
cmd/relay/serve_test.go               flag parse only (no herdr, no systemd)
docs/plans/2026-09-21-serve-queue-core.md   copy of this plan (last step)
```

Before naming any new field, grep the struct for an existing one with the
same meaning; if one exists, halt and report rather than duplicate it.

## 3. Data structures

### 3.1 `internal/policy/policy.go`

```go
// Serve configures relay serve (#285). nil is every default.
Serve *ServePolicy `json:"serve,omitempty"`

type ServePolicy struct {
    // MaxBuilders caps headless builders running at once across all
    // owners. nil = max(1, runtime.NumCPU()-1). A value below 1 is a Load error.
    MaxBuilders *int `json:"max_builders,omitempty"`
    // Scope is the per-round systemd scope (part 3 uses it). nil = defaults.
    Scope *ScopePolicy `json:"scope,omitempty"`
}

type ScopePolicy struct {
    Enabled   *bool  `json:"enabled,omitempty"`    // nil = true
    Slice     string `json:"slice,omitempty"`      // "" = systemd default; else must end in ".slice"
    CPUWeight int    `json:"cpu_weight,omitempty"` // 0 = 100; else 1..10000
    MemoryMax string `json:"memory_max,omitempty"` // "" = none; else ^[0-9]+[KMGT]?$
    TasksMax  int    `json:"tasks_max,omitempty"`  // 0 = none; else >= 1
}

func (p Policy) MaxBuildersOrDefault() int
```

`Load` validation errors read `policy.json: serve.max_builders: must be at
least 1`, `serve.scope.slice: must end in ".slice"`, `serve.scope.cpu_weight:
must be 1..10000`, `serve.scope.memory_max: must match ^[0-9]+[KMGT]?$`,
`serve.scope.tasks_max: must be at least 1`. Follow the exact error style
`Load` already uses for `max_tier`; if it wraps differently, match that.

### 3.2 `internal/store`

```go
// QueuedAt is non-zero while the current round is accepted on a server
// and waiting for a builder slot (#285). Zeroed by relay.Admit. Never set
// off the server.
QueuedAt time.Time `json:"queued_at,omitempty"`      // on Binding, next to RoundStartedAt

KindQueue Kind = "queue" // relay -> log only: a served round was queued, admitted or re-queued (#285)
```

`KindQueue` entries take the same `Dir` as `KindSwitch` entries (relay ->
log only). Their `Round` is the queued round. Notes are exact strings, pinned by tests:

```
queued (%d/%d builders busy)             running, cap  -- at accept
started after %s queued                  age via time.Duration.Round(time.Second).String()
started after %s queued (switched: %s)   age, reason    -- admitted through switchBuilder
re-queued (builder lost to a restart)
```

### 3.3 `internal/remote/proto.go`

```go
RoundQueued RoundState = "queued"       // accepted, no process yet (#285)
const FeatureQueue = "queue"            // WhoAmI.Features entry; define next to FeatureTier

// QueueView is a queued round's place in the server's queue.
type QueueView struct {
    Position int       `json:"position"` // 1-based
    Ahead    int       `json:"ahead"`    // Position-1
    Running  int       `json:"running"`
    Cap      int       `json:"cap"`
    Since    time.Time `json:"since"`
}
BindingView.Queue *QueueView `json:"queue,omitempty"`   // non-nil iff RoundState == RoundQueued

// BuildersView is the server's builder census.
type BuildersView struct {
    Running int    `json:"running"`
    Queued  int    `json:"queued"`
    Cap     int    `json:"cap"`
    Scopes  bool   `json:"scopes"`          // part 3 sets it; false here
    Slice   string `json:"slice,omitempty"` // part 3 sets it; "" here
}
WhoAmI.Builders *BuildersView `json:"builders,omitempty"`
```

### 3.4 `internal/relay`

```go
SendOptions.Defer bool   // stage the round but do not spawn; the caller admits it (server only)
var ErrNotQueued = errors.New("round is not queued")
func Admit(ctx context.Context, rt Runtime, name string) error
```

### 3.5 `internal/serve`

```go
Config.MaxBuilders int              // 0 -> cfg.Policy.MaxBuildersOrDefault()
Config.Hooks       hooks.Dispatcher // nil -> none

type queuedRound struct {
    Owner     remote.ClientID
    OwnerRoot string           // the owner's store root, what runtimeAt takes
    Name      string
    QueuedAt  time.Time
}
type census struct {
    Running int
    Queued  []queuedRound      // sorted by QueuedAt, then Owner, then Name
}
func (s *Server) cap() int
func (s *Server) census() (census, error)
func (s *Server) admit(ctx context.Context) error   // caller holds s.mu
```

### 3.6 `internal/hooks/events.go`

```go
EventRoundQueued   EventType = "round_queued"    // a served round was accepted at the cap (#285)
EventRoundAdmitted EventType = "round_admitted"  // a queued round's builder started
```

## 4. Contracts

### 4.1 `relay.Send` with `Defer` (`send.go`)

With `opts.Defer` and a headless binding: run preflight unchanged (runner
present, not busy, candidate resolves, `headlessLaunch` proves argv), then
inside `WithLock` do everything the non-deferred path does **except**
`startRound` and `RoundStartedAt = rt.Now()`; additionally set `b.QueuedAt =
rt.Now()`. The `KindPlan` log entry is appended exactly as today (it is what
makes the round "open"). Postconditions: `Builder.PID == 0`,
`RoundStartedAt.IsZero()`, `!QueuedAt.IsZero()`, `State == active`,
`RoundStateOf(b, entries) == RoundQueued`. A pane binding ignores `Defer`
(the pane path has no spawn). Errors: unchanged.

Do not duplicate the stamp block: factor the shared stamps into a helper
if that is cleaner, but the non-deferred path's behaviour and its existing
tests must not change.

### 4.2 `relay.Admit` (`queue.go`)

Under `rt.Store.WithLock`:

1. `b := tx.Load(name)`; if `b.QueuedAt.IsZero() || b.State != StateActive` -> `ErrNotQueued`.
2. `prompt := composePrompt(b, rt.Store.PlanPath(name, b.Round), rt.Store.ReportPath(name, b.Round), rt.Store.DonePath(name, b.Round))` -- the same call and arguments `Send` used (read `send.go` and match exactly; if Send passes other values, halt and report).
3. If `gatedBuilder(rt, b)` says the candidate is gated: `b, err = switchBuilder(ctx, rt, tx, b, "gated while queued", false /*closeOld*/, false /*counted*/)`; note for the queue entry = `"started after %s queued (switched: gated while queued)"`. Otherwise `b, err = startRound(ctx, rt, b, prompt)`.
4. On `err`: mirror `send.go`'s spawn-failure handling (`State = StateNeedsYou`, `Halt = "builder spawn failed: " + err`), and set `b.QueuedAt = time.Time{}` so the round is never re-admitted; `tx.Save(b)`; return `err`.
5. On success: `age := rt.Now().Sub(b.QueuedAt).Round(time.Second)`; `b.RoundStartedAt = rt.Now()`; `b.QueuedAt = time.Time{}`; append `KindQueue` entry with the note from §3.2; `tx.Save(b)`.

Outside the lock, if `rt.Hooks != nil`: dispatch `Event{Type: EventRoundAdmitted, BindingID: name, State: string(b.State), Round: b.Round, Timestamp: rt.Now()}` the way `emitMutations` in `reconcile.go` dispatches (copy its call shape).

### 4.3 `serve.admit` (`admit.go`)

`cap()`: `cfg.MaxBuilders` if > 0 else `cfg.Policy.MaxBuildersOrDefault()`.

`census()`: walk `<Root>/bindings/*` exactly as `Tick` does (same
`remote.IDFromDir` and skip rules); for each owner `store.New(root).List()`;
a binding is **running** when `b.Builder.Headless() && b.Builder.PID != 0 &&
b.State != StateDone && b.State != StatePaused`; **queued** when
`!b.QueuedAt.IsZero() && b.State == StateActive`. A per-binding load error
is logged and skipped; the first such error is returned after the walk
completes (the census is still usable). Sort `Queued` by `QueuedAt`, then
`Owner`, then `Name`.

`admit(ctx)`: `c, cerr := census()`; `running := c.Running`; for each `q`
in `c.Queued`: if `running >= cap()` break; `err := relay.Admit(ctx,
s.runtimeAt(q.OwnerRoot), q.Name)`; `nil` -> `running++` and log
`admitted owner=<id> binding=<name> running=<running>/<cap>`;
`ErrNotQueued` -> continue; other -> log `admit owner=<id> binding=<name>:
<err>` and continue. Return `cerr`.

Use whatever logger the serve package already uses for daemon lines
(grep `daemon.go` / `serve.go`); do not add a new one.

### 4.4 `handleStartRound` (`rounds.go`)

Replace the `relay.Send(..., SendOptions{Tier: tierStr})` call with
`SendOptions{Tier: tierStr, Defer: true}`. Immediately after a successful
Send, still under `s.mu`: if `s.cfg.Hooks != nil` dispatch
`EventRoundQueued` (Round = the round just staged), then `if err :=
s.admit(r.Context()); err != nil { log it }`. Then reload the binding and
entries and build the 201 `ServedView` as today; if `view.RoundState ==
RoundQueued`, fill `view.Queue` via §4.7. Nothing else in the handler
changes; the error mapping stays. A spawn failure inside `admit` is not a
send error: the send was accepted; the client learns `needs_you` on its
next GET.

Also append the accept-time `KindQueue` entry `queued (%d/%d builders
busy)` -- where? In `Send`'s deferred branch relay does not know the
census. So: `handleStartRound` appends it **after** Send and **before**
admit, through the owner runtime's store (`rt.Store.Append` or whatever
`Send` uses to append its plan entry -- match it), with `running` and
`cap` from a fresh `census()`. If admit then starts the round at once, the
log shows `queued (0/3 busy)` followed by `started after 0s queued`: that
is intended, the log is the audit trail.

### 4.5 `Tick` (`daemon.go`)

After the per-owner loop (all owners reconciled, pids of exited builders
cleared, closed rounds released), call `s.admit(ctx)` and log its error.
Still under `s.mu`.

### 4.6 `reconcileHeadless` (`headless.go`)

Two insertions, nothing else:

- After `roundOpen` is computed and the not-open branch has returned
  (around the current line 350, before `escapeCheck`): `if !b.QueuedAt.IsZero()
  { return b, nil }` (return whatever the function's no-change shape is).
  This must precede the `PID == 0` "spawn failed earlier, wait for human"
  branch, or a queued round would be mistaken for a failed spawn.
- In the `lost := ...` branch (currently ~line 537-562): when `b.Owner !=
  ""`, do not call `startRound`; instead `b.QueuedAt = b.RoundStartedAt`,
  `b.RoundStartedAt = time.Time{}`, `b.Builder = clearProcess(b.Builder)`
  (pid and start already zeroed at line ~540 -- keep that), append
  `KindQueue` `re-queued (builder lost to a restart)`, and return without
  a switch and without counting. `b.Owner == ""` keeps today's relaunch
  byte for byte.

### 4.7 `RoundStateOf`, `ServedView`, `handleGetBinding`

`RoundStateOf` (`served.go`): keep `needs_you` first; then if the plan
entry for `b.Round` is open (present, no report) **and** `!b.QueuedAt.IsZero()`
-> `RoundQueued`; then running / closed / idle as today. `ServedView`
unchanged (it cannot see the census). In `handleGetBinding` and the 201 of
`handleStartRound`: when `view.RoundState == RoundQueued`, `c, _ :=
s.census()`; find this owner+name in `c.Queued` (index `i`); `view.Queue =
&QueueView{Position: i+1, Ahead: i, Running: c.Running, Cap: s.cap(),
Since: b.QueuedAt}`. Not found (raced) -> leave `Queue` nil.

### 4.8 `handleDone`, `GCAbandoned`, `handleWhoAmI`

- `handleDone` (`bindings.go`): where it refuses `RoundRunning`, also refuse
  `RoundQueued` with the same code and the message `round %d is queued;
  unbind to drop it`. `handleUnbind` needs no change (archiving drops the
  round). `relay.Done` (`status.go`): the same refusal for a binding with
  `Owner != "" && !QueuedAt.IsZero()`, so the server-local `relay serve`
  admin verbs agree with the wire.
- `GCAbandoned` (`admin.go`): wherever it skips a running binding, also
  skip a queued one.
- `handleWhoAmI` (`routes.go`): `c, _ := s.census()`; `who.Builders =
  &BuildersView{Running: c.Running, Queued: len(c.Queued), Cap: s.cap()}`;
  append `FeatureQueue` to `who.Features`.

### 4.9 Client minimal tolerance (`remote.go`, `logline.go`)

`observeRemote`: add `case remote.RoundQueued:` that sets
`b.Builder.RemoteStatus = "queued"` (the generic assignment at line ~624
already does this; confirm) and otherwise behaves like `RoundRunning`
minus the stall copy -- no halt, no catch-up, clear `StalledSince`. Part 2
renders it. `LogLine` (`logline.go`): `KindQueue` renders as its note,
prefixed the way `KindSwitch` is (`switch: ...` -> `queue: ...`).

### 4.10 `cmd/relay serve`

`serveFlagSet`: `--max-builders N` (int, default 0 = policy/default). Set
`Config.MaxBuilders` and `Config.Hooks = newHooksDispatcher(hooksCfg, pol)`
using the same constructor and `hooksCfg` the local daemon uses in
`main.go` (`newHooksDispatcher`; find how `hooksCfg` is built there and
reuse it -- config paths compose through `userConfigRoot()`, never by
hand). After the server is constructed, log one line: `builders cap=<n>`
(part 3 appends the scopes word). `serve.runtimeAt` sets `Hooks:
s.cfg.Hooks`.

### 4.11 `scriptRunner` per-pid liveness (`serve_test.go`)

The fake at `serve_test.go:1198` has one shared `alive` flag and a constant
pid 4242, so two bindings cannot be told apart. Change it to allocate an
incrementing pid per `Start` (starting at 4242), keep `alive map[int]bool`,
`Alive(h)` = `alive[h.PID]`, `Kill` clears that pid, `ExitCode` returns
`(0, !alive[h.PID])`. Add `started []relay.ProcSpec` if not already there.
Provide a helper `finish(pid)` that flips one pid dead. Existing tests that
used the shared flag must keep passing; adapt them to the new shape only as
far as compilation and their current assertions require. **Do not touch
`internal/e2e`'s `scriptRunner`** (a separate fake; the e2e tests keep
their single-binding shape).

## 5. Tests

`internal/policy/policy_test.go`: table over `serve.max_builders` (missing
-> `max(1, NumCPU-1)`; 1 ok; 0 error; `-1` error), `serve.scope.*` each
constraint (one good, one bad row per field), unknown key under `serve`
rejected (already by `DisallowUnknownFields`; pin it).

`internal/relay/queue_test.go` (use the package's existing test runtime
helpers and fake runner; look at how `send_test.go` / `headless_test.go`
build a headless binding and a fake `Runner`):
- `TestSendDeferQueues`: after `Send(Defer)`: pid 0, `RoundStartedAt` zero,
  `QueuedAt` == fake now, `State == active`, one `KindPlan` entry, `RoundStateOf == queued`, runner `Start` **not** called.
- `TestAdmitStartsQueuedRound`: `Admit` -> runner `Start` called once, pid
  recorded, `RoundStartedAt` == now, `QueuedAt` zero, last entry `KindQueue` with note `started after 0s queued` (advance the fake clock by 90s first and assert `started after 1m30s queued`).
- `TestAdmitNotQueued`: fresh binding -> `ErrNotQueued`, no spawn.
- `TestAdmitSpawnFailure`: runner `Start` errors -> `needs_you`, `Halt` has `builder spawn failed`, `QueuedAt` zero, error returned.
- `TestAdmitGatedSwitches`: gate the candidate the way `limit_test.go` / `switch_test.go` do; `Admit` spawns the next candidate; `RoundSwitches` unchanged (uncounted); note contains `(switched: gated while queued)`.
- `TestReconcileHeadlessSkipsQueued`: a queued binding through `Reconcile` is unchanged (`SameBinding`), no spawn, no halt.
- `TestLostBuilderRequeuesOnServer`: `Owner != ""`, `rt.StartedAt` after `Builder.StartedAt`, runner reports not alive, no exit trailer -> `QueuedAt == old RoundStartedAt`, `RoundStartedAt` zero, pid 0, entry `re-queued (builder lost to a restart)`, `RoundSwitches` unchanged, **no** `Start` call. And the mirror `TestLostBuilderRelaunchesLocally` (`Owner == ""`) must still pass as it does today -- if such a test exists, do not weaken it.
- `TestRoundStateOfQueued` (`served_test.go`): open plan entry + `QueuedAt` -> queued; open plan entry, zero `QueuedAt` -> running; `needs_you` wins over queued.
- `TestDoneRefusesQueued`.

`internal/serve/admit_test.go` (build on `setupTestEnv` and the handler
tests' way of creating bindings and sending rounds; two enrolled owners):
- `TestAdmitCapQueuesSecondOwner`: cap 1; owner A sends -> 201 `running`; owner B sends -> 201 `queued`, `Queue == {1, 0, 1, 1, since}`; `whoami.Builders == {1, 1, 1}`; B's `bind.json` has `QueuedAt` set and pid 0; the runner has exactly one `Start`.
- `TestAdmitAfterSlotFrees`: continue: finish A's pid (marker + report as the existing round-close tests do), `Tick` -> A closed, B `running`, runner `Start` count 2, B's log has `queued (1/1 builders busy)` then `started after ... queued`.
- `TestAdmitStrictFIFO`: cap 1; A running; B queued; C queued; finish A; Tick -> B running, C still queued with `Position 1`; finish B; Tick -> C running. Order is by `QueuedAt` (use the server's fake `Now` and advance it between sends).
- `TestAdmitFreeSlotButQueueNonEmpty`: cap 1; A running; B queued; finish A **without ticking**; C sends -> C is `queued` behind B (position 2): the queue is non-empty, so a new send never jumps it even though A's pid is dead-not-yet-reaped. Then Tick -> B running, C position 1.
- `TestRestartRequeuesDeadBuilder`: A running; new `Server` over the same root with `StartedAt` later than A's `Builder.StartedAt` and a runner where A's pid is dead with no trailer; Tick -> A `queued` with `QueuedAt == its old RoundStartedAt`, then (cap allows) admitted on the same tick with a new pid; log shows `re-queued` then `started after`.
- `TestUnbindDropsQueued`: B queued; unbind B -> 200; census has no queued; Tick starts nothing new.
- `TestDoneRefusesQueuedRound`: 409 with the message in §4.8.
- `TestGetBindingQueuePosition`: three queued -> positions 1,2,3 by `QueuedAt`.

Each of these must fail when the rule it pins is removed; the planner
mutation-tests `cap()` (return 1000) against `TestAdmitCapQueuesSecondOwner`
and the `QueuedAt` check in `RoundStateOf` against `TestRoundStateOfQueued`.

`internal/ingest`: run the existing tests; if any switch on kinds
exhaustively with a default that errors, add `KindQueue` as a no-op case.

`cmd/relay/serve_test.go`: `--max-builders 3` parses into
`Config.MaxBuilders == 3`; nothing that starts a server, touches herdr or
systemd. This package's `TestMain` already isolates HOME/XDG.

## 6. Steps (one commit each, in this order)

1. `policy`: types, validation, accessor, tests.
2. `store` + `remote` + `hooks`: fields, kind, constants, wire types. `go build ./...`.
3. `relay`: `Send(Defer)`, `Admit`, `RoundStateOf`, `reconcileHeadless` insertions, `Done` refusal, `LogLine`, `observeRemote` case, tests (§5).
4. `serve`: `scriptRunner` per-pid (existing tests green), then `admit.go`, handler/tick/whoami/done/gc changes, tests (§5).
5. `cmd/relay`: flag, hooks wiring, startup line, test. `ingest` pass-through.
6. Gate (§7), plan copy, report.

## 7. Gate

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/ ./internal/serve/ ./internal/policy/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Then copy this plan to `docs/plans/2026-09-21-serve-queue-core.md` and
commit it as `chore(plans): serve queue core`. Code commits: `feat(serve):
...` per step, each message naming #285.

## Report

Per step: tests added (names), the gate output's last lines, commit shas.
List every exported identifier you added or changed with its signature.
State plainly anything you did that this plan did not say, or halt where
it could not be done as written.
