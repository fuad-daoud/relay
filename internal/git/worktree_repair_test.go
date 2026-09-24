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

// TestWorktreeRepairAfterBareRepoAndWorktreeBothMove pins the claim that a
// repair driven from the *moved* bare repo is enough when the bare repo and the
// worktree moved together, which is what a state-root move does (#385).
func TestWorktreeRepairAfterBareRepoAndWorktreeBothMove(t *testing.T) {
	ctx := context.Background()

	tmp := t.TempDir()
	// macOS resolves /var to /private/var; comparing paths as git reports them
	// needs the resolved form.
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}

	a := filepath.Join(tmp, "a")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}

	// 1. A bare repo, with main as HEAD so the worktree add below can name a
	// ref.
	bare := filepath.Join(a, "r.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("mkdir r.git: %v", err)
	}
	runGit(t, bare, "init", "--bare")
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	// 2. Seed it with one commit, through a temp clone and a push.
	seed := filepath.Join(tmp, "seed")
	runGit(t, tmp, "clone", bare, seed)
	runGit(t, seed, "config", "maintenance.auto", "false")
	runGit(t, seed, "config", "gc.auto", "0")
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGit(t, seed, "add", "a.txt")
	runGit(t, seed, "commit", "-m", "initial")
	runGit(t, seed, "push", "origin", "main")

	// 3. A worktree of the bare repo, inside a -- which step 4 then moves.
	runGit(t, bare, "worktree", "add", filepath.Join(a, "wt"), "main")

	// 4. Move the whole directory the bare repo and the worktree live in.
	if err := os.Rename(a, filepath.Join(tmp, "b")); err != nil {
		t.Fatalf("move a to b: %v", err)
	}
	movedRepo := filepath.Join(tmp, "b", "r.git")
	moved := filepath.Join(tmp, "b", "wt")

	// 5. Repair from the moved bare repo alone.
	c := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)
	if err := c.WorktreeRepair(ctx, movedRepo, moved); err != nil {
		t.Fatalf("WorktreeRepair: %v", err)
	}

	// 6. The worktree must be usable as a repository again.
	runGit(t, moved, "status")
}
