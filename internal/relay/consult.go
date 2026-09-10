package relay

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

const (
	// consultTimeout is how long a consult may stay `working` before relay
	// gives up on it. A consult reads and writes one file; the alternative to a
	// deadline is a record that never becomes terminal and so is never reaped.
	consultTimeout = 10 * time.Minute

	// consultGrace is how long after its single nudge a consult has to write
	// findings before relay reports that it wrote none.
	//
	// A consult does NOT inherit the builder's screen-fingerprint quiescence or
	// scrape fallback. Those exist because a builder runs for hours and a quiet
	// builder is usually a live one waiting on its own sub-agents. A consult
	// runs for a minute or two, so idle plus a nudge plus a minute is enough
	// evidence.
	consultGrace = 60 * time.Second
)

const consultNudgePrompt = `You went idle without writing your findings.

Write them to: %s

Reply here with only that path.`

// reconcileConsults advances every running consult on one binding by one tick.
//
// It reads the agent snapshot it is given rather than fetching one, so a tick
// costs zero additional herdr calls no matter how many consults are attached.
//
// Preconditions:  the caller holds the state lock and passes its tx.
// Postconditions: each running consult is unchanged, nudged, or moved to done
//
//	or silent with exactly one findings entry queued for the
//	transition. Terminal records are never revisited.
func reconcileConsults(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	now := rt.Now().UTC()

	for i := range b.Consults {
		// Terminal records stay until `relay reap` closes their pane. Without
		// this guard every tick re-queues findings that were already
		// delivered, which is one notification per poll, forever.
		if b.Consults[i].State != store.ConsultRunning {
			continue
		}

		agent, live := FindAgent(agents, b.Consults[i].Endpoint)
		if !live {
			// The findings file is the record, not the pane: a consult that
			// wrote and then died has still done its job.
			state, note := store.ConsultSilent, "consult pane is gone"
			if fileExists(b.Consults[i].FindingsPath) {
				state, note = store.ConsultDone, ""
			}
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, state, note); err != nil {
				return b, err
			}
			continue
		}

		b.Consults[i].Endpoint = refreshEndpoint(b.Consults[i].Endpoint, agent)

		idle := agent.Status == herdr.StatusIdle || agent.Status == herdr.StatusDone

		if idle && fileExists(b.Consults[i].FindingsPath) {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultDone, ""); err != nil {
				return b, err
			}
			continue
		}

		// A blocked consult is reported and abandoned, not negotiated with.
		// `relay answer` stays builder-only: a one-shot agent that needs a
		// conversation has already failed its contract. The pane stays open so
		// a human can answer the dialog and the planner can re-ask.
		if agent.Status == herdr.StatusBlocked {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
				"blocked on a prompt in pane "+agent.PaneID); err != nil {
				return b, err
			}
			continue
		}

		if idle {
			if b.Consults[i].NudgedAt.IsZero() {
				text := fmt.Sprintf(consultNudgePrompt, b.Consults[i].FindingsPath)
				if err := promptWithRetry(ctx, rt, agent.PaneID, text); err != nil {
					// A failed prompt is not evidence the consult stopped.
					continue
				}
				b.Consults[i].NudgedAt = now
				continue
			}
			if now.Sub(b.Consults[i].NudgedAt) >= consultGrace {
				var err error
				if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
					"went idle without writing findings"); err != nil {
					return b, err
				}
			}
			continue
		}

		if now.Sub(b.Consults[i].SpawnedAt) >= consultTimeout {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
				"no findings after "+consultTimeout.String()); err != nil {
				return b, err
			}
		}
	}

	return b, nil
}

// finishConsult queues one planner-bound entry and marks the record terminal.
//
// It goes through Queue rather than appending directly, which is what makes
// consults inherit the anti-clobber rule, held, notifications and `relay pull`
// without a line of new delivery code.
func finishConsult(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, i int, state store.ConsultState, note string) (store.Binding, error) {
	c := b.Consults[i]

	entry := store.LogEntry{
		TS:        rt.Now().UTC(),
		Round:     c.Round,
		Direction: store.DirToPlanner,
		Kind:      store.KindFindings,
		Note:      note,
	}

	if state == store.ConsultDone {
		entry.Path = c.FindingsPath
		entry.Payload = fmt.Sprintf("Findings from %s consult %s: %s", c.Role, c.ID, c.FindingsPath)
	} else {
		// No Path: a silent consult wrote no file, and pointing at one that
		// does not exist would send the planner to read nothing.
		entry.Payload = fmt.Sprintf("Consult %s (%s) wrote no findings: %s. Pane %s is still open.",
			c.ID, c.Role, note, c.Endpoint.PaneID)
	}

	if err := Queue(ctx, rt, tx, b.Name, entry); err != nil {
		return b, err
	}

	b.Consults[i].State = state
	b.Consults[i].Note = note

	return b, nil
}

// fileExists is the entire completion gate for a consult. It is a fact, not an
// assessment: relay never reads findings to judge them.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
