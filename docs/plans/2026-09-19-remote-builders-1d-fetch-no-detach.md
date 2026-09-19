# Remote builders, plan 1d: FetchBundle never leaves a background git process (#100, PR #206)

Round 4 on the same branch. CI job `check (ubuntu-latest, go 1.22)` on PR
#206 failed once with:

```
--- FAIL: TestFetchBundleFastForwards (0.08s)
    testing.go:1231: TempDir RemoveAll cleanup: unlinkat /tmp/TestFetchBundleFastForwards.../002/objects/pack: directory not empty
```

Cause: `git fetch` runs `gc --auto` when the object count crosses its
threshold, and `gc.autoDetach` defaults to true, so the gc forks into the
background and outlives the `fetch` relay waited on. In the test it was
still writing pack files when `t.TempDir` cleaned up; on a server it would
outlive `Absorb` and race an `unbind`'s worktree removal. The fix is in
`FetchBundle`: the fetch runs with `-c gc.autoDetach=false`, so any auto-gc
completes inside the bounded call.

**Halt rule for the builder.** If any step below is impossible as written
or contradicts the code you find, stop, write the report saying which step
and why, create the done marker, and do nothing else.

**Scope guard.** Touch only `internal/git/client.go` and copy this plan to
`docs/plans/2026-09-19-remote-builders-1d-fetch-no-detach.md`. No test
change is needed (the existing tests cover the path; the fix removes the
race rather than working around it). Foreground only, no sub-agents.

## 1. The change

`internal/git/client.go`, in `FetchBundle`, the one `c.run(...)` call that
invokes `fetch` (currently line 701):

```
before:  c.run(ctx, dir, nil, "fetch", "--no-tags", path, ref+":"+ref)
after:   c.run(ctx, dir, nil, "-c", "gc.autoDetach=false", "fetch", "--no-tags", path, ref+":"+ref)
```

`-c key=value` must come **before** the subcommand (`fetch`); git parses
it as a global option. Extend `FetchBundle`'s doc comment with one
sentence: "The fetch runs with `gc.autoDetach=false` so an auto-gc it
triggers finishes inside this call instead of forking a process that
outlives it (and would race a later worktree removal)."

## 2. Steps

**Step 1.** Copy this plan to `docs/plans/2026-09-19-remote-builders-1d-fetch-no-detach.md`.

**Step 2.** Make the §1 edit. Run `go test -race -count=20 ./internal/git
-run 'TestFetchBundle'` -- twenty iterations must pass; that is the
regression check for the race (before the change it is timing-dependent,
so a green run before the change proves nothing -- do not bother).

**Step 3.** `make check` green. One commit: `git: FetchBundle runs
auto-gc in the foreground (#100)` including the plan file. Report the
`git diff` of client.go verbatim. Create the done marker.

## Verification the planner runs

Diff touches only the two files; `go test -race -count=20 ./internal/git
-run TestFetchBundle` green locally; every CI cell on PR #206 passes.
