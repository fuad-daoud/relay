# Plan: a round's scope ends with its round (#378)

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit.

**Parallel branches:** #372 (`internal/store`, `planner`, `ledger`, `policy`, `candidate`, `db`, `cmd/relay/main.go`) and #373 (`internal/remote`, `internal/relay/remote.go`, `runtime.go`, `cmd/relay/main.go`). This round touches only `internal/proc`. Don't edit anything outside it.

## 1. System overview

Found on contabo-01 on 2026-09-23. `relay-round-8ef156ea-h-locks-1.scope` stayed `active running` 12½ hours after its builder had gone. The only processes left in it were two `git fsmonitor--daemon run --detach`, started by the builder's git commands and reparented to the user manager.

Every spawn relay scopes gets the same `supervisorScript` (`internal/proc/proc.go:65-78`): builders, gates, verify and consults. Anything the harness leaves running after it exits keeps its scope alive indefinitely. That leaks memory and inotify watches, leaves a transient unit that never collects, and inflates anything that counts `relay-round-*.scope`, such as the servers repo's deploy note.

**The fix, in the supervisor.** When it runs inside its own scope (the existing `want` guard, #216), then after the harness has exited and the rusage line is printed, it terminates every other process still in that scope's cgroup: SIGTERM, a short grace, then SIGKILL. Only then does it print the exit trailer and exit, so the scope empties and `--collect` removes it.

**Belt and braces.** Every spawn also runs with git's fsmonitor disabled through `GIT_CONFIG_*` environment entries, so a builder doesn't start the daemon in the first place.

## 2. File structure

```
internal/proc/proc.go        supervisorScript: reap the scope before the exit trailer, only inside `want`
internal/proc/gitenv.go      NEW  gitNoFsmonitorEnv(parent, extra []string) []string (pure)
internal/proc/gitenv_test.go NEW
internal/proc/proc_test.go   tests for the script's reap fragment; env wiring test
```

## 3. Data structures

None. `supervisorScript` stays one POSIX `sh` string. Add a second exported const **`ReapFragment`**, the shell function below, which `supervisorScript` embeds by string concatenation. The test can then source exactly the text production runs.

## 4. Interfaces and contracts

- **The shell function, in `ReapFragment`:** `relay_reap_scope <procs_file> <self_pid>`.
  - It reads `<procs_file>` with a builtin `while read` loop (no `cat`: a subprocess would itself be in the cgroup) and sends `kill -TERM` to every pid other than `<self_pid>`.
  - Then it polls up to 20 times, 0.1 s apart. It reads the file again each time and stops early when no pid other than self remains. The sleep may be `sleep 0.1`; a sleep child exits before the next read.
  - Then it sends `kill -KILL` to any pid still listed.
  - It always returns 0, and prints nothing on stdout. Errors go to `/dev/null`.
- **The supervisor order,** inside the existing `case "$cg" in */"$want")` branch only: (1) the rusage line (unchanged), (2) `relay_reap_scope "/sys/fs/cgroup$cg/cgroup.procs" "$$"`. After the `fi`, the exit trailer is printed exactly as today, and it stays the stream's **last line**. A plain spawn (empty `want`) or a mismatched cgroup never reaps.
- **`func gitNoFsmonitorEnv(parent, extra []string) []string`** returns the entries to append.
  - Let `n` be the value of `GIT_CONFIG_COUNT` in `extra` if set there, else in `parent`, else 0. A non-numeric value is treated as 0 and replaced.
  - Return `GIT_CONFIG_KEY_<n>=core.fsmonitor`, `GIT_CONFIG_VALUE_<n>=false` and `GIT_CONFIG_COUNT=<n+1>`.
  - The caller must make the new `GIT_CONFIG_COUNT` win over the parent's. Use `ChildEnv`'s deny list for `GIT_CONFIG_COUNT`, the same pattern Start uses for `GOMAXPROCS`.
- **`Start` wiring:** apply this to every spawn, next to where `goMaxProcsEnv` is applied, without mutating the caller's `spec.Env`. Use the same full-slice-expression copy discipline the code already documents.

## 5. High-level pseudocode

```
supervisorScript =
  ReapFragment +
  want=$1; shift; oom_score_adj (unchanged)
  "$@" </dev/null; rc=$?
  if [ -n "$want" ]; then
    cg=...
    case "$cg" in */"$want")
      rusage line (unchanged)
      relay_reap_scope "/sys/fs/cgroup$cg/cgroup.procs" "$$"
      ;;
    esac
  fi
  printf '\nrelay-exit:%s\n' "$rc"
```

## 6. Error handling strategy

The reap can't fail the supervisor: every command's errors are discarded, and the function returns 0. The worst case is a straggler that survives SIGKILL (an uninterruptible sleep), which leaves the scope as it is today. The exit trailer is written regardless, so `ExitCode` is unaffected.

## 7. Ordered implementation steps

1. **`ReapFragment` and the supervisor change.** Tests in `proc_test.go` (unix), **never** pointing the fragment at a real cgroup file. Pointing it at the test process's own cgroup would kill `go test`. Use a fake procs file:
   - start two `sleep 60` processes with `exec.Command` (A and B). Write a temp file listing A's pid and a made-up self pid. Run `sh -c "<ReapFragment>; relay_reap_scope <file> <selfpid>"`. Then: A has exited (`cmd.Wait` returns, killed by a signal), and B is still running (kill B in cleanup);
   - a process that ignores TERM (`sh -c 'trap "" TERM; sleep 60'`) listed in the file is gone after the call, via KILL. Assert the call took under about 3 s;
   - a missing procs file → exit 0 and no output;
   - `supervisorScript` still ends with the exit-trailer `printf`, and its reap call sits inside the `*/"$want"` branch. Assert with string checks on the const.

   Mutations:
   - drop the TERM loop → the first test fails, or passes only through KILL (then assert on timing). Pick a check that fails and say which;
   - move the reap call outside the `case` → the string-position test fails.
2. **`gitNoFsmonitorEnv`**, with a table test: no count; count 2 in the parent; count 1 in extra, overriding a parent 3; a non-numeric count.
   - Start wiring test, using the existing stub pattern in `proc_test.go` (e.g. `TestStartParentGoMaxProcsPassesThroughWithoutLimits`): a spawned `env` shows `GIT_CONFIG_KEY_n=core.fsmonitor`, `GIT_CONFIG_VALUE_n=false` and exactly one `GIT_CONFIG_COUNT`.
   - Run a real `git -C <tmp repo> config --get core.fsmonitor` under that env: it prints `false`. git is available in CI.
3. **Local acceptance, on this machine only:** it has a systemd user manager. **Never touch `relay.service` or any `relay-*.scope` you didn't create.**
   - Use `proc.New().Start` from a small `go run` program, or a test behind a build tag that CI doesn't run (e.g. `//go:build scopeaccept`, not run by `make check`).
   - Spawn, in a scope with a unique unit name (`relay-accept-<random>`), a harness script that starts `setsid sleep 300 &` and exits 0.
   - Within 5 s: the stream ends with `relay-exit:0`, `systemctl --user is-active relay-accept-<random>.scope` is not `active`, and the sleep pid is gone.
   - Paste the observed output into the report. If you add the tagged test file, it's in scope. Otherwise add nothing.
4. **Gate:** `make check` and `make e2e`. Commit ending `(#378)`.

Declared scope: §2's files (plus an optional `internal/proc/scope_accept_test.go` with a non-default build tag).
