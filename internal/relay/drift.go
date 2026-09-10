package relay

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/store"
)

// DriftResult is what one between-rounds capture attempt produced. A zero value
// means "nothing to say", which is the normal outcome.
type DriftResult struct {
	Available bool     // a comparison ran, or was short-circuited by equal trees
	Path      string   // patch file; "" when the trees matched or the body was truncated
	Stat      git.Stat // exact whenever Available
	Truncated bool     // patch omitted because it exceeded the cap
	Reason    string   // why Available is false; "" when it is true
}

// CaptureDrift compares the tree a round ended at against the tree the next round
// is opening at, writing the patch to rt.Store.DriftPath(b.Name, b.Round) when
// there is a body worth keeping.
//
// When baseline == b.RoundClosedTree, CaptureDrift short-circuits before any git
// call. It NEVER returns an error: every failure lands in DriftResult.Reason.
//
// Preconditions: none.
// Postconditions:
//   - Available is false with empty Reason when rt.Git is nil, b.RoundClosedTree
//     is empty, baseline is empty, or DiffTrees returned git.ErrNotRepo.
//   - Available is true with zero Stat when baseline == b.RoundClosedTree.
//   - Available is false with Reason set when DiffTrees or writing the patch failed.
//   - When Available is true and Stat is non-empty, Path names an existing file
//     unless Truncated is true.
func CaptureDrift(ctx context.Context, rt Runtime, b store.Binding, baseline string) DriftResult {
	if rt.Git == nil || b.RoundClosedTree == "" || baseline == "" {
		return DriftResult{Available: false}
	}
	if baseline == b.RoundClosedTree {
		return DriftResult{Available: true}
	}

	diff, err := rt.Git.DiffTrees(ctx, b.CWD, b.RoundClosedTree, baseline)
	if errors.Is(err, git.ErrNotRepo) {
		return DriftResult{Available: false}
	}
	if err != nil {
		return DriftResult{Available: false, Reason: brief(err)}
	}

	if diff.Stat.Empty() {
		return DriftResult{Available: true, Stat: diff.Stat}
	}

	if diff.Truncated {
		return DriftResult{Available: true, Stat: diff.Stat, Truncated: true}
	}

	patchPath := rt.Store.DriftPath(b.Name, b.Round)
	if err := os.WriteFile(patchPath, diff.Patch, 0o644); err != nil {
		return DriftResult{Available: false, Reason: brief(err)}
	}

	return DriftResult{Available: true, Path: patchPath, Stat: diff.Stat}
}

// DriftSummary returns the human summary for the drift log entry.
func DriftSummary(res DriftResult) string {
	if !res.Available {
		if res.Reason != "" {
			return fmt.Sprintf("unavailable: %s", res.Reason)
		}
		return "unavailable"
	}
	if res.Stat.Empty() {
		return "no drift"
	}
	if res.Truncated {
		return "truncated"
	}
	return fmt.Sprintf("%s, +%d -%d", formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
}

// DriftLine renders the stdout line for drift detected between rounds, or "" when
// there is nothing worth telling the planner (rt.Git off, not a repository, or
// an empty Stat).
//
// round is the opening round; the prose names round-1, the round that closed.
func DriftLine(res DriftResult, round int) string {
	if !res.Available {
		if res.Reason == "" {
			return ""
		}
		return fmt.Sprintf("drift: unavailable (%s)", res.Reason)
	}
	if res.Stat.Empty() {
		return ""
	}
	msg := fmt.Sprintf("drift: %s, +%d -%d between round %d's report and this send",
		formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions, round-1)
	if res.Truncated {
		return msg + "\n       (patch omitted, over the 4 MiB cap)"
	}
	if res.Path != "" {
		return msg + "\n       " + res.Path
	}
	return msg
}

// ReadDrift returns the stored drift patch for one round, and whether one exists.
// It is the read path behind `relay diff --drift`.
//
// Errors: store.ErrNotFound for an unknown binding; a wrapped read error.
func ReadDrift(rt Runtime, name string, round int) ([]byte, bool, error) {
	if _, err := rt.Store.Load(name); err != nil {
		return nil, false, err
	}

	path := rt.Store.DriftPath(name, round)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read drift %s: %w", path, err)
	}
	return data, true, nil
}
