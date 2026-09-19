package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client runs the git CLI to capture tree snapshots and compare trees.
type Client struct {
	bin           string
	timeout       time.Duration
	maxPatchBytes int
}

// NewClient returns a Client invoking bin, defaulting to "git", a 10s timeout
// and DefaultMaxPatchBytes. Every call it makes is bounded by timeout.
func NewClient(bin string, timeout time.Duration, maxPatchBytes int) *Client {
	if bin == "" {
		bin = "git"
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if maxPatchBytes <= 0 {
		maxPatchBytes = DefaultMaxPatchBytes
	}
	return &Client{
		bin:           bin,
		timeout:       timeout,
		maxPatchBytes: maxPatchBytes,
	}
}

func (c *Client) run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrGitUnavailable
		}
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return nil, fmt.Errorf("%w: %v", ErrGitUnavailable, execErr)
		}
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && (errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist)) {
			return nil, fmt.Errorf("%w: %v", ErrGitUnavailable, pathErr)
		}

		outStr := strings.TrimSpace(stderr.String())
		if outStr == "" {
			outStr = strings.TrimSpace(stdout.String())
		}
		if strings.Contains(strings.ToLower(outStr), "not a git repository") {
			return nil, fmt.Errorf("%w: %s", ErrNotRepo, outStr)
		}
		return nil, fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), outStr, err)
	}

	return stdout.Bytes(), nil
}

// SnapshotTree writes a git tree object capturing dir's entire working tree --
// tracked and untracked, honouring .gitignore -- and returns its object id.
//
// Preconditions:  dir exists.
// Postconditions: the repository's own index, HEAD, refs and working tree are
//
//	byte-for-byte unchanged. The returned tree (and any blobs it
//	required) exist in the object database, unreferenced.
//
// Errors: ErrNotRepo (dir is not in a working tree), ErrGitUnavailable (binary
//
//	missing or not executable), context.DeadlineExceeded, or a wrapped
//	git failure. Never partially succeeds: on error, no id is returned.
func (c *Client) SnapshotTree(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}

	tempDir, err := os.MkdirTemp("", "relay-git-index-*")
	if err != nil {
		return "", fmt.Errorf("create temp index dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	tempIndex := filepath.Join(tempDir, "index")
	repoIndex := filepath.Join(gitDir, "index")

	src, err := os.Open(repoIndex)
	if err == nil {
		dst, err := os.OpenFile(tempIndex, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			src.Close()
			return "", fmt.Errorf("create temp index: %w", err)
		}
		_, copyErr := io.Copy(dst, src)
		src.Close()
		closeErr := dst.Close()
		if copyErr != nil {
			return "", fmt.Errorf("copy index: %w", copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close temp index: %w", closeErr)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("open repo index: %w", err)
	}

	env := []string{"GIT_INDEX_FILE=" + tempIndex}
	if _, err := c.run(ctx, dir, env, "add", "-A"); err != nil {
		return "", err
	}

	treeOut, err := c.run(ctx, dir, env, "write-tree")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(treeOut)), nil
}

// DiffTrees compares two tree objects and returns the patch and its stat.
//
// Preconditions:  from and to are tree ids reachable in dir's object database.
// Postconditions: Stat is exact. Patch is the unified diff, or nil when the
//
//	body exceeded maxPatchBytes (Truncated) or nothing changed.
//
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, wrapped git
//
//	failure (including an unknown tree id).
func (c *Client) DiffTrees(ctx context.Context, dir, from, to string) (Diff, error) {
	if _, err := c.run(ctx, dir, nil, "rev-parse", "--git-dir"); err != nil {
		return Diff{}, err
	}

	out, err := c.run(ctx, dir, nil, "diff", "--numstat", from, to)
	if err != nil {
		return Diff{}, err
	}

	var stat Stat
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 3 {
			stat.FilesChanged++
			if parts[0] != "-" {
				if ins, err := strconv.Atoi(parts[0]); err == nil {
					stat.Insertions += ins
				}
			}
			if parts[1] != "-" {
				if del, err := strconv.Atoi(parts[1]); err == nil {
					stat.Deletions += del
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Diff{}, fmt.Errorf("scan numstat: %w", err)
	}

	if stat.Empty() {
		return Diff{Stat: stat}, nil
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, c.bin, "diff", from, to)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Diff{}, fmt.Errorf("git diff stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(ctxTimeout.Err(), context.DeadlineExceeded) {
			return Diff{}, ctxTimeout.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return Diff{}, ErrGitUnavailable
		}
		return Diff{}, err
	}

	limited := io.LimitReader(stdoutPipe, int64(c.maxPatchBytes)+1)
	patch, readErr := io.ReadAll(limited)
	if readErr != nil {
		_ = cmd.Wait()
		return Diff{}, fmt.Errorf("read git diff: %w", readErr)
	}

	if len(patch) > c.maxPatchBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return Diff{
			Stat:      stat,
			Patch:     nil,
			Truncated: true,
		}, nil
	}

	if err := cmd.Wait(); err != nil {
		if errors.Is(ctxTimeout.Err(), context.DeadlineExceeded) {
			return Diff{}, ctxTimeout.Err()
		}
		outStr := strings.TrimSpace(stderr.String())
		if strings.Contains(strings.ToLower(outStr), "not a git repository") {
			return Diff{}, fmt.Errorf("%w: %s", ErrNotRepo, outStr)
		}
		return Diff{}, fmt.Errorf("git diff %s %s: %s: %w", from, to, outStr, err)
	}

	return Diff{
		Stat:      stat,
		Patch:     patch,
		Truncated: false,
	}, nil
}

// HeadCommit returns dir's current HEAD commit id.
// Errors: ErrNotRepo, ErrGitUnavailable, or a wrapped git failure -- including
// an unborn HEAD in a repository with no commits.
func (c *Client) HeadCommit(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RevListCount returns the number of commits reachable from to and not from
// from: `git rev-list --count from..to`. 0 when they are the same commit.
// Errors: ErrNotRepo, ErrGitUnavailable, or a wrapped git failure --
// including a ref that no longer resolves.
func (c *Client) RevListCount(ctx context.Context, dir, from, to string) (int, error) {
	out, err := c.run(ctx, dir, nil, "rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count: parse %q: %w", strings.TrimSpace(string(out)), err)
	}
	return n, nil
}

// BranchExists reports whether branch resolves in dir's repository.
// Errors: ErrNotRepo, ErrGitUnavailable, wrapped git failure.
func (c *Client) BranchExists(ctx context.Context, dir, branch string) (bool, error) {
	ref := branch
	if !strings.HasPrefix(ref, "refs/heads/") {
		ref = "refs/heads/" + ref
	}

	_, err := c.run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", ref)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false, err
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// AddWorktree creates a worktree at path, checking out a NEW branch at commit.
//
// Preconditions:  path does not exist; branch does not exist; commit resolves.
// Postconditions: path is a working tree on branch; dir's own working tree,
//
//	index and HEAD are unchanged.
//
// Errors: ErrBranchExists, ErrNotRepo, ErrGitUnavailable, wrapped git failure.
//
//	On any error nothing is left behind at path.
func (c *Client) AddWorktree(ctx context.Context, dir, path, branch, commit string) (retErr error) {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(dir, absPath)
	}

	pathExisted := false
	if _, err := os.Stat(absPath); err == nil {
		pathExisted = true
	}
	defer func() {
		if retErr != nil && !pathExisted {
			_ = os.RemoveAll(absPath)
			_, _ = c.run(ctx, dir, nil, "worktree", "prune")
		}
	}()

	branchName := strings.TrimPrefix(branch, "refs/heads/")

	exists, err := c.BranchExists(ctx, dir, branchName)
	if err != nil {
		return err
	}
	if exists {
		return ErrBranchExists
	}

	_, err = c.run(ctx, dir, nil, "worktree", "add", "-b", branchName, absPath, commit)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "branch named") ||
			(strings.Contains(errStr, "branch") && strings.Contains(errStr, "already exists")) ||
			strings.Contains(errStr, "is already checked out") {
			return ErrBranchExists
		}
		return err
	}

	return nil
}

// CheckoutWorktree adds a worktree at path on an existing branch.
//
// Preconditions:  path does not exist; branch exists.
// Postconditions: path is a working tree on branch; dir's own working tree,
//
//	index and HEAD are unchanged.
//
// Errors: ErrBranchCheckedOut, ErrNotRepo, ErrGitUnavailable, wrapped git failure.
//
//	On any error nothing is left behind at path.
func (c *Client) CheckoutWorktree(ctx context.Context, dir, path, branch string) (retErr error) {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(dir, absPath)
	}

	pathExisted := false
	if _, err := os.Stat(absPath); err == nil {
		pathExisted = true
	}
	defer func() {
		if retErr != nil && !pathExisted {
			_ = os.RemoveAll(absPath)
			_, _ = c.run(ctx, dir, nil, "worktree", "prune")
		}
	}()

	branchName := strings.TrimPrefix(branch, "refs/heads/")

	_, err := c.run(ctx, dir, nil, "worktree", "add", absPath, branchName)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "is already checked out") ||
			strings.Contains(errStr, "is already used by worktree") {
			return ErrBranchCheckedOut
		}
		return err
	}

	return nil
}

// RemoveWorktree removes a worktree and prunes its administrative entry. It
// never removes the branch: a branch holds commits, and commits are work.
//
// Preconditions:  path is a worktree of dir's repository.
// Postconditions: path no longer exists; the branch survives.
// Errors: ErrWorktreeDirty when the tree has uncommitted or untracked changes
//
//	and force is false; ErrNotRepo; ErrGitUnavailable; wrapped failure.
func (c *Client) RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(dir, absPath)
	}

	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, absPath)

	_, err := c.run(ctx, dir, nil, args...)
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return err
		}
		errStr := strings.ToLower(err.Error())
		if !force && (strings.Contains(errStr, "contains modified or untracked files") ||
			strings.Contains(errStr, "uncommitted changes") ||
			strings.Contains(errStr, "use --force")) {
			return ErrWorktreeDirty
		}
		if !force {
			if dirty, dirtyErr := c.Dirty(ctx, absPath); dirtyErr == nil && dirty {
				return ErrWorktreeDirty
			}
		}
		return err
	}
	return nil
}

// Dirty reports whether dir has uncommitted or untracked (non-ignored) changes.
// Errors: ErrNotRepo, ErrGitUnavailable, wrapped git failure.
func (c *Client) Dirty(ctx context.Context, dir string) (bool, error) {
	out, err := c.run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// RootCommit returns the single root commit SHA of the repository at dir.
// If the repository has several roots (a grafted or multi-root history), it
// returns the lexicographically smallest root SHA so the result is deterministic.
//
// Preconditions:  dir is inside a git repository with at least one commit.
// Postconditions: the repository is unchanged.
// Errors: ErrNotRepo, ErrGitUnavailable, ErrRefMissing (empty repo with no HEAD),
//
//	context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) RootCommit(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", err
		}
		return "", ErrRefMissing
	}
	roots := strings.Fields(string(out))
	if len(roots) == 0 {
		return "", ErrRefMissing
	}
	sort.Strings(roots)
	return roots[0], nil
}

// RefSHA resolves ref to a commit SHA.
//
// Preconditions:  dir is inside a git repository.
// Postconditions: the repository is unchanged; returns (sha, true, nil) when ref
//
//	resolves, or ("", false, nil) when ref does not exist.
//
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) RefSHA(ctx context.Context, dir, ref string) (sha string, ok bool, err error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", false, err
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	res := strings.TrimSpace(string(out))
	if res == "" {
		return "", false, nil
	}
	return res, true, nil
}

// UpdateRef updates ref to newSHA, optionally verifying that oldSHA matches.
//
// Preconditions:  dir is inside a git repository; newSHA is a valid object ID.
//
//	oldSHA is empty ("create or overwrite") or a commit SHA to compare-and-swap.
//
// Postconditions: ref points to newSHA.
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a wrapped git failure
//
//	(including CAS mismatch).
func (c *Client) UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA string) error {
	args := []string{"update-ref", ref, newSHA}
	if oldSHA != "" {
		args = append(args, oldSHA)
	}
	_, err := c.run(ctx, dir, nil, args...)
	return err
}

// CommitTree creates a commit object directly from a tree and parent commit.
//
// The commit is created with fixed author and committer identity:
//
//	GIT_AUTHOR_NAME=relay GIT_AUTHOR_EMAIL=relay@localhost
//	GIT_COMMITTER_NAME=relay GIT_COMMITTER_EMAIL=relay@localhost
//
// The environment variables are passed through run's env parameter, overriding
// the caller's identity. A global commit.gpgsign does not apply because
// commit-tree never signs unless -S is given.
//
// Preconditions:  dir is inside a git repository; tree is a valid tree SHA;
//
//	parent is empty or a valid commit SHA.
//
// Postconditions: the commit exists in the object database, unreferenced.
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) CommitTree(ctx context.Context, dir, tree, parent, message string) (string, error) {
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", message)
	env := []string{
		"GIT_AUTHOR_NAME=relay",
		"GIT_AUTHOR_EMAIL=relay@localhost",
		"GIT_COMMITTER_NAME=relay",
		"GIT_COMMITTER_EMAIL=relay@localhost",
	}
	out, err := c.run(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// BundleCreate creates a git bundle file at path containing refs relative to since.
//
// Preconditions:  every ref in refs resolves in dir's repository (else ErrRefMissing);
//
//	since is "" or a commit SHA in dir's repository that is an ancestor of every ref.
//
// Postconditions: if empty is false, file at path is a complete bundle;
//
//	heads maps each ref to its resolved SHA. If since is non-empty and
//	every ref's head equals since, no bundle is created and empty is true.
//
// Errors: ErrRefMissing, ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded,
//
//	or a wrapped git failure.
func (c *Client) BundleCreate(ctx context.Context, dir, path string, refs []string, since string) (map[string]string, bool, error) {
	heads := make(map[string]string, len(refs))
	for _, ref := range refs {
		sha, ok, err := c.RefSHA(ctx, dir, ref)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, fmt.Errorf("%w: %s", ErrRefMissing, ref)
		}
		heads[ref] = sha
	}

	if since != "" && len(heads) > 0 {
		allEqual := true
		for _, head := range heads {
			if head != since {
				allEqual = false
				break
			}
		}
		if allEqual {
			return heads, true, nil
		}
	}

	if since != "" {
		for _, ref := range refs {
			_, err := c.run(ctx, dir, nil, "merge-base", "--is-ancestor", since, ref)
			if err != nil {
				if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, false, err
				}
				return nil, false, fmt.Errorf("%w: since is not an ancestor of %s", ErrRefMissing, ref)
			}
		}
	}

	args := []string{"bundle", "create", path}
	for _, ref := range refs {
		if since != "" {
			args = append(args, since+".."+ref)
		} else {
			args = append(args, ref)
		}
	}

	_, err := c.run(ctx, dir, nil, args...)
	if err != nil {
		return nil, false, err
	}
	return heads, false, nil
}

// BundleHeads lists the heads carried in the bundle file at path.
//
// Preconditions:  path exists and is a readable git bundle file.
// Postconditions: returns a map of ref name -> commit SHA.
// Errors: ErrBadBundle (file is malformed), ErrNotRepo, ErrGitUnavailable,
//
//	context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) BundleHeads(ctx context.Context, dir, path string) (map[string]string, error) {
	out, err := c.run(ctx, dir, nil, "bundle", "list-heads", path)
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrBadBundle, err)
	}

	heads := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%w: malformed bundle head entry %q", ErrBadBundle, line)
		}
		heads[parts[1]] = parts[0]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBundle, err)
	}
	return heads, nil
}
