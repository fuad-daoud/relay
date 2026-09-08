package git

import (
	"context"
	"errors"
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
