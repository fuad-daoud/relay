package relay

import (
	"context"

	"github.com/fuad-daoud/relay/internal/store"
)

// Pull returns the newest pending payload and marks it delivered, WITHOUT
// injecting anything. This is the path the planner uses mid-turn: the CLI
// prints the result to stdout and the planner reads it as tool output, so it
// can neither collide with the human's typing nor be rejected by herdr.
func Pull(_ context.Context, rt Runtime, name string) (string, bool, error) {
	var (
		payload string
		found   bool
	)

	// Same critical section as DeliverPending: the daemon may be delivering
	// this very payload right now, and only one of us may claim it.
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		pending, ok, err := tx.PendingForPlanner(name)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := tx.ConfirmLatest(name); err != nil {
			return err
		}

		payload, found = pending.Payload, true

		return nil
	})
	if err != nil {
		return "", false, err
	}

	return payload, found, nil
}
