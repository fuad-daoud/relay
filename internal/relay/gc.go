package relay

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relay/internal/store"
)

// GCOptions controls how finished bindings are cleared away.
type GCOptions struct {
	// Delete removes each finished binding's directory instead of archiving it.
	// Archiving is the default because every other destruction decision in relay
	// keeps by default, and the round logs are the only record of how a feature
	// was built.
	Delete bool
	// DryRun reports what would happen and changes nothing.
	DryRun bool
}

// GCResult is one binding gc considered.
type GCResult struct {
	Name            string `json:"name"`
	CWD             string `json:"cwd"`
	Rounds          int    `json:"rounds"`
	ArchivedTo      string `json:"archived_to,omitempty"`
	Deleted         bool   `json:"deleted"`
	WorktreeRemoved string `json:"worktree_removed,omitempty"`
	WorktreeKept    string `json:"worktree_kept,omitempty"`
	KeptReason      string `json:"kept_reason,omitempty"`
	WorktreeGone    string `json:"worktree_gone,omitempty"` // recorded worktree whose directory no longer exists
}

// GC clears away every binding the planner has marked done. Only StateDone is
// touched: a broken or orphaned binding still needs a human, and removing it
// would throw away the state that explains why.
//
// By default, finished bindings are archived rather than deleted; passing
// opts.Delete removes them entirely.
//
// The whole sweep runs in one critical section so a binding cannot be marked
// done, or resumed, between the scan and the removal.
func GC(ctx context.Context, rt Runtime, opts GCOptions) ([]GCResult, error) {
	var out []GCResult

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		bindings, err := tx.List()
		if err != nil {
			return err
		}

		for _, b := range bindings {
			if b.State != store.StateDone {
				continue
			}

			res := GCResult{Name: b.Name, CWD: b.CWD, Rounds: b.Round}

			outcome := worktreeTeardown(ctx, rt, b, opts.DryRun)
			res.WorktreeRemoved = outcome.Removed
			res.WorktreeKept = outcome.Kept
			res.KeptReason = outcome.Reason
			res.WorktreeGone = outcome.Gone

			if opts.DryRun {
				out = append(out, res)
				continue
			}

			if opts.Delete {
				if err := tx.Delete(b.Name); err != nil {
					return fmt.Errorf("delete %q: %w", b.Name, err)
				}
				res.Deleted = true
			} else {
				dest, err := tx.Archive(b.Name)
				if err != nil {
					return fmt.Errorf("archive %q: %w", b.Name, err)
				}
				res.ArchivedTo = dest
			}

			out = append(out, res)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}
