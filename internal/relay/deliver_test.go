package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func queuedBinding(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedBound(t, f)

	entry := store.LogEntry{
		Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Payload: "Builder finished round 1. Report: /x/001-report.md",
	}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		return Queue(context.Background(), rt, tx, b.Name, entry)
	})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	return rt, b
}

// deliverPending wraps DeliverPending with the state lock a real caller
// (the daemon or `relay pull`) would already be holding, since DeliverPending
// itself takes the tx rather than locking.
func deliverPending(t *testing.T, rt Runtime, b store.Binding, agents []herdr.Agent) (store.Binding, Delivery, error) {
	t.Helper()
	var out Delivery
	var next store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, out, err = DeliverPending(context.Background(), rt, tx, b, agents)
		return err
	})
	return next, out, err
}

func plannerWith(status string, focused bool) herdr.Agent {
	a := plannerAgent()
	a.Status = status
	a.Focused = focused
	return a
}

// claudeIdleScreen is a focused idle claude planner: the input box is a bare
// `❯` line, so relay can prove there is nothing to clobber.
const claudeIdleScreen = "transcript\n────\n❯\n────\n  ? for shortcuts\n"

// claudeDraftScreen is a focused claude planner with a half-typed draft in its
// input box, so the input-empty detector cannot clear the payload.
const claudeDraftScreen = "transcript\n────\n❯ half a thought\n────\n  ? for shortcuts\n"

// heldClock swaps rt's fixed clock for a closure over a `now` variable the
// test advances between ticks, the way real time passes between daemon ticks.
// It returns that variable and the updated runtime.
func heldClock(rt Runtime) (*time.Time, Runtime) {
	now := baseTime
	rt.Now = func() time.Time { return now }
	return &now, rt
}

func TestDeliverInjectsWhenIdleAndUnfocused(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
	f.prompts = nil

	_, got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if !got.Delivered {
		t.Fatalf("want delivered, got %+v", got)
	}
	if len(f.prompts) != 1 || f.prompts[0].Target != "w2:p3" {
		t.Fatalf("prompts = %+v", f.prompts)
	}

	if _, pending, err := rt.Store.PendingForPlanner(b.Name); err != nil || pending {
		t.Errorf("delivery must confirm the log entry: pending=%v err=%v", pending, err)
	}
}

func TestDeliverHoldsWhilePlannerPaneFocused(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeDraftScreen
	f.prompts = nil

	_, got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if !got.Held || got.Delivered {
		t.Fatalf("want held, got %+v", got)
	}
	if len(f.prompts) != 0 {
		t.Error("nothing may be typed into a focused planner pane")
	}
	if len(f.notices) != 1 {
		t.Errorf("a held delivery must raise exactly one herdr notification, got %d", len(f.notices))
	}

	if _, pending, _ := rt.Store.PendingForPlanner(b.Name); !pending {
		t.Error("a held payload stays pending")
	}
}

func TestDeliverNotifiesOnlyOnceWhileHeld(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeDraftScreen
	f.prompts = nil

	_, got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if !got.Held {
		t.Fatalf("want held on first tick, got %+v", got)
	}
	if len(f.notices) != 1 {
		t.Fatalf("first tick must notify once, got %d", len(f.notices))
	}

	// The daemon persists the held state between ticks (Task 11); simulate the
	// second tick of the same hold by passing a binding already marked held.
	b.State = store.StateHeld

	_, got, err = deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if !got.Held || got.Delivered {
		t.Fatalf("want still held, got %+v", got)
	}
	if len(f.prompts) != 0 {
		t.Error("nothing may be typed into a focused planner pane")
	}
	if len(f.notices) != 1 {
		t.Errorf("a repeat tick of the same hold must not notify again, got %d notices", len(f.notices))
	}

	if _, pending, _ := rt.Store.PendingForPlanner(b.Name); !pending {
		t.Error("a held payload stays pending")
	}
}

func TestDeliverInjectsWhenPlannerDone(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusDone, false)}
	f.prompts = nil

	_, got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if !got.Delivered {
		t.Fatalf("want delivered, got %+v", got)
	}
	if len(f.prompts) != 1 || f.prompts[0].Target != "w2:p3" {
		t.Fatalf("prompts = %+v", f.prompts)
	}

	if _, pending, err := rt.Store.PendingForPlanner(b.Name); err != nil || pending {
		t.Errorf("delivery must confirm the log entry: pending=%v err=%v", pending, err)
	}
}

func TestDeliverWaitsWhilePlannerWorking(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false)}
	f.prompts = nil

	_, got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if got.Delivered || got.Held {
		t.Fatalf("want a plain wait, got %+v", got)
	}
	if len(f.notices) != 0 {
		t.Error("a busy planner is not worth a notification")
	}
}

func TestDeliverReportsPlannerGone(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = nil

	_, got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if !got.PlannerGone {
		t.Fatalf("want PlannerGone, got %+v", got)
	}
}

func TestDeliverWithNothingPendingIsNoop(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
	f.prompts = nil

	_, got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if got.Delivered || got.Held || got.PlannerGone {
		t.Fatalf("want a no-op, got %+v", got)
	}
}

// twoBindingsOnePlanner seeds two active bindings that share one planner pane,
// each with a report already queued for that planner. This is the shape a
// planner running peer builders has, and the shape the fan-in clobber needs.
func twoBindingsOnePlanner(t *testing.T, f *fakeHerdr) (Runtime, store.Binding, store.Binding) {
	t.Helper()
	rt, first := queuedBinding(t, f)

	second := store.Binding{
		Name:    "storefront",
		CWD:     "/repo2",
		Planner: first.Planner,
		Builder: store.Endpoint{PaneID: "w2:p5"},
		Round:   1,
		State:   store.StateActive,
	}
	if err := rt.Store.Save(second); err != nil {
		t.Fatalf("save second binding: %v", err)
	}

	entry := store.LogEntry{
		Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Payload: "Builder finished round 1. Report: /x2/001-report.md",
	}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		return Queue(context.Background(), rt, tx, second.Name, entry)
	})
	if err != nil {
		t.Fatalf("Queue second: %v", err)
	}

	stored, err := rt.Store.Load(second.Name)
	if err != nil {
		t.Fatalf("Load second: %v", err)
	}
	return rt, first, stored
}

func TestDeliverMarksPlannerBusyForTheRestOfTheTick(t *testing.T) {
	f := &fakeHerdr{}
	rt, first, second := twoBindingsOnePlanner(t, f)

	// One snapshot, shared by both bindings, exactly as Tick shares it.
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
	f.prompts = nil

	_, got, err := deliverPending(t, rt, first, agents)
	if err != nil {
		t.Fatalf("DeliverPending first: %v", err)
	}
	if !got.Delivered {
		t.Fatalf("the first payload should land, got %+v", got)
	}

	_, got, err = deliverPending(t, rt, second, agents)
	if err != nil {
		t.Fatalf("DeliverPending second: %v", err)
	}
	if got.Delivered {
		t.Error("a planner already prompted in this tick must not be typed into again")
	}
	if len(f.prompts) != 1 {
		t.Fatalf("one planner pane takes at most one injection per tick, got %+v", f.prompts)
	}
	if _, pending, err := rt.Store.PendingForPlanner(second.Name); err != nil || !pending {
		t.Errorf("the undelivered payload must stay pending: pending=%v err=%v", pending, err)
	}
}

// heldTick is one daemon tick over a held binding: it passes next in, and the
// caller persists the returned binding between ticks exactly as the daemon
// does. Every multi-tick hold test funnels through here.
func heldTick(t *testing.T, rt Runtime, b store.Binding, f *fakeHerdr) (store.Binding, Delivery) {
	t.Helper()
	next, got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	return next, got
}

func TestHeldDeliversWhenPlannerInputEmpty(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeIdleScreen
	f.prompts = nil

	_, got := heldTick(t, rt, b, f)
	if !got.Delivered || got.Held {
		t.Fatalf("want delivered, got %+v", got)
	}
	if got.Reason != "planner focused, input empty" {
		t.Errorf("Reason = %q, want %q", got.Reason, "planner focused, input empty")
	}
	if len(f.prompts) != 1 {
		t.Fatalf("prompts = %+v, want one", f.prompts)
	}
	last := f.reads[len(f.reads)-1]
	if last.Source != "visible" || last.Lines != heldScreenLines {
		t.Errorf("last read = %+v, want source %q with %d lines", last, "visible", heldScreenLines)
	}
}

func TestHeldFingerprintsADraft(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeDraftScreen
	f.prompts = nil
	now, rt := heldClock(rt)

	next, got := heldTick(t, rt, b, f)
	if !got.Held || got.Delivered {
		t.Fatalf("want held, got %+v", got)
	}
	if next.PlannerScreen != fingerprint(claudeDraftScreen) {
		t.Errorf("PlannerScreen = %q, want the fingerprint of the draft screen", next.PlannerScreen)
	}
	if !next.PlannerScreenAt.Equal(*now) {
		t.Errorf("PlannerScreenAt = %v, want %v", next.PlannerScreenAt, *now)
	}
	if len(f.prompts) != 0 {
		t.Errorf("prompts = %+v, want none", f.prompts)
	}
	if len(f.notices) != 1 {
		t.Errorf("notices = %d, want one", len(f.notices))
	}
}

func TestHeldDeliversAfterQuietGrace(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeDraftScreen
	f.prompts = nil
	now, rt := heldClock(rt)

	next, got := heldTick(t, rt, b, f)
	if !got.Held {
		t.Fatalf("first tick: want held, got %+v", got)
	}
	next.State = store.StateHeld

	*now = now.Add(DefaultHeldGrace)
	f.prompts = nil
	next, got = heldTick(t, rt, next, f)
	if !got.Delivered || got.Held {
		t.Fatalf("second tick: want delivered, got %+v", got)
	}
	if got.Reason != "planner focused, quiet for 1m0s" {
		t.Errorf("Reason = %q, want %q", got.Reason, "planner focused, quiet for 1m0s")
	}
	if next.PlannerScreen != "" || !next.PlannerScreenAt.IsZero() {
		t.Errorf("a delivery must clear the fingerprint state, got %+v", next)
	}
	if len(f.prompts) != 1 {
		t.Errorf("prompts = %+v, want one", f.prompts)
	}
}

func TestHeldResetsWhenScreenMoves(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeDraftScreen
	f.prompts = nil
	now, rt := heldClock(rt)

	next, got := heldTick(t, rt, b, f)
	if !got.Held {
		t.Fatalf("first tick: want held, got %+v", got)
	}
	next.State = store.StateHeld

	*now = now.Add(30 * time.Second)
	moved := "transcript\n────\n❯ half a thought more\n────\n  ? for shortcuts\n"
	f.readOut = moved
	next, got = heldTick(t, rt, next, f)
	if !got.Held {
		t.Fatalf("tick with a moved screen: want held, got %+v", got)
	}
	if !next.PlannerScreenAt.Equal(*now) {
		t.Errorf("PlannerScreenAt = %v, want the later instant %v", next.PlannerScreenAt, *now)
	}
	if next.PlannerScreen != fingerprint(moved) {
		t.Errorf("PlannerScreen = %q, want the fingerprint of the new screen", next.PlannerScreen)
	}
	next.State = store.StateHeld

	*now = now.Add(30 * time.Second)
	next, got = heldTick(t, rt, next, f)
	if !got.Held || got.Delivered {
		t.Fatalf("only 30s have passed since the screen changed; want held, got %+v", got)
	}
}

func TestHeldUnknownKindUsesGraceOnly(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	planner := plannerWith(herdr.StatusIdle, true)
	planner.Kind = "agy" // its idle screen has no captured marker yet
	f.agents = []herdr.Agent{planner}
	f.readOut = claudeIdleScreen // a bare `❯`, which a known kind would deliver on
	f.prompts = nil
	now, rt := heldClock(rt)

	next, got := heldTick(t, rt, b, f)
	if !got.Held {
		t.Fatalf("first tick: want held, got %+v", got)
	}
	next.State = store.StateHeld

	*now = now.Add(DefaultHeldGrace)
	f.prompts = nil
	next, got = heldTick(t, rt, next, f)
	if !got.Delivered {
		t.Fatalf("after the grace an unknown kind must deliver, got %+v", got)
	}
	if got.Reason != "planner focused, quiet for 1m0s" {
		t.Errorf("Reason = %q, want the quiet reason", got.Reason)
	}
	if len(f.prompts) != 1 {
		t.Errorf("prompts = %+v, want one", f.prompts)
	}
}

func TestHeldReadFailureHoldsWithoutEvidence(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.prompts = nil
	b.PlannerScreen = "keep"
	b.PlannerScreenAt = baseTime
	f.readErr = errors.New("boom")

	next, got := heldTick(t, rt, b, f)
	if got.Delivered {
		t.Fatal("a failed read is not evidence; it must not inject")
	}
	if !got.Held {
		t.Fatalf("want held, got %+v", got)
	}
	if got.Reason != "planner pane is focused; screen unreadable" {
		t.Errorf("Reason = %q, want %q", got.Reason, "planner pane is focused; screen unreadable")
	}
	if next.PlannerScreen != "keep" || !next.PlannerScreenAt.Equal(baseTime) {
		t.Errorf("a failed read must not touch the fingerprint, got %+v", next)
	}
}

func TestHeldNotifiesOnceAcrossThreeTicks(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeDraftScreen
	f.prompts = nil
	now, rt := heldClock(rt)

	next := b
	var got Delivery
	for tick := 0; tick < 3; tick++ {
		if tick > 0 {
			next.State = store.StateHeld
			*now = now.Add(10 * time.Second)
		}
		next, got = heldTick(t, rt, next, f)
		if !got.Held {
			t.Fatalf("tick %d: want held, got %+v", tick, got)
		}
	}

	if len(f.notices) != 1 {
		t.Errorf("notices = %d, want exactly one across three held ticks", len(f.notices))
	}
}

func TestEmptyClearsPlannerScreen(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f) // nothing queued
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	b.PlannerScreen = "stale"
	b.PlannerScreenAt = baseTime

	next, got := heldTick(t, rt, b, f)
	if !got.Empty {
		t.Fatalf("want empty, got %+v", got)
	}
	if next.PlannerScreen != "" || !next.PlannerScreenAt.IsZero() {
		t.Errorf("an empty delivery must clear the fingerprint state, got %+v", next)
	}
}

func TestUnfocusedDeliveryClearsPlannerScreen(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
	f.prompts = nil
	b.PlannerScreen = "stale"
	b.PlannerScreenAt = baseTime

	next, got := heldTick(t, rt, b, f)
	if !got.Delivered {
		t.Fatalf("want delivered, got %+v", got)
	}
	if next.PlannerScreen != "" || !next.PlannerScreenAt.IsZero() {
		t.Errorf("an unfocused delivery must clear the fingerprint state, got %+v", next)
	}
	if len(f.reads) != 0 {
		t.Errorf("the unfocused path never reads the screen, reads = %+v", f.reads)
	}
}

func TestHeldGraceZeroMeansDefault(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeDraftScreen
	f.prompts = nil
	rt.HeldGrace = 0
	now, rt := heldClock(rt)

	next, got := heldTick(t, rt, b, f)
	if !got.Held {
		t.Fatalf("first tick: want held, got %+v", got)
	}
	next.State = store.StateHeld

	*now = now.Add(59 * time.Second)
	next, got = heldTick(t, rt, next, f)
	if !got.Held || got.Delivered {
		t.Fatalf("59s is under the 60s default; want held, got %+v", got)
	}
	next.State = store.StateHeld

	*now = now.Add(1 * time.Second)
	f.prompts = nil
	_, got = heldTick(t, rt, next, f)
	if !got.Delivered {
		t.Fatalf("60s meets the 60s default; want delivered, got %+v", got)
	}

	// A non-zero grace must be read, not just defaulted: 5s delivers after 5s.
	f2 := &fakeHerdr{}
	rt2, b2 := queuedBinding(t, f2)
	f2.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f2.readOut = claudeDraftScreen
	f2.prompts = nil
	rt2.HeldGrace = 5 * time.Second
	now2, rt2 := heldClock(rt2)

	next2, got2 := heldTick(t, rt2, b2, f2)
	if !got2.Held {
		t.Fatalf("second run, first tick: want held, got %+v", got2)
	}
	next2.State = store.StateHeld

	*now2 = now2.Add(5 * time.Second)
	_, got2 = heldTick(t, rt2, next2, f2)
	if !got2.Delivered {
		t.Fatalf("second run after 5s of quiet with a 5s grace: want delivered, got %+v", got2)
	}
}
