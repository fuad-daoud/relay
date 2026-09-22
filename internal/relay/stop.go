package relay

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/store"
)

// ErrNothingToStop reports a stop on a binding with no round in flight. The
// CLI turns it into a message and exit 0: nothing to stop is an answer, not
// a failure.
var ErrNothingToStop = errors.New("no round is open; nothing to stop")

// StopOptions is what a stop request may add. It is empty since the
// graceful pane wrap-up (#138) went with pane builders.
type StopOptions struct{}

// StopResult is what Stop did.
type StopResult struct {
	Round  int
	Action string // "killed"
}

// Stop ends an open round on purpose (#138): a headless builder has no
// stdin to type into, so it is killed now and its round is closed (with the
// report when one is on disk, else `noreport stopped`). A stop is not a
// failure, so this path never switches builders -- the wrap-up process
// variant is deferred.
//
// It runs entirely under the state lock (the Done/Pause pattern), with
// refusals first so a refusal changes nothing.
func Stop(ctx context.Context, rt Runtime, name string, _ StopOptions) (StopResult, error) {
	var out StopResult
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		// Refusals first: nothing has changed yet.
		if b.Builder.Remote() {
			return fmt.Errorf("binding %q is remote; relay done ends a remote round", name)
		}
		if b.State == store.StateDone {
			return fmt.Errorf("binding %q is done; nothing to stop", name)
		}
		if b.State == store.StatePaused {
			return fmt.Errorf("binding %q is paused; nothing to stop", name)
		}

		if !b.Builder.Headless() {
			return fmt.Errorf("binding %q: %w", name, ErrPaneBuilder)
		}
		out.Round = b.Round
		if b.RoundStartedAt.IsZero() {
			return ErrNothingToStop
		}

		// Kill now, then close the round without a report. Ordered so a
		// failed kill leaves the round open and nothing recorded, exactly as
		// `done` does.
		if _, err := stopProcess(ctx, rt, b.Builder, "stop"); err != nil {
			return err
		}
		b.Builder = clearProcess(b.Builder)
		b, err = closeStopped(ctx, rt, tx, b, "killed")
		if err != nil {
			return err
		}
		out.Action = "killed"
		return tx.Save(b)
	})
	return out, err
}

// closeStopped closes an open round whose builder was stopped (#138): the
// report is queued if one is on disk, and the round closes without the
// switch a builder's own exit-without-report would trigger -- a stop is not
// a failure. how names the close for the log ("killed").
//
// queueReport does the round advance and clears the stop bookkeeping, so the
// entry appended after it is filed under the round that was stopped.
func closeStopped(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, how string) (store.Binding, error) {
	stoppedRound := b.Round
	reportPath := rt.Store.ReportPath(b.Name, stoppedRound)

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}

	var next store.Binding
	if _, err := os.Stat(reportPath); err == nil {
		next, err = queueReport(ctx, rt, tx, b, entries, reportPath,
			fmt.Sprintf("Builder was stopped (%s) for round %d. Report: %s", how, stoppedRound, reportPath),
			"stopped", nil, nil, nil)
	} else {
		next, err = queueReport(ctx, rt, tx, b, entries, reportPath,
			fmt.Sprintf("Builder was stopped (%s) for round %d; no report was written.", how, stoppedRound),
			"noreport stopped", nil, nil, nil)
	}
	if err != nil {
		return b, err
	}

	if err := tx.AppendLog(b.Name, store.LogEntry{
		TS: rt.Now().UTC(), Round: stoppedRound, Direction: store.DirToPlanner,
		Kind: store.KindStop, Note: "stopped/" + how, Confirmed: true,
	}); err != nil {
		return next, err
	}
	return next, nil
}
