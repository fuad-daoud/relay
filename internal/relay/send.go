package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrPromptLate is returned by promptWithRetry in place of nil when the first
// stall was contradicted by the screen. Every caller treats it as success and
// records/logs late.
var ErrPromptLate = errors.New("prompt landed late")

// lateScanLines is how many lines are read from the visible source when
// confirming a fingerprint.
const lateScanLines = 40

// ErrBuilderBlocked reports that the builder is sitting at a dialog, so a plan
// cannot be submitted until the planner answers it with `relay answer`.
var ErrBuilderBlocked = errors.New("builder is blocked at a dialog; answer it with relay answer")

// ErrBuilderGone reports that a binding's builder could not be located among
// the live agents, so there is nothing to address.
var ErrBuilderGone = errors.New("builder is gone; rebind before sending")

// ErrBuilderNotBlocked is returned when `relay answer` is asked to type into a
// builder that is not at a dialog. herdr's blocked-detection false-positives
// (#55), and relay prints an instruction to answer whenever it fires, so the
// guard has to live where the keystrokes are sent rather than in the prose.
var ErrBuilderNotBlocked = errors.New("builder is not blocked; nothing to answer")

// builderPrompt is the fixed handoff template. It names both paths explicitly
// because alternate-screen output is unrecoverable, so the report must be a
// file rather than something relay reads off the terminal. The marker is the
// builder's own "the tree is final": relay closes the round on it, not on the
// report appearing (spec 2026-09-12-completion-marker §1).
//
// It opens by naming the working tree and a halt rule (#192): a headless
// agy builder has been observed to run its shell somewhere else and execute
// a round against the planner's main checkout instead of its own worktree.
// Telling the builder which tree is its own, and to check with `git status`
// before doing anything else, is relay's second line of defence alongside
// pinning the process's workspace with --add-dir.
const builderPrompt = `Your working tree is: %s
It is the only tree you may touch. Before anything else, run ` + "`git status`" + `
there. If that fails, or reports a different directory or branch than you
expect for this tree, stop: write a report saying so, create the done marker,
and do nothing else.

Read: %s
When you are done, write your report to: %s
Then, as the very last thing you do -- after every edit, test and commit --
create this empty file: %s
End the report with this block as its last lines, filled in honestly:

` + "```relay" + `
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # commands you ran, e.g. ["make check"]
not_done: []            # adjacent work you deliberately left
` + "```" + `
Reply here with only the report path.`

// Target is the herdr target for an endpoint: its pane id, which Reconcile
// keeps current by refreshing every endpoint it locates. AgentName is
// provenance rather than an address, because herdr can forget it across a
// server restart while the pane stays addressable (#20).
func Target(ep store.Endpoint) string {
	return ep.PaneID
}

// SendResult is what one successful Send produced.
type SendResult struct {
	Round int    // the round the plan was filed under
	Drift string // the drift line for stdout, or "" when there is nothing to say
}

// Send copies the planner's plan into relay state and hands it to the builder:
// typed into its pane, or -- for a headless binding (#99) -- as the prompt of
// a fresh process started in the binding's tree. It returns a SendResult
// describing the round and any between-rounds drift.
func Send(ctx context.Context, rt Runtime, name, file string) (SendResult, error) {
	// Read the caller's file before taking the lock; it is the one input that
	// does not depend on binding state.
	body, err := os.ReadFile(file)
	if err != nil {
		return SendResult{}, fmt.Errorf("read plan %s: %w", file, err)
	}

	var baseline, baselineHead string
	var hintRound int
	var builder herdr.Agent
	var locatedBuilder bool
	if hint, err := rt.Store.Load(name); err == nil {
		baseline, baselineHead = CaptureBaseline(ctx, rt, hint)
		hintRound = hint.Round
		// A headless builder (#99) is a process relay starts per round; there
		// is no herdr agent to find. Its liveness check is under the lock.
		if !hint.Builder.Headless() {
			agents, err := rt.Herdr.ListAgents(ctx)
			if err != nil {
				return SendResult{}, fmt.Errorf("list agents: %w", err)
			}
			var ok bool
			builder, ok = FindAgent(agents, hint.Builder)
			if !ok {
				return SendResult{}, fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, hint.Builder.PaneID, hint.BuilderCandidate, ErrBuilderGone)
			}
			locatedBuilder = true
		}
	}

	var round int
	var driftLineOut string

	// The whole round advance is one critical section: the daemon rewrites this
	// same binding on every tick, and a lost update here would re-send a plan
	// the builder already has. `Prompt` does not wait on the agent, so holding
	// the lock across it costs milliseconds, not the length of a turn.
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		if b.State == store.StateBroken {
			return fmt.Errorf("binding %q is broken; rebind before sending", name)
		}
		if b.Round > b.RoundCap {
			return fmt.Errorf("binding %q hit its round cap of %d", name, b.RoundCap)
		}
		// The pre-lock load and this locked load are two separate acquisitions
		// of the state lock, so a binding can appear between them. An unlocated
		// builder must never fall through to an empty target.
		if b.Builder.Headless() {
			// One process per round (headless spec §5.2): a previous round's
			// process still running means the human is early, not that
			// relay should start a second builder in the same tree.
			if b.Builder.PID != 0 {
				if rt.Runner == nil {
					return fmt.Errorf("binding %q: %w", name, ErrRunnerUnavailable)
				}
				alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
				if err != nil {
					return fmt.Errorf("binding %q: check previous process %d: %w", name, b.Builder.PID, err)
				}
				if alive {
					return fmt.Errorf("binding %q (pid %d): %w", name, b.Builder.PID, ErrBuilderBusy)
				}
			}
		} else {
			if !locatedBuilder {
				return fmt.Errorf("binding %q: %w", name, ErrBuilderGone)
			}
			if !SameAgent(builder, b.Builder) {
				return fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, b.Builder.PaneID, b.BuilderCandidate, ErrBuilderGone)
			}
		}

		planPath := rt.Store.PlanPath(name, b.Round)
		reportPath := rt.Store.ReportPath(name, b.Round)
		donePath := rt.Store.DonePath(name, b.Round)
		if err := os.WriteFile(planPath, body, 0o644); err != nil {
			return fmt.Errorf("stage plan at %s: %w", planPath, err)
		}

		text := composePrompt(b, planPath, reportPath, donePath)

		late := false
		if b.Builder.Headless() {
			started, err := startRound(ctx, rt, b, text)
			if err != nil {
				// The plan is staged and the round is open; nothing was
				// started. NEEDS YOU says so in status, and the ledger's
				// spawn_failed (written by startRound) gates the candidate
				// for the next pick, as a pane spawn failure would.
				b.State = store.StateNeedsYou
				b.Halt = "builder spawn failed: " + err.Error()
				b.HaltAt = rt.Now().UTC()
				if saveErr := tx.Save(b); saveErr != nil {
					return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", err, saveErr)
				}
				return err
			}
			b = started
		} else {
			if builder.Status == herdr.StatusUnknown {
				patterns := dialogPatterns(rt, builder.Kind, b.BuilderCandidate)
				if dialogGuard(ctx, rt, builder.PaneID, patterns) {
					return fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
				}
			}

			if err := promptWithRetry(ctx, rt, builder.PaneID, text, planPath); err != nil {
				if errors.Is(err, ErrPromptLate) {
					late = true
				} else if errors.Is(err, herdr.ErrAgentBlocked) {
					return fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
				} else {
					return fmt.Errorf("prompt builder: %w", err)
				}
			}
		}

		entry := store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round,
			Direction: store.DirToBuilder, Kind: store.KindPlan,
			Path: planPath, Confirmed: true, Late: late,
		}
		if err := tx.AppendLog(name, entry); err != nil {
			return err
		}

		driftLine := ""
		if b.Round == hintRound {
			res := CaptureDrift(ctx, rt, b, baseline)
			if (res.Available && !res.Stat.Empty()) || res.Reason != "" {
				driftEntry := store.LogEntry{
					TS: rt.Now().UTC(), Round: b.Round,
					Direction: store.DirToPlanner, Kind: store.KindDrift,
					Path: res.Path, Note: DriftSummary(res),
					Confirmed: true,
				}
				if err := tx.AppendLog(name, driftEntry); err != nil {
					return err
				}
				driftLine = DriftLine(res, b.Round)
			}
		}

		round = b.Round
		driftLineOut = driftLine
		b.RoundBaselineTree = baseline
		b.RoundBaselineHead = baselineHead
		b.RoundClosedTree = ""
		b.RoundStartedAt = rt.Now().UTC()
		b.State = store.StateActive
		b.Halt = ""
		b.HaltAt = time.Time{}

		return tx.Save(b)
	})
	if err != nil {
		return SendResult{}, err
	}

	return SendResult{Round: round, Drift: driftLineOut}, nil
}

// promptWithRetry retries once past herdr's five second stall detection, then
// gives up. It never fires a third time: a double-submitted plan means two
// builders' worth of edits, which is worse than a stalled round.
func promptWithRetry(ctx context.Context, rt Runtime, target, text, fingerprint string) error {
	err := rt.Herdr.Prompt(ctx, target, text)
	if !errors.Is(err, herdr.ErrPromptStalled) {
		return err
	}

	if fingerprint != "" {
		screen, rerr := rt.Herdr.ReadAgentSource(ctx, target, "visible", lateScanLines)
		if rerr != nil {
			slog.Warn("late check: screen unreadable", "target", target, "err", rerr)
		} else if strings.Contains(screen, fingerprint) {
			return ErrPromptLate
		}
	}

	if retryErr := rt.Herdr.Prompt(ctx, target, text); retryErr != nil {
		// Only a second stall is a stall. The retry can fail for an unrelated
		// reason -- the builder became blocked between the two attempts, say --
		// and reporting that as a stall sends the human looking at the wrong
		// thing.
		if errors.Is(retryErr, herdr.ErrPromptStalled) {
			return fmt.Errorf("prompt %s stalled twice: %w", target, retryErr)
		}
		return fmt.Errorf("prompt %s failed on retry: %w", target, retryErr)
	}

	return nil
}

// composePrompt renders the builder prompt for this round. Line 1 is the
// origin line naming the round and builder, followed by a blank line and the
// handoff text (#139).
func composePrompt(b store.Binding, planPath, reportPath, donePath string) string {
	origin := OriginLine(b.Name, b.Round, store.DirToBuilder, store.KindPlan)
	body := fmt.Sprintf(builderPrompt, b.CWD, planPath, reportPath, donePath)
	return origin + "\n\n" + body
}
