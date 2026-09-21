package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/transcript"
)

const (
	// consultSpawnTimeout is how long a consult reservation may stay in the
	// spawning state before the daemon expires it. It must exceed Ask's
	// worst-case spawn phase (five herdr calls at the client's 30 s timeout) so
	// a slow live spawn is never expired from under its owner, and it is how
	// long a crashed Ask holds a cap slot. It follows the client timeout, as
	// lockAcquireLimit does.
	consultSpawnTimeout = 5 * time.Minute

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

const consultNudgeFingerprint = "You went idle without writing your findings."

const consultNudgePrompt = consultNudgeFingerprint + `

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

	// Own the slice: the caller compares its copy of the binding against the
	// returned one to decide whether to save, and an in-place write to a
	// shared backing array would change both sides at once.
	b.Consults = append([]store.Consult(nil), b.Consults...)

	for i := range b.Consults {
		// Terminal records stay until `relay reap` closes their pane. Without
		// this guard every tick re-queues findings that were already
		// delivered, which is one notification per poll, forever.
		if b.Consults[i].State == store.ConsultDone || b.Consults[i].State == store.ConsultSilent {
			continue
		}

		if b.Consults[i].State == store.ConsultSpawning {
			// FindAgent is never called for a spawning record: there is no pane
			// yet to match, and a fresh reservation is not gone. If the
			// reservation has expired, finish it as silent.
			if now.Sub(b.Consults[i].SpawnedAt) >= consultSpawnTimeout {
				var err error
				if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
					"spawn did not complete within "+consultSpawnTimeout.String()); err != nil {
					return b, err
				}
			}
			continue
		}

		// A headless consult is a process, not a pane: it is observed through
		// the Runner and its completion is the stream's final message, so the
		// pane steps below (FindAgent, nudge, dialog) never run for one
		// (#147, #144).
		if b.Consults[i].Endpoint.Headless() {
			c := b.Consults[i]

			if rt.Runner == nil {
				// Cannot observe the process; leave the record alone.
				slog.Warn("headless consult but no Runner configured",
					"binding", b.Name, "consult", c.ID)
				continue
			}

			alive, err := rt.Runner.Alive(ctx, handleOf(c.Endpoint))
			if err != nil {
				// An OS hiccup is not evidence the process stopped: treat it as
				// alive this tick, exactly as reconcileHeadless does.
				slog.Warn("headless consult liveness check failed; treating as alive",
					"binding", b.Name, "consult", c.ID, "pid", c.Endpoint.PID, "err", err)
				alive = true
			}

			if alive {
				if now.Sub(c.SpawnedAt) >= consultTimeout {
					if err := rt.Runner.Kill(ctx, handleOf(c.Endpoint)); err != nil {
						slog.Warn("headless consult not killed",
							"binding", b.Name, "consult", c.ID, "pid", c.Endpoint.PID, "err", err)
					}
					var ferr error
					if b, ferr = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
						"timed out after "+consultTimeout.String()+"; process killed"); ferr != nil {
						return b, ferr
					}
				}
				continue
			}

			// Exited. The stream carries the process's final message and the
			// supervisor's exit trailer.
			stream, _ := os.ReadFile(c.Endpoint.LogPath)
			text := transcript.FinalText(c.Endpoint.Kind, stream)
			codeText := "unknown"
			if code, ok := rt.Runner.ExitCode(ctx, handleOf(c.Endpoint), c.Endpoint.LogPath); ok {
				codeText = strconv.Itoa(code)
			}

			if text == "" {
				var ferr error
				if b, ferr = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
					fmt.Sprintf("process exited (code %s) with no final message; see %s",
						codeText, c.Endpoint.LogPath)); ferr != nil {
					return b, ferr
				}
				continue
			}

			// The final message is the findings; relay writes the file the
			// planner is sent to, exactly as a pane consult writes its own.
			if err := os.WriteFile(c.FindingsPath, []byte(text+"\n"), 0o644); err != nil {
				var ferr error
				if b, ferr = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
					"could not write findings: "+brief(err)); ferr != nil {
					return b, ferr
				}
				continue
			}
			var ferr error
			if b, ferr = finishConsult(ctx, rt, tx, b, i, store.ConsultDone, ""); ferr != nil {
				return b, ferr
			}
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

		status := effectiveStatus(b.Consults[i].Endpoint, agent)
		idle := status == herdr.StatusIdle || status == herdr.StatusDone

		if idle && fileExists(b.Consults[i].FindingsPath) {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultDone, ""); err != nil {
				return b, err
			}
			continue
		}

		// A blocked consult is reported and abandoned, not negotiated with.
		// `relay answer` stays builder-only: a one-shot agent that needs a
		// conversation has already failed its contract. The pane is left open
		// so a human can answer the dialog and the planner can re-ask -- but
		// only until the next `relay reap`, which closes the pane of every
		// non-running consult, this one included. The message says so rather
		// than promising a pane that a routine sweep will take away.
		if status == herdr.StatusBlocked {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
				"blocked on a prompt in pane "+agent.PaneID); err != nil {
				return b, err
			}
			continue
		}

		if idle && b.Consults[i].NudgedAt.IsZero() {
			text := fmt.Sprintf(consultNudgePrompt, b.Consults[i].FindingsPath)
			if err := promptWithRetry(ctx, rt, agent.PaneID, text, consultNudgeFingerprint); err == nil || errors.Is(err, ErrPromptLate) {
				b.Consults[i].NudgedAt = now
				continue
			}
			// A failed prompt is not evidence the consult stopped, so it is not
			// recorded as a nudge -- but it must not exempt the consult from its
			// deadline either. Fall through to the consultTimeout check below:
			// a nudge that can never be delivered would otherwise leave the
			// record at ConsultRunning forever, unreapable and holding a slot
			// against ConsultCap.
		} else if idle {
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
		Usage:     recordUsage(ctx, rt, consultSource(rt, b, c, rt.Now().UTC())),
	}

	if state == store.ConsultDone {
		entry.Path = c.FindingsPath
		entry.Payload = fmt.Sprintf("Findings from %s consult %s: %s", c.Role, c.ID, c.FindingsPath)
	} else {
		// No Path: a silent consult wrote no file, and pointing at one that
		// does not exist would send the planner to read nothing.
		if c.Endpoint.PaneID == "" {
			entry.Payload = fmt.Sprintf("Consult %s (%s) wrote no findings: %s. No pane was spawned.",
				c.ID, c.Role, note)
		} else {
			entry.Payload = fmt.Sprintf("Consult %s (%s) wrote no findings: %s. Pane %s is open until the next `relay reap`.",
				c.ID, c.Role, note, c.Endpoint.PaneID)
		}
	}

	// A verify consult is the round-close reviewer (#144): its findings carry
	// a verdict, which is recorded on the entry and on the binding, and its
	// throwaway worktree is removed whatever the terminal state is.
	if c.Role == verifyRole {
		if state == store.ConsultDone {
			body, readErr := os.ReadFile(c.FindingsPath)
			if readErr != nil {
				body = nil
			}
			verdict, reasons := parseVerdict(body)
			entry.Verdict = verdict
			entry.Reasons = reasons
			entry.Payload = fmt.Sprintf("relay: round %d · verdict %s · %d reasons · %s",
				c.Round, verdict, len(reasons), c.FindingsPath)
			b.LastVerdict = &store.Verdict{
				Round:    c.Round,
				Verdict:  verdict,
				Reasons:  reasons,
				Findings: c.FindingsPath,
			}
		} else {
			// No findings file at all: delivered as unstructured, and the
			// payload keeps saying why (#144).
			b.LastVerdict = &store.Verdict{Round: c.Round, Verdict: verdictUnstructured}
		}
	}

	if err := Queue(ctx, rt, tx, b.Name, entry); err != nil {
		return b, err
	}

	b.Consults[i].State = state
	b.Consults[i].Note = note

	if c.Role == verifyRole {
		removeVerifyWorktree(ctx, rt, b, c.Round)
	}

	return b, nil
}

// fileExists is the entire completion gate for a consult. It is a fact, not an
// assessment: relay never reads findings to judge them.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
