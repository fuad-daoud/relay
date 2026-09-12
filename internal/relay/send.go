package relay

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

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
// file rather than something relay reads off the terminal.
const builderPrompt = `Round %d from the planner.
Read: %s
When you are done, write your report to: %s
Reply here with only that path.`

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

	var baseline string
	var hintRound int
	var builder herdr.Agent
	var locatedBuilder bool
	if hint, err := rt.Store.Load(name); err == nil {
		baseline = CaptureBaseline(ctx, rt, hint)
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
		if err := os.WriteFile(planPath, body, 0o644); err != nil {
			return fmt.Errorf("stage plan at %s: %w", planPath, err)
		}

		text := composePrompt(b, planPath, reportPath)

		if b.Builder.Headless() {
			started, err := startRound(ctx, rt, b, text)
			if err != nil {
				// The plan is staged and the round is open; nothing was
				// started. NEEDS YOU says so in status, and the ledger's
				// spawn_failed (written by startRound) gates the candidate
				// for the next pick, as a pane spawn failure would.
				b.State = store.StateNeedsYou
				if saveErr := tx.Save(b); saveErr != nil {
					return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", err, saveErr)
				}
				return err
			}
			b = started
		} else if err := promptWithRetry(ctx, rt, builder.PaneID, text); err != nil {
			if errors.Is(err, herdr.ErrAgentBlocked) {
				return fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
			}
			return fmt.Errorf("prompt builder: %w", err)
		}

		entry := store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round,
			Direction: store.DirToBuilder, Kind: store.KindPlan,
			Path: planPath, Confirmed: true,
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
		b.RoundClosedTree = ""
		b.RoundStartedAt = rt.Now().UTC()
		b.State = store.StateActive

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
func promptWithRetry(ctx context.Context, rt Runtime, target, text string) error {
	err := rt.Herdr.Prompt(ctx, target, text)
	if !errors.Is(err, herdr.ErrPromptStalled) {
		return err
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

// composePrompt renders the builder prompt for this round. Nothing is
// prepended on any round: every kind selects its role with --agent at
// launch (#85), so there is no first-prompt courtesy left to pay.
func composePrompt(b store.Binding, planPath, reportPath string) string {
	return fmt.Sprintf(builderPrompt, b.Round, planPath, reportPath)
}
