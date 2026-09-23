# Plan: restart hardening, round 2 of 3: operability (#370)

**Read first:** `docs/specs/2026-09-23-restart-hardening-design.md` in your tree, §4.7, §4.8 and §4.9. This round implements those three sections. Where the two disagree, the spec wins. Stop and report rather than pick.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit.

**Parallel branch:** #371 edits `cmd/relay/main.go` (cmdDaemon, and a `daemonNotice` line next to `statusNotice` in `relay status`), `internal/doctor/doctor.go` (the daemon row) and `internal/doctor/env.go`. Keep your edits there minimal and in separate hunks:
- put the restart check in a new file;
- add its row *after* the daemon row with a single call;
- add the status notice line with a single call next to the existing notices.

## 1. System overview

Round 1 made the daemon's judgement across a restart correct. This round makes restarts *safe to do, and visibly so*:

- the systemd scope probe no longer latches a single failure for the daemon's whole life;
- `relay doctor` (and `relay status`, only when it matters) says whether a restart right now would kill anything, from the cgroup every running process actually sits in;
- the shipped unit no longer lets systemd's start limit leave the daemon down.

## 2. File structure

```
internal/proc/proc.go (+ proc_test.go)     scope probe: probeMu/scopes state/scopesFailedAt/now; ScopeReprobeAfter (spec §4.7). Pin probe unchanged.
internal/doctor/restart.go                 NEW  RunningProc, ParseUnifiedCgroup, RestartSafety (spec §4.8)
internal/doctor/restart_test.go            NEW
internal/relay/<new file>.go (+ test)      NEW  RunningProcs(bindings []store.Binding) []doctor-agnostic tuple -- see §4 (pure)
cmd/relay/main.go / doctor wiring file     one call adding the RestartSafety row after the daemon row; status notice via restartNotice
cmd/relay/restart_notice.go (+ test)       NEW  restartNotice(doctor.Check) string (pure)
dist/relay.service                         [Unit] StartLimitIntervalSec=0 + comment (spec §4.9)
scripts/relay-service-template_test.sh     asserts exactly one ^StartLimitIntervalSec=0$ in [Unit]
```

## 3. Data structures

- `doctor.RunningProc{Binding string; Kind string; PID int}`. `Kind` is one of `builder`, `gate` and `consult`.
- `proc.Runner` probe state per spec §4.7: `probeMu sync.Mutex`, `scopes probeState` (the unexported enum `probeUnknown`, `probeOK`, `probeFailed`), `scopesFailedAt time.Time` and an exported `Now func() time.Time` (nil means `time.Now`; tests set it). `ScopeReprobeAfter = 5 * time.Minute` is an exported const.

## 4. Interfaces and contracts

- `func ParseUnifiedCgroup(content string) (path string, ok bool)`: returns the path of the line starting `0::`. No such line → `("", false)`.
- `func RestartSafety(procs []RunningProc, readCgroup func(pid int) (string, error)) Check`: spec §4.8, with the exact texts given there. Details:
  - A process is safe when `path.Base(cgroupPath)` matches `relay-*.scope`.
  - A read error satisfying `errors.Is(err, fs.ErrNotExist)` skips that process (it exited).
  - Any other error, or no `0::` line → OK with the detail `cannot tell on this host`. This is also what darwin gets.
  - The Warn detail lists at most 3 offenders, then `and K more`.
- The collector lives in `internal/relay` so it is testable without cmd/relay: `func RunningProcs(bs []store.Binding) []RunningProcRef`, where `RunningProcRef{Binding, Kind string; PID int}`. cmd/relay converts it to `doctor.RunningProc`, which avoids an import cycle; if doctor already imports relay, or relay imports doctor, halt. It includes only local bindings (`Owner == ""`, and not a remote binding, using whatever predicate the codebase uses for that):
  - `Builder.PID > 0` when `Builder.Headless()`;
  - `GateRun != nil && GateRun.PID > 0`;
  - consults with `State == running`, `Endpoint.Headless()` and `PID > 0`.
- cmd/relay passes `readCgroup = func(pid) { os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid)) }`.
- `func restartNotice(c doctor.Check) string`: "" unless `c.Severity == SevWarn`. Otherwise `restart unsafe: <M> running outside their own scope -- relay doctor`. Take M from a field you add to `Check`, or return it alongside; don't parse it out of the detail string. If `Check` has no suitable field, make `RestartSafety` return `(Check, int)`.
- Scope probe (spec §4.7). The observable contract:
  - a failed probe is retried on the first scoped Start at least `ScopeReprobeAfter` later;
  - success is sticky;
  - the Warn is logged once per failure;
  - a scoped Start made while the state is `probeFailed` and inside the window runs unscoped, as today.

## 5. High-level pseudocode

```
Runner.Start (scoped part only):
  if spec.Scope != nil:
     probeMu.Lock()
     now := r.now()
     if scopes == probeUnknown || (scopes == probeFailed && now.Sub(scopesFailedAt) >= ScopeReprobeAfter):
        if err := ProbeScopes(ctx, slice); err != nil { scopes, scopesFailedAt = probeFailed, now; Warn once }
        else { if scopes == probeFailed { Info "scopes available again" }; scopes = probeOK }
     ok := scopes == probeOK
     probeMu.Unlock()
     if !ok { spec.Scope = nil }
  (pin probe block unchanged)

doctor wiring:  bindings := rt.Store.List(); procs := convert(relay.RunningProcs(bindings)); checks = append(checks, doctor.RestartSafety(procs, readCgroup))
status:         same computation (cheap: one small file read per running process); print restartNotice(...) with the other notices
```

## 6. Error handling strategy

- The restart check never fails `doctor` or `status`. Every read problem degrades to "cannot tell on this host", and an exited process is skipped.
- A probe failure → a Warn, then unscoped as today. There is no new halt path.

## 7. Ordered implementation steps

**Step 1: scope probe re-run** (`internal/proc`). Make the probe injectable: `ProbeScopes` is currently called directly, so add an unexported `probe func(ctx, slice) error` field on Runner, where nil means `ProbeScopes`. Unit tests with a fake probe and fake `Now`:
- a failure then an immediate Start → not re-probed (call count), unscoped;
- after 5 min → re-probed; success → scoped; later Starts don't probe again;
- two failures in a row → two Warns in total, not one per Start (capture slog with a test handler if the package already does that; otherwise assert on the call counts only and say so).

Mutation: drop the time condition, and a named test fails.

If exercising Start in a unit test would need a real `systemd-run`, test through a small extracted method, `scopesUsable(ctx, slice) bool`, holding exactly the pseudocode above.

**Step 2: `ParseUnifiedCgroup` and `RestartSafety`**, with table tests:
- no procs;
- all in `relay-round-local-x-1.scope`;
- one in `relay.service`;
- a mix, with more than 3 offenders (the truncation);
- not-exist skipped;
- a permission error → "cannot tell";
- cgroup v1 content with no `0::` line → "cannot tell".

**Step 3: `relay.RunningProcs`**, with a table test over bindings: a headless builder, a remote/served binding (excluded), a gate run, a running consult, a done consult (excluded) and PID 0 (excluded).

**Step 4: wire doctor and status.** Add `restartNotice` and its test. Add no cmd/relay test that runs doctor against real processes: the rule is tested in steps 2–3 (CLAUDE.md: CI has no harness).

**Step 5: unit.** Add `StartLimitIntervalSec=0` to `dist/relay.service` under `[Unit]`, with a comment: on 2026-09-23 the default start limit (5 in 10 s) left the daemon down until `reset-failed`, and `RestartSec=5` already paces restarts. Extend `scripts/relay-service-template_test.sh` to assert exactly one `^StartLimitIntervalSec=0$` line, and that it appears before the `[Service]` line. Update the script's ok message. Check with `shellcheck` if it is installed.

**Step 6: gate.** `make check` and `make e2e`. Commit ending `(#370)`.

Declared scope: §2's files.
