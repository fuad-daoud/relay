package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadHeadlessDispatch(t *testing.T) {
	dir := t.TempDir()
	r := New(nil, dir)
	cases := []struct {
		harness, fixture string
		samples          int
	}{
		{"claude", "testdata/claude-stream.jsonl", 1},
		{"agy", "testdata/agy-stream.jsonl", 1},
		{"opencode", "testdata/opencode-stream.jsonl", 2},
	}
	for _, c := range cases {
		path := filepath.Join(dir, c.harness+".jsonl")
		copyFixture(t, c.fixture, path)
		got, note := r.Read(context.Background(), Source{Harness: c.harness, Mode: ModeHeadless, Provider: "p", Model: "m", StreamPath: path})
		if note != "" || len(got) != c.samples {
			t.Errorf("%s: %d samples, note %q; want %d", c.harness, len(got), note, c.samples)
		}
	}
}

func TestReadHeadlessNotes(t *testing.T) {
	r := New(nil, t.TempDir())
	if _, note := r.Read(context.Background(), Source{Harness: "claude", Mode: ModeHeadless, StreamPath: "/nonexistent"}); note != "no stream" {
		t.Errorf("missing stream: note = %q", note)
	}
	_, empty := mustTemp(t, "relay-exit:0\n")
	if _, note := r.Read(context.Background(), Source{Harness: "claude", Mode: ModeHeadless, StreamPath: empty}); note != "no usage events" {
		t.Errorf("no events: note = %q", note)
	}
	if _, note := r.Read(context.Background(), Source{Harness: "unknown-harness", Mode: ModeHeadless, StreamPath: empty}); note != "no reader for unknown-harness" {
		t.Errorf("unknown harness: note = %q", note)
	}
}

func TestReadPaneClaudeUsesProjectSlug(t *testing.T) {
	home := t.TempDir()
	slug := filepath.Join(home, ".claude", "projects", ProjectSlug("/wt"))
	copyFixture(t, "testdata/claude-project/sess-a.jsonl", filepath.Join(slug, "sess-a.jsonl"))
	copyFixture(t, "testdata/claude-project/sess-a/subagents/agent-x.jsonl", filepath.Join(slug, "sess-a", "subagents", "agent-x.jsonl"))
	start := time.Date(2026, 9, 18, 7, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	got, note := New(nil, home).Read(context.Background(), Source{Harness: "claude", Mode: ModePane, Provider: "anthropic", Worktree: "/wt", Start: start, End: end})
	if note != "" || len(got) != 3 {
		t.Errorf("%d samples, note %q; want 3", len(got), note)
	}
	if _, note := New(nil, home).Read(context.Background(), Source{Harness: "claude", Mode: ModePane, Worktree: "/never-seen", Start: start, End: end}); note != "no claude project dir" {
		t.Errorf("missing slug dir: note = %q", note)
	}
}

func TestReadPaneNotes(t *testing.T) {
	r := New(nil, t.TempDir())
	if _, note := r.Read(context.Background(), Source{Harness: "agy", Mode: ModePane, Worktree: "/wt"}); note != "agy keeps no usage record" {
		t.Errorf("agy pane: note = %q", note)
	}
	if _, note := r.Read(context.Background(), Source{Harness: "claude", Mode: ModePane, Worktree: ""}); note != "shared cwd" {
		t.Errorf("--cwd binding: note = %q", note)
	}
	if _, note := r.Read(context.Background(), Source{Harness: "opencode", Mode: ModePane, Worktree: "/wt"}); note != "sqlite3 not on PATH" {
		t.Errorf("opencode pane, nil exec: note = %q", note)
	}
}

func TestReadPaneOpencodeUsesHomeStore(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	copyFixture(t, "testdata/opencode-db.json", db) // any existing file; the fake never opens it
	raw, _ := os.ReadFile("testdata/opencode-db.json")
	fe := &fakeExec{out: raw}
	got, note := New(fe, home).Read(context.Background(), Source{Harness: "opencode", Mode: ModePane, Worktree: "/wt", Start: time.UnixMilli(0), End: time.UnixMilli(1)})
	if note != "" || len(got) != 2 {
		t.Errorf("%d samples, note %q", len(got), note)
	}
	if len(fe.args) < 3 || fe.args[2] != db {
		t.Errorf("args = %v, want the store under home", fe.args)
	}
}

func TestStreamClosed(t *testing.T) {
	_, open := mustTemp(t, "{\"type\":\"assistant\"}\n")
	if streamClosed(open) {
		t.Error("no trailer: must be open")
	}
	_, closed := mustTemp(t, "{\"type\":\"result\"}\n\nrelay-exit:0\n")
	if !streamClosed(closed) {
		t.Error("trailer as last line: must be closed")
	}
	_, mid := mustTemp(t, "relay-exit:0\n{\"type\":\"assistant\"}\n")
	if streamClosed(mid) {
		t.Error("trailer not last: must be open")
	}
	if streamClosed("/nonexistent") {
		t.Error("missing file is not closed")
	}
}

func TestReadHeadlessWaitsForTrailer(t *testing.T) {
	// Start with everything but the result event and the trailer, append
	// them 300 ms later, and expect the result-based sample.
	raw, err := os.ReadFile("testdata/claude-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	head := strings.Join(lines[:len(lines)-2], "\n") + "\n"
	tail := strings.Join(lines[len(lines)-2:], "\n") + "\n"
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString(tail)
		f.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, note := New(nil, t.TempDir()).Read(ctx, Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path})
	if note != "" || len(got) != 1 || !got[0].HasCost {
		t.Fatalf("want the measured result sample after the trailer lands; got %d samples, note %q, %+v", len(got), note, got)
	}
}

func TestReadHeadlessTimesOutOnOpenStream(t *testing.T) {
	raw, _ := os.ReadFile("testdata/claude-stream-killed.jsonl")
	// The killed fixture ends in a trailer; strip it to make an open stream.
	body := strings.TrimSuffix(strings.TrimRight(string(raw), "\n"), "relay-exit:137")
	_, path := mustTemp(t, body)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	got, note := New(nil, t.TempDir()).Read(ctx, Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path})
	if note != "stream still open" {
		t.Errorf("note = %q, want \"stream still open\"", note)
	}
	if len(got) == 0 {
		t.Error("an open stream is still read: the fallback samples must come back with the note")
	}
}
