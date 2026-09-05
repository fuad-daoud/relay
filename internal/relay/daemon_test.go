package relay

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestTickReconcilesAndPersists(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("upjo", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, err := rt.Store.Load("upjo")
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
// hardwired to the name "upjo" and cwd "/repo" -- because Bind's shared
// fakeHerdr.newPane would otherwise collide the two builder panes.
func TestTickContinuesPastFailingBinding(t *testing.T) {
	f := &fakeHerdr{readErr: errors.New("read pane failed")}
	rt, _ := sentBinding(t, f) // "upjo": builder goes Blocked below, and its
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
		plannerWith(herdr.StatusWorking, false), // "upjo" planner
		builderAgent(herdr.StatusBlocked),       // "upjo" builder: errors on read
		{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w9:p1",
			Session: herdr.Session{Value: "planner2-sess"}}, // "kobe" planner
		{Kind: "agy", Status: herdr.StatusIdle, PaneID: "w9:p2"}, // "kobe" builder
	}

	// Tick itself must not return an error: a per-binding failure is logged,
	// not propagated, precisely so the rest of the loop keeps running.
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick must not fail the whole loop over one binding, got %v", err)
	}

	// The assertion that matters: if Tick had returned early on "upjo"'s
	// error, "kobe" would still be sitting at round 1 with nothing queued.
	got, err := rt.Store.Load("kobe")
	if err != nil {
		t.Fatalf("Load kobe: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("kobe round = %d, want 2 -- the failing upjo binding must not block it", got.Round)
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
	rt := Runtime{}

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
