# Headless transcript, round 2: tool output on the result line; the ui keeps the last round's log (#168)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-17-headless-transcript-design.md` (§ below).
This round amends it: §4.1's result line and a new §4.7 for the `ui`
terminal tab. The amendments are Task 3.
**Issue:** #168. Round 1 landed Tasks 1-8 of
`docs/plans/2026-09-17-headless-transcript.md` on this branch.
**Depends on:** nothing open.

**Goal:** Two things a live run on 2026-09-17 showed. (1) A tool result
renders as a bare `  -> ok`, so three parallel claude calls give three
identical lines and a `run_command` whose output was `FAIL` reads as
success; the first line of the tool's output goes on the result line.
(2) `relay ui`'s terminal tab shows "no round is running, so there is no
log yet" the moment a round closes, because `clearProcess` blanks
`Builder.LogPath`; between rounds the tab shows the last round's log,
which `Builder.StreamRound` names exactly.

**Architecture:** `transcript` gains `okLine(output)` beside `errLine`;
claude's `tool_result` content and agy's `tool_info.output` feed it.
`ui.fetchTerminal` falls back from `LogPath` to
`Store.BuilderLogPath(name, StreamRound)`. No other reader changes.

**Tech stack:** Go 1.22. Verification is the `make check` constituent set.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-transcript` | **the git worktree. Every source edit goes here.** Branch `relay/headless-transcript`, already carrying round 1's eight commits. |
| `~/.local/state/relay/headless-transcript` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Run the constituents of `make check` directly, in this order, and say so in
your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr`, `agy`, `claude` or `opencode` yourself. No test is
added under `cmd/relay`.

## Global constraints

- Result lines are exactly `  -> ok` / `  -> ok: <first line of output>` /
  `  -> error` / `  -> error: <first line of message>`. The first line is
  `oneLine`: first line, `\r` stripped, 200 bytes on a rune boundary plus
  `...`. Still one line per event; never more of the output than that.
- `ui` never renders and never writes; it reads whichever log the rule in
  Task 2 names.
- One commit per task.

---

### Task 1: `-> ok: <first line of output>` for claude and agy

**Files:**
- Modify: `internal/transcript/transcript.go` (`oneLine` strips `\r`; new `okLine`)
- Modify: `internal/transcript/claude.go` (the `user`/`tool_result` case)
- Modify: `internal/transcript/agy.go` (the `DONE` case)
- Modify: `internal/transcript/transcript_test.go`
- Modify: `internal/transcript/testdata/claude.log`, `testdata/agy.jsonl`, `testdata/agy.log`

**Interfaces:**
- Produces: `func okLine(output string) string` -- `"  -> ok"` when `oneLine(output) == ""`, else `"  -> ok: " + oneLine(output)`.

- [ ] **Step 1: Update the fixtures.**

`testdata/claude.log` line 2 becomes `  -> ok: probe` (the fixture's
`tool_result` content is `"probe"`).

`testdata/agy.jsonl`: insert these two lines (from the 2026-09-17 live
run) **before** the final `result` line, so the file has eight lines:

```
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go test -count=1 ./internal/transcript/"}}}}
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":3,"state":"DONE","step_type":"tool","tool_name":"run_command","duration_seconds":0.9,"tool_info":{"name":"run_command","parameters":{"CommandLine":"go test -count=1 ./internal/transcript/"},"output":"ok  \tgithub.com/fuad-daoud/relay/internal/transcript\t0.003s\r\n"}}}
```

`testdata/agy.log` becomes (five lines; the tab characters inside the
`ok` line are the real ones from `go test`):

```
view_file /etc/hostname
  -> error: permission check failed for read_file "/etc/hostname": user denied permission for read_file(/etc/hostname)
run_command go test -count=1 ./internal/transcript/
  -> ok: ok  	github.com/fuad-daoud/relay/internal/transcript	0.003s
denied: read_file (ViewFile)
```

- [ ] **Step 2: Update and add tests** in `transcript_test.go`:

In `TestClaudeTable`, add cases:

```go
		"result ok with string content": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":"     1\t# relay\n     2\t"}]}}`,
			[]string{"  -> ok:      1\t# relay"},
		},
		"result ok with array content": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":[{"type":"text","text":"7c3ca64 docs: x\ne19924c feat: y"}]}]}}`,
			[]string{"  -> ok: 7c3ca64 docs: x"},
		},
		"result ok with empty content": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":""}]}}`,
			[]string{"  -> ok"},
		},
```

In `TestAgyTable`, change `"tool done"` to:

```go
		"tool done with output": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"list_dir","tool_info":{"output":"cmd/\ndist/\ndocs/"}}}`,
			[]string{"  -> ok: cmd/"},
		},
		"tool done without output": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"write_to_file","tool_info":{"output":""}}}`,
			[]string{"  -> ok"},
		},
		"tool done with crlf output": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"run_command","tool_info":{"output":"FAIL\tx [setup failed]\r\nmore\r\n"}}}`,
			[]string{"  -> ok: FAIL\tx [setup failed]"},
		},
```

Add to `TestOneLineCutsOnARuneBoundary`:

```go
	if got := oneLine("with cr\r\nnext"); got != "with cr" {
		t.Errorf("oneLine = %q, want the carriage return stripped", got)
	}
```

- [ ] **Step 3: Run to verify they fail** -- `go test -count=1 ./internal/transcript`: `TestFixtures/{claude,agy}`, the new table cases and the `\r` case FAIL.

- [ ] **Step 4: Implement.** In `transcript.go`, `oneLine` strips the
carriage return after cutting the first line:

```go
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "\r")
```

and beside `errLine`:

```go
// okLine is a successful tool result: "  -> ok: <first line of its output>",
// or "  -> ok" when the harness gave none. One line, so three parallel
// calls' results still tell apart.
func okLine(output string) string {
	if output = oneLine(output); output == "" {
		return "  -> ok"
	}
	return "  -> ok: " + output
}
```

In `claude.go`, the non-error branch of the `user` case becomes
`out = append(out, okLine(resultText(blk["content"])))`. In `agy.go`, the
`DONE` case becomes `return []string{okLine(str(info["output"]))}`.

- [ ] **Step 5: Run** `go test -count=1 ./internal/transcript`. Expected: PASS. Then `go test -count=1 ./internal/relay` -- the reconcile tests in `headless_test.go` use an agy `DONE` event without `output` (`agyToolDone`), so they still expect `  -> ok`; if any fails, stop and say which.

- [ ] **Step 6: Commit**

```bash
git add internal/transcript
git commit -m "feat(transcript): the first line of a tool's output rides on its result line (#168)"
```

---

### Task 2: `ui` terminal tab keeps the last round's log between rounds

**Files:**
- Modify: `internal/ui/fetch.go:151-160` (the headless branch of `fetchTerminal`)
- Modify: `internal/ui/fetch_test.go:380-405` (`TestFetchTerminalHeadlessIdleAndMissingLog`) and one new test

**Interfaces:**
- Rule: `logPath := b.Builder.LogPath; if logPath == "" && b.Builder.StreamRound != 0 { logPath = rt.Store.BuilderLogPath(name, b.Builder.StreamRound) }`. Empty only when both are unset.

- [ ] **Step 1: Update the tests.** In `TestFetchTerminalHeadlessIdleAndMissingLog`,
the first assertion's message becomes
`"headless builder; no round has run yet, so there is no log"`. Add:

```go
func TestFetchTerminalHeadlessBetweenRoundsShowsTheLastRoundsLog(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	b := newTestBinding("webshop")
	b.Round = 3 // round 2 closed; nothing sent yet
	// Between rounds: no process, LogPath cleared, but the cursor still
	// names round 2 (transcript spec §3.4).
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless, StreamRound: 2, StreamOffset: 100}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	logPath := st.BuilderLogPath("webshop", 2)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("Bash go test ./...\n  -> ok: ok\nrelay-exit:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tMsg := fetchTerminal(context.Background(), rt, "webshop", 24)().(tabMsg)
	if tMsg.content.empty != "" || tMsg.content.err != nil {
		t.Fatalf("between rounds the last log must show: %+v", tMsg.content)
	}
	if tMsg.content.body != "Bash go test ./...\n  -> ok: ok\nrelay-exit:0" {
		t.Errorf("body = %q", tMsg.content.body)
	}
	if fh.readCalls != 0 {
		t.Errorf("readCalls = %d, want 0", fh.readCalls)
	}
}
```

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/ui -run TestFetchTerminalHeadless`. Expected: the new test FAILS on `empty`, the renamed message FAILS.

- [ ] **Step 3: Implement.** Replace the head of the headless branch:

```go
		if b.Builder.Headless() {
			// Between rounds clearProcess blanks LogPath, but the cursor
			// still names the last round that ran (transcript spec §4.7):
			// keep showing that log rather than a blank tab.
			logPath := b.Builder.LogPath
			if logPath == "" && b.Builder.StreamRound != 0 {
				logPath = rt.Store.BuilderLogPath(name, b.Builder.StreamRound)
			}
			if logPath == "" {
				return tabMsg{
					name: name,
					t:    tabTerminal,
					content: tabContent{
						loaded: true,
						empty:  "headless builder; no round has run yet, so there is no log",
					},
				}
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				return tabMsg{
					name: name,
					t:    tabTerminal,
					content: tabContent{
						loaded: true,
						empty:  "log not written yet: " + logPath,
					},
				}
			}
```

(the rest of the branch unchanged, reading `data`).

- [ ] **Step 4: Run** `go test -count=1 ./internal/ui`. Expected: PASS.

- [ ] **Step 5: Mutation check.** Remove the `StreamRound` fallback (keep
`logPath := b.Builder.LogPath` only). Run the new test: FAIL. Restore:
PASS. Say so in your report.

- [ ] **Step 6: Commit**

```bash
git add internal/ui
git commit -m "feat(ui): the terminal tab keeps a headless builder's last log between rounds (#168)"
```

---

### Task 3: spec and README

**Files:**
- Modify: `docs/specs/2026-09-17-headless-transcript-design.md` (§4.1 vocabulary table, new §4.7)
- Modify: `README.md` "Headless builders" paragraph

- [ ] **Step 1: Spec §4.1.** In the vocabulary table, the row
`| tool result, ok | `  -> ok` |` becomes
`| tool result, ok | `  -> ok: <first line of the tool's output>`, or `  -> ok` when there is none (claude: `tool_result` content; agy: `tool_info.output`) |`.
In the claude table, the `user`/`tool_result` row: "tool result; error iff
`is_error`; message is `content` when a string, else the first `text`
element" -> "...; the message (error) or output (ok) is `content` when a
string, else the first `text` element". In the agy table, the `DONE` row:
`  -> ok` -> `  -> ok: <tool_info.output first line>`. Under "Vocabulary",
add: "`oneLine` also strips a trailing `\r`: agy's command output is CRLF."

- [ ] **Step 2: Spec §4.7 (new, after §4.6).**

```
### 4.7 `ui.fetchTerminal` (existing; one fallback)

`clearProcess` blanks `Builder.LogPath` at round close, so the terminal
tab of a headless binding went blank the moment a round closed. Between
rounds the tab shows the log of `Builder.StreamRound` -- the last round
that had a process (§3.4) -- and says "no round has run yet" only when
that is 0. The tab reads; it never renders or writes.
```

- [ ] **Step 3: README.** In the "Headless builders" paragraph, the
parenthetical `its result (`  -> ok`, `  -> error: …`)` becomes
`its result with the first line of what it printed (`  -> ok: ok  github.com/… 0.4s`, `  -> error: …`)`,
and after "about two seconds behind." add: "Between rounds the tab keeps
the last round's log."

- [ ] **Step 4: Run** the four `make check` constituents. Expected: green.

- [ ] **Step 5: Commit**

```bash
git add docs/specs/2026-09-17-headless-transcript-design.md README.md
git commit -m "docs: transcript result line carries output; ui keeps the last log (#168)"
```

---

## Report

Per task: commit hash, the four constituents' last lines verbatim, the
mutation check's before/after (Task 2), anything not done and why. List
any file touched outside the plan's `Files` lists -- there should be none.
