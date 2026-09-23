# Plan: a failed probe reports the harness's own error message

If any step is impossible as written or contradicts the code you find, STOP
and report. Do not bend a test to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so read the "Read first"
files in one step (parallel reads), write each new file in one call, make
each existing file's change in one edit call, and loop with
`go test ./internal/transcript/ ./internal/relay/ -run '<names>' -count=1`.
Run `make check` once at the end.

Read first:
- `internal/transcript/first.go` and `first_test.go`: the pattern to copy
  (one pure per-kind line classifier plus a table test)
- `internal/transcript/{claude,opencode,codex,agy}.go`: only the `error` /
  `turn.failed` / `result` cases, for field names
- `internal/relay/probe.go` lines 60-160 (`ProbeCandidate`) and
  `probe_test.go` lines 20-80 (`fakeExec`) plus
  `TestProbeCandidateRunErrorKeepsTTFT`

## 1. System Overview

When a probe's harness fails, `relay candidates --probe` reports `Err` from
the exit error plus the last 300 bytes of **stderr**. codex writes the real
reason to **stdout** as a JSON event, so the probe showed
`error: exit status 1: Reading additional input from stdin...`. The actual
message was:

```
{"type":"error","message":"You’ve hit your usage limit. ... try again at Oct 19th, 2026 10:14 PM."}
{"type":"turn.failed","error":{"message":"You’ve hit your usage limit. ..."}}
```

Fix: a pure classifier that pulls a fatal error message out of a stream
line for each harness. `ProbeCandidate` keeps the last one it sees, and on
failure reports that message instead of the stderr tail.

## 2. File Structure

```
internal/transcript/errtext.go       CREATE  ErrorText(kind, line) (string, bool)
internal/transcript/errtext_test.go  CREATE  table test
internal/relay/probe.go              MODIFY  ProbeCandidate collects ErrorText; Err assembly
internal/relay/probe_test.go         MODIFY  two tests
```

## 3. Data Structures & Type Definitions

None new.

## 4. Interface Definitions & Component Contracts

### `transcript.ErrorText(kind string, line []byte) (string, bool)`

Pure. It decodes one JSON line and returns the harness's own message for a
**fatal** error event, trimmed of surrounding whitespace. It returns
`("", false)` for anything else, including non-JSON, an unknown kind, or an
empty message.
- `codex`:
  - `type == "error"` → `message`;
  - `type == "turn.failed"` → `error.message`.
  - **Not** `item.completed` with `item.type == "error"`: that is a
    non-fatal warning (e.g. "Exceeded skills context budget").
- `opencode`: top-level `type == "error"` → `error.message`. Not a
  tool_use part whose state is error: that is a tool failure the model
  sees, not a run failure.
- `claude`:
  - `type == "result"` with `is_error == true` → `result` (string); if that
    is empty, `"error result"`;
  - `type == "error"` → `message`, else `error.message`.
- `agy`: `event == "result"` whose `result.status` is non-empty and not
  `"SUCCESS"` → `result.error` if it is a string, else
  `result.error.message`, else `"result status " + status`.

### `ProbeCandidate` (changed)

- In the harness `onLine` callback (not the git init call), **before** the
  `seen` latch check: if `transcript.ErrorText(c.Harness, line)` returns
  ok, store the message in `harnessErr`, so the last one wins. This must
  run even after the first output has been seen.
- Err assembly, replacing today's `switch`:
  1. `runErr != nil && harnessErr != ""`:
     `Err = probeErrText(harnessErr + " [" + exitWord + "]")`, where
     `exitWord` is `runErr.Error()` cut at its first `":"` (e.g.
     `exit status 1`). If there is no colon, use the whole string.
  2. `runErr != nil`: as today (`probeErrText(runErr.Error())`).
  3. `!seen && harnessErr != ""`: `Err = probeErrText(harnessErr)`.
  4. `!seen`: `"no model output"`, as today.
  5. Otherwise `Err` stays "". A harness that produced output and exited 0
     is a success, even if it logged an error event.
- TTFTMS and TotalMS are unchanged.

## 5. High-Level Pseudocode

```
onLine(line):
    if msg, ok = ErrorText(kind, line): harnessErr = msg
    if seen: return
    if FirstOutput(kind, line): seen = true; ttft = now - start
after Run: Err per the five rules above
```

## 6. Error Handling Strategy

Unchanged: probe failures are data in `Err`. The 300-byte cap
(`probeErrText`) still applies.

## 7. Ordered Implementation Steps

### Step 1: `ErrorText`

- Deliverable: `internal/transcript/errtext.go`.
- Test `TestErrorText` (`errtext_test.go`), one table:
  - codex `error` → its message; codex `turn.failed` → its error.message;
    codex `item.completed` of type `error` → false;
  - opencode top-level `error` → error.message; opencode `tool_use` with
    `state.status:"error"` → false;
  - claude `result` with `is_error:true` and `result:"boom"` → `boom`;
    claude `result` with `is_error:false` → false;
  - agy `result` with `status:"ERROR"` and `error:{"message":"quota"}` →
    `quota`; agy `result` with status `SUCCESS` → false;
  - `relay-exit:1` → false for every kind; an unknown kind → false.
- Verify: the test passes.

### Step 2: use it in `ProbeCandidate`

- Deliverable: §4 in `probe.go`.
- Tests in `probe_test.go`:
  - `TestProbeCandidateReportsHarnessError`: a codex candidate. Script:
    `thread.started`, `turn.started`,
    `{"type":"error","message":"You've hit your usage limit."}`,
    `{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}`,
    then Run returns
    `errors.New("exit status 1: Reading additional input from stdin...")`.
    Want: Err == `You've hit your usage limit. [exit status 1]`, and Err
    does **not** contain `stdin`.
  - `TestProbeCandidateHarnessErrorWithoutExitError`: an opencode candidate
    whose script has only a top-level `error` event and Run returns nil.
    Want: Err == that event's message (not `no model output`).
- Every existing probe test passes unmodified. In particular,
  `TestProbeCandidateRunErrorKeepsTTFT` has no error event, so it still
  gets rule 2.
- Verify: `go test ./internal/relay/ -run 'TestProbe|TestFormatProbe' -count=1`
  passes. Mutation (report it): drop rule 1, so a run error always uses the
  stderr text. `TestProbeCandidateReportsHarnessError` fails. Restore.

### Step 3: full check

- `make check` passes, and `git diff --stat` shows exactly the four files
  in §2.
- Report: each verify output, the mutation result, and the diff stat.
  Commit on the binding's branch with the message
  `fix(candidates): a failed probe reports the harness's own error message`.
- Do not run a real probe. The planner does that after the merge.
