package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// minInterval keeps a misconfigured interval from spinning the herdr socket.
const minInterval = 500 * time.Millisecond

// Daemon polls herdr and advances every binding. It is the only reason relay
// needs a background process: the inbound leg happens after the planner's turn
// has ended, when no model is running to notice.
type Daemon struct {
	rt       Runtime
	interval time.Duration

	// refresh re-reads candidates.json and policy.json when they have
	// changed and returns the Runtime the tick should use. Nil (the
	// default) keeps today's behaviour: rt is used as given.
	refresh func(Runtime) Runtime

	// applied is what the daemon last reported to herdr per binding
	// (#129): the pane it wrote to, the token fingerprint, and when. In
	// memory only: a daemon restart re-applies everything on its first
	// tick, and metadataRefresh bounds how stale a herdr restart can leave
	// a pane.
	applied map[string]appliedMeta
}

// NewDaemon returns a Daemon ticking at interval, floored at minInterval.
func NewDaemon(rt Runtime, interval time.Duration) *Daemon {
	if interval < minInterval {
		interval = minInterval
	}
	return &Daemon{rt: rt, interval: interval, applied: map[string]appliedMeta{}}
}

// WithRefresh installs a per-tick refresh on the daemon. nil (the default)
// keeps today's behaviour: the Runtime given to NewDaemon is used as-is.
// Returns d for chaining.
func (d *Daemon) WithRefresh(f func(Runtime) Runtime) *Daemon {
	d.refresh = f
	return d
}

// Run ticks until ctx is cancelled. A failing tick is logged and retried on the
// next interval rather than killing the daemon, because a transient herdr
// hiccup must not drop every binding on the floor.
func (d *Daemon) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			if err := d.Tick(ctx); err != nil {
				slog.Error("relay tick failed", "err", err)
			}
		}
	}
}

// Tick reconciles every binding against a single agent list snapshot.
func (d *Daemon) Tick(ctx context.Context) error {
	if d.refresh != nil {
		d.rt = d.refresh(d.rt)
	}

	bindings, err := d.rt.Store.List()
	if err != nil {
		return fmt.Errorf("list bindings: %w", err)
	}
	if len(bindings) == 0 {
		return nil
	}

	agents, err := d.rt.Herdr.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	// Own the snapshot. DeliverPending records a planner it has just prompted
	// by marking it working in this slice, and that record is true only for the
	// remainder of this pass -- it must not reach the Herdr implementation's own
	// storage, nor survive into the next tick, which fetches fresh state anyway.
	agents = append([]herdr.Agent(nil), agents...)

	for _, b := range bindings {
		// One critical section per binding: re-read under the lock, reconcile,
		// and write back without releasing it. Reconcile and everything it
		// calls take the *store.Tx rather than locking themselves, so a CLI
		// command running concurrently cannot land a write between the read
		// and the save.
		err := d.rt.Store.WithLock(func(tx *store.Tx) error {
			fresh, err := tx.Load(b.Name)
			if errors.Is(err, store.ErrNotFound) {
				// A `relay unbind` landed between List and here. That is
				// normal use, not a failure worth logging.
				return nil
			}
			if err != nil {
				return err
			}

			next, err := Reconcile(ctx, d.rt, tx, fresh, agents)
			if err != nil {
				return err
			}
			if store.SameBinding(next, fresh) {
				return nil
			}

			return tx.Save(next)
		})
		if err != nil {
			slog.Error("reconcile failed", "binding", b.Name, "err", err)
		}
	}

	fresh, err := d.rt.Store.List()
	if err != nil {
		slog.Warn("list bindings for metadata sync", "err", err)
		return nil
	}
	syncPaneMetadata(ctx, d.rt, d.applied, fresh)
	notifyFinished(ctx, d.rt, fresh, agents)

	return nil
}
