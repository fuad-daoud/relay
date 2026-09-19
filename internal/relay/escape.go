package relay

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relay/internal/store"
)

// EscapeOutcome is what a headless round's close decided about worktree
// escape (#192): a headless builder that ran its shell somewhere else and
// executed the round against a directory relay never gave it, leaving its
// own worktree untouched while the source repo it actually worked in went
// dirty.
type EscapeOutcome int

const (
	EscapeNone EscapeOutcome = iota // nothing to say
	EscapeNote                      // annotate the report entry with "escaped"
	EscapeHalt                      // halt NEEDS YOU instead of switching
)

// escapeNote is the report-entry annotation an EscapeNote outcome adds.
const escapeNote = "escaped"

// escapeOutcome is the pure decision behind escapeCheck: given what the
// round's worktree and source repo look like at close, and whether a report
// was produced, what should relay say about it.
//
//   - !treeUnchanged || !repoDirty: nothing suspicious -- EscapeNone.
//   - treeUnchanged && repoDirty && hasReport: the builder still produced a
//     report, so the round closes normally with an "escaped" note.
//   - treeUnchanged && repoDirty && !hasReport: no report and no sign the
//     builder touched its own tree while the source repo moved -- halt
//     rather than switch, since switching would dispatch a new builder into
//     the same broken setup.
func escapeOutcome(treeUnchanged, repoDirty, hasReport bool) EscapeOutcome {
	if !treeUnchanged || !repoDirty {
		return EscapeNone
	}
	if hasReport {
		return EscapeNote
	}
	return EscapeHalt
}

// escapeCheck gathers escapeOutcome's inputs and runs it, for one headless
// round close (#192). It never fails the close it is called from: any git
// error is a skipped check (EscapeNone), logged at Warn, not a reason to
// hold up the round.
//
// Preconditions for running at all -- otherwise EscapeNone without touching
// git: b.Builder.Headless(), b.Repo != "", b.RoundBaselineTree != "", and
// rt.Git != nil.
func escapeCheck(ctx context.Context, rt Runtime, b store.Binding, hasReport bool) EscapeOutcome {
	if !b.Builder.Headless() || b.Repo == "" || b.RoundBaselineTree == "" || rt.Git == nil {
		return EscapeNone
	}

	tree, err := rt.Git.SnapshotTree(ctx, b.CWD)
	if err != nil {
		slog.Warn("escape check skipped", "binding", b.Name, "round", b.Round, "err", err)
		return EscapeNone
	}
	treeUnchanged := tree == b.RoundBaselineTree

	dirty, err := rt.Git.Dirty(ctx, b.Repo)
	if err != nil {
		slog.Warn("escape check skipped", "binding", b.Name, "round", b.Round, "err", err)
		return EscapeNone
	}

	outcome := escapeOutcome(treeUnchanged, dirty, hasReport)
	if outcome != EscapeNone {
		slog.Warn("worktree escape suspected", "binding", b.Name, "round", b.Round, "worktree", b.CWD, "repo", b.Repo)
	}
	return outcome
}

// escapeDiagnosis is the halt message for an EscapeHalt outcome: it names
// both directories and the log, so the human reading NEEDS YOU has enough
// to go straight to the evidence.
func escapeDiagnosis(b store.Binding, codeText string) string {
	return fmt.Sprintf(
		"%s: builder exited (code %s) without a report; worktree %s unchanged since the round began while %s is dirty -- the builder likely worked outside its tree; see %s",
		b.Name, codeText, b.CWD, b.Repo, b.Builder.LogPath)
}
