package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/ingest"
	"github.com/fuad-daoud/relay/internal/release"
	"github.com/fuad-daoud/relay/internal/store"
)

// minInterval keeps a misconfigured interval from spinning the herdr socket.
const minInterval = 500 * time.Millisecond

// reconnectResult is how the reconnect goroutine -- the one exception to
// "Run is single-goroutine" (see Run's doc comment) -- hands a freshly
// (re)subscribed stream back to Run's own goroutine. reconnect must not set
// d.subscribedPanes or d.streamCancel directly: both are read and written on
// Run's goroutine without a lock, so only Run itself may assign them, after
// receiving one of these off the channel.
type reconnectResult struct {
	events <-chan herdr.Event
	panes  []string
	cancel context.CancelFunc
}

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

	// cache is the daemon's current view of herdr's agent list while a
	// socket subscription is live (#146). Safe for concurrent access: the
	// socket reader and reconnect update it from their own goroutines while
	// Run and Tick read a Snapshot from theirs.
	cache agentCache
	// eventsLive is true while the daemon trusts d.cache over a fresh
	// ListAgents call. Atomic because reconnect sets it from its own
	// goroutine while Run and Tick read it from theirs.
	eventsLive atomic.Bool
	// subscribedPanes is the pane list the current subscription covers,
	// compared against a fresh boundPanes each tick to notice a pane bound
	// after Run started. Touched only on Run's own goroutine.
	subscribedPanes []string
	// streamCancel cancels the current subscription's child context, which
	// closes its socket read loop. Touched only on Run's own goroutine.
	streamCancel context.CancelFunc
	// pendingEvents is how Tick's resubscribe (called on Run's own
	// goroutine, from inside Tick) hands a freshly opened stream back to
	// Run's select loop: Run reads and clears it right after Tick returns,
	// no channel round-trip needed since both run on the same goroutine.
	pendingEvents <-chan herdr.Event
	// reconnected is how the reconnect goroutine hands a freshly
	// (re)subscribed stream back to Run after a stream drop.
	reconnected chan reconnectResult
	// backoff overrides backoffAfter for tests; nil uses the package
	// function.
	backoff func(attempt int) time.Duration
}

// NewDaemon returns a Daemon ticking at interval, floored at minInterval.
func NewDaemon(rt Runtime, interval time.Duration) *Daemon {
	if interval < minInterval {
		interval = minInterval
	}
	return &Daemon{
		rt:          rt,
		interval:    interval,
		applied:     map[string]appliedMeta{},
		reconnected: make(chan reconnectResult),
	}
}

// WithRefresh installs a per-tick refresh on the daemon. nil (the default)
// keeps today's behaviour: the Runtime given to NewDaemon is used as-is.
// Returns d for chaining.
func (d *Daemon) WithRefresh(f func(Runtime) Runtime) *Daemon {
	d.refresh = f
	return d
}

// WithBackoff overrides the reconnect backoff schedule for tests. nil (the
// default) uses backoffAfter. Returns d for chaining.
func (d *Daemon) WithBackoff(f func(attempt int) time.Duration) *Daemon {
	d.backoff = f
	return d
}

// subscribe opens one socket subscription covering every currently bound
// pane (#146) and bootstraps d.cache from a fresh snapshot, in that order --
// documented by herdr: subscribing first and snapshotting second means no
// event can land in the gap between listing agents and opening the stream.
//
// It only touches d.cache (its own mutex) and d.eventsLive (atomic), so it
// is safe to call from any goroutine. The caller is responsible for
// recording the returned pane list and cancel func on d.subscribedPanes and
// d.streamCancel, which must happen on Run's own goroutine (see Run's doc
// comment).
func (d *Daemon) subscribe(ctx context.Context) (events <-chan herdr.Event, panes []string, cancel context.CancelFunc, err error) {
	bindings, err := d.rt.Store.List()
	if err != nil {
		return nil, nil, nil, err
	}
	panes = boundPanes(bindings)

	subCtx, cancel := context.WithCancel(ctx)
	events, err = d.rt.Herdr.Subscribe(subCtx, panes)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}

	agents, err := d.rt.Herdr.ListAgents(ctx)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	d.cache.Replace(agents)
	d.eventsLive.Store(true)
	return events, panes, cancel, nil
}

// reconnect retries subscribe with backoff until it succeeds or ctx ends,
// then hands the new stream to Run over d.reconnected. It is the one
// exception to "Run is single-goroutine besides the socket reader": while it
// runs, it must touch only d.cache (via subscribe, under cache's own mutex)
// and d.eventsLive (atomic, via subscribe) -- never d.subscribedPanes or
// d.streamCancel, which Run's goroutine owns.
func (d *Daemon) reconnect(ctx context.Context) {
	backoff := d.backoff
	if backoff == nil {
		backoff = backoffAfter
	}
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(attempt)):
		}

		events, panes, cancel, err := d.subscribe(ctx)
		if err != nil {
			slog.Debug("events: reconnect failed", "attempt", attempt, "err", err)
			continue
		}

		slog.Info("events resumed")
		select {
		case d.reconnected <- reconnectResult{events: events, panes: panes, cancel: cancel}:
		case <-ctx.Done():
			cancel()
		}
		return
	}
}

// resubscribe closes the daemon's current stream and opens a fresh one
// covering the currently bound panes, re-snapshotting the cache. Tick calls
// it -- on Run's own goroutine -- when a pane bound after Run started is
// missing from d.subscribedPanes.
func (d *Daemon) resubscribe(ctx context.Context) {
	events, panes, cancel, err := d.subscribe(ctx)
	if err != nil {
		d.eventsLive.Store(false)
		slog.Warn("events: resubscribe failed; polling", "err", err)
		return
	}
	if d.streamCancel != nil {
		d.streamCancel()
	}
	d.subscribedPanes = panes
	d.streamCancel = cancel
	d.pendingEvents = events
}

// bindingForPane returns the name of the binding whose planner or builder
// pane is paneID, or "" when no binding claims it -- a stale pane, or an
// event for a pane that was never bound.
func (d *Daemon) bindingForPane(paneID string) string {
	if paneID == "" {
		return ""
	}
	bindings, err := d.rt.Store.List()
	if err != nil {
		slog.Warn("events: list bindings for pane lookup failed", "err", err)
		return ""
	}
	for _, b := range bindings {
		if b.Planner.PaneID == paneID || b.Builder.PaneID == paneID {
			return b.Name
		}
	}
	return ""
}

// Run ticks until ctx is cancelled. A failing tick is logged and retried on
// the next interval rather than killing the daemon, because a transient
// herdr hiccup must not drop every binding on the floor.
//
// Besides this loop, exactly two other goroutines ever touch Daemon state:
// the socket reader inside herdr.Client.Subscribe (writes to the events
// channel Run reads) and reconnect (see its own doc comment for what it may
// touch). Every other field -- subscribedPanes, streamCancel, pendingEvents
// -- is read and written only here, in Run's loop and in the functions Run
// calls synchronously from it (Tick, tickOne, resubscribe).
func (d *Daemon) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	events, panes, cancel, err := d.subscribe(ctx)
	if err != nil {
		slog.Info(fmt.Sprintf("events: unavailable (%s); polling every %s", err, d.interval))
		events = nil
	} else {
		d.subscribedPanes = panes
		d.streamCancel = cancel
		slog.Info(fmt.Sprintf("events: socket %s", herdr.SocketPath()))
	}

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
			if d.pendingEvents != nil {
				events = d.pendingEvents
				d.pendingEvents = nil
			}
		case res := <-d.reconnected:
			events = res.events
			d.subscribedPanes = res.panes
			d.streamCancel = res.cancel
		case ev, ok := <-events:
			if !ok {
				d.eventsLive.Store(false)
				if d.streamCancel != nil {
					d.streamCancel()
				}
				events = nil
				go d.reconnect(ctx)
				continue
			}
			pane, refresh := d.cache.Apply(ev)
			if refresh {
				if agents, err := d.rt.Herdr.ListAgents(ctx); err == nil {
					d.cache.Replace(agents)
				} else {
					slog.Warn("events: refresh snapshot failed", "err", err)
				}
			}
			if name := d.bindingForPane(pane); name != "" {
				if err := d.tickOne(ctx, name, d.cache.Snapshot()); err != nil {
					slog.Error("reconcile failed", "binding", name, "err", err)
				}
			}
		}
	}
}

// Tick reconciles every binding against a single agent list snapshot: the
// cache while a socket subscription is live, or a fresh ListAgents call
// otherwise -- the same fallback behaviour as before #146.
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

	var agents []herdr.Agent
	if d.eventsLive.Load() {
		agents = d.cache.Snapshot()
	} else {
		agents, err = d.rt.Herdr.ListAgents(ctx)
		if err != nil {
			return fmt.Errorf("list agents: %w", err)
		}
	}
	// Own the snapshot. DeliverPending records a planner it has just prompted
	// by marking it working in this slice, and that record is true only for the
	// remainder of this pass -- it must not reach the Herdr implementation's own
	// storage, nor survive into the next tick, which fetches fresh state anyway.
	agents = append([]herdr.Agent(nil), agents...)

	for _, b := range bindings {
		if err := d.tickOne(ctx, b.Name, agents); err != nil {
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

	// Edges evaluateEdges armed (Result "firing", Fired false) fire here,
	// after every binding this tick reconciled has been saved (#37): a
	// fire-mode edge armed by Reconcile above is in fresh already, and one
	// left armed by a daemon that crashed between arming it and running it
	// is picked back up the same way, since armedFires reads every
	// binding's saved state rather than just what changed this tick. Firing
	// happens here, outside every WithLock the per-binding loop took, so
	// Send's own lock on the target never nests inside the source's.
	runFires(ctx, d.rt, armedFires(fresh))
	// A pane bound after Run started (a fresh `relay bind`/`relay add`) is
	// not in the subscription Run opened at bootstrap: pick it up on the
	// next tick rather than waiting for an event that will never arrive for
	// a pane the daemon never subscribed to.
	if d.eventsLive.Load() && !equalPanes(boundPanes(fresh), d.subscribedPanes) {
		d.resubscribe(ctx)
	}

	ingestLiveBindings(ctx, d.rt, fresh)

	d.refreshRelease(ctx)

	return nil
}

// refreshRelease refreshes the day-cached answer to "is a newer relay out?"
// (#293). It runs after everything else Tick does and never before it: no
// reconcile decision may wait on a release check.
//
// The common path is one small file read -- a fresh cache ends it there, so a
// 2s tick stays cheap. A stale cache costs one bracketed HTTP GET, and every
// failure of that GET is swallowed and logged at debug: an offline machine
// saves nothing, writes no wrong answer, and simply retries next tick.
func (d *Daemon) refreshRelease(ctx context.Context) {
	if d.rt.Fetcher == nil {
		return
	}

	root, err := store.DefaultRoot()
	if err != nil {
		slog.Debug("release check: no state root", "err", err)
		return
	}

	now := time.Now
	if d.rt.Now != nil {
		now = d.rt.Now
	}

	cached, ok, err := release.Load(root)
	if err != nil {
		slog.Debug("release check: read cache", "err", err)
		return
	}
	if !release.Stale(cached, ok, now(), release.TTL) {
		return
	}

	tag, err := d.rt.Fetcher.Latest(ctx)
	if err != nil {
		// Save nothing: a wrong or empty answer in the cache would read as
		// truth for a whole day.
		slog.Debug("release check: fetch", "err", err)
		return
	}

	if err := release.Save(root, release.Cache{
		Latest:    tag,
		CheckedAt: now(),
		Source:    release.Source(),
	}); err != nil {
		slog.Debug("release check: save cache", "err", err)
	}
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

// tickOne is the per-binding body Tick's whole-store pass and Run's
// per-event wake both use: one critical section that reads the binding
// fresh under the lock, reconciles it against agents, and writes it back
// without releasing the lock. Reconcile and everything it calls take the
// *store.Tx rather than locking themselves, so a CLI command running
// concurrently cannot land a write between the read and the save.
func (d *Daemon) tickOne(ctx context.Context, name string, agents []herdr.Agent) error {
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

		next, err := Reconcile(ctx, d.rt, tx, fresh, agents)
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
