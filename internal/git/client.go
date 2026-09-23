package git

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// gitEnv builds the environment every git child process Client starts: the
// caller's own environment, any extra variables the call needs, and
// GIT_OPTIONAL_LOCKS=0. extra comes last so a caller can still override it.
//
// Relevo reads a builder's worktree with git while that builder works in it.
// By default a read such as `git status` refreshes the index and takes the
// repository's index.lock to write the refreshed stat data, which makes a
// concurrent `git commit` in the same worktree fail with
// `fatal: Unable to create '.../index.lock': File exists` (exit 128). With
// optional locks off, git skips every side-effect write it does not strictly
// need, so `status` and friends never take index.lock. Commands that must
// write the index (`add`, `commit`, `checkout`, `reset`, ...) still take their
// mandatory lock, so the setting is safe on every call.
func gitEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+1+len(extra))
	env = append(env, os.Environ()...)
	env = append(env, "GIT_OPTIONAL_LOCKS=0")
	return append(env, extra...)
}

func (c *Client) run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(env...)
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

	tempDir, err := os.MkdirTemp("", "relevo-git-index-*")
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
	cmd.Env = gitEnv()
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

// DiffWorktreeStat compares a tree object against dir's current working tree
// and returns just the stat, with no patch body: `git diff --numstat <tree>`
// (tree vs the working tree, including staged changes). It is the live
// counterpart to DiffTrees' first half, for a status row that wants a cheap
// "how far has this round drifted" figure without paying for a patch.
//
// Preconditions:  tree is a tree id reachable in dir's object database.
// Postconditions: dir's working tree, index and HEAD are unchanged.
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a
// wrapped git failure (including an unknown tree id).
func (c *Client) DiffWorktreeStat(ctx context.Context, dir, tree string) (Stat, error) {
	if _, err := c.run(ctx, dir, nil, "rev-parse", "--git-dir"); err != nil {
		return Stat{}, err
	}

	out, err := c.run(ctx, dir, nil, "diff", "--numstat", tree)
	if err != nil {
		return Stat{}, err
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
		return Stat{}, fmt.Errorf("scan numstat: %w", err)
	}

	return stat, nil
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

// TreeFingerprint returns a short, stable hash of dir's HEAD commit and its
// porcelain status -- the cheap "did the tree move?" signal the daemon's
// progress clock samples (#135). It reads no diff and writes no snapshot, so
// sampling costs one status walk.
//
// An unborn branch is not an error: rev-parse HEAD fails there, and the
// fingerprint is taken from the status alone.
// Errors: ErrNotRepo, ErrGitUnavailable, or a wrapped git failure from status.
func (c *Client) TreeFingerprint(ctx context.Context, dir string) (string, error) {
	head, err := c.run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		head = nil // unborn HEAD: the status still tells an empty tree from a full one
	}
	st, err := c.run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(head)) + "\n" + string(st)))
	return hex.EncodeToString(sum[:])[:16], nil
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

// CreateBranch creates a branch at commit in dir without checking it out.
//
// Errors: ErrBranchExists, ErrNotRepo, ErrGitUnavailable, wrapped git failure.
func (c *Client) CreateBranch(ctx context.Context, dir, branch, commit string) error {
	branchName := strings.TrimPrefix(branch, "refs/heads/")
	exists, err := c.BranchExists(ctx, dir, branchName)
	if err != nil {
		return err
	}
	if exists {
		return ErrBranchExists
	}
	_, err = c.run(ctx, dir, nil, "branch", branchName, commit)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "already exists") {
			return ErrBranchExists
		}
		return err
	}
	return nil
}

// CreateTrackingBranch creates branch in dir as a local branch tracking
// upstream -- `git branch --track <branch> <upstream>`. It is the
// existing-branch form of branch creation: unlike CreateBranch it does not
// accept a start point, because the upstream ref is the start point.
//
// Preconditions:  upstream resolves in dir's repository; branch does not exist.
// Postconditions: refs/heads/<branch> exists and its upstream is upstream.
// Errors: ErrBranchExists, ErrNotRepo, ErrGitUnavailable, wrapped git failure.
func (c *Client) CreateTrackingBranch(ctx context.Context, dir, branch, upstream string) error {
	branchName := strings.TrimPrefix(branch, "refs/heads/")
	_, err := c.run(ctx, dir, nil, "branch", "--track", branchName, upstream)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "already exists") {
			return ErrBranchExists
		}
		return err
	}
	return nil
}

// DeleteBranch force-removes branch from dir. A branch that does not exist is
// not an error: relevo's own cleanup calls this on a branch it just created
// itself, without knowing whether a later failure left it in place, so
// idempotence keeps the caller from having to check first.
//
// Errors: ErrNotRepo, ErrGitUnavailable, wrapped git failure -- never for a
// missing branch.
func (c *Client) DeleteBranch(ctx context.Context, dir, branch string) error {
	branchName := strings.TrimPrefix(branch, "refs/heads/")
	_, err := c.run(ctx, dir, nil, "branch", "-D", branchName)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not found") {
			return nil
		}
		return err
	}
	return nil
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

// AddDetachedWorktree creates a worktree at path with a DETACHED HEAD at
// commit -- `git worktree add --detach <abs> <commit>`. Unlike AddWorktree it
// creates no branch: the caller wants a throwaway tree it will remove, not a
// new ref in the repository (#144).
//
// Preconditions:  path does not exist; commit resolves.
// Postconditions: path is a working tree at commit with a detached HEAD;
// dir's own working tree, index and HEAD are unchanged.
//
// Errors: ErrNotRepo, ErrGitUnavailable, wrapped git failure. On any error
// nothing is left behind at path.
func (c *Client) AddDetachedWorktree(ctx context.Context, dir, path, commit string) (retErr error) {
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

	_, err := c.run(ctx, dir, nil, "worktree", "add", "--detach", absPath, commit)
	if err != nil {
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

// ListTags lists dir's tags by short name, each mapped to the commit it points
// at: an annotated tag is peeled to its commit, a lightweight tag already is
// one (#242).
//
// Preconditions:  dir is inside a git repository.
// Postconditions: returns every refs/tags entry; the value is the tag's peeled
// commit. Empty map, nil error when there are no tags.
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a wrapped
// git failure.
func (c *Client) ListTags(ctx context.Context, dir string) (map[string]string, error) {
	out, err := c.run(ctx, dir, nil,
		"for-each-ref",
		"--format=%(refname:strip=2)%00%(objectname)%00%(*objectname)",
		"refs/tags")
	if err != nil {
		return nil, err
	}

	tags := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		sha := fields[1]
		if len(fields) > 2 && fields[2] != "" {
			sha = fields[2]
		}
		tags[fields[0]] = sha
	}
	return tags, nil
}

// CommitTree creates a commit object directly from a tree and parent commit.
//
// The commit is created with fixed author and committer identity:
//
//	GIT_AUTHOR_NAME=relevo GIT_AUTHOR_EMAIL=relevo@localhost
//	GIT_COMMITTER_NAME=relevo GIT_COMMITTER_EMAIL=relevo@localhost
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
		"GIT_AUTHOR_NAME=relevo",
		"GIT_AUTHOR_EMAIL=relevo@localhost",
		"GIT_COMMITTER_NAME=relevo",
		"GIT_COMMITTER_EMAIL=relevo@localhost",
	}
	out, err := c.run(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitAll stages the whole working tree and commits it: `git add -A`
// followed by `git commit -q -m message`.
//
// The commit uses the same fixed relevo identity CommitTree does
// (GIT_AUTHOR_* and GIT_COMMITTER_* = relevo/relevo@localhost), passed through
// run's env parameter, and passes -c commit.gpgsign=false so a global
// commit.gpgsign cannot make it reach for gpg.
//
// Preconditions:  dir is inside a git repository.
// Postconditions: every tracked and untracked (non-ignored) change is in one
// new commit on the current branch; the returned sha is the new HEAD. When
// there is nothing to commit the repository is left untouched and
// ("", nil) is returned.
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a
// wrapped git failure.
func (c *Client) CommitAll(ctx context.Context, dir, message string) (string, error) {
	if _, err := c.run(ctx, dir, nil, "add", "-A"); err != nil {
		return "", err
	}

	env := []string{
		"GIT_AUTHOR_NAME=relevo",
		"GIT_AUTHOR_EMAIL=relevo@localhost",
		"GIT_COMMITTER_NAME=relevo",
		"GIT_COMMITTER_EMAIL=relevo@localhost",
	}
	if _, err := c.run(ctx, dir, env, "-c", "commit.gpgsign=false", "commit", "-q", "-m", message); err != nil {
		if strings.Contains(err.Error(), "nothing to commit") {
			return "", nil
		}
		return "", err
	}

	out, err := c.run(ctx, dir, nil, "rev-parse", "HEAD")
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

// FetchBundle verifies the bundle file at path and fetches each ref in refs that
// the bundle carries into dir's repository.
//
// The fetch is performed one ref at a time, in refs order, using the refspec
// <ref>:<ref> without a leading '+', enforcing that every update is a fast-forward.
// If any ref update fails, execution stops and any refs already moved stay moved.
// Callers treat a partial absorb as retryable; a re-run is idempotent because a ref
// already at its target SHA is a no-op fetch.
//
// The fetch runs with `gc.autoDetach=false` so an auto-gc it triggers finishes
// inside this call instead of forking a process that outlives it (and would race
// a later worktree removal).
//
// Preconditions:  dir is inside a git repository; path is a bundle file.
// Postconditions: refs carried by the bundle that appear in refs are updated to the
//
//	bundle's heads; returns a map of ref -> SHA for the refs fetched.
//
// Errors: ErrBadBundle (bundle verify fails or prerequisites missing),
//
//	ErrNotFastForward (a ref cannot be fast-forwarded), ErrNotRepo, ErrGitUnavailable,
//	context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) FetchBundle(ctx context.Context, dir, path string, refs []string) (map[string]string, error) {
	_, err := c.run(ctx, dir, nil, "bundle", "verify", path)
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrBadBundle, err)
	}

	heads, err := c.BundleHeads(ctx, dir, path)
	if err != nil {
		return nil, err
	}

	fetched := make(map[string]string)
	for _, ref := range refs {
		sha, ok := heads[ref]
		if !ok {
			continue
		}
		_, err := c.run(ctx, dir, nil, "-c", "gc.autoDetach=false", "fetch", "--no-tags", path, ref+":"+ref)
		if err != nil {
			if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fetched, err
			}
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "non-fast-forward") || strings.Contains(errStr, "[rejected]") {
				return fetched, ErrNotFastForward
			}
			return fetched, err
		}
		fetched[ref] = sha
	}
	return fetched, nil
}

// MergeFF fast-forwards dir's current branch to ref.
//
// Preconditions:  dir is inside a git worktree.
// Postconditions: on success the worktree and its branch are at ref.
// Errors: ErrNotFastForward if fast-forward is not possible, ErrMergeConflict
// if uncommitted changes conflict with the update, ErrNotRepo, ErrGitUnavailable,
// context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) MergeFF(ctx context.Context, dir, ref string) error {
	_, err := c.run(ctx, dir, nil, "merge", "--ff-only", ref)
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return err
		}
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not possible to fast-forward") {
			return ErrNotFastForward
		}
		if strings.Contains(errStr, "would be overwritten") || strings.Contains(errStr, "local changes") {
			return ErrMergeConflict
		}
		return err
	}
	return nil
}

// RepoFacts reports dir's repository identity for the coming history
// database (docs/specs/2026-09-20-persistence-design.md §5.4): the absolute
// path of the main worktree's .git directory, and the origin remote's raw
// URL (normalisation is the caller's job via NormalizeOriginURL).
//
// Preconditions:  dir exists.
// Postconditions: the repository is unchanged. originURL is "" with no
//
//	error when dir has no origin remote; commonDir is the same
//	absolute path for every worktree of one repository (rev-parse
//	--git-common-dir), so a worktree and its main tree share it.
//
// Errors: ErrNotRepo (dir is not a git work tree), ErrGitUnavailable,
// context.DeadlineExceeded, or a wrapped git failure. Never returned for a
// missing origin remote.
func (c *Client) RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	commonDir = strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	// Canonical: common_dir is a database key, and git resolves symlinks
	// when answering from a worktree but not from the main tree (macOS
	// /var -> /private/var), which would give one repo two keys.
	if real, rerr := filepath.EvalSymlinks(commonDir); rerr == nil {
		commonDir = real
	}

	out, err = c.run(ctx, dir, nil, "remote", "get-url", "origin")
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", "", err
		}
		// No origin remote configured (git exits non-zero, "No such remote").
		// That is not a failure relevo reports: it simply has nothing to say
		// about origin.
		return "", commonDir, nil
	}
	originURL = strings.TrimSpace(string(out))

	return originURL, commonDir, nil
}

// Identity reports the git identity dir's repository would commit as: the
// effective user.name and user.email `git config --get` resolves, which
// includes global and system config (from anywhere the caller runs, and
// from the repository itself) (#335).
//
// Preconditions:  dir exists.
// Postconditions: the repository is unchanged. An unset key is ("", nil):
// `git config --get` exits 1 with no output for a key that is not set,
// which is a fact about the machine, not a failure -- the caller decides
// whether a missing identity is fatal, and for a remote builder it is.
//
// Errors: ErrNotRepo (dir is not a git work tree), ErrGitUnavailable,
// context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) Identity(ctx context.Context, dir string) (name, email string, err error) {
	get := func(key string) (string, error) {
		out, gerr := c.run(ctx, dir, nil, "config", "--get", key)
		if gerr != nil {
			if errors.Is(gerr, ErrNotRepo) || errors.Is(gerr, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", gerr
			}
			var exitErr *exec.ExitError
			if errors.As(gerr, &exitErr) && exitErr.ExitCode() == 1 {
				return "", nil
			}
			return "", gerr
		}
		return strings.TrimSpace(string(out)), nil
	}

	name, err = get("user.name")
	if err != nil {
		return "", "", err
	}
	email, err = get("user.email")
	if err != nil {
		return "", "", err
	}
	return name, email, nil
}

// NormalizeOriginURL puts an origin remote URL into one comparable form, so
// the same repository reached over SSH and HTTPS groups as one (spec §3
// decision 6). Recognised forms:
//
//	git@host:owner/repo(.git)       -> https://host/owner/repo
//	ssh://git@host/owner/repo(.git) -> https://host/owner/repo
//	https://host/owner/repo(.git)/  -> https://host/owner/repo
//	http://...                      stays http://
//
// The host is lowercased; the path keeps its case. Trailing "/" and ".git"
// are removed. Anything else is returned trimmed and otherwise unchanged.
func NormalizeOriginURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}

	// git@host:owner/repo(.git) -- scp-like syntax. Only matched when there
	// is no "://" scheme, which the ssh:// and http(s):// forms below own.
	if !strings.Contains(s, "://") {
		if at := strings.Index(s, "@"); at >= 0 {
			rest := s[at+1:]
			if colon := strings.Index(rest, ":"); colon >= 0 {
				host := rest[:colon]
				path := rest[colon+1:]
				if host != "" && path != "" && !strings.Contains(host, "/") {
					return "https://" + strings.ToLower(host) + "/" + trimRepoPath(path)
				}
			}
		}
	}

	if rest, ok := strings.CutPrefix(s, "ssh://"); ok {
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		if slash := strings.Index(rest, "/"); slash >= 0 {
			host := rest[:slash]
			path := rest[slash+1:]
			return "https://" + strings.ToLower(host) + "/" + trimRepoPath(path)
		}
	}

	for _, scheme := range []string{"https://", "http://"} {
		rest, ok := strings.CutPrefix(s, scheme)
		if !ok {
			continue
		}
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return scheme + strings.ToLower(rest)
		}
		host := rest[:slash]
		path := rest[slash+1:]
		return scheme + strings.ToLower(host) + "/" + trimRepoPath(path)
	}

	return s
}

// trimRepoPath strips a trailing "/" and then a trailing ".git" (in that
// order, since "owner/repo.git/" is a valid trailing form) from a repo path.
func trimRepoPath(p string) string {
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.TrimSuffix(p, "/")
	return p
}

// InitBare initializes a bare git repository at path.
//
// Preconditions:  path is the destination repository directory.
// Postconditions: path is a bare repository; idempotent (an existing bare repo is left as is).
// Errors: ErrGitUnavailable, context.DeadlineExceeded, or a wrapped failure.
func (c *Client) InitBare(ctx context.Context, path string) error {
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, err := c.run(ctx, dir, nil, "init", "--bare", path)
	return err
}

// CurrentBranch returns dir's checked-out branch name, or "" when HEAD is
// detached.
//
// `git rev-parse --abbrev-ref HEAD` answers the literal "HEAD" for a detached
// worktree, which is not a branch name and must never be recorded as one
// (relevo land would then try to fetch and rebase onto a ref called HEAD).
//
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a
// wrapped git failure -- including an unborn HEAD, which git cannot
// abbreviate to a branch.
func (c *Client) CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "HEAD" {
		return "", nil
	}
	return name, nil
}

// Fetch fetches ref from remote into dir's repository: `git fetch <remote>
// <ref>`. git opportunistically updates the matching remote-tracking ref
// (refs/remotes/<remote>/<ref>) when the remote is configured with a fetch
// refspec, which is what makes "origin/<base>" resolvable afterwards.
//
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a
// wrapped git failure (an unknown remote or ref).
func (c *Client) Fetch(ctx context.Context, dir, remote, ref string) error {
	_, err := c.run(ctx, dir, nil, "fetch", remote, ref)
	return err
}

// Rebase rebases dir's current branch onto onto: `git rebase <onto>`.
//
// Preconditions:  dir is inside a git worktree whose branch is checked out.
// Postconditions: on success dir's branch is rewritten onto onto. On a
// conflict the unmerged paths are returned, the rebase is aborted, and the
// worktree and HEAD are exactly where they were -- nothing is left half
// rebased.
// Errors: ErrMergeConflict (with the conflicting paths), ErrNotRepo,
// ErrGitUnavailable, context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) Rebase(ctx context.Context, dir, onto string) ([]string, error) {
	_, err := c.run(ctx, dir, nil, "rebase", onto)
	if err == nil {
		return nil, nil
	}
	if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, err
	}

	paths, perr := c.unmergedPaths(ctx, dir)
	if perr != nil {
		return nil, perr
	}
	// No unmerged path means this was not a conflict at all -- an unresolvable
	// onto, a missing committer identity, a dirty index -- so the original
	// failure is the honest answer. The abort still runs: git may have left a
	// rebase in progress before failing.
	if len(paths) == 0 {
		_, _ = c.run(ctx, dir, nil, "rebase", "--abort")
		return nil, err
	}
	if _, aerr := c.run(ctx, dir, nil, "rebase", "--abort"); aerr != nil {
		return nil, fmt.Errorf("%w: rebase %s: %v; and rebase --abort failed: %v", ErrMergeConflict, onto, err, aerr)
	}
	return paths, ErrMergeConflict
}

// Merge merges ref into dir's current branch: `git merge --no-edit <ref>`.
// It is the --merge escape hatch: it integrates the base without rewriting
// the branch, so the push that follows needs no lease.
//
// Preconditions:  dir is inside a git worktree whose branch is checked out.
// Postconditions: on success ref is merged. On a conflict the unmerged paths
// are returned, the merge is aborted, and the worktree and HEAD are exactly
// where they were.
// Errors: ErrMergeConflict (with the conflicting paths), ErrNotRepo,
// ErrGitUnavailable, context.DeadlineExceeded, or a wrapped git failure.
func (c *Client) Merge(ctx context.Context, dir, ref string) ([]string, error) {
	_, err := c.run(ctx, dir, nil, "merge", "--no-edit", ref)
	if err == nil {
		return nil, nil
	}
	if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, err
	}

	paths, perr := c.unmergedPaths(ctx, dir)
	if perr != nil {
		return nil, perr
	}
	// As in Rebase: no unmerged path means the failure was not a conflict, so
	// the original error is returned instead of inventing one.
	if len(paths) == 0 {
		_, _ = c.run(ctx, dir, nil, "merge", "--abort")
		return nil, err
	}
	if _, aerr := c.run(ctx, dir, nil, "merge", "--abort"); aerr != nil {
		return nil, fmt.Errorf("%w: merge %s: %v; and merge --abort failed: %v", ErrMergeConflict, ref, err, aerr)
	}
	return paths, ErrMergeConflict
}

// unmergedPaths lists the paths git marked unmerged in dir
// (`git diff --name-only --diff-filter=U`), the conflict set Rebase and Merge
// return.
func (c *Client) unmergedPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := c.run(ctx, dir, nil, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// Push pushes branch to remote and sets it as branch's upstream: `git push -u
// <remote> <branch>`, with --force-with-lease when forceWithLease is true. A
// rebase rewrites the branch, so a branch that already exists on the remote
// needs the lease; --merge does not rewrite it and does not.
//
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a
// wrapped git failure (rejected push, no such remote).
func (c *Client) Push(ctx context.Context, dir, remote, branch string, forceWithLease bool) error {
	args := []string{"push", "-u", remote, branch}
	if forceWithLease {
		args = append(args, "--force-with-lease")
	}
	_, err := c.run(ctx, dir, nil, args...)
	return err
}

// RemoteBranchExists reports whether remote already has a branch named branch:
// `git ls-remote --heads <remote> <branch>` printing anything at all.
//
// Errors: ErrNotRepo, ErrGitUnavailable, context.DeadlineExceeded, or a
// wrapped git failure (an unreachable remote).
func (c *Client) RemoteBranchExists(ctx context.Context, dir, remote, branch string) (bool, error) {
	out, err := c.run(ctx, dir, nil, "ls-remote", "--heads", remote, branch)
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}
