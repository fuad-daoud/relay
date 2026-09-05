package relay

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestPullReturnsAndConfirmsHeldPayload(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.prompts = nil

	payload, found, err := Pull(context.Background(), rt, b.Name)
	if err != nil || !found {
		t.Fatalf("Pull: found=%v err=%v", found, err)
	}
	if !strings.Contains(payload, "001-report.md") {
		t.Errorf("payload = %q", payload)
	}
	if len(f.prompts) != 0 {
		t.Error("pull must never inject; it returns the payload for stdout")
	}

	if _, pending, _ := rt.Store.PendingForPlanner(b.Name); pending {
		t.Error("pull must confirm the entry, or the daemon will deliver it twice")
	}
}

func TestPullWithNothingPending(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)

	if _, found, err := Pull(context.Background(), rt, b.Name); err != nil || found {
		t.Fatalf("found=%v err=%v, want false/nil", found, err)
	}
}

// TestReconcileClearsHeldAfterPull covers the state a pull leaves behind: it
// claims the payload without delivering it, so nothing else clears Held and
// `relay status` kept showing HELD for a binding with nothing pending.
func TestReconcileClearsHeldAfterPull(t *testing.T) {
	f := &fakeHerdr{}
	rt, seeded := queuedBinding(t, f)

	// Reload: Bind returns its pre-save copy, which carries no round cap yet.
	b, err := rt.Store.Load(seeded.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.State = store.StateHeld

	if _, found, err := Pull(context.Background(), rt, b.Name); err != nil || !found {
		t.Fatalf("Pull: found=%v err=%v", found, err)
	}

	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)}
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active once the pull left nothing pending", got.State)
	}
}
