# Served builders: a per-server cap with a queue, and a systemd scope per round

**Issues:** #285 (cap concurrent headless builders per box; park the rest --
this design calls the state **queued**), #244 half 2 (builders survive a
daemon restart), and the "server must measure usage" bullet of #216
(per-round cpu/memory facts). Touched, not closed: #204 (the scope is the
seam tenant isolation will hang on), #186 (orthogonal).
**Depends on:** nothing open. Builds on remote builders (#100,
`2026-09-19-remote-builders-design.md`), headless recovery
(`2026-09-13-headless-recovery-design.md`, whose "limiting how many headless
builders run at once ... can become a policy.json knob" is this knob), the
#244 relaunch branch in `headless.go`, hooks (#4).
**Amends:** `policy.json` gains a `serve` section; `remote.RoundState` gains
`queued`; `remote.BindingView` and `remote.WhoAmI` gain optional fields;
`relay.Runner` gains `Rusage`; `relay serve` gains `--max-builders`.
**Status:** design approved 2026-09-21; plans
`docs/plans/2026-09-21-serve-queue-core.md`,
`docs/plans/2026-09-21-serve-queue-surfaces.md`,
`docs/plans/2026-09-21-serve-scopes.md`.

## 1. System overview

Every headless round `relay serve` accepts starts a process immediately.
On 2026-09-21 five rounds landed on a 4-core / 8 GB box inside ten minutes
and all five ran at once: `cpu.pressure some=133 s`, `relay.slice` at its
`MemoryMax`, and the shared opencode service was watchdog-killed by a
newcomer (#256, #285). A slice cap can only OOM-kill; it cannot make the
fifth builder wait. The wait has to live where rounds are dispatched.

Separately (#244), `systemctl --user restart relay-serve` stops the unit's
whole cgroup, and every builder is a child of the daemon, so a restart kills
a builder one step from its report and the round burns its switch budget
on `exited (code unknown)`.

This design does two things on the server and nothing off it:

1. **A cap and a queue.** `serve.max_builders` (default `max(1, NumCPU-1)`)
   counts running headless builders across all owners. A round sent at the
   cap is accepted and stored exactly as today but **queued**: no process,
   no clocks. One serialized `admit` pass -- run by the start handler and
   by every daemon tick -- is the only place a served builder starts. FIFO
   across owners by queue time; a queued round is never a failure, never
   times out, never counts as a switch, and survives a restart because its
   state is on disk.
2. **A transient systemd scope per round.** The runner launches each served
   builder under `systemd-run --user --scope`, a sibling of the service
   under the operator's slice, with an equal `CPUWeight` per round and an
   optional `MemoryMax`. CFS then shares the cores per *round*, not per
   thread, and a daemon restart leaves builders running: the new daemon's
   first tick finds the recorded pid alive (`proc.Alive` is `ps`-based, not
   child-based -- no change needed). The supervisor script reads its own
   cgroup's `cpu.stat` and `memory.peak` before it exits and leaves them as
   a `relay-rusage:` trailer; relay records them on the report entry.

Principles kept: the report file is the contract; the server never loses a
round; nothing over the wire is required for an old client to keep working
(a pre-queue client sees `round_state: "queued"` as an unknown word and
waits; a new client against an old server never sees it).

### Scope boundary

- Server only. `relay daemon` gets no cap and no scopes (`ProcSpec.Scope`
  is nil off the server); CI never sees `systemd-run`.
- Strict FIFO. Per-owner fair share, harness weights, load-based
  admission: out. The one function that picks the next queued round
  (`serve.census` ordering) is where fair share would go later.
- No `withdraw` verb: `relay unbind` drops a queued round. `relay stop`
  stays refused for remote bindings as today.
- No cross-server placement (the separate "server groups" design).
- No tenant isolation (#204): the scope is the seam, not the isolation.
- No `systemctl stop <scope>` kill path; `Runner.Kill` is unchanged.

## 2. File structure

```
internal/policy/policy.go             Policy.Serve *ServePolicy; ServePolicy, ScopePolicy;
                                      validation in Load; accessors MaxBuildersOrDefault, ScopeSpec
internal/store/types.go               Binding.QueuedAt; Endpoint.RemoteQueue *QueueFacts; QueueFacts; Rusage
internal/store/log.go                 KindQueue; LogEntry.Rusage *Rusage
internal/remote/proto.go              RoundQueued; QueueView; BindingView.Queue, .Rusage;
                                      WhoAmI.Builders *BuildersView, FeatureQueue
internal/relay/runner.go              ScopeSpec; ProcSpec.Scope; Runner.Rusage; ProcRusage
internal/relay/queue.go        (new)  Admit, ErrNotQueued, queueEntry helpers
internal/relay/queue_test.go   (new)
internal/relay/send.go                SendOptions.Defer; deferred branch of Send
internal/relay/served.go              RoundStateOf queued; ServedView Rusage
internal/relay/headless.go            queued early return; server-side re-queue instead of relaunch;
                                      startRound sets ProcSpec.Scope from rt.Scope
internal/relay/herdr.go               Runtime.Scope *ScopeSpec
internal/relay/reconcile.go           queueReport records Rusage from rt.Runner.Rusage
internal/relay/remote.go              observeRemote queued case; catchUp records Rusage
internal/relay/status.go              statusRow renders queued; Done refuses queued
internal/relay/logline.go             LogLine for KindQueue and Rusage
internal/proc/scope.go         (new)  ScopeArgv, ProbeScopes, rusage trailer constant + parser
internal/proc/scope_test.go    (new)
internal/proc/proc.go                 Start wraps argv when Scope set; supervisorScript emits relay-rusage;
                                      Rusage method
internal/serve/serve.go               Config.MaxBuilders, Config.Scope, Config.Hooks; runtimeAt passes Hooks, Scope
internal/serve/admit.go        (new)  census, admit, Cap
internal/serve/admit_test.go   (new)
internal/serve/rounds.go              handleStartRound: Send(Defer) then admit
internal/serve/daemon.go              Tick: admit after all owners reconciled
internal/serve/bindings.go            handleGetBinding fills Queue; handleDone refuses queued; GC skips queued
internal/serve/routes.go              handleWhoAmI fills Builders
internal/serve/admin.go               AdminStatus/FlatStatus: builders line, queued rows
internal/serve/serve_test.go          scriptRunner: per-pid liveness
internal/hooks/events.go              EventRoundQueued, EventRoundAdmitted
internal/ingest/facts.go, outcome.go  KindQueue passes through (no outcome change)
cmd/relay/serve.go                    --max-builders; scope probe at start; startup log line; serve status header
cmd/relay/client.go                   relay servers row: builders/queued/scopes
cmd/relay/doctor.go                   servers check: scopes unavailable warning
docs/specs/2026-09-21-serve-queue-and-scopes-design.md   this file
docs/plans/2026-09-21-serve-queue-core.md
docs/plans/2026-09-21-serve-queue-surfaces.md
docs/plans/2026-09-21-serve-scopes.md
```

## 3. Data structures and type definitions

### 3.1 `policy.ServePolicy` (`internal/policy/policy.go`)

```
Policy.Serve *ServePolicy `json:"serve,omitempty"`

type ServePolicy struct {
    MaxBuilders *int         `json:"max_builders,omitempty"` // nil -> max(1, runtime.NumCPU()-1); <1 -> Load error
    Scope       *ScopePolicy `json:"scope,omitempty"`        // nil -> defaults (enabled)
}

type ScopePolicy struct {
    Enabled   *bool  `json:"enabled,omitempty"`    // nil -> true
    Slice     string `json:"slice,omitempty"`      // "" -> systemd default; else must end in ".slice"
    CPUWeight int    `json:"cpu_weight,omitempty"` // 0 -> 100; else 1..10000
    MemoryMax string `json:"memory_max,omitempty"` // "" -> none; else ^[0-9]+[KMGT]?$
    TasksMax  int    `json:"tasks_max,omitempty"`  // 0 -> none; else >= 1
}

func (p Policy) MaxBuildersOrDefault() int          // the rule above
func (p Policy) ScopeSpec() *relay.ScopeSpec        // NOT here: policy must not import relay.
```

`ScopeSpec()` lives in `cmd/relay/serve.go` as `scopeFromPolicy(pol) *relay.ScopeSpec`
(nil when disabled). `policy.Load` validates every constraint above and fails
with `policy.json: serve.<field>: <reason>`; `DisallowUnknownFields` already
rejects a misspelled key.

### 3.2 Store (`internal/store`)

```
Binding.QueuedAt time.Time `json:"queued_at,omitempty"`
    // non-zero iff the current round is accepted and waiting for a slot;
    // zeroed by Admit; never set off the server.

Endpoint.RemoteQueue *QueueFacts `json:"remote_queue,omitempty"`
    // client only: what the last GET said while RoundState was queued;
    // nil in every other state.

type QueueFacts struct {
    Position int       `json:"position"` // 1-based
    Ahead    int       `json:"ahead"`    // Position-1
    Running  int       `json:"running"`
    Cap      int       `json:"cap"`
    Since    time.Time `json:"since"`    // the server's QueuedAt
}

const KindQueue Kind = "queue"
    // relay -> log only. Notes (exact, tests pin them):
    //   "queued (%d/%d builders busy)"            at accept,     Round = the queued round
    //   "started after %s queued"                 at admission,  %s = age, e.g. "4m12s"
    //   "re-queued (builder lost to a restart)"   server restart with a dead pid
    //   "started after %s queued (switched: %s)"  admission through switchBuilder

LogEntry.Rusage *Rusage `json:"rusage,omitempty"`   // on the KindReport entry only

type Rusage struct {
    CPUMS        int64 `json:"cpu_ms"`         // cgroup cpu.stat usage_usec / 1000
    PeakMemBytes int64 `json:"peak_mem_bytes"` // cgroup memory.peak; 0 when unreadable
}
```

### 3.3 Wire (`internal/remote/proto.go`)

```
RoundQueued RoundState = "queued"
FeatureQueue = "queue"                       // in WhoAmI.Features

BindingView.Queue  *QueueView    `json:"queue,omitempty"`   // non-nil iff RoundState == queued
BindingView.Rusage *store.Rusage `json:"rusage,omitempty"`  // closed round's report-entry facts

type QueueView struct {
    Position int       `json:"position"`
    Ahead    int       `json:"ahead"`
    Running  int       `json:"running"`
    Cap      int       `json:"cap"`
    Since    time.Time `json:"since"`
}

WhoAmI.Builders *BuildersView `json:"builders,omitempty"`
type BuildersView struct {
    Running int    `json:"running"`
    Queued  int    `json:"queued"`
    Cap     int    `json:"cap"`
    Scopes  bool   `json:"scopes"`           // scopes enabled AND the startup probe passed
    Slice   string `json:"slice,omitempty"`  // effective slice, "" = systemd default
}
```

`remote` importing `store` for `Rusage`: `remote` already imports `usage`;
check for a cycle (`store` must not import `remote`). If it does, define
`remote.RusageView` with the same two fields and convert at `ServedView`.

### 3.4 Runner (`internal/relay/runner.go`)

```
ProcSpec.Scope *ScopeSpec   // nil = plain spawn (today)

type ScopeSpec struct {
    Unit      string // "relay-round-<ownerhex[:8]>-<name>-<round>"; the runner appends ".scope"
    Slice     string // "" = omit --slice
    CPUWeight int    // always set (>=1)
    MemoryMax string // "" = omit
    TasksMax  int    // 0 = omit
}

type ProcRusage struct { CPUMS, PeakMemBytes int64 }

Runner.Rusage(ctx, h ProcHandle, streamPath string) (ProcRusage, bool)
    // ok iff a relay-rusage: trailer is present; reads the last two lines.

Runtime.Scope *ScopeSpec    // template: Unit is empty; startRound fills Unit per round.
```

### 3.5 Serve (`internal/serve`)

```
Config.MaxBuilders int              // 0 -> policy.MaxBuildersOrDefault()
Config.Scope       *relay.ScopeSpec // nil -> no scopes
Config.Hooks       hooks.Dispatcher // nil -> none (today)

type queuedRound struct { Owner remote.ClientID; OwnerRoot string; Name string; QueuedAt time.Time }
type census struct { Running int; Queued []queuedRound }   // Queued sorted by QueuedAt, Owner, Name
```

### 3.6 Hooks (`internal/hooks/events.go`)

```
EventRoundQueued   EventType = "round_queued"    // Round = queued round
EventRoundAdmitted EventType = "round_admitted"  // Round = the round that just started
```

### 3.7 Send (`internal/relay/send.go`)

```
SendOptions.Defer bool
    // true: stage the round and return without spawning; the caller admits it.
    // Only meaningful for a headless binding; a pane binding ignores it.
```

## 4. Interface definitions and component contracts

### 4.1 `relay.Send` with `Defer` (`send.go`)

Responsibility: accept a round. With `Defer`, everything Send does today up
to and including the plan file, the `KindPlan` log entry, baseline stamps,
`State = active` and the per-round clears -- but **no** `startRound`, no
`RoundStartedAt`, and `b.QueuedAt = rt.Now()`. Preflight is unchanged
(runner present, not busy, candidate resolves, `headlessLaunch` proves the
argv). Postcondition: `RoundStateOf(b, entries) == RoundQueued`,
`Builder.PID == 0`, `RoundStartedAt.IsZero()`. Errors: as today.

### 4.2 `relay.Admit` (`queue.go`)

```
func Admit(ctx context.Context, rt Runtime, name string) error
var ErrNotQueued = errors.New("round is not queued")
```

Under `rt.Store.WithLock`: load; `QueuedAt.IsZero() || State != active` ->
`ErrNotQueued`. Recompose the prompt: `composePrompt(b, Store.PlanPath(name,
Round), Store.ReportPath(...), Store.DonePath(...))` -- the same call Send
made. If `gatedBuilder(rt, b)` reports the candidate gated: `switchBuilder(ctx,
rt, tx, b, "gated while queued", closeOld=false, counted=false)` (it resolves
the next candidate and spawns; a switch here does not count). Else
`startRound(ctx, rt, b, prompt)`. Spawn failure: mirror `send.go`'s handling
(`StateNeedsYou`, `Halt = "builder spawn failed: ..."`, `QueuedAt` zeroed so
the round is not re-admitted), save, return the error. Success: `RoundStartedAt
= rt.Now()`, `QueuedAt = zero`, append `KindQueue` "started after ..." entry,
save, then (outside the lock) `rt.Hooks.Dispatch(EventRoundAdmitted)` when
`Hooks != nil`. Precondition: the caller serializes Admit calls against each
other and against sends (the server's `s.mu`).

### 4.3 `serve.(*Server).admit` (`admit.go`)

```
func (s *Server) cap() int                          // cfg.MaxBuilders or policy default
func (s *Server) census() (census, error)           // walks <Root>/bindings/*/ * like Tick; read-only
func (s *Server) admit(ctx context.Context) error   // caller holds s.mu
```

`census`: per owner dir, `store.List()`; a binding counts as **running** when
`Builder.Headless() && Builder.PID != 0 && State not in {done, paused}`;
**queued** when `!QueuedAt.IsZero() && State == active`. Sort queued by
`QueuedAt`, then owner id, then name.

`admit`: `for q in c.Queued: if running >= cap break; err := relay.Admit(ctx,
s.runtimeAt(q.OwnerRoot), q.Name); nil -> running++; ErrNotQueued -> continue;
other -> log "admit <owner>/<name> round N: <err>" and continue` (the failure
is already recorded on the binding as NEEDS YOU). Returns the first
census error only; Admit errors never abort the pass.

Invariant: `running` never exceeds `cap` as a result of `admit`; a mid-round
switch (kill + spawn in one step) and `handleResume` do not go through admit
and cannot raise the count.

### 4.4 `handleStartRound` (`rounds.go`)

After the worktree is ready and the plan is staged, replace
`relay.Send(ctx, rt, name, plan, SendOptions{Tier})` with
`relay.Send(..., SendOptions{Tier, Defer: true})`, then `s.admit(ctx)` while
still holding `s.mu`. Response: 201 with the reloaded `ServedView`, whose
`RoundState` is `running` when the queue was empty and a slot was free, else
`queued` with `Queue` filled (§4.7). Error mapping unchanged; an Admit spawn
failure surfaces on the next GET as `needs_you` (the client already handles
that), not as a 5xx on the send -- the send *was* accepted.

### 4.5 `(*Server).Tick` (`daemon.go`)

After the per-owner reconcile loop (so exited pids are cleared and closed
rounds have released their slot), call `s.admit(ctx)`. Log its error.

### 4.6 `reconcileHeadless` (`headless.go`)

- Right after `roundOpen` is computed and the not-open branch has returned:
  `if roundOpen && !b.QueuedAt.IsZero() { return b, nil }` -- a queued round
  has nothing to reconcile (no process, no clocks). This must precede the
  `PID == 0` "spawn failed earlier" branch.
- The #244 relaunch branch (`lost := ...`): when `b.Owner != ""` (served
  binding), instead of `startRound`: `b.QueuedAt = b.RoundStartedAt`;
  `b.RoundStartedAt = zero`; `PID, StartedAt = 0, 0`; append `KindQueue`
  "re-queued (builder lost to a restart)"; return. The local daemon keeps
  today's relaunch. `QueuedAt = old RoundStartedAt` puts it ahead of anything
  queued after it started.
- `startRound`: when `rt.Scope != nil`, `spec.Scope = &ScopeSpec{Unit:
  fmt.Sprintf("relay-round-%s-%s-%d", ownerHex8(b.Owner), b.Name, b.Round),
  Slice/CPUWeight/MemoryMax/TasksMax: from rt.Scope}`. `ownerHex8` is the
  first 8 hex chars of `remote.ClientID(b.Owner).Dir()`; "" owner -> "local".
  Unit names must match `^[a-zA-Z0-9:_.\\-]+$`: replace any other rune in the
  binding name with `-`.

### 4.7 `RoundStateOf` / `ServedView` / `handleGetBinding`

`RoundStateOf`: `needs_you` first (unchanged); then, if the plan entry is
open for `b.Round` **and** `!b.QueuedAt.IsZero()` -> `RoundQueued`; then
running/closed/idle as today. `ServedView` copies the closed round's report
entry `Rusage` into `view.Rusage`. `handleGetBinding` (and the 201 of
`handleStartRound`): when `view.RoundState == RoundQueued`, take a `census`
and fill `view.Queue = {Position: index of this binding in c.Queued + 1,
Ahead: Position-1, Running: c.Running, Cap: s.cap(), Since: b.QueuedAt}`.

### 4.8 Client (`remote.go`, `status.go`)

`observeRemote`: new case `RoundQueued`: `b.Builder.RemoteQueue =
&QueueFacts{from view.Queue}`; clear `StalledSince`; do not halt, do not
catch up. Every other case sets `RemoteQueue = nil`. `statusRow` remote
branch: when `RemoteStatus == "queued"` and `RemoteQueue != nil`,
`BuilderStatus = fmt.Sprintf("queued (%d/%d busy on %s, %d ahead, %s)",
Running, Cap, Server, Ahead, age(Since))`; `RemoteQueue == nil` ->
`"queued"`. `catchUp` copies `view.Rusage` onto the report entry it records
(beside `Usage`). `Done` (`status.go`): a served binding with `!QueuedAt.IsZero()`
is refused with `round %d is queued; unbind to drop it` -- same shape as the
running refusal in `handleDone`.

### 4.9 `proc` (`scope.go`, `proc.go`)

```
func ScopeArgv(s relay.ScopeSpec, inner []string) []string
    // ["systemd-run","--user","--scope","--quiet","--collect",
    //  "--unit=<Unit>.scope", ["--slice=<Slice>"], "-p","CPUWeight=<n>",
    //  ["-p","MemoryMax=<m>"], ["-p","TasksMax=<t>"], "--", inner...]
func ProbeScopes(ctx context.Context, slice string) error
    // runs ScopeArgv({Unit: "relay-probe-<8 random hex>", Slice: slice, CPUWeight: 100}, ["true"])
    // with a 10 s timeout; non-zero exit -> error wrapping stderr's first line.
const RusageTrailer = "relay-rusage:"
func ParseRusageTrailer(line string) (relay.ProcRusage, bool)
    // "relay-rusage:cpu_usec=<n> mem_peak=<n>"; either field may be absent; unknown fields ignored.
```

`Start`: when `spec.Scope != nil`, `argv = ScopeArgv(*spec.Scope, [sh, -c,
supervisorScript, relay-supervisor, bin, args...])`; everything else
(Setsid, env, stdout/stderr, Wait goroutine, psInfo) unchanged. Contract
relied on: `systemd-run --scope` registers the scope and then execs the
command in place, so the pid Go recorded is the supervisor's. Verified by a
plan step (`sh -c 'echo $$'` under the scope equals the spawned pid).

`supervisorScript`: after `"$@" </dev/null; rc=$?` and before the exit
trailer, if `/proc/self/cgroup`'s path ends in `/relay-round-*.scope`: read
`/sys/fs/cgroup<path>/cpu.stat`'s `usage_usec` and `/sys/fs/cgroup<path>/memory.peak`,
print `relay-rusage:cpu_usec=<n> mem_peak=<n>` (omit a field that cannot be
read). Any failure prints nothing; the exit trailer is unconditional as today.

`Rusage`: read the stream's last two lines; if the second-to-last starts
with `RusageTrailer`, parse it. (Last line is `relay-exit:`.)

### 4.10 `cmd/relay serve` (`serve.go`)

`--max-builders N` (0 = policy/default) -> `Config.MaxBuilders`. Scope:
`scopeFromPolicy(pol)`; if non-nil, `proc.ProbeScopes(ctx, slice)`; on error
log `scopes unavailable: <err>; builders will run in the daemon's cgroup`
and set `Config.Scope = nil`. Startup log line: `builders cap=<n> scopes=<on
(slice X)|off|unavailable>`. `serve status` header line `builders <running>/<cap>,
queued <n>`; queued rows `queued <age> (<ahead> ahead)`.

`handleWhoAmI`: `Builders = {Running, Queued from census, Cap, Scopes: cfg.Scope != nil,
Slice}`; append `FeatureQueue` to `Features`. `relay servers` row appends
`builders 2/3, 1 queued, scopes on (relay.slice)`; `relay doctor`'s servers
check warns `scopes unavailable on <server>: a daemon restart kills its
builders` when `Features` has `queue` and `Builders.Scopes == false`.

## 5. High-level pseudocode

```
handleStartRound(req):
    lock s.mu; load, authz, round checks (unchanged); unlock for Absorb; relock
    checkout/merge worktree; stage plan (unchanged)
    b, err := relay.Send(rt, name, plan, {Tier, Defer: true})
    map err (unchanged)
    hooks(EventRoundQueued)            -- only when Hooks != nil
    s.admit(ctx)                       -- may start this round immediately
    reload; view := ServedView(b); if queued: view.Queue = position(census)
    201 view

Tick():
    lock s.mu
    for owner in bindings/: NewDaemon(runtimeAt(owner)).Tick()   -- clears exited pids, closes rounds
    s.admit(ctx)
    unlock

admit():
    c := census()
    running := c.Running
    for q in c.Queued:
        if running >= cap: break
        switch err := relay.Admit(ctx, runtimeAt(q.OwnerRoot), q.Name):
            nil:          running++
            ErrNotQueued: continue
            default:      log; continue

relay.Admit(rt, name):
    WithLock:
        b := load; if !queued: return ErrNotQueued
        prompt := composePrompt(b, plan, report, done)
        if gated(b): b, err = switchBuilder(... closeOld=false, counted=false)
        else:        b, err = startRound(rt, b, prompt)
        if err: b.State = needs_you; b.Halt = "builder spawn failed: "+err; b.QueuedAt = 0; save; return err
        age := now - b.QueuedAt
        b.RoundStartedAt = now; b.QueuedAt = 0
        append KindQueue "started after <age> queued"
        save
    hooks(EventRoundAdmitted)

reconcileHeadless(b):                    -- additions only
    ... roundOpen computed; not-open branch returns ...
    if roundOpen && b.QueuedAt != 0: return b        -- nothing to do
    ... exited branch ...
    if lost && b.Owner != "":            -- served: re-queue, never relaunch past the cap
        b.QueuedAt = b.RoundStartedAt; b.RoundStartedAt = 0; clearProcess
        append KindQueue "re-queued (builder lost to a restart)"; return b

startRound(rt, b, prompt):               -- addition only
    if rt.Scope != nil: spec.Scope = scopeFor(rt.Scope, b)

proc.Start(spec):
    inner := [sh, -c, supervisorScript, relay-supervisor, bin, args...]
    argv := spec.Scope == nil ? inner : ScopeArgv(*spec.Scope, inner)
    ... unchanged ...

supervisorScript:
    raise oom_score_adj (unchanged)
    "$@" </dev/null; rc=$?
    if own cgroup is relay-round-*.scope: print relay-rusage:cpu_usec=.. mem_peak=..
    print relay-exit:$rc

queueReport(b):                          -- addition only, headless
    if r, ok := rt.Runner.Rusage(h, streamPath); ok: reportEntry.Rusage = &Rusage{r}

client observeRemote(view):
    case queued:  RemoteStatus="queued"; RemoteQueue = view.Queue; StalledSince = 0
    other cases:  RemoteQueue = nil (plus today's handling)
catchUp: reportEntry.Rusage = view.Rusage
```

## 6. Error handling strategy

| Situation | Class | Handling |
|---|---|---|
| `policy.json serve.*` invalid | non-recoverable at start | `policy.Load` error names the field; `relay serve` exits 1 as for any policy error |
| `systemd-run` missing / probe fails | recoverable | scopes off for the process lifetime; one log line; surfaced on whoami/servers/doctor/serve status |
| Admit spawn failure | recoverable by the human | binding `needs_you`, `Halt = builder spawn failed`, `QueuedAt` zeroed (never re-admitted); no slot consumed; next GET tells the client |
| Admit on an unbound/done binding (raced) | expected | `ErrNotQueued`, skipped |
| census read error (a corrupt bind.json) | recoverable | that binding is skipped; error logged once per tick; other owners still admitted |
| Daemon restart, builder alive in its scope | none | first tick: `Alive` true, round continues |
| Daemon restart, builder dead (box reboot) | recoverable | re-queued at the head, no switch counted |
| Rusage trailer absent (killed supervisor) | expected | no facts recorded, as for `code unknown` |
| Old client, new server, queued round | compatible | client shows the raw word `queued`, keeps waiting |

Logging: every queue transition is a `KindQueue` log entry (so `relay
history`, `relay show --log` and the db ingest see it) plus a daemon log
line `queued|admitted|re-queued owner=<label> binding=<name> round=<n>
running=<r>/<cap>`. Hooks: `round_queued`, `round_admitted`, best-effort
like every other event.

## 7. Testing (what the plans pin)

- `internal/relay/queue_test.go`: Defer leaves PID 0 / `QueuedAt` set /
  `RoundStateOf == queued` / no `RoundStartedAt`; Admit spawns and stamps and
  writes the `queue` entry; Admit on a non-queued binding is `ErrNotQueued`;
  spawn failure is `needs_you` with `QueuedAt` zero; gated candidate goes
  through `switchBuilder` uncounted. Mutation: drop the `QueuedAt` test in
  `RoundStateOf` -> `TestRoundStateOfQueued` fails.
- `internal/serve/admit_test.go`: cap 1, two owners: second is queued;
  finish first + Tick admits second (order by QueuedAt across owners); a
  third send with a free slot but a non-empty queue still queues; a
  restarted server with a dead pid re-queues at the head; unbind drops a
  queued round; `GET` fills `Queue` with the right position; `whoami`
  reports the census. Prerequisite: `scriptRunner` tracks liveness per pid.
- `internal/proc/scope_test.go`: `ScopeArgv` table; `ParseRusageTrailer`
  table; `Rusage` reads the second-to-last line; `ProbeScopes` against a
  stub `systemd-run` on a temp `PATH` (passes when the stub execs after
  `--`, fails when the stub exits 1) -- CI has no systemd.
- `cmd/relay`: none that reach herdr or systemd; `--max-builders` parse is a
  flag test only.
- `make e2e` after the rusage step (touches `queueReport`).
- Manual, planner runs on contabo: start a round; `systemctl --user restart
  relay-serve`; the builder pid is alive under `relay.slice/relay-round-*.scope`;
  the round closes normally with a `rusage` on its report entry.

## 8. Out of scope (repeated so a builder does not add them)

Fair share, load-based admission, harness weights, `withdraw`, local daemon
cap, cross-server placement, tenant isolation, scope-based kill.
