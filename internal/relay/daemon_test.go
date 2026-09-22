package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/planner"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/release"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestTickReconcilesAndPersists(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Errorf("tick must persist the advanced round, got %d", b.Round)
	}
	if len(f.prompts) != 1 {
		t.Errorf("an idle unfocused planner must receive the report, got %+v", f.prompts)
	}
}

func TestTickIsOneAgentListCallForAllBindings(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}
	f.listCalls = 0 // ignore the calls Bind made while setting up

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if f.listCalls != 1 {
		t.Errorf("agent list called %d times, want 1 per tick", f.listCalls)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := NewDaemon(rt, 10*time.Millisecond).Run(ctx); err != nil {
		t.Fatalf("Run must exit cleanly on cancel, got %v", err)
	}
}

func TestTickSkipsDoneBindings(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateDone
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(f.prompts) != 0 {
		t.Error("a done binding must be left entirely alone")
	}
}

// TestDaemonBackfillsPlannerID is the plan's required case for §5.6 (last
// paragraph): a tick back-fills PlannerID from (Planner.Kind,
// Planner.SessionID) when the registry knows that session, and leaves it
// empty when it does not.
func TestDaemonBackfillsPlannerID(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	if b.PlannerID != "" {
		t.Fatalf("precondition: the seeded binding already has PlannerID %q", b.PlannerID)
	}
	if b.Planner.SessionID == "" {
		t.Fatal("precondition: the seeded binding has no Planner.SessionID")
	}

	reg := &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}
	rec, err := reg.Create(planner.Record{
		ID:          "pl_aaaaaaaabbbb",
		Name:        "architect-1",
		HarnessKind: b.Planner.Kind,
		SessionID:   b.Planner.SessionID,
		CWD:         "/repo",
	})
	if err != nil {
		t.Fatalf("create planner record: %v", err)
	}
	rt.Planners = reg

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.PlannerID != rec.ID {
		t.Errorf("PlannerID = %q, want the record's %q", got.PlannerID, rec.ID)
	}

	// A miss does nothing: no record for this session, no PlannerID.
	f2 := &fakeHerdr{}
	rt2, b2 := seedBound(t, f2)
	rt2.Planners = &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}
	if err := NewDaemon(rt2, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick (miss): %v", err)
	}
	got2, err := rt2.Store.Load(b2.Name)
	if err != nil {
		t.Fatalf("Load (miss): %v", err)
	}
	if got2.PlannerID != "" {
		t.Errorf("a miss must leave PlannerID empty, got %q", got2.PlannerID)
	}
}

// TestTickSurfacesListAgentsFailure guards the resilience contract at the
// boundary where the daemon actually talks to herdr: a hiccup in the one
// ListAgents call a tick makes must be visible to the caller, not silently
// swallowed. listErr's gate fires from the second ListAgents call onward, and
// sentBinding's Bind already made the first, so setting listErr here targets
// exactly the tick's own call.
func TestTickSurfacesListAgentsFailure(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.listErr = errors.New("herdr socket unavailable")

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err == nil {
		t.Fatal("Tick must surface a ListAgents failure")
	}
	if len(f.prompts) != 0 {
		t.Errorf("a failed agent list must not deliver anything, got %+v", f.prompts)
	}
}

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
func TestRunSurvivesFailingTick(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.listErr = errors.New("herdr socket unavailable") // every tick's own
	// ListAgents call fails from here on.

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewDaemon(rt, 10*time.Millisecond).Run(ctx)
	}()

	time.Sleep(1100 * time.Millisecond) // a few floored (500ms) tick intervals
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run must survive repeated tick failures and exit clean on cancel, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel (timeout) -- a failing tick must not wedge it")
	}
}

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
func TestTickIgnoresBindingUnboundMidTick(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}
	f.onList = func() {
		if err := rt.Store.Delete("webshop"); err != nil {
			t.Errorf("unbind mid-tick: %v", err)
		}
	}

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(previous)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if strings.Contains(logged.String(), "reconcile failed") {
		t.Errorf("an unbind mid-tick must not be logged as a failure: %s", logged.String())
	}
}

func TestTickDoesNotMutateTheHerdrAgentList(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}
	f.prompts = nil

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("expected the payload to be delivered, prompts = %+v", f.prompts)
	}

	// DeliverPending writes the planner's new status into the tick's snapshot.
	// That record belongs to the tick and must not reach the Herdr client's own
	// list, where it would outlive the pass that made it true.
	if f.agents[0].Status != herdr.StatusIdle {
		t.Errorf("Tick must own its snapshot; herdr's agent list now reads %q", f.agents[0].Status)
	}
}

func TestTickDoesNotRestampAnUnchangedBinding(t *testing.T) {
	// The next == fresh short-circuit this replaces was never tested. save()
	// stamps UpdatedAt unconditionally, so without the short-circuit every tick
	// rewrites every bind.json and UpdatedAt stops meaning "last change".
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)

	before, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	after, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("UpdatedAt moved %v -> %v on a tick that changed nothing",
			before.UpdatedAt, after.UpdatedAt)
	}
}

func TestTickInjectsOncePerPlannerPane(t *testing.T) {
	f := &fakeHerdr{}
	rt, first, second := twoBindingsOnePlanner(t, f)

	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusIdle, false),
		builderAgent(herdr.StatusIdle),
		{Kind: "agy", Status: herdr.StatusIdle, CWD: "/repo2", PaneID: "w2:p5", Title: "storefront-builder"},
	}
	f.prompts = nil

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	toPlanner := 0
	for _, p := range f.prompts {
		if p.Target == "w2:p3" {
			toPlanner++
		}
	}
	if toPlanner != 1 {
		t.Fatalf("one planner pane takes at most one injection per tick, got %d: %+v", toPlanner, f.prompts)
	}
	_, p1, err1 := rt.Store.PendingForPlanner(first.Name)
	_, p2, err2 := rt.Store.PendingForPlanner(second.Name)
	if err1 != nil || err2 != nil {
		t.Errorf("PendingForPlanner: %v %v", err1, err2)
	}
	if !p1 && !p2 {
		t.Error("the payload that lost the race must still be pending")
	}
}

// TestTickRefreshesRuntimeBeforeReconcile confirms Tick calls d.refresh
// before it reconciles, so the round the reconcile pass sees is whatever the
// refresh just swapped in -- not last tick's copy.
func TestTickRefreshesRuntimeBeforeReconcile(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	const marker = "refreshed-marker"
	refreshCalls := 0
	d := NewDaemon(rt, time.Second).WithRefresh(func(in Runtime) Runtime {
		refreshCalls++
		in.Policy = policy.Policy{Order: map[string][]string{"builder": {marker}}}
		return in
	})

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1", refreshCalls)
	}
	if got := d.rt.Policy.Order["builder"]; len(got) != 1 || got[0] != marker {
		t.Errorf("d.rt.Policy not swapped by refresh, got %v", got)
	}
}

// TestTickWithoutRefreshIsUnchanged confirms a Daemon with no WithRefresh
// call behaves exactly as before #209 -- the same fixture and assertions as
// TestTickReconcilesAndPersists, the test this one relies on to prove the
// nil-refresh path is untouched.
func TestTickWithoutRefreshIsUnchanged(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Errorf("tick must persist the advanced round, got %d", b.Round)
	}
	if len(f.prompts) != 1 {
		t.Errorf("an idle unfocused planner must receive the report, got %+v", f.prompts)
	}
}

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
func TestTickIngestsLiveBindings(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)

	d, err := db.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	rt.DB = d

	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, found, err := d.Binding("webshop")
	if err != nil {
		t.Fatalf("Binding: %v", err)
	}
	if !found {
		t.Fatal("Binding(webshop) not found after Tick")
	}
	rounds, err := d.Rounds(b.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if len(rounds) != 1 {
		t.Errorf("len(rounds) = %d, want 1", len(rounds))
	}
}

// TestTickWithoutDBIsUnchanged guards the nil-DB path: every call site
// (here, the ingest hook) must treat Runtime.DB == nil exactly like a
// machine with no database -- no panic, and no relay.db file conjured into
// existence by the mere act of ticking.
func TestTickWithoutDBIsUnchanged(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if _, err := os.Stat(rt.Store.DBPath()); !os.IsNotExist(err) {
		t.Errorf("relay.db stat = %v, want os.ErrNotExist (DB == nil must write nothing)", err)
	}
}

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
func TestDaemonDetectedRefreshesSnapshot(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)

	events := make(chan herdr.Event)
	f.subscribeCh = events
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}
	listCalls := make(chan struct{}, 64)
	f.onList = func() { listCalls <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewDaemon(rt, time.Hour)
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	waitListCall(t, listCalls, "bootstrap ListAgents never happened") // listCalls == 1

	events <- herdr.Event{Kind: "pane_agent_detected"}

	waitListCall(t, listCalls, "pane_agent_detected must trigger a refresh ListAgents call") // listCalls == 2

	cancel()
	<-done
}

// TestDaemonFallsBackToPollingWithoutSocket guards the default mode every
// other daemon test runs in: fakeHerdr.Subscribe returns herdr.ErrNoSocket
// with nothing scripted, so Run must fall back to calling ListAgents on
// every tick exactly as it did before #146.
func TestDaemonFallsBackToPollingWithoutSocket(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}
	listCalls := make(chan struct{}, 64)
	f.onList = func() { listCalls <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	d := NewDaemon(rt, 20*time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// subscribe's own Subscribe call fails immediately (ErrNoSocket), so
	// bootstrap makes no ListAgents call of its own; every one of these
	// three signals must come from a tick's poll fallback.
	for i := 1; i <= 3; i++ {
		waitListCall(t, listCalls, fmt.Sprintf("tick %d: ListAgents never called while polling", i))
	}

	cancel()
	<-done
}

// TestDaemonReconnectsAfterStreamClose guards the reconnect loop: a stream
// close drops eventsLive at once (so the very next tick polls instead of
// trusting a cache the daemon no longer believes), and reconnect retries
// with backoff, re-subscribes, and re-snapshots via ListAgents.
func TestDaemonReconnectsAfterStreamClose(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	firstEvents := make(chan herdr.Event)
	secondEvents := make(chan herdr.Event)
	// subscribeSignal fires once per Subscribe call, independently of
	// ListAgents: waiting on it (rather than inferring a Subscribe call
	// happened from a ListAgents signal) is what makes "reconnect
	// resubscribed" observable without racing the poll-fallback ticks that
	// fire in the same window.
	subscribeSignal := make(chan struct{}, 64)
	attempt := 0
	f.onSubscribe = func() (<-chan herdr.Event, error) {
		attempt++
		subscribeSignal <- struct{}{}
		if attempt == 1 {
			return firstEvents, nil
		}
		return secondEvents, nil
	}
	// Buffered generously: while eventsLive is false and reconnect has not
	// yet succeeded, every tick in the backoff window polls, and a blocked
	// send here would stall Run's own goroutine (Tick runs on it) and hang
	// the test's cancel/done at the end.
	listCalls := make(chan struct{}, 64)
	f.onList = func() { listCalls <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// NewDaemon floors any interval below minInterval (500ms), so the
	// regular ticker fires roughly every 500ms regardless of what is passed
	// here. The backoff must be comfortably longer than that floor, or
	// reconnect could succeed before a single regular tick has had a chance
	// to poll -- which is exactly what a too-short backoff in an earlier
	// draft of this test did.
	d := NewDaemon(rt, minInterval).WithBackoff(func(int) time.Duration { return 900 * time.Millisecond })
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	select {
	case <-subscribeSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap Subscribe never happened")
	}
	waitListCall(t, listCalls, "bootstrap ListAgents never happened") // subscribeCalls == 1

	close(firstEvents)

	// The stream closing drops eventsLive at once, so a tick in the backoff
	// window polls instead of trusting a cache the daemon no longer
	// believes.
	waitListCall(t, listCalls, "a tick after the stream closed must poll")

	select {
	case <-subscribeSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect never resubscribed")
	}
	waitListCall(t, listCalls, "reconnect never re-snapshotted") // subscribeCalls == 2

	// Safe to read here: the subscribeSignal receive above happened-after
	// the Subscribe call that appended this entry, in the same goroutine.
	if len(f.subscribeCalls) != 2 {
		t.Errorf("subscribeCalls = %d, want 2 (bootstrap + reconnect)", len(f.subscribeCalls))
	}

	cancel()
	<-done
}

// TestDaemonResubscribesWhenAPaneIsBound guards Tick's post-loop check: a
// binding created after Run's bootstrap subscription is not in
// d.subscribedPanes, and the next tick must notice and resubscribe with the
// full, current pane list rather than wait forever for an event on a pane
// the daemon never asked herdr to watch.
func TestDaemonResubscribesWhenAPaneIsBound(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	events := make(chan herdr.Event)
	f.subscribeCh = events
	listCalls := make(chan struct{}, 64)
	f.onList = func() { listCalls <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewDaemon(rt, 20*time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	waitListCall(t, listCalls, "bootstrap ListAgents never happened") // subscribeCalls == 1

	// Bind a second binding directly into the store, the way `relay bind`
	// would while the daemon is already running.
	second := store.Binding{
		Name:    "kobe",
		CWD:     "/repo2",
		Planner: store.Endpoint{PaneID: "w9:p1", SessionID: "planner2-sess"},
		Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		Round:   1,
		State:   store.StateActive,
	}
	if err := rt.Store.Save(second); err != nil {
		t.Fatalf("save second binding: %v", err)
	}

	waitListCall(t, listCalls, "resubscribe never re-snapshotted") // subscribeCalls == 2

	if len(f.subscribeCalls) < 2 {
		t.Fatalf("subscribeCalls = %d, want at least 2 (bootstrap + resubscribe)", len(f.subscribeCalls))
	}
	last := f.subscribeCalls[len(f.subscribeCalls)-1]
	found := false
	for _, p := range last {
		if p == "w9:p1" || p == "w9:p2" {
			found = true
		}
	}
	if !found {
		t.Errorf("last subscribeCalls = %v, want it to include kobe's new panes", last)
	}

	cancel()
	<-done
}

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
func TestTickRefreshesOncePastTTL(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		seed      bool
		checkedAt time.Time
		wantCalls int
	}{
		{
			name:      "fresh cache is left alone",
			seed:      true,
			checkedAt: now.Add(-(release.TTL - time.Second)),
			wantCalls: 0,
		},
		{
			name:      "stale cache refetches",
			seed:      true,
			checkedAt: now.Add(-(release.TTL + time.Second)),
			wantCalls: 1,
		},
		{
			name:      "no cache at all fetches",
			wantCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := releaseStateRoot(t)
			if tc.seed {
				if err := release.Save(root, release.Cache{
					Latest:    "v0.7.0",
					CheckedAt: tc.checkedAt,
					Source:    "test",
				}); err != nil {
					t.Fatalf("seed cache: %v", err)
				}
			}

			ff := &fakeFetcher{tag: "v0.8.0"}
			f := &fakeHerdr{}
			rt, _ := sentBinding(t, f)
			rt.Fetcher = ff
			rt.Now = func() time.Time { return now }
			f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

			if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}

			if ff.calls != tc.wantCalls {
				t.Errorf("fetch calls = %d, want %d", ff.calls, tc.wantCalls)
			}

			c, ok, err := release.Load(root)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := "v0.8.0"
			if tc.wantCalls == 0 {
				want = "v0.7.0" // the fresh answer stays exactly as it was
			}
			if !ok || c.Latest != want {
				t.Errorf("cache = (%+v, ok %v), want latest %s", c, ok, want)
			}
			if tc.wantCalls == 0 && !c.CheckedAt.Equal(tc.checkedAt) {
				t.Errorf("fresh cache checked_at = %s, want it untouched at %s", c.CheckedAt, tc.checkedAt)
			}
		})
	}
}

// TestTickSurvivesFetchError pins §4.4's failure rule: a fetch error is
// swallowed, Tick still returns nil, and the cache is not written -- so an
// offline machine retries next tick instead of recording a wrong answer.
func TestTickSurvivesFetchError(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	t.Run("no cache yet", func(t *testing.T) {
		root := releaseStateRoot(t)

		ff := &fakeFetcher{err: errors.New("dial tcp: network is unreachable")}
		f := &fakeHerdr{}
		rt, _ := sentBinding(t, f)
		rt.Fetcher = ff
		rt.Now = func() time.Time { return now }
		f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

		if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
			t.Errorf("Tick = %v, want nil: a failed release check must not stop the daemon", err)
		}
		if ff.calls != 1 {
			t.Errorf("fetch calls = %d, want 1 (the stale check tried)", ff.calls)
		}
		if _, err := os.Stat(filepath.Join(root, "release-check.json")); !os.IsNotExist(err) {
			t.Errorf("cache stat = %v, want os.ErrNotExist: a failed fetch saves nothing", err)
		}
	})

	t.Run("stale cache is left alone", func(t *testing.T) {
		root := releaseStateRoot(t)
		stale := release.Cache{Latest: "v0.7.0", CheckedAt: now.Add(-2 * release.TTL), Source: "test"}
		if err := release.Save(root, stale); err != nil {
			t.Fatalf("seed cache: %v", err)
		}

		ff := &fakeFetcher{err: errors.New("504 gateway timeout")}
		f := &fakeHerdr{}
		rt, _ := sentBinding(t, f)
		rt.Fetcher = ff
		rt.Now = func() time.Time { return now }
		f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

		if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
			t.Errorf("Tick = %v, want nil", err)
		}

		c, ok, err := release.Load(root)
		if err != nil || !ok {
			t.Fatalf("Load = (%+v, ok %v, %v), want the seeded cache", c, ok, err)
		}
		if c.Latest != stale.Latest || !c.CheckedAt.Equal(stale.CheckedAt) {
			t.Errorf("cache = %+v, want the stale answer untouched at %+v", c, stale)
		}
	})
}
