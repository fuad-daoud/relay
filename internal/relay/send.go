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

// builderPrompt is the fixed handoff template. It names both paths explicitly
// because alternate-screen output is unrecoverable, so the report must be a
// file rather than something relay reads off the terminal.
const builderPrompt = `Round %d from the planner.
Read: %s
When you are done, write your report to: %s
Reply here with only that path.`

// Target is the herdr target for an endpoint: its agent name when relay
// started it, otherwise its pane id.
func Target(ep store.Endpoint) string {
	if ep.AgentName != "" {
		return ep.AgentName
	}
	return ep.PaneID
}

// Send copies the planner's plan into relay state and hands it to the builder.
// It returns the round number it was filed under.
func Send(ctx context.Context, rt Runtime, name, file string) (int, error) {
	// Read the caller's file before taking the lock; it is the one input that
	// does not depend on binding state.
	body, err := os.ReadFile(file)
	if err != nil {
		return 0, fmt.Errorf("read plan %s: %w", file, err)
	}

	var round int

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

		planPath := rt.Store.PlanPath(name, b.Round)
		reportPath := rt.Store.ReportPath(name, b.Round)
		if err := os.WriteFile(planPath, body, 0o644); err != nil {
			return fmt.Errorf("stage plan at %s: %w", planPath, err)
		}

		text, err := composePrompt(rt, b, planPath, reportPath)
		if err != nil {
			return err
		}

		if err := promptWithRetry(ctx, rt, Target(b.Builder), text); err != nil {
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

		round = b.Round
		b.RoundStartedAt = rt.Now().UTC()
		b.State = store.StateActive

		return tx.Save(b)
	})
	if err != nil {
		return 0, err
	}

	return round, nil
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

// composePrompt prepends the alias preamble on round 1 only. Harnesses without
// a role flag select their role there and remember it for the session.
func composePrompt(rt Runtime, b store.Binding, planPath, reportPath string) (string, error) {
	text := fmt.Sprintf(builderPrompt, b.Round, planPath, reportPath)
	if b.Round != 1 {
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
