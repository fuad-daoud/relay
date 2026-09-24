package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/store"
)

// pullPending returns the oldest pending entry's text for name and marks it
// delivered with route, WITHOUT pushing anything. It is what the removed pull
// verb did, and the helper `relevo wait` calls once its round has ended
// (P4a round 2 §4.1, #303 §5.4): the CLI prints the result to
// stdout and the planner reads it as tool output.
//
// The text is PushText(entry, name, rt.Store.ReadFile): the stored payload
// (origin line first) plus a blank line plus the report file's text, capped at
// MaxPushBytes. found is false when nothing is pending.
func pullPending(_ context.Context, rt Runtime, name, route string) (text string, found bool, err error) {
	var entry store.LogEntry

	// Same critical section as DeliverPending: the daemon may be delivering
	// this very payload right now, and only one of us may claim it.
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		pending, idx, ok, err := tx.PendingForPlanner(name)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := tx.ConfirmIndex(name, idx, route); err != nil {
			return err
		}

		entry, found = pending, true

		return nil
	})
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}

	// The file read happens outside the lock: no file I/O under the state
	// lock.
	text, _ = PushText(entry, name, rt.Store.ReadFile)
	return text, true, nil
}
