package relay

import (
	"context"
	"testing"

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
func deliverPending(t *testing.T, rt Runtime, b store.Binding, agents []herdr.Agent) (Delivery, error) {
	t.Helper()
	var out Delivery
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = DeliverPending(context.Background(), rt, tx, b, agents)
		return err
	})
	return out, err
}

func plannerWith(status string, focused bool) herdr.Agent {
	a := plannerAgent()
	a.Status = status
	a.Focused = focused
	return a
}

func TestDeliverInjectsWhenIdleAndUnfocused(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
	f.prompts = nil

	got, err := deliverPending(t, rt, b, f.agents)
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
	f.prompts = nil

	got, err := deliverPending(t, rt, b, f.agents)
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
	f.prompts = nil

	got, err := deliverPending(t, rt, b, f.agents)
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

	got, err = deliverPending(t, rt, b, f.agents)
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

	got, err := deliverPending(t, rt, b, f.agents)
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

	got, err := deliverPending(t, rt, b, f.agents)
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

	got, err := deliverPending(t, rt, b, f.agents)
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

	got, err := deliverPending(t, rt, b, f.agents)
	if err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	if got.Delivered || got.Held || got.PlannerGone {
		t.Fatalf("want a no-op, got %+v", got)
	}
}
