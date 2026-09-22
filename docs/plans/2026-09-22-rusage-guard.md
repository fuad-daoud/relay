# Round 2: the rusage guard must key on relay's intent, not on an inherited cgroup (#216, regression from #290)

This plan stands alone. If a step is impossible as written or contradicts
the code, **halt and report** -- do not improvise around it. Your round-1
changes are still in this worktree, uncommitted; **keep them** (they are
correct and this round commits them together with the change below).

You are a headless builder on a server-side worktree. No `origin`: never
fetch, pull or rebase. Never run `make check`; run the gate in §5. Every
command in the foreground; no sub-agents for edits.

## 1. What round 1 got right, and what it misdiagnosed

Round 1's `Rusage` tail-scan fix is correct and stays exactly as it is.

Round 1 then reported five failures in `internal/proc/proc_test.go`
(`TestStartCapturesBothStreamsAndTheExitTrailer`,
`TestTrailerIsOnItsOwnLineAfterAPartialWrite`, `TestStartRunsInDirWithExtraEnv`,
`TestStartedProcessInheritsRaisedOOMScore`, `TestSupervisorEmitsRusageOnlyInScope`)
as "pre-existing, environment-caused, unrelated". They are pre-existing on
that host and they are not caused by round 1 -- but they are **not**
environmental. They are a real defect introduced by #290, and the planner
reproduced them on a different machine by running the same tests inside a
scope:

```
$ systemd-run --user --scope --unit=relay-round-probe-1.scope -- go test -run TestSupervisorEmitsRusageOnlyInScope ./internal/proc/
proc_test.go:304: stream = "\nrelay-rusage:cpu_usec=657117 mem_peak=100106240\n\nrelay-exit:0\n";
                  a plain spawn outside a relay-round-*.scope must print no rusage line
```

Cause: `supervisorScript` decides whether to emit the trailer by matching
its **inherited** cgroup against `*/relay-round-*.scope`. A process spawned
*inside* a round's scope inherits that cgroup, so a **plain** (unscoped)
spawn started by a builder that is itself running in a scope wrongly emits
a rusage line. You are that builder: relay's own test suite, run on a
scoped server, is exactly the case that breaks. It also corrupts the stream
of any unscoped process relay spawns from inside a round.

## 2. The fix: the supervisor is told which unit is its own

`buildArgv` passes the expected scope unit file name to the supervisor as
its **first argument**, and the script emits the trailer only when that
argument is non-empty **and** its own cgroup's last path element equals it.
A plain spawn passes `""` and therefore never emits, whatever cgroup it
inherited.

- `internal/proc/scope.go`: add `func ScopeUnitFileName(unit string) string`
  returning `unit + ".scope"`, and use it in `ScopeArgv` for the `--unit=`
  value, so the name is built in exactly one place.
- `internal/proc/proc.go`, `buildArgv`: the inner argv becomes
  `[]string{"/bin/sh", "-c", supervisorScript, "relay-supervisor", want, bin}`
  plus `spec.Argv[1:]`, where `want = ScopeUnitFileName(spec.Scope.Unit)`
  when `spec.Scope != nil`, else `""`.
- `internal/proc/proc.go`, `supervisorScript`: take and consume that first
  argument, and gate the whole rusage block on it:

```
want=$1
shift
{ echo 500 >/proc/self/oom_score_adj; } 2>/dev/null || true
"$@" </dev/null; rc=$?
if [ -n "$want" ]; then
  cg=$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -1)
  case "$cg" in */"$want")
    u=$(awk '/^usage_usec/{print $2}' "/sys/fs/cgroup$cg/cpu.stat" 2>/dev/null)
    m=$(cat "/sys/fs/cgroup$cg/memory.peak" 2>/dev/null)
    printf '\nrelay-rusage:%s%s\n' "${u:+cpu_usec=$u}" "${m:+ mem_peak=$m}"
    ;;
  esac
fi
printf '\nrelay-exit:%s\n' "$rc"
```

Keep the quoting in `*/"$want"` exactly as written: it makes the unit name
a literal, not a glob. Change nothing else about the script -- the
`oom_score_adj` line, the `</dev/null`, the leading `\n` of each printf and
the unconditional exit trailer all stay.

## 3. Tests

In `internal/proc` (put each beside the most similar existing test):

1. **`TestBuildArgvPassesTheWantedUnit`** (pure, no processes): a spec with
   `Scope: &relay.ScopeSpec{Unit: "relay-round-abc-x-1", CPUWeight: 100}`
   puts `"relay-round-abc-x-1.scope"` in the supervisor's first-argument
   slot (immediately after `"relay-supervisor"`) **and** in the
   `--unit=relay-round-abc-x-1.scope` element; a spec with `Scope: nil`
   puts `""` in that slot and has no `systemd-run` element.
2. **`TestSupervisorEmitsRusageOnlyInScope`** (the existing test): it must
   now pass **whatever cgroup the test process is in**. Do not weaken it;
   it is the regression. If its current body needs the new argument slot to
   compile or to keep meaning the same, adjust only that.
3. **`TestSupervisorEmitsRusageWhenUnitMatches`**: read the test process's
   own cgroup (`/proc/self/cgroup`, last path element); `t.Skip` when
   `/sys/fs/cgroup<cg>/cpu.stat` is not readable (CI runners and macOS).
   Otherwise run `/bin/sh -c <supervisorScript> relay-supervisor <that
   element> /bin/echo hi` and assert the stream contains a
   `relay-rusage:cpu_usec=` line and ends with `relay-exit:0`.
4. Keep round 1's three `Rusage` tests exactly as they are.
5. Mutation-check before reporting: set `want` to `$1` but drop the
   `[ -n "$want" ]` guard, confirm `TestSupervisorEmitsRusageOnlyInScope`
   fails **when run inside your own scope**, then restore by re-editing
   (never `git checkout`, which would drop the round-1 fix too).

## 4. Why the gate must pass here specifically

You are running inside `relay-round-<...>.scope`. That is the environment
that exposes this bug, so `go test ./internal/proc/` passing **in this
worktree** is the proof the fix works; the five failures round 1 reported
must all be gone. If any of them still fails after this change, halt and
report its full output -- do not adjust the test to fit.

## 5. Gate

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/proc/ ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Then commit, in this order:
1. round 1's tail-scan fix plus this round's guard fix and all the tests, as
   one commit: `fix(proc): scan the stream tail for the rusage trailer, and
   emit it only for the round's own scope (#216)`.
2. the two plan copies (`docs/plans/2026-09-22-rusage-scan.md` from round 1,
   already in the tree, and this plan as
   `docs/plans/2026-09-22-rusage-guard.md`): `chore(plans): rusage scan and guard`.

## Report

The gate output's tail (all five previously-failing tests passing), the
mutation check both ways, commit shas, and anything you did that this plan
did not say.
