# Plan: #378 round 2: a timing bound too tight for macOS CI

PR #381 failed both macOS jobs:

```
--- FAIL: TestReapScopeKillsWhatIgnoresTERM (3.11s)
    proc_test.go:1159: reap took 3.10797175s; want under about 3s (20 polls 0.1s apart)
```

The test's purpose is to show that the KILL fallback ends a TERM-ignoring process, rather than the call waiting on its `sleep 60`. On the macOS runner under `-race`, forking a `sleep 0.1` per poll makes 20 polls take about 3 s. The bound was too tight. The behaviour is correct.

The branch was rebased by the planner. Run `git status` and `git log --oneline -3` first. HEAD should be `eaf3312 docs(plans): the plan behind #378 (#378)`. If it isn't, halt.

**Stop rather than improvise.** Test-only change. If anything else seems to need changing, halt and report.

## Steps

1. In `internal/proc/proc_test.go` (~1159), change the bound in `TestReapScopeKillsWhatIgnoresTERM` from about 3 s to **15 s**. The target sleeps 60 s, so 15 s still proves the KILL fallback ran. Update the failure message and add a one-line comment giving this reason, citing macOS CI under `-race`. Change no other test.
2. `go test -race -count=3 -run 'ReapScope' ./internal/proc/`, then `make check`. Commit ending `(#378)`. Don't push; the planner pushes.

Declared scope: `internal/proc/proc_test.go`, that one assertion only.
