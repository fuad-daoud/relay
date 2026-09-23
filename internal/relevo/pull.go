package relevo

import (
	"context"
	"os"

	"github.com/fuad-daoud/relevo/internal/store"
)

// PullOptions selects the text Pull returns for the entry it claims.
type PullOptions struct {
	// PathOnly returns the entry's stored Payload verbatim: the pointer
	// payload (report path and diff line) `relevo pull` printed before it
	// expanded reports. The zero value is false -- the default is to return
	// the report's text.
	PathOnly bool
}

// Pull returns the report's text for the oldest pending entry and marks it
// delivered with route=pull (#303 §5.4), WITHOUT pushing anything. This is
// the path a planner uses mid-turn and the one the background wait runs after
// every `relevo wait`: the CLI prints the result to stdout and the planner
// reads it as tool output.
//
// The text is PushText(entry, os.ReadFile): the stored payload (origin line
// first) plus a blank line plus the report file's text, capped at
// MaxPushBytes. With PullOptions.PathOnly, Pull returns the stored pointer
// payload instead.
func Pull(_ context.Context, rt Runtime, name string, opts PullOptions) (string, bool, error) {
	var (
		entry store.LogEntry
		found bool
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
	if opts.PathOnly {
		return entry.Payload, true, nil
	}

	text, _ := PushText(entry, os.ReadFile)
	return text, true, nil
}
