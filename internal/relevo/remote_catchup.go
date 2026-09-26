package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// catchUp fetches a closed round's files and bundle and installs them under
// tx. Callers that fetched ahead pass their fetch to applyCatchUp instead, so
// only the inline one-off path pays the fetch here.
func catchUp(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView) (store.Binding, error) {
	cf := fetchCatchUp(ctx, rt, b, view)
	defer cf.release()
	return applyCatchUp(ctx, rt, tx, b, view, cf)
}

// applyCatchUpFiles renames the fetched report, log and stream temps into
// place and stores the diff and DB-form log under tx. A failure anywhere logs
// as the inline write did and leaves the binding for the next tick.
func applyCatchUpFiles(rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	n := view.ClosedRound
	name := b.Name
	if cf.ReportTemp != "" {
		path := rt.Store.ReportPath(name, n)
		if err := os.Rename(cf.ReportTemp, path); err != nil {
			slog.Warn("write report failed", "path", path, "err", err)
			return false
		}
	}
	if cf.Diff != nil {
		path := rt.Store.DiffPath(name, n)
		if err := tx.PutRoundFile(name, n, path, cf.Diff); err != nil {
			slog.Warn("store diff failed", "path", path, "err", err)
			return false
		}
	}
	logPath := rt.Store.BuilderLogPath(name, n)
	if cf.LogTemp != "" {
		if err := os.Rename(cf.LogTemp, logPath); err != nil {
			slog.Warn("write log failed", "path", logPath, "err", err)
			return false
		}
	}
	if cf.Log != nil {
		if err := tx.PutRoundFile(name, n, logPath, cf.Log); err != nil {
			slog.Warn("write log failed", "path", logPath, "err", err)
			return false
		}
	}
	streamPath := rt.Store.BuilderStreamPath(name, n)
	if cf.StreamTemp != "" {
		if err := os.Rename(cf.StreamTemp, streamPath); err != nil {
			slog.Warn("write stream failed", "path", streamPath, "err", err)
			return false
		}
	}
	return true
}

// applyCatchUpAbsorb records the fetch's bundle outcome in b under tx. stop
// reports whether it already decided the binding's fate: a checked-out branch
// and an absorb failure both wait for the next tick, and the tenth failure
// halts. A fatal ref error is returned to the caller.
func applyCatchUpAbsorb(ctx context.Context, rt Runtime, b store.Binding, cf *catchUpFetch) (next store.Binding, stop bool, err error) {
	switch {
	case cf.CheckedOut:
		return b, true, nil
	case cf.AbsorbErr != nil:
		b.RemoteAbsorbFailures++
		if b.RemoteAbsorbFailures >= 10 {
			next, err = haltBinding(ctx, rt, b, cf.AbsorbErr.Error())
			return next, true, err
		}
		return b, true, nil
	case cf.Fatal != nil:
		return b, true, cf.Fatal
	}
	return b, false, nil
}

// applyCatchUpSettle carries out the apply half's last steps: it records the
// absorbed result, acks the round, and queues the report entry. A failed ack
// leaves the round for the next tick exactly as before.
func applyCatchUpSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) (store.Binding, error) {
	n := view.ClosedRound
	server, name := b.Builder.Server, b.Name

	b.Builder.LastKnown = view.ResultCommit
	b.RemoteAbsorbFailures = 0

	if _, err := rt.Remote.Ack(ctx, server, name, n); err != nil {
		slog.Warn("ack failed", "server", server, "name", name, "round", n, "err", err)
		return b, nil
	}
	return applyCatchUpReport(ctx, rt, tx, b, view, cf)
}

// applyCatchUpReport queues the round's report entry: the client's own diff
// entry from the server's facts first, then the payload, the stop entry and
// the idle status, in the order the inline catch-up used.
func applyCatchUpReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) (store.Binding, error) {
	n := view.ClosedRound
	name := b.Name

	entries, err := tx.ReadLog(name)
	if err != nil {
		return b, err
	}
	// The server already recorded a diff entry at close; writing the client's
	// own from the view's facts makes queueReport skip its own capture.
	if view.DiffNote != "" && !HasEntry(entries, n, store.DirToPlanner, store.KindDiff) {
		diffPath := ""
		if cf.Diff != nil {
			diffPath = rt.Store.DiffPath(name, n)
		}
		diffEntry := store.LogEntry{
			TS: rt.Now().UTC(), Round: n, Direction: store.DirToPlanner, Kind: store.KindDiff,
			Path: diffPath, Note: view.DiffNote, Commits: view.DiffCommits, Tree: view.DiffTree, Confirmed: true,
		}
		if err := tx.AppendLog(name, diffEntry); err != nil {
			return b, err
		}
		entries = append(entries, diffEntry)
	}

	payload, note := catchUpPayload(b, view, cf.ReportTemp != "")
	var u *usage.Usage = view.Usage
	if u == nil {
		// A pre-usage server ships no figure: record honestly that the server
		// sent none rather than reading a record the client does not have.
		u = remoteNoUsage(rt, b, b.RoundStartedAt, rt.Now().UTC())
	}
	next, err := queueReport(ctx, rt, tx, b, entries, rt.Store.ReportPath(name, n), payload, note, nil, u, view.Rusage, view.PriorTokens)
	if err != nil {
		return b, err
	}
	if view.Stopped != "" {
		// After queueReport, so the entry is filed under the stopped round.
		if err := tx.AppendLog(name, store.LogEntry{
			TS: rt.Now().UTC(), Round: n, Direction: store.DirToPlanner,
			Kind: store.KindStop, Note: "stopped/" + view.Stopped, Confirmed: true,
		}); err != nil {
			return next, err
		}
	}
	// The caller (reconcileRemote or SyncRemote) decides whether to deliver.
	next.Builder.RemoteStatus = "idle"
	next.Builder.RemoteQueue = nil
	next.Builder.RemoteLive = nil
	return next, nil
}

// catchUpPayload builds the planner payload and note for a collected round:
// the stopped form when the server stopped it, else the finished form naming
// the report, both carrying the diff's summary lines.
func catchUpPayload(b store.Binding, view remote.BindingView, haveReport bool) (payload, note string) {
	n := view.ClosedRound
	server, name := b.Builder.Server, b.Name
	if view.Stopped != "" {
		payload, note = stopPayload(view.Stopped, name, n, " on "+server, haveReport)
	} else {
		payload = fmt.Sprintf("The runner finished round %d on %s. Report: %s", n, server, showCommand(name, n, "report"))
	}
	if line := capture.DiffLineFromNote(view.DiffNote, view.DiffCommits, view.DiffTree, b.Branch); line != "" {
		payload = payload + "\n" + line
	}
	if line := capture.PathsLineFromNote(view.DiffNote); line != "" {
		payload = payload + "\n" + line
	}
	if view.DirtyCommit != "" {
		note = joinNotes(note, fmt.Sprintf("uncommitted work at refs/relevo/%s/round-%d", name, n))
	}
	return payload, note
}
