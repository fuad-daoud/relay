# Plan: record model steps, tool calls and step latency per round (#323 part 5, #324 part 2)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so:
- Read everything in "Read first" in **one** step (parallel reads). Don't grep
  for what this plan already locates.
- Write `internal/usage/steps.go` and `steps_test.go` each in one write call.
  Make every other file's change in one edit call.
- Loop with `go test ./internal/usage/ ./internal/relay/ -run '<names>'`,
  fixing every reported error before the next run. Run `make check` once at
  the end.

Read first:
- `internal/usage/usage.go` lines 55-100 (`Usage`, `Sample`, `Fold`)
- `internal/usage/format.go` lines 35-135 (`ShortDuration`, `Line`, `Parts`)
- `internal/usage/spend.go` whole file (`Spend`, `Sum`, `Add`)
- `internal/usage/carry.go` lines 1-180 (how each harness's events are
  decoded today; reuse its event field names)
- `internal/relay/usage.go` lines 100-160 (`recordUsage`)
- `internal/relay/tab.go` lines 100-150 (`RenderTab`)
- `internal/relay/tab_test.go` and `internal/usage/format_test.go`, the
  tests whose expected strings may move
- One real opencode stream for shape reference, first 40 lines only:
  `head -40 ~/.local/state/relay/h-plist/001-builder.jsonl`. If that file is
  absent, use `internal/usage/testdata/opencode-stream.jsonl`.

## 1. System Overview

#323 showed that on a fast model behind a high-latency provider, a round's
wall time is set by its **number of model steps**. #324 needs time-to-first-
output per step to tell provider latency apart from generation. Today relay
records tokens, cost and wall time per round (`usage.Usage` on the report's
log entry), but not steps. The figures in #323 were hand-parsed from the
builder's stream.

This plan adds a pure stream analyser, `usage.StreamSteps`, which counts
model steps and tool calls in a round's builder stream
(`NNN-builder.jsonl`) and computes the median step duration and the median
time to first output per step. `recordUsage` attaches those figures to the
round's `Usage` at close. `relay show --log` (through `usage.Line`) and
`relay tab` (through `Spend`) display them.

Remote rounds get this for free: the server computes `Usage` at close, and
`BindingView.Usage` carries the same JSON struct to the client.

Out of scope: the sqlite history db columns (the full entry JSON is already
stored in `event.entry_json`), `usage.Parts`/`LiveParts` (the ui), and
back-filling old rounds.

## 2. File Structure

```
internal/usage/steps.go          CREATE  StepStats, StreamSteps, per-harness rules, median helper
internal/usage/steps_test.go     CREATE  per-harness synthetic-stream tests
internal/usage/usage.go          MODIFY  Usage gains four fields
internal/usage/format.go         MODIFY  Line appends the step parts; new msText helper
internal/usage/format_test.go    MODIFY  new Line case(s)
internal/usage/spend.go          MODIFY  Spend gains Steps, ToolCalls; Sum/Add
internal/usage/spend_test.go     MODIFY  new Sum/Add case
internal/relay/usage.go          MODIFY  recordUsage attaches StreamSteps
internal/relay/usage_test.go     MODIFY  one new test
internal/relay/tab.go            MODIFY  RenderTab: steps and calls/step columns
internal/relay/tab_test.go       MODIFY  expected output gains the columns
```

## 3. Data Structures & Type Definitions

### `usage.StepStats` (new, `internal/usage/steps.go`)

| Field | Type | Meaning |
|-------|------|---------|
| `Steps` | int | model steps (requests to the model); 0 = not measurable for this harness |
| `ToolCalls` | int | tool calls the model made |
| `StepP50MS` | int64 | median per-step model time in ms; 0 = unknown |
| `FirstOutputP50MS` | int64 | median per-step time from the step's start to its first output, in ms; 0 = unknown |

### `usage.Usage` (changed): four new fields, after `Samples`

```
Steps            int   `json:"steps,omitempty"`
ToolCalls        int   `json:"tool_calls,omitempty"`
StepP50MS        int64 `json:"step_p50_ms,omitempty"`
FirstOutputP50MS int64 `json:"first_output_p50_ms,omitempty"`
```

Doc comment: "Step figures from the builder stream (#323, #324); zero when
the harness's stream does not show them."

### `usage.Spend` (changed)

```
Steps     int `json:"steps"`
ToolCalls int `json:"tool_calls"`
```

`Sum` adds `u.Steps` and `u.ToolCalls` for every usage, and `Add` adds them
field-wise.

## 4. Interface Definitions & Component Contracts

### `func StreamSteps(harness string, stream []byte) StepStats`

- Pure: no I/O, no clock.
- Considers only lines whose first non-space byte is `{` that decode as JSON.
  Every other line (the `relay-exit:` / `relay-rusage:` trailers, blank
  lines, junk) is skipped, and a line that fails to decode is skipped
  silently.
- Dispatches on `harness` (`"opencode"`, `"claude"`, `"agy"`, `"codex"`).
  Any other harness returns the zero `StepStats`.
- A stream may hold a second run after a mid-round switch. The rules below
  key on event names unique to each harness, so another harness's lines are
  simply not matched. Do not try to split the file.
- Median: sort the samples ascending and take the element at index
  `(n-1)/2` (the lower median). No samples gives 0.

Per-harness rules:

**opencode** (top-level `"type"`, top-level numeric `"timestamp"` in epoch ms)
1. `step_start`: `Steps++`. Remember `stepStart = timestamp` and set
   `firstSeen = false`.
2. `text`, `reasoning` or `tool_use`, when a step is open and `!firstSeen`:
   the output time is `part.time.start` if present and > 0, else
   `part.state.time.start` (tool_use) if present and > 0, else the
   top-level `timestamp`. If `outputTime - stepStart >= 0`, append it to
   the first-output samples. Set `firstSeen = true`.
3. `tool_use`: `ToolCalls++`. This applies whether or not rule 2 fired.
4. `step_finish`, when a step is open: append `timestamp - stepStart` (if
   >= 0) to the step samples.
   Note: real opencode streams end with one `step_start` that has no
   `step_finish`. It counts as a step but contributes no duration sample.
   That is intended.

**claude** (top-level `"type"`, RFC3339 string `"timestamp"`)
- Consider only events of type `assistant` and `user` whose
  `parent_tool_use_id` is absent or null. Sub-agent traffic is excluded.
- A step is a distinct `message.id` among those `assistant` events:
  `Steps` = the number of distinct ids.
- `ToolCalls` = distinct `id`s of content blocks with `type == "tool_use"`
  in those `assistant` events. Each block may be repeated across events
  that share a message id, so dedupe by block id.
- Timing needs timestamps. Keep `lastUserTS` = the timestamp of the most
  recent main-thread `user` event, or of the `system`/`init` event if no
  user event has been seen yet (only when that event carries a timestamp).
  For each message id, at its **first** `assistant` event: if both that
  event and `lastUserTS` carry a timestamp, append `ts - lastUserTS` to
  first-output (if >= 0) and remember `(id, lastUserTS)`. At each later
  event with the same id, update the id's last-seen ts. When the stream
  ends, append each id's `lastSeen - lastUserTSAtStart` (if >= 0) to the
  step samples. Events without a timestamp contribute no timing, only
  counts.

**agy** (top-level `"event"`)
- `step_update` with `step_update.step_type == "agent_response"` and
  `state == "DONE"`: a step, counted once per distinct `step_index`. Append
  `duration_seconds * 1000` (rounded to int64, when > 0) to the step
  samples.
- `step_update` with `step_type == "tool"` and `state == "ACTIVE"`: a tool
  call, counted once per distinct `step_index`.
- No first-output timing, because agy has no wall-clock timestamps.

**codex** (top-level `"type"`)
- `Steps` stays 0, because codex does not expose model steps.
- `item.completed` whose `item.type` is `command_execution`,
  `file_change`, `collab_tool_call` or `mcp_tool_call`: `ToolCalls++`.
- No timing.

### `recordUsage` (changed, `internal/relay/usage.go`)

After the existing Fold/Harness/Provider/DurationMS lines: if
`src.StreamPath != ""`, read it with `os.ReadFile`. An error is ignored:
the step fields stay zero. Then copy `StreamSteps(src.Harness, data)` into
`u.Steps`, `u.ToolCalls`, `u.StepP50MS` and `u.FirstOutputP50MS`. This
happens whether or not `rt.Usage` is nil.

### `usage.Line` (changed)

- New helper `msText(ms int64) string`: `"%dms"` below 1000, else
  `"%.1fs"` of seconds (e.g. `3100` gives `"3.1s"`).
- After the token cells and **before** the money part, append these parts,
  each only when its value is non-zero:
  - `"<Steps> steps"`;
  - `"<ToolCalls/Steps formatted %.2f> calls/step"`, when both are > 0;
  - `"<ToolCalls> tool calls"`, only when `Steps == 0 && ToolCalls > 0`;
  - `"step p50 " + msText(StepP50MS)`;
  - `"first out p50 " + msText(FirstOutputP50MS)`.
- A Usage with all four fields zero renders exactly as today, so existing
  `TestLine` cases do not change.
- `Parts`, `LiveParts` and `LiveShort` do not change.

### `RenderTab` (changed, `internal/relay/tab.go`)

- Two new columns right after `rounds`: `steps` (`%6s`, empty when 0) and
  `calls/st` (`%8s`, `ToolCalls/Steps` as `%.2f`, empty when Steps is 0).
- Apply this to the header format, the row format and the total line alike.
  `--json` gains the two `Spend` fields automatically.

## 5. High-Level Pseudocode

```
StreamSteps(h, stream):
    stats, samples = zero
    for line in split(stream, "\n"):
        if not starts with "{": continue
        ev = decode(line) or continue
        rules[h].feed(ev)                  # per §4
    rules[h].finish()                       # claude: flush per-id step samples
    stats.StepP50MS = lowerMedian(stepSamples)
    stats.FirstOutputP50MS = lowerMedian(firstSamples)
    return stats

recordUsage(ctx, rt, src):
    ... existing ...
    if src.StreamPath != "":
        if data, err = readFile(src.StreamPath); err == nil:
            copy StreamSteps(src.Harness, data) into u
    return &u
```

Decode into small local structs in `steps.go`. Do not change the existing
carries in `carry.go`, and do not make `StreamSteps` depend on them.

## 6. Error Handling Strategy

None of this can fail a round. A missing or unreadable stream, or
undecodable lines, leave zeros. No new errors and no logging.

## 7. Ordered Implementation Steps

### Step 1: `StreamSteps`

- Deliverable: `internal/usage/steps.go` per §3/§4.
- Tests in `internal/usage/steps_test.go`. Build each stream inline in the
  test as a joined slice of JSON lines, ending with `"\n\nrelay-exit:0\n"`:
  - `TestStreamStepsOpencode`: 3 step_starts (at t=1000, 5000, 9000), the
    first two with step_finish (at 4000 and 8200). Step 1 has a `text` whose
    `part.time.start`=1600, then a `tool_use` (at 3900). Step 2 has two
    `tool_use`, the first with `part.state.time.start`=5700. Step 3 has only
    a `text` at `timestamp` 9300 with no part.time.
    Want: Steps=3, ToolCalls=3; StepP50MS = lowerMedian(3000, 3200) = 3000;
    FirstOutputP50MS = lowerMedian(600, 700, 300) = 600.
  - `TestStreamStepsClaude`: an init event with timestamp T0. A main-thread
    assistant message `m1` split across two events (T0+2s with a tool_use
    block `tu1`, then T0+3s repeating `tu1` plus a text block). A user
    tool_result at T0+4s. Assistant `m2` at T0+6s with tool_use `tu2`. A
    sub-agent assistant `m9` with `parent_tool_use_id:"tu2"` and a tool_use
    `tu9`.
    Want: Steps=2, ToolCalls=2 (`tu9` excluded, `tu1` deduped);
    FirstOutputP50MS = lowerMedian(2000, 2000) = 2000;
    StepP50MS = lowerMedian(3000, 2000) = 2000.
  - `TestStreamStepsClaudeNoTimestamps`: the same shape without any
    `timestamp` fields gives Steps=2, ToolCalls=2, both P50s 0.
  - `TestStreamStepsAgy`: two agent_response DONE (step_index 1 with
    duration 1.84, index 3 with 2.5), and one duplicate DONE line for index
    1. Tool steps at index 2: ACTIVE then DONE.
    Want: Steps=2, ToolCalls=1, StepP50MS=1840, FirstOutputP50MS=0.
  - `TestStreamStepsCodex`: two command_execution and one file_change
    item.completed, and one agent_message item.completed.
    Want: Steps=0, ToolCalls=3.
  - `TestStreamStepsUnknownHarnessAndJunk`: harness `"nope"` gives zero; for
    opencode, a stream of only junk lines and trailers gives zero.
- Depends on: nothing.
- Verify: all pass. Mutation (report it): count opencode steps on
  `step_finish` instead of `step_start`. `TestStreamStepsOpencode` fails
  (Steps=2). Restore.

### Step 2: carry it on `Usage` and attach at close

- Deliverable: the four `Usage` fields, and the `recordUsage` change.
- Test `TestRecordUsageAttachesStepStats` in `internal/relay/usage_test.go`:
  write an opencode stream with 2 steps and 1 tool call into `t.TempDir()`,
  then call `recordUsage` with a `usage.Source{Harness: "opencode",
  StreamPath: path}` and a runtime whose `Usage` is nil. Assert
  `Steps == 2` and `ToolCalls == 1`. A second case with a missing path
  gives zeros and no panic.
- Depends on: step 1.
- Verify: the new test passes, and all existing `internal/relay` usage
  tests pass unmodified.

### Step 3: display

- Deliverable: the `Line`/`msText` change, the `Spend` fields with
  `Sum`/`Add`, and the `RenderTab` columns.
- Tests:
  - `format_test.go`: a new `TestLine` case with Steps=157, ToolCalls=183,
    StepP50MS=3100, FirstOutputP50MS=640. It expects `"157 steps"`,
    `"1.17 calls/step"`, `"step p50 3.1s"` and `"first out p50 640ms"`, in
    that order, before the money part. Add another case with Steps=0 and
    ToolCalls=4 that expects `"4 tool calls"`.
  - `spend_test.go`: Sum over two usages adds Steps and ToolCalls, and Add
    does the same.
  - `tab_test.go`: update the expected `RenderTab` strings for the two new
    columns. Add one row with Steps=10 and ToolCalls=15 expecting
    `calls/st` of `1.50`, and a row with Steps=0 whose cells are blank.
    Change only expected strings for the new columns; if any other
    expectation would have to change, STOP and report it.
- Depends on: step 2.
- Verify: `go test ./internal/usage/ ./internal/relay/ ./internal/ui/...`
  passes. The ui golden tests must be unaffected, because `Parts` is
  unchanged.

### Step 4: real-stream sanity check (report only, no commit)

- Write a throwaway `go run` program under `/tmp` (not in the repo) that
  calls `usage.StreamSteps("opencode", <file>)` on
  `~/.local/state/relay/h-deliv/001-builder.jsonl`, or, if absent, on any
  `~/.local/state/relay/*/001-builder.jsonl` that is opencode. Put its
  output in the report next to that round's figures from #323 (h-deliv: 157
  steps, 183 tool calls). A mismatch beyond ±2 is a finding to report, not
  something to tune the rules toward.
- Depends on: step 1.

### Step 5: full check

- `make check` passes, and `git diff --stat` touches only the files in §2.
- Report: each verify output, the mutation result, the step 4 figures, and
  the diff stat. Commit on the binding's branch with the message
  `feat(usage): record model steps, tool calls and step latency per round (#323, #324)`.
