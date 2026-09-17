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
		"priority key wins over others": {map[string]any{"description": "d", "command": "c"}, "Bash c"},
		"single string param":           {map[string]any{"AbsolutePathX": "/x"}, "Bash /x"},
		"first in sorted order":         {map[string]any{"zeta": "z", "alpha": "a", "n": 3}, "Bash a"},
		"non-string only":               {map[string]any{"n": 3, "ok": true}, "Bash"},
		"empty string is not an arg":    {map[string]any{"command": "", "b": "x"}, "Bash x"},
		"nil params":                    {nil, "Bash"},
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
