package relay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/planner"
	"github.com/fuad-daoud/relay/internal/store"
)

// TestDaemonBackfillsPlannerID is the plan's required case for §5.6 (last
// paragraph): a tick back-fills PlannerID from (Planner.Kind,
// Planner.SessionID) when the registry knows that session, and leaves it
// empty when it does not.
// TestTickSurfacesListAgentsFailure guards the resilience contract at the
// boundary where the daemon actually talks to herdr: a hiccup in the one
// ListAgents call a tick makes must be visible to the caller, not silently
// swallowed. listErr's gate fires from the second ListAgents call onward, and
// sentBinding's Bind already made the first, so setting listErr here targets
// exactly the tick's own call.
// TestTickContinuesPastFailingBinding guards the other half of resilience:
// one binding's reconcile error must not abort the rest of the tick. It
// builds a second, independent binding by hand -- seedBound/sentBinding are
// hardwired to the name "webshop" and cwd "/repo" -- because Bind's shared
// fakeHerdr.newPane would otherwise collide the two builder panes.
// TestRunSurvivesFailingTick guards Run's half of resilience: a tick that
// keeps failing must not stop the loop or bubble the tick error out of Run.
// Guarded by a timeout so a regression that makes Run return the tick error
// (or hang) fails the test loudly instead of wedging the suite, the same
// shape as store.TestNestedAccessDoesNotDeadlock.
// TestNewDaemonFloorsInterval guards the floor by inspection made concrete:
// a misconfigured (zero or negative) interval must not spin the herdr socket.
func TestNewDaemonFloorsInterval(t *testing.T) {
	rt := Runtime{LedgerPath: filepath.Join(t.TempDir(), "ledger.json"), AvailabilityPath: filepath.Join(t.TempDir(), "availability.json")}

	if d := NewDaemon(rt, 0); d.interval != minInterval {
		t.Errorf("zero interval -> %s, want floor %s", d.interval, minInterval)
	}
	if d := NewDaemon(rt, -time.Second); d.interval != minInterval {
		t.Errorf("negative interval -> %s, want floor %s", d.interval, minInterval)
	}
	if d := NewDaemon(rt, time.Minute); d.interval != time.Minute {
		t.Errorf("an interval already above the floor must pass through unchanged, got %s", d.interval)
	}
}

// TestTickIgnoresBindingUnboundMidTick covers the window between Tick's
// binding list and its per-binding load: a `relay unbind` landing in it is
// normal use, not a failure, and must not be logged as one.
// TestTickRefreshesRuntimeBeforeReconcile confirms Tick calls d.refresh
// before it reconciles, so the round the reconcile pass sees is whatever the
// refresh just swapped in -- not last tick's copy.
// TestTickWithoutRefreshIsUnchanged confirms a Daemon with no WithRefresh
// call behaves exactly as before #209 -- the same fixture and assertions as
// TestTickReconcilesAndPersists, the test this one relies on to prove the
// nil-refresh path is untouched.
// TestTickSyncsMetadataAndFinishedAfterReconcile checks that Tick calls both
// syncPaneMetadata and notifyFinished after the binding loop (#129, #182).
// The finished-toast decision itself is covered by finished_test.go; here
// only that Tick wires both into the pass, using a binding with a closed
// round, no pending payload, and an idle planner so the toast fires within
// this one tick.
// TestTickIngestsLiveBindings guards the daemon's end-of-tick ingest hook
// (docs/specs/2026-09-20-persistence-design.md §5.5): with a db configured,
// a tick over a live, sent binding must leave a matching binding and round
// row behind.
// TestTickWithoutDBIsUnchanged guards the nil-DB path: every call site
// (here, the ingest hook) must treat Runtime.DB == nil exactly like a
// machine with no database -- no panic, and no relay.db file conjured into
// existence by the mere act of ticking.
// waitListCall drains one signal from a channel fed by fakeHerdr.onList,
// which is the race-safe way these event-loop tests observe a ListAgents
// call: fakeHerdr has no lock of its own, so reading its listCalls counter
// directly from the test goroutine while Run's goroutine (or a reconnect
// goroutine it spawned) may still be writing it would be a data race. The
// channel receive is the synchronisation point instead, and it also gives a
// happens-before edge for anything that goroutine did earlier in the same
// call (e.g. appending to subscribeCalls before making the ListAgents call
// that follows it in d.subscribe).
func waitListCall(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}

// waitForState busy-polls the store (itself lock-synchronised, unlike
// fakeHerdr) until name reaches want or the deadline passes.
func waitForState(t *testing.T, rt Runtime, name string, want store.State) store.Binding {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		b, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		if b.State == want {
			return b
		}
		select {
		case <-deadline:
			t.Fatalf("%s never reached state %s, last seen %s", name, want, b.State)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestDaemonEventWakesOneBinding guards the whole point of #146: a socket
// status event reconciles exactly the binding whose pane it names, at once,
// without the daemon spawning another ListAgents call or touching any other
// binding.
// TestDaemonPaneClosedMarksBuilderMissing guards the gone path: a
// pane_closed event removes the builder from the cache, and the next
// reconcile -- driven by that same event, not a separate poll -- finds it
// missing and stamps BuilderMissingSince exactly like today's ListAgents
// path does.
// TestDaemonDetectedRefreshesSnapshot guards agentCache.Apply's
// pane_agent_detected case wired into Run: a new pane means the snapshot is
// stale everywhere, so the daemon must take a fresh ListAgents call rather
// than trust the cache.
// TestDaemonFallsBackToPollingWithoutSocket guards the default mode every
// other daemon test runs in: fakeHerdr.Subscribe returns herdr.ErrNoSocket
// with nothing scripted, so Run must fall back to calling ListAgents on
// every tick exactly as it did before #146.
// TestDaemonReconnectsAfterStreamClose guards the reconnect loop: a stream
// close drops eventsLive at once (so the very next tick polls instead of
// trusting a cache the daemon no longer believes), and reconnect retries
// with backoff, re-subscribes, and re-snapshots via ListAgents.
// TestDaemonResubscribesWhenAPaneIsBound guards Tick's post-loop check: a
// binding created after Run's bootstrap subscription is not in
// d.subscribedPanes, and the next tick must notice and resubscribe with the
// full, current pane list rather than wait forever for an event on a pane
// the daemon never asked herdr to watch.
// fakeFetcher counts calls so a test can prove the tick asked the endpoint --
// or, on a fresh cache, never asked at all.
type fakeFetcher struct {
	calls int
	tag   string
	err   error
}

func (f *fakeFetcher) Latest(ctx context.Context) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.tag, nil
}

// releaseStateRoot points store.DefaultRoot() at a temp root, so no test in
// this package ever reads or writes the user's real state. The release check
// is the one daemon path that composes its own path from that root (#293).
func releaseStateRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	return root
}

// TestTickRefreshesOncePastTTL counts fetches: none while the cached answer is
// fresh, one once it is stale. Drop the Stale guard and the fresh case fails.
// TestTickSurvivesFetchError pins §4.4's failure rule: a fetch error is
// swallowed, Tick still returns nil, and the cache is not written -- so an
// offline machine retries next tick instead of recording a wrong answer.
// TestBackfillLeavesDoneBindingsAlone pins the DONE guard in
// backfillPlannerID: a finished binding is history, and a tick must not
// rewrite it even when its planner session now has a record.
func TestBackfillLeavesDoneBindingsAlone(t *testing.T) {
	reg := &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}
	if _, err := reg.Create(planner.Record{
		ID:          "pl_aaaaaaaacccc",
		Name:        "architect-1",
		HarnessKind: "claude",
		SessionID:   "sess-done",
		CWD:         "/repo",
	}); err != nil {
		t.Fatalf("create planner record: %v", err)
	}
	b := store.Binding{Name: "old", State: store.StateDone}
	b.Planner.Kind, b.Planner.SessionID = "claude", "sess-done"

	if got := backfillPlannerID(Runtime{Planners: reg}, b); got.PlannerID != "" {
		t.Errorf("DONE binding back-filled with %q; history must not be rewritten", got.PlannerID)
	}
	b.State = store.StateActive
	if got := backfillPlannerID(Runtime{Planners: reg}, b); got.PlannerID != "pl_aaaaaaaacccc" {
		t.Errorf("ACTIVE binding PlannerID = %q, want pl_aaaaaaaacccc", got.PlannerID)
	}
}
