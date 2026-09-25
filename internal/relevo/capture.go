package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
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

// commitClause renders the commit facts for a diff note (forLine false) or
// the planner's Diff: line (forLine true), or "" when there is nothing to
// say: unknown facts with no reason.
func commitClause(facts CommitResult, branch string, forLine bool) string {
	if !facts.Known {
		if facts.Reason == "" {
			return ""
		}
		return "commits unknown (" + facts.Reason + ")"
	}
	tree := "clean"
	if facts.Dirty {
		tree = "dirty"
	}
	if !forLine {
		if facts.Commits == 0 {
			return "no commits, " + tree
		}
		return fmt.Sprintf("%s, %s", formatCommits(facts.Commits), tree)
	}
	if facts.Commits == 0 {
		if facts.Dirty {
			return "no commits; changes are uncommitted in the worktree"
		}
		return "no commits, tree clean"
	}
	on := ""
	if branch != "" {
		on = " on " + branch
	}
	return fmt.Sprintf("%s%s, tree %s", formatCommits(facts.Commits), on, tree)
}

func formatCommits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

// DiffSummary returns the human summary for the diff log entry, with the
// round's commit facts appended after "; " unless the diff is empty.
func DiffSummary(res DiffResult, facts CommitResult) string {
	var base string
	switch {
	case !res.Available && res.Reason != "":
		base = fmt.Sprintf("unavailable: %s", res.Reason)
	case !res.Available:
		base = "unavailable"
	case res.Stat.Empty():
		return "no changes"
	case res.Truncated:
		base = "truncated"
	default:
		base = fmt.Sprintf("%s, +%d -%d", formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	}
	if clause := commitClause(facts, "", false); clause != "" {
		return base + "; " + clause
	}
	return base
}

// CaptureBaseline returns the tree a round starts from, or "" when no snapshot
// is possible. It NEVER returns an error: a handoff must not fail because a
// snapshot did.
//
// Preconditions:  none.
// Postconditions: "" whenever rt.Git is nil, b.CWD is not a repository, or git
//
//	failed for any reason; a tree id otherwise. head is HEAD's commit id when
//	the snapshot succeeded and HeadCommit did; "" otherwise. A tree without a
//	head is normal (unborn HEAD) and the round then reports commits unknown
//	(no baseline).
func CaptureBaseline(ctx context.Context, rt Runtime, b store.Binding) (tree, head string) {
	if rt.Git == nil || b.CWD == "" {
		return "", ""
	}
	tree, err := rt.Git.SnapshotTree(ctx, b.CWD)
	if err != nil {
		return "", ""
	}
	head, err = rt.Git.HeadCommit(ctx, b.CWD)
	if err != nil {
		return tree, ""
	}
	return tree, head
}

// CaptureRoundDiff closes out the diff for b's current round: it snapshots the
// tree again and compares it against b.RoundBaselineTree, storing the patch as
// a round_file row at rt.Store.DiffPath(b.Name, b.Round) when there is a body
// worth keeping.
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
//	Stat is exact and Path resolves through Store.ReadFile (a round_file
//	row, no file on disk) unless the diff was empty or truncated.
//
//	EndTree is the snapshotted tree whenever the snapshot succeeded, and empty
//	otherwise. It is set independently of Available.
func CaptureRoundDiff(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) DiffResult {
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
	if err := tx.PutRoundFile(b.Name, b.Round, patchPath, diff.Patch); err != nil {
		return DiffResult{Available: false, Reason: brief(err), EndTree: end}
	}

	return DiffResult{Available: true, Path: patchPath, Stat: diff.Stat, EndTree: end}
}

// CommitResult is what one round-end commit-facts capture produced (#130).
// Known is all-or-nothing: either both facts were captured or neither was,
// and Reason names the step that failed ("" for a non-repository, which is
// not worth a sentence).
type CommitResult struct {
	Known   bool   // both facts were captured
	Commits int    // rev-list --count RoundBaselineHead..HEAD; 0 when Known is false
	Dirty   bool   // uncommitted or untracked changes; false when Known is false
	Reason  string // why Known is false; "" when it is true
}

// CommitFacts captures how many commits b's current round added and whether
// its tree is dirty, for the round that is closing. It NEVER returns an
// error: the facts are informational and a round advance must not be blocked
// by them. Runs under the state lock, like CaptureRoundDiff.
//
// Preconditions:  none.
// Postconditions: Known is false with an empty Reason when rt.Git is nil or
//
//	the tree is not a repository; false with a Reason naming the
//	step when RoundBaselineHead is empty or a git call failed; true
//	with both facts otherwise. The sequence HeadCommit, RevListCount,
//	Dirty stops at the first failure.
func CommitFacts(ctx context.Context, rt Runtime, b store.Binding) CommitResult {
	if rt.Git == nil {
		return CommitResult{}
	}
	if b.RoundBaselineHead == "" {
		return CommitResult{Reason: "no baseline"}
	}
	head, err := rt.Git.HeadCommit(ctx, b.CWD)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "head: " + brief(err)}
	}
	n, err := rt.Git.RevListCount(ctx, b.CWD, b.RoundBaselineHead, head)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "rev-list: " + brief(err)}
	}
	dirty, err := rt.Git.Dirty(ctx, b.CWD)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "dirty check: " + brief(err)}
	}
	return CommitResult{Known: true, Commits: n, Dirty: dirty}
}

// DiffLine renders the report-payload line for a result, or "" when the result
// says nothing worth telling the planner (rt.Git off, or not a repository).
// The commit facts follow " -- " unless the diff is empty; branch names the
// binding's branch in the clause, or is "" for a tree relevo did not create.
// DiffLine renders the report-payload Diff: line from a fresh diff result.
// name and round are named so the line can point the planner at
// `relevo show <name> --round <round> --diff` instead of the patch's path
// (P4a round 2 §4.2). branch names the binding's branch in the clause, or is
// "" for a tree relevo did not create.
func DiffLine(res DiffResult, facts CommitResult, branch, name string, round int) string {
	var base string
	switch {
	case !res.Available && res.Reason == "":
		return ""
	case !res.Available:
		base = fmt.Sprintf("Diff: unavailable (%s)", res.Reason)
	case res.Stat.Empty():
		return "Diff: no file changes"
	case res.Truncated:
		base = fmt.Sprintf("Diff: %s, +%d -%d (patch omitted, over the 4 MiB cap)",
			formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	default:
		base = fmt.Sprintf("Diff: %s (%s, +%d -%d)",
			showCommand(name, round, "diff"), formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	}
	if clause := commitClause(facts, branch, true); clause != "" {
		return base + " -- " + clause
	}
	return base
}

// DiffLineFromNote renders the report-payload Diff: line from a diff log
// entry's stored facts (Note, Commits, Tree) rather than a fresh DiffResult:
// the shape a remote binding's catchUp writes, since the wire carries the
// server's diff Note and commit facts, never a patch to re-diff against a
// local baseline. It reuses commitClause -- the same helper DiffLine calls
// -- so the two never drift on how a commit count and a clean/dirty tree
// are worded, and reproduces DiffLine's output exactly whenever the note
// alone says everything DiffLine would ("no changes", "unavailable[:
// reason]"); for every other shape it keeps the note's own wording as the
// base clause, since the original Stat and patch path never crossed the
// wire.
func DiffLineFromNote(note string, commits int, tree, branch string) string {
	if note == "" {
		return ""
	}

	base := note
	if idx := strings.Index(base, "; "); idx != -1 {
		base = base[:idx]
	}

	switch {
	case base == "no changes":
		return "Diff: no file changes"
	case base == "unavailable":
		// Matches DiffLine's own !Available && Reason == "" case: nothing
		// worth telling the planner.
		return ""
	case strings.HasPrefix(base, "unavailable: "):
		base = fmt.Sprintf("unavailable (%s)", strings.TrimPrefix(base, "unavailable: "))
	}

	line := "Diff: " + base
	facts := CommitResult{Known: tree != "", Commits: commits, Dirty: tree == "dirty"}
	if clause := commitClause(facts, branch, true); clause != "" {
		line += " -- " + clause
	}
	return line
}

// pathsClauseRe matches the KindDiff note's changed_paths mismatch clause,
// "paths: report N, diff M" (#216). The clause can sit anywhere in the note,
// because joinNotes appends it after the commit clause.
var pathsClauseRe = regexp.MustCompile(`paths: report (\d+), diff (\d+)`)

// PathsLine renders the report-payload line for a changed_paths mismatch
// (#216), so the planner reading the payload sees the same counts the
// KindDiff note's clause carries. report is the number of paths the report
// listed, diff the number of files the diff touched. Pure.
func PathsLine(report, diff int) string {
	return fmt.Sprintf("Paths: the report's changed_paths lists %d, the diff has %s -- check the diff, not the list",
		report, formatFiles(diff))
}

// PathsLineFromNote renders PathsLine from a KindDiff note -- the shape a
// remote binding's catchUp holds, since the wire carries the server's note
// and never a patch -- or "" when the note carries no "paths: report N, diff
// M" clause. It searches the whole note, so the clause is found wherever
// joinNotes placed it. Pure.
func PathsLineFromNote(note string) string {
	m := pathsClauseRe.FindStringSubmatch(note)
	if m == nil {
		return ""
	}
	report, _ := strconv.Atoi(m[1])
	diff, _ := strconv.Atoi(m[2])
	return PathsLine(report, diff)
}

// ReadDiff returns the stored patch for one round, and whether one exists.
// It is the read path behind `relevo show --diff`.
//
// Errors: store.ErrNotFound for an unknown binding; a wrapped read error.
func ReadDiff(rt Runtime, name string, round int) ([]byte, bool, error) {
	if _, err := rt.Store.Load(name); err != nil {
		return nil, false, err
	}

	path := rt.Store.DiffPath(name, round)
	data, err := rt.Store.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read diff %s: %w", path, err)
	}
	return data, true, nil
}
