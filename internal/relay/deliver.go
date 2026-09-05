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

// DeliverPending attempts the newest pending payload for one binding. It takes
// the agent list rather than fetching one, so a daemon tick costs exactly one
// herdr call regardless of how many bindings it reconciles.
//
// The focus check is the anti-clobber rule: herdr agent prompt types text and
// presses enter, and herdr cannot see the human's input buffer, so injecting
// into a focused planner pane risks merging the payload with a half-typed
// message. Focus is the only proxy available.
//
// The caller holds the state lock across pending -> prompt -> confirm and
// passes tx in: `relay pull` runs the same sequence from another process, and
// unserialised both could deliver the same payload, and Reconcile needs this
// step inside the same lock as the rest of one binding's advance.
func DeliverPending(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (Delivery, error) {
	planner, ok := FindAgent(agents, b.Planner)
	if !ok {
		return Delivery{PlannerGone: true, Reason: "planner session is gone"}, nil
	}

	if planner.Status != herdr.StatusIdle && planner.Status != herdr.StatusDone {
		return Delivery{Reason: "planner is " + planner.Status}, nil
	}

	pending, found, err := tx.PendingForPlanner(b.Name)
	if err != nil {
		return Delivery{}, err
	}
	if !found {
		return Delivery{Reason: "nothing pending"}, nil
	}

	if planner.Focused {
		// Notify only on the transition into held. The daemon reconciles
		// every couple of seconds and a payload stays held for as long as
		// the human sits in the planner pane, so notifying per tick would
		// fire indefinitely instead of nudging once.
		if b.State != store.StateHeld {
			msg := fmt.Sprintf("%s: round %d payload ready", b.Name, pending.Round)
			if err := rt.Herdr.Notify(ctx, msg); err != nil {
				return Delivery{}, fmt.Errorf("notify held delivery: %w", err)
			}
		}

		return Delivery{Held: true, Reason: "planner pane is focused"}, nil
	}

	if err := promptWithRetry(ctx, rt, Target(b.Planner), pending.Payload); err != nil {
		return Delivery{}, fmt.Errorf("prompt planner: %w", err)
	}
	if err := tx.ConfirmLatest(b.Name); err != nil {
		return Delivery{}, err
	}

	return Delivery{Delivered: true}, nil
}
