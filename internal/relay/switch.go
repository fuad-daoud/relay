package relay

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
)

// switchGrace is how long a builder must be unlocatable before the daemon
// replaces it. Measured from Binding.BuilderMissingSince. It is the same
// 30s as startGrace and for the same reason: herdr's view lags reality, and
// a replacement spawned on a flicker orphans a live builder (#20).
const switchGrace = 30 * time.Second

// gatedBuilder reports the first live rate-limit gate on b's own builder
// candidate, if any. Pure over Gates(rt).
//
// SpawnFailed gates are ignored: a running builder is not a failed spawn, so
// a spawn-failure gate recorded against this same token by an earlier switch
// attempt must not itself trigger another switch.
func gatedBuilder(rt Runtime, b store.Binding) (ledger.Gate, bool) {
	for _, g := range Gates(rt) {
		if g.Token == b.BuilderCandidate && g.Kind == ledger.RateLimited {
			return g, true
		}
	}
	return ledger.Gate{}, false
}

// switchEntry is the log record of one builder switch: why the switch
// happened, and what ExplainResolution says about the pick that replaced
// the builder (spec §3.3, §4.4). Always Confirmed and DirToPlanner, the same
// reasoning as pickEntry: a switch is never a pending payload.
func switchEntry(now time.Time, round int, reason string, res Resolution) store.LogEntry {
	return store.LogEntry{
		TS: now.UTC(), Round: round, Direction: store.DirToPlanner,
		Kind: store.KindSwitch, Confirmed: true,
		Note: "switched builder (" + reason + "): " + ExplainResolution("builder", res),
	}
}

// switchBuilder replaces b's builder mid-round with the next candidate the
// policy order and the ledger's live gates pick, and hands it the SAME
// round's plan. The round number does not change -- the new builder
// inherits the partial diff CaptureRoundDiff already handles -- but
// RoundStartedAt is restarted, so the replacement gets its own startGrace
// before a nudge and its own round budget, exactly like a fresh handoff.
//
// For a headless binding the replacement is a new process started on the
// same round's prompt; closeOld kills the old process instead of closing
// a pane.
//
// closeOld must close the replaced pane before the replacement is spawned:
// herdr agent names are unique, the replacement is again named
// "<name>-builder", and StartAgent refuses that name while the old agent
// under it is still alive. The gone trigger has no pane left to close and
// passes closeOld=false; the gated trigger's pane is still open and passes
// true.
//
// resolveBuilder's own Resolution is always HowExplicit -- it is handed the
// already-chosen token -- and is discarded. The Resolution switchBuilder
// records in the switch log entry is the one it makes itself, by calling
// resolveCandidate with an omitted token, so the audit trail explains the
// real policy decision rather than the trivial "explicit" one. This is the
// same rule Add and Fork follow (policy-order spec §4.3).
//
// A switch tick returns without deliverAndSettle, like the halt paths in
// Reconcile: replacing a builder is the only thing that tick does to this
// binding.
func switchBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reason string, closeOld bool) (store.Binding, error) {
	limit := rt.Policy.SwitchLimit()
	if b.RoundSwitches >= limit {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: builder %s (%s); already switched %d time(s) this round (max_switches %d)",
			b.Name, reason, b.BuilderCandidate, b.RoundSwitches, limit))
	}

	res, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), "", "builder")
	if err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: builder %s (%s); cannot switch: %v",
			b.Name, reason, b.BuilderCandidate, err))
	}

	if closeOld {
		if b.Builder.Headless() {
			// The one place besides done/unbind where relay stops a process
			// it started (#99): the planner gated the provider while the
			// round's process was still running.
			if b.Builder.PID != 0 && rt.Runner != nil {
				if err := rt.Runner.Kill(ctx, handleOf(b.Builder)); err != nil {
					return haltBinding(ctx, rt, b, fmt.Sprintf(
						"%s: builder %s; could not stop its process %d to replace it: %v",
						b.Name, reason, b.Builder.PID, err))
				}
			}
		} else if err := rt.Herdr.ClosePane(ctx, b.Builder.PaneID); err != nil {
			return haltBinding(ctx, rt, b, fmt.Sprintf(
				"%s: builder %s; could not close its pane %s to replace it: %v",
				b.Name, reason, b.Builder.PaneID, err))
		}
	}

	old := b.BuilderCandidate
	now := rt.Now().UTC()

	// The replacement inherits the mode (spec §5.4): a headless binding gets
	// a headless endpoint, which startRound below fills in.
	ep, _, err := resolveBuilder(ctx, rt, tx, BindOptions{Candidate: res.Token(), CWD: b.CWD, Headless: b.Builder.Headless()}, b.Name, b.Planner.PaneID)
	if err != nil {
		// resolveBuilder already recorded spawn_failed for the pick, which
		// gates it for the next resolution. Count the attempt and leave the
		// binding for the next tick: with the old pane closed (or already
		// gone) the gone trigger fires again after switchGrace and walks on
		// to the next candidate.
		b.RoundSwitches++
		b.State = store.StateBroken
		slog.Warn("builder switch failed", "binding", b.Name, "round", b.Round, "pick", res.Token(), "err", err)
		return b, nil
	}

	b.Builder = ep
	b.BuilderCandidate = res.Token()
	b.RoundSwitches++
	b.BuilderMissingSince = time.Time{}
	b.BuilderScreen = ""
	b.BuilderScreenAt = time.Time{}
	b.State = store.StateActive

	if err := tx.AppendLog(b.Name, switchEntry(now, b.Round, reason, res)); err != nil {
		return b, err
	}

	text := composePrompt(b, rt.Store.PlanPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round))
	if b.Builder.Headless() {
		started, err := startRound(ctx, rt, b, text)
		if err != nil {
			return haltBinding(ctx, rt, b, fmt.Sprintf(
				"%s: switched builder to %s but could not start round %d: %v",
				b.Name, res.Token(), b.Round, err))
		}
		b = started
	} else if err := promptWithRetry(ctx, rt, ep.PaneID, text); err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: switched builder to %s but could not hand it round %d: %v",
			b.Name, res.Token(), b.Round, err))
	}

	b.RoundStartedAt = now

	if err := rt.Herdr.Notify(ctx, fmt.Sprintf("%s: switched builder to %s (%s)", b.Name, res.Token(), reason)); err != nil {
		slog.Warn("switch notify failed", "binding", b.Name, "err", err)
	}

	slog.Info("builder switched", "binding", b.Name, "round", b.Round,
		"from", old, "to", res.Token(), "reason", reason, "switches", b.RoundSwitches)

	return b, nil
}
