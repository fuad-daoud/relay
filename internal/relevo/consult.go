package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

const (
	// consultSpawnTimeout is how long a consult reservation may stay in the
	// spawning state before the daemon expires it. It must exceed Ask's
	// worst-case spawn phase (a Runner.Start at the client's 30 s timeout) so
	// a slow live spawn is never expired from under its owner, and it is how
	// long a crashed Ask holds a cap slot. It follows the client timeout, as
	// lockAcquireLimit does.
	consultSpawnTimeout = 5 * time.Minute

	// consultTimeout is how long a consult may stay `working` before relevo
	// gives up on it. A consult reads and writes one file; the alternative to a
	// deadline is a record that never becomes terminal and holds a cap slot.
	consultTimeout = 10 * time.Minute
)

// reconcileConsults advances every running consult on one binding by one tick.
//
// A consult is a process since #303, observed through the Runner: its
// completion is the stream's final message, never a pane's screen.
//
// Preconditions:  the caller holds the state lock and passes its tx.
// Postconditions: each running consult is unchanged, or moved to done or silent
//
//	with exactly one findings entry queued for the transition. Terminal
//	records are never revisited.
func reconcileConsults(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	now := rt.Now().UTC()

	// Own the slice: the caller compares its copy of the binding against the
	// returned one to decide whether to save, and an in-place write to a
	// shared backing array would change both sides at once.
	b.Consults = append([]store.Consult(nil), b.Consults...)

	for i := range b.Consults {
		// Terminal records are never revisited. Without this guard every tick
		// re-queues findings that were already delivered, which is one
		// notification per poll, forever.
		if b.Consults[i].State == store.ConsultDone || b.Consults[i].State == store.ConsultSilent {
			continue
		}

		if b.Consults[i].State == store.ConsultSpawning {
			// A spawning record is never reconciled against a live process:
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

		// A consult endpoint with Mode "" predates #303. Pane consults were
		// removed, so relevo can no longer drive it: close it silent with the
		// reason instead of reconciling a pane.
		if !b.Consults[i].Endpoint.Headless() {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
				"pane consults were removed (#303)"); err != nil {
				return b, err
			}
			continue
		}

		// A headless consult is a process, not a pane: it is observed through
		// the Runner and its completion is the stream's final message
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
			if err == nil && alive {
				// A sighting (#370, spec §4.2): this daemon now knows the
				// consult is running, so its own restart is never blamed for
				// this process later.
				rt.Watched.Mark(c.Endpoint.PID, c.Endpoint.StartedAt)
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

			// Exited. The exit trailer is read before the stream's final
			// message (#370, spec §4.5): a process that wrote no trailer was
			// killed before it finished, so whatever text its stream already
			// holds is partial and is never delivered as findings.
			code, ok := rt.Runner.ExitCode(ctx, handleOf(c.Endpoint), c.Endpoint.LogPath)
			if !ok {
				note := "ended without an exit trailer (killed before it finished); partial output: " + c.Endpoint.LogPath
				if lostToRestart(rt, c.Endpoint.PID, c.Endpoint.StartedAt) {
					note = "lost to a daemon restart before it finished (no exit trailer); partial output: " + c.Endpoint.LogPath
				}
				var ferr error
				if b, ferr = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent, note); ferr != nil {
					return b, ferr
				}
				continue
			}
			codeText := strconv.Itoa(code)
			stream, _ := rt.Store.ReadFile(c.Endpoint.LogPath)
			text := transcript.FinalText(c.Endpoint.Kind, stream)

			if text == "" {
				var ferr error
				if b, ferr = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
					fmt.Sprintf("process exited (code %s) with no final message; see %s",
						codeText, c.Endpoint.LogPath)); ferr != nil {
					return b, ferr
				}
				continue
			}

			// The final message is the findings; relevo writes the file the
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

	}

	return b, nil
}

// finishConsult queues one planner-bound entry and marks the record terminal.
//
// It goes through Queue rather than appending directly, which is what makes
// consults inherit the anti-clobber rule, held, notifications and `relevo pull`
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
		// does not exist would send the planner to read nothing. The note is
		// arbitrary text, so trim one trailing period from it: the payload
		// supplies its own, and a doubled period reads as a typo.
		entry.Payload = fmt.Sprintf("Consult %s (%s) wrote no findings: %s.",
			c.ID, c.Role, strings.TrimSuffix(note, "."))
	}

	// A verify consult is the round-close reviewer (#144): its findings carry
	// a verdict, which is recorded on the entry and on the binding, and its
	// throwaway worktree is removed whatever the terminal state is.
	if c.Role == verifyRole {
		if state == store.ConsultDone {
			body, readErr := rt.Store.ReadFile(c.FindingsPath)
			if readErr != nil {
				body = nil
			}
			verdict, reasons := parseVerdict(body)
			entry.Verdict = verdict
			entry.Reasons = reasons
			entry.Payload = fmt.Sprintf("relevo: round %d · verdict %s · %d reasons · %s",
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
