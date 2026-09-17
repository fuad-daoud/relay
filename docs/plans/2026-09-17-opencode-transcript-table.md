# Pin the opencode transcript table to a live capture (#173)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #173, the task #168 left open (plan
`docs/plans/2026-09-17-headless-transcript.md` Task 9; spec
`docs/specs/2026-09-17-headless-transcript-design.md` §1 scope boundary,
§4.1, §7 step 6).
**Depends on:** nothing open. Not a new verb; not gated by the #114 freeze.

**Goal:** a headless opencode builder's `NNN-builder.log` reads like a
claude or agy builder's. Today every opencode event except `error` renders
as `[type]` (rule 5) because no live capture existed. The capture now
exists -- the planner ran `opencode run … --format json` on 2026-09-17
against `opencode/nemotron-3.5-lightning-free` -- and this plan pins the
table to it. The trimmed fixture and its expected rendering are in Task 1
verbatim; nothing is inferred.

**Architecture:** `internal/transcript/opencode.go` gains one `case` per
event type the capture shows, using only the shared helpers (`toolLine`,
`okLine`, `errLine`, `str`, `asMap`, `unknown`). The one shape opencode has
that claude and agy do not: a `tool_use` event arrives **once, after the
tool has finished**, carrying both the call (`part.state.input`) and the
result (`part.state.output` or `part.state.error`). It therefore renders as
**two lines** from one event -- the call line and the result line -- which
`Render`'s `[]string` contract already allows. A `tool_use` whose `status`
is neither `completed` nor `error` falls to rule 5, the same stance the agy
table takes for a tool step in a state it does not know. The spec's
vocabulary (§4.1) needs no new shape; opencode's auto-rejected permission
surfaces as a tool error, not a `denied:` line, and the spec says so.

**Tech stack:** Go, `encoding/json` maps, table-driven tests, fixture pair
under `testdata/`. No new dependencies.

## What the capture showed (2026-09-17, opencode v2.0.5)

Probe: `opencode run "Read the file ./probe.txt with your file-reading
tool, run 'echo probe' with your shell tool, then reply with the single word
done." -m opencode/nemotron-3.5-lightning-free --format json`, from a
directory containing `probe.txt`. Exit 0 in 12 s. A second run asking for
`/etc/hostname` hit opencode's `external_directory` permission, which
`run` auto-rejects (to stderr, not the stream) and then aborted the session;
that run gave the `tool_use` error shape and the `aborted` error event.

Every event is `{"type": T, "timestamp": ms, "sessionID": s, ...}`. Types
seen, with the fields the table reads:

| `type` | payload the table reads | notes |
| --- | --- | --- |
| `step_start` | -- | `part.type: "step-start"`; one per model turn |
| `step_finish` | -- | `part.reason`, `part.cost`, `part.tokens` -- accounting, not transcript |
| `text` | `part.text` | assistant text; the whole text in one event |
| `tool_use` | `part.tool`, `part.state.status`, `part.state.input`, `part.state.output`, `part.state.error` | one event per call, emitted after completion; `status` seen: `completed`, `error` |
| `error` | `error.type`, `error.message` | session-level; `error.type` seen: `aborted`, `provider.auth`, `provider.no-route` |

Stream order is emission order, not timestamp order: in the aborted run the
`error` line precedes the `tool_use` line whose timestamp is earlier. Render
is stateless per line, so this only matters for reading a `.log`; the
fixture keeps the captured order.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/opencode-table` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/opencode-table`, cut from `main`. |
| `~/.local/state/relay/opencode-table` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent behaviour that is not in the plan. In particular: if the expected
`.log` in Task 1 Step 2 cannot be produced from the fixture by a
`renderOpencode` that uses only the shared helpers, that is a spec question
-- report it, do not add a helper.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` that this change touches directly, and say so
in your report:

```bash
gofmt -l .
go vet ./...
go test -count=1 -race ./internal/transcript/
go mod tidy && git diff --exit-code go.mod go.sum
```

Do **not** run `herdr`, `agy`, `claude` or `opencode` yourself: the capture
is already in this plan. **No test is added under `cmd/relay`** (CI runners
have no `herdr`; CLAUDE.md). Nothing here reaches herdr.

## Global constraints

- Files touched, and no others: `internal/transcript/opencode.go`,
  `internal/transcript/transcript_test.go`,
  `internal/transcript/testdata/opencode.jsonl` (new),
  `internal/transcript/testdata/opencode.log` (new),
  `docs/specs/2026-09-17-headless-transcript-design.md`.
- `transcript.go` is not edited. If a helper seems missing, stop.
- The claude and agy fixtures and tables are not edited.
- Commit after each task with the message given.

---

### Task 1: the fixture pair and the table

**Files:**
- Create: `internal/transcript/testdata/opencode.jsonl`
- Create: `internal/transcript/testdata/opencode.log`
- Modify: `internal/transcript/opencode.go`
- Modify: `internal/transcript/transcript_test.go`

- [ ] **Step 1: write the fixture.** Create
`internal/transcript/testdata/opencode.jsonl` with exactly these seven
lines (one JSON object per line, trailing newline after the last). They are
the live capture with ids and timestamps fixed and the per-tool `metadata`
and `time` objects dropped; every value the table reads is untouched.

```
{"type":"step_start","timestamp":1789589781193,"sessionID":"ses_1","part":{"id":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"step-start"}}
{"type":"tool_use","timestamp":1789589781193,"sessionID":"ses_1","part":{"partID":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"tool","id":"call-1","tool":"read","state":{"status":"completed","input":{"path":"./probe.txt"},"output":"Read file ./probe.txt, lines 1-1\n1: probe-file-contents","title":"read"}}}
{"type":"tool_use","timestamp":1789589781193,"sessionID":"ses_1","part":{"partID":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"tool","id":"call-1","tool":"shell","state":{"status":"completed","input":{"command":"echo probe"},"output":"probe\n","title":"shell"}}}
{"type":"step_finish","timestamp":1789589781193,"sessionID":"ses_1","part":{"id":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"step-finish","reason":"tool-calls","cost":0,"tokens":{"input":121449,"output":49,"reasoning":57,"cache":{"read":0,"write":0}}}}
{"type":"text","timestamp":1789589781193,"sessionID":"ses_1","part":{"id":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"text","text":"done"}}
{"type":"error","timestamp":1789589781193,"sessionID":"ses_1","error":{"type":"aborted","message":"Session interrupted: shutdown"}}
{"type":"tool_use","timestamp":1789589781193,"sessionID":"ses_1","part":{"partID":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"tool","id":"call-1","tool":"read","state":{"status":"error","input":{"path":"/etc/hostname"},"error":"The user declined this tool call"}}}
```

- [ ] **Step 2: write the expected rendering.** Create
`internal/transcript/testdata/opencode.log` with exactly these eight lines
(trailing newline after the last; the two result lines are indented by two
spaces, as `okLine`/`errLine` produce):

```
read ./probe.txt
  -> ok: Read file ./probe.txt, lines 1-1
shell echo probe
  -> ok: probe
done
  -> error: Session interrupted: shutdown
read /etc/hostname
  -> error: The user declined this tool call
```

Derivation, line by line, so nothing is taken on trust: `step_start` and
`step_finish` are noise (nothing). `read` picks `path` by the shared rule;
its output's first line is the `ok` text. `shell` picks `command`; its
output `"probe\n"` has first line `probe`. `text` renders `part.text`
verbatim. `error` renders as it already does. The errored `tool_use`
renders its call line, then `errLine(part.state.error)`.

- [ ] **Step 3: watch it fail.** Run
`go test -count=1 ./internal/transcript -run TestFixtures/opencode`. It
must FAIL, with every non-error line rendered as `[step_start]`,
`[tool_use]`, `[step_finish]`, `[text]`. If it passes, the fixture was not
picked up -- check the filename.

- [ ] **Step 4: pin the table.** Replace the body of `renderOpencode` in
`internal/transcript/opencode.go` so that it implements this table, and
replace its doc comment (the word "provisional" and the sentence about the
scope boundary go; say instead that the table was pinned from the
2026-09-17 capture and that `tool_use` carries call and result in one
event):

| `str(obj["type"])` | render |
| --- | --- |
| `step_start`, `step_finish` | `nil` (noise) |
| `text` | `[]string{str(part["text"])}` where `part := asMap(obj["part"])` |
| `tool_use` | see below |
| `error` | `[]string{errLine(str(asMap(obj["error"])["message"]))}` -- unchanged |
| anything else | `[]string{unknown(obj)}` |

`tool_use`, in pseudocode -- use only the named helpers:

```
part  := asMap(obj["part"])
state := asMap(part["state"])
call  := toolLine(str(part["tool"]), asMap(state["input"]))
switch str(state["status"]):
  "completed": return [call, okLine(str(state["output"]))]
  "error":     return [call, errLine(str(state["error"]))]
  default:     return [unknown(obj)]        // rule 5: a status the capture did not show
```

A `text` event whose `part.text` is the empty string renders as nothing,
the same as the claude table's empty text block (`claude.go` drops it);
opencode emits such events in JSON mode for a turn with an empty preamble
before its tool calls.

- [ ] **Step 5: replace the provisional unit test.** In
`internal/transcript/transcript_test.go`, rename
`TestOpencodeTableProvisional` to `TestOpencodeTable`, rewrite its comment
to say the table is pinned from the 2026-09-17 capture and that this test
covers the branches the fixture cannot (the fixture covers the happy
shapes), and make its cases exactly these five:

| name | line | want |
| --- | --- | --- |
| `error` | the existing `provider.no-route` line, unchanged | unchanged |
| `tool_use in an unknown status is rule 5` | `{"type":"tool_use","part":{"type":"tool","tool":"read","state":{"status":"running","input":{"path":"a"}}}}` | `[]string{"[tool_use]"}` |
| `tool_use with no argument` | `{"type":"tool_use","part":{"type":"tool","tool":"todoread","state":{"status":"completed","input":{},"output":""}}}` | `[]string{"todoread", "  -> ok"}` |
| `unknown type is its type` | `{"type":"session_compacted","part":{}}` | `[]string{"[session_compacted]"}` |
| `missing type is ?` | `{"part":{"type":"text","text":"hi"}}` | `[]string{"[?]"}` |

The case named `anything else is its type` (which asserts `text` renders
as `[text]`) is deleted: it pinned the provisional behaviour this task
removes.

- [ ] **Step 6: green.** Run `go test -count=1 -race ./internal/transcript/`.
PASS, including `TestFixtures/opencode`, `TestFixtures/claude`,
`TestFixtures/agy` and `TestOpencodeTable`.

- [ ] **Step 7: mutation check.** Temporarily change the `"completed"` arm
to return only `[call]` (drop the ok line); `TestFixtures/opencode` must
fail. Temporarily change the `default` arm to return `[call]`;
`TestOpencodeTable/tool_use in an unknown status is rule 5` must fail.
Revert both. Name both results in your report.

- [ ] **Step 8: Commit**

```bash
git add internal/transcript/opencode.go internal/transcript/transcript_test.go internal/transcript/testdata/opencode.jsonl internal/transcript/testdata/opencode.log
git commit -m "feat(transcript): pin the opencode table to a live capture (#173)"
```

---

### Task 2: spec amendments

**Files:**
- Modify: `docs/specs/2026-09-17-headless-transcript-design.md`

Text edits only; no code. Keep the document's voice and table style.

- [ ] **Step 1: status line.** In the `**Status:**` line near the top,
replace `opencode table provisional until step 6 runs` with `opencode
table pinned 2026-09-17 (#173)`.

- [ ] **Step 2: §1 scope boundary.** Delete the bullet that begins
`opencode's rendering table is **provisional**` (five lines, ending
`is noise, not silence.`).

- [ ] **Step 3: §2 file structure.** In the file-structure block, change
the annotation on `internal/transcript/opencode.go` from
`opencode table (provisional)` to `opencode table`. The block names
fixtures only generically (`testdata/<kind>.jsonl + testdata/<kind>.log` on
the `transcript_test.go` line), so nothing else changes there.

- [ ] **Step 4: §4.1 opencode paragraph.** Replace the paragraph that
begins `**opencode** (`type`), provisional:` with a table in the same form
as the claude and agy tables, followed by one short paragraph:

```
**opencode** (`type`):

| event | render |
|---|---|
| `tool_use`, `part.state.status: completed` | tool call: `part.tool`, `part.state.input`; then `  -> ok: <part.state.output first line>` -- two lines from one event |
| `tool_use`, `part.state.status: error` | tool call as above; then `  -> error: <part.state.error first line>` |
| `tool_use`, any other status | rule 5 |
| `text` | `part.text` verbatim |
| `error` | `  -> error: <error.message>` (session-level: `aborted`, `provider.auth`, `provider.no-route` seen) |
| `step_start`, `step_finish` | noise |

opencode emits one `tool_use` per call, after the tool has finished, with
the call and its result in the same event; the table renders both lines
from it. A permission `run` auto-rejects is not a denial list but a
`tool_use` in status `error` with the message `The user declined this tool
call`, so it renders as a tool error, not a `denied:` line. Fixture:
`testdata/opencode.jsonl` from the 2026-09-17 probe against
`opencode/nemotron-3.5-lightning-free` (#173).
```

- [ ] **Step 5: §7 step 6.** Change the step that begins
`**opencode capture** (needs a working provider)` so it reads as done:
`**opencode capture**: done 2026-09-17 (#173); fixture and table pinned.`
Keep the numbering. In the same section's step 1, change `and the
provisional opencode table` to `and the opencode table`.

- [ ] **Step 6: check nothing else still says provisional.**
`grep -n provisional docs/specs/2026-09-17-headless-transcript-design.md internal/transcript/` must print nothing.

- [ ] **Step 7: Commit**

```bash
git add docs/specs/2026-09-17-headless-transcript-design.md
git commit -m "docs: opencode transcript table is pinned, not provisional (#173)"
```

---

## Report

Write `NNN-report.md` in the drop directory with: the four `make check`
constituents you ran and their results; the two mutation results from Task
1 Step 7, each naming the test that failed; `git diff --stat main` (it must
list exactly the five files in Global constraints); anything you did not do
and why. List every file you touched outside the plan's `Files` lists, if
any -- there should be none. Then create the `NNN-done` marker.
