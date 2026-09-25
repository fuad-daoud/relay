package chatlabel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// readFixture reads one synthetic transcript from testdata. Fixtures are
// hand-written, never copied from a real session.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func TestClaudeLatestPromptAndLink(t *testing.T) {
	label := Claude(readFixture(t, "claude-prompt.jsonl"))

	wantText := "\"The quick brown fox jumps over the lazy dog again…\""
	if label.Text != wantText {
		t.Errorf("Text = %q, want %q", label.Text, wantText)
	}
	if got := utf8.RuneCountInString(strings.Trim(label.Text, "\"")); got != MaxTextRunes {
		t.Errorf("prompt text is %d runes, want the truncation cap %d", got, MaxTextRunes)
	}
	if want := "https://claude.ai/code/session_01TESTTESTTESTTEST"; label.Link != want {
		t.Errorf("Link = %q, want %q", label.Link, want)
	}
}

func TestClaudeCustomTitleWins(t *testing.T) {
	label := Claude(readFixture(t, "claude-titled.jsonl"))
	if want := "new name"; label.Text != want {
		t.Errorf("Text = %q, want %q", label.Text, want)
	}
	if label.Link != "" {
		t.Errorf("Link = %q, want empty when no bridge-session line is present", label.Link)
	}
}

func TestClaudeAITitleBeatsPrompt(t *testing.T) {
	label := Claude(readFixture(t, "claude-aititle.jsonl"))
	if want := "auto title"; label.Text != want {
		t.Errorf("Text = %q, want %q", label.Text, want)
	}
}

func TestClaudeNothing(t *testing.T) {
	label := Claude(readFixture(t, "claude-none.jsonl"))
	if label != (Label{}) {
		t.Errorf("Label = %+v, want the zero Label", label)
	}
	if got := label.String(); got != "-" {
		t.Errorf("String() = %q, want %q", got, "-")
	}
}

func TestClaudeGarbledLinesSkipped(t *testing.T) {
	label := Claude(readFixture(t, "claude-garbled.jsonl"))
	if want := "\"survivor prompt\""; label.Text != want {
		t.Errorf("Text = %q, want %q", label.Text, want)
	}
}

func TestClaudeBridgeWithoutPrefix(t *testing.T) {
	label := Claude([]byte("{\"type\":\"bridge-session\",\"bridgeSessionId\":\"ses_01TEST\"}\n"))
	if label.Link != "" {
		t.Errorf("Link = %q, want empty for an id without the cse_ prefix", label.Link)
	}
}

func TestReadTail(t *testing.T) {
	dir := t.TempDir()
	content := "aaaa\nbbbb\ncccc\n"

	big := filepath.Join(dir, "big.jsonl")
	if err := os.WriteFile(big, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", big, err)
	}

	// The window opens mid-line: everything up to the first newline goes, so
	// the bytes returned start at a line boundary.
	got, err := ReadTail(big, 8)
	if err != nil {
		t.Fatalf("ReadTail(max 8): %v", err)
	}
	if want := "cccc\n"; string(got) != want {
		t.Errorf("ReadTail(max 8) = %q, want %q", got, want)
	}

	small := filepath.Join(dir, "small.jsonl")
	if err := os.WriteFile(small, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", small, err)
	}
	got, err = ReadTail(small, 64)
	if err != nil {
		t.Fatalf("ReadTail(max 64): %v", err)
	}
	if string(got) != content {
		t.Errorf("ReadTail(max 64) = %q, want the whole file", got)
	}

	// max <= 0 means DefaultTailBytes, which is larger than this file.
	got, err = ReadTail(small, 0)
	if err != nil {
		t.Fatalf("ReadTail(max 0): %v", err)
	}
	if string(got) != content {
		t.Errorf("ReadTail(max 0) = %q, want the whole file", got)
	}

	if _, err := ReadTail(filepath.Join(dir, "missing.jsonl"), 64); err == nil {
		t.Error("ReadTail on a missing path must return an error")
	}
}

func TestLabelString(t *testing.T) {
	tests := []struct {
		name string
		l    Label
		want string
	}{
		{"nothing", Label{}, "-"},
		{"text only", Label{Text: "my chat"}, "my chat"},
		{"link only", Label{Link: "https://claude.ai/code/session_01TEST"}, "https://claude.ai/code/session_01TEST"},
		{"both", Label{Text: "my chat", Link: "https://claude.ai/code/session_01TEST"}, "my chat · https://claude.ai/code/session_01TEST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpencode(t *testing.T) {
	if got := Opencode([]byte("My session\n")); got.Text != "My session" {
		t.Errorf("Text = %q, want %q", got.Text, "My session")
	}
	if got := Opencode(nil); got != (Label{}) {
		t.Errorf("empty output gave %+v, want the zero Label", got)
	}
	if got, want := OpencodeQuery("ses_a'b"), "select title from session_v2 where id = 'ses_a''b' union all select title from session where id = 'ses_a''b' and not exists (select 1 from session_v2 where id = 'ses_a''b')"; got != want {
		t.Errorf("OpencodeQuery = %q, want %q", got, want)
	}
}

// fakeExec records every invocation and answers with fixed output, so a test
// can prove what a Resolver asked sqlite3 for.
type fakeExec struct {
	calls [][]string
	out   []byte
	err   error
}

func (f *fakeExec) Run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{bin}, args...))
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func TestResolveOpencode(t *testing.T) {
	fake := &fakeExec{out: []byte("Planner round 2\n")}
	res := Resolver{Exec: fake, OpencodeDB: "/tmp/opencode.db"}

	label := res.Resolve(context.Background(), "opencode", "ses_abc123", "")
	if label.Text != "Planner round 2" {
		t.Errorf("Text = %q, want %q", label.Text, "Planner round 2")
	}
	if label.Link != "" {
		t.Errorf("Link = %q, want empty", label.Link)
	}
	wantArgs := []string{"sqlite3", "-readonly", "/tmp/opencode.db", OpencodeQuery("ses_abc123")}
	if len(fake.calls) != 1 {
		t.Fatalf("Exec ran %d times, want 1", len(fake.calls))
	}
	if !slices.Equal(fake.calls[0], wantArgs) {
		t.Errorf("Exec ran %q, want %q", fake.calls[0], wantArgs)
	}

	// An invalid session id never reaches sqlite3.
	if got := res.Resolve(context.Background(), "opencode", "bad id", ""); got != (Label{}) {
		t.Errorf("invalid session id gave %+v, want the zero Label", got)
	}
	if len(fake.calls) != 1 {
		t.Errorf("invalid session id reached Exec: %q", fake.calls[1:])
	}

	// A nil Exec means sqlite3 is not available.
	noExec := Resolver{OpencodeDB: "/tmp/opencode.db"}
	if got := noExec.Resolve(context.Background(), "opencode", "ses_abc123", ""); got != (Label{}) {
		t.Errorf("nil Exec gave %+v, want the zero Label", got)
	}
}

// cliExec is usage.Exec over the real sqlite3 binary, so a test can read a
// fixture database the way the daemon does.
type cliExec struct{}

func (cliExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).Output()
}

// opencodeFixture builds a fixture opencode.db with the sqlite3 binary: no
// test may touch the real ~/.local/share/opencode/opencode.db.
func opencodeFixture(t *testing.T, stmts ...string) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skipf("sqlite3 not on PATH: %v", err)
	}
	path := filepath.Join(t.TempDir(), "opencode.db")
	for _, stmt := range stmts {
		out, err := exec.Command("sqlite3", path, stmt).CombinedOutput()
		if err != nil {
			t.Fatalf("sqlite3 %q: %v: %s", stmt, err, out)
		}
	}
	return path
}

// TestResolveOpencodeV2Title proves the title is read from session_v2 first
// (#393): OpenCode 2.0.14 keeps its sessions there, and the legacy session
// table is only the fallback for pre-2.0 rows.
func TestResolveOpencodeV2Title(t *testing.T) {
	db := opencodeFixture(t,
		"create table session (id text, title text)",
		"create table session_v2 (id text, title text)",
		"insert into session values ('ses_both', 'Legacy title')",
		"insert into session values ('ses_legacyonly', 'Legacy-only title')",
		"insert into session_v2 values ('ses_both', 'V2 title')",
	)
	res := Resolver{Exec: cliExec{}, OpencodeDB: db}

	if got := res.Resolve(context.Background(), "opencode", "ses_both", ""); got.Text != "V2 title" {
		t.Errorf("session_v2 title = %q, want %q", got.Text, "V2 title")
	}
	if got := res.Resolve(context.Background(), "opencode", "ses_legacyonly", ""); got.Text != "Legacy-only title" {
		t.Errorf("legacy title = %q, want %q", got.Text, "Legacy-only title")
	}
}

func TestResolveOther(t *testing.T) {
	res := Resolver{}
	if got := res.Resolve(context.Background(), "agy", "x", "/some/path"); got != (Label{}) {
		t.Errorf("agy gave %+v, want the zero Label", got)
	}
	if got := res.Resolve(context.Background(), "claude", "x", "/does/not/exist"); got != (Label{}) {
		t.Errorf("a missing claude transcript gave %+v, want the zero Label", got)
	}
}
