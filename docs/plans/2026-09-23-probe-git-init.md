# Plan: the probe runs in a git repo, so codex can be probed

If any step is impossible as written or contradicts the code you find, STOP
and report. Do not bend a test to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so read the two files
below in one step, and make each file's change in one edit call. Loop with
`go test ./internal/relay/ -run 'TestProbe|TestFormatProbe' -count=1`. Run
`make check` once at the end.

Read first: `internal/relay/probe.go` lines 60-140 (`ProbeCandidate`), and
`internal/relay/probe_test.go` lines 20-160 (`fakeExec`, `probeRuntime`,
`TestProbeCandidateMeasuresFirstOutput`).

## 1. System Overview

`relay candidates --probe` (#343) runs each candidate in a fresh
`os.MkdirTemp` directory. The first real probes on the laptop and on
contabo got this for the codex candidate on both machines:

```
codex/openai/gpt-5.6-terra:high  error: exit status 1: Reading additional input from stdin...
Not inside a trusted directory and --skip-git-repo-check was not specified.
```

codex refuses to run outside a git repository. A real round always runs in
a git worktree, so the fix is to make the probe directory a git repo too
(`git init -q`), not to add a codex-only flag. That keeps the probe's argv
identical to a real round's, which is the point of #324.

## 2. File Structure

```
internal/relay/probe.go       MODIFY  ProbeCandidate runs `git init -q` in the temp dir, through x, before the harness
internal/relay/probe_test.go  MODIFY  fakeExec passes git calls through; two new assertions/tests
```

No other file changes.

## 3. Data Structures & Type Definitions

None.

## 4. Interface Definitions & Component Contracts

### `ProbeCandidate` (changed)

- After `os.MkdirTemp` and `probeTier`, and after `headlessLaunch` has
  succeeded, but **before** `start := rt.Now()`, it calls
  `x.Run(pctx, dir, []string{"git", "init", "-q"}, nil)`.
  - Use the same `pctx` (the 90 s timeout context). Create `pctx` before
    this call rather than after it.
  - Keep the git call outside the timed window, so neither TTFTMS nor
    TotalMS includes it.
- If the git call errors, return a result with `At: rt.Now().UTC()`,
  `Token`, `Host` and `Err: probeErrText("git init: " + err.Error())`, and
  do not run the harness. relay needs git in any case.
- Everything else is unchanged.
- Update the doc comment to say the probe runs in a fresh git repository
  (`git init`), like a real round's worktree, because codex refuses to run
  outside one.

### `fakeExec.Run` (test helper, changed)

When `len(argv) > 0 && argv[0] == "git"`:
- append argv to a new field `gitCalls [][]string` and the dir to `dirs`;
- do **not** consume a script, increment `calls`, append to `argvs`, or
  move the clock;
- return the new field `gitErr error` (nil by default).

Every existing test's `fake.argvs[0]` / `scripts[0]` then still refers to
the harness call, so existing tests pass unchanged.

## 5. High-Level Pseudocode

```
ProbeCandidate:
    role, dir (MkdirTemp, deferred RemoveAll), tier, argv  -- as today
    pctx, cancel = WithTimeout(ctx, probeTimeout); defer cancel()
    if err = x.Run(pctx, dir, ["git","init","-q"], nil): return Err "git init: ..."
    start = rt.Now()
    runErr = x.Run(pctx, dir, argv, onLine)      -- as today
    ... result as today
```

## 6. Error Handling Strategy

A git init failure is a probe result with `Err`, not a command error, in
the same way as every other per-candidate failure.

## 7. Ordered Implementation Steps

### Step 1: `git init` before the harness

- Deliverable: §4 in `probe.go`, and the `fakeExec` change in
  `probe_test.go`.
- Tests:
  - In `TestProbeCandidateMeasuresFirstOutput`, assert that
    `fake.gitCalls` has exactly one entry equal to `["git","init","-q"]`,
    and that it ran in the same dir as the harness call
    (`fake.dirs[0] == fake.dirs[1]`). The existing TTFTMS=600 and
    TotalMS=900 assertions must stay unchanged, which proves the git call
    is outside the timed window.
  - New `TestProbeCandidateGitInitFailureRunsNoHarness`: with
    `fake.gitErr = errors.New("no git")`, the result's Err starts with
    `git init: ` and contains `no git`, `fake.calls == 0` (the harness
    never ran), and TTFTMS is 0.
- Verify: `go test ./internal/relay/ -run 'TestProbe|TestFormatProbe' -count=1`
  passes, with every pre-existing probe test unmodified apart from the
  added assertions above.
- Mutation (report it): delete the git call from `ProbeCandidate`. The
  added assertion in `TestProbeCandidateMeasuresFirstOutput` fails. Restore.

### Step 2: full check

- `make check` passes, and `git diff --stat` shows exactly the two files.
- Report: each verify output, the mutation result, and the diff stat.
  Commit on the binding's branch with the message
  `fix(candidates): the probe runs in a git repo, so codex can be probed`.
- Do **not** run a real `relay candidates --probe`. The planner does that
  after the merge.
