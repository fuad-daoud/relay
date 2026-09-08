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

	badClient := NewClient("nonexistent-git-binary-xyz", 5*time.Second, DefaultMaxPatchBytes)
	_, err = badClient.SnapshotTree(ctx, notRepoDir)
	if !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("got %v, want ErrGitUnavailable", err)
	}
}
