package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		// Ignore the developer's own git config. Setting the identity is not
		// enough: a global commit.gpgsign makes every fixture commit here try
		// to reach gpg, which fails outright without a key and blocks for the
		// agent timeout with one -- a suite that passes or hangs depending on
		// whose machine it runs on.
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\nOutput: %s", strings.Join(args, " "), dir, err, string(out))
	}

	// A repo these tests create gets auto-maintenance off. Every `git commit`
	// otherwise spawns `git maintenance run --auto --quiet --detach`, which
	// outlives the command and writes under .git/objects while t.TempDir()'s
	// RemoveAll is removing the tree -- and that cleanup failure fails the
	// test, not just the teardown (#304). Repo-local config, so every later
	// git command on it inherits it, including ones the code under test runs.
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return string(out)
}

func TestClientSnapshotAndDiff(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()

	// Initialize git repo
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	// Create an initial commit
	if err := os.WriteFile(filepath.Join(repoDir, "initial.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "initial.txt")
	runGit(t, repoDir, "commit", "-m", "initial")

	// Set up .gitignore
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", ".gitignore")
	runGit(t, repoDir, "commit", "-m", "add gitignore")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// Baseline status
	statusBefore := runGit(t, repoDir, "status", "--porcelain")

	// Snapshot 1: no changes
	tree1, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree 1: %v", err)
	}
	if tree1 == "" {
		t.Fatal("tree1 is empty")
	}

	// Status after snapshot must be identical
	statusAfter1 := runGit(t, repoDir, "status", "--porcelain")
	if statusBefore != statusAfter1 {
		t.Fatalf("git status changed after snapshot 1: got %q, want %q", statusAfter1, statusBefore)
	}

	// Snapshot 2: still no changes, must return identical tree id
	tree2, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree 2: %v", err)
	}
	if tree1 != tree2 {
		t.Fatalf("tree2 (%s) != tree1 (%s) with no changes", tree2, tree1)
	}

	// Add an ignored file: must NOT change snapshot
	if err := os.WriteFile(filepath.Join(repoDir, "ignored.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	treeIgnored, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree with ignored file: %v", err)
	}
	if treeIgnored != tree1 {
		t.Fatalf("treeIgnored (%s) != tree1 (%s) after adding ignored file", treeIgnored, tree1)
	}

	// Add an untracked file: MUST change snapshot
	if err := os.WriteFile(filepath.Join(repoDir, "new_file.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree3, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree 3: %v", err)
	}
	if tree3 == tree1 {
		t.Fatal("tree3 equals tree1 after adding untracked file")
	}

	// Status after snapshot must still show new_file.txt as untracked (not staged)
	statusAfter3 := runGit(t, repoDir, "status", "--porcelain")
	if !strings.Contains(statusAfter3, "?? new_file.txt") {
		t.Fatalf("git status does not show new_file.txt as untracked: %s", statusAfter3)
	}

	// Diff tree1 to tree2 (empty diff)
	emptyDiff, err := client.DiffTrees(ctx, repoDir, tree1, tree2)
	if err != nil {
		t.Fatalf("DiffTrees empty: %v", err)
	}
	if !emptyDiff.Stat.Empty() {
		t.Fatalf("expected empty stat, got %+v", emptyDiff.Stat)
	}
	if emptyDiff.Patch != nil {
		t.Fatalf("expected nil patch for empty diff, got %q", string(emptyDiff.Patch))
	}

	// Diff tree1 to tree3
	diff, err := client.DiffTrees(ctx, repoDir, tree1, tree3)
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	if diff.Stat.FilesChanged != 1 || diff.Stat.Insertions != 2 || diff.Stat.Deletions != 0 {
		t.Fatalf("unexpected stat: %+v", diff.Stat)
	}
	if diff.Truncated {
		t.Fatal("unexpected truncated diff")
	}
	if !strings.Contains(string(diff.Patch), "+line1") || !strings.Contains(string(diff.Patch), "+line2") {
		t.Fatalf("patch missing content: %s", string(diff.Patch))
	}
}

func TestDiffWorktreeStat(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "a.txt")
	runGit(t, repoDir, "commit", "-m", "initial")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	tree := strings.TrimSpace(runGit(t, repoDir, "write-tree"))

	// A clean worktree against its own tree diffs to zero.
	stat, err := client.DiffWorktreeStat(ctx, repoDir, tree)
	if err != nil {
		t.Fatalf("DiffWorktreeStat clean: %v", err)
	}
	if !stat.Empty() {
		t.Fatalf("expected empty stat on a clean worktree, got %+v", stat)
	}

	// Edit the tracked file and stage a new one: the tree is now behind both.
	if err := os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "b.txt")

	stat, err = client.DiffWorktreeStat(ctx, repoDir, tree)
	if err != nil {
		t.Fatalf("DiffWorktreeStat dirty: %v", err)
	}
	if stat.FilesChanged != 2 || stat.Insertions != 2 || stat.Deletions != 0 {
		t.Fatalf("unexpected stat: %+v", stat)
	}

	// Not a repository -> ErrNotRepo.
	if _, err := client.DiffWorktreeStat(ctx, t.TempDir(), tree); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("DiffWorktreeStat outside a repo: got %v, want ErrNotRepo", err)
	}
}

func TestClientDiffTruncation(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "a.txt")
	runGit(t, repoDir, "commit", "-m", "init")

	client := NewClient("git", 5*time.Second, 10) // tiny 10-byte cap

	tree1, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "b.txt"), []byte(strings.Repeat("a long line of text\n", 5)), 0o644); err != nil {
		t.Fatal(err)
	}
	tree2, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}

	diff, err := client.DiffTrees(ctx, repoDir, tree1, tree2)
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	if !diff.Truncated {
		t.Fatal("expected Truncated to be true")
	}
	if diff.Patch != nil {
		t.Fatal("expected Patch to be nil when Truncated")
	}
	if diff.Stat.FilesChanged != 1 {
		t.Fatalf("expected 1 file changed, got %d", diff.Stat.FilesChanged)
	}
}

func TestClientErrors(t *testing.T) {
	ctx := context.Background()
	notRepoDir := t.TempDir()

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	_, err := client.SnapshotTree(ctx, notRepoDir)
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("got %v, want ErrNotRepo", err)
	}

	_, err = client.DiffTrees(ctx, notRepoDir, "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("got %v, want ErrNotRepo", err)
	}

	if _, err := client.HeadCommit(ctx, notRepoDir); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("HeadCommit: got %v, want ErrNotRepo", err)
	}

	if _, err := client.BranchExists(ctx, notRepoDir, "main"); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("BranchExists: got %v, want ErrNotRepo", err)
	}

	if err := client.AddWorktree(ctx, notRepoDir, filepath.Join(notRepoDir, "wt"), "branch", "HEAD"); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("AddWorktree: got %v, want ErrNotRepo", err)
	}

	if err := client.RemoveWorktree(ctx, notRepoDir, filepath.Join(notRepoDir, "wt"), false); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("RemoveWorktree: got %v, want ErrNotRepo", err)
	}

	if _, err := client.Dirty(ctx, notRepoDir); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("Dirty: got %v, want ErrNotRepo", err)
	}

	badClient := NewClient("nonexistent-git-binary-xyz", 5*time.Second, DefaultMaxPatchBytes)
	if _, err := badClient.SnapshotTree(ctx, notRepoDir); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("got %v, want ErrGitUnavailable", err)
	}

	if _, err := badClient.HeadCommit(ctx, notRepoDir); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("HeadCommit: got %v, want ErrGitUnavailable", err)
	}

	if _, err := badClient.BranchExists(ctx, notRepoDir, "main"); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("BranchExists: got %v, want ErrGitUnavailable", err)
	}

	if err := badClient.AddWorktree(ctx, notRepoDir, filepath.Join(notRepoDir, "wt"), "branch", "HEAD"); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("AddWorktree: got %v, want ErrGitUnavailable", err)
	}

	if err := badClient.RemoveWorktree(ctx, notRepoDir, filepath.Join(notRepoDir, "wt"), false); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("RemoveWorktree: got %v, want ErrGitUnavailable", err)
	}

	if _, err := badClient.Dirty(ctx, notRepoDir); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("Dirty: got %v, want ErrGitUnavailable", err)
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "first commit")
	commit1 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "commit", "-am", "second commit")
	commit2 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// Status before AddWorktree
	statusBefore := runGit(t, repoDir, "status", "--porcelain")

	// 1. AddWorktree produces a working tree on a new branch at the requested commit
	// while the source tree's git status is unchanged.
	wtDir := filepath.Join(t.TempDir(), "wt1")
	branch := "relevo/test-wt"
	if err := client.AddWorktree(ctx, repoDir, wtDir, branch, commit1); err != nil {
		t.Fatalf("AddWorktree failed: %v", err)
	}

	// Verify working tree exists
	if fi, err := os.Stat(wtDir); err != nil || !fi.IsDir() {
		t.Fatalf("worktree directory %s does not exist or is not a directory", wtDir)
	}

	// Verify worktree HEAD is commit1
	wtHead, err := client.HeadCommit(ctx, wtDir)
	if err != nil {
		t.Fatalf("HeadCommit on worktree: %v", err)
	}
	if wtHead != commit1 {
		t.Fatalf("worktree HEAD is %s, want %s", wtHead, commit1)
	}

	// Verify source tree status is unchanged
	statusAfter := runGit(t, repoDir, "status", "--porcelain")
	if statusAfter != statusBefore {
		t.Fatalf("source tree status changed: got %q, want %q", statusAfter, statusBefore)
	}

	// Verify source tree HEAD is still commit2
	srcHead, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit on repo: %v", err)
	}
	if srcHead != commit2 {
		t.Fatalf("repo HEAD is %s, want %s", srcHead, commit2)
	}

	// 2. A second AddWorktree on the same branch returns ErrBranchExists.
	wtDir2 := filepath.Join(t.TempDir(), "wt2")
	err = client.AddWorktree(ctx, repoDir, wtDir2, branch, commit2)
	if !errors.Is(err, ErrBranchExists) {
		t.Fatalf("second AddWorktree got %v, want ErrBranchExists", err)
	}
	// Verify nothing was left behind at wtDir2
	if _, err := os.Stat(wtDir2); !os.IsNotExist(err) {
		t.Fatalf("wtDir2 was not cleaned up after error: %v", err)
	}

	// 3. RemoveWorktree with force: false returns ErrWorktreeDirty on a tree with
	// an uncommitted edit and succeeds on a clean one.
	dirtyFile := filepath.Join(wtDir, "uncommitted.txt")
	if err := os.WriteFile(dirtyFile, []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = client.RemoveWorktree(ctx, repoDir, wtDir, false)
	if !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("RemoveWorktree on dirty tree got %v, want ErrWorktreeDirty", err)
	}
	if _, err := os.Stat(wtDir); os.IsNotExist(err) {
		t.Fatal("wtDir was removed despite ErrWorktreeDirty")
	}

	// Clean the edit and remove again
	if err := os.Remove(dirtyFile); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveWorktree(ctx, repoDir, wtDir, false); err != nil {
		t.Fatalf("RemoveWorktree on clean tree failed: %v", err)
	}
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatal("wtDir still exists after successful RemoveWorktree")
	}

	// 4. After a successful removal the branch still resolves.
	exists, err := client.BranchExists(ctx, repoDir, branch)
	if err != nil {
		t.Fatalf("BranchExists after removal: %v", err)
	}
	if !exists {
		t.Fatal("branch was removed after RemoveWorktree; branch must survive")
	}
	branchCommit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "refs/heads/"+branch))
	if branchCommit != commit1 {
		t.Fatalf("branch resolves to %s, want %s", branchCommit, commit1)
	}

	// 5. RemoveWorktree with force: true succeeds on dirty tree.
	wtDir3 := filepath.Join(t.TempDir(), "wt3")
	branch3 := "relevo/test-wt3"
	if err := client.AddWorktree(ctx, repoDir, wtDir3, branch3, commit1); err != nil {
		t.Fatalf("AddWorktree 3: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtDir3, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveWorktree(ctx, repoDir, wtDir3, true); err != nil {
		t.Fatalf("RemoveWorktree with force: true failed: %v", err)
	}
	if _, err := os.Stat(wtDir3); !os.IsNotExist(err) {
		t.Fatal("wtDir3 still exists after forced RemoveWorktree")
	}

	// 6. AddWorktree cleans up path on error (e.g. bad commit).
	wtDirBad := filepath.Join(t.TempDir(), "wt-bad")
	err = client.AddWorktree(ctx, repoDir, wtDirBad, "relevo/bad", "0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("AddWorktree with invalid commit expected error, got nil")
	}
	if _, err := os.Stat(wtDirBad); !os.IsNotExist(err) {
		t.Fatal("wtDirBad was not cleaned up on error")
	}
}

func TestCheckoutWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "first commit")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	wt1 := filepath.Join(t.TempDir(), "wt1")
	if err := client.AddWorktree(ctx, repoDir, wt1, "feature", "HEAD"); err != nil {
		t.Fatalf("AddWorktree failed: %v", err)
	}
	if err := client.RemoveWorktree(ctx, repoDir, wt1, false); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}

	// (a) CheckoutWorktree(ctx, repo, wt2, "feature") returns nil and
	// wt2/.git exists and git -C wt2 rev-parse --abbrev-ref HEAD prints feature.
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if err := client.CheckoutWorktree(ctx, repoDir, wt2, "feature"); err != nil {
		t.Fatalf("CheckoutWorktree wt2 failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt2, ".git")); err != nil {
		t.Fatalf("wt2/.git does not exist: %v", err)
	}
	branchOut := strings.TrimSpace(runGit(t, wt2, "rev-parse", "--abbrev-ref", "HEAD"))
	if branchOut != "feature" {
		t.Fatalf("wt2 branch is %q, want %q", branchOut, "feature")
	}

	// (b) CheckoutWorktree(ctx, repo, wt3, "feature") -- the branch is now
	// checked out in wt2 -- returns an error with errors.Is(err, ErrBranchCheckedOut)
	// and wt3 does not exist afterwards (cleanup ran).
	wt3 := filepath.Join(t.TempDir(), "wt3")
	err := client.CheckoutWorktree(ctx, repoDir, wt3, "feature")
	if !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("CheckoutWorktree wt3 got %v, want ErrBranchCheckedOut", err)
	}
	if _, err := os.Stat(wt3); !os.IsNotExist(err) {
		t.Fatalf("wt3 was not cleaned up after error: %v", err)
	}

	// (c) CheckoutWorktree(ctx, repo, wt4, "no-such-branch") returns a non-nil
	// error that is not ErrBranchCheckedOut.
	wt4 := filepath.Join(t.TempDir(), "wt4")
	err = client.CheckoutWorktree(ctx, repoDir, wt4, "no-such-branch")
	if err == nil {
		t.Fatal("CheckoutWorktree wt4 expected error, got nil")
	}
	if errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("CheckoutWorktree wt4 got ErrBranchCheckedOut, want another error")
	}
}

func TestHeadCommit_BranchExists_Dirty(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// Unborn HEAD: git init without commits
	emptyRepo := t.TempDir()
	runGit(t, emptyRepo, "init")
	runGit(t, emptyRepo, "config", "user.name", "Test")
	runGit(t, emptyRepo, "config", "user.email", "test@example.com")

	_, err := client.HeadCommit(ctx, emptyRepo)
	if err == nil {
		t.Fatal("HeadCommit on repo with no commits expected error, got nil")
	}

	dirty, err := client.Dirty(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("Dirty on empty repo: %v", err)
	}
	if dirty {
		t.Fatal("empty repo reported as dirty")
	}

	// Commit a file
	if err := os.WriteFile(filepath.Join(emptyRepo, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, emptyRepo, "add", "a.txt")
	runGit(t, emptyRepo, "commit", "-m", "first")

	head, err := client.HeadCommit(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if len(head) != 40 {
		t.Fatalf("expected 40-char commit hash, got %q", head)
	}

	// BranchExists
	defaultBranch := strings.TrimSpace(runGit(t, emptyRepo, "branch", "--show-current"))
	exists, err := client.BranchExists(ctx, emptyRepo, defaultBranch)
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if !exists {
		t.Fatalf("BranchExists(%q) = false, want true", defaultBranch)
	}

	// With refs/heads/ prefix
	exists, err = client.BranchExists(ctx, emptyRepo, "refs/heads/"+defaultBranch)
	if err != nil {
		t.Fatalf("BranchExists with prefix: %v", err)
	}
	if !exists {
		t.Fatalf("BranchExists(refs/heads/%s) = false, want true", defaultBranch)
	}

	// Nonexistent branch
	exists, err = client.BranchExists(ctx, emptyRepo, "nonexistent-branch-xyz")
	if err != nil {
		t.Fatalf("BranchExists nonexistent: %v", err)
	}
	if exists {
		t.Fatal("BranchExists(nonexistent) = true, want false")
	}

	// Dirty states
	// Clean repo
	dirty, err = client.Dirty(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("Dirty clean: %v", err)
	}
	if dirty {
		t.Fatal("clean repo reported as dirty")
	}

	// Untracked non-ignored file
	untracked := filepath.Join(emptyRepo, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = client.Dirty(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("Dirty untracked: %v", err)
	}
	if !dirty {
		t.Fatal("untracked file not reported as dirty")
	}

	// Stage it
	runGit(t, emptyRepo, "add", "untracked.txt")
	dirty, err = client.Dirty(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("Dirty staged: %v", err)
	}
	if !dirty {
		t.Fatal("staged file not reported as dirty")
	}

	// Commit it
	runGit(t, emptyRepo, "commit", "-m", "second")
	dirty, err = client.Dirty(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("Dirty after commit: %v", err)
	}
	if dirty {
		t.Fatal("repo reported as dirty after commit")
	}

	// Modify tracked file
	if err := os.WriteFile(untracked, []byte("bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = client.Dirty(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("Dirty modified: %v", err)
	}
	if !dirty {
		t.Fatal("modified tracked file not reported as dirty")
	}

	// Revert modification
	runGit(t, emptyRepo, "checkout", "--", "untracked.txt")
	dirty, err = client.Dirty(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("Dirty after checkout: %v", err)
	}
	if dirty {
		t.Fatal("repo reported as dirty after revert")
	}

	// Ignored file
	if err := os.WriteFile(filepath.Join(emptyRepo, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, emptyRepo, "add", ".gitignore")
	runGit(t, emptyRepo, "commit", "-m", "ignore")

	if err := os.WriteFile(filepath.Join(emptyRepo, "ignored.txt"), []byte("skip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = client.Dirty(ctx, emptyRepo)
	if err != nil {
		t.Fatalf("Dirty with ignored file: %v", err)
	}
	if dirty {
		t.Fatal("repo with only ignored untracked file reported as dirty")
	}
}

func TestRevListCount(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "first")
	base, err := client.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}

	// same commit -> 0
	n, err := client.RevListCount(ctx, repo, base, base)
	if err != nil || n != 0 {
		t.Fatalf("RevListCount(base..base) = %d, %v; want 0, nil", n, err)
	}

	// two more commits -> 2
	for i, name := range []string{"b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", name)
		runGit(t, repo, "commit", "-m", fmt.Sprintf("commit %d", i+2))
	}
	head, err := client.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	n, err = client.RevListCount(ctx, repo, base, head)
	if err != nil || n != 2 {
		t.Fatalf("RevListCount(base..head) = %d, %v; want 2, nil", n, err)
	}

	// unresolvable ref -> error
	if _, err := client.RevListCount(ctx, repo, "0123456789abcdef0123456789abcdef01234567", head); err == nil {
		t.Fatal("RevListCount with an unresolvable ref returned nil error")
	}

	// not a repository -> ErrNotRepo
	if _, err := client.RevListCount(ctx, t.TempDir(), base, head); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("RevListCount outside a repo: err = %v, want ErrNotRepo", err)
	}
}

func TestRootCommit(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// empty git init -> ErrRefMissing
	emptyRepo := t.TempDir()
	runGit(t, emptyRepo, "init")
	if _, err := client.RootCommit(ctx, emptyRepo); !errors.Is(err, ErrRefMissing) {
		t.Fatalf("RootCommit(empty): got err %v, want ErrRefMissing", err)
	}

	// two commits -> the first's sha
	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f1.txt")
	runGit(t, repo, "commit", "-m", "first")
	firstSHA := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repo, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f2.txt")
	runGit(t, repo, "commit", "-m", "second")

	root, err := client.RootCommit(ctx, repo)
	if err != nil {
		t.Fatalf("RootCommit: %v", err)
	}
	if root != firstSHA {
		t.Fatalf("RootCommit = %q, want first commit %q", root, firstSHA)
	}
}

func TestRefSHAMissingIsOkFalse(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "first")
	headSHA := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	// missing ref -> "", false, nil
	sha, ok, err := client.RefSHA(ctx, repo, "refs/heads/nope")
	if err != nil || ok || sha != "" {
		t.Fatalf("RefSHA(refs/heads/nope) = (%q, %v, %v); want (\"\", false, nil)", sha, ok, err)
	}

	// existing ref -> sha, true, nil
	sha, ok, err = client.RefSHA(ctx, repo, "HEAD")
	if err != nil || !ok || sha != headSHA {
		t.Fatalf("RefSHA(HEAD) = (%q, %v, %v); want (%q, true, nil)", sha, ok, err, headSHA)
	}
}

func TestUpdateRefCreatesAndCAS(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f1.txt")
	runGit(t, repo, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repo, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f2.txt")
	runGit(t, repo, "commit", "-m", "c2")
	c2 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	ref := "refs/heads/testref"

	// create with old ""
	if err := client.UpdateRef(ctx, repo, ref, c1, ""); err != nil {
		t.Fatalf("UpdateRef create: %v", err)
	}
	sha := strings.TrimSpace(runGit(t, repo, "rev-parse", ref))
	if sha != c1 {
		t.Fatalf("ref after create = %q, want %q", sha, c1)
	}

	// CAS with wrong old sha errors and ref is unchanged
	wrongSHA := "0123456789abcdef0123456789abcdef01234567"
	if err := client.UpdateRef(ctx, repo, ref, c2, wrongSHA); err == nil {
		t.Fatal("UpdateRef CAS with wrong old sha succeeded, want error")
	}
	sha = strings.TrimSpace(runGit(t, repo, "rev-parse", ref))
	if sha != c1 {
		t.Fatalf("ref after failed CAS = %q, want %q", sha, c1)
	}

	// CAS with correct old sha succeeds
	if err := client.UpdateRef(ctx, repo, ref, c2, c1); err != nil {
		t.Fatalf("UpdateRef CAS with correct old sha: %v", err)
	}
	sha = strings.TrimSpace(runGit(t, repo, "rev-parse", ref))
	if sha != c2 {
		t.Fatalf("ref after successful CAS = %q, want %q", sha, c2)
	}
}

func TestCommitTreeFromLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// git init --bare a repo
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	// seed it by cloning, committing one file and pushing refs/heads/relevo/api
	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relevo/api")

	// git worktree add from the bare repo at that branch
	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relevo/api")

	// write an untracked file in the worktree
	if err := os.WriteFile(filepath.Join(wt, "untracked.txt"), []byte("dirty work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// SnapshotTree on the worktree
	tree, err := client.SnapshotTree(ctx, wt)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}

	head, ok, err := client.RefSHA(ctx, bare, "refs/heads/relevo/api")
	if err != nil || !ok {
		t.Fatalf("RefSHA(bare): %v, ok=%v", err, ok)
	}

	// CommitTree(bare, tree, head, msg)
	msg := "[relevo] api: round 1, uncommitted work"
	sha, err := client.CommitTree(ctx, bare, tree, head, msg)
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}

	// UpdateRef(bare, "refs/relevo/api/round-1", sha, "")
	sideRef := "refs/relevo/api/round-1"
	if err := client.UpdateRef(ctx, bare, sideRef, sha, ""); err != nil {
		t.Fatalf("UpdateRef(sideRef): %v", err)
	}

	// assert git cat-file -p <sha> in the bare repo shows tree, parent, author relevo <relevo@localhost>
	catOut := runGit(t, bare, "cat-file", "-p", sha)
	if !strings.Contains(catOut, "tree "+tree) {
		t.Errorf("cat-file missing tree %q in:\n%s", tree, catOut)
	}
	if !strings.Contains(catOut, "parent "+head) {
		t.Errorf("cat-file missing parent %q in:\n%s", head, catOut)
	}
	if !strings.Contains(catOut, "author relevo <relevo@localhost>") {
		t.Errorf("cat-file missing relevo author in:\n%s", catOut)
	}
	if !strings.Contains(catOut, "committer relevo <relevo@localhost>") {
		t.Errorf("cat-file missing relevo committer in:\n%s", catOut)
	}

	// refs/heads/relevo/api still equals head
	headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relevo/api")
	if err != nil || !ok || headAfter != head {
		t.Fatalf("refs/heads/relevo/api changed: got %q, want %q", headAfter, head)
	}
}

func TestBundleCreateFullThenIncremental(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := t.TempDir()
	runGit(t, repo, "init")

	// Make first commit substantial so full bundle is larger
	payload := make([]byte, 50000)
	for i := range payload {
		payload[i] = byte(i*31 + 7)
	}
	if err := os.WriteFile(filepath.Join(repo, "large.txt"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "large.txt")
	runGit(t, repo, "commit", "-m", "first")
	c1 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	bFull := filepath.Join(t.TempDir(), "full.bundle")
	ref := "refs/heads/master"
	runGit(t, repo, "update-ref", ref, c1)

	heads, empty, err := client.BundleCreate(ctx, repo, bFull, []string{ref}, "")
	if err != nil || empty {
		t.Fatalf("BundleCreate full: empty=%v, err=%v", empty, err)
	}
	if heads[ref] != c1 {
		t.Fatalf("heads[%s] = %q, want %q", ref, heads[ref], c1)
	}

	bHeads, err := client.BundleHeads(ctx, repo, bFull)
	if err != nil {
		t.Fatalf("BundleHeads full: %v", err)
	}
	if bHeads[ref] != c1 {
		t.Fatalf("bHeads[%s] = %q, want %q", ref, bHeads[ref], c1)
	}

	// Add second commit
	if err := os.WriteFile(filepath.Join(repo, "small.txt"), []byte("small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "small.txt")
	runGit(t, repo, "commit", "-m", "second")
	c2 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "update-ref", ref, c2)

	bIncr := filepath.Join(t.TempDir(), "incr.bundle")
	heads2, empty, err := client.BundleCreate(ctx, repo, bIncr, []string{ref}, c1)
	if err != nil || empty {
		t.Fatalf("BundleCreate incr: empty=%v, err=%v", empty, err)
	}
	if heads2[ref] != c2 {
		t.Fatalf("heads2[%s] = %q, want %q", ref, heads2[ref], c2)
	}

	bHeads2, err := client.BundleHeads(ctx, repo, bIncr)
	if err != nil {
		t.Fatalf("BundleHeads incr: %v", err)
	}
	if bHeads2[ref] != c2 {
		t.Fatalf("bHeads2[%s] = %q, want %q", ref, bHeads2[ref], c2)
	}

	fiFull, err := os.Stat(bFull)
	if err != nil {
		t.Fatal(err)
	}
	fiIncr, err := os.Stat(bIncr)
	if err != nil {
		t.Fatal(err)
	}
	if fiIncr.Size() >= fiFull.Size() {
		t.Fatalf("incremental bundle size (%d) >= full bundle size (%d)", fiIncr.Size(), fiFull.Size())
	}

	// Malformed bundle -> ErrBadBundle
	badBundle := filepath.Join(t.TempDir(), "bad.bundle")
	if err := os.WriteFile(badBundle, []byte("bad bundle content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := client.BundleHeads(ctx, repo, badBundle); !errors.Is(err, ErrBadBundle) {
		t.Fatalf("BundleHeads on bad bundle: got %v, want ErrBadBundle", err)
	}
}

func TestBundleCreateEmptyWhenNothingNew(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "first")
	c1 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	ref := "refs/heads/master"
	runGit(t, repo, "update-ref", ref, c1)

	bPath := filepath.Join(t.TempDir(), "empty.bundle")
	heads, empty, err := client.BundleCreate(ctx, repo, bPath, []string{ref}, c1)
	if err != nil {
		t.Fatalf("BundleCreate: %v", err)
	}
	if !empty {
		t.Fatal("BundleCreate returned empty=false, want true")
	}
	if heads[ref] != c1 {
		t.Fatalf("heads[%s] = %q, want %q", ref, heads[ref], c1)
	}
	if _, err := os.Stat(bPath); !os.IsNotExist(err) {
		t.Fatalf("expected bundle file not to exist, got err: %v", err)
	}
}

func TestBundleCreateSinceNotAncestor(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "root.txt"), []byte("root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "root.txt")
	runGit(t, repo, "commit", "-m", "root")

	// Branch A
	runGit(t, repo, "checkout", "-b", "branchA")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "a")
	refA := "refs/heads/branchA"

	// Branch B
	runGit(t, repo, "checkout", "master")
	runGit(t, repo, "checkout", "-b", "branchB")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "b.txt")
	runGit(t, repo, "commit", "-m", "b")
	cBranchB := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	bPath := filepath.Join(t.TempDir(), "unrelated.bundle")
	_, _, err := client.BundleCreate(ctx, repo, bPath, []string{refA}, cBranchB)
	if !errors.Is(err, ErrRefMissing) {
		t.Fatalf("BundleCreate with unrelated since: got %v, want ErrRefMissing", err)
	}
}

func TestBundleCreateTwoRefs(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f1.txt")
	runGit(t, repo, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repo, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f2.txt")
	runGit(t, repo, "commit", "-m", "c2")
	c2 := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	ref1 := "refs/heads/relevo/api"
	ref2 := "refs/relevo/api/round-1"
	runGit(t, repo, "update-ref", ref1, c2)
	runGit(t, repo, "update-ref", ref2, c1)

	bPath := filepath.Join(t.TempDir(), "two_refs.bundle")
	heads, empty, err := client.BundleCreate(ctx, repo, bPath, []string{ref1, ref2}, "")
	if err != nil || empty {
		t.Fatalf("BundleCreate two refs: empty=%v, err=%v", empty, err)
	}
	if heads[ref1] != c2 || heads[ref2] != c1 {
		t.Fatalf("unexpected heads: %v", heads)
	}

	bHeads, err := client.BundleHeads(ctx, repo, bPath)
	if err != nil {
		t.Fatalf("BundleHeads: %v", err)
	}
	if bHeads[ref1] != c2 || bHeads[ref2] != c1 {
		t.Fatalf("unexpected bHeads: %v", bHeads)
	}
}

func TestFetchBundleFastForwards(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoA := t.TempDir()
	runGit(t, repoA, "init")
	if err := os.WriteFile(filepath.Join(repoA, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f1.txt")
	runGit(t, repoA, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))
	ref := "refs/heads/relevo/api"
	runGit(t, repoA, "update-ref", ref, c1)

	bareB := t.TempDir()
	runGit(t, bareB, "init", "--bare")

	// Bundle 1: full
	b1 := filepath.Join(t.TempDir(), "b1.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, b1, []string{ref}, ""); err != nil {
		t.Fatalf("BundleCreate b1: %v", err)
	}

	fetched, err := client.FetchBundle(ctx, bareB, b1, []string{ref})
	if err != nil {
		t.Fatalf("FetchBundle b1: %v", err)
	}
	if fetched[ref] != c1 {
		t.Fatalf("fetched[%s] = %q, want %q", ref, fetched[ref], c1)
	}
	shaB, ok, err := client.RefSHA(ctx, bareB, ref)
	if err != nil || !ok || shaB != c1 {
		t.Fatalf("bareB ref = (%q, %v, %v); want (%q, true, nil)", shaB, ok, err, c1)
	}

	// Bundle 2: incremental
	if err := os.WriteFile(filepath.Join(repoA, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f2.txt")
	runGit(t, repoA, "commit", "-m", "c2")
	c2 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))
	runGit(t, repoA, "update-ref", ref, c2)

	b2 := filepath.Join(t.TempDir(), "b2.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, b2, []string{ref}, c1); err != nil {
		t.Fatalf("BundleCreate b2: %v", err)
	}

	fetched2, err := client.FetchBundle(ctx, bareB, b2, []string{ref})
	if err != nil {
		t.Fatalf("FetchBundle b2: %v", err)
	}
	if fetched2[ref] != c2 {
		t.Fatalf("fetched2[%s] = %q, want %q", ref, fetched2[ref], c2)
	}
	shaB2, ok, err := client.RefSHA(ctx, bareB, ref)
	if err != nil || !ok || shaB2 != c2 {
		t.Fatalf("bareB ref = (%q, %v, %v); want (%q, true, nil)", shaB2, ok, err, c2)
	}
}

func TestFetchBundleRefusesNonFastForward(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoA := t.TempDir()
	runGit(t, repoA, "init")
	if err := os.WriteFile(filepath.Join(repoA, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f1.txt")
	runGit(t, repoA, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))
	ref := "refs/heads/relevo/api"
	runGit(t, repoA, "update-ref", ref, c1)

	bareB := t.TempDir()
	runGit(t, bareB, "init", "--bare")

	// initial bundle into B
	b1 := filepath.Join(t.TempDir(), "b1.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, b1, []string{ref}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchBundle(ctx, bareB, b1, []string{ref}); err != nil {
		t.Fatal(err)
	}

	// B's ref is moved ahead by a local commit
	treeB := strings.TrimSpace(runGit(t, bareB, "rev-parse", c1+"^{tree}"))
	localCommit, err := client.CommitTree(ctx, bareB, treeB, c1, "local divergence")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	if err := client.UpdateRef(ctx, bareB, ref, localCommit, c1); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	// A produces commit c2
	if err := os.WriteFile(filepath.Join(repoA, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f2.txt")
	runGit(t, repoA, "commit", "-m", "c2")
	c2 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))
	runGit(t, repoA, "update-ref", ref, c2)

	b2 := filepath.Join(t.TempDir(), "b2.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, b2, []string{ref}, ""); err != nil {
		t.Fatal(err)
	}

	// Client's bundle fetch must fail with ErrNotFastForward
	_, err = client.FetchBundle(ctx, bareB, b2, []string{ref})
	if !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("FetchBundle non-fast-forward: got %v, want ErrNotFastForward", err)
	}

	// B's ref remains unchanged at localCommit
	shaB, ok, err := client.RefSHA(ctx, bareB, ref)
	if err != nil || !ok || shaB != localCommit {
		t.Fatalf("bareB ref = %q, want %q", shaB, localCommit)
	}
}

func TestFetchBundleMissingPrereq(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoA := t.TempDir()
	runGit(t, repoA, "init")
	if err := os.WriteFile(filepath.Join(repoA, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f1.txt")
	runGit(t, repoA, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repoA, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f2.txt")
	runGit(t, repoA, "commit", "-m", "c2")
	c2 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))

	ref := "refs/heads/relevo/api"
	runGit(t, repoA, "update-ref", ref, c2)

	// Incremental bundle requiring c1
	bIncr := filepath.Join(t.TempDir(), "incr.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, bIncr, []string{ref}, c1); err != nil {
		t.Fatal(err)
	}

	// Bare repo B has never seen c1
	bareB := t.TempDir()
	runGit(t, bareB, "init", "--bare")

	_, err := client.FetchBundle(ctx, bareB, bIncr, []string{ref})
	if !errors.Is(err, ErrBadBundle) {
		t.Fatalf("FetchBundle missing prereq: got %v, want ErrBadBundle", err)
	}
}

func TestFetchBundleIgnoresRefsNotAsked(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoA := t.TempDir()
	runGit(t, repoA, "init")
	if err := os.WriteFile(filepath.Join(repoA, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f1.txt")
	runGit(t, repoA, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repoA, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "f2.txt")
	runGit(t, repoA, "commit", "-m", "c2")
	c2 := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))

	ref1 := "refs/heads/relevo/api"
	ref2 := "refs/relevo/api/round-1"
	runGit(t, repoA, "update-ref", ref1, c1)
	runGit(t, repoA, "update-ref", ref2, c2)

	bPath := filepath.Join(t.TempDir(), "two.bundle")
	if _, _, err := client.BundleCreate(ctx, repoA, bPath, []string{ref1, ref2}, ""); err != nil {
		t.Fatal(err)
	}

	bareB := t.TempDir()
	runGit(t, bareB, "init", "--bare")

	fetched, err := client.FetchBundle(ctx, bareB, bPath, []string{ref1})
	if err != nil {
		t.Fatalf("FetchBundle: %v", err)
	}
	if len(fetched) != 1 || fetched[ref1] != c1 {
		t.Fatalf("unexpected fetched map: %v", fetched)
	}

	sha1, ok1, err := client.RefSHA(ctx, bareB, ref1)
	if err != nil || !ok1 || sha1 != c1 {
		t.Fatalf("bareB ref1: got (%q, %v, %v), want (%q, true, nil)", sha1, ok1, err, c1)
	}

	_, ok2, err := client.RefSHA(ctx, bareB, ref2)
	if err != nil || ok2 {
		t.Fatalf("bareB ref2 unexpectedly present: ok=%v, err=%v", ok2, err)
	}
}

func TestMergeFF(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	if err := os.WriteFile(filepath.Join(repoDir, "f1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "f1.txt")
	runGit(t, repoDir, "commit", "-m", "c1")
	c1 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	wtDir := filepath.Join(t.TempDir(), "wt")
	if err := client.AddWorktree(ctx, repoDir, wtDir, "feature", c1); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// 1. Fast-forward succeeds and moves the worktree
	if err := os.WriteFile(filepath.Join(repoDir, "f2.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "f2.txt")
	runGit(t, repoDir, "commit", "-m", "c2")
	c2 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	refOut := "refs/relevo/test/out"
	runGit(t, repoDir, "update-ref", refOut, c2)

	if err := client.MergeFF(ctx, wtDir, refOut); err != nil {
		t.Fatalf("MergeFF expected success, got %v", err)
	}
	head, err := client.HeadCommit(ctx, wtDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if head != c2 {
		t.Fatalf("wt HEAD = %s, want %s", head, c2)
	}
	if _, err := os.Stat(filepath.Join(wtDir, "f2.txt")); err != nil {
		t.Fatalf("f2.txt missing in worktree after ff: %v", err)
	}

	// 2. Diverged ref -> ErrNotFastForward
	if err := os.WriteFile(filepath.Join(wtDir, "wt_only.txt"), []byte("wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtDir, "add", "wt_only.txt")
	runGit(t, wtDir, "commit", "-m", "c3 in wt")

	if err := os.WriteFile(filepath.Join(repoDir, "repo_only.txt"), []byte("repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "repo_only.txt")
	runGit(t, repoDir, "commit", "-m", "c4 in repo")
	c4 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	refDiverged := "refs/relevo/test/diverged"
	runGit(t, repoDir, "update-ref", refDiverged, c4)

	err = client.MergeFF(ctx, wtDir, refDiverged)
	if !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("MergeFF on diverged ref: got %v, want ErrNotFastForward", err)
	}

	// 3. Dirty file the update touches -> ErrMergeConflict
	// Create another commit on repoDir modifying f2.txt from c2 (or c4)
	wtHead, err := client.HeadCommit(ctx, wtDir)
	if err != nil {
		t.Fatal(err)
	}
	// Commit on top of wtHead in repoDir
	runGit(t, repoDir, "checkout", "-b", "conflict-branch", wtHead)
	if err := os.WriteFile(filepath.Join(repoDir, "f2.txt"), []byte("modified in conflict branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "f2.txt")
	runGit(t, repoDir, "commit", "-m", "conflict commit")
	conflictHead := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	refConflict := "refs/relevo/test/conflict"
	runGit(t, repoDir, "update-ref", refConflict, conflictHead)

	// Make f2.txt dirty in wtDir
	if err := os.WriteFile(filepath.Join(wtDir, "f2.txt"), []byte("dirty in wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = client.MergeFF(ctx, wtDir, refConflict)
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("MergeFF with dirty conflicting file: got %v, want ErrMergeConflict", err)
	}
}

func TestInitBare(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	barePath := filepath.Join(t.TempDir(), "sub", "test.git")

	// Creates bare repo
	if err := client.InitBare(ctx, barePath); err != nil {
		t.Fatalf("InitBare failed: %v", err)
	}
	headFile := filepath.Join(barePath, "HEAD")
	if _, err := os.Stat(headFile); err != nil {
		t.Fatalf("HEAD file does not exist: %v", err)
	}

	// Second call is a no-op
	if err := client.InitBare(ctx, barePath); err != nil {
		t.Fatalf("second InitBare failed: %v", err)
	}
}

func TestCreateBranch(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := t.TempDir()

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "initial")

	headSHA, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}

	// Create branch succeeds
	if err := client.CreateBranch(ctx, repoDir, "feature", headSHA); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Verify branch resolves to headSHA
	sha, ok, err := client.RefSHA(ctx, repoDir, "refs/heads/feature")
	if err != nil || !ok {
		t.Fatalf("RefSHA failed: ok=%v, err=%v", ok, err)
	}
	if sha != headSHA {
		t.Fatalf("branch sha = %q, want %q", sha, headSHA)
	}

	// Second create on same branch returns ErrBranchExists
	err = client.CreateBranch(ctx, repoDir, "feature", headSHA)
	if !errors.Is(err, ErrBranchExists) {
		t.Fatalf("second CreateBranch got %v, want ErrBranchExists", err)
	}

	// Creating with refs/heads/ prefix also returns ErrBranchExists
	err = client.CreateBranch(ctx, repoDir, "refs/heads/feature", headSHA)
	if !errors.Is(err, ErrBranchExists) {
		t.Fatalf("prefixed CreateBranch got %v, want ErrBranchExists", err)
	}
}

func TestDeleteBranch(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := t.TempDir()

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "initial")
	headSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	// An existing branch is removed.
	if err := client.CreateBranch(ctx, repoDir, "feature", headSHA); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := client.DeleteBranch(ctx, repoDir, "feature"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	exists, err := client.BranchExists(ctx, repoDir, "feature")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if exists {
		t.Fatal("branch still exists after DeleteBranch")
	}

	// A missing branch is nil (idempotent) -- both a branch just removed and
	// one that never existed.
	if err := client.DeleteBranch(ctx, repoDir, "feature"); err != nil {
		t.Fatalf("DeleteBranch on already-removed branch: got %v, want nil", err)
	}
	if err := client.DeleteBranch(ctx, repoDir, "never-existed"); err != nil {
		t.Fatalf("DeleteBranch on never-created branch: got %v, want nil", err)
	}

	// Prefixed form addresses the same branch.
	if err := client.CreateBranch(ctx, repoDir, "feature2", headSHA); err != nil {
		t.Fatalf("CreateBranch feature2: %v", err)
	}
	if err := client.DeleteBranch(ctx, repoDir, "refs/heads/feature2"); err != nil {
		t.Fatalf("DeleteBranch with refs/heads/ prefix: %v", err)
	}
	exists, err = client.BranchExists(ctx, repoDir, "feature2")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if exists {
		t.Fatal("feature2 still exists after prefixed DeleteBranch")
	}
}

// TestCreateTrackingBranch creates a local branch for a ref that exists only
// on origin, and refuses a second time with ErrBranchExists.
func TestCreateTrackingBranch(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// Repo A has one commit; B is a bare clone of A that A then fetches from,
	// so refs/remotes/origin/feat can exist while refs/heads/feat does not.
	repoA := initRepo(t)
	if err := os.WriteFile(filepath.Join(repoA, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "file.txt")
	runGit(t, repoA, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "commit", "-m", "first")
	sha := strings.TrimSpace(runGit(t, repoA, "rev-parse", "HEAD"))

	repoB := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(repoB), "clone", "--bare", repoA, repoB)
	runGit(t, repoA, "remote", "add", "origin", repoB)
	runGit(t, repoA, "fetch", "origin")

	// feat exists only as refs/remotes/origin/feat: create it in B, fetch in A.
	runGit(t, repoB, "branch", "feat", sha)
	runGit(t, repoA, "fetch", "origin")

	if exists, err := client.BranchExists(ctx, repoA, "feat"); err != nil || exists {
		t.Fatalf("feat must not exist locally before tracking: exists=%v err=%v", exists, err)
	}

	if err := client.CreateTrackingBranch(ctx, repoA, "feat", "origin/feat"); err != nil {
		t.Fatalf("CreateTrackingBranch: %v", err)
	}
	up := strings.TrimSpace(runGit(t, repoA, "rev-parse", "--abbrev-ref", "feat@{u}"))
	if up != "origin/feat" {
		t.Fatalf("feat upstream = %q, want origin/feat", up)
	}

	// Calling it again is refused: the branch now exists.
	if err := client.CreateTrackingBranch(ctx, repoA, "feat", "origin/feat"); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("second CreateTrackingBranch = %v, want ErrBranchExists", err)
	}
}

// requireGit skips the test when git is not on PATH: RepoFacts's tests spin
// up throwaway repositories, which is pointless on a machine without git.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// initRepo creates a throwaway git repository in t.TempDir(), with
// commit/tag signing off so a fixture commit never tries to reach gpg.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	return dir
}

func TestListTagsPeelsAnnotated(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := initRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "a.txt")
	runGit(t, repoDir, "commit", "-m", "first")
	sha1 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	runGit(t, repoDir, "-c", "tag.gpgsign=false", "tag", "lw")

	if err := os.WriteFile(filepath.Join(repoDir, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "b.txt")
	runGit(t, repoDir, "commit", "-m", "second")
	sha2 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	runGit(t, repoDir, "-c", "tag.gpgsign=false", "tag", "-a", "-m", "x", "an")

	tags, err := client.ListTags(ctx, repoDir)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("ListTags = %v, want 2 tags", tags)
	}
	if tags["lw"] != sha1 {
		t.Errorf("tags[lw] = %q, want %q", tags["lw"], sha1)
	}
	if tags["an"] != sha2 {
		t.Errorf("tags[an] = %q, want %q (the peeled commit, not the tag object)", tags["an"], sha2)
	}

	untagged := initRepo(t)
	empty, err := client.ListTags(ctx, untagged)
	if err != nil {
		t.Fatalf("ListTags (untagged): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListTags (untagged) = %v, want an empty map", empty)
	}
}

func TestRepoFactsNoRemote(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := initRepo(t)

	originURL, commonDir, err := client.RepoFacts(ctx, repoDir)
	if err != nil {
		t.Fatalf("RepoFacts: %v", err)
	}
	if originURL != "" {
		t.Errorf("originURL = %q, want empty", originURL)
	}
	wantCommonDir, err := filepath.EvalSymlinks(filepath.Join(repoDir, ".git")) // canonical, as RepoFacts returns it (macOS /var -> /private/var)
	if err != nil {
		t.Fatal(err)
	}
	if commonDir != wantCommonDir {
		t.Errorf("commonDir = %q, want %q", commonDir, wantCommonDir)
	}
	if !filepath.IsAbs(commonDir) {
		t.Errorf("commonDir %q is not absolute", commonDir)
	}
}

func TestRepoFactsWithOrigin(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := initRepo(t)
	runGit(t, repoDir, "remote", "add", "origin", "git@github.com:o/r.git")

	originURL, commonDir, err := client.RepoFacts(ctx, repoDir)
	if err != nil {
		t.Fatalf("RepoFacts: %v", err)
	}
	if originURL != "git@github.com:o/r.git" {
		t.Errorf("originURL = %q, want the raw remote value unnormalised", originURL)
	}
	wantCommonDir, err := filepath.EvalSymlinks(filepath.Join(repoDir, ".git")) // canonical, as RepoFacts returns it (macOS /var -> /private/var)
	if err != nil {
		t.Fatal(err)
	}
	if commonDir != wantCommonDir {
		t.Errorf("commonDir = %q, want %q", commonDir, wantCommonDir)
	}
}

func TestRepoFactsFromWorktree(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := initRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "initial")
	headSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	if err := client.CreateBranch(ctx, repoDir, "wt", headSHA); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	worktreeDir := filepath.Join(t.TempDir(), "wt")
	if err := client.CheckoutWorktree(ctx, repoDir, worktreeDir, "wt"); err != nil {
		t.Fatalf("CheckoutWorktree: %v", err)
	}

	_, mainCommonDir, err := client.RepoFacts(ctx, repoDir)
	if err != nil {
		t.Fatalf("RepoFacts(main): %v", err)
	}
	_, worktreeCommonDir, err := client.RepoFacts(ctx, worktreeDir)
	if err != nil {
		t.Fatalf("RepoFacts(worktree): %v", err)
	}
	if worktreeCommonDir != mainCommonDir {
		t.Errorf("RepoFacts(worktree) commonDir = %q, want the main tree's %q", worktreeCommonDir, mainCommonDir)
	}
}

func TestRepoFactsNotARepo(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	dir := t.TempDir()

	_, _, err := client.RepoFacts(ctx, dir)
	if err == nil {
		t.Fatal("RepoFacts on a non-repo directory: got nil error, want one")
	}
}

// TestIdentityReadsConfig pins Identity's happy path (#335): a repo with its
// own user.name/user.email resolves both, which is the identity a remote
// builder then commits as.
func TestIdentityReadsConfig(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := initRepo(t) // repo-local user.name "Test", user.email "test@example.com"

	name, email, err := client.Identity(ctx, repoDir)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if name != "Test" || email != "test@example.com" {
		t.Errorf("Identity = (%q, %q), want (%q, %q)", name, email, "Test", "test@example.com")
	}
}

// TestIdentityUnsetIsEmptyNotError pins Identity's other contract (#335): a
// key `git config --get` cannot find exits 1 with no output, which is an
// empty value, not a failure. HOME and XDG_CONFIG_HOME point at empty temp
// dirs and the system config is off, so nothing can resolve a global
// user.name/user.email from the machine this test runs on.
func TestIdentityUnsetIsEmptyNotError(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(emptyHome, "xdg"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	repoDir := t.TempDir()
	runGit(t, repoDir, "init")

	name, email, err := client.Identity(ctx, repoDir)
	if err != nil {
		t.Fatalf("Identity with nothing set: %v", err)
	}
	if name != "" || email != "" {
		t.Errorf("Identity = (%q, %q), want (\"\", \"\")", name, email)
	}
}

// TestCommitAllCommitsEverything pins CommitAll's contract: it stages and
// commits the whole working tree (a new untracked file and a tracked
// modification alike), reports the new HEAD, and answers a tree with nothing
// left to commit with ("", nil) rather than an error.
func TestCommitAllCommitsEverything(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	sha, err := client.CommitAll(ctx, dir, "msg")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if sha == "" {
		t.Fatal("CommitAll returned an empty sha")
	}

	if got := strings.TrimSpace(runGit(t, dir, "status", "--porcelain")); got != "" {
		t.Errorf("status after CommitAll = %q, want clean", got)
	}
	if got := strings.TrimSpace(runGit(t, dir, "log", "-1", "--format=%s")); got != "msg" {
		t.Errorf("last commit subject = %q, want msg", got)
	}
	if head := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")); head != sha {
		t.Errorf("HEAD = %q, want returned sha %q", head, sha)
	}

	// Nothing left to commit: ("", nil), not an error.
	sha2, err := client.CommitAll(ctx, dir, "again")
	if err != nil {
		t.Fatalf("second CommitAll: %v", err)
	}
	if sha2 != "" {
		t.Errorf("second CommitAll sha = %q, want empty", sha2)
	}
}

// TestTreeFingerprintChangesOnEditAndCommit pins #135's tree signal: the same
// tree twice is one fingerprint; an edit of a tracked file is a second; a
// commit is a third; an untracked file is a fourth.
func TestTreeFingerprintChangesOnEditAndCommit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-m", "initial")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	first, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint: %v", err)
	}
	again, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (again): %v", err)
	}
	if first != again {
		t.Fatalf("fingerprint changed with no edit: %q then %q", first, again)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (edited): %v", err)
	}
	if edited == first {
		t.Fatalf("fingerprint unchanged after editing a tracked file: %q", edited)
	}

	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-m", "edit")
	committed, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (committed): %v", err)
	}
	if committed == edited {
		t.Fatalf("fingerprint unchanged after committing: %q", committed)
	}

	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (untracked): %v", err)
	}
	if untracked == committed {
		t.Fatalf("fingerprint unchanged after adding an untracked file: %q", untracked)
	}
}

// TestTreeFingerprintUnbornHead pins the branch a fresh repo lands on: rev-parse
// HEAD fails with no commit, and the fingerprint must still be taken from the
// status alone rather than erroring.
func TestTreeFingerprintUnbornHead(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	runGit(t, dir, "init")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	fp, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint on an unborn branch: %v", err)
	}
	if fp == "" {
		t.Fatal("fingerprint on an unborn branch is empty")
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (untracked): %v", err)
	}
	if edited == fp {
		t.Fatalf("fingerprint unchanged after adding an untracked file: %q", edited)
	}
}

// TestAddDetachedWorktree pins #144's throwaway tree: AddDetachedWorktree
// produces a working tree at the requested commit with a DETACHED HEAD (no
// branch, so `git symbolic-ref -q HEAD` fails), leaves the source tree's
// status and HEAD untouched, and the tree is removable afterwards.
func TestAddDetachedWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "first commit")
	commit1 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "commit", "-am", "second commit")
	commit2 := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	statusBefore := runGit(t, repoDir, "status", "--porcelain")
	branchesBefore := runGit(t, repoDir, "branch", "--format=%(refname)")

	wtDir := filepath.Join(t.TempDir(), "verify-001")
	if err := client.AddDetachedWorktree(ctx, repoDir, wtDir, commit1); err != nil {
		t.Fatalf("AddDetachedWorktree failed: %v", err)
	}

	if fi, err := os.Stat(wtDir); err != nil || !fi.IsDir() {
		t.Fatalf("worktree directory %s does not exist or is not a directory", wtDir)
	}

	// HEAD is the requested commit...
	wtHead, err := client.HeadCommit(ctx, wtDir)
	if err != nil {
		t.Fatalf("HeadCommit on worktree: %v", err)
	}
	if wtHead != commit1 {
		t.Fatalf("worktree HEAD is %s, want %s", wtHead, commit1)
	}

	// ...and detached: symbolic-ref refuses to name a branch for it.
	cmd := exec.Command("git", "-C", wtDir, "symbolic-ref", "-q", "HEAD")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("symbolic-ref succeeded on a detached worktree: %s", string(out))
	}

	// The source tree is untouched.
	statusAfter := runGit(t, repoDir, "status", "--porcelain")
	if statusAfter != statusBefore {
		t.Fatalf("source tree status changed: got %q, want %q", statusAfter, statusBefore)
	}
	srcHead, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit on repo: %v", err)
	}
	if srcHead != commit2 {
		t.Fatalf("repo HEAD is %s, want %s", srcHead, commit2)
	}
	// No branch was created for the throwaway tree.
	if branchesAfter := runGit(t, repoDir, "branch", "--format=%(refname)"); branchesAfter != branchesBefore {
		t.Fatalf("branches changed: got %q, want %q", branchesAfter, branchesBefore)
	}

	// Removing it works, and removes the directory.
	if err := client.RemoveWorktree(ctx, repoDir, wtDir, true); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after removal: %v", err)
	}
}

// TestMaterializeTreeCarriesWorkingState pins the scratch worktree primitive
// (2026-09-24-cockpit-design.md §3.4): a fresh detached worktree receives the
// source's whole working state -- uncommitted edits, staged edits, deletions
// and untracked files, ignored files excluded -- as UNSTAGED changes, and the
// source's own index, status and branches are untouched.
func TestMaterializeTreeCarriesWorkingState(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "a.txt", "a1\n")
	writeGitFile(t, repoDir, "b.txt", "b1\n")
	writeGitFile(t, repoDir, "c.txt", "c1\n")
	writeGitFile(t, repoDir, ".gitignore", "*.log\n")
	runGit(t, repoDir, "add", "a.txt", "b.txt", "c.txt", ".gitignore")
	runGit(t, repoDir, "commit", "-m", "first")

	// The binding's working state, uncommitted: an unstaged edit, a staged
	// edit, a deletion, an untracked file and an ignored one.
	writeGitFile(t, repoDir, "a.txt", "a2\n")
	writeGitFile(t, repoDir, "b.txt", "b2\n")
	runGit(t, repoDir, "add", "b.txt")
	if err := os.Remove(filepath.Join(repoDir, "c.txt")); err != nil {
		t.Fatal(err)
	}
	writeGitFile(t, repoDir, "d.txt", "d1\n")
	writeGitFile(t, repoDir, "e.log", "ignored\n")

	statusBefore := runGit(t, repoDir, "status", "--porcelain")
	branchesBefore := runGit(t, repoDir, "branch", "--list")
	indexBefore, err := os.ReadFile(filepath.Join(repoDir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	head, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}

	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := client.AddDetachedWorktree(ctx, repoDir, scratch, head); err != nil {
		t.Fatalf("AddDetachedWorktree: %v", err)
	}
	if err := client.MaterializeTree(ctx, scratch, tree); err != nil {
		t.Fatalf("MaterializeTree: %v", err)
	}

	// The scratch worktree holds the snapshot.
	for _, c := range []struct{ name, want string }{
		{"a.txt", "a2\n"},
		{"b.txt", "b2\n"},
		{"d.txt", "d1\n"},
	} {
		got, err := os.ReadFile(filepath.Join(scratch, c.name))
		if err != nil {
			t.Fatalf("scratch %s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("scratch %s = %q, want %q", c.name, got, c.want)
		}
	}
	for _, name := range []string{"c.txt", "e.log"} {
		if _, err := os.Stat(filepath.Join(scratch, name)); !os.IsNotExist(err) {
			t.Errorf("scratch %s should be absent, stat err = %v", name, err)
		}
	}

	if got := strings.TrimSpace(runGit(t, scratch, "rev-parse", "HEAD")); got != head {
		t.Errorf("scratch HEAD = %s, want %s", got, head)
	}

	// Every difference is unstaged, and the untracked file stays untracked.
	wantStatus := " M a.txt\n M b.txt\n D c.txt\n?? d.txt\n"
	if got := runGit(t, scratch, "status", "--porcelain"); got != wantStatus {
		t.Errorf("scratch status --porcelain =\n%q\nwant\n%q", got, wantStatus)
	}

	// The source is untouched.
	if got := runGit(t, repoDir, "status", "--porcelain"); got != statusBefore {
		t.Errorf("source status changed: got %q, want %q", got, statusBefore)
	}
	indexAfter, err := os.ReadFile(filepath.Join(repoDir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexAfter, indexBefore) {
		t.Error("source index changed")
	}
	if got := runGit(t, repoDir, "branch", "--list"); got != branchesBefore {
		t.Errorf("source branches changed: got %q, want %q", got, branchesBefore)
	}
}

// TestScratchWritesNeverReachTheSource pins the other half of the primitive
// (2026-09-24-cockpit-design.md §3.4): writes in the scratch worktree stay
// there, and removing it leaves the source's worktree list clean.
func TestScratchWritesNeverReachTheSource(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "a.txt", "a1\n")
	writeGitFile(t, repoDir, "b.txt", "b1\n")
	writeGitFile(t, repoDir, "c.txt", "c1\n")
	writeGitFile(t, repoDir, ".gitignore", "*.log\n")
	runGit(t, repoDir, "add", "a.txt", "b.txt", "c.txt", ".gitignore")
	runGit(t, repoDir, "commit", "-m", "first")

	writeGitFile(t, repoDir, "a.txt", "a2\n")
	writeGitFile(t, repoDir, "b.txt", "b2\n")
	runGit(t, repoDir, "add", "b.txt")
	if err := os.Remove(filepath.Join(repoDir, "c.txt")); err != nil {
		t.Fatal(err)
	}
	writeGitFile(t, repoDir, "d.txt", "d1\n")
	writeGitFile(t, repoDir, "e.log", "ignored\n")

	statusBefore := runGit(t, repoDir, "status", "--porcelain")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	head, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}

	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := client.AddDetachedWorktree(ctx, repoDir, scratch, head); err != nil {
		t.Fatalf("AddDetachedWorktree: %v", err)
	}
	if err := client.MaterializeTree(ctx, scratch, tree); err != nil {
		t.Fatalf("MaterializeTree: %v", err)
	}

	// Write in the scratch: nothing of it reaches the source.
	writeGitFile(t, scratch, "z.txt", "z\n")
	writeGitFile(t, scratch, "a.txt", "scratch\n")

	if got := runGit(t, repoDir, "status", "--porcelain"); got != statusBefore {
		t.Errorf("source status changed: got %q, want %q", got, statusBefore)
	}
	if got, err := os.ReadFile(filepath.Join(repoDir, "a.txt")); err != nil || string(got) != "a2\n" {
		t.Errorf("source a.txt = %q (err %v), want %q", got, err, "a2\n")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "z.txt")); !os.IsNotExist(err) {
		t.Errorf("source gained z.txt: %v", err)
	}

	if err := client.RemoveWorktree(ctx, repoDir, scratch, true); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch still present after removal: %v", err)
	}
	if list := runGit(t, repoDir, "worktree", "list", "--porcelain"); strings.Contains(list, scratch) {
		t.Errorf("worktree list still names the scratch:\n%s", list)
	}
}

// writeGitFile writes one fixture file into dir, failing the test on error.
func writeGitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFetchRebasePush drives the land flow's git primitives against real
// repositories: a work clone on feat fetches and rebases onto origin/main
// after origin moves, pushes, and -- after origin moves again -- pushes again
// with --force-with-lease, which a rewritten branch needs.
func TestFetchRebasePush(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)

	seed := initRepo(t)
	runGit(t, seed, "checkout", "-b", "main")
	writeGitFile(t, seed, "base.txt", "v1\n")
	runGit(t, seed, "add", "base.txt")
	runGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-m", "main 1")

	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(origin), "clone", "--bare", seed, origin)
	runGit(t, seed, "remote", "add", "origin", origin)

	work := filepath.Join(t.TempDir(), "work")
	runGit(t, filepath.Dir(work), "clone", origin, work)
	runGit(t, work, "checkout", "-b", "feat")
	// A clone carries no local identity, and the client runs git without the
	// runGit fixture env: a rebase that replays a commit needs one here.
	runGit(t, work, "config", "user.name", "Test")
	runGit(t, work, "config", "user.email", "test@example.com")
	writeGitFile(t, work, "feat.txt", "feat\n")
	runGit(t, work, "add", "feat.txt")
	runGit(t, work, "-c", "commit.gpgsign=false", "commit", "-m", "feat 1")

	// 1. origin/main gains a commit the work clone has not seen.
	writeGitFile(t, seed, "main2.txt", "v2\n")
	runGit(t, seed, "add", "main2.txt")
	runGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-m", "main 2")
	runGit(t, seed, "push", "origin", "main")

	if err := client.Fetch(ctx, work, "origin", "main"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	paths, err := client.Rebase(ctx, work, "origin/main")
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("Rebase conflicts = %v, want none", paths)
	}
	// feat now contains origin's commit. runGit fails the test when this is
	// not an ancestor, which is exactly the assertion asked for.
	runGit(t, work, "merge-base", "--is-ancestor", "origin/main", "HEAD")

	if err := client.Push(ctx, work, "origin", "feat", false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	exists, err := client.RemoteBranchExists(ctx, work, "origin", "feat")
	if err != nil {
		t.Fatalf("RemoteBranchExists: %v", err)
	}
	if !exists {
		t.Fatal("RemoteBranchExists = false after Push, want true")
	}

	// 2. origin/main moves again, so the rebase that follows rewrites feat and
	// the push needs the lease.
	writeGitFile(t, seed, "main3.txt", "v3\n")
	runGit(t, seed, "add", "main3.txt")
	runGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-m", "main 3")
	runGit(t, seed, "push", "origin", "main")

	if err := client.Fetch(ctx, work, "origin", "main"); err != nil {
		t.Fatalf("Fetch (2): %v", err)
	}
	if paths, err := client.Rebase(ctx, work, "origin/main"); err != nil || len(paths) != 0 {
		t.Fatalf("Rebase (2) = (%v, %v), want no conflicts", paths, err)
	}
	if err := client.Push(ctx, work, "origin", "feat", true); err != nil {
		t.Fatalf("Push --force-with-lease: %v", err)
	}
}

// TestRebaseConflictAbortsAndListsPaths pins the conflict contract: unmerged
// paths come back, ErrMergeConflict is returned, the rebase is aborted, and
// the worktree and HEAD are exactly where they were.
func TestRebaseConflictAbortsAndListsPaths(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)

	seed := initRepo(t)
	runGit(t, seed, "checkout", "-b", "main")
	writeGitFile(t, seed, "file.txt", "base\n")
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-m", "base")

	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(origin), "clone", "--bare", seed, origin)
	runGit(t, seed, "remote", "add", "origin", origin)

	work := filepath.Join(t.TempDir(), "work")
	runGit(t, filepath.Dir(work), "clone", origin, work)
	runGit(t, work, "checkout", "-b", "feat")
	// A clone carries no local identity; see TestFetchRebasePush.
	runGit(t, work, "config", "user.name", "Test")
	runGit(t, work, "config", "user.email", "test@example.com")
	writeGitFile(t, work, "file.txt", "feat\n")
	runGit(t, work, "add", "file.txt")
	runGit(t, work, "-c", "commit.gpgsign=false", "commit", "-m", "feat edit")
	featHead := strings.TrimSpace(runGit(t, work, "rev-parse", "HEAD"))

	// origin/main edits the same file, so the rebase cannot apply cleanly.
	writeGitFile(t, seed, "file.txt", "main\n")
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-m", "main edit")
	runGit(t, seed, "push", "origin", "main")

	if err := client.Fetch(ctx, work, "origin", "main"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	paths, err := client.Rebase(ctx, work, "origin/main")
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("Rebase err = %v, want ErrMergeConflict", err)
	}
	if len(paths) != 1 || paths[0] != "file.txt" {
		t.Errorf("conflict paths = %v, want [file.txt]", paths)
	}
	if out := strings.TrimSpace(runGit(t, work, "status", "--porcelain")); out != "" {
		t.Errorf("worktree is not clean after rebase --abort: %q", out)
	}
	if got := strings.TrimSpace(runGit(t, work, "rev-parse", "HEAD")); got != featHead {
		t.Errorf("HEAD = %s, want %s (the abort must leave it where it was)", got, featHead)
	}
}

// TestMergeConflictAborts is TestRebaseConflictAbortsAndListsPaths for the
// --merge escape hatch.
func TestMergeConflictAborts(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)

	seed := initRepo(t)
	runGit(t, seed, "checkout", "-b", "main")
	writeGitFile(t, seed, "file.txt", "base\n")
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-m", "base")

	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(origin), "clone", "--bare", seed, origin)
	runGit(t, seed, "remote", "add", "origin", origin)

	work := filepath.Join(t.TempDir(), "work")
	runGit(t, filepath.Dir(work), "clone", origin, work)
	runGit(t, work, "checkout", "-b", "feat")
	// A clone carries no local identity; see TestFetchRebasePush.
	runGit(t, work, "config", "user.name", "Test")
	runGit(t, work, "config", "user.email", "test@example.com")
	writeGitFile(t, work, "file.txt", "feat\n")
	runGit(t, work, "add", "file.txt")
	runGit(t, work, "-c", "commit.gpgsign=false", "commit", "-m", "feat edit")
	featHead := strings.TrimSpace(runGit(t, work, "rev-parse", "HEAD"))

	writeGitFile(t, seed, "file.txt", "main\n")
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-m", "main edit")
	runGit(t, seed, "push", "origin", "main")

	if err := client.Fetch(ctx, work, "origin", "main"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	paths, err := client.Merge(ctx, work, "origin/main")
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("Merge err = %v, want ErrMergeConflict", err)
	}
	if len(paths) != 1 || paths[0] != "file.txt" {
		t.Errorf("conflict paths = %v, want [file.txt]", paths)
	}
	if out := strings.TrimSpace(runGit(t, work, "status", "--porcelain")); out != "" {
		t.Errorf("worktree is not clean after merge --abort: %q", out)
	}
	if got := strings.TrimSpace(runGit(t, work, "rev-parse", "HEAD")); got != featHead {
		t.Errorf("HEAD = %s, want %s (the abort must leave it where it was)", got, featHead)
	}
}

// TestCurrentBranchDetached: a detached HEAD is not a branch name, so
// CurrentBranch reports "" for it. `git rev-parse --abbrev-ref HEAD` answers
// the literal "HEAD" there, which land must never record as a base branch.
func TestCurrentBranchDetached(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := initRepo(t)
	writeGitFile(t, repo, "file.txt", "v1\n")
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-m", "first")

	name, err := client.CurrentBranch(ctx, repo)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if name == "" || name == "HEAD" {
		t.Errorf("CurrentBranch = %q, want the checked-out branch name", name)
	}

	sha := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "checkout", "--detach", sha)

	name, err = client.CurrentBranch(ctx, repo)
	if err != nil {
		t.Fatalf("CurrentBranch (detached): %v", err)
	}
	if name != "" {
		t.Errorf("CurrentBranch (detached) = %q, want %q", name, "")
	}
}

func TestNormalizeOriginURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"scp-like", "git@github.com:o/r.git", "https://github.com/o/r"},
		{"ssh scheme", "ssh://git@github.com/o/r.git", "https://github.com/o/r"},
		{"https trailing git and slash", "https://github.com/o/r.git/", "https://github.com/o/r"},
		{"http stays http, host lowercased, path case kept", "http://GitHub.com/o/R", "http://github.com/o/R"},
		{"unrecognised string returned trimmed", "  not a url  ", "not a url"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeOriginURL(c.in); got != c.want {
				t.Errorf("NormalizeOriginURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestClientSetsNoOptionalLocks pins relevo's git reads to GIT_OPTIONAL_LOCKS=0:
// without it a read can take the repository's index.lock, and that lock is what
// fails a builder's concurrent commit in the same worktree.
//
// The client's binary is a shell script in place of git, because NewClient
// takes the binary path. It prints the variable's value where each path parses
// its output: plain text for run, a numstat line plus the patch for DiffTrees.
func TestClientSetsNoOptionalLocks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	fakeGit := filepath.Join(dir, "fake-git")
	script := `#!/bin/sh
case "$*" in
*--numstat*)
	printf '1\t1\tfile.txt\n'
	;;
*diff*)
	printf 'GIT_OPTIONAL_LOCKS=%s\n' "${GIT_OPTIONAL_LOCKS:-unset}"
	;;
*)
	printf '%s\n' "${GIT_OPTIONAL_LOCKS:-unset}"
	;;
esac
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	client := NewClient(fakeGit, 5*time.Second, DefaultMaxPatchBytes)

	// The run path: every Client method but DiffTrees' patch read uses it.
	sha, err := client.HeadCommit(ctx, dir)
	if err != nil {
		t.Fatalf("HeadCommit through the fake git: %v", err)
	}
	if sha != "0" {
		t.Errorf("run path GIT_OPTIONAL_LOCKS = %q, want %q", sha, "0")
	}

	// The diff path: DiffTrees' patch read builds its own exec.Command, so it
	// does not inherit run's environment by construction.
	d, err := client.DiffTrees(ctx, dir, "from", "to")
	if err != nil {
		t.Fatalf("DiffTrees through the fake git: %v", err)
	}
	if got := strings.TrimSpace(string(d.Patch)); got != "GIT_OPTIONAL_LOCKS=0" {
		t.Errorf("diff path reported %q, want GIT_OPTIONAL_LOCKS=0", got)
	}
}

// TestStatusDoesNotTakeIndexLock is the regression test for the collision that
// failed TestRemoteTierOverWire: relevo's git read took the index lock while a
// builder was committing in the same worktree, and the builder's commit died
// with `fatal: Unable to create '.../index.lock': File exists`.
//
// Real git, two assertions. First, a status read over an index that a plain
// `git status` would refresh leaves .git/index byte-for-byte alone: writing it
// means taking index.lock, and it is GIT_OPTIONAL_LOCKS=0 that makes git skip
// that write (git asks for the index lock only when optional locks are on).
// Drop the variable from gitEnv and this assertion fails, because git then
// rewrites the index. Second, with .git/index.lock held by hand the way a
// concurrent git write holds it, the read still succeeds.
func TestStatusDoesNotTakeIndexLock(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	tracked := filepath.Join(repoDir, "a.txt")
	if err := os.WriteFile(tracked, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGit(t, repoDir, "add", "a.txt")
	runGit(t, repoDir, "commit", "-qm", "init")

	// Backdate the tracked file: the index's recorded stat data is then stale,
	// so a plain `git status` re-hashes the file, finds its content unchanged,
	// and rewrites the index to record the new mtime.
	stale := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(tracked, stale, stale); err != nil {
		t.Fatalf("backdate a.txt: %v", err)
	}

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	indexPath := filepath.Join(repoDir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read .git/index: %v", err)
	}

	dirty, err := client.Dirty(ctx, repoDir)
	if err != nil {
		t.Fatalf("Dirty with a stale index: %v", err)
	}
	if dirty {
		t.Error("Dirty reported a change, but a.txt's content is unchanged")
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read .git/index after the status read: %v", err)
	}
	if !bytes.Equal(indexBefore, indexAfter) {
		t.Error("the status read rewrote .git/index, so it took index.lock; a builder's commit in this worktree would fail against it")
	}

	// The same read with the lock held by hand, as another process would hold
	// it: relevo's read must still succeed.
	lockPath := filepath.Join(repoDir, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("hold .git/index.lock: %v", err)
	}
	defer os.Remove(lockPath)

	if _, err := client.Dirty(ctx, repoDir); err != nil {
		t.Fatalf("Dirty while .git/index.lock is held: %v", err)
	}
}

// TestClientRefs covers the two ref helpers the server's settled-binding
// cleanup uses (#7): create refs, list by prefix, delete one, and delete a
// missing one.
func TestClientRefs(t *testing.T) {
	ctx := context.Background()
	c := NewClient("git", 0, 0)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "initial")
	sha := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	for _, ref := range []string{
		"refs/relevo/api/out",
		"refs/relevo/api/round-1",
		"refs/relevo/other/out",
	} {
		if err := c.UpdateRef(ctx, repo, ref, sha, ""); err != nil {
			t.Fatalf("update-ref %s: %v", ref, err)
		}
	}

	got, err := c.ListRefs(ctx, repo, "refs/relevo/api/")
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	want := []string{"refs/relevo/api/out", "refs/relevo/api/round-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ListRefs = %v, want %v", got, want)
	}

	if err := c.DeleteRef(ctx, repo, "refs/relevo/api/out"); err != nil {
		t.Fatalf("DeleteRef existing: %v", err)
	}
	got, err = c.ListRefs(ctx, repo, "refs/relevo/api/")
	if err != nil {
		t.Fatalf("ListRefs after delete: %v", err)
	}
	if strings.Join(got, ",") != "refs/relevo/api/round-1" {
		t.Fatalf("ListRefs after delete = %v, want [refs/relevo/api/round-1]", got)
	}

	// A missing ref is success, and deleting it changes nothing.
	if err := c.DeleteRef(ctx, repo, "refs/relevo/api/out"); err != nil {
		t.Fatalf("DeleteRef missing: %v", err)
	}

	// The other binding's ref is untouched.
	if gotSHA, ok, err := c.RefSHA(ctx, repo, "refs/relevo/other/out"); err != nil || !ok || gotSHA != sha {
		t.Fatalf("refs/relevo/other/out = (%q, %v, %v), want %q", gotSHA, ok, err, sha)
	}
}

// TestRemoveWorktreeAlreadyGone: a worktree whose directory is already gone
// (removed by hand, or by an earlier relevo done) is removed successfully and
// its stale registration pruned, so its branch can then be deleted -- the
// case the served-binding cleanup hit on contabo (git: "is not a working tree").
func TestRemoveWorktreeAlreadyGone(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 10*time.Second, 0)
	repoDir := initRepo(t)
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "first commit")
	head := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	wtDir := filepath.Join(t.TempDir(), "gone")
	if err := client.AddWorktree(ctx, repoDir, wtDir, "relevo/gone", head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := os.RemoveAll(wtDir); err != nil {
		t.Fatal(err)
	}
	// The contabo state: the directory is gone AND git no longer lists it, so
	// a bare `git worktree remove` says "is not a working tree".
	runGit(t, repoDir, "worktree", "prune")
	if err := client.RemoveWorktree(ctx, repoDir, wtDir, true); err != nil {
		t.Fatalf("RemoveWorktree on a gone path = %v, want nil", err)
	}
	if err := client.DeleteBranch(ctx, repoDir, "relevo/gone"); err != nil {
		t.Fatalf("DeleteBranch after removing a gone worktree = %v, want nil (stale registration pruned)", err)
	}
}

// TestSnapshotTreeCleanTreeNeedsNoTempIndex snapshots a repo with one commit
// and a clean tree while TMPDIR points at a directory that does not exist, so
// the temp-index path's os.MkdirTemp("", ...) would fail if it ran.
//
// Mutation: remove the clean fast path and MkdirTemp fails under the missing TMPDIR.
func TestSnapshotTreeCleanTreeNeedsNoTempIndex(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "a.txt", "hello\n")
	runGit(t, repoDir, "add", "a.txt")
	runGit(t, repoDir, "commit", "-m", "first")

	// A missing TMPDIR makes os.MkdirTemp("", ...) fail: only the clean fast
	// path can produce a tree here.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	want := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD^{tree}"))
	got, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree on a clean tree: %v", err)
	}
	if got != want {
		t.Fatalf("SnapshotTree = %s, want HEAD^{tree} = %s", got, want)
	}

	// Ignored files do not count as dirty: a clean repo with a committed
	// .gitignore and an untracked, ignored x.log still returns HEAD^{tree}.
	writeGitFile(t, repoDir, ".gitignore", "*.log\n")
	runGit(t, repoDir, "add", ".gitignore")
	runGit(t, repoDir, "commit", "-m", "ignore logs")
	writeGitFile(t, repoDir, "x.log", "noise\n")

	wantIgnored := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD^{tree}"))
	gotIgnored, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree with an ignored file: %v", err)
	}
	if gotIgnored != wantIgnored {
		t.Fatalf("SnapshotTree with an ignored file = %s, want HEAD^{tree} = %s", gotIgnored, wantIgnored)
	}
}

// TestSnapshotTreeDirtyTreeCapturesChanges snapshots a repo whose only change
// is one untracked, non-ignored file: the result must be a fresh tree, not
// HEAD^{tree}, and the repo's index must be left untouched.
func TestSnapshotTreeDirtyTreeCapturesChanges(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "a.txt", "hello\n")
	runGit(t, repoDir, "add", "a.txt")
	runGit(t, repoDir, "commit", "-m", "first")

	writeGitFile(t, repoDir, "new_file.txt", "line1\nline2\n")

	statusBefore := runGit(t, repoDir, "status", "--porcelain")
	headTree := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD^{tree}"))

	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree on a dirty tree: %v", err)
	}
	if tree == headTree {
		t.Fatalf("SnapshotTree = HEAD^{tree} (%s) with an untracked file present", tree)
	}

	lsTree := runGit(t, repoDir, "ls-tree", "-r", "--name-only", tree)
	if !strings.Contains(lsTree, "new_file.txt") {
		t.Fatalf("tree %s does not list new_file.txt:\n%s", tree, lsTree)
	}

	statusAfter := runGit(t, repoDir, "status", "--porcelain")
	if statusAfter != statusBefore {
		t.Fatalf("git status changed after snapshot: got %q, want %q", statusAfter, statusBefore)
	}
}

// TestSnapshotTreeUnbornHeadFallsBack snapshots a repo that has no commits
// yet: HEAD^{tree} does not resolve, so SnapshotTree must fall through to the
// existing temp-index path and still capture the untracked file.
func TestSnapshotTreeUnbornHeadFallsBack(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "new_file.txt", "line1\n")

	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree with an unborn HEAD: %v", err)
	}

	lsTree := runGit(t, repoDir, "ls-tree", "-r", "--name-only", tree)
	if !strings.Contains(lsTree, "new_file.txt") {
		t.Fatalf("tree %s does not list new_file.txt:\n%s", tree, lsTree)
	}
}
