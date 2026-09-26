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

// TestRewriteJSON pins the rewrite contract: only JSON files and only values
// starting with the old root change; an untouched file keeps its mtime.
func TestRewriteJSON(t *testing.T) {
	root := t.TempDir()

	fixture := func(rel, data string) string {
		path := filepath.Join(root, rel)
		mustWrite(t, path, data)
		return path
	}
	bindPath := fixture("bind.json", `{"cwd":"/o/r/.worktrees/x","root":"/o/r"}`)
	logPath := fixture("log.jsonl", "{\"a\":\"/o/r/one\"}\n{\"b\":\"other\"}\n")
	notesPath := fixture(".worktrees/x/notes.json", `{"cwd":"/o/r/.worktrees/x"}`)
	servePath := fixture("serve/repos/o/r.git/config.json", `{"cwd":"/o/r/.worktrees/x"}`)
	txtPath := fixture("other.txt", "/o/r/.worktrees/x")
	gitPath := fixture("work/.git", "gitdir: /somewhere\n")
	gitConfigPath := fixture("work/config.json", `{"cwd":"/o/r/.worktrees/x"}`)

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
