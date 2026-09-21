package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/policy"
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
func TestTickContinuesPastFailingBinding(t *testing.T) {
	f := &fakeHerdr{readErr: errors.New("read pane failed")}
	rt, _ := sentBinding(t, f) // "webshop": builder goes Blocked below, and its
	// dialog capture uses ReadAgent, which readErr makes fail.

	second := store.Binding{
		Name:    "kobe",
		CWD:     "/repo2",
		Planner: store.Endpoint{PaneID: "w9:p1", SessionID: "planner2-sess"},
		Builder: store.Endpoint{PaneID: "w9:p2"},
		Round:   1,
		State:   store.StateActive,
	}
	if err := rt.Store.Save(second); err != nil {
		t.Fatalf("save second binding: %v", err)
	}
	sentEntry := store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan,
		Path: rt.Store.PlanPath("kobe", 1), Confirmed: true,
	}
	if err := rt.Store.AppendLog("kobe", sentEntry); err != nil {
		t.Fatalf("seed sent entry: %v", err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("kobe", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("kobe", 1))

	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false), // "webshop" planner
		builderAgent(herdr.StatusBlocked),       // "webshop" builder: errors on read
		{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w9:p1",
			Session: herdr.Session{Value: "planner2-sess"}}, // "kobe" planner
		{Kind: "agy", Status: herdr.StatusIdle, PaneID: "w9:p2"}, // "kobe" builder
	}

	// Tick itself must not return an error: a per-binding failure is logged,
	// not propagated, precisely so the rest of the loop keeps running.
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick must not fail the whole loop over one binding, got %v", err)
	}

	// The assertion that matters: if Tick had returned early on "webshop"'s
	// error, "kobe" would still be sitting at round 1 with nothing queued.
	got, err := rt.Store.Load("kobe")
	if err != nil {
		t.Fatalf("Load kobe: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("kobe round = %d, want 2 -- the failing webshop binding must not block it", got.Round)
	}
	if len(f.prompts) != 1 {
		t.Errorf("kobe's report must still be delivered, got %+v", f.prompts)
	}
}

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
	rt, first, _ := twoBindingsOnePlanner(t, f)

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
	if _, pending, err := rt.Store.PendingForPlanner(first.Name); err != nil || !pending {
		t.Errorf("the payload that lost the race must still be pending: pending=%v err=%v", pending, err)
	}
}

func TestTickIgnoresASubAgentsIdle(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)

	a := builderAgent(herdr.StatusWorking)
	a.Session = herdr.Session{Value: "parent"}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), a}

	// Tick once so builder's session ("parent") is recorded.
	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}

	clock.Advance(startGrace + time.Second)

	// Replace the builder agent with one carrying Session.Value: "child" and StatusIdle.
	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		{
			Name:    "webshop-builder",
			Kind:    "agy",
			Status:  herdr.StatusIdle,
			CWD:     "/repo",
			PaneID:  "w2:p4",
			Session: herdr.Session{Value: "child"},
		},
	}
	f.prompts = nil

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	if len(f.prompts) != 0 {
		t.Errorf("no prompt should be sent to builder under sub-agent idle, got %+v", f.prompts)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Kind == store.KindReport {
			t.Errorf("no report entry should be queued, found %+v", e)
		}
	}

	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.State != store.StateActive {
		t.Errorf("state = %s, want active", loaded.State)
	}
	if loaded.Builder.SessionID != "parent" {
		t.Errorf("Builder.SessionID = %q, want parent", loaded.Builder.SessionID)
	}
}

func TestTickStillCapturesASubAgentsBlock(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)

	a := builderAgent(herdr.StatusWorking)
	a.Session = herdr.Session{Value: "parent"}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), a}

	// Tick once so builder's session ("parent") is recorded.
	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}

	clock.Advance(startGrace + time.Second)

	f.readOut = "Allow edit to src/main.go?  1. Yes  2. No"
	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		{
			Name:    "webshop-builder",
			Kind:    "agy",
			Status:  herdr.StatusBlocked,
			CWD:     "/repo",
			PaneID:  "w2:p4",
			Session: herdr.Session{Value: "child"},
		},
	}

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("question must be queued for planner: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindQuestion {
		t.Errorf("pending kind = %v, want question", pending.Kind)
	}
}

func TestTickLocatesABuilderByNameAfterAPaneMove(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)

	a := builderAgent(herdr.StatusWorking)
	a.Session = herdr.Session{Value: "parent"}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), a}

	// Tick once so builder's session ("parent") is recorded.
	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}

	// Builder agent with fixture name, different PaneID, and Session.Value: "child".
	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		{
			Name:    "webshop-builder",
			Kind:    "agy",
			Status:  herdr.StatusWorking,
			CWD:     "/repo",
			PaneID:  "w9:p8",
			Session: herdr.Session{Value: "child"},
		},
	}

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.State != store.StateActive {
		t.Errorf("state = %s, want active", loaded.State)
	}
	if loaded.Builder.PaneID != "w9:p8" {
		t.Errorf("Builder.PaneID = %q, want w9:p8", loaded.Builder.PaneID)
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
func TestTickSyncsMetadataAndFinishedAfterReconcile(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	b := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:          store.Endpoint{PaneID: "w2:p4"},
		BuilderCandidate: "agy",
		Round:            2,
		RoundCap:         10,
		State:            store.StateActive,
		FinishPending:    true,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save webshop: %v", err)
	}

	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if len(f.metadata) != 1 {
		t.Fatalf("got %d metadata calls, want 1", len(f.metadata))
	}
	if f.metadata[0].Pane != "w2:p4" {
		t.Errorf("metadata pane = %q, want w2:p4", f.metadata[0].Pane)
	}
	wantTokens := map[string]string{TokenName: "webshop", TokenRound: "002", TokenState: "active"}
	if !reflect.DeepEqual(f.metadata[0].Meta.Tokens, wantTokens) {
		t.Errorf("metadata tokens = %+v, want %+v", f.metadata[0].Meta.Tokens, wantTokens)
	}
	if _, ok := d.applied["webshop"]; !ok {
		t.Errorf("d.applied = %+v, want an entry for webshop", d.applied)
	}

	if len(f.notices) != 1 {
		t.Fatalf("got %d notices, want 1 (all rounds finished)", len(f.notices))
	}
	if f.sounds[0] != herdr.SoundDone {
		t.Errorf("notice sound = %q, want %q", f.sounds[0], herdr.SoundDone)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.FinishPending {
		t.Error("FinishPending = true after the toast, want false")
	}
}

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
func TestDaemonEventWakesOneBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f) // "webshop": planner w2:p3, builder w2:p4

	second := store.Binding{
		Name:    "kobe",
		CWD:     "/repo2",
		Planner: store.Endpoint{PaneID: "w9:p1", SessionID: "planner2-sess"},
		Builder: store.Endpoint{PaneID: "w9:p2"},
		Round:   1,
		State:   store.StateActive,
	}
	if err := rt.Store.Save(second); err != nil {
		t.Fatalf("save second binding: %v", err)
	}

	events := make(chan herdr.Event)
	f.subscribeCh = events
	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		builderAgent(herdr.StatusWorking),
		{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w9:p1", Session: herdr.Session{Value: "planner2-sess"}},
		{Kind: "agy", Status: herdr.StatusIdle, PaneID: "w9:p2"},
	}
	listCalls := make(chan struct{}, 64)
	f.onList = func() { listCalls <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewDaemon(rt, time.Hour) // long enough that the ticker never fires
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	waitListCall(t, listCalls, "bootstrap ListAgents never happened") // listCalls == 1

	events <- herdr.Event{Kind: "pane_agent_status_changed", PaneID: "w2:p4", AgentStatus: herdr.StatusBlocked}

	waitForState(t, rt, "webshop", store.StateNeedsYou)

	kobe, err := rt.Store.Load("kobe")
	if err != nil {
		t.Fatalf("Load kobe: %v", err)
	}
	if kobe.Round != 1 || kobe.State != store.StateActive {
		t.Errorf("kobe must be untouched by webshop's event, got round=%d state=%s", kobe.Round, kobe.State)
	}

	// A known-pane status change never needs a refresh (agentCache.Apply
	// returns refresh=false for it), so no second ListAgents call should
	// ever arrive. Mutation check: an Apply that always reports
	// refresh=true would make this fail.
	select {
	case <-listCalls:
		t.Error("a known-pane status event must not trigger another ListAgents call")
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	<-done
}

// TestDaemonPaneClosedMarksBuilderMissing guards the gone path: a
// pane_closed event removes the builder from the cache, and the next
// reconcile -- driven by that same event, not a separate poll -- finds it
// missing and stamps BuilderMissingSince exactly like today's ListAgents
// path does.
func TestDaemonPaneClosedMarksBuilderMissing(t *testing.T) {
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

	waitListCall(t, listCalls, "bootstrap ListAgents never happened")

	events <- herdr.Event{Kind: "pane_closed", PaneID: "w2:p4"}

	deadline := time.After(2 * time.Second)
	for {
		b, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !b.BuilderMissingSince.IsZero() {
			break
		}
		select {
		case <-deadline:
			t.Fatal("BuilderMissingSince was never set after a pane_closed event")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done
}

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
		Builder: store.Endpoint{PaneID: "w9:p2"},
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
