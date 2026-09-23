package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClientWorktreeRepair moves a worktree's directory out from under git and
// then repairs it, which is exactly what a state-root move does (#292 §3 step
// 5). Real git, no fakes.
func TestClientWorktreeRepair(t *testing.T) {
	ctx := context.Background()

	tmp := t.TempDir()
	// macOS resolves /var to /private/var; comparing paths as git reports them
	// needs the resolved form.
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}

	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "initial")

	if err := os.MkdirAll(filepath.Join(tmp, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	runGit(t, repo, "worktree", "add", filepath.Join(tmp, "a", "wt"))

	if err := os.Rename(filepath.Join(tmp, "a"), filepath.Join(tmp, "b")); err != nil {
		t.Fatalf("move a to b: %v", err)
	}
	moved := filepath.Join(tmp, "b", "wt")

	c := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)
	if err := c.WorktreeRepair(ctx, repo, moved); err != nil {
		t.Fatalf("WorktreeRepair: %v", err)
	}

	list := runGit(t, repo, "worktree", "list", "--porcelain")
	if !strings.Contains(list, moved) {
		t.Errorf("worktree list does not name %s:\n%s", moved, list)
	}

	// The repaired worktree must be usable as a repository again.
	runGit(t, moved, "status")
}
