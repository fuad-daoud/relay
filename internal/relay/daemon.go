package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relay/internal/ingest"
	"github.com/fuad-daoud/relay/internal/store"
)

// minInterval keeps a misconfigured interval from spinning the daemon.
const minInterval = 500 * time.Millisecond

// Daemon polls the store and advances every binding. It is the only reason
// relay needs a background process: the inbound leg happens after the
// planner's turn has ended, when no model is running to notice.
type Daemon struct {
	rt       Runtime
	interval time.Duration

	// refresh re-reads candidates.json and policy.json when they have
	// changed and returns the Runtime the tick should use. Nil (the
	// default) keeps today's behaviour: rt is used as given.
	refresh func(Runtime) Runtime
}

// NewDaemon returns a Daemon ticking at interval, floored at minInterval.
func NewDaemon(rt Runtime, interval time.Duration) *Daemon {
	if interval < minInterval {
		interval = minInterval
	}
	return &Daemon{rt: rt, interval: interval}
}

// WithRefresh installs a per-tick refresh on the daemon. nil (the default)
// keeps today's behaviour: the Runtime given to NewDaemon is used as-is.
// Returns d for chaining.
func (d *Daemon) WithRefresh(f func(Runtime) Runtime) *Daemon {
	d.refresh = f
	return d
}

// Run ticks until ctx is cancelled. A failing tick is logged and retried on
// the next interval rather than killing the daemon.
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

// Tick reconciles every binding once.
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

	for _, b := range bindings {
		if err := d.tickOne(ctx, b.Name); err != nil {
			slog.Error("reconcile failed", "binding", b.Name, "err", err)
		}
	}

	fresh, err := d.rt.Store.List()
	if err != nil {
		slog.Warn("list bindings after tick", "err", err)
		return nil
	}
	// SPIKE(decision): the "all rounds finished" toast (#182) and the pane
	// sidebar tokens (#129) were herdr notifications and are gone; whether
	// the MCP channel should carry an equivalent event is undecided.

	// Edges evaluateEdges armed (Result "firing", Fired false) fire here,
	// after every binding this tick reconciled has been saved (#37): a
	// fire-mode edge armed by Reconcile above is in fresh already, and one
	// left armed by a daemon that crashed between arming it and running it
	// is picked back up the same way, since armedFires reads every
	// binding's saved state rather than just what changed this tick. Firing
	// happens here, outside every WithLock the per-binding loop took, so
	// Send's own lock on the target never nests inside the source's.
	runFires(ctx, d.rt, armedFires(fresh))

	ingestLiveBindings(ctx, d.rt, fresh)

	return nil
}

// armedFires collects every fire-mode edge left armed across every binding:
// Result == "firing" and Fired == false. evaluateEdges sets exactly that
// pair on a fire-mode edge whose artifact was ready but whose Send has not
// yet run (#37).
func armedFires(bindings []store.Binding) []firePending {
	var pendings []firePending
	for _, b := range bindings {
		for _, e := range b.Edges {
			if !e.Fired && e.Result == "firing" {
				pendings = append(pendings, firePending{Source: b.Name, Edge: e})
			}
		}
	}
	return pendings
}

// tickOne is the per-binding body of Tick's whole-store pass: one critical
// section that reads the binding fresh under the lock, reconciles it, and
// writes it back
// without releasing the lock. Reconcile and everything it calls take the
// *store.Tx rather than locking themselves, so a CLI command running
// concurrently cannot land a write between the read and the save.
func (d *Daemon) tickOne(ctx context.Context, name string) error {
	return d.rt.Store.WithLock(func(tx *store.Tx) error {
		fresh, err := tx.Load(name)
		if errors.Is(err, store.ErrNotFound) {
			// A `relay unbind` landed between the caller's binding list and
			// here. That is normal use, not a failure worth logging.
			return nil
		}
		if err != nil {
			return err
		}

		next, err := Reconcile(ctx, d.rt, tx, fresh)
		if err != nil {
			return err
		}
		if store.SameBinding(next, fresh) {
			return nil
		}

		return tx.Save(next)
	})
}

// ingestLiveBindings runs internal/ingest over every live binding's
// directory at the end of a tick, so the database stays current with what
// files just recorded (docs/specs/2026-09-20-persistence-design.md §5.5).
// d.rt.DB == nil (no database configured) is a no-op. A source or database
// error is logged at Warn and that binding is skipped this tick -- it
// never fails the tick or touches a binding, a round file, or a state.
func ingestLiveBindings(ctx context.Context, rt Runtime, bindings []store.Binding) {
	if rt.DB == nil {
		return
	}
	deps := IngestDeps(rt)
	for _, b := range bindings {
		stats, err := ingest.Ingest(ctx, ingest.DirSource(rt.Store.Dir(b.Name)), rt.DB, deps)
		if err != nil {
			slog.Warn("ingest", "binding", b.Name, "err", err)
			continue
		}
		if stats != (ingest.Stats{}) {
			slog.Info("ingest", "binding", b.Name,
				"rounds", stats.Rounds, "events", stats.Events,
				"artifacts", stats.Artifacts, "transcript", stats.TranscriptRecords)
		}
	}
}
