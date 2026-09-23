package relevo

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrRoundOpen reports a pause refused because a round is still in flight: a
// builder may still be writing into the tree pause would release.
var ErrRoundOpen = errors.New("round is open; wait for it or relevo done")

// ErrPauseDirty reports a pause refused because the worktree has uncommitted
// changes and the caller did not ask for --commit.
var ErrPauseDirty = errors.New("worktree has uncommitted changes; commit them, or relevo pause --commit")

// PauseOptions is what a pause request may add.
type PauseOptions struct {
	// Commit commits everything in the worktree on the binding's branch
	// before releasing it, rather than refusing a dirty tree.
	Commit bool
}

// PauseResult is what Pause actually did.
type PauseResult struct {
	Round     int // the last completed round (b.Round - 1)
	Branch    string
	Worktree  string // the path that was released
	Committed string // sha of the --commit commit, "" when nothing was committed
}

// Pause releases a binding's worktree and its builder pane between rounds,
// and parks the binding in PAUSED: the branch and the round log stay, and
// `relevo bind --resume --name <n>` restores the worktree and rebinds.
//
// It runs entirely under the state lock (the Done pattern): the daemon
// rewrites this binding on every tick, so state is reached only through tx.
//
// Refusals, in order: a remote binding (no local worktree), DONE, already
// PAUSED, a binding relevo did not create a tree for (--cwd/adopted), an open
// round, and missing git. A dirty tree is refused unless --commit.
//
// Order of work is refusals -> dirty/commit -> remove worktree ->
// state+log+save -> hook. A failure before the worktree is removed changes
// nothing; a failure at the removal changes nothing either, and names the
// --commit commit that is now on the branch (it is not undone).
func Pause(ctx context.Context, rt Runtime, name string, opts PauseOptions) (PauseResult, error) {
	var out PauseResult
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		// Refusals first: nothing has changed yet.
		if b.Builder.Remote() {
			return fmt.Errorf("binding %q is remote; it has no local worktree to release -- relevo done ends it", name)
		}
		if b.State == store.StateDone {
			return fmt.Errorf("binding %q is done; nothing to pause", name)
		}
		if b.State == store.StatePaused {
			return fmt.Errorf("binding %q is already paused", name)
		}
		if b.Worktree == "" {
			return fmt.Errorf("binding %q drives %s, a tree relevo did not create; nothing to release", name, b.CWD)
		}

		// A round is open when the round has its start stamp, or when the
		// builder's process is recorded and still alive.
		roundOpen := !b.RoundStartedAt.IsZero()
		if !roundOpen && b.Builder.Headless() && b.Builder.PID != 0 && rt.Runner != nil {
			alive, aerr := rt.Runner.Alive(ctx, handleOf(b.Builder))
			if aerr != nil {
				return fmt.Errorf("binding %q: check builder process %d: %w", name, b.Builder.PID, aerr)
			}
			roundOpen = alive
		}
		if roundOpen {
			return fmt.Errorf("binding %q: round %d %w", name, b.Round, ErrRoundOpen)
		}

		if rt.Git == nil {
			return ErrGitRequired
		}

		// Dirty tree: refuse, or commit it on the binding's branch.
		dirty, err := rt.Git.Dirty(ctx, b.Worktree)
		if err != nil {
			return err
		}
		if dirty && !opts.Commit {
			return fmt.Errorf("binding %q: %w", name, ErrPauseDirty)
		}
		if dirty {
			sha, cerr := rt.Git.CommitAll(ctx, b.Worktree,
				fmt.Sprintf("[relevo] %s: paused after round %d", name, b.Round-1))
			if cerr != nil {
				return fmt.Errorf("binding %q: commit worktree %s: %w", name, b.Worktree, cerr)
			}
			out.Committed = sha
		}

		// Release the worktree. The branch and the log stay. A failure here
		// leaves the state untouched; the commit, if any, is on the branch.
		if err := rt.Git.RemoveWorktree(ctx, b.CWD, b.Worktree, false); err != nil {
			if out.Committed != "" {
				return fmt.Errorf("binding %q: release worktree %s: %w; the --commit commit %s is on %s",
					name, b.Worktree, err, out.Committed, b.Worktree)
			}
			return fmt.Errorf("binding %q: release worktree %s: %w", name, b.Worktree, err)
		}

		// Identity cleared: resume spawns a fresh builder. Mode and Kind stay,
		// so a headless binding resumes headless and the candidate is not lost.
		b.Builder = store.Endpoint{Kind: b.Builder.Kind, Mode: b.Builder.Mode}

		oldState := b.State
		b.State = store.StatePaused
		if err := tx.AppendLog(name, store.LogEntry{
			Round:     b.Round,
			Direction: store.DirToPlanner,
			Kind:      store.KindPause,
			Confirmed: true,
			Note:      fmt.Sprintf("paused after round %d", b.Round-1),
		}); err != nil {
			return err
		}
		if err := tx.Save(b); err != nil {
			return err
		}

		if rt.Hooks != nil && oldState != store.StatePaused {
			rt.Hooks.Dispatch(ctx, hooks.Event{
				Type:      hooks.EventStateChanged,
				BindingID: b.Name,
				State:     string(store.StatePaused),
				OldState:  string(oldState),
				Round:     b.Round,
				Timestamp: rt.Now().UTC(),
			})
		}

		out.Round = b.Round - 1
		out.Branch = b.Branch
		out.Worktree = b.Worktree
		return nil
	})
	return out, err
}
