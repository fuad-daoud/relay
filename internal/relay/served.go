package relay

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
)

// RoundStateOf returns the execution state of an owned binding (remote-builders spec §3.1).
// It is derived live from the binding and its entries, never stored.
func RoundStateOf(b store.Binding, entries []store.LogEntry) remote.RoundState {
	if b.State == store.StateNeedsYou {
		return remote.RoundNeedsYou
	}
	if HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
		!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport) {
		return remote.RoundRunning
	}
	if b.Serve != nil && b.Serve.ClosedRound > b.Serve.AckedRound {
		return remote.RoundClosed
	}
	return remote.RoundIdle
}

// closeServedRound runs once per closed round on an owned binding, inside
// reconcileHeadless after closeOnMarker reports closed and before
// clearProcess. It never fails the close: any git error is a slog.Warn and
// the facts stay at their previous values, so the owner's next GET shows the
// round still running until the next tick retries.
func closeServedRound(ctx context.Context, rt Runtime, b store.Binding) store.Binding {
	if b.Owner == "" || b.Serve == nil {
		return b
	}
	closed := b.Round - 1
	head, ok, err := rt.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/heads/"+b.Branch)
	if err != nil || !ok {
		slog.Warn("branch missing at close", "binding", b.Name, "branch", b.Branch, "err", err)
		return b
	}
	dirty, err := rt.Git.Dirty(ctx, b.Worktree)
	if err != nil {
		slog.Warn("dirty check failed at close", "binding", b.Name, "err", err)
		return b
	}
	dirtyCommit := ""
	if dirty {
		tree, err := rt.Git.SnapshotTree(ctx, b.Worktree)
		if err != nil {
			slog.Warn("snapshot tree failed at close", "binding", b.Name, "err", err)
			return b
		}
		sha, err := rt.Git.CommitTree(ctx, b.Serve.BareRepo, tree, head,
			fmt.Sprintf("[relay] %s: round %d, uncommitted work", b.Name, closed))
		if err != nil {
			slog.Warn("commit tree failed at close", "binding", b.Name, "err", err)
			return b
		}
		if err := rt.Git.UpdateRef(ctx, b.Serve.BareRepo, fmt.Sprintf("refs/relay/%s/round-%d", b.Name, closed), sha, ""); err != nil {
			slog.Warn("update ref failed at close", "binding", b.Name, "err", err)
			return b
		}
		dirtyCommit = sha
	}
	b.Serve.ClosedRound = closed
	b.Serve.ResultCommit = head
	b.Serve.DirtyCommit = dirtyCommit
	return b
}

// ServedView is the wire view of an owned binding (spec §3.1).
func ServedView(b store.Binding, entries []store.LogEntry) remote.BindingView {
	rState := RoundStateOf(b, entries)
	var halt string
	if b.State == store.StateNeedsYou {
		halt = b.Halt
	}
	var resultCommit, dirtyCommit string
	if rState == remote.RoundClosed && b.Serve != nil {
		resultCommit = b.Serve.ResultCommit
		dirtyCommit = b.Serve.DirtyCommit
	}
	var reportOutcome string
	if b.Serve != nil && b.Serve.ClosedRound > 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Round == b.Serve.ClosedRound && entries[i].Kind == store.KindReport {
				reportOutcome = entries[i].Outcome
				break
			}
		}
	}
	var diffNote, diffTree string
	var diffCommits int
	if b.Serve != nil && b.Serve.ClosedRound > 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Round == b.Serve.ClosedRound && entries[i].Kind == store.KindDiff {
				diffNote = entries[i].Note
				diffCommits = entries[i].Commits
				diffTree = entries[i].Tree
				break
			}
		}
	}
	var ackedRound int
	var closedRound int
	if b.Serve != nil {
		ackedRound = b.Serve.AckedRound
		closedRound = b.Serve.ClosedRound
	}
	return remote.BindingView{
		Name:           b.Name,
		State:          string(b.State),
		Round:          b.Round,
		RoundState:     rState,
		ClosedRound:    closedRound,
		Halt:           halt,
		ResultCommit:   resultCommit,
		DirtyCommit:    dirtyCommit,
		ReportOutcome:  reportOutcome,
		DiffNote:       diffNote,
		DiffCommits:    diffCommits,
		DiffTree:       diffTree,
		AckedRound:     ackedRound,
		Candidate:      b.BuilderCandidate,
		RoundStartedAt: b.RoundStartedAt,
		RoundCap:       b.RoundCap,
		RoundTimeoutMS: b.RoundTimeoutMS,
	}
}

// PickServedCandidate resolves the builder candidate token and harness kind for a served binding.
func PickServedCandidate(rt Runtime, token string) (string, string) {
	if rt.Candidates == nil {
		return token, ""
	}
	res, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), token, "builder")
	if err == nil {
		return res.Candidate.Ref().String(), res.Candidate.Harness
	}
	if token != "" {
		if ref, err := candidate.ParseRef(token); err == nil {
			if c, err := rt.Candidates.Lookup(ref); err == nil {
				return c.Ref().String(), c.Harness
			}
		}
	}
	return token, ""
}
