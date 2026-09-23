package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFinalTextPerKind pins FinalText against one captured stream per
// harness. Each expectation is read from the fixture: claude's last
// assistant text block is "done", opencode's line-5 text event is "done",
// codex's last agent_message text is "LUNA-RESEARCHER PONG", and agy's
// fixture has neither a result response nor a text step, so its expectation
// is "".
func TestFinalTextPerKind(t *testing.T) {
	cases := []struct {
		kind string
		file string
		want string
	}{
		{"claude", "claude.jsonl", "done"},
		{"opencode", "opencode.jsonl", "done"},
		{"codex", "codex.jsonl", "LUNA-RESEARCHER PONG"},
		{"agy", "agy.jsonl", ""},
	}
	for _, tc := range cases {
		stream, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatalf("%s: read fixture: %v", tc.kind, err)
		}
		if got := FinalText(tc.kind, stream); got != tc.want {
			t.Errorf("FinalText(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// TestFinalTextAgySynthetic covers the two agy non-empty sources the fixture
// does not carry: a result.response, and -- when that is empty -- a
// step_update text.
func TestFinalTextAgySynthetic(t *testing.T) {
	withResponse := `{"event":"result","result":{"status":"SUCCESS","response":"AGY FINDINGS","denied_actions":[]}}` + "\n"
	if got := FinalText("agy", []byte(withResponse)); got != "AGY FINDINGS" {
		t.Errorf("result.response: got %q, want %q", got, "AGY FINDINGS")
	}

	emptyResponseThenStep := `{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":[]}}` + "\n" +
		`{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE","text":"STEP FINDINGS"}}` + "\n"
	if got := FinalText("agy", []byte(emptyResponseThenStep)); got != "STEP FINDINGS" {
		t.Errorf("step_update fallback: got %q, want %q", got, "STEP FINDINGS")
	}
}

// TestFinalTextClaudeFallsBackToResult covers claude's second rule: no
// assistant text block anywhere, so the result event's result string stands.
func TestFinalTextClaudeFallsBackToResult(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"","signature":"sig"}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"RESULT STRING"}` + "\n"
	if got := FinalText("claude", []byte(stream)); got != "RESULT STRING" {
		t.Errorf("got %q, want %q", got, "RESULT STRING")
	}
}

// TestFinalTextStreamOfOnlyTheTrailer is nothing: the relevo-exit trailer is
// not a JSON object and the stream carries no assistant message.
func TestFinalTextStreamOfOnlyTheTrailer(t *testing.T) {
	for _, kind := range []string{"claude", "opencode", "agy", "codex"} {
		if got := FinalText(kind, []byte("relevo-exit:0\n")); got != "" {
			t.Errorf("FinalText(%q, trailer-only) = %q, want \"\"", kind, got)
		}
	}
}

// TestFinalTextTrimsAndIgnoresNonJSON keeps the contract narrow: surrounding
// whitespace is trimmed and a non-JSON line never contributes text.
func TestFinalTextTrimsAndIgnoresNonJSON(t *testing.T) {
	stream := "not json at all\n" +
		`{"type":"text","part":{"text":"  PADDED  "}}` + "\n"
	if got := FinalText("opencode", []byte(stream)); got != "PADDED" {
		t.Errorf("got %q, want %q", got, "PADDED")
	}
}
