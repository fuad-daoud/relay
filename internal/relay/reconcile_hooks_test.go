package relay

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/store"
)

type recordDispatcher struct {
	mu     sync.Mutex
	events []hooks.Event
}

func (r *recordDispatcher) Dispatch(ctx context.Context, event hooks.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordDispatcher) getEvents() []hooks.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]hooks.Event, len(r.events))
	copy(cp, r.events)
	return cp
}

func TestReconcile_EmitsStateChangedOnBroken(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		// Empty agents slice causes FindAgent to fail -> b.State becomes StateBroken
		out, err = Reconcile(context.Background(), rt, tx, b, nil)
		return err
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if out.State != store.StateBroken {
		t.Fatalf("expected state Broken, got %s", out.State)
	}

	events := disp.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
	}
	if events[0].Type != hooks.EventStateChanged {
		t.Errorf("expected EventStateChanged, got %s", events[0].Type)
	}
	if events[0].State != string(store.StateBroken) {
		t.Errorf("expected State %s, got %s", store.StateBroken, events[0].State)
	}
	if events[0].OldState != string(store.StateActive) {
		t.Errorf("expected OldState %s, got %s", store.StateActive, events[0].OldState)
	}
	if events[0].BindingID != b.Name {
		t.Errorf("expected BindingID %s, got %s", b.Name, events[0].BindingID)
	}
}

func TestReconcile_EmitsRoundStartedOnReport(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	// Write report file so handleIdleBuilder completes the round
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if err := os.WriteFile(reportPath, []byte("round 1 report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	agents := []herdr.Agent{
		plannerAgent(),
		builderAgent(herdr.StatusIdle),
	}

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = Reconcile(context.Background(), rt, tx, b, agents)
		return err
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if out.Round != 2 {
		t.Fatalf("expected round 2, got %d", out.Round)
	}

	events := disp.getEvents()
	var hasRoundStarted bool
	for _, e := range events {
		if e.Type == hooks.EventRoundStarted {
			hasRoundStarted = true
			if e.Round != 2 {
				t.Errorf("expected Round 2, got %d", e.Round)
			}
			if e.BindingID != b.Name {
				t.Errorf("expected BindingID %s, got %s", b.Name, e.BindingID)
			}
		}
	}
	if !hasRoundStarted {
		t.Errorf("expected EventRoundStarted in events: %+v", events)
	}
}

func TestReconcile_NoEventsWhenUnchanged(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	agents := []herdr.Agent{
		plannerAgent(),
		builderAgent(herdr.StatusWorking),
	}

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, err := Reconcile(context.Background(), rt, tx, b, agents)
		return err
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	events := disp.getEvents()
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d: %+v", len(events), events)
	}
}

func TestDone_EmitsStateChanged(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	if err := Done(context.Background(), rt, b.Name); err != nil {
		t.Fatalf("Done: %v", err)
	}

	events := disp.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
	}
	if events[0].Type != hooks.EventStateChanged {
		t.Errorf("expected EventStateChanged, got %s", events[0].Type)
	}
	if events[0].State != string(store.StateDone) {
		t.Errorf("expected State DONE, got %s", events[0].State)
	}
	if events[0].OldState != string(store.StateActive) {
		t.Errorf("expected OldState ACTIVE, got %s", events[0].OldState)
	}
}
