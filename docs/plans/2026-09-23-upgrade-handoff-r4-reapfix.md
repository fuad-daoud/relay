# Plan: upgrade handoff, round 4: the macOS reaper counts its own `ps` (#371, PR #376)

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit.

The branch was rebased onto main by the planner (after #375 merged). Run `git status` and `git log --oneline -6` first. HEAD should be `1c17212 docs(upgrade): …`. If it isn't, halt.

## 1. What failed

PR #376's CI fails on both macOS jobs:

```
--- FAIL: TestInheritedReaperLeavesNewChildrenAlone (0.23s)
    reap_test.go:47: Reap() = [5417], want nothing for a child started after construction
FAIL github.com/fuad-daoud/relay/internal/proc
```

Linux passes because `NewInheritedReaper` reads `/proc` there. On darwin there is no `/proc`, so `scanPS` runs `ps -A -o pid=,ppid=` as a **child of the calling process**. `ps` lists itself with ppid == self, so its own pid lands in the inherited set.

`Reap()` then reports it, because `reap()` returns true on ECHILD ("not ours any more") and `Reap` appends every true to `reaped`. Beyond the test, this is a real hazard: the ps pid sits in the set, and if the kernel later reuses it for a new child of the daemon, `Wait4` would steal that child's status from its `exec.Cmd`.

## 2. Files

```
internal/proc/reap.go        scanPS excludes its own ps pid; reap distinguishes reaped from dropped
internal/proc/procstat.go    (only if ParsePSChildren lives here) -- no signature change needed
internal/proc/reap_test.go   tests below
```

## 3. Contracts

- `scanPS(self int) []int` starts `ps` with `exec.Command`, keeps `cmd.Process.Pid` as `psPID`, collects the output, and returns `ParsePSChildren(out, self)` **minus `psPID`**. Put the removal in a pure helper, `withoutPID(pids []int, pid int) []int`, with a unit test.
- `reap(pid int) (reaped, drop bool)`:
  - `wpid == pid` → `(true, true)`;
  - ECHILD or another error → `(false, true)`;
  - `wpid == 0`, still running → `(false, false)`.
- `Reap()` returns only the pids with `reaped == true`, and drops every pid with `drop == true`. That matches the spec §4.6 wording, "returning the reaped pids".

## 4. Steps

1. Implement §3.
2. Tests (unix):
   - `withoutPID` table test;
   - a `Reap` test where the set holds a pid that isn't our child (use `os.Getpid()`'s parent, `os.Getppid()`, which is never our child): `Reap()` returns nothing, and a second `Reap()` also returns nothing because the pid was dropped;
   - keep both existing reaper tests unchanged.
   - Also run the darwin path on Linux: add a test that calls `scanChildren(os.Getpid(), "")` (empty `procRoot` forces `scanPS`) with no other children running, and asserts the result is empty. Without the fix it contains ps's own pid. Confirm this test **fails** with the exclusion removed, record that as your mutation check, then restore.
3. Gate: `make check`, `make e2e`, and `GOOS=darwin CGO_ENABLED=0 go build ./...`. Commit ending `(#371)`. **Don't push**: the planner pushes after verifying.

Declared scope: `internal/proc/reap.go`, `internal/proc/reap_test.go` (and `procstat.go` only if the helper lives there).
