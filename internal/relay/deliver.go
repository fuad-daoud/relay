package relay

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// Delivery is the outcome of one delivery attempt. All-false means "not yet,
// try again next tick".
type Delivery struct {
	Delivered   bool
	Held        bool
	PlannerGone bool
	Empty       bool // the planner was reachable and nothing was waiting
	Reason      string
}

// Queue records a planner-bound payload as pending BEFORE any delivery is
// attempted. A crash between here and confirmation leaves the entry
// unconfirmed, which is exactly how relay notices it on restart.
//
// It takes the caller's tx rather than locking itself: Reconcile (Task 11)
// needs to append this entry inside the same critical section as the round
// advance that follows it, and Go mutexes are not reentrant.
func Queue(_ context.Context, rt Runtime, tx *store.Tx, name string, e store.LogEntry) error {
	if e.Direction != store.DirToPlanner {
		return fmt.Errorf("queue expects a planner-bound entry, got %q", e.Direction)
	}
	if e.Payload == "" {
		return fmt.Errorf("queue expects a payload for binding %q", name)
	}
	if e.TS.IsZero() {
		e.TS = rt.Now().UTC()
	}
	e.Confirmed = false

	return tx.AppendLog(name, e)
}

// DeliverPending attempts the oldest pending payload for one binding. It takes
// the agent list rather than fetching one, so a daemon tick costs exactly one
// herdr call regardless of how many bindings it reconciles.
//
// The hold is the anti-clobber rule: herdr agent prompt types text and presses
// enter, and herdr cannot see the human's input buffer, so injecting into a
// focused planner pane risks merging the payload with a half-typed message.
// Focus alone is no longer the test: on a focused planner, plannerHold applies
// the two signals that mean "nothing to clobber" -- the planner's input box
// reads empty, or its visible screen has been unchanged for rt.HeldGrace. The
// returned binding carries the fingerprint state a hold sets; every other
// return clears it, and the caller must persist the returned binding.
//
// The caller holds the state lock across pending -> prompt -> confirm and
// passes tx in: `relay pull` runs the same sequence from another process, and
// unserialised both could deliver the same payload, and Reconcile needs this
// step inside the same lock as the rest of one binding's advance.
func DeliverPending(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, Delivery, error) {
	planner, ok := FindAgent(agents, b.Planner)
	if !ok {
		return clearPlannerScreen(b), Delivery{PlannerGone: true, Reason: "planner session is gone"}, nil
	}

	if planner.Status != herdr.StatusIdle && planner.Status != herdr.StatusDone {
		return clearPlannerScreen(b), Delivery{Reason: "planner is " + planner.Status}, nil
	}

	pending, idx, found, err := tx.PendingForPlanner(b.Name)
	if err != nil {
		return b, Delivery{}, err
	}
	if !found {
		return clearPlannerScreen(b), Delivery{Empty: true, Reason: "nothing pending"}, nil
	}

	reason := ""
	if planner.Focused {
		var inject bool
		b, inject, reason = plannerHold(ctx, rt, b, planner)
		if !inject {
			// Notify only on the transition into held. The daemon reconciles
			// every couple of seconds and a payload stays held for as long as
			// the human sits in the planner pane, so notifying per tick would
			// fire indefinitely instead of nudging once.
			if b.State != store.StateHeld {
				msg := fmt.Sprintf("%s: round %d payload ready", b.Name, pending.Round)
				if err := rt.Herdr.Notify(ctx, msg); err != nil {
					return b, Delivery{}, fmt.Errorf("notify held delivery: %w", err)
				}
			}

			return b, Delivery{Held: true, Reason: reason}, nil
		}
	}

	// Address the agent FindAgent just located; see internal/ui/fetch.go:170.
	if err := promptWithRetry(ctx, rt, planner.PaneID, pending.Payload); err != nil {
		return b, Delivery{}, fmt.Errorf("prompt planner: %w", err)
	}
	if err := tx.ConfirmIndex(b.Name, idx); err != nil {
		return b, Delivery{}, err
	}

	// The planner is mid-turn on this payload now, but the snapshot still says
	// idle: it was taken once, before the daemon began its pass over the
	// bindings, and nothing refreshes it. Two bindings sharing a planner would
	// both read that stale idle and both type into the pane -- which is the
	// exact clobber the status gate above exists to prevent (#46). Record what
	// we just did, so every later binding in this pass sees the truth and takes
	// the ordinary "planner is working" path. Tick owns a copy of the snapshot,
	// so this never reaches the Herdr client or the next tick.
	if i, ok := findAgentIndex(agents, b.Planner); ok {
		agents[i].Status = herdr.StatusWorking
	}

	return clearPlannerScreen(b), Delivery{Delivered: true, Reason: reason}, nil
}
