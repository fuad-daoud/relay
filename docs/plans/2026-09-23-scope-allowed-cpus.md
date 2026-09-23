# Plan: each round is pinned to one core from `scope.allowed_cpus` (#314)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt and
report if any of the following is false.

- **Every `startRound` caller holds a `*store.Tx` for that binding's store.**
  The callers are `startRepairRound` (`repair.go` ~123), `Admit`
  (`queue.go` ~45), `Send` (`send.go` ~370, inside `WithLock`),
  `reconcileHeadless`'s relaunch (`headless.go` ~641) and `switchBuilder`
  (`switch.go` ~165). This plan adds a `tx` parameter to `startRound`, so
  the census can read the store without taking its lock a second time.
  `store.List` takes the state lock, and a nested lock on the same root
  would deadlock.
- **Every server path that reaches `startRound` holds `Server.mu`.** That is
  `Tick` (the per-owner daemon ticks, then `admit`) and `handleStartRound`
  (then `admit`). If any handler or goroutine can start a round **without**
  `s.mu`, the cross-owner census in §4 can race. Do not add a lock yourself.
- **Every closed round passes through `queueReport`'s reset block**
  (`reconcile.go` ~488–520). That covers marker closes, `closeStopped` and
  switch closes. The plan releases the core there.
- **`proc.Runner.Start` takes `spec` by value**, but `spec.Scope` points at
  the caller's `ScopeSpec`. The pinning fallback in §4 clears a field on a
  **copy**, never through that pointer.
- **On this laptop, `cgroup.controllers` exists and lacks `cpuset`.** The
  file is
  `/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/cgroup.controllers`.
  The planner read `cpu memory pids` there. If the file is missing or has a
  different format, the doctor check is built on the wrong file. Running
  `cat` is fine. Do not change any systemd or cgroup setting.

## 1. System Overview

`cpu_quota` (#308) bounds **how much** CPU a round uses. It does not bound
**where** the round runs. The benchmark on #314 (last comment) measured
this on contabo with a CPU-bound `go build`:

- Pinning a job to **one** core cut its CPU time and wall time by 10–18%.
- That held even with nothing else running.
- Three floating jobs at once were no slower than one floating job alone.

So the gain comes from the scheduler migrating a job across cores, not from
jobs contending with each other. A shared multi-core list would still let a
job migrate inside it. What gets the gain is **one core per round**.

This change:

1. **Pool.** `scope.allowed_cpus` (and `serve.scope.allowed_cpus`) is a
   cpu-list such as `"0-2"`. It is the **pool** of cores relay hands out.
   Its syntax is checked at `policy.Load`. When it is unset, nothing in this
   plan runs and no new field is ever written.
2. **One core per round.** When `startRound` launches a round, the round
   takes the lowest pool core that no other live round holds, and it is
   pinned to that core (`AllowedCPUs=<n>`).
   - The core is recorded on the binding (`RoundCPU`).
   - A relaunch after a restart, or a mid-round switch, keeps it.
   - `queueReport` releases it when the round closes.
   - If the pool is exhausted, the round runs on the whole pool with one
     warning. That is never a refusal.
3. **Census.** "Held" means: another binding has `RoundCPU` set **and** its
   builder process is live, or its gate is running.
   - Locally, the census reads the binding store through the caller's `tx`.
   - On a server, it reads every owner's store: the current owner through
     `tx`, the others by path. It is injected as a `Runtime` field so that
     `internal/relay` stays unaware of owners.
4. **Gate, consults and verify.**
   - The **gate** runs on its round's core. It runs on the round's behalf,
     while the round is still open.
   - **Consults** and the **verify reviewer** run on the whole pool. A
     consult runs alongside the builder, so sharing the builder's single
     core would slow both. Verify starts after `queueReport` has already
     released the core.
5. **Pinning probe.** This is a lazy probe in `proc.Runner`. If systemd
   **refuses** `AllowedCPUs`, relay warns once and runs every later spawn
   with its scope and quota but without pinning. A round never fails because
   of pinning.
6. **Doctor.** When `allowed_cpus` is set, `relay doctor` reads the user
   manager's `cgroup.controllers`.
   - It warns, naming the root drop-in as the fix, when `cpuset` is not
     delegated. In that case systemd ignores the property silently, which no
     exit code shows.
   - It warns when the pool names a core this host lacks.

**Decisions already made. Do not revisit them:**

- **Allocation is lowest-free-core, with no balancing and no affinity
  memory.** A binding's next round may land on a different core.
- **Census errors and pool exhaustion both mean "whole pool, warn".** They
  never mean unpinned, and never a failed round.
- **The race between owners is closed by `Server.mu`** (second halt
  condition). Locally, the one store's state lock serialises every
  `startRound`, whether it comes from the CLI or the daemon.
- **relay's own unit files are unchanged.** The fix for delegation is a root
  drop-in on `user@.service`, and doctor prints it.
- **Observability** is the `scopeStatusText` suffix, plus the `round_cpu`
  field in the binding JSON. A `relay status` column is out of scope.
- **Out of scope:**
  - `GOMAXPROCS` (#315). The benchmark ran with `GOMAXPROCS=1`, so a
    single-core round that still runs Go with `GOMAXPROCS=nproc` is the
    obvious follow-up measurement. It is not part of this change.
  - Any `dist/*.service` change.
  - Per-binding core affinity across rounds.

## 2. File Structure

```
internal/policy/policy.go          MODIFY  ScopePolicy.AllowedCPUs; cpuListPattern; ParseCPUList; validateScope rule
internal/policy/policy_test.go     MODIFY  ParseCPUList table; allowed_cpus rows for both blocks
internal/store/types.go            MODIFY  Binding.RoundCPU *int
internal/relay/runner.go           MODIFY  ScopeSpec.AllowedCPUs
internal/relay/runtime.go          MODIFY  Runtime.HeldCPUs
internal/relay/cpus.go             CREATE  pickCPU, localHeldCPUs, heldIn, assignRoundCPU, cpuPinText
internal/relay/cpus_test.go        CREATE  pure tables + census-from-temp-store tests
internal/relay/headless.go         MODIFY  scopeFor gains cpus param; startRound gains tx, assigns the core
internal/relay/headless_test.go    MODIFY  startRound call sites (+tx); scopeFor rows; pin/keep/release tests
internal/relay/{repair,queue,send,switch}.go MODIFY  pass tx to startRound (one line each)
internal/relay/reconcile.go        MODIFY  queueReport reset block: b.RoundCPU = nil
internal/relay/gate.go             MODIFY  gate scope uses the round's core
internal/relay/ask.go              MODIFY  scopeFor(..., "") (pool)
internal/relay/verify.go           MODIFY  scopeFor(..., "") (pool)
internal/proc/scope.go             MODIFY  ScopeArgv emits AllowedCPUs; ProbeAllowedCPUs
internal/proc/scope_test.go        MODIFY  argv rows; probe stub tests
internal/proc/proc.go              MODIFY  Runner pinOnce/pinOK; drop AllowedCPUs on a copy when refused
internal/proc/proc_test.go         MODIFY  fallback, probe-once, caller-unmutated tests
internal/serve/serve.go            MODIFY  runtimeAt sets HeldCPUs (cross-owner census)
internal/serve/admit_test.go       MODIFY  two-owner census test (or serve_test.go; say which)
internal/doctor/scope.go           CREATE  ScopeBlock, UserManagerControllersPath, ScopeChecks
internal/doctor/scope_test.go      CREATE  table over the fake Env
cmd/relay/serve.go                 MODIFY  scopeFromPolicy copies AllowedCPUs; scopeStatusText suffix
cmd/relay/serve_test.go            MODIFY  extend pure TestScopeFromPolicy / TestScopeStatusText tables only
cmd/relay/doctor.go                MODIFY  append doctor.ScopeChecks rows
README.md                          MODIFY  **Scopes.** paragraph (~557)
```

## 3. Data Structures & Type Definitions

### `policy.ScopePolicy.AllowedCPUs` (`internal/policy/policy.go`, after `GateCPUQuota`)

`AllowedCPUs string` with json tag `allowed_cpus,omitempty`.

- `""` means no pinning.
- Otherwise it is the pool of cores relay hands out, one per round. The doc
  comment says so, and says it needs `cpuset` delegated to the user manager,
  which `relay doctor` checks.

### `policy.ParseCPUList(s string) ([]int, error)` (new, exported, pure)

- `cpuListPattern` is `^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`, placed beside
  `cpuQuotaPattern`. No spaces are allowed.
- A range `a-b` requires `a <= b`.
- Any number above 1023 is an error. This stops a typo from making the
  expansion allocate a huge slice.
- It returns the cores **sorted and de-duplicated**.
- Its errors are plain `fmt.Errorf` values, which `validateScope` wraps:
  - `not a cpu list`
  - `range %d-%d runs backwards`
  - `cpu %d is above 1023`

### `store.Binding.RoundCPU` (`internal/store/types.go`, beside `RoundTier`)

`RoundCPU *int` with json tag `round_cpu,omitempty`.

- It is the core the **current** round is pinned to.
- `nil` means none: no pool, an exhausted pool, a census error, or no round
  started yet.
- It is a pointer because core 0 is valid.
- An old binding without the key decodes as `nil`.
- It is cleared by `queueReport`, together with `RoundTier`.

### `relay.ScopeSpec.AllowedCPUs` (`internal/relay/runner.go`)

`AllowedCPUs string`, placed after `TasksMax`. It is a real launch field:
`""` omits it, and otherwise it is a systemd cpu-list (`"2"` or `"0-2"`).

### `relay.Runtime.HeldCPUs` (`internal/relay/runtime.go`)

`HeldCPUs func(tx *store.Tx, self string) ([]int, error)`

- It returns the cores held by live rounds **other than** the binding named
  `self`, in every store that shares this host's pool.
- `nil` means `localHeldCPUs`, which reads only `tx`.
- The server sets it to a closure over its owner root (§4).

### Exact strings

| Where | Text |
|---|---|
| policy error | `"%s: %s.allowed_cpus: %s, got %q: %w"` with `path, prefix, parseErr, sc.AllowedCPUs, ErrBadPolicy` |
| argv | `-p`, `AllowedCPUs=<list>`, after the `CPUQuota` pair and before `MemoryMax` |
| probe unit | `relay-probe-cpus-<8 hex>` |
| probe error | `"systemd-run AllowedCPUs=%s: %s"`, mirroring `ProbeScopes` |
| Runner warn (once) | `"cpu pinning unavailable; scopes will run without AllowedCPUs"`, attrs `allowed_cpus`, `err` |
| pool-exhausted warn | `"cpu pool exhausted; round runs on the whole pool"`, attrs `binding`, `round`, `pool`, `held` (count) |
| census-error warn | `"cpu census failed; round runs on the whole pool"`, attrs `binding`, `round`, `err` |
| `scopeStatusText` suffix | when `AllowedCPUs != ""`, append `"cpus <pool>, one per round"` inside the parentheses, e.g. `on (slice relay.slice, 200%, cpus 0-2, one per round)` or `on (cpus 0-2, one per round)` |
| doctor Group / Name | Group `"scope"`; Name `"allowed_cpus"` for `scope`, `"serve.allowed_cpus"` for `serve.scope` |
| doctor OK | `"cpuset delegated; rounds pinned one per core from %s"` |
| doctor not delegated | Detail `"%s = %s is set but cpuset is not delegated to your user manager; systemd refuses or ignores AllowedCPUs"`. Fix `"as root: mkdir -p /etc/systemd/system/user@.service.d && printf '[Service]\\nDelegate=cpu cpuset io memory pids\\n' > /etc/systemd/system/user@.service.d/delegate.conf && systemctl daemon-reload; then log out and back in"` |
| doctor unreadable | Detail `"cannot read %s (%v); cpu pinning cannot be confirmed"`; `ProbeFailed: true`; empty Fix |
| doctor too many | Detail `"%s names cpu %d but this host has %d (0-%d)"`. Fix `"narrow %s in policy.json to cpus this host has"` |

The `%s` key in the doctor rows is `scope.allowed_cpus` or
`serve.scope.allowed_cpus`.

## 4. Interface Definitions & Component Contracts

### `pickCPU(pool, held []int) (int, bool)` (pure, `cpus.go`)

It returns the lowest core in `pool` that is not in `held`. `ok` is false
when every pool core is held, or when `pool` is empty. `held` may contain
cores outside the pool and duplicates, and both are ignored.

### `heldIn(bindings []store.Binding, self string) []int` (pure, `cpus.go`)

It collects `*b.RoundCPU` for every binding where all of these hold:

- `b.Name != self`
- `b.RoundCPU != nil`
- `b.Builder.PID != 0 || b.GateRun != nil`

Using liveness rather than "the field is set" means a halted, done,
re-queued or crashed round never blocks a core, even on a path that forgets
to clear the field.

### `localHeldCPUs(tx *store.Tx, self string) ([]int, error)`

It is `heldIn(tx.List(), self)`.

### `assignRoundCPU(rt Runtime, tx *store.Tx, b store.Binding) store.Binding` (`cpus.go`)

It is called by `startRound` right before it builds the spec. It never fails.

```
pool = rt.Scope == nil ? nil : policy.ParseCPUList(rt.Scope.AllowedCPUs)   // "" → nil; a parse error cannot happen after Load: treat as nil
if pool empty or tx == nil: b.RoundCPU = nil; return b
held, err = (rt.HeldCPUs ?? localHeldCPUs)(tx, b.Name)
if err: slog.Warn(census-error); b.RoundCPU = nil; return b
if b.RoundCPU != nil and *b.RoundCPU in pool and *b.RoundCPU not in held: return b   // relaunch / switch keep their core
cpu, ok = pickCPU(pool, held)
if !ok: slog.Warn(pool-exhausted); b.RoundCPU = nil; return b
b.RoundCPU = &cpu; return b
```

### `cpuPinText(b store.Binding) string` (pure)

It returns `strconv.Itoa(*b.RoundCPU)` when that is set, and `""` otherwise.
`scopeFor` treats `""` as "use the pool".

### `scopeFor` (changed): `scopeFor(rt Runtime, kind scopeKind, unit, cpus string) *ScopeSpec`

It behaves as it does today, plus: when `cpus != ""`, it sets
`s.AllowedCPUs = cpus`. Otherwise it keeps the template's `AllowedCPUs`,
which is the pool or `""`.

| Call site | `cpus` argument |
|---|---|
| `startRound` | `cpuPinText(b)`, after `assignRoundCPU` |
| gate (`gate.go`) | `cpuPinText(b)` |
| both consult sites (`ask.go`) | `""` |
| verify (`verify.go`) | `""` |

### `startRound` (changed): `startRound(ctx, rt, tx *store.Tx, b, prompt)`

Before it builds `spec`, it runs `b = assignRoundCPU(rt, tx, b)`. The
returned binding carries `RoundCPU`, so the caller's existing `tx.Save`
persists it. On a spawn failure it returns `b` **with** `RoundCPU` set. That
is harmless: `heldIn` ignores it because `PID == 0`. Every caller passes its
`tx`. The seven `headless_test.go` call sites pass a `tx` from `WithLock`, or
`nil` where `rt.Scope` is nil (then `tx` is never read).

### `queueReport` reset block

Add `b.RoundCPU = nil` beside `b.RoundTier = ""`.

### Server census (`internal/serve/serve.go` `runtimeAt(root)`)

It sets
`HeldCPUs: func(tx *store.Tx, self string) ([]int, error) { return s.heldCPUs(root, tx, self) }`.

`(*Server).heldCPUs(root, tx, self)`:

- It walks `<cfg.Root>/bindings/*` the way `census()` does.
- For the directory equal to `root`, it uses `heldIn(tx.List(), self)`.
- For every other owner, it uses
  `heldIn(store.New(dir).List(), "")`: another owner's binding can never be
  `self`.
- It concatenates the results. The first List error is returned, and an
  owner that fails is skipped, as `census()` does.
- `heldIn` must be exported (`relay.HeldIn`) for this.
- The caller already holds `s.mu` (second halt condition). Say so in the doc
  comment.
- Taking another owner's store lock while holding the current owner's is
  safe, because every tick and admit is serialised by `s.mu`, and the admin
  CLI takes one owner lock at a time.

### Pinning probe (`internal/proc`)

This carries over from the previous draft unchanged.

- `ScopeArgv` emits `-p AllowedCPUs=<list>` when the field is set, and its
  argv is otherwise byte-identical.
- `ProbeAllowedCPUs(ctx, slice, cpus string) error` copies `ProbeScopes`: a
  throwaway `relay-probe-cpus-<hex>` scope running `true`, with a 10s timeout.
- `Runner` gains `pinOnce sync.Once` and `pinOK bool`. After the existing
  scope-probe block, if `spec.Scope != nil && spec.Scope.AllowedCPUs != ""`,
  it probes once.
  - On refusal it warns once and sets `pinOK = false`.
  - When `!pinOK`, it does `sc := *spec.Scope; sc.AllowedCPUs = ""; spec.Scope = &sc`
    (a copy, never through the pointer).
- The probe detects refusal only. A silent ignore is doctor's job.

### Doctor (`internal/doctor/scope.go`)

This carries over from the previous draft, with the §3 strings.

- `ScopeBlock{Key, AllowedCPUs string; MaxCPU int}`. The caller computes
  `MaxCPU` with `policy.ParseCPUList`, so doctor does not import policy.
- `UserManagerControllersPath(uid int) string`.
- `ScopeChecks(env Env, blocks []ScopeBlock, controllersPath string, ncpu int) []Check`:
  - It returns nil when every block is empty.
  - It reads `controllersPath` once through `env.ReadFile`.
  - Each non-empty block gets one row. The precedence is: unreadable, then
    no `cpuset` field, then `MaxCPU >= ncpu`, then OK.
- `cmd/relay/doctor.go` builds the blocks, skipping a block whose scope is
  disabled the same way `scopeFromPolicy` does. It uses `os.Getuid()` and
  `runtime.NumCPU()`. No `cmd/relay` test is added for this wiring.

## 5. High-Level Pseudocode

```
Load: validateScope → ParseCPUList(allowed_cpus) or wrapped ErrBadPolicy
wiring: pol.ScopeFor → scopeFromPolicy (copies AllowedCPUs = pool) → Runtime.Scope
server: runtimeAt(root).HeldCPUs = cross-owner census

startRound(ctx, rt, tx, b, prompt):
    b = assignRoundCPU(rt, tx, b)             // keep own core if still free, else lowest free, else pool+warn
    spec.Scope = scopeFor(rt, scopeRound, unit, cpuPinText(b))
    Runner.Start → scope probe (existing) → pin probe (once) → systemd-run ... -p AllowedCPUs=<n>
    return b (RoundCPU set) → caller's tx.Save

gate:     scopeFor(rt, scopeGate, unit, cpuPinText(b))      // the round's core; round still open
consults: scopeFor(rt, scopeConsult, unit, "")               // pool
verify:   scopeFor(rt, scopeVerify, unit, "")                // pool; core already released
close:    queueReport reset → RoundCPU = nil
census:   held = RoundCPU of other bindings with PID != 0 or GateRun != nil
```

## 6. Error Handling Strategy

- **Bad `allowed_cpus`:** `policy.Load` fails with `ErrBadPolicy`, and
  nothing starts. This is the same as a bad `cpu_quota`.
- **Census error or exhausted pool:** one warning, and the round runs on the
  whole pool. This is recoverable per round.
- **systemd refuses `AllowedCPUs`:** the Runner warns once and drops pinning
  for its lifetime. The scope and its quota stay.
- **systemd ignores `AllowedCPUs`:** nothing shows at runtime, and doctor
  warns.
- **Spawn failure:** unchanged. A `RoundCPU` left on a failed binding blocks
  nothing, because its `PID` is 0.
- **Logging:** exactly the three warnings in §3.

## 7. Ordered Implementation Steps

After every step, run `make check` and `gofmt -l $(git ls-files '*.go')`.
The second must print nothing. New tests go in `internal/policy`,
`internal/relay`, `internal/proc`, `internal/serve` or `internal/doctor`. In
`cmd/relay`, only the pure `TestScopeFromPolicy` and `TestScopeStatusText`
tables may grow. Add no `cmd/relay` test that runs a subcommand: CI runners
have no harness binary and no network. No test needs systemd or cpuset
delegation.

1. **Policy.** Add `cpuListPattern`, `ParseCPUList`,
   `ScopePolicy.AllowedCPUs` and the `validateScope` rule.
   *Verify:* a `ParseCPUList` table:
   - `"0"` gives `[0]`, `"0-2"` gives `[0 1 2]`, `"1,3,5-7"` gives
     `[1 3 5 6 7]` and `"3,1-3"` gives `[1 2 3]`.
   - `""`, `"0-"`, `"a"`, `"1 ,2"`, `"3-1"` and `"0-1024"` are each errors.

   Rows for both `scope` and `serve.scope`: `"0-2"` loads, and `"3-1"` is
   refused with `ErrBadPolicy`, naming the field, the block and
   `runs backwards`.

2. **Launch field, argv and status text.**
   - Add `ScopeSpec.AllowedCPUs` and the `ScopeArgv` emission.
   - `scopeFromPolicy` copies the field, and `scopeStatusText` gets the
     suffix.
   - `scopeFor` gains the `cpus` parameter. Update all five call sites with
     the §4 table. In this step `startRound` passes `""`, and step 5 wires
     the real value.

   *Verify:*
   - A `TestScopeArgv` row with `AllowedCPUs: "2"` produces
     `-p AllowedCPUs=2` after the CPUQuota pair.
   - A `TestScopeFromPolicy` row passes the field through.
   - `TestScopeStatusText` covers `on (cpus 0-2, one per round)`.
   - `TestScopeFor` rows: with pool `"0-2"`, `cpus=""` gives
     `AllowedCPUs "0-2"`, and `cpus="1"` gives `"1"`, for every kind.

3. **Pinning probe.** Add `ProbeAllowedCPUs` and the Runner's
   `pinOnce`/`pinOK`, with a `systemd-run` stub written like the existing
   `writeStub` tests.
   *Verify:*
   - `TestProbeAllowedCPUsStub` passes for both success and failure.
   - `TestStartDropsPinningWhenRefused`: the command still runs.
   - After `Start`, the **caller's** `ScopeSpec.AllowedCPUs` is unchanged.
   - Two Starts produce exactly one pinning probe.
   - A Start with no `AllowedCPUs` never probes.

   *Mutation (required):* clear the field through `spec.Scope` instead of on
   a copy, and confirm the caller-unchanged assertion fails. Restore it.

4. **Allocation, pure.** Add `store.Binding.RoundCPU`,
   `Runtime.HeldCPUs`, and in `cpus.go`: `pickCPU`, `HeldIn`,
   `localHeldCPUs` and `cpuPinText`.
   *Verify:*
   - A `pickCPU` table:
     - pool `[0 1 2]` with held `[]` gives 0;
     - with held `[0]` gives 1;
     - with held `[0 2]` gives 1;
     - with held `[0 1 2]` gives not ok;
     - with held `[5 0]` gives 1;
     - an empty pool gives not ok.
   - A `HeldIn` table: `self` is excluded; `PID 0` with no `GateRun` is
     excluded; `PID 0` with a `GateRun` is included; a nil `RoundCPU` is
     excluded.
   - `localHeldCPUs` over a `t.TempDir()` store holding two bindings.
   - A `RoundCPU` of 0 survives a JSON round trip, and a binding JSON without
     the key decodes to nil.

5. **Allocation, wired.** Add `assignRoundCPU`. Give `startRound` its `tx`
   parameter and have it call `assignRoundCPU` and pass `cpuPinText(b)`.
   Update the five callers and the seven test call sites. Add
   `b.RoundCPU = nil` to `queueReport`'s reset block.
   *Verify, with a fake runner that records every spec:*
   - `TestTwoRoundsGetDistinctCores`: pool `0-1`. The first round gets
     `AllowedCPUs "0"` and `RoundCPU 0`, and with that one live, the second
     gets `"1"`.
   - `TestExhaustedPoolRunsOnWholePool`: pool `"0"`, one round live. The
     second gets `AllowedCPUs "0"` (the pool) and a nil `RoundCPU`.
   - `TestRelaunchKeepsItsCore`: a binding with `RoundCPU 1` whose core is
     free gets `"1"` again.
   - `TestRoundCloseReleasesCore`: after `queueReport`, `RoundCPU` is nil,
     and the next round on another binding can take that core.
   - `TestNoPoolWritesNoField`: with `rt.Scope` lacking `AllowedCPUs`,
     `RoundCPU` stays nil and `AllowedCPUs` is `""`.

   *Mutation (required, 1):* make `pickCPU` ignore `held`, and confirm
   `TestTwoRoundsGetDistinctCores` fails. Restore it.

   *Mutation (required, 2):* delete `b.RoundCPU = nil` from `queueReport`,
   and confirm `TestRoundCloseReleasesCore` fails. Restore it.

6. **Gate on the round's core.** The gate's `scopeFor` gets
   `cpuPinText(b)`.
   *Verify:* extend `TestGateStepScopesTheGate` with a binding whose
   `RoundCPU` is 2 and pool `"0-3"`. The gate spec has `AllowedCPUs "2"`.
   The consult and verify scope tests (#313) gain one assertion each: with
   pool `"0-3"`, their `AllowedCPUs` is `"0-3"`.

7. **Server census.** Add `(*Server).heldCPUs`, and have `runtimeAt` set
   `HeldCPUs`.
   *Verify* in `internal/serve` (say which file), using the existing
   two-owner setup from the census and admit tests:
   - Owner A's binding is live with `RoundCPU 0`.
   - Owner B's round, started through the served runtime's `HeldCPUs`, gets
     core 1.
   - A `List` error for one owner skips that owner and still returns the
     others' cores.

8. **Doctor.** Add `internal/doctor/scope.go` and wire it in
   `cmd/relay/doctor.go`.
   *Verify:* a table over the fake `Env`:
   - no blocks gives nil;
   - `"cpuset cpu io memory pids\n"` gives OK;
   - `"cpu io memory pids"` gives the not-delegated warn, whose Fix contains
     `Delegate=cpu cpuset`;
   - an unreadable file gives `ProbeFailed`;
   - `MaxCPU 7` with `ncpu 4` gives `names cpu 7 but this host has 4 (0-3)`;
   - two blocks give two rows.

   `UserManagerControllersPath(1000)` returns the literal path.

   *Mutation (required):* test for `"cpu"` instead of `"cpuset"`, and
   confirm the not-delegated row test fails. Restore it.

9. **README.** In the **Scopes.** paragraph, add:
   - `allowed_cpus`: a pool (`0-2`), and each round is pinned to one core
     from it. The gate uses its round's core. Consults and verify use the
     pool. When every core is taken, a round runs on the whole pool.
   - It needs `cpuset` delegated to the user manager through a root drop-in
     on `user@.service`, and `relay doctor` checks this.
   - If systemd refuses it, relay logs once and runs unpinned.
   - One sentence citing #314's measurement: 10–18% less CPU time for a
     CPU-bound job pinned to one core.

   *Verify:* `make check` passes and gofmt prints nothing.

**Out of scope, do not do:** `GOMAXPROCS` (#315); balancing or affinity
across rounds; a `relay status` column; any `dist/*.service` or
`scripts/relay-service-template_test.sh` change.

## Report

- The files touched, per step.
- All four required mutation checks: what you broke, and which named test
  failed.
- What you found for each halt condition, including the real
  `cgroup.controllers` contents on this machine and every server path to
  `startRound`, with its lock.
- Which `internal/serve` test file you used for step 7.
