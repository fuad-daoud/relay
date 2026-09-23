package migrate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	testOldRoot = "/o/r"
	testNewRoot = "/n/r"
)

// TestRewriteJSON covers the rewrite contract: only JSON files change, only
// values that start with the old root change, the skipped subtrees are skipped,
// and an untouched file keeps its mtime.
func TestRewriteJSON(t *testing.T) {
	root := t.TempDir()

	bindPath := writeFixture(t, root, "bind.json", `{"cwd":"/o/r/.worktrees/x","root":"/o/r"}`)
	logPath := writeFixture(t, root, "log.jsonl", "{\"a\":\"/o/r/one\"}\n{\"b\":\"other\"}\n")
	notesPath := writeFixture(t, root, ".worktrees/x/notes.json", `{"cwd":"/o/r/.worktrees/x"}`)
	servePath := writeFixture(t, root, "serve/repos/o/r.git/config.json", `{"cwd":"/o/r/.worktrees/x"}`)
	txtPath := writeFixture(t, root, "other.txt", "/o/r/.worktrees/x")
	gitPath := writeFixture(t, root, "work/.git", "gitdir: /somewhere\n")
	gitConfigPath := writeFixture(t, root, "work/config.json", `{"cwd":"/o/r/.worktrees/x"}`)

	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, p := range []string{bindPath, logPath, notesPath, servePath, txtPath, gitPath, gitConfigPath} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	files, err := RewriteJSON(root, []Prefix{{Old: testOldRoot, New: testNewRoot}})
	if err != nil {
		t.Fatalf("RewriteJSON: %v", err)
	}
	if files != 2 {
		t.Errorf("changed files = %d, want 2", files)
	}

	if got, want := readFixture(t, bindPath), `{"cwd":"/n/r/.worktrees/x","root":"/n/r"}`; got != want {
		t.Errorf("bind.json = %q, want %q", got, want)
	}
	if got, want := readFixture(t, logPath), "{\"a\":\"/n/r/one\"}\n{\"b\":\"other\"}\n"; got != want {
		t.Errorf("log.jsonl = %q, want %q", got, want)
	}

	for _, c := range []struct{ path, want string }{
		{notesPath, `{"cwd":"/o/r/.worktrees/x"}`},
		{servePath, `{"cwd":"/o/r/.worktrees/x"}`},
		{txtPath, "/o/r/.worktrees/x"},
		{gitConfigPath, `{"cwd":"/o/r/.worktrees/x"}`},
	} {
		if got := readFixture(t, c.path); got != c.want {
			t.Errorf("%s = %q, want unchanged %q", c.path, got, c.want)
		}
		fi, err := os.Stat(c.path)
		if err != nil {
			t.Fatalf("stat %s: %v", c.path, err)
		}
		if !fi.ModTime().Equal(old) {
			t.Errorf("%s mtime = %v, want untouched %v", c.path, fi.ModTime(), old)
		}
	}
}

func writeFixture(t *testing.T, root, rel, data string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
