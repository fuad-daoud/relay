package relay

import (
	"context"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// Outcome is what a PlannerDeliverer reports back to DeliverPending.
type Outcome int

const (
	// OutcomeNotMine: this deliverer cannot address that planner -- wrong kind,
	// no usable session id, a missing dependency, or it has given up
	// (FallbackAfter). The pane path takes the payload this same tick.
	OutcomeNotMine Outcome = iota
	// OutcomeUnavailable: the push path exists but is not reachable right now.
	// The payload stays pending; the next tick retries.
	OutcomeUnavailable
	// OutcomeDelivered: the planner's session provably received the payload.
	// Only this value may confirm the entry.
	OutcomeDelivered
)

// PlannerDeliverer hands one payload to one planner over that harness's
// own push path. Implementations are keyed by planner kind and are called
// from the daemon's tick goroutine.
//
// Deliver must be idempotent in effect: after any non-OutcomeDelivered outcome it
// is called again next tick with the same payload, so it must not deliver
// twice.
//
// The returned string is a short reason for the log and Delivery.Reason,
// and must never contain a credential. The error is for a bug in relay
// (a request it could not build); an unreachable planner is OutcomeUnavailable,
// not an error.
type PlannerDeliverer interface {
	Deliver(ctx context.Context, planner store.Endpoint, payload, path string, queuedAt time.Time) (Outcome, string, error)
}
