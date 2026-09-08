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
