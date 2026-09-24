package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/store"
)

// minInterval keeps a misconfigured interval from spinning the tick.
const minInterval = 500 * time.Millisecond

// releaseRetryAfter is how long a failed release fetch is left alone before the
// daemon tries again (#371 §4.10): an endpoint that is down must be asked once
// an hour, not once a tick.
const releaseRetryAfter = time.Hour

// ErrReexec reports that Run stopped because a new relevo binary is ready and
// the caller should exec into it (#371). It is not a failure: the process
// keeps its pid and its children through the exec.
var ErrReexec = errors.New("relevo daemon: re-exec onto a new binary")

// Daemon ticks on its interval and advances every binding. It is the only
// reason relevo needs a background process: the inbound leg happens after the
// planner's turn has ended, when no model is running to notice.
type Daemon struct {
	rt       Runtime
	interval time.Duration

	// refresh re-reads candidates.json and policy.json when they have
	// changed and returns the Runtime the tick should use. Nil (the
	// default) keeps today's behaviour: rt is used as given.
	refresh func(Runtime) Runtime

	// upgrade, when set, runs after every completed tick and only while ctx
	// is live. True means a new binary passed its preflight, and Run returns
	// ErrReexec so the caller can exec into it (#371 §4.5). Nil (the default)
	// keeps today's behaviour: the daemon never re-execs.
	upgrade func(ctx context.Context) bool

	// releaseRetryAt is when a release fetch that failed may be tried again.
	// The zero time means "no failure to back off from" (#371 §4.10).
	releaseRetryAt time.Time
}

// NewDaemon returns a Daemon ticking at interval, floored at minInterval.
func NewDaemon(rt Runtime, interval time.Duration) *Daemon {
	if interval < minInterval {
		interval = minInterval
	}
	return &Daemon{
		rt:       rt,
		interval: interval,
	}
}

// WithRefresh installs a per-tick refresh on the daemon. nil (the default)
// keeps today's behaviour: the Runtime given to NewDaemon is used as-is.
// Returns d for chaining.
func (d *Daemon) WithRefresh(f func(Runtime) Runtime) *Daemon {
	d.refresh = f
	return d
}

// WithUpgrade installs a post-tick hook that decides whether to re-exec onto a
// new binary. nil (the default) keeps today's behaviour: no re-exec.
// Returns d for chaining.
func (d *Daemon) WithUpgrade(f func(ctx context.Context) bool) *Daemon {
	d.upgrade = f
	return d
}

// Run ticks until ctx is cancelled. A failing tick is logged and retried on
// the next interval rather than killing the daemon, because a transient
// failure must not drop every binding on the floor.
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
			// The tick runs under WithoutCancel, so a cancel mid-tick lets
			// that tick finish rather than tearing a reconcile in half
			// (#371 §4.5). Run then returns nil: it never starts another
			// tick after cancellation, and never lets the upgrade hook
			// re-exec from under a shutting-down daemon.
			tctx := context.WithoutCancel(ctx)
			if err := d.Tick(tctx); err != nil {
				slog.Error("relevo tick failed", "err", err)
			}
			if ctx.Err() != nil {
				return nil
			}
			if d.upgrade != nil && d.upgrade(ctx) {
				return ErrReexec
			}
		}
	}
}

// Tick reconciles every binding. It is the whole tick: the daemon has no other
// input than its interval (#303 §5.5).
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
		slog.Warn("list bindings for metadata sync", "err", err)
		return nil
	}

	// Edges evaluateEdges armed (Result "firing", Fired false) fire here,
	// after every binding this tick reconciled has been saved (#37): a
	// fire-mode edge armed by Reconcile above is in fresh already, and one
	// left armed by a daemon that crashed between arming it and running it
	// is picked back up the same way, since armedFires reads every
	// binding's saved state rather than just what changed this tick. Firing
	// happens here, outside every WithLock the per-binding loop took, so
	// Send's own lock on the target never nests inside the source's.
	// Each of Tick's non-binding phases runs through safely, so a panic in
	// one cannot take the whole daemon down (#370, spec §4.6): it is logged
	// with a stack and the next tick tries again.
	d.safely("fires", func() { runFires(ctx, d.rt, armedFires(fresh)) })

	d.safely("ingest", func() { ingestLiveBindings(ctx, d.rt, fresh) })

	d.safely("refresh", func() { d.refreshRelease(ctx) })

	return nil
}

// refreshRelease refreshes the day-cached answer to "is a newer relevo out?"
// (#293). It runs after everything else Tick does and never before it: no
// reconcile decision may wait on a release check.
//
// The common path is one small file read -- a fresh cache ends it there, so a
// 2s tick stays cheap. A stale cache costs one bracketed HTTP GET, and every
// failure of that GET is swallowed and logged at debug: an offline machine
// saves nothing, writes no wrong answer, and waits out a one-hour backoff
// before trying again (#371 §4.10).
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

	// A failed fetch backs off for an hour (#371 §4.10), so a down endpoint
	// is asked once an hour instead of on every tick.
	if now().Before(d.releaseRetryAt) {
		return
	}

	// The cache lives in the machine database's kv row "release-check"
	// (P3b plan §4.5); opening it here is what creates relevo.db on a
	// machine whose daemon has nothing else to store.
	mdb, err := store.New(root).DB()
	if err != nil {
		slog.Debug("release check: no database", "err", err)
		return
	}

	cached, ok, err := release.Load(mdb, filepath.Join(root, "release-check.json"))
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
		// truth for a whole day. The next attempt waits out the backoff.
		d.releaseRetryAt = now().Add(releaseRetryAfter)
		slog.Debug("release check: fetch", "err", err)
		return
	}

	if err := release.Save(mdb, release.Cache{
		Latest:    tag,
		CheckedAt: now(),
		Source:    release.Source(),
	}); err != nil {
		slog.Debug("release check: save cache", "err", err)
		return
	}
	d.releaseRetryAt = time.Time{}
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

// tickOne is the per-binding body Tick's whole-store pass uses: one critical
// section that reads the binding fresh under the lock, reconciles it, and
// writes it back without releasing the lock. Reconcile and everything it calls
// take the *store.Tx rather than locking themselves, so a CLI command running
// concurrently cannot land a write between the read and the save.
func (d *Daemon) tickOne(ctx context.Context, name string) (err error) {
	// A panic anywhere under this binding is contained here (#370, spec
	// §4.6): it is logged with a stack and returned as an error, so Tick logs
	// "reconcile failed" and goes on to the next binding. WithLock releases
	// both of its locks through defers, so unwinding through it is safe.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("reconcile panicked", "binding", name, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("reconcile %s panicked: %v", name, r)
		}
	}()

	return d.rt.Store.WithLock(func(tx *store.Tx) error {
		loaded, err := tx.Load(name)
		if errors.Is(err, store.ErrNotFound) {
			// A `relevo unbind` landed between the caller's binding list and
			// here. That is normal use, not a failure worth logging.
			return nil
		}
		if err != nil {
			return err
		}

		// A binding written by a newer relevo is left to that relevo: this
		// binary's Save would erase every field it does not know (#372).
		// Format 1 is stored as 0, so any loaded Format above BindingFormat
		// is a newer file. The binding is neither reconciled nor saved, and
		// the file keeps every field it had.
		if loaded.Format > store.BindingFormat {
			warnOnce(name, "newer-format",
				fmt.Sprintf("binding %s is format %d; this relevo knows %d; leaving it to a newer relevo",
					name, loaded.Format, store.BindingFormat),
				"binding", name, "format", loaded.Format, "known", store.BindingFormat)
			return nil
		}

		// P3c §4.3: before Reconcile, seal every closed round whose files
		// nothing can still read. Errors are logged per binding and never
		// fail the tick.
		sealRounds(d.rt.Store, tx, loaded)

		fresh := backfillPlannerID(d.rt, loaded)

		next, err := Reconcile(ctx, d.rt, tx, fresh)
		if err != nil {
			return err
		}
		// Compared against what was on disk, not against fresh: a back-fill
		// is itself a change worth saving.
		if store.SameBinding(next, loaded) {
			return nil
		}

		return tx.Save(next)
	})
}

// sealRounds seals every sealable closed round of one binding (P3c §4.3):
// the round's NNN-* files become round_file rows and then leave the binding
// directory. It never fails the tick -- each error is logged and the next
// tick retries, which is also what makes a failed removal harmless.
func sealRounds(st *store.Store, tx *store.Tx, b store.Binding) {
	rounds, err := st.RoundsOnDisk(b.Name)
	if err != nil {
		slog.Warn("seal: list rounds", "binding", b.Name, "err", err)
		return
	}
	for _, r := range rounds {
		drained := st.StreamDrained(b, r)
		if !store.Sealable(b, r, drained) {
			continue
		}
		n, err := tx.SealRound(b.Name, r)
		if err != nil {
			slog.Warn("seal: round", "binding", b.Name, "round", r, "err", err)
			continue
		}
		if n > 0 {
			slog.Info("seal", "binding", b.Name, "round", r, "files", n)
		}
	}
}

// safely runs one of Tick's non-binding phases, recovering a panic so that a
// single bad phase cannot take the whole daemon down (#370, spec §4.6). It logs
// at Error with phase, the panic value and the stack, and never re-panics: the
// phase simply does not happen this tick, and the next tick tries again.
func (d *Daemon) safely(phase string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tick phase panicked", "phase", phase, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	f()
}

// backfillPlannerID is §5.6's upgrade path (#303 §5.6, last paragraph): a
// binding written before Binding.PlannerID existed has no id, but its
// Planner.SessionID still names the harness session the planner registered
// with. When the registry knows that (kind, session), the record's id is set
// on the binding, under the lock tickOne already holds, so the channel lookup,
// the forget guard and the status row all key on the planner. A miss, a DONE
// binding, an empty session and a Runtime with no registry all leave the
// binding exactly as it was.
func backfillPlannerID(rt Runtime, b store.Binding) store.Binding {
	if b.PlannerID != "" || b.State == store.StateDone || b.Planner.SessionID == "" || rt.Planners == nil {
		return b
	}
	rec, err := rt.Planners.BySession(b.Planner.Kind, b.Planner.SessionID)
	if err != nil {
		return b
	}
	b.PlannerID = rec.ID
	slog.Debug("planner backfilled", "binding", b.Name, "planner", rec.ID)
	return b
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
		stats, err := ingest.Ingest(ctx, ingest.StoreSource(rt.Store, b.Name), rt.DB, deps)
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
