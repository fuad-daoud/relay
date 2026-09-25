package relevo

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/store"
)

// GCOptions controls how finished bindings are cleared away.
type GCOptions struct {
	// Delete removes each finished binding's directory instead of archiving it.
	// Archiving is the default because every other destruction decision in relevo
	// keeps by default, and the round logs are the only record of how a feature
	// was built.
	Delete bool
	// DryRun reports what would happen and changes nothing.
	DryRun bool
	// PlannerID clears only DONE bindings whose Binding.PlannerID equals this.
	// It must be non-empty unless AllPlanners is set (#482).
	PlannerID string
	// AllPlanners clears every DONE binding regardless of planner, including
	// bindings with an empty PlannerID. It is mutually exclusive with a
	// non-empty PlannerID (#482).
	AllPlanners bool
}

// ErrGCNoScope reports that GC was called with neither a PlannerID nor
// AllPlanners set, so no caller can get "everything" by leaving the scope
// empty (#482).
var ErrGCNoScope = errors.New("gc needs a planner id or AllPlanners")

// GCResult is one binding gc considered.
type GCResult struct {
	Name            string       `json:"name"`
	CWD             string       `json:"cwd"`
	Rounds          int          `json:"rounds"`
	Archived        bool         `json:"archived"`
	Deleted         bool         `json:"deleted"`
	WorktreeRemoved string       `json:"worktree_removed,omitempty"`
	WorktreeKept    string       `json:"worktree_kept,omitempty"`
	KeptReason      string       `json:"kept_reason,omitempty"`
	WorktreeGone    string       `json:"worktree_gone,omitempty"` // recorded worktree whose directory no longer exists
	Refs            []RefOutcome `json:"refs,omitempty"`
	// PlannerID is the binding's PlannerID, verbatim, which may be "" (#482).
	PlannerID string `json:"planner_id,omitempty"`
}

// GC clears away every binding the calling planner has marked done, or, with
// opts.AllPlanners, every planner's. Only StateDone is touched: a broken or
// orphaned binding still needs a human, and removing it would throw away the
// state that explains why.
//
// Exactly one of opts.PlannerID and opts.AllPlanners must be set (#482): GC
// refuses to run with neither, so no caller can get "everything" by leaving
// the scope empty, and refuses with both, since they are exclusive. A
// binding with an empty PlannerID (written before #303) belongs to no
// planner, and is cleared only by AllPlanners.
//
// By default, finished bindings are archived rather than deleted; passing
// opts.Delete removes them entirely.
//
// GC also deletes the binding's branch and client refs once they are on a
// remote-tracking ref, never an adopted branch, and never while the worktree
// is kept.
//
// The whole sweep runs in one critical section so a binding cannot be marked
// done, or resumed, between the scan and the removal.
func GC(ctx context.Context, rt Runtime, opts GCOptions) ([]GCResult, error) {
	switch {
	case opts.PlannerID == "" && !opts.AllPlanners:
		return nil, ErrGCNoScope
	case opts.PlannerID != "" && opts.AllPlanners:
		return nil, fmt.Errorf("%w: PlannerID and AllPlanners are exclusive", ErrGCNoScope)
	}

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
			if !opts.AllPlanners && b.PlannerID != opts.PlannerID {
				continue
			}

			res := GCResult{Name: b.Name, CWD: b.CWD, Rounds: b.Round, PlannerID: b.PlannerID}

			outcome := worktreeTeardown(ctx, rt, b, opts.DryRun)
			res.WorktreeRemoved = outcome.Removed
			res.WorktreeKept = outcome.Kept
			res.KeptReason = outcome.Reason
			res.WorktreeGone = outcome.Gone

			if rt.Git != nil && outcome.Kept == "" {
				if dir := repoDirOf(b); dir != "" {
					refs, err := bindingRefCandidates(ctx, rt, dir, b)
					if err != nil {
						res.Refs = []RefOutcome{{Ref: "refs/relevo/" + b.Name + "/", Reason: "check failed: " + brief(err)}}
					} else {
						res.Refs = cleanRefs(ctx, rt, dir, refs, opts.DryRun)
					}
				}
			}

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
				if _, err := tx.Archive(b.Name); err != nil {
					return fmt.Errorf("archive %q: %w", b.Name, err)
				}
				res.Archived = true
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
