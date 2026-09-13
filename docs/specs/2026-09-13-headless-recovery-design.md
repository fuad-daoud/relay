# Headless recovery: builders outlive the daemon's cap, rebind keeps the mode

**Issues:** #120 (headless builders run under the daemon unit's
`MemoryMax=128M`; an OOM there kills the daemon and every builder) and #119
(`bind --resume --rebind` turns a headless binding into a pane builder).
Both found on 2026-09-12/13 while running the first real headless rounds
(#114 step 2); #120 is what ended those rounds, #119 bit while recovering.
**Depends on:** nothing open. #99 (headless builders, #111) is the feature
both defects live in.
**Amends:** `dist/relay.service` (the unit no longer caps memory);
`internal/proc` supervisor script (one more line before the builder runs);
`Bind`'s resume contract in `bind.go` (headless bindings rebind as headless).
README and `docs/design.md` say nothing about the cap; nothing to amend there.
**Status:** implemented by `docs/plans/2026-09-13-headless-recovery.md`.

## 1. System overview

A headless builder (#99) is a process `relay` starts per round through
`proc.Runner.Start`. That runner has two callers: `send.go`, where the
planner's `relay send` starts the round from the planner's terminal, and
`switch.go`, where the daemon starts it after a mid-round switch. A process
started by the daemon is a child of `relay daemon` and so lives in
`relay.service`'s cgroup, which `dist/relay.service` caps at
`MemoryMax=128M` -- a figure chosen when the daemon was the only thing in the
unit. A single `claude -p` exceeds it. On 2026-09-12 two bindings switched to
the same provider on one tick, the cgroup spilled into swap, the kernel
OOM-killed the unit, and because systemd's default `OOMPolicy=stop` treats any
OOM kill in the unit as the unit failing, the *daemon* died with the builders.
`Restart=on-failure` brought it back, it found both processes gone "without a
report" (a SIGKILLed supervisor never writes the `relay-exit:` trailer),
switched again into the same cap, and repeated until both bindings halted at
`max_switches`.

Recovering from that exposed #119: `relay bind --resume --rebind` on a
binding created with `--headless` produced a pane builder. The headless spec
fixes a binding's mode at creation (`ErrHeadlessResume`), and the daemon's
`switchBuilder` honours it by passing `Headless: b.Builder.Headless()` into
`resolveBuilder`; the manual path in `resume` passes the CLI's `opts`, whose
`Headless` is always false on resume because `--headless --resume` is refused
up front, so `resolveBuilder` splits a pane. The same branch has a second
headless gap: its "is the old builder still alive" check is `FindAgent` over
herdr's agent list, which cannot see a process, so a rebind over a *running*
headless builder would silently orphan it.

This design does three things to the unit and supervisor and one thing to
`resume`:

1. The unit stops capping memory. The daemon was never what used it.
2. The unit sets `OOMPolicy=continue`: an OOM-killed builder is an exited
   builder, seen on the next tick and handled by the existing exit/switch/halt
   path; the daemon stays up.
3. The supervisor raises its own `oom_score_adj` before running the builder,
   so under real memory pressure the kernel takes a builder before the daemon.
4. `resume` derives the new builder's mode from the stored binding, checks a
   headless builder's liveness through the `Runner`, and refuses a pane where a
   process was.

Out of scope, as #120 says: limiting how many headless builders run at once,
or staggering simultaneous switches. That is a policy about the machine, not a
defect, and can become a `policy.json` knob if it is ever needed.

## 2. File structure

```
dist/relay.service                      MemoryMax removed; OOMPolicy=continue added
internal/proc/proc.go                   supervisorScript gains the oom_score_adj line
internal/proc/proc_test.go              real-process case: builder sees oom_score_adj 500
scripts/relay-service-template_test.sh  new: asserts the template's [Service] keys
internal/relay/bind.go                  resume: headless branch (mode, liveness, refuse pane)
internal/relay/bind_test.go             three headless resume cases
docs/specs/2026-09-13-headless-recovery-design.md   this file
docs/plans/2026-09-13-headless-recovery.md          the plan
```

`scripts/plugin-install-service_test.sh` stubs the unit template on purpose
(it tests the installer's behaviour, not the template's content), so the
template assertion is a separate script test. It takes the template path as
an optional first argument, defaulting to `$(dirname "$0")/../dist/relay.service`,
so it can be pointed at an old revision to prove it fails. `make check`
already runs every `scripts/*_test.sh`; the new file is picked up by the glob.

## 3. Data structures and type definitions

No new types. Fields read or written that were not before:

| Type | Field | Use in this design |
|---|---|---|
| `store.Endpoint` | `Mode` | `resume` reads it (via `Headless()`) to choose the rebind path and to set `BindOptions.Headless` |
| `store.Endpoint` | `PID`, `StartedAt` | `resume` builds a `ProcHandle` from them (`handleOf`) for the liveness check; `PID == 0` means no process to check |
| `relay.BindOptions` | `Headless` | set from the stored binding in the resume branch, no longer only from the CLI flag |

The rebound endpoint is what `resolveBuilder` returns for the headless case
today: `AgentName` and `Kind` from the candidate, `Mode: ModeHeadless`,
`PID 0`, `StartedAt 0`, `LogPath ""`. Nothing is running until the planner's
next `relay send`, which is the same contract a pane rebind has ("still on
round N; resend the round").

## 4. Interface definitions and component contracts

### 4.1 `dist/relay.service` -- `[Service]` section

Postconditions on the checked-in template:

- No `MemoryMax=` key.
- `OOMPolicy=continue` present.
- The comment that justified the cap ("the daemon holds no long-lived state")
  is replaced by one that says why there is none: headless builders are
  children of the daemon and share its cgroup; a cap sized for the daemon
  kills them, and `OOMPolicy=stop` (the default) then kills the daemon too.

The macOS plist template has no memory limit and is untouched.

### 4.2 `proc.supervisorScript`

Contract, unchanged parts first: plain `sh`, no bash-isms; runs `"$@"` with
stdin closed; whatever happens to the builder, appends `relay-exit:<code>`.

Added: before `"$@"`, the script writes `500` to `/proc/self/oom_score_adj`,
ignoring failure and hiding stderr. The write applies to the `sh` process and
is inherited by the builder it starts. On a system without `/proc` (macOS) or
where the write is refused, the builder runs exactly as before.

Postconditions: the builder's `/proc/<pid>/oom_score_adj` reads `500` on
Linux. A builder that is OOM-killed still leaves `relay-exit:137` in the log,
because the `sh` that writes the trailer is not the process the kernel chose
(it is idle and tiny).

### 4.3 `relay.resume` (in `bind.go`) -- headless branch

Only the `rebinding` case changes. Preconditions and errors, in the order the
branch checks them, for a stored binding `b` with `b.Builder.Headless()`:

| Check | Result |
|---|---|
| `b.State == StateDone` | error "binding is done" (unchanged, checked first) |
| `opts.BuilderPane != ""` | `ErrHeadlessAdopt` -- a pane cannot replace a process builder |
| `rt.Runner == nil` | `ErrRunnerUnavailable` |
| `b.Builder.PID != 0 && Runner.Alive(handleOf(b.Builder))` | `ErrBuilderAlive` |
| `Runner.Alive` returns an error | that error, wrapped `"check builder process: %w"` |

The `DiagnoseBuilder` guard (`ErrBuilderUnverified`, `--assume-dead`) is not
consulted for a headless binding: it exists to tell a dead pane from a moved
one, and a PID has no such ambiguity. `ListAgents` is not called either --
there is no pane to look for and the planner pane was already resolved by the
caller.

Then `resolveBuilder` is called with `opts.Headless = true`, so it takes the
headless branch and records an endpoint without spawning anything.

Postconditions (added to the existing list on `resume`): a headless binding
rebinds to a headless endpoint; a pane binding rebinds to a pane, as today.
The mode of a binding is never changed by `bind --resume`.

The pane branch is untouched: `ListAgents`, `FindAgent`, `DiagnoseBuilder`,
`resolveBuilder` with the CLI's `opts`, in that order.

## 5. High-level pseudocode

```
resume(rt, opts, planner):
    rebinding = opts.Rebind or opts.Candidate != "" or opts.BuilderPane != ""
    if rebinding:
        b = Store.Load(opts.Name)
        if b.State == Done: return "is done" error
        if b.Builder.Headless():
            if opts.BuilderPane != "": return ErrHeadlessAdopt
            if rt.Runner == nil:      return ErrRunnerUnavailable
            if b.Builder.PID != 0:
                alive, err = Runner.Alive(handleOf(b.Builder))
                if err:   return wrapped err
                if alive: return ErrBuilderAlive
            opts.Headless = true
        else:
            (existing pane path: ListAgents, FindAgent, DiagnoseBuilder guard)
        builder, res = resolveBuilder(rt, nil, opts, opts.Name, planner.PaneID)
    (existing locked save: Planner, State, Builder, BuilderCandidate, cleared screen fields, log entry)
```

```
supervisorScript (sh):
    echo 500 > /proc/self/oom_score_adj 2>/dev/null || true
    "$@" </dev/null
    echo "relay-exit:$?"
```

Daemon behaviour after an OOM kill of a builder, with `OOMPolicy=continue`:
the daemon is not restarted; on its next tick `Runner.Alive` is false,
`ExitCode` reads `137` from the trailer, and the existing headless exit path
runs (exit-with-report closes `unmarked`; exit-without-report switches or
halts per `policy.json`). No relay logic changes for this; the unit key is
what makes the existing path reachable.

## 6. Error handling strategy

All errors in this design are refusals surfaced to the planner by `relay
bind`; none are new types.

| Error | Recoverable | What the planner does |
|---|---|---|
| `ErrHeadlessAdopt` on resume | yes | drop `--builder <pane>`; `--rebind` alone picks a candidate |
| `ErrRunnerUnavailable` | no (runtime misconfiguration) | not reachable from the CLI, which always sets `Runner` |
| `ErrBuilderAlive` | yes | wait for the process, or `relay done`/`relay unbind` to stop it |
| wrapped `Runner.Alive` error | yes | `ps` failed; retry |

Observability: the resume branch logs nothing new. A rebind already appends a
`pickEntry` to the round log; that entry now records a headless candidate for
a headless binding, which is the evidence `relay status` and the log give that
the mode was kept.

## 7. Ordered implementation steps

1. **Unit template.** Edit `dist/relay.service` per §4.1. Add
   `scripts/relay-service-template_test.sh` that greps the template for
   `OOMPolicy=continue` and for the absence of `MemoryMax`. Verify: the new
   script fails when given the pre-change template (`git show
   HEAD:dist/relay.service > /tmp/old.service; sh scripts/relay-service-template_test.sh /tmp/old.service`)
   and passes on the edited one; `make check` runs it.
2. **Supervisor.** Add the `oom_score_adj` line to `supervisorScript` per
   §4.2. Add a `proc_test.go` case that starts `cat /proc/self/oom_score_adj`
   under the runner, waits for the trailer, and asserts the log's first line
   is `500`; skip on non-Linux. Verify: `make check`; mutation -- delete the
   echo line, the new case reads `0` and fails, nothing else does.
3. **Resume keeps the mode.** Implement the headless branch of §4.3 / §5 in
   `bind.go`. Add to `bind_test.go`: (a) headless binding, dead PID in
   `fakeRunner`, `Resume+Rebind` -> endpoint `ModeHeadless`, `f.starts` empty,
   no tab created; (b) headless binding, alive PID -> `ErrBuilderAlive`;
   (c) headless binding, `Resume` with `BuilderPane` set -> `ErrHeadlessAdopt`.
   Verify: `make check`; mutations -- drop `opts.Headless = true` and (a)
   records a tab; revert the liveness check to `FindAgent` and (b) passes
   through to a rebind. These are pure `internal/relay` tests; no CLI test
   reaches herdr.
4. **Real-machine check** (planner, after merge): reinstall the service,
   `systemctl --user show relay -p MemoryMax -p OOMPolicy` reads `infinity`
   and `continue`; during a headless round `cat /proc/<builder pid>/oom_score_adj`
   reads `500`; `relay bind --resume --name <headless binding> --rebind` on a
   halted headless binding leaves `relay status` showing `builder headless`.
