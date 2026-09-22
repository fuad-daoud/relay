package relay

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/transcript"
)

const (
	// consultSpawnTimeout is how long a consult reservation may stay in the
	// spawning state before the daemon expires it. It must exceed Ask's
	// worst-case spawn phase so a slow live spawn is never expired from under
	// its owner, and it is how long a crashed Ask holds a cap slot.
	consultSpawnTimeout = 5 * time.Minute

	// consultTimeout is how long a consult may stay `working` before relay
	// gives up on it. A consult reads and writes one file; the alternative to a
	// deadline is a record that never becomes terminal and so is never reaped.
	consultTimeout = 10 * time.Minute
)

// reconcileConsults advances every running consult on one binding by one tick.
//
// Preconditions:  the caller holds the state lock and passes its tx.
// Postconditions: each running consult is unchanged or moved to done
//
//	or silent with exactly one findings entry queued for the
//	transition. Terminal records are never revisited.
func reconcileConsults(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
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
			// A fresh reservation is not gone. If the
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

		// A consult is a headless process: it is observed through the Runner
		// and its completion is the stream's final message (#147, #144).
		if !b.Consults[i].Endpoint.Headless() {
			// A pane consult persisted by an older relay: nothing can observe
			// it any more, so it finishes on whatever it left on disk.
			state, note := store.ConsultSilent, "pane consults are no longer supported"
			if fileExists(b.Consults[i].FindingsPath) {
				state, note = store.ConsultDone, ""
			}
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, state, note); err != nil {
				return b, err
			}
			continue
		}
		{
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
			// planner is sent to.
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

	}

	return b, nil
}

// finishConsult queues one planner-bound entry and marks the record terminal.
//
// It goes through Queue rather than appending directly, which is what makes
// consults inherit channel delivery and `relay pull` without a line of new
// delivery code.
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
		entry.Payload = fmt.Sprintf("Consult %s (%s) wrote no findings: %s.",
			c.ID, c.Role, note)
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
