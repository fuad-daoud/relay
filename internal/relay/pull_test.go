package relay

import (
	"context"
	"strings"
	"testing"
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
