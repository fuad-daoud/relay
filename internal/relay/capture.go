package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/store"
)

// DiffResult is what one round-end capture attempt produced. A zero value means
// "nothing to say about this round's diff", which is a normal outcome.
type DiffResult struct {
	Available bool     // a comparison actually ran
	Path      string   // patch file; "" when there is no body to read
	Stat      git.Stat // exact whenever Available
	Truncated bool     // patch omitted because it exceeded the cap
	Reason    string   // why Available is false; "" when it is true

	// The tree snapshotted at round close. Non-empty whenever the snapshot itself
	// succeeded -- including when the subsequent DiffTrees failed, because a
	// successful snapshot is a valid drift origin regardless of what the comparison
	// did.
	EndTree string
}

func formatFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

func brief(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if idx := strings.Index(s, "\n"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

// DiffSummary returns the human summary for the diff log entry.
func DiffSummary(res DiffResult) string {
	if !res.Available {
		if res.Reason != "" {
			return fmt.Sprintf("unavailable: %s", res.Reason)
		}
		return "unavailable"
	}
	if res.Stat.Empty() {
		return "no changes"
	}
	if res.Truncated {
		return "truncated"
	}
	return fmt.Sprintf("%s, +%d -%d", formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
}

// CaptureBaseline returns the tree a round starts from, or "" when no snapshot
// is possible. It NEVER returns an error: a handoff must not fail because a
// snapshot did.
//
// Preconditions:  none.
// Postconditions: "" whenever rt.Git is nil, b.CWD is not a repository, or git
//
//	failed for any reason; a tree id otherwise.
func CaptureBaseline(ctx context.Context, rt Runtime, b store.Binding) string {
	if rt.Git == nil || b.CWD == "" {
		return ""
	}
	treeID, err := rt.Git.SnapshotTree(ctx, b.CWD)
	if err != nil {
		return ""
	}
	return treeID
}

// CaptureRoundDiff closes out the diff for b's current round: it snapshots the
// tree again and compares it against b.RoundBaselineTree, writing the patch to
// rt.Store.DiffPath(b.Name, b.Round) when there is a body worth keeping.
//
// When no baseline is available, CaptureRoundDiff performs no git calls at
// all. The check is ordered ahead of the snapshot because the snapshot is the
// expensive half and this function runs with the state lock held.
//
// It NEVER returns an error, for the same reason: a round advance must not be
// blocked by a failed diff. Every failure lands in DiffResult.Reason.
//
// Preconditions:  none.
// Postconditions: Available is false with a Reason when rt.Git is nil, the
//
//	baseline is empty, or git failed. When Available is true,
//	Stat is exact and Path names an existing file unless the diff
//	was empty or truncated.
//
//	EndTree is the snapshotted tree whenever the snapshot succeeded, and empty
//	otherwise. It is set independently of Available.
func CaptureRoundDiff(ctx context.Context, rt Runtime, b store.Binding) DiffResult {
	if rt.Git == nil {
		return DiffResult{Available: false}
	}
	if b.RoundBaselineTree == "" {
		return DiffResult{Available: false, Reason: "no baseline"}
	}

	end, err := rt.Git.SnapshotTree(ctx, b.CWD)
	if errors.Is(err, git.ErrNotRepo) {
		return DiffResult{Available: false}
	}
	if err != nil {
		return DiffResult{Available: false, Reason: brief(err)}
	}

	diff, err := rt.Git.DiffTrees(ctx, b.CWD, b.RoundBaselineTree, end)
	if errors.Is(err, git.ErrNotRepo) {
		return DiffResult{Available: false, EndTree: end}
	}
	if err != nil {
		return DiffResult{Available: false, Reason: brief(err), EndTree: end}
	}

	if diff.Stat.Empty() {
		return DiffResult{Available: true, Stat: diff.Stat, EndTree: end}
	}

	if diff.Truncated {
		return DiffResult{Available: true, Stat: diff.Stat, Truncated: true, EndTree: end}
	}

	patchPath := rt.Store.DiffPath(b.Name, b.Round)
	if err := os.WriteFile(patchPath, diff.Patch, 0o644); err != nil {
		return DiffResult{Available: false, Reason: brief(err), EndTree: end}
	}

	return DiffResult{Available: true, Path: patchPath, Stat: diff.Stat, EndTree: end}
}

// DiffLine renders the report-payload line for a result, or "" when the result
// says nothing worth telling the planner (rt.Git off, or not a repository).
func DiffLine(res DiffResult) string {
	if !res.Available {
		if res.Reason == "" {
			return ""
		}
		return fmt.Sprintf("Diff: unavailable (%s)", res.Reason)
	}
	if res.Stat.Empty() {
		return "Diff: no file changes"
	}
	if res.Truncated {
		return fmt.Sprintf("Diff: %s, +%d -%d (patch omitted, over the 4 MiB cap)",
			formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	}
	return fmt.Sprintf("Diff: %s (%s, +%d -%d)",
		res.Path, formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
}

// ReadDiff returns the stored patch for one round, and whether one exists.
// It is the read path behind `relay diff`.
//
// Errors: store.ErrNotFound for an unknown binding; a wrapped read error.
func ReadDiff(rt Runtime, name string, round int) ([]byte, bool, error) {
	if _, err := rt.Store.Load(name); err != nil {
		return nil, false, err
	}

	path := rt.Store.DiffPath(name, round)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read diff %s: %w", path, err)
	}
	return data, true, nil
}
