package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrScratch is the sentinel every CreateScratch failure wraps, so a caller
// can tell "the scratch worktree could not be created" from any other error
// (2026-09-24-cockpit-design.md §3.4; §4.3 of the Cockpit A5a plan).
var ErrScratch = errors.New("scratch worktree")

// Scratch is a reader round's throwaway worktree: the binding's working state
// copied into a detached tree that is discarded when the round closes
// (2026-09-24-cockpit-design.md §3.4).
type Scratch struct {
	Path string // <root>/.worktrees/.scratch/<binding>-NNN
	Head string // the binding's HEAD commit at creation
	Tree string // the snapshot tree written into it
}

// CreateScratch makes the throwaway worktree a reader round runs in
// (2026-09-24-cockpit-design.md §3.4): the binding's HEAD plus its whole
// working state -- tracked edits (staged or not), deletions and untracked
// non-ignored files -- with every difference unstaged and nothing committed.
//
// Preconditions:  rt.Git is set and b.CWD names the binding's git tree.
// Postconditions: on success a detached worktree at Scratch.Path holds b.CWD's
// working state; b.CWD itself is untouched. On any failure no worktree is left
// behind.
//
// Errors: every failure wraps ErrScratch and names the step that failed
// ("head", "snapshot", "add", "materialize" or "leftover").
func CreateScratch(ctx context.Context, rt Runtime, b store.Binding, round int) (Scratch, error) {
	if rt.Git == nil || b.CWD == "" {
		return Scratch{}, fmt.Errorf("%w: binding %s has no git tree", ErrScratch, b.Name)
	}

	path := rt.Store.ScratchWorktreePath(b.Name, round)

	// A path that is already there is a leftover from a crash: take it away
	// before `git worktree add`, which refuses an existing path.
	if _, err := os.Stat(path); err == nil {
		if err := rt.Git.RemoveWorktree(ctx, b.CWD, path, true); err != nil {
			return Scratch{}, fmt.Errorf("%w: leftover: %w", ErrScratch, err)
		}
		if _, err := os.Stat(path); err == nil {
			if err := os.RemoveAll(path); err != nil {
				return Scratch{}, fmt.Errorf("%w: leftover: %w", ErrScratch, err)
			}
		}
	}

	if err := os.MkdirAll(rt.Store.ScratchWorktreeDir(), 0o755); err != nil {
		return Scratch{}, fmt.Errorf("%w: leftover: %w", ErrScratch, err)
	}

	// HEAD first, the snapshot second: a commit landing between the two calls
	// can then only add to the snapshot and never drop the new commit's
	// changes.
	head, err := rt.Git.HeadCommit(ctx, b.CWD)
	if err != nil {
		return Scratch{}, fmt.Errorf("%w: head: %w", ErrScratch, err)
	}
	tree, err := rt.Git.SnapshotTree(ctx, b.CWD)
	if err != nil {
		return Scratch{}, fmt.Errorf("%w: snapshot: %w", ErrScratch, err)
	}
	if err := rt.Git.AddDetachedWorktree(ctx, b.CWD, path, head); err != nil {
		return Scratch{}, fmt.Errorf("%w: add: %w", ErrScratch, err)
	}
	if err := rt.Git.MaterializeTree(ctx, path, tree); err != nil {
		if rmErr := rt.Git.RemoveWorktree(ctx, b.CWD, path, true); rmErr != nil {
			slog.Warn("scratch worktree not removed", "binding", b.Name, "round", round, "err", rmErr)
		}
		return Scratch{}, fmt.Errorf("%w: materialize: %w", ErrScratch, err)
	}

	return Scratch{Path: path, Head: head, Tree: tree}, nil
}

// RemoveScratch takes a reader round's throwaway worktree away. A path that is
// already gone is success: RemoveWorktree prunes the stale administrative
// entry in that case (client.go:604-618).
func RemoveScratch(ctx context.Context, rt Runtime, b store.Binding, round int) error {
	return rt.Git.RemoveWorktree(ctx, b.CWD, rt.Store.ScratchWorktreePath(b.Name, round), true)
}

// SweepScratch removes the scratch worktrees whose round is closed -- the
// daemon's cleanup of leftovers a crash left behind (2026-09-24-cockpit-design.md
// §3.4). keep is the caller's answer to "this binding still has an open reader
// round N"; a kept entry is left alone.
//
// A missing scratch directory is (nil, nil). The removed paths are returned
// sorted. Per-entry failures are collected and joined after the whole sweep,
// so one bad entry never stops the others.
func SweepScratch(ctx context.Context, rt Runtime, keep func(binding string, round int) bool) ([]string, error) {
	entries, err := os.ReadDir(rt.Store.ScratchWorktreeDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var (
		removed []string
		errs    []error
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name, round, ok := parseScratchName(e.Name())
		if !ok {
			slog.Warn("scratch sweep: entry does not parse as <name>-<NNN>", "entry", e.Name())
			continue
		}
		if keep != nil && keep(name, round) {
			continue
		}

		path := filepath.Join(rt.Store.ScratchWorktreeDir(), e.Name())
		if err := removeScratchEntry(ctx, rt, name, path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, path)
	}

	sort.Strings(removed)
	return removed, errors.Join(errs...)
}

// removeScratchEntry removes one scratch worktree. With a binding record, the
// binding's own tree names the repository. Without one -- the binding is gone
// -- the worktree's .git file names it instead: the directory goes first, and
// the RemoveWorktree call afterwards only prunes the now-stale admin entry.
func removeScratchEntry(ctx context.Context, rt Runtime, name, path string) error {
	if b, err := rt.Store.Load(name); err == nil {
		return rt.Git.RemoveWorktree(ctx, b.CWD, path, true)
	}

	repo, err := scratchRepoFromGitFile(path)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return rt.Git.RemoveWorktree(ctx, repo, path, true)
}

// parseScratchName splits a scratch directory name "<name>-<NNN>": name is
// everything before the last "-", and the digits after it are the round.
func parseScratchName(entry string) (name string, round int, ok bool) {
	i := strings.LastIndex(entry, "-")
	if i <= 0 || i == len(entry)-1 {
		return "", 0, false
	}
	name = entry[:i]
	digits := entry[i+1:]
	for j := 0; j < len(digits); j++ {
		if digits[j] < '0' || digits[j] > '9' {
			return "", 0, false
		}
	}
	round, err := strconv.Atoi(digits)
	if err != nil {
		return "", 0, false
	}
	return name, round, true
}

// scratchRepoFromGitFile derives the repository a scratch worktree belongs to
// from the "gitdir: <repo>/.git/worktrees/<id>" line of its .git file. It is
// adminRepo in internal/migrate/orphans.go (:80) reduced to what the sweep
// needs: the repository path to hand RemoveWorktree, with no relocation
// mapping.
func scratchRepoFromGitFile(path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", err
	}

	const prefix = "gitdir: "
	line := strings.TrimSuffix(string(data), "\n")
	if !strings.HasPrefix(line, prefix) || strings.ContainsAny(line, "\r\n") {
		return "", fmt.Errorf("%s: .git is not a single \"gitdir: <path>\" line", path)
	}
	admin := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if admin == "" {
		return "", fmt.Errorf("%s: .git gitdir line names no path", path)
	}

	const marker = "/worktrees/"
	i := strings.LastIndex(admin, marker)
	if i < 0 || i+len(marker) == len(admin) {
		return "", fmt.Errorf("%s: gitdir %s does not end in /worktrees/<name>", path, admin)
	}
	repo := admin[:i]
	// A non-bare repo is named by its worktree root, so the administrative
	// ".git" the gitdir points through is not part of the repo's name.
	if strings.HasSuffix(repo, "/.git") {
		repo = strings.TrimSuffix(repo, "/.git")
	}
	if repo == "" {
		return "", fmt.Errorf("%s: gitdir %s names no repository", path, admin)
	}
	return repo, nil
}
