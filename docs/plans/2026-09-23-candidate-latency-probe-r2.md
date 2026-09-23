# Plan, round 2: `relay candidates --probe` (#324 part 1): fix the probe tier, then finish

If any step is impossible as written or contradicts the code you find, STOP
and report. Do not bend a test or the design to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so:
- Read the "Read first" list in **one** step (parallel reads).
- Make every change to a file in one edit call. Write each new file in one
  write call.
- Loop with `go build ./... 2>&1 | head -80` and focused `go test` runs,
  fixing every reported error before the next run. Run `make check` once at
  the end.

## Where the tree is

Round 1 halted with **uncommitted** work in this worktree. It is correct and
stays:
- `internal/latency/latency.go` and `latency_test.go` (step 1, passing);
- `internal/transcript/first.go` and `first_test.go` (step 2, passing);
- `internal/store/store.go` `LatencyPath()`, `internal/relay/runtime.go`
  `Runtime.LatencyPath`, and the wiring in `cmd/relay/main.go`;
- `internal/relay/probe.go` and `probe_test.go` (step 3, written, blocked).

Start with `git status` and confirm those nine paths are the only changes.
If they aren't, STOP and report.

Round 1 halted because `probe.go` calls
`headlessLaunch(c, role, harness.TierRead, ...)`, and
`harness.PermissionArgs` refuses `TierRead` for `opencode` (it has no
read-only flag) and for `codex` (read-only can't write). That was a design
error in the plan. The fix follows.

Read first:
- `internal/relay/probe.go` and `internal/relay/probe_test.go` (whole files)
- `internal/harness/tier.go` lines 1-120 (`Tier`, `PermissionArgs`)
- `internal/harness/harness.go`, just `Lookup` and the `Harness` type
- `internal/relay/candidates_list.go` (whole file)
- `cmd/relay/main.go` lines 670-700 (`cmdCandidates`) and the `Commands:`
  help block (~line 84, the `candidates` line)
- `internal/proc/env.go`

## Design change: the probe's tier

New pure function in `internal/relay/probe.go`:

```
// probeTier is the most restrictive tier kind's harness can honour, for a
// probe that must not change anything: read, else edit, else harness (the
// harness's own config decides). Never yolo.
func probeTier(kind string) (harness.Tier, error)
```

- Contract: look the kind up with `harness.Lookup`; an unknown kind is an
  error. For `t` in `[TierRead, TierEdit, TierHarness]`, return the first
  `t` for which `h.PermissionArgs(t)` returns a nil error.
  `TierHarness` always succeeds, so there is no error path after the
  lookup.
- The expected results are claude → read, agy → read, codex → edit,
  opencode → harness.
- `ProbeCandidate` calls `probeTier(c.Harness)` in place of the literal
  `harness.TierRead`. An error goes into `Err` and nothing runs, like the
  existing `headlessLaunch` error path.

Everything else in `ProbeCandidate` and `Probe` stays exactly as round 1
wrote it: temp dir, prompt, 60 s budget, 90 s timeout, first-output latch,
`Err` rules, sequential probing, and recording under the store lock.

## Steps

### Step 1: `probeTier` and its use

- Deliverable: `probeTier` and the one-line change in `ProbeCandidate`.
- Test `TestProbeTierPerKind` in `probe_test.go`, a table:
  - claude → `TierRead`;
  - agy → `TierRead`;
  - codex → `TierEdit`;
  - opencode → `TierHarness`;
  - `"nope"` → an error.
- In `TestProbeCandidateMeasuresFirstOutput` (opencode), add an assertion
  that the recorded argv does **not** contain `--auto`, which is the yolo
  flag for opencode.
- Verify: `go test ./internal/relay/ -run 'TestProbe|TestFormatProbe' -count=1`.
  All of round 1's step 3 tests now pass unchanged apart from that added
  assertion. If any other round 1 test fails, fix the **code** to match the
  test, not the test. If the test itself contradicts round 1's plan
  (quoted below), STOP and report.
- Mutation (report it): in `ProbeCandidate`, set TTFT on **every** matching
  line rather than the first. `TestProbeCandidateMeasuresFirstOutput` must
  fail, because its script has a second `text` line at +800ms. Restore.

### Step 2: listing shows p50; CLI (round 1's step 4, unchanged)

- `relay.FormatCandidatesLatency(set *candidate.Set, gates []ledger.Gate, lat map[string]latency.Summary) string`:
  - It is today's `FormatCandidates` body. For each candidate whose token
    has `lat[token].N > 0`, it adds the suffix
    `   ttft p50 <probeMS> (n=<N>, 30d)` after the existing
    tier/extra-args part and before any `unavailable:` note.
  - `FormatCandidates(set, gates)` becomes a one-line call of
    `FormatCandidatesLatency(set, gates, nil)`, so all existing call sites
    and tests are unchanged.
- `cmdCandidates`:
  - Flag `--probe` (bool), usage `"run each candidate once with a one-line prompt from this machine and record its time to first output"`.
  - Positional tokens are allowed only with `--probe`. Otherwise print the
    usage error `usage: relay candidates [--probe [token...]]`.
  - Without `--probe`: `latency.Load(rt.LatencyPath)` (an error prints one
    stderr line and uses an empty history), then `Prune(rt.Now())`, then
    `Summary` for every ref in `rt.Candidates.Refs()`, then print
    `FormatCandidatesLatency`.
  - With `--probe`: `host, _ := os.Hostname()`, then print to stderr
    `probing <n> candidate(s) from <host>, one at a time`. Here n is the
    number of tokens given, or `rt.Candidates.Len()`. Then call
    `relay.Probe(ctx, rt, lineExec{}, fs.Args(), host, func(r relay.ProbeResult){ fmt.Println(relay.FormatProbe(r, width)) })`,
    where width is the longest token among those probed. Exit 0 even when
    probes failed. Return only `Probe`'s own error.
  - Help line: `candidates   list the configured harness/provider/model candidates [--probe]`.
- `cmd/relay/probe_exec.go`: `type lineExec struct{}` implementing
  `relay.LineExec`:
  - `exec.CommandContext(ctx, argv[0], argv[1:]...)`, with `cmd.Dir = dir`
    and `cmd.Env = proc.ChildEnv(os.Environ(), proc.DeniedEnv, nil)`;
  - stdout through `bufio.Scanner` with a 4 MiB buffer, calling `onLine`
    per line;
  - stderr into a buffer, keeping its last 300 bytes;
  - a non-zero exit returns `fmt.Errorf("%w: %s", err, lastStderr)`.
  Keep it under 50 lines and give it no tests (it spawns processes, and CI
  has no harness).
- Test `TestFormatCandidatesLatencySuffix`, in the file holding the
  `FormatCandidates` tests (`grep -ln "FormatCandidates(" internal/relay/*_test.go`):
  a two-candidate set where one has `Summary{N: 3, TTFTP50MS: 640}` gives
  `ttft p50 640ms (n=3, 30d)` on that line only, and the other line equals
  `FormatCandidates`' output for it. All existing `FormatCandidates` tests
  pass unmodified.
- **Do not add a `cmd/relay` test that runs `relay candidates --probe`**: it
  spawns a harness, and CI runners have no harness binary and no network.
- Verify: `go build ./cmd/relay && ./relay candidates --help 2>&1 | grep probe`
  prints the flag. Delete the binary afterwards.

### Step 3: full check and commit

- `make check` passes.
- `git diff --stat` shows exactly these paths:
  - `cmd/relay/main.go`, `cmd/relay/probe_exec.go`;
  - `internal/latency/latency.go`, `latency_test.go`;
  - `internal/transcript/first.go`, `first_test.go`;
  - `internal/relay/probe.go`, `probe_test.go`, `candidates_list.go`,
    `runtime.go`, and the FormatCandidates test file;
  - `internal/store/store.go`.
- Report: each verify output, the mutation result, and the diff stat.
  Commit everything (round 1's work and this round's) on the binding's
  branch as one commit, with the message
  `feat(candidates): relay candidates --probe records time to first output per candidate (#324)`.

## Round 1's contracts, for reference (unchanged)

- `ProbeCandidate(ctx, rt, x LineExec, c candidate.Candidate, host string) ProbeResult`:
  - The role is the first of `c.Roles` known to `harness.RoleByName`,
    otherwise `Err "no known role"` and nothing runs.
  - It runs in a temp dir that is removed afterwards.
  - Argv: `headlessLaunch(c, role, probeTier(c.Harness), 60s, "relay latency probe: reply with the single word ok. Do not use any tools.", dir, dir)`.
  - `x.Run` runs with a 90 s timeout. TTFTMS comes from the first line for
    which `transcript.FirstOutput(c.Harness, line)` is true; TotalMS is
    measured at Run's return.
  - `Err`: the Run error's text (300-byte cap) if Run failed, otherwise
    `"no model output"` when no first output was seen.
- `Probe(ctx, rt, x, tokens, host, each) ([]ProbeResult, error)`:
  - An unknown token errors before anything runs; with no tokens, it probes
    all candidates in `Refs()` order.
  - Probes run sequentially. After each one it calls `each`, then records to
    `rt.LatencyPath` under `rt.Store.WithLock` (load, prune, append, save);
    a record error is one stderr line.
- `FormatProbe(r, width)`:
  - success: `%-*s  ttft %s  total %s`;
  - failure: `%-*s  error: <Err>`, plus `  (ttft %s)` when TTFTMS > 0.
  - `probeMS` formats `"%dms"` below 1000, else `"%.1fs"`.
