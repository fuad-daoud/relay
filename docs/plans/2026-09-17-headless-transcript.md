# Headless transcript: the builder log grows as the round runs (#168)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-17-headless-transcript-design.md`. Section
numbers below (§) refer to it.
**Issue:** #168. Closes it.
**Depends on:** nothing open.

**Goal:** A headless builder's `NNN-builder.log` grows as the round runs --
one human line per tool call, result, text and denial -- instead of being
written once at exit, so `relay ui`, `relay status` and `tail -f` show a live
round.

**Architecture:** The print form per kind switches to the harness's
streaming JSON output. The supervisor writes stdout (the stream) and its
`relay-exit:N` trailer to a new round file `NNN-builder.jsonl`, and stderr
to `NNN-builder.log` as today. A new package `internal/transcript` turns
one raw stream line into the human lines for it, per kind, stateless. The
daemon drains: each `reconcileHeadless` tick reads the `.jsonl` past a
cursor persisted on the builder endpoint (`StreamRound`, `StreamOffset`),
renders complete lines, appends them to the `.log` in one write, and
advances the cursor. Every existing reader and writer of the `.log`
(`logTail`, the `ui` terminal tab, `limitText`, `appendLogMarker`) is
untouched.

**Tech stack:** Go 1.22 (`go.mod`). Verification is the `make check`
constituent set (runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-transcript` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-transcript`, cut from `main`. |
| `~/.local/state/relay/headless-transcript` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr`, `agy`, `claude` or `opencode` yourself. **No test is
added under `cmd/relay`** (CI runners have no `herdr`; CLAUDE.md).
`internal/proc`'s tests start real processes (`sh`); Task 4 edits them.

## Global constraints

- `transcript.Render` never returns an error and never panics, whatever the
  input (§4.1). Rules in order: empty line -> nothing; non-JSON-object line ->
  itself verbatim; known event -> the table; noise -> nothing; anything else
  -> `[<type>]` (the `type` string, else the `event` string, else `?`).
- Rendered vocabulary is exactly (§4.1): `<name> <arg>` / `<name>`;
  `  -> ok`; `  -> error: <first line>` (or `  -> error` when the message is
  empty); assistant text verbatim; `denied: <what>`; `result: <status>`;
  final text verbatim. Two leading spaces on result lines. No usage or cost
  line, ever.
- The main argument is picked by this key order: `command`, `file_path`,
  `path`, `AbsolutePath`, `pattern`, `description`, `prompt`, `query`,
  `url`; then the only string parameter if there is exactly one; then the
  first string parameter in sorted key order; else none. One line, cut to
  200 bytes on a rune boundary plus `...`.
- Print forms (§3.5), before `extra`: agy
  `-p <prompt> --model M --agent <def> --output-format stream-json --print-timeout <budget>`;
  claude `-p <prompt> --model M --agent <def> --output-format stream-json --verbose`;
  opencode `run <prompt> -m P/M --agent <def> --format json`. No
  `--include-partial-messages`.
- The supervisor's trailer goes to stdout (the stream file) as
  `printf '\nrelay-exit:%s\n' "$?"`. `ExitCode` keeps reading the last line
  of the file it is given; callers now give it the stream path (§3.2, §3.3).
- The cursor (`Endpoint.StreamRound`, `StreamOffset`) is moved only by
  `startRound` and `drainStream`. `clearProcess` and `finishRound` do not
  touch it (§3.4, §4.4).
- `drainStream` never fails a tick: every failure is a `slog.Warn` and an
  unchanged binding; the cursor advances only after the append succeeded
  (§4.2, §6).
- Only the daemon drains. `status`, `ui` and the CLI never write rendered
  lines.
- One commit per task, on the worktree's branch.

---

### Task 1: `internal/transcript` -- `Render`, the shared rules, the claude table

**Files:**
- Create: `internal/transcript/transcript.go`
- Create: `internal/transcript/claude.go`
- Create: `internal/transcript/transcript_test.go`
- Create: `internal/transcript/testdata/claude.jsonl`
- Create: `internal/transcript/testdata/claude.log`

**Interfaces:**
- Produces: `func Render(kind string, line []byte) []string` (§4.1).
- Produces (package-private, used by Task 2): `func toolLine(name string, params map[string]any) string`,
  `func errLine(msg string) string`, `func oneLine(s string) string`,
  `func unknown(obj map[string]any) string`, `func str(v any) string`,
  `func asMap(v any) map[string]any`, `func asList(v any) []any`.

- [ ] **Step 1: Write the fixtures.**

`internal/transcript/testdata/claude.jsonl` (ten lines, from a live
`claude -p … --output-format stream-json --verbose` run on 2026-09-16,
trimmed of hook payloads and ids; keep each JSON object on one line):

```
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"}}
{"type":"system","subtype":"init","cwd":"/repo","session_id":"sess-1","tools":["Bash","Read","Edit"],"model":"claude-haiku-4-5-20251001"}
{"type":"system","subtype":"thinking_tokens","estimated_tokens":50,"session_id":"sess-1"}
{"type":"system","subtype":"thinking_tokens","estimated_tokens":50,"session_id":"sess-1"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"}]},"session_id":"sess-1"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo probe","description":"Run echo probe"}}]},"session_id":"sess-1"}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"probe","is_error":false}]},"session_id":"sess-1"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"}]},"session_id":"sess-1"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"done"}]},"session_id":"sess-1"}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"done","session_id":"sess-1","total_cost_usd":0.0299,"usage":{"input_tokens":18,"output_tokens":249},"permission_denials":[]}
```

`internal/transcript/testdata/claude.log` (the expected rendering; four
lines, trailing newline; the last two are the assistant's text and then
the result's `result` field -- claude repeats the final text there, and
both are rendered verbatim by the table):

```
Bash echo probe
  -> ok
done
done
```

- [ ] **Step 2: Write the tests** in `internal/transcript/transcript_test.go`:

```go
package transcript

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestFixtures renders every testdata/<kind>.jsonl line by line and compares
// the joined output to testdata/<kind>.log. Add a kind by adding its pair.
func TestFixtures(t *testing.T) {
	streams, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil || len(streams) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, stream := range streams {
		kind := strings.TrimSuffix(filepath.Base(stream), ".jsonl")
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile(stream)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", kind+".log"))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
				got = append(got, Render(kind, []byte(line))...)
			}
			if g, w := strings.Join(got, "\n")+"\n", string(want); g != w {
				t.Errorf("rendered:\n%s\nwant:\n%s", g, w)
			}
		})
	}
}

func TestRenderRules(t *testing.T) {
	cases := map[string]struct {
		kind string
		line string
		want []string
	}{
		"empty":               {"claude", "", nil},
		"blank":               {"claude", "   ", nil},
		"non-json":            {"claude", "relay-exit:0", []string{"relay-exit:0"}},
		"json but not object": {"claude", `["a"]`, []string{`["a"]`}},
		"broken json":         {"agy", `{"event":`, []string{`{"event":`}},
		"unknown type":        {"claude", `{"type":"brand_new"}`, []string{"[brand_new]"}},
		"unknown event":       {"agy", `{"event":"brand_new"}`, []string{"[brand_new]"}},
		"no type at all":      {"claude", `{"x":1}`, []string{"[?]"}},
		"unknown kind":        {"codex", `{"type":"item"}`, []string{"[item]"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Render(c.kind, []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Render(%q, %q) = %q, want %q", c.kind, c.line, got, c.want)
			}
		})
	}
}

func TestClaudeTable(t *testing.T) {
	cases := map[string]struct {
		line string
		want []string
	}{
		"text and tool in one message": {
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading it.\nTwo lines."},{"type":"tool_use","name":"Read","input":{"file_path":"internal/doctor/doctor.go"}}]}}`,
			[]string{"Reading it.\nTwo lines.", "Read internal/doctor/doctor.go"},
		},
		"tool with no argument": {
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"ListAgents","input":{}}]}}`,
			[]string{"ListAgents"},
		},
		"tool with a long multi-line command": {
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` + strings.Repeat("x", 250) + `\nsecond"}}]}}`,
			[]string{"Bash " + strings.Repeat("x", 200) + "..."},
		},
		"result error with array content": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":[{"type":"text","text":"exit status 1\nmore"}]}]}}`,
			[]string{"  -> error: exit status 1"},
		},
		"result error with empty message": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":""}]}}`,
			[]string{"  -> error"},
		},
		"final result with denials and error": {
			`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"gave up","permission_denials":[{"tool_name":"Bash"},{"tool_name":"Edit"}]}`,
			[]string{"denied: Bash", "denied: Edit", "result: error_during_execution", "gave up"},
		},
		"final result empty":  {`{"type":"result","subtype":"success","is_error":false,"result":""}`, nil},
		"system is noise":     {`{"type":"system","subtype":"hook_started"}`, nil},
		"rate limit is noise": {`{"type":"rate_limit_event"}`, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Render("claude", []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestMainArgumentPick(t *testing.T) {
	cases := map[string]struct {
		params map[string]any
		want   string
	}{
		"priority key wins over others":  {map[string]any{"description": "d", "command": "c"}, "Bash c"},
		"single string param":            {map[string]any{"AbsolutePathX": "/x"}, "Bash /x"},
		"first in sorted order":          {map[string]any{"zeta": "z", "alpha": "a", "n": 3}, "Bash a"},
		"non-string only":                {map[string]any{"n": 3, "ok": true}, "Bash"},
		"empty string is not an arg":     {map[string]any{"command": "", "b": "x"}, "Bash x"},
		"nil params":                     {nil, "Bash"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := toolLine("Bash", c.params); got != c.want {
				t.Errorf("toolLine = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOneLineCutsOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("é", 101) // 202 bytes; byte 200 is inside the 101st rune
	got := oneLine(s)
	if !strings.HasSuffix(got, "...") || strings.Count(got, "é") != 100 {
		t.Errorf("oneLine = %q; want 100 whole runes then ...", got)
	}
	if got := oneLine("short\nrest"); got != "short" {
		t.Errorf("oneLine = %q, want the first line", got)
	}
}
```

- [ ] **Step 3: Run to verify it fails** -- `go test -count=1 ./internal/transcript`
fails to compile (package does not exist).

- [ ] **Step 4: Implement `transcript.go`:**

```go
// Package transcript renders a headless builder's streamed output -- one
// JSON event per line, in each harness's own shape -- into the lines a
// human reads in NNN-builder.log (#168). It knows harness kinds and
// nothing else: no rounds, no files, no bindings. It is presentation only:
// nothing in relay decides anything on what it returns.
package transcript

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxArg is how much of a tool's main argument one rendered line carries.
const maxArg = 200

// argKeys is the order in which a tool call's parameters are tried for the
// one worth showing; the first non-empty string wins.
var argKeys = []string{"command", "file_path", "path", "AbsolutePath", "pattern", "description", "prompt", "query", "url"}

// Render turns one raw line of the stream (without its trailing newline)
// into the lines to append to the log, each without a trailing newline.
// Rules, in order: an empty line is nothing; a line that is not a JSON
// object is itself, verbatim (that is how the relay-exit trailer and a
// plain-text error reach the log); a known event renders per its kind's
// table; noise renders as nothing; anything else renders as "[<type>]" so a
// harness upgrade degrades to noise, not silence. Never errors, never
// panics.
func Render(kind string, line []byte) []string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	var obj map[string]any
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		return []string{string(line)}
	}
	switch kind {
	case "claude":
		return renderClaude(obj)
	case "agy":
		return renderAgy(obj)
	case "opencode":
		return renderOpencode(obj)
	}
	return []string{unknown(obj)}
}

// unknown is rule 5: the event's type, or event, or "?".
func unknown(obj map[string]any) string {
	if t := str(obj["type"]); t != "" {
		return "[" + t + "]"
	}
	if e := str(obj["event"]); e != "" {
		return "[" + e + "]"
	}
	return "[?]"
}

// toolLine is "<name> <main argument>", or "<name>" when no parameter is a
// non-empty string.
func toolLine(name string, params map[string]any) string {
	if arg, ok := mainArg(params); ok {
		return name + " " + oneLine(arg)
	}
	return name
}

func mainArg(params map[string]any) (string, bool) {
	for _, k := range argKeys {
		if s := str(params[k]); s != "" {
			return s, true
		}
	}
	var strs []string
	for k, v := range params {
		if s := str(v); s != "" {
			strs = append(strs, k)
		}
	}
	if len(strs) == 0 {
		return "", false
	}
	sort.Strings(strs)
	return str(params[strs[0]]), true
}

// errLine is a failed tool result: "  -> error: <first line>", or "  -> error"
// when the harness gave no message.
func errLine(msg string) string {
	if msg = oneLine(msg); msg == "" {
		return "  -> error"
	}
	return "  -> error: " + msg
}

// oneLine keeps the first line of s and at most maxArg bytes of it, cut on
// a rune boundary and marked with "...".
func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxArg {
		cut := maxArg
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
```

- [ ] **Step 5: Implement `claude.go`:**

```go
package transcript

// renderClaude is the table for `claude -p --output-format stream-json
// --verbose` (spec §4.1). One assistant event carries one message whose
// content blocks render in order; tool results arrive as user events.
func renderClaude(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "assistant":
		var out []string
		for _, blk := range contentBlocks(obj) {
			switch str(blk["type"]) {
			case "tool_use":
				out = append(out, toolLine(str(blk["name"]), asMap(blk["input"])))
			case "text":
				if t := str(blk["text"]); t != "" {
					out = append(out, t)
				}
			}
		}
		return out
	case "user":
		var out []string
		for _, blk := range contentBlocks(obj) {
			if str(blk["type"]) != "tool_result" {
				continue
			}
			if isErr, _ := blk["is_error"].(bool); isErr {
				out = append(out, errLine(resultText(blk["content"])))
			} else {
				out = append(out, "  -> ok")
			}
		}
		return out
	case "result":
		var out []string
		for _, d := range asList(obj["permission_denials"]) {
			if name := str(asMap(d)["tool_name"]); name != "" {
				out = append(out, "denied: "+name)
			}
		}
		if isErr, _ := obj["is_error"].(bool); isErr {
			out = append(out, "result: "+str(obj["subtype"]))
		}
		if r := str(obj["result"]); r != "" {
			out = append(out, r)
		}
		return out
	case "system", "rate_limit_event":
		return nil
	}
	return []string{unknown(obj)}
}

// contentBlocks is message.content as a list of objects; nil when absent.
func contentBlocks(obj map[string]any) []map[string]any {
	var out []map[string]any
	for _, b := range asList(asMap(obj["message"])["content"]) {
		if m := asMap(b); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// resultText is a tool_result's content: a string as is, or the first text
// block of a list, or "".
func resultText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	for _, b := range asList(v) {
		if t := str(asMap(b)["text"]); t != "" {
			return t
		}
	}
	return ""
}
```

Add stubs so the package compiles before Task 2 (Task 2 replaces them):

```go
// agy.go
package transcript

func renderAgy(obj map[string]any) []string { return []string{unknown(obj)} }
```

```go
// opencode.go
package transcript

func renderOpencode(obj map[string]any) []string { return []string{unknown(obj)} }
```

- [ ] **Step 6: Run** `go test -count=1 ./internal/transcript`. Expected: PASS.

- [ ] **Step 7: Mutation check.** In `renderClaude`'s `user` case, replace
`errLine(resultText(blk["content"]))` with `"  -> ok"`. Run
`go test -count=1 ./internal/transcript -run TestClaudeTable`. Expected:
FAIL on `result error with array content`. Restore; PASS. Say so in your
report.

- [ ] **Step 8: Commit**

```bash
git add internal/transcript
git commit -m "feat(transcript): render a claude stream-json line as the human lines for it (#168)"
```

---

### Task 2: the agy table and the provisional opencode table

**Files:**
- Modify: `internal/transcript/agy.go` (replace the stub)
- Modify: `internal/transcript/opencode.go` (replace the stub)
- Modify: `internal/transcript/transcript_test.go`
- Create: `internal/transcript/testdata/agy.jsonl`
- Create: `internal/transcript/testdata/agy.log`

**Interfaces:**
- Consumes: `toolLine`, `errLine`, `unknown`, `str`, `asMap`, `asList` from Task 1.
- Produces: `renderAgy`, `renderOpencode` (package-private, called by `Render`).

- [ ] **Step 1: Write the fixtures.**

`internal/transcript/testdata/agy.jsonl` (six lines, from a live
`agy -p … --output-format stream-json` run on 2026-09-16 without
`--dangerously-skip-permissions` -- the exact failure #168 was filed on:
a tool denied under headless permissions, empty `response`):

```
{"event":"init","conversation_id":"conv-1","init":{"model":"gemini-3.8-flash-high","cwd":"/repo","tools":["view_file","run_command"]}}
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":0,"state":"DONE","step_type":"user_input"}}
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":1,"state":"DONE","step_type":"agent_response","duration_seconds":6.646302301,"usage":{"input_tokens":15123,"output_tokens":475,"thinking_tokens":439,"cache_read_tokens":0,"total_tokens":15598}}}
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"view_file","tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/etc/hostname"}}}}
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":2,"state":"ERROR","step_type":"tool","tool_name":"view_file","duration_seconds":0.015961166,"tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/etc/hostname"},"error":{"type":"TOOL_ERROR","message":"permission check failed for read_file \"/etc/hostname\": user denied permission for read_file(/etc/hostname)"}}}}
{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"","duration_seconds":6.714531471,"num_turns":1,"usage":{"input_tokens":15123,"output_tokens":475,"thinking_tokens":439,"cache_read_tokens":0,"total_tokens":15598},"denied_actions":[{"action":"read_file","display_name":"ViewFile"}]}}
```

`internal/transcript/testdata/agy.log` (three lines, trailing newline):

```
view_file /etc/hostname
  -> error: permission check failed for read_file "/etc/hostname": user denied permission for read_file(/etc/hostname)
denied: read_file (ViewFile)
```

- [ ] **Step 2: Add the tests** to `transcript_test.go`:

```go
func TestAgyTable(t *testing.T) {
	cases := map[string]struct {
		line string
		want []string
	}{
		"tool active with several params": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"parameters":{"Cwd":"/repo","CommandLine":"go test ./..."}}}}`,
			[]string{"run_command go test ./..."},
		},
		"tool name falls back to tool_info.name": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/x"}}}}`,
			[]string{"view_file /x"},
		},
		"tool done": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"view_file"}}`,
			[]string{"  -> ok"},
		},
		"tool error without message": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"ERROR","tool_name":"view_file","tool_info":{"error":{"type":"TOOL_ERROR"}}}}`,
			[]string{"  -> error"},
		},
		"tool in an unknown state": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"PAUSED","tool_name":"view_file"}}`,
			[]string{"[step_update]"},
		},
		"agent_response step is noise": {
			`{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE"}}`,
			nil,
		},
		"init is noise": {`{"event":"init","init":{}}`, nil},
		"result failure with response": {
			`{"event":"result","result":{"status":"ERROR","response":"could not continue","denied_actions":[]}}`,
			[]string{"result: ERROR", "could not continue"},
		},
		"denied action without display name": {
			`{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":[{"action":"run_command"}]}}`,
			[]string{"denied: run_command"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Render("agy", []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// opencode's table is provisional (spec §1 scope boundary): only its error
// event was captured live. Everything else is rule 5 until a capture pins it.
func TestOpencodeTableProvisional(t *testing.T) {
	cases := map[string]struct {
		line string
		want []string
	}{
		"error": {
			`{"type":"error","timestamp":1789589781193,"sessionID":"ses_1","error":{"type":"provider.no-route","message":"Model unavailable: openrouter/z-ai/glm-5.3-flash"}}`,
			[]string{"  -> error: Model unavailable: openrouter/z-ai/glm-5.3-flash"},
		},
		"anything else is its type": {`{"type":"text","part":{"text":"hi"}}`, []string{"[text]"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Render("opencode", []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run to verify they fail** -- `go test -count=1 ./internal/transcript`:
`TestFixtures/agy`, `TestAgyTable` and `TestOpencodeTableProvisional/error`
FAIL (the stubs render `[step_update]`, `[result]`, `[error]`).

- [ ] **Step 4: Implement `agy.go`:**

```go
package transcript

// renderAgy is the table for `agy -p --output-format stream-json` (spec
// §4.1). agy streams tool steps but not assistant text; the only text is
// result.response at the end. A tool step in a state the table does not
// know falls to rule 5 rather than being guessed at.
func renderAgy(obj map[string]any) []string {
	switch str(obj["event"]) {
	case "step_update":
		su := asMap(obj["step_update"])
		if str(su["step_type"]) != "tool" {
			return nil
		}
		info := asMap(su["tool_info"])
		switch str(su["state"]) {
		case "ACTIVE":
			name := str(su["tool_name"])
			if name == "" {
				name = str(info["name"])
			}
			return []string{toolLine(name, asMap(info["parameters"]))}
		case "DONE":
			return []string{"  -> ok"}
		case "ERROR":
			return []string{errLine(str(asMap(info["error"])["message"]))}
		}
		return []string{unknown(obj)}
	case "result":
		r := asMap(obj["result"])
		var out []string
		for _, d := range asList(r["denied_actions"]) {
			m := asMap(d)
			action := str(m["action"])
			if action == "" {
				continue
			}
			line := "denied: " + action
			if dn := str(m["display_name"]); dn != "" {
				line += " (" + dn + ")"
			}
			out = append(out, line)
		}
		if st := str(r["status"]); st != "" && st != "SUCCESS" {
			out = append(out, "result: "+st)
		}
		if resp := str(r["response"]); resp != "" {
			out = append(out, resp)
		}
		return out
	case "init":
		return nil
	}
	return []string{unknown(obj)}
}
```

- [ ] **Step 5: Implement `opencode.go`:**

```go
package transcript

// renderOpencode is the table for `opencode run --format json`. It is
// provisional (spec §1 scope boundary): the flag is verified, the event
// shapes are not -- only an error event was captured live. Every other
// event falls to rule 5, which is noise, not silence, until a live capture
// pins the table (spec §7 step 6).
func renderOpencode(obj map[string]any) []string {
	if str(obj["type"]) == "error" {
		return []string{errLine(str(asMap(obj["error"])["message"]))}
	}
	return []string{unknown(obj)}
}
```

- [ ] **Step 6: Run** `go test -count=1 ./internal/transcript`. Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/transcript
git commit -m "feat(transcript): agy table from the #168 failure, provisional opencode table (#168)"
```

---

### Task 3: store -- `BuilderStreamPath`, `Endpoint.StreamRound`/`StreamOffset`

**Files:**
- Modify: `internal/store/store.go:146-152` (after `BuilderLogPath`)
- Modify: `internal/store/types.go:54-57` (after `LogPath`)
- Modify: `internal/store/store_test.go:589` (extend `TestBuilderLogPathIsARoundFileBesideTheReport`)
- Modify: `internal/store/types_test.go:195-225` (the headless round-trip)

**Interfaces:**
- Produces: `func (s *Store) BuilderStreamPath(name string, round int) string` -> `<binding dir>/NNN-builder.jsonl` (§3.1).
- Produces: `Endpoint.StreamRound int` (`json:"stream_round,omitempty"`), `Endpoint.StreamOffset int64` (`json:"stream_offset,omitempty"`) (§3.4).

- [ ] **Step 1: Extend the tests.**

In `TestBuilderLogPathIsARoundFileBesideTheReport` add, after the existing
assertions:

```go
	if got, want := s.BuilderStreamPath("webshop", 3), filepath.Join("/state", "webshop", "003-builder.jsonl"); got != want {
		t.Errorf("BuilderStreamPath = %q, want %q", got, want)
	}
	if r, ok := roundOfFile(filepath.Base(s.BuilderStreamPath("webshop", 12))); !ok || r != 12 {
		t.Errorf("roundOfFile(012-builder.jsonl) = %d, %v; want 12, true", r, ok)
	}
```

In the headless round-trip test in `types_test.go`, add
`StreamRound: 3, StreamOffset: 4096,` to `want` (after `LogPath`), and
extend the decoded-keys assertion:

```go
	if decoded["stream_round"] != float64(3) || decoded["stream_offset"] != float64(4096) {
		t.Errorf("stream cursor keys: %s", data)
	}
```

Also, in the same test's pane-endpoint loop that checks keys are absent,
add `"stream_round"` and `"stream_offset"` to the list of keys a pane
endpoint's JSON must not carry (find the `for _, key := range` over
`"pid"`, `"started_at"`, `"log_path"` at the top of that test and extend
it).

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1 ./internal/store`: compile error (undefined `BuilderStreamPath`, unknown fields).

- [ ] **Step 3: Implement.** In `store.go` after `BuilderLogPath`:

```go
// BuilderStreamPath is where a headless builder's raw stdout for a round --
// the harness's streamed JSON, one event per line -- and the supervisor's
// relay-exit trailer are appended (#168). A round file like the log, so
// fork copies it and gc archives it. relay renders it into the log for
// humans (relay.drainStream) and never reads it for meaning.
// Layout: <binding dir>/NNN-builder.jsonl
func (s *Store) BuilderStreamPath(name string, round int) string {
	return s.roundFile(name, round, "builder", ".jsonl")
}
```

In `types.go` after `LogPath`:

```go
	// StreamRound is the round whose builder stream (Store.BuilderStreamPath)
	// the daemon is rendering into that round's log, and StreamOffset how
	// many bytes of it are rendered (#168). They belong to the round's file,
	// not to the process or to Binding.Round: a mid-round switch keeps them,
	// clearProcess keeps them, finishRound's Round++ keeps them, and only
	// startRound on a later round moves them. 0 means no stream was started.
	StreamRound  int   `json:"stream_round,omitempty"`
	StreamOffset int64 `json:"stream_offset,omitempty"`
```

Update `BuilderLogPath`'s comment: "stdout and stderr" becomes "stderr and
the rendered stream (#168)".

- [ ] **Step 4: Run** `go test -count=1 ./internal/store`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): builder stream path and the transcript cursor on the endpoint (#168)"
```

---

### Task 4: proc -- stdout and the trailer to the stream file

**Files:**
- Modify: `internal/relay/runner.go:10-15` (`ProcSpec.StreamPath`), `:26-33` (`Runner` doc comment)
- Modify: `internal/proc/proc.go:38` (`supervisorScript`), `:70-100` (`Start`), `:150` (`ExitCode` comment)
- Modify: `internal/proc/proc_test.go`

**Interfaces:**
- Produces: `ProcSpec.StreamPath string` -- stdout and the exit trailer, appended, created if absent (§3.2).
- Contract: `Runner.ExitCode(ctx, h, path)` unchanged in shape; `path` is now the stream path (§3.2).

- [ ] **Step 1: Update the tests.** In `proc_test.go`:

Change the `start` helper to create and return both paths:

```go
func start(t *testing.T, r *Runner, argv ...string) (relay.ProcHandle, string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relay.ProcSpec{Dir: dir, Argv: argv, LogPath: log, StreamPath: stream})
	if err != nil {
		t.Fatalf("Start(%v): %v", argv, err)
	}
	return h, log, stream
}
```

Update every caller (`h, log := start(...)` becomes `h, log, stream := start(...)`
or `h, _, stream := ...` as each test needs; `go vet` will name the ones
you miss).

Rewrite `TestStartCapturesBothStreamsAndTheExitTrailer`:

```go
func TestStartCapturesBothStreamsAndTheExitTrailer(t *testing.T) {
	r := New()
	h, log, stream := start(t, r, "sh", "-c", "echo out; echo err >&2; exit 3")
	if h.PID <= 0 || h.StartedAt.IsZero() {
		t.Fatalf("handle = %+v; want a pid and a start time", h)
	}
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got, want := string(data), "out\n\n"+ExitTrailer+"3\n"; got != want {
		t.Errorf("stream = %q, want stdout, a blank line, then the trailer %q", got, want)
	}
	data, err = os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got, want := string(data), "err\n"; got != want {
		t.Errorf("log = %q, want stderr only %q", got, want)
	}
	code, ok := r.ExitCode(context.Background(), h, stream)
	if !ok || code != 3 {
		t.Errorf("ExitCode(stream) = %d, %v; want 3, true", code, ok)
	}
	if _, ok := r.ExitCode(context.Background(), h, log); ok {
		t.Error("the log carries no trailer any more; ExitCode(log) must be ok=false")
	}
}

func TestTrailerIsOnItsOwnLineAfterAPartialWrite(t *testing.T) {
	r := New()
	h, _, stream := start(t, r, "sh", "-c", "printf 'no newline'; exit 0")
	waitGone(t, r, h, 5*time.Second)
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "no newline\n"+ExitTrailer+"0\n"; got != want {
		t.Errorf("stream = %q, want %q", got, want)
	}
	if code, ok := r.ExitCode(context.Background(), h, stream); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true after a partial last line", code, ok)
	}
}
```

In `TestStartedProcessIsInItsOwnGroupAndKillReturnsWithinGrace`, the
`ExitCode` assertion at line ~148 reads the log; change it to read the
stream (`stream` from the updated helper).

In `TestStartRefusesAMissingBinaryBeforeTouchingTheLog`, pass
`StreamPath: stream` (`stream := filepath.Join(dir, "001-builder.jsonl")`)
in every `ProcSpec`, assert neither file exists after the refused Start,
and add one case: `ProcSpec{Dir: dir, Argv: []string{"sh", "-c", "true"}, LogPath: log}`
with no `StreamPath` must fail and create nothing.

`TestExitCodeReadsOnlyATrailingRelayExitLine` is path-agnostic; leave it.

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1 ./internal/proc`: compile error (unknown field `StreamPath`).

- [ ] **Step 3: Implement.** In `runner.go`:

```go
// ProcSpec is one process a headless builder round runs (#99, spec §3.4;
// #168 split the output).
type ProcSpec struct {
	Dir        string   // working directory: the binding's CWD
	Argv       []string // Argv[0] is the binary name, resolved on PATH by the runner
	Env        []string // additions to the parent environment; nil for none
	LogPath    string   // stderr, appended, created if absent
	StreamPath string   // stdout and the exit trailer, appended, created if absent
}
```

and in the `Runner` doc comment, "ExitCode reports the code the runner's
supervisor left as the log's last line" becomes "as the stream's last line
(the caller passes the stream path)".

In `proc.go`, the script:

```go
const supervisorScript = `{ echo 500 >/proc/self/oom_score_adj; } 2>/dev/null || true; "$@" </dev/null; printf '\nrelay-exit:%s\n' "$?"`
```

(`ExitTrailer` is no longer spliced in; add a comment line above the
constant: "The trailer is printed with a leading newline so a builder that
died mid-line leaves it on a line of its own; the blank line before it is
rendered as nothing (transcript rule 1). It goes to stdout -- the stream
file -- so the stream is the complete raw record and ExitCode reads one
file." Also update the `ExitTrailer` comment: "It is the only thing relay
ever reads out of a builder stream.")

In `Start`, beside the `Argv` check (so it runs before `os.Stat(spec.Dir)`
and a refused Start creates nothing):

```go
	if spec.StreamPath == "" {
		return relay.ProcHandle{}, errors.New("proc: empty stream path")
	}
```

and after the `LookPath` check:

```go
	logf, err := os.OpenFile(spec.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return relay.ProcHandle{}, fmt.Errorf("proc: log: %w", err)
	}
	defer logf.Close()
	streamf, err := os.OpenFile(spec.StreamPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return relay.ProcHandle{}, fmt.Errorf("proc: stream: %w", err)
	}
	defer streamf.Close()
```

and `cmd.Stdout = streamf`, `cmd.Stderr = logf`. Update `Start`'s doc
comment: "Checks that can fail run before either file is created".

`ExitCode`'s comment: "reads the trailer the supervisor appended, if it is
the stream's last line. The handle is unused: the stream is the record."

- [ ] **Step 4: Run** `go test -count=1 ./internal/proc`. Expected: PASS.

- [ ] **Step 5: Run** `go build ./... && go vet ./...`. Expected: `internal/relay`
compiles (nothing there sets `StreamPath` yet; the field is optional at the
type level) -- if `go vet` reports anything, stop and say so.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/runner.go internal/proc
git commit -m "feat(proc): stdout and the exit trailer go to the round's stream file (#168)"
```

---

### Task 5: `harness.Launch` -- the streaming print form

**Files:**
- Modify: `internal/harness/harness.go:270-297` (`Launch` doc comment and the three `print` slices)
- Modify: `internal/harness/harness_test.go:386-490` (`TestLaunchPrintPerKind`, `TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating`)
- Modify: `internal/relay/headless_test.go:59-95` (`TestHeadlessLaunchPerKind`)

**Interfaces:**
- Modifies `Launch.Print` per kind (§3.5). `PrintArgs` and `PromptAt` are unchanged.

- [ ] **Step 1: Update the tests.** In `TestLaunchPrintPerKind`, the five
`wantPrint` values become:

```go
// agy
[]string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
	"--output-format", "stream-json", "--print-timeout", BudgetPlaceholder}
// agy + extra
[]string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
	"--output-format", "stream-json", "--print-timeout", BudgetPlaceholder, "--dangerously-skip-permissions"}
// claude
[]string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"}
// opencode
[]string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor", "--format", "json"}
// opencode + extra
[]string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor", "--format", "json", "--auto"}
```

Add to the same test, after the table loop:

```go
	// claude refuses stream-json in print mode without --verbose (verified
	// 2026-09-16: "Error: When using --print, --output-format=stream-json
	// requires --verbose"); no candidate's extra_args should have to know.
	c, _ := Lookup("claude")
	if p := c.Launch("prov", "m/x", nil, builder).Print; !containsAdjacent(p, "--output-format", "stream-json") || !contains(p, "--verbose") {
		t.Errorf("claude print form must carry --output-format stream-json and --verbose: %v", p)
	}
	for _, kind := range []string{"agy", "claude"} {
		h, _ := Lookup(kind)
		if p := h.Launch("prov", "m/x", nil, builder).Print; contains(p, "--include-partial-messages") {
			t.Errorf("%s: partial messages are out of scope (spec §1): %v", kind, p)
		}
	}
```

with two small helpers at the bottom of the test file:

```go
func contains(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}

func containsAdjacent(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}
```

In `TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating`, `want` becomes
`{"-p", prompt, "--model", "m/x", "--agent", "plan-executor", "--output-format", "stream-json", "--print-timeout", "1h30m0s", "--dangerously-skip-permissions"}`
and the claude expectation becomes
`{"-p", "hello", "--model", "m/x", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"}`.

In `internal/relay/headless_test.go` `TestHeadlessLaunchPerKind`, the three
`want` argvs become:

```go
{testAgyRef, []string{"agy", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor",
	"--output-format", "stream-json", "--print-timeout", "2h0m0s", "--dangerously-skip-permissions"}},
{testClaudeRef, []string{"claude", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"}},
{testOpencodeRef, []string{"opencode", "run", "PROMPT", "-m", "test/m", "--agent", "plan-executor", "--format", "json"}},
```

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/harness ./internal/relay -run 'TestLaunchPrintPerKind|TestPrintArgs|TestHeadlessLaunchPerKind'`. Expected: FAIL on `Print = ...`.

- [ ] **Step 3: Implement.** In `Launch`:

```go
	case "claude":
		base = []string{"--model", model, "--agent", role.Definition}
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition,
			"--output-format", "stream-json", "--verbose"}
		promptAt = 1
	case "opencode":
		base = []string{"--agent", role.Definition, "-m", provider + "/" + model}
		print = []string{"run", PromptPlaceholder, "-m", provider + "/" + model, "--agent", role.Definition, "--format", "json"}
		promptAt = 1
	case "agy":
		base = []string{"--model", model, "--agent", role.Definition}
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition,
			"--output-format", "stream-json", "--print-timeout", BudgetPlaceholder}
		promptAt = 1
```

Update the doc comment's table to the same three lines, and add after
"agy gets the budget because…": "Every kind streams (#168, transcript spec
§3.5): one JSON event per line on stdout as the turn runs, rendered into
the builder log by the daemon. claude refuses stream-json in print mode
without --verbose. No --include-partial-messages: token deltas add nothing
a human line needs."

- [ ] **Step 4: Run** `go test -count=1 ./internal/harness ./internal/relay`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harness internal/relay/headless_test.go
git commit -m "feat(harness): headless print form streams JSON events on every kind (#168)"
```

---

### Task 6: `startRound` moves the cursor and passes the stream path; the exit path reads the trailer from it

**Files:**
- Modify: `internal/relay/headless.go:75-102` (`startRound`), `:255` (the `ExitCode` call)
- Modify: `internal/relay/headless_test.go:96-126` (`TestStartRoundRecordsTheHandleAndTheLogPath`), plus three new tests
- Modify: `internal/relay/fake_test.go:401-404` (`fakeRunner.ExitCode` records its path)

**Interfaces:**
- Modifies `startRound` (§4.4): before `Runner.Start`, `if b.Builder.StreamRound != b.Round { b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 0 }`; the spec gains `StreamPath: rt.Store.BuilderStreamPath(b.Name, b.Round)`.
- Modifies the exit path: `rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.BuilderStreamPath(b.Name, b.Round))`.

- [ ] **Step 1: Write the tests.**

Extend `TestStartRoundRecordsTheHandleAndTheLogPath`: after the
`spec.Dir`/`spec.LogPath` check add

```go
	if want := rt.Store.BuilderStreamPath("webshop", 1); spec.StreamPath != want {
		t.Errorf("spec StreamPath = %q, want %q", spec.StreamPath, want)
	}
	if got.Builder.StreamRound != 1 || got.Builder.StreamOffset != 0 {
		t.Errorf("cursor after a fresh start = round %d offset %d; want 1, 0", got.Builder.StreamRound, got.Builder.StreamOffset)
	}
```

New tests, after it:

```go
func TestStartRoundOnTheSameRoundKeepsTheCursor(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr)
	b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 512 // a switch mid-round: the file already has 512 bytes rendered
	got, err := startRound(context.Background(), rt, b, "again")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if got.Builder.StreamRound != b.Round || got.Builder.StreamOffset != 512 {
		t.Errorf("cursor = round %d offset %d; a same-round start must keep it at %d/512", got.Builder.StreamRound, got.Builder.StreamOffset, b.Round)
	}
}

func TestStartRoundOnALaterRoundMovesTheCursor(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr)
	b.Round = 2
	b.Builder.StreamRound, b.Builder.StreamOffset = 1, 512
	got, err := startRound(context.Background(), rt, b, "round two")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if got.Builder.StreamRound != 2 || got.Builder.StreamOffset != 0 {
		t.Errorf("cursor = round %d offset %d; want 2, 0", got.Builder.StreamRound, got.Builder.StreamOffset)
	}
	if fr.specs[0].StreamPath != rt.Store.BuilderStreamPath("webshop", 2) {
		t.Errorf("StreamPath = %q, want round 2's", fr.specs[0].StreamPath)
	}
}
```

The fake runner's `ExitCode` ignores its path argument today. In
`internal/relay/fake_test.go`, give `fakeRunner` a field
`exitPaths []string` and have `ExitCode` record it:

```go
func (f *fakeRunner) ExitCode(_ context.Context, h ProcHandle, path string) (int, bool) {
	f.exitPaths = append(f.exitPaths, path)
	code, ok := f.exits[h.PID]
	return code, ok
}
```

Then add, modelled on `TestReconcileHeadlessExitWithoutReportLogsAndSwitches`
(line ~672; same setup through `fr.exit(oldPID, 3)`, no log file needed):

```go
func TestReconcileHeadlessExitReadsTheTrailerFromTheStream(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)
	if _, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.exitPaths) == 0 || fr.exitPaths[0] != rt.Store.BuilderStreamPath("webshop", 1) {
		t.Errorf("ExitCode was asked about %v; want the round-1 stream %s", fr.exitPaths, rt.Store.BuilderStreamPath("webshop", 1))
	}
}
```

`headlessStatus`'s own `ExitCode` call (line ~377) stays as it is: it has
no round to hand, and `e.LogPath` is `""` between rounds anyway.

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'TestStartRound|TestReconcileHeadlessExitReadsTheTrailer'`. Expected: FAIL (`StreamPath` empty, cursor not set, `ExitCode` asked about the `.log`).

- [ ] **Step 3: Implement.** In `startRound`, replace the two lines around `Runner.Start`:

```go
	if b.Builder.StreamRound != b.Round {
		// A new round is a new stream file; a mid-round switch (same
		// round) keeps rendering the file both processes append to.
		b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 0
	}
	logPath := rt.Store.BuilderLogPath(b.Name, b.Round)
	h, err := rt.Runner.Start(ctx, ProcSpec{
		Dir: b.CWD, Argv: argv,
		LogPath:    logPath,
		StreamPath: rt.Store.BuilderStreamPath(b.Name, b.Round),
	})
```

Check the failure branch just below: it returns `b` "as it was" -- keep
the cursor move on the returned binding too (the doc comment's "endpoint
is returned as it was" refers to PID/StartedAt/LogPath; add ", cursor
moved to this round" to that sentence). A failed start followed by a
retry on the same round must not re-render.

In the exit path, change the `ExitCode` call to
`rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.BuilderStreamPath(b.Name, b.Round))`
and update the comment above it: "The exit code is read once, from the
stream's trailer".

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/headless.go internal/relay/headless_test.go internal/relay/fake_test.go
git commit -m "feat(relay): startRound owns the transcript cursor and the stream path (#168)"
```

---

### Task 7: `drainStream` -- the daemon renders the stream into the log every tick

**Files:**
- Modify: `internal/relay/headless.go` (new `drainStream`, `appendLines`; one call at the top of `reconcileHeadless` at line ~172)
- Modify: `internal/relay/headless_test.go` (new tests; `TestClearProcessKeepsIdentity` extended)

**Interfaces:**
- Consumes: `transcript.Render` (Task 1), `Store.BuilderStreamPath`, `Endpoint.StreamRound/StreamOffset` (Task 3).
- Produces: `func drainStream(rt Runtime, b store.Binding) store.Binding` (§4.2, §5); `func appendLines(path string, lines []string) error`.

- [ ] **Step 1: Write the tests.** A helper first:

```go
// streamWrite appends raw to webshop's round-1 stream file, creating it.
func streamWrite(t *testing.T, rt Runtime, raw string) {
	t.Helper()
	p := rt.Store.BuilderStreamPath("webshop", 1)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// readLog is webshop's round-1 builder log, "" when absent.
func readLog(t *testing.T, rt Runtime) string {
	t.Helper()
	data, err := os.ReadFile(rt.Store.BuilderLogPath("webshop", 1))
	if err != nil {
		return ""
	}
	return string(data)
}

const (
	agyToolActive = `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"go test ./..."}}}}` + "\n"
	agyToolDone   = `{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"run_command"}}` + "\n"
	agyResult     = `{"event":"result","result":{"status":"SUCCESS","response":"all done","denied_actions":[]}}` + "\n"
)
```

Then the tests:

```go
func TestDrainStreamRendersNewLinesInOrderAndAdvances(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr) // round 1 open, cursor at 1/0
	streamWrite(t, rt, agyToolActive+agyToolDone)

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "run_command go test ./...\n  -> ok\n"; readLog(t, rt) != want {
		t.Errorf("log = %q, want %q", readLog(t, rt), want)
	}
	if got.Builder.StreamOffset != int64(len(agyToolActive+agyToolDone)) {
		t.Errorf("offset = %d, want the whole file %d", got.Builder.StreamOffset, len(agyToolActive+agyToolDone))
	}

	// Nothing new: nothing appended.
	again, err := reconcile(t, rt, got, []herdr.Agent{plannerAgent()})
	if err != nil || readLog(t, rt) != "run_command go test ./...\n  -> ok\n" || again.Builder.StreamOffset != got.Builder.StreamOffset {
		t.Errorf("a tick with no new stream data must change nothing: log=%q offset=%d err=%v", readLog(t, rt), again.Builder.StreamOffset, err)
	}

	// More arrives: appended after, in order.
	streamWrite(t, rt, agyResult)
	got, err = reconcile(t, rt, again, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "run_command go test ./...\n  -> ok\nall done\n"; readLog(t, rt) != want {
		t.Errorf("log = %q, want %q", readLog(t, rt), want)
	}
}

func TestDrainStreamWaitsForAPartialLine(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	whole := strings.TrimSuffix(agyToolActive, "\n")
	streamWrite(t, rt, whole[:40]) // mid-event, no newline yet

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "" || got.Builder.StreamOffset != 0 {
		t.Errorf("a partial line must not be rendered or consumed: log=%q offset=%d", readLog(t, rt), got.Builder.StreamOffset)
	}
	streamWrite(t, rt, whole[40:]+"\n")
	got, err = reconcile(t, rt, got, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "run_command go test ./...\n" || got.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Errorf("completed line: log=%q offset=%d", readLog(t, rt), got.Builder.StreamOffset)
	}
}

func TestDrainStreamCursorSurvivesAReload(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	streamWrite(t, rt, agyToolActive)
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.Save(got) }); err != nil {
		t.Fatal(err)
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Builder.StreamRound != 1 || loaded.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Fatalf("cursor after reload = %d/%d", loaded.Builder.StreamRound, loaded.Builder.StreamOffset)
	}
	// A daemon restarted from that state renders nothing twice.
	if _, err := reconcile(t, rt, loaded, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatal(err)
	}
	if readLog(t, rt) != "run_command go test ./...\n" {
		t.Errorf("log after reload tick = %q; the line was rendered twice", readLog(t, rt))
	}
}

func TestDrainStreamCursorPastEndRendersFromTheStart(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	streamWrite(t, rt, agyToolActive)
	b.Builder.StreamOffset = 10_000 // a state file rewritten by hand
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "run_command go test ./...\n" || got.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Errorf("log=%q offset=%d; want rendered from 0 and the cursor at EOF", readLog(t, rt), got.Builder.StreamOffset)
	}
}

func TestDrainStreamNoiseAdvancesWithoutWriting(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, &fakeHerdr{}, fr)
	noise := `{"event":"init","init":{}}` + "\n"
	streamWrite(t, rt, noise)
	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, statErr := os.Stat(rt.Store.BuilderLogPath("webshop", 1)); statErr == nil {
		t.Error("all-noise input must not create the log")
	}
	if got.Builder.StreamOffset != int64(len(noise)) {
		t.Errorf("offset = %d, want %d", got.Builder.StreamOffset, len(noise))
	}
}

func TestReconcileHeadlessExitEntryCarriesTheRenderedResult(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	// stderr went straight to the log; stdout and the trailer to the stream,
	// all before this tick (the process is gone).
	if err := os.MkdirAll(filepath.Dir(b.Builder.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Builder.LogPath, []byte("jetski: starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	streamWrite(t, rt, agyToolActive+agyToolDone+agyResult+"\nrelay-exit:0\n")

	if _, err := reconcile(t, rt, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("exit entries = %d, want 1", len(ex))
	}
	want := "jetski: starting\nrun_command go test ./...\n  -> ok\nall done\nrelay-exit:0"
	if ex[0].Payload != want {
		t.Errorf("payload = %q, want the drained log %q", ex[0].Payload, want)
	}
	if !strings.Contains(ex[0].Note, "(code 0)") {
		t.Errorf("note = %q; the code must still come from the trailer", ex[0].Note)
	}
}

func TestDrainStreamKeepsGoingAfterAMarkerClose(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	streamWrite(t, rt, agyToolActive)

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 {
		t.Fatalf("round=%d pid=%d; want the marker to close round 1", got.Round, got.Builder.PID)
	}
	if got.Builder.StreamRound != 1 {
		t.Fatalf("StreamRound = %d after the close; the cursor must stay on round 1's file", got.Builder.StreamRound)
	}
	// The builder flushes its result after relay saw the marker.
	streamWrite(t, rt, agyResult+"\nrelay-exit:0\n")
	if _, err := reconcile(t, rt, got, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "run_command go test ./...\nall done\nrelay-exit:0\n"; readLog(t, rt) != want {
		t.Errorf("round 1 log after the close = %q, want %q", readLog(t, rt), want)
	}
}

func TestDrainStreamIsANoopBeforeAnyRound(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedHeadless(t, &fakeHerdr{}, fr) // bound, never sent: StreamRound 0
	got := drainStream(rt, b)
	if got != b {
		t.Errorf("drainStream changed a binding with no stream: %+v", got.Builder)
	}
}
```

Extend `TestClearProcessKeepsIdentity`: give `e` `StreamRound: 3, StreamOffset: 99`
and put the same two fields in `want` -- `clearProcess` must keep them.

Check the existing `TestReconcileHeadlessIdleIsNotBroken` still passes as
written (it asserts nothing happens on an idle tick; `StreamRound` is 0
there).

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'TestDrainStream|TestReconcileHeadlessExitEntryCarries|TestClearProcess'`. Expected: compile error (undefined `drainStream`), then after stubbing, FAIL.

- [ ] **Step 3: Implement.** In `headless.go`, directly before `logTail`:

```go
// drainStream brings the round's builder log up to date with its stream
// (transcript spec §4.2): every complete line of the stream file past the
// endpoint's cursor is rendered with transcript.Render and appended to the
// log in one write, and the cursor moves past the last newline consumed.
// A trailing partial line waits for the next tick. The cursor is keyed on
// StreamRound, not b.Round, so a round that closed on its marker while the
// builder was still flushing keeps draining until the next round starts.
//
// It never fails the tick: every problem is a slog.Warn and an unchanged
// binding, and the cursor advances only after the append succeeded, so a
// failed write renders the same lines again next tick rather than dropping
// them. Nothing here is read for meaning (headless spec §1).
func drainStream(rt Runtime, b store.Binding) store.Binding {
	round := b.Builder.StreamRound
	if round == 0 {
		return b
	}
	streamPath := rt.Store.BuilderStreamPath(b.Name, round)
	info, err := os.Stat(streamPath)
	if err != nil {
		return b // not started yet, or gone with the round: nothing to drain
	}
	off := b.Builder.StreamOffset
	if off > info.Size() {
		slog.Warn("builder transcript: cursor past end of stream; rendering from the start",
			"binding", b.Name, "round", round, "offset", off, "size", info.Size())
		off = 0
	}
	if off == info.Size() {
		return b
	}
	data, err := readFrom(streamPath, off)
	if err != nil {
		slog.Warn("builder transcript", "binding", b.Name, "round", round, "err", err)
		return b
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return b
	}
	var out []string
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		out = append(out, transcript.Render(b.Builder.Kind, line)...)
	}
	if len(out) > 0 {
		if err := appendLines(rt.Store.BuilderLogPath(b.Name, round), out); err != nil {
			slog.Warn("builder transcript", "binding", b.Name, "round", round, "err", err)
			return b
		}
	}
	b.Builder.StreamOffset = off + int64(end) + 1
	return b
}

// readFrom is the file's bytes from off to its end.
func readFrom(path string, off int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// appendLines appends lines, each newline-terminated, to the file at path
// in one write, the same discipline as appendLogMarker: O_APPEND writes of
// one buffer interleave with the supervisor's stderr at line boundaries.
func appendLines(path string, lines []string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	return err
}
```

Add `"bytes"`, `"io"` and `"github.com/fuad-daoud/relay/internal/transcript"`
to the imports.

In `reconcileHeadless`, after the round-cap check and before
`entries, err := tx.ReadLog(b.Name)`:

```go
	// Render what the builder has streamed since the last tick before
	// anything below reads the log: the exit entry's tail, the limit scan
	// and the status snippet all see a log that is current (transcript
	// spec §4.3).
	b = drainStream(rt, b)
```

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay`. Expected: PASS, every pre-existing test unedited except `TestClearProcessKeepsIdentity`.

- [ ] **Step 5: Mutation check (two).** (a) Move the `drainStream` call to
just after `closeOnMarker` returns (inside the round-open branch). Run
`go test -count=1 ./internal/relay -run 'TestDrainStreamKeepsGoingAfterAMarkerClose|TestReconcileHeadlessExitEntryCarries'`.
Expected: both FAIL -- the marker tick returns before the moved call, and
the between-rounds tick never reaches it. Restore; PASS. (b) In `drainStream`, advance the cursor
before `appendLines` instead of after, and make `appendLines` return an
error by pointing the log at a directory: add a one-off test
`TestDrainStreamDoesNotAdvanceOnAWriteFailure` that creates
`rt.Store.BuilderLogPath("webshop", 1)` as a directory
(`os.MkdirAll`), writes `agyToolActive` to the stream, ticks, and asserts
`StreamOffset == 0`. With the mutation it FAILS; restored it PASSES. Keep
that test. Say both results in your report.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/headless.go internal/relay/headless_test.go
git commit -m "feat(relay): the daemon renders a headless builder's stream into its log every tick (#168)"
```

---

### Task 8: spec amendments and README

**Files:**
- Modify: `docs/specs/2026-09-12-headless-builders-design.md:24`, `:62-63`, `:94`, `:123-127`, `:140-152`, `:159-175`, `:215-218`, `:246`, `:305`
- Modify: `README.md:317-330` ("Headless builders")
- Modify: `docs/specs/2026-09-17-headless-transcript-design.md:12` (Status line)

- [ ] **Step 1: Amend the headless spec.** Each edit is a sentence, not a rewrite:

- line 24: "stdout and stderr go to a log file beside the round's plan and
  report" -> "stderr goes to a log file beside the round's plan and report
  and stdout, streamed as JSON events, to a stream file the daemon renders
  into that log (amended by `2026-09-17-headless-transcript-design.md`)".
- lines 62-63: delete the bullet "No live log streaming. `relay ui` polls
  the log file the way it polls a pane today." and put in its place:
  "Live log: struck by `2026-09-17-headless-transcript-design.md`; the
  daemon renders the stream into the log each tick."
- line 94 (file structure, `types.go`): append `, StreamRound, StreamOffset (#168)`.
- lines 123-127 (§3.2 endpoint fields): after `LogPath` add
  `StreamRound int / StreamOffset int64  the transcript cursor; see the 2026-09-17 spec §3.4`.
- §3.3 (line 140): add a second sentence: "`BuilderStreamPath(name, round)`
  is `NNN-builder.jsonl` beside it: raw stdout plus the exit trailer
  (2026-09-17 spec §3.1)."
- §3.4 (line 152): `LogPath string  stderr, appended, created if absent` and
  a new line `StreamPath string  stdout and the exit trailer, appended, created if absent`.
- §3.5 table (lines 159-175): replace the three Print rows with the ones in
  Task 5 and add the sentence: "Every kind streams (2026-09-17 spec §3.5);
  claude needs `--verbose` for it."
- lines 215-218 (`Start` contract): "stdout+stderr appended to LogPath and
  … appends one final line `relay-exit:<code>` to LogPath" -> "stderr
  appended to LogPath, stdout to StreamPath, and, when Argv exits, one
  final line `relay-exit:<code>` to StreamPath".
- line 246: append `, StreamPath=BuilderStreamPath(name, round)`.
- line 305: `Runner.ExitCode(handle, LogPath)` -> `Runner.ExitCode(handle, BuilderStreamPath(name, round))`.

- [ ] **Step 2: README "Headless builders".** Replace the sentence
"appends its stdout and stderr to `~/.local/state/relay/<name>/NNN-builder.log`
beside the round's plan and report, and returns." with:

"writes the harness's streamed JSON events to
`~/.local/state/relay/<name>/NNN-builder.jsonl` and its stderr to
`NNN-builder.log`, both beside the round's plan and report, and returns.
The daemon renders the stream into the `.log` as it grows -- one line per
tool call (`Bash go test ./...`), its result (`  -> ok`, `  -> error: …`),
the builder's text, any denied permission, and the final answer -- so
`relay ui`'s terminal tab, `relay status` and `tail -f` on the `.log` show
the round live, about two seconds behind. The `.jsonl` is the raw record;
relay never reads it for meaning."

- [ ] **Step 3: Transcript spec status.** In
`docs/specs/2026-09-17-headless-transcript-design.md` line 12, "draft" ->
"implemented by this plan; opencode table provisional until step 6 runs".

- [ ] **Step 4: Run** `test -z "$(gofmt -l .)"; go vet ./...; go test -race -count=1 ./...`. Expected: all green (docs only, but the whole suite runs once here).

- [ ] **Step 5: Commit**

```bash
git add docs/specs/2026-09-12-headless-builders-design.md docs/specs/2026-09-17-headless-transcript-design.md README.md
git commit -m "docs: headless spec and README describe the streamed transcript (#168)"
```

---

### Task 9: opencode capture -- conditional; report it open if it cannot run

**Files:**
- Create: `internal/transcript/testdata/opencode.jsonl`, `internal/transcript/testdata/opencode.log`
- Modify: `internal/transcript/opencode.go`, `internal/transcript/transcript_test.go`

This task needs a live `opencode run … --format json` against a provider
that answers. **Do not run `opencode` yourself** (Running commands). Check
whether the planner attached a capture to the plan's drop directory as
`~/.local/state/relay/headless-transcript/opencode-capture.jsonl`. If it is
not there, **skip this task, list it as open in your report, and stop
after Task 8** -- do not invent event shapes.

If the capture is there:

- [ ] **Step 1:** copy it to `internal/transcript/testdata/opencode.jsonl`,
trimming ids and timestamps to fixed values (`"ses_1"`, `1789589781193`)
and keeping one event of every `type` it contains.

- [ ] **Step 2:** write `testdata/opencode.log` by hand from the spec's
vocabulary (§4.1): a tool call event renders `<name> <arg>` with the arg
picked by the shared rule, a tool result `  -> ok` / `  -> error: …`, a
text event verbatim, step boundaries as noise, the error event as already
implemented. Where the capture shows a shape the vocabulary cannot express,
stop and say so.

- [ ] **Step 3:** run `go test -count=1 ./internal/transcript -run TestFixtures/opencode`
-- FAIL (rule 5 renders `[type]`).

- [ ] **Step 4:** extend `renderOpencode` with the cases the capture shows,
each a `case` on `str(obj["type"])`, using only `toolLine`, `errLine`,
`str`, `asMap`, `asList`; replace `TestOpencodeTableProvisional`'s
"anything else is its type" case with a real unknown type, and drop the
word "provisional" from the test name and from the comment on
`renderOpencode`.

- [ ] **Step 5:** run `go test -count=1 ./internal/transcript`. PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/transcript
git commit -m "feat(transcript): opencode table pinned to a live capture (#168)"
```

---

## Report

Say, per task: the commit hash; the four `make check` constituents' results
verbatim (the last lines of each); every mutation check's before/after;
anything you did not do and why (Task 9 in particular). List every file
you touched outside the plan's `Files` lists, if any -- there should be
none.
