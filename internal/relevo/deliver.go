package relevo

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Delivery is the outcome of one delivery attempt. All-false means "not yet,
// try again next tick".
type Delivery struct {
	Delivered bool
	Empty     bool // nothing was pending for this binding
	// Route is how this attempt would deliver, or did: "channel",
	// "deliverer:<kind>" or "pull" (#303 §4.6). "pull" is a route, not a
	// fault: the entry stays pending for `relevo pull`, which is exactly how a
	// Claude Code planner in tools mode gets its report through the
	// background wait (D6).
	Route  string
	Reason string
	// Round is the pending entry's round, set on Delivered and on a left-
	// pending attempt so the caller can log the outcome without re-reading
	// the queue.
	Round int
}

// Queue records a planner-bound payload as pending BEFORE any delivery is
// attempted. A crash between here and confirmation leaves the entry
// unconfirmed, which is exactly how relevo notices it on restart.
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
	e.Payload = WithOrigin(e.Payload, OriginLine(name, e.Round, e.Direction, e.Kind))

	return tx.AppendLog(name, e)
}

// DeliverPending attempts the oldest pending payload for one binding against
// the routes #303 §5.4 defines, in order:
//
//  1. the channel: a live claim for b.PlannerID. The claim holder's own poll
//     pushes the entry and confirms it with route=channel, so this returns
//     without touching the log -- confirming here would empty the mailbox
//     before the channel reader saw it.
//  2. rt.Deliverers[b.Planner.Kind]: #300's port, unchanged. A deliverer that
//     reports OutcomeNotMine leaves the entry pending with its reason; it no
//     longer falls through to a pane (there is none).
//  3. otherwise the entry stays pending with Delivery.Route "pull". For a
//     Claude Code planner in tools mode that is the normal path, not a fault:
//     the background wait's `relevo pull` delivers it.
//
// The caller holds the state lock across pending -> deliver -> confirm and
// passes tx in: `relevo pull` runs the same sequence from another process, and
// unserialised both could deliver the same payload, and Reconcile needs this
// step inside the same lock as the rest of one binding's advance.
func DeliverPending(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, Delivery, error) {
	pending, idx, found, err := tx.PendingForPlanner(b.Name)
	if err != nil {
		return b, Delivery{}, err
	}
	if !found {
		return b, Delivery{Empty: true, Reason: "nothing pending"}, nil
	}

	if rt.Channels != nil && b.PlannerID != "" {
		c, err := rt.Channels.Live(b.PlannerID, rt.Now())
		if err != nil {
			return b, Delivery{}, fmt.Errorf("channel claim: %w", err)
		}
		if c != nil {
			return b, Delivery{Route: "channel", Reason: "planner has a channel", Round: pending.Round}, nil
		}
	}

	kind := b.Planner.Kind
	if d, ok := rt.Deliverers[kind]; ok && kind != "" {
		text, _ := PushText(pending, rt.Store.ReadFile)
		out, reason, err := d.Deliver(ctx, b.Planner, text, pending.Path, pending.TS)
		if err != nil {
			return b, Delivery{}, fmt.Errorf("deliver to planner: %w", err)
		}
		if out == OutcomeDelivered {
			route := "deliverer:" + kind
			if err := tx.ConfirmIndex(b.Name, idx, route); err != nil {
				return b, Delivery{}, err
			}
			return b, Delivery{Delivered: true, Route: route, Reason: reason, Round: pending.Round}, nil
		}
		// OutcomeNotMine and OutcomeUnavailable both leave the entry
		// pending: the deliverer's own reason is the answer, and `relevo pull`
		// is the route that will finally take it.
		return b, Delivery{Route: "pull", Reason: reason, Round: pending.Round}, nil
	}

	return b, Delivery{
		Route:  "pull",
		Reason: fmt.Sprintf("awaiting pull for planner %s (%s)", plannerLabel(rt, b), kind),
		Round:  pending.Round,
	}, nil
}

// plannerLabel names the binding's planner for a delivery reason: the record's
// name when the registry knows it, else the planner id. Pure best-effort; a
// nil registry leaves the id.
func plannerLabel(rt Runtime, b store.Binding) string {
	if b.PlannerID == "" {
		return b.Planner.Kind
	}
	if rt.Planners != nil {
		if rec, err := rt.Planners.Get(b.PlannerID); err == nil && rec.Name != "" {
			return rec.Name
		}
	}
	return b.PlannerID
}
