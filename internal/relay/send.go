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

// Send copies the planner's plan into relay state and hands it to the builder.
// It returns a SendResult describing the round and any between-rounds drift.
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
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return SendResult{}, fmt.Errorf("list agents: %w", err)
		}
		var ok bool
		builder, ok = FindAgent(agents, hint.Builder)
		if !ok {
			return SendResult{}, fmt.Errorf("binding %q (pane %s, alias %s): %w", name, hint.Builder.PaneID, hint.BuilderAlias, ErrBuilderGone)
		}
		locatedBuilder = true
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
		if !locatedBuilder {
			return fmt.Errorf("binding %q: %w", name, ErrBuilderGone)
		}
		if !SameAgent(builder, b.Builder) {
			return fmt.Errorf("binding %q (pane %s, alias %s): %w", name, b.Builder.PaneID, b.BuilderAlias, ErrBuilderGone)
		}

		planPath := rt.Store.PlanPath(name, b.Round)
		reportPath := rt.Store.ReportPath(name, b.Round)
		if err := os.WriteFile(planPath, body, 0o644); err != nil {
			return fmt.Errorf("stage plan at %s: %w", planPath, err)
		}

		text, err := composePrompt(rt, b, planPath, reportPath)
		if err != nil {
			return err
		}

		if err := promptWithRetry(ctx, rt, builder.PaneID, text); err != nil {
			if errors.Is(err, herdr.ErrAgentBlocked) {
				return fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
			}
			return fmt.Errorf("prompt builder: %w", err)
		}

		b.PreamblePending = false

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

// composePrompt prepends the alias preamble on round 1, and on any round where
// a rebind left PreamblePending set: a replacement builder is a new session
// that has never selected its role.
func composePrompt(rt Runtime, b store.Binding, planPath, reportPath string) (string, error) {
	text := fmt.Sprintf(builderPrompt, b.Round, planPath, reportPath)
	if !(b.Round == 1 || b.PreamblePending) {
		return text, nil
	}

	// An adopted builder has no alias: the human started it with their own
	// launcher, which already selected the role. Nothing to prepend.
	if b.BuilderAlias == "" {
		return text, nil
	}

	spec, err := rt.Aliases.Lookup(b.BuilderAlias)
	if err != nil {
		return "", err
	}
	if spec.Preamble == "" {
		return text, nil
	}

	return spec.Preamble + "\n\n" + text, nil
}
