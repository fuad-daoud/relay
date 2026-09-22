package relay

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relay/internal/store"
)

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
	e.Payload = WithOrigin(e.Payload, OriginLine(name, e.Round, e.Direction, e.Kind))

	return tx.AppendLog(name, e)
}

// deliverAndSettle is where the daemon used to type a queued payload into
// the planner's pane. With no pane delivery, a planner-bound payload stays
// queued until the planner's `relay mcp` channel drains it (drain.go) or the
// planner runs `relay pull` / `relay wait`.
//
// SPIKE(decision): a planner with no push channel gets reports only by
// wait/pull (#303 open decision 2). A binding persisted as HELD by an older
// daemon settles back to ACTIVE here, since nothing holds a payload any more.
func deliverAndSettle(_ context.Context, _ Runtime, _ *store.Tx, b store.Binding) (store.Binding, error) {
	if b.State == store.StateHeld {
		b.State = store.StateActive
	}
	return b, nil
}
