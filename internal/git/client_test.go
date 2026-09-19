package git

import (
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
	branch := "relay/test-wt"
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
	branch3 := "relay/test-wt3"
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
	err = client.AddWorktree(ctx, repoDir, wtDirBad, "relay/bad", "0000000000000000000000000000000000000000")
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

	// seed it by cloning, committing one file and pushing refs/heads/relay/api
	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relay/api")

	// git worktree add from the bare repo at that branch
	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relay/api")

	// write an untracked file in the worktree
	if err := os.WriteFile(filepath.Join(wt, "untracked.txt"), []byte("dirty work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// SnapshotTree on the worktree
	tree, err := client.SnapshotTree(ctx, wt)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}

	head, ok, err := client.RefSHA(ctx, bare, "refs/heads/relay/api")
	if err != nil || !ok {
		t.Fatalf("RefSHA(bare): %v, ok=%v", err, ok)
	}

	// CommitTree(bare, tree, head, msg)
	msg := "[relay] api: round 1, uncommitted work"
	sha, err := client.CommitTree(ctx, bare, tree, head, msg)
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}

	// UpdateRef(bare, "refs/relay/api/round-1", sha, "")
	sideRef := "refs/relay/api/round-1"
	if err := client.UpdateRef(ctx, bare, sideRef, sha, ""); err != nil {
		t.Fatalf("UpdateRef(sideRef): %v", err)
	}

	// assert git cat-file -p <sha> in the bare repo shows tree, parent, author relay <relay@localhost>
	catOut := runGit(t, bare, "cat-file", "-p", sha)
	if !strings.Contains(catOut, "tree "+tree) {
		t.Errorf("cat-file missing tree %q in:\n%s", tree, catOut)
	}
	if !strings.Contains(catOut, "parent "+head) {
		t.Errorf("cat-file missing parent %q in:\n%s", head, catOut)
	}
	if !strings.Contains(catOut, "author relay <relay@localhost>") {
		t.Errorf("cat-file missing relay author in:\n%s", catOut)
	}
	if !strings.Contains(catOut, "committer relay <relay@localhost>") {
		t.Errorf("cat-file missing relay committer in:\n%s", catOut)
	}

	// refs/heads/relay/api still equals head
	headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relay/api")
	if err != nil || !ok || headAfter != head {
		t.Fatalf("refs/heads/relay/api changed: got %q, want %q", headAfter, head)
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

	ref1 := "refs/heads/relay/api"
	ref2 := "refs/relay/api/round-1"
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
	ref := "refs/heads/relay/api"
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
	ref := "refs/heads/relay/api"
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

	ref := "refs/heads/relay/api"
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

	ref1 := "refs/heads/relay/api"
	ref2 := "refs/relay/api/round-1"
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

	refOut := "refs/relay/test/out"
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
	refDiverged := "refs/relay/test/diverged"
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
	refConflict := "refs/relay/test/conflict"
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
