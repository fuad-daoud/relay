package relay

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relay/internal/store"
)

// GCOptions controls how finished bindings are cleared away.
type GCOptions struct {
	// Archive moves each binding aside instead of deleting it.
	Archive bool
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
}

// GC clears away every binding the planner has marked done. Only StateDone is
// touched: a broken or orphaned binding still needs a human, and removing it
// would throw away the state that explains why.
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

			if opts.DryRun {
				out = append(out, res)
				continue
			}

			if opts.Archive {
				dest, err := tx.Archive(b.Name)
				if err != nil {
					return fmt.Errorf("archive %q: %w", b.Name, err)
				}
				res.ArchivedTo = dest
			} else {
				if err := tx.Delete(b.Name); err != nil {
					return fmt.Errorf("delete %q: %w", b.Name, err)
				}
				res.Deleted = true
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
