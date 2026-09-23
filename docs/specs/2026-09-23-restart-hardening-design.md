# Restart hardening (#370)

Status: approved design, 2026-09-23. Implementation: `docs/plans/2026-09-23-restart-hardening-r1.md`, `-r2.md`, `-r3.md`.

## 1. System overview

Releasing several times a day means restarting `relay daemon` several times a day, and the owner wants a restart to affect no user at all. The 2026-09-23 spike (issue #370, comment "Spike, 2026-09-23") found that the problem the issue was filed for no longer exists on the default configuration. Since #309, every local builder, gate and verify consult runs in its own `relay-{round,gate,verify,consult}-*.scope`. A live test with a throwaway daemon unit confirmed that builders and gates survive `systemctl --user restart`, that the new daemon adopts them, and that a round finished while the daemon was down closes when it comes back.

This design closes what is still wrong. The failure modes are:

- a process that is still alive being read as dead;
- a process lost to a restart being handled as the builder's own failure, or the reverse;
- a lost gate or consult being reported as a result;
- one bad binding taking the whole daemon down;
- the daemon quietly losing its scopes;
- nothing telling the user whether a restart is safe right now;
- a lost builder being restarted from scratch instead of resumed.

Out of scope, and tracked elsewhere:

- the daemon picking up a new binary: #371;
- state-file version skew: #372;
- `relay serve` redeploys: #373, which keeps its #285 re-queue;
- macOS: launchd has no scopes. The `Setsid` supervisor already leaves launchd's process group. This design adds no macOS code, and the doctor check says it can't tell on such a host.

## 2. Components and files

```
internal/proc/
  proc.go            psInfo: a failed ps is no longer "no such process" (R1);
                     the scope probe re-runs after a failure instead of latching it (R2)
  pscheck.go         NEW (R1): classifyPS, the pure decision behind psInfo
internal/relay/
  watched.go         NEW (R1): Watched, the daemon's in-memory "seen alive" set;
                     lostToRestart, the one predicate for lost-to-a-restart
  runtime.go         Runtime.Watched *Watched (R1)
  headless.go        the lost branch uses lostToRestart; RoundStartedAt kept; relaunch
                     prompt gains the interrupted note (R1); resume first (R3)
  gate.go            a lost gate is started again once (R1)
  consult.go         a consult with no exit trailer is Silent, never Done (R1)
  daemon.go          per-binding and per-phase panic recovery in Tick/tickOne (R1).
                     Run() is NOT touched here: #371 owns it.
internal/store/types.go  GateRun.Attempt (R1)
internal/doctor/
  restart.go         NEW (R2): RestartSafety check (cgroup of every running process)
cmd/relay/
  main.go            daemon: rt.Watched = relay.NewWatched() (R1); status notice line (R2)
  doctor wiring      gathers running processes for RestartSafety (R2)
dist/relay.service   [Unit] StartLimitIntervalSec=0 (R2)
scripts/relay-service-template_test.sh  asserts it (R2)
internal/harness/
  resume.go          ResumeBuild: builder-grade resume argv per harness (R3)
```

## 3. Data structures

**`relay.Watched`** (in memory, daemon only)
- `mu sync.Mutex`
- `seen map[watchKey]struct{}`, where `watchKey{PID int; StartedAt int64}` and `StartedAt` is in Unix seconds, exactly as `Endpoint.StartedAt` and `GateRun.StartedAt` record it.
- A nil `*Watched` is valid: it has seen nothing, and `Mark` is a no-op on it.
- Only `relay daemon` creates one. CLI one-shots leave `Runtime.Watched` nil.

**`store.GateRun.Attempt int`** (`json:"attempt,omitempty"`)
- 0 for the first run of a round's gate, 1 for the one re-run allowed after a loss. It is never greater than 1.

**`doctor.RunningProc`** (R2)
- `Binding string`, `Kind string` (`builder`, `gate` or `consult`), `PID int`.

## 4. Contracts

### 4.1 `proc.classifyPS` (R1)

`func classifyPS(ctxErr error, exitErr *exec.ExitError, stdout []byte) psVerdict`

- `psVerdict` is one of `psOK`, `psNoProcess` and `psTransient`.
- `exitErr == nil` → `psOK`.
- `ctxErr != nil` → `psTransient`, because the caller's context was cancelled and the kill was ours.
- The process was signalled (`WaitStatus.Signaled()`) → `psTransient`. systemd SIGTERMs the `ps` child in the unit's cgroup during a stop.
- It exited with code 1 and trimmed stdout is empty → `psNoProcess`. This is how procps and BSD ps say "no such pid".
- Anything else → `psTransient`.

`psInfo` maps `psNoProcess` to `errNoProcess` and `psTransient` to a wrapped non-nil error. Every `Alive` caller already treats an error as "alive this tick" (headless.go:505, gate.go:70, consult.go:96).

### 4.2 `relay.Watched` and `lostToRestart` (R1)

- `func NewWatched() *Watched`
- `func (w *Watched) Mark(pid int, startedAt int64)`: nil-safe.
- `func (w *Watched) Seen(pid int, startedAt int64) bool`: nil-safe, returns false.
- `func lostToRestart(rt Runtime, pid int, startedAt int64) bool` is true only when all of these hold:
  - `!rt.StartedAt.IsZero()`, so only the daemon ever answers true;
  - `startedAt != 0`;
  - `time.Unix(startedAt, 0).Before(rt.StartedAt)`;
  - `!rt.Watched.Seen(pid, startedAt)`.

  So a nil `Watched` answers exactly as the #244 rule did, which keeps the existing #244 tests valid.
- Mark points, so "seen" means "this daemon knew it was running":
  - every `Runner.Alive` in a reconcile path (builder, gate, consult) that returns `(true, nil)`. `(true, err)` is not a sighting;
  - every successful `Runner.Start` made from a reconcile path: `startRound`, the gate start, the verify consult start.

### 4.3 The headless lost branch (R1, then R3)

`lost := codeText == "unknown" && lostToRestart(rt, b.Builder.PID, b.Builder.StartedAt)`

When the round is `switchable`:
- **served** (`b.Owner != ""`): re-queue exactly as today (#285).
- **local**:
  - R3: when `b.Builder.StreamSessionID != ""`, try `resumeRound`. If the harness can't resume, fall through.
  - R1: `startRound` with `composePrompt(...) + "\n\n" + interruptedNote(rt.StartedAt)`.
  - Both keep `b.RoundStartedAt`. Today it is reset (`headless.go:659`), which restarts the round's budget clock.
  - Both stay uncounted against the switch budget.
  - The log entry note names what happened: "resumed session <id> (lost to a daemon restart at T)" or "relaunched ... (lost to a daemon restart at T)".

A builder this daemon saw alive that later exits with no trailer is **not** lost. It takes the normal exited-without-report path: gateOnLimit, denial, then switch or halt as policy says.

`interruptedNote(t time.Time) string` is a constant text with the time interpolated. It says that:
- the round was interrupted at T by a relay daemon restart;
- the working tree may already hold partial edits from an earlier attempt at this same plan, and those edits are the builder's own;
- the builder should run `git status` and `git diff` first, keep what is correct and finish the plan.

### 4.4 Gate (R1)

In `gateStep`'s exited branch, when `!ok` (no trailer):
- If `lostToRestart(rt, GateRun.PID, GateRun.StartedAt) && GateRun.Attempt == 0`: start the gate again with `Attempt = 1`, through the same spec as the first start (factor out a `startGate` helper), and append a `KindGate` entry with the note `gate restarted (lost to a daemon restart): <cmd>`. Return `done == false`.
- Otherwise: `Result "error"`, `Note "no exit trailer"`, as today.

### 4.5 Consult (R1)

In `reconcileConsults`, once the process has exited, read `ExitCode` **before** `FinalText`.

- No trailer: `finishConsult(..., ConsultSilent, note)` and never write the findings file. The note is one of:
  - lost: `lost to a daemon restart before it finished (no exit trailer); partial output: <log>`
  - otherwise: `ended without an exit trailer (killed before it finished); partial output: <log>`
- A trailer: today's logic, unchanged.

Verify consults go through this same path. A silent verify has to keep producing the planner-visible line it produces today.

### 4.6 Panic recovery (R1)

- `tickOne` recovers a panic from anything under it. It logs at Error with `binding`, the panic value and `debug.Stack()`, and returns an error, so `Tick` logs "reconcile failed" and goes on to the next binding. `WithLock` releases both of its locks through defers (store.go:777-802), so unwinding through it is safe.
- `Tick` also wraps `runFires`, `ingestLiveBindings` and `refreshRelease` in a helper, `safely(phase string, f func())`, with the same logging.
- `Daemon.Run` is not modified.

### 4.7 Scope probe (R2)

- `Runner` replaces `probeOnce`/`scopesOK` with `probeMu sync.Mutex`, `scopes probeState` (`unknown`, `ok` or `failed`), `scopesFailedAt time.Time` and `now func() time.Time` (nil means `time.Now`).
- `ScopeReprobeAfter = 5 * time.Minute`.
- A scoped Start probes when the state is `unknown`, or when it is `failed` and at least `ScopeReprobeAfter` has passed since `scopesFailedAt`.
  - Success → `ok`, which is sticky.
  - Failure → `failed` with the time. The warning is logged once per failure, not once per Start.
- The pin probe keeps its `sync.Once`: pinning never decides whether anything survives a restart.

### 4.8 `doctor.RestartSafety` (R2)

- `func ParseUnifiedCgroup(content string) (path string, ok bool)`: the `0::<path>` line of `/proc/<pid>/cgroup`.
- `func RestartSafety(procs []RunningProc, readCgroup func(pid int) (string, error)) Check`, with `Name: "restart"`. A process counts as safe when the last element of its cgroup path matches `relay-*.scope`.
  - No processes: OK, `no rounds running`.
  - All safe: OK, `N running, each in its own scope; a daemon restart leaves them running`.
  - Any unsafe: Warn, `M of N running outside their own scope (<binding> <kind> pid <p> in <last element>, ...): a daemon restart may kill them`, Fix `let them finish before restarting relay.service`.
  - A process whose read fails with not-exist has exited, so it is skipped. Any other read error, or no `0::` line: OK, `cannot tell on this host`.
- `relay status` prints one notice line above the rows only when the check is Warn: `restart unsafe: M running outside their own scope -- relay doctor`. The pure `restartNotice(Check) string` returns "" otherwise.
- The running processes are gathered from local bindings only (`Owner == ""` and not remote):
  - `Builder.PID` when the builder is headless;
  - `GateRun.PID`;
  - every consult in state running with a headless endpoint and `PID > 0`.

### 4.9 `dist/relay.service` (R2)

- Add `[Unit] StartLimitIntervalSec=0`, with a comment: the systemd default of 5 starts in 10 s left the daemon down until `reset-failed` in the 2026-09-23 spike, and `RestartSec=5` already paces restarts.
- The template test asserts that exactly one such line exists.

### 4.10 Resume (R3)

- `func (h Harness) ResumeBuild(sessionID string, l Launch, prompt string, budget time.Duration, dir, state string) ([]string, error)`
  - It returns the builder-grade argv after the binary: the same flags `l.PrintArgs(prompt, budget, dir, state)` yields, plus the harness's resume selector.

  | harness | resume selector |
  |---|---|
  | claude | `--resume <id>` |
  | agy | `--conversation <id>` |
  | opencode | `--session <id> --fork` |
  | codex, unknown | `ErrResumeUnsupported` |

  - A harness whose combined flags R3's step 0 cannot verify against that harness's `--help` is also `ErrResumeUnsupported`.
- `resumeRound(ctx, rt, tx, b, prompt)` has `startRound`'s pre- and postconditions, and builds the argv with `ResumeBuild`.
- The resume prompt restates the contract: the plan path, the report path, the done-marker path and the report block, prefixed by `interruptedNote`.

## 5. Flow (daemon tick, a builder whose process is gone)

```
Alive(pid,start) ->
  err            -> treat alive (unchanged), no Mark
  true           -> Mark; continue as today
  false          -> code := ExitCode(stream)
     marker present            -> marker close (unchanged)
     report present            -> unmarked close (unchanged)
     code unknown && lostToRestart
        served                 -> re-queue (unchanged)
        local  -> [R3] session? ResumeBuild ok? -> resumeRound (keeps RoundStartedAt)
                  else        -> startRound(prompt + interruptedNote) (keeps RoundStartedAt)
     otherwise                 -> escape / gateOnLimit / denial / switch-or-halt (unchanged)
```

## 6. Errors and observability

- `psTransient` becomes a Warn from the caller ("liveness check failed; treating as alive"). This is unchanged.
- A lost gate re-run, a lost consult, a resume and a relaunch each append a log entry the planner sees. No lost event is silent.
- A panic is logged at Error with a stack. The binding is retried on the next tick, and nothing halts it automatically.
- A probe failure is logged at Warn once per failure, and a later success is logged at Info.
- A CLI test must not execute a subcommand that spawns a harness or reaches the network (CLAUDE.md). Every rule above is a pure function or runs against the fake Runner in `internal/relay`.
