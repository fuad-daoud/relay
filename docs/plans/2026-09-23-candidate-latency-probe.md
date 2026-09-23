# Plan: `relay candidates --probe` measures time to first output per candidate (#324 part 1)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so:
- Read everything in "Read first" in **one** step (parallel reads). Don't grep
  for what this plan already locates.
- Write each new file in one write call. Make every change to an existing
  file in one edit call.
- Loop with `go build ./... 2>&1 | head -80` and focused
  `go test ./internal/<pkg>/ -run '<names>'`, fixing every reported error
  before the next run. Run `make check` once at the end.

Read first:
- `internal/history/history.go` whole file: the model for the new
  `internal/latency` package (30-day window, atomic save, pure Prune/Append)
- `internal/relay/headless.go` lines 84-105 (`headlessLaunch`, pure argv)
- `internal/harness/harness.go` lines 20-70 (`RoleSpec`, `RoleByName`) and
  `internal/harness/tier.go` lines 1-20 (`TierRead`)
- `internal/candidate/candidate.go` lines 20-140 (`Ref`, `Candidate`, `Set`)
- `internal/relay/candidates_list.go` whole file (`FormatCandidates`)
- `internal/relay/runtime.go` lines 100-115 (`AvailabilityPath`)
- `internal/store/store.go` lines 585-600 (`AvailabilityPath()`)
- `cmd/relay/main.go` lines 505-525 (runtime wiring) and 670-700 (`cmdCandidates`)
- `internal/proc/env.go` (`DeniedEnv`, `ChildEnv`)
- `internal/transcript/opencode.go`, `claude.go`, `agy.go`, `codex.go`: the
  top of each, for the event names

## 1. System Overview

#324 asks for a measurement before any region or provider move: from the
machine it runs on, how long does each candidate take to produce its first
output for a trivial request? This adds `relay candidates --probe
[token...]`. It runs each candidate's harness once, headless, with a
one-line prompt, in a temp directory at the read tier. It records time to
first model output (TTFT) and total time, prints them, and appends them to a
30-day history at `$XDG_STATE_HOME/relay/latency.json`. Plain
`relay candidates` then shows each candidate's p50 TTFT over that window.

The server reads the same `candidates.json` (through `userConfigRoot()`),
so `relay candidates --probe` run on the server box, as the user
`relay serve` runs as, probes exactly the server's candidates. No wire or
server change is needed.

The rule parts are pure and tested in `internal/relay`, `internal/latency`
and `internal/transcript`. The process-spawning part is a small seam
implemented in `cmd/relay`. **No test anywhere may spawn a harness or reach
the network.** CI runners have neither.

Out of scope: a doctor row, preferring low-latency candidates in the policy
order, and per-step TTFT from real rounds (a separate plan).

## 2. File Structure

```
internal/latency/latency.go          CREATE  Sample, History, Load/Save/Prune/Append, Summary
internal/latency/latency_test.go     CREATE
internal/transcript/first.go         CREATE  FirstOutput(kind, line) bool
internal/transcript/first_test.go    CREATE
internal/relay/probe.go              CREATE  LineExec seam, ProbeResult, ProbeCandidate, Probe, FormatProbe
internal/relay/probe_test.go         CREATE
internal/relay/candidates_list.go    MODIFY  FormatCandidatesLatency; FormatCandidates delegates with nil
internal/relay/candidates_list_test.go MODIFY (or the file holding FormatCandidates tests) latency suffix test
internal/relay/runtime.go            MODIFY  Runtime.LatencyPath
internal/store/store.go              MODIFY  LatencyPath()
cmd/relay/main.go                    MODIFY  wiring LatencyPath; cmdCandidates --probe; help line
cmd/relay/probe_exec.go              CREATE  lineExec: the real LineExec (os/exec, stdout scanned by line)
```

## 3. Data Structures & Type Definitions

### `latency.Sample`

| Field | Type | JSON | Meaning |
|-------|------|------|---------|
| `At` | time.Time | `at` | when the probe started (UTC) |
| `Token` | string | `token` | the candidate ref, `candidate.Ref.String()` |
| `Host` | string | `host` | `os.Hostname()` of the probing machine; "" if unknown |
| `TTFTMS` | int64 | `ttft_ms` | ms from spawn to the first model-output line; 0 when none was seen |
| `TotalMS` | int64 | `total_ms` | ms from spawn to process exit |
| `Err` | string | `err,omitempty` | why the probe did not yield a TTFT; "" on success |

### `latency.History`

`{ Samples []Sample \`json:"samples"\` }`, plus `const RetainWindow = 30 * 24 * time.Hour`.

### `latency.Summary`

| Field | Type | Meaning |
|-------|------|---------|
| `N` | int | successful samples (Err == "") for the token in the window |
| `Errors` | int | failed samples for the token in the window |
| `TTFTP50MS` | int64 | lower median of the successful samples' TTFTMS; 0 when N == 0 |

### `relay.ProbeResult`

The same fields as `latency.Sample`. Define it as `type ProbeResult = latency.Sample`
so there is only one shape.

### `relay.LineExec` (seam)

```
type LineExec interface {
    // Run starts argv in dir with the parent environment minus proc.DeniedEnv,
    // calls onLine for every stdout line as it arrives (without the newline),
    // and returns when the process exits or ctx is done. A non-zero exit is an
    // error whose text ends with the last 300 bytes of stderr.
    Run(ctx context.Context, dir string, argv []string, onLine func(line []byte)) error
}
```

`Runtime` does **not** gain this. It is passed explicitly to `Probe`.

## 4. Interface Definitions & Component Contracts

### `internal/latency`

- `Load(path string) (History, error)`: a missing file gives an empty
  History and nil. Invalid JSON is an error.
- `Save(path string, h History) error`: MkdirAll the parent, marshal with
  indent, write `path+".tmp"`, then rename. This is the same as
  `history.Save`.
- `(h History) Prune(now time.Time) History`: a new History without samples
  older than `now - RetainWindow`. Pure; it does not mutate h.
- `(h History) Append(s Sample) History`: pure.
- `(h History) Summary(token string) Summary`: over all samples in h with
  `Token == token` (the caller prunes first).

### `transcript.FirstOutput(kind string, line []byte) bool`

Pure. It decodes one JSON line and returns true when the line is the first
kind of event that means the model produced output:
- `claude`: `type == "assistant"`.
- `opencode`: `type` is one of `text`, `reasoning`, `tool_use`.
- `agy`: `event == "step_update"` with `step_update.step_type == "agent_response"`,
  or `event == "result"`.
- `codex`: `type` is `item.started` or `item.completed`, with `item.type`
  one of `agent_message`, `reasoning`, `command_execution`, `file_change`.
- A non-JSON line, or an unknown kind, returns false.

### `relay.ProbeCandidate(ctx context.Context, rt Runtime, x LineExec, c candidate.Candidate, host string) ProbeResult`

- Role: the first entry of `c.Roles` that `harness.RoleByName` knows. If
  there is none, return a result with `Err: "no known role"` and do not run
  anything.
- Dir: `os.MkdirTemp("", "relay-probe-")`, removed afterwards (defer).
  State dir: the same dir.
- Argv: `headlessLaunch(c, role, harness.TierRead, probeBudget, probePrompt, dir, dir)`.
  An error is returned in `Err` and nothing runs.
  - `probeBudget = 60 * time.Second`
  - `probePrompt = "relay latency probe: reply with the single word ok. Do not use any tools."`
- Runs `x.Run(ctx', dir, argv, onLine)` with
  `ctx' = context.WithTimeout(ctx, 90*time.Second)`.
  - `start = rt.Now()` just before Run.
  - In `onLine`: the first time `transcript.FirstOutput(c.Harness, line)` is
    true, set `TTFTMS = rt.Now().Sub(start).Milliseconds()`.
  - After Run: `TotalMS = rt.Now().Sub(start).Milliseconds()`.
- `Err`: the Run error's text, truncated to 300 bytes. If Run returned nil
  but no first output was seen, `Err` is `"no model output"`. If Run
  returned an error but a TTFT was seen, TTFTMS is kept **and** Err is set.
  A sample counts as successful only when Err is "".
- `At = start.UTC()`, `Token = c.Ref().String()`, `Host = host`.

### `relay.Probe(ctx context.Context, rt Runtime, x LineExec, tokens []string, host string, each func(ProbeResult)) ([]ProbeResult, error)`

- Candidates: when `tokens` is empty, every candidate in `rt.Candidates`,
  in `Refs()` order. Otherwise each token parsed with `candidate.ParseRef`
  and looked up. An unknown or unparsable token returns an error **before**
  anything runs.
- A nil or empty candidate set gives `nil, nil`.
- Probes run **sequentially**, one at a time, so they don't contend with
  each other.
- After each probe: call `each(result)` if it is non-nil, so the CLI can
  print as it goes. Then, under `rt.Store.WithLock`, load the latency
  history from `rt.LatencyPath`, then `Prune(rt.Now())`, `Append(result)`,
  `Save`. A history error is written to stderr as one line
  (`relay: could not record latency: <err>`) and does not stop the probe.
  If `rt.LatencyPath` is "", skip the recording.
- Returns every result.

### `relay.FormatProbe(r ProbeResult, width int) string`

One line, no trailing newline:
- success: `%-*s  ttft %s  total %s`, using `msText`-style formatting:
  `"%dms"` below 1000, else `"%.1fs"`;
- failure: `%-*s  error: <Err>`, with the TTFT appended as `  (ttft %s)`
  when TTFTMS > 0.

Put the ms formatter in `probe.go` as the unexported `probeMS`. Do not
import it from elsewhere.

### `relay.FormatCandidatesLatency(set *candidate.Set, gates []ledger.Gate, lat map[string]latency.Summary) string`

- It is today's `FormatCandidates` body. For each candidate whose token has
  `lat[token].N > 0`, it adds the suffix `   ttft p50 <probeMS> (n=<N>, 30d)`
  after the existing tier/extra-args part and before any `unavailable:`
  note.
- `FormatCandidates(set, gates)` becomes a one-line call of
  `FormatCandidatesLatency(set, gates, nil)`, so every existing call site
  and test is unchanged.

### `cmdCandidates` (changed)

- Flag `--probe` (bool): `"run each candidate once with a one-line prompt from this machine and record its time to first output"`.
- Positional tokens are allowed only with `--probe`. Without `--probe`, any
  positional argument is the usage error
  `usage: relay candidates [--probe [token...]]`.
- Without `--probe`: load `latency.Load(rt.LatencyPath)` (an error means
  one stderr line and an empty history), prune it, build the map for every
  ref in the set, and print `FormatCandidatesLatency(...)`.
- With `--probe`: `host, _ := os.Hostname()`, and print to stderr
  `probing <n> candidate(s) from <host>, one at a time`. Then call
  `relay.Probe(ctx, rt, lineExec{}, fs.Args(), host, func(r){ fmt.Println(relay.FormatProbe(r, width)) })`,
  where width is the longest token. The exit status is 0 even when some
  probes failed, and non-zero only for an error returned by `Probe`.
- Help line (the `Commands:` block, the `candidates` entry):
  `candidates   list the configured harness/provider/model candidates [--probe]`.

### `lineExec` (`cmd/relay/probe_exec.go`)

- Implements `relay.LineExec` with `exec.CommandContext`:
  - `cmd.Dir = dir`;
  - `cmd.Env = proc.ChildEnv(os.Environ(), proc.DeniedEnv, nil)`;
  - stdout through a `bufio.Scanner` with a 4 MiB max token, calling
    `onLine` per line;
  - stderr into a bounded buffer that keeps the last 300 bytes.
- It has no tests of its own: it spawns processes, and CI has no harness.
  Keep it under 50 lines.

## 5. High-Level Pseudocode

```
relay candidates --probe [tokens]:
    rt = newRuntime(); host = hostname()
    results, err = Probe(ctx, rt, lineExec{}, tokens, host, printEach)
    if err: return err

Probe:
    cands = resolve(tokens) or error
    for c in cands:
        r = ProbeCandidate(ctx, rt, x, c, host)
        each(r)
        record(r)   # under the store lock: load, prune, append, save latency.json
    return results

ProbeCandidate:
    role = firstKnownRole(c) or return Err "no known role"
    dir = tempdir; argv = headlessLaunch(c, role, TierRead, 60s, prompt, dir, dir)
    start = now
    err = x.Run(ctx with 90s timeout, dir, argv, onLine: first FirstOutput -> ttft)
    total = now - start
    set Err per §4
```

## 6. Error Handling Strategy

- An unknown token is a user error, returned before any spawn.
- A probe failure (the harness is missing, a spawn error, a non-zero exit, a
  timeout, no output) is data. It is recorded with `Err`, printed, and does
  not fail the command.
- A latency history read or write failure is one stderr line. It never
  fails a probe or a listing.

## 7. Ordered Implementation Steps

### Step 1: `internal/latency`

- Deliverable: the package per §3/§4.
- Tests (`latency_test.go`):
  - `TestLoadMissingIsEmpty`;
  - `TestSaveLoadRoundTrip`;
  - `TestPruneDropsOlderThan30Days` (samples at now-31d, now-29d);
  - `TestSummaryLowerMedianIgnoresErrors`: TTFTs 900, 600, 700 plus one
    errored sample give N=3, Errors=1, TTFTP50MS=700;
  - `TestSummaryOtherTokenIgnored`.
- Depends on: nothing.
- Verify: the tests pass.

### Step 2: `transcript.FirstOutput`

- Tests (`first_test.go`), one table: for each kind, at least one true line
  and two false lines. The false lines must include that kind's pre-model
  events: claude `system` init, opencode `step_start`, agy `init`, codex
  `thread.started` and `turn.started`. Also cover a codex `item.completed`
  of type `error`, which is false. A non-JSON line (`relay-exit:0`) is false
  for every kind, and an unknown kind is false.
- Depends on: nothing.
- Verify: the tests pass.

### Step 3: store path, runtime field, `Probe`, `FormatProbe`

- Deliverable:
  - `store.LatencyPath()` (`<root>/latency.json`, beside `AvailabilityPath()`);
  - `Runtime.LatencyPath`, wired in `cmd/relay/main.go` next to `AvailabilityPath`;
  - `internal/relay/probe.go` per §4.
- Tests (`probe_test.go`) use a fake `LineExec` that records argv and dir
  and feeds scripted lines. Each scripted line advances a fake clock that
  the test installs as `rt.Now` by a set step. Use a real `store.New(t.TempDir())`
  and a `LatencyPath` under `t.TempDir()`. Build the candidate set the way
  existing `internal/relay` tests do (find a helper with
  `grep -n "candidate.NewSet\|candidate.Set{" internal/relay/*_test.go | head -3`).
  - `TestProbeCandidateMeasuresFirstOutput`: an opencode candidate with
    role builder. Lines: step_start at +100ms, text at +600ms, step_finish,
    then Run returns at +900ms. Want TTFTMS=600, TotalMS=900, Err="". The
    argv contains `--agent`, `plan-executor` and the probe prompt. The dir
    no longer exists after return.
  - `TestProbeCandidateNoOutput`: only a step_start, then nil gives
    Err "no model output" and TTFTMS 0.
  - `TestProbeCandidateRunErrorKeepsTTFT`: a text line, then Run returns an
    error gives TTFTMS > 0 and Err containing the error text.
  - `TestProbeCandidateNoKnownRole`: Roles `["nope"]` gives
    Err "no known role", and the fake was never called.
  - `TestProbeUnknownTokenRunsNothing`: the error is returned and the fake
    was never called.
  - `TestProbeRecordsHistory`: two candidates give two results in `Refs()`
    order. `latency.Load(rt.LatencyPath)` then has 2 samples with the right
    tokens and host, and `each` was called twice, in order.
  - `TestFormatProbe`: a success line and a failure line, with exact
    strings.
- Depends on: steps 1 and 2.
- Verify: the tests pass. Mutation (report it): in `ProbeCandidate`, set
  TTFT on **every** matching line rather than the first. Add a second
  `text` line at +800ms to the first test's script so that this mutation
  makes `TestProbeCandidateMeasuresFirstOutput` fail. Restore.

### Step 4: listing shows p50; CLI

- Deliverable: `FormatCandidatesLatency` with `FormatCandidates`
  delegating, `cmdCandidates --probe`, `probe_exec.go`, and the help line.
- Test: in the file holding the `FormatCandidates` tests
  (`grep -ln "FormatCandidates(" internal/relay/*_test.go`), add
  `TestFormatCandidatesLatencySuffix`. A two-candidate set, where one has
  `Summary{N: 3, TTFTP50MS: 640}`, gives `ttft p50 640ms (n=3, 30d)` on
  that line only. The other line equals `FormatCandidates`' line for the
  same candidate. Every existing `FormatCandidates` test passes unmodified.
- **Do not add a `cmd/relay` test that runs `relay candidates --probe`**: it
  would spawn a harness, and CI runners have no harness binary and no
  network. Plain `relay candidates` is already covered through the pure
  formatter.
- Depends on: step 3.
- Verify: `go build ./cmd/relay && ./relay candidates --help 2>&1 | grep probe`
  shows the flag. Delete the binary afterwards.

### Step 5: full check

- `make check` passes, and `git diff --stat` touches only the files in §2
  (name any substitution you made for the FormatCandidates test file).
- Report: each verify output, the mutation result, and the diff stat.
  Commit on the binding's branch with the message
  `feat(candidates): relay candidates --probe records time to first output per candidate (#324)`.
