package relay

import (
	"context"

	"github.com/fuad-daoud/relay/internal/store"
)

// Pull returns the oldest pending payload and marks it delivered with
// route=pull (#303 §5.4), WITHOUT pushing anything. This is the path a
// planner uses mid-turn and the one the background wait runs after every
// `relay wait`: the CLI prints the result to stdout and the planner reads it
// as tool output.
func Pull(_ context.Context, rt Runtime, name string) (string, bool, error) {
	var (
		payload string
		found   bool
	)

	// Same critical section as DeliverPending: the daemon may be delivering
	// this very payload right now, and only one of us may claim it.
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		pending, idx, ok, err := tx.PendingForPlanner(name)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := tx.ConfirmIndex(name, idx, "pull"); err != nil {
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
