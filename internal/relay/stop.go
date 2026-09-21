package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// DefaultStopGrace is how long a builder gets to wrap up after `relay stop`
// before a pane round is abandoned and the human is told to close its pane
// (#138).
const DefaultStopGrace = 5 * time.Minute

// ErrNothingToStop reports a stop on a binding with no round in flight. The
// CLI turns it into a message and exit 0: nothing to stop is an answer, not
// a failure.
var ErrNothingToStop = errors.New("no round is open; nothing to stop")

// stopPrompt is typed into a pane builder's terminal when a stop is
// requested. It names both files because the round is only recoverable if
// the builder commits and reports before it stops.
const stopPrompt = "relay: stop requested by the planner. Finish the step you are in if it is seconds away, otherwise stop where you are. Commit what should be kept on the current branch. Write your report to %s saying exactly which steps completed and where you stopped, then create %s. Do not start anything new."

// StopOptions is what a stop request may add. A Grace <= 0 means
// DefaultStopGrace; Now skips the wrap-up prompt and abandons at once.
type StopOptions struct {
	Grace time.Duration
	Now   bool
}

// StopResult is what Stop did.
type StopResult struct {
	Round  int
	Action string // "requested" | "killed" | "abandoned" | "nothing"
	Grace  time.Duration
}

// stopAction is what a binding's stop bookkeeping implies should happen next.
type stopAction int

const (
	stopRequest stopAction = iota // no stop yet: ask the builder to wrap up
	stopWait                      // requested, inside the grace: leave it alone
	stopKill                      // grace elapsed on a headless round: kill it
	stopAbandon                   // grace elapsed on a pane round: ask for a human
	stopNothing                   // no round is open
)

// stopGrace is the grace a binding was stopped with, or the default when it
// records none.
func stopGrace(b store.Binding) time.Duration {
	if b.StopGraceMS <= 0 {
		return DefaultStopGrace
	}
	return time.Duration(b.StopGraceMS) * time.Millisecond
}

// stopDecision says what a binding's stop bookkeeping implies, without
// touching it: the one place the pane path, the headless path and the verb
// agree about whether a round is stopping, stopped, or untouched. Pure.
//
// A round is open when its start stamp is set. Nothing requested means the
// caller decides pane vs headless from the endpoint; requested but inside
// the grace means leave the builder alone; past the grace, a headless round
// is killed and a pane round is abandoned to the human.
func stopDecision(b store.Binding, now time.Time) stopAction {
	if b.RoundStartedAt.IsZero() {
		return stopNothing
	}
	if b.StopRequestedAt.IsZero() {
		return stopRequest
	}
	if now.Sub(b.StopRequestedAt) < stopGrace(b) {
		return stopWait
	}
	if b.Builder.Headless() {
		return stopKill
	}
	return stopAbandon
}

// Stop ends an open round on purpose (#138), so the work in it is not lost:
//
//   - a pane builder is asked to wrap up through the same delivery path as a
//     plan; the request and its grace are recorded, and the daemon closes the
//     round on the completion marker as usual. Once the grace elapses with no
//     marker the binding goes NEEDS YOU and relay leaves the pane alone.
//   - a headless builder has no stdin to type into, so it is killed now and
//     its round is closed without a report (`noreport stopped`). A stop is
//     not a failure, so this path never switches builders -- the wrap-up
//     process variant is deferred.
//   - --now skips the prompt for panes too and abandons at once.
//
// It runs entirely under the state lock (the Done/Pause pattern), with
// refusals first so a refusal changes nothing.
func Stop(ctx context.Context, rt Runtime, name string, opts StopOptions) (StopResult, error) {
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

		now := rt.Now().UTC()
		grace := opts.Grace
		if grace <= 0 {
			grace = DefaultStopGrace
		}
		out.Round = b.Round
		out.Grace = grace

		decision := stopDecision(b, now)
		if decision == stopNothing {
			return ErrNothingToStop
		}

		if b.Builder.Headless() {
			// No stdin to type into: kill now, then close the round without
			// a report. ordered so a failed kill leaves the round open and
			// nothing recorded, exactly as `done` does.
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
		}

		if opts.Now || decision == stopAbandon {
			// --now, or a grace that already elapsed: abandon at once. relay
			// never kills a pane; closing it stays the human's decision.
			b, err = haltBinding(ctx, rt, b, fmt.Sprintf(
				"%s: stopped now; the builder pane is still running round %d -- close it yourself", name, b.Round))
			if err != nil {
				return err
			}
			if err := tx.AppendLog(name, store.LogEntry{
				TS: now, Round: b.Round, Direction: store.DirToPlanner,
				Kind: store.KindStop, Note: "stopped/abandoned", Confirmed: true,
			}); err != nil {
				return err
			}
			out.Action = "abandoned"
			return tx.Save(b)
		}

		if decision == stopWait {
			// Already requested and still inside the grace: a second stop is
			// an answer, not an error. Do not prompt the builder twice.
			out.Action = "requested"
			return nil
		}

		// A fresh request. Locate the builder first, the way Send does, so a
		// prompt never falls through to an empty target; then type it through
		// the same delivery path as a plan.
		reportPath := rt.Store.ReportPath(name, b.Round)
		donePath := rt.Store.DonePath(name, b.Round)
		prompt := fmt.Sprintf(stopPrompt, reportPath, donePath)

		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return fmt.Errorf("list agents: %w", err)
		}
		if _, ok := FindAgent(agents, b.Builder); !ok {
			return fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, b.Builder.PaneID, b.BuilderCandidate, ErrBuilderGone)
		}
		if err := promptWithRetry(ctx, rt, b.Builder.PaneID, prompt, reportPath); err != nil && !errors.Is(err, ErrPromptLate) {
			// The prompt is the request; if it never landed, record nothing
			// and let the human retry or pass --now.
			return fmt.Errorf("prompt builder: %w", err)
		}

		b.StopRequestedAt = now
		b.StopGraceMS = int(grace / time.Millisecond)
		if err := tx.AppendLog(name, store.LogEntry{
			TS: now, Round: b.Round, Direction: store.DirToPlanner,
			Kind: store.KindStop, Note: fmt.Sprintf("stop requested (grace %s)", grace), Confirmed: true,
		}); err != nil {
			return err
		}
		out.Action = "requested"
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
