package relay

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
)

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

// roundExclusionGates is one ledger.Gate per token in b.RoundExcluded --
// candidates that exited without a report during the CURRENT round (#191).
// Pure. switchBuilder folds these into the live ledger gates it passes to
// resolveCandidate, so a mid-round switch never lands the pick back on a
// builder that already proved it cannot finish this round. gatedBuilder is
// NOT changed to look at these: it looks only at RateLimited, so an
// exclusion never triggers a switch by itself -- only the switch's own
// resolution sees it.
func roundExclusionGates(b store.Binding) []ledger.Gate {
	gates := make([]ledger.Gate, 0, len(b.RoundExcluded))
	for _, t := range b.RoundExcluded {
		gates = append(gates, ledger.Gate{
			Token:   t,
			Kind:    ledger.ExitedNoReport,
			Note:    "round " + strconv.Itoa(b.Round),
			Binding: b.Name,
		})
	}
	return gates
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
// RoundStartedAt is restarted, so the replacement gets its own round
// budget, exactly like a fresh handoff.
//
// The replacement is a new headless process started on the same round's
// prompt; closeOld kills the old process first.
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
//
// counted controls whether the switch advances b.RoundSwitches. A rate-limit
// switch -- whether the gate came from a pattern match or from a human's
// `relay unavailable` -- passes false: max_switches counts builders that
// fail, not providers that close, and counting one trigger but not the
// other would make the halt depend on who noticed the gate first. The
// `>= limit` check above is unaffected either way: `0` still disables
// switching, and a binding already at the limit still halts instead of
// switching again.
func switchBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reason string, closeOld, counted bool) (store.Binding, error) {
	limit := rt.Policy.SwitchLimit()
	if b.RoundSwitches >= limit {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: builder %s (%s); already switched %d time(s) this round (max_switches %d)",
			b.Name, reason, b.BuilderCandidate, b.RoundSwitches, limit))
	}

	res, err := resolveCandidate(rt.Candidates, rt.Policy, append(Gates(rt), roundExclusionGates(b)...), "", "builder")
	if err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: builder %s (%s); cannot switch: %v",
			b.Name, reason, b.BuilderCandidate, err))
	}

	if closeOld {
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
	}

	old := b.BuilderCandidate
	now := rt.Now().UTC()

	// The replacement is a headless endpoint, which startRound below fills
	// in (spec §5.4).
	ep, _, err := resolveBuilder(ctx, rt, tx, BindOptions{
		Candidate: res.Token(),
		CWD:       b.CWD,
		Tier:      string(effectiveTier(b)),
	}, b.Name)
	if err != nil {
		// Count the attempt and leave the binding for the next tick.
		if counted {
			b.RoundSwitches++
		}
		b.State = store.StateBroken
		slog.Warn("builder switch failed", "binding", b.Name, "round", b.Round, "pick", res.Token(), "err", err)
		return b, nil
	}

	b.Builder = ep
	b.BuilderCandidate = res.Token()
	if counted {
		b.RoundSwitches++
	}
	b.BuilderMissingSince = time.Time{}
	b.State = store.StateActive

	if err := tx.AppendLog(b.Name, switchEntry(now, b.Round, reason, res)); err != nil {
		return b, err
	}

	appendLogMarker(rt.Store.BuilderLogPath(b.Name, b.Round), now, "switched to "+res.Token()+" ("+reason+")")

	text := composePrompt(b, rt.Store.PlanPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round), rt.Store.DonePath(b.Name, b.Round))
	started, err := startRound(ctx, rt, b, text)
	if err != nil {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: switched builder to %s but could not start round %d: %v",
			b.Name, res.Token(), b.Round, err))
	}
	b = started

	b.RoundStartedAt = now

	slog.Info("builder switched", "binding", b.Name, "round", b.Round,
		"from", old, "to", res.Token(), "reason", reason, "switches", b.RoundSwitches)

	return b, nil
}
