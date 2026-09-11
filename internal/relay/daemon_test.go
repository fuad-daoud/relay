package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestTickReconcilesAndPersists(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
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
	rt := Runtime{LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}

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
