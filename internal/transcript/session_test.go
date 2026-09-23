package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// streamFixture is line n (1-based) of a usage stream fixture, without its
// trailing newline.
func streamFixture(t *testing.T, name string, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "usage", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if n > len(lines) {
		t.Fatalf("fixture %s has %d lines; want line %d", name, len(lines), n)
	}
	return lines[n-1]
}

func TestSessionID(t *testing.T) {
	cases := []struct {
		name string
		kind string
		line string
		want string
	}{
		{"claude init line", "claude", streamFixture(t, "claude-stream.jsonl", 1), "sess-1"},
		{"claude assistant line", "claude", streamFixture(t, "claude-stream.jsonl", 2), "sess-1"},
		{"opencode step_start line", "opencode", streamFixture(t, "opencode-stream.jsonl", 1), "ses_1"},
		{"agy init line", "agy", streamFixture(t, "agy-stream.jsonl", 1), "conv-1"},
		{"agy step_update nested conversation_id", "agy", streamFixture(t, "agy-stream.jsonl", 2), "conv-1"},
		{"agy init without a top-level conversation_id", "agy", `{"event":"init","init":{"conversation_id":"conv-9"}}`, "conv-9"},
		{"agy step_update without a nested conversation_id", "agy", `{"event":"step_update","step_update":{"step_index":0}}`, ""},
		{"codex thread.started line", "codex", streamFixture(t, "codex-stream.jsonl", 1), "01a0bb3b-6da3-79d1-a85d-9ea76187d710"},
		{"codex turn.started line", "codex", streamFixture(t, "codex-stream.jsonl", 2), ""},
		{"relevo-exit trailer", "claude", "relevo-exit:0", ""},
		{"unknown kind", "mystery", streamFixture(t, "claude-stream.jsonl", 1), ""},
		{"empty line", "claude", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SessionID(tc.kind, []byte(tc.line)); got != tc.want {
				t.Errorf("SessionID(%q, %q) = %q, want %q", tc.kind, tc.line, got, tc.want)
			}
		})
	}
}
