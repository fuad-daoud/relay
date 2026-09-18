package usage

import (
	"context"
	"os"
	"path/filepath"
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
