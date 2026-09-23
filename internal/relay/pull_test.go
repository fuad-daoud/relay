package relay

import (
	"context"
	"testing"
)

// TestPullReturnsAndMarksDelivered is the surviving half of the old pull
// test: Pull hands back the oldest pending payload and confirms it.
func TestPullReturnsAndMarksDelivered(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	payload, found, err := Pull(context.Background(), rt, "webshop")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !found || payload == "" {
		t.Fatalf("Pull found=%v payload=%q, want the queued report", found, payload)
	}
	if _, still, err := rt.Store.PendingForPlanner("webshop"); err != nil || still {
		t.Errorf("Pull must confirm what it returns (still pending=%v err=%v)", still, err)
	}
}

// TestPullWithNothingPending: nothing queued reads as nothing to print.
func TestPullWithNothingPending(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")
	if _, _, err := Pull(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("first Pull: %v", err)
	}

	payload, found, err := Pull(context.Background(), rt, "webshop")
	if err != nil {
		t.Fatalf("second Pull: %v", err)
	}
	if found || payload != "" {
		t.Errorf("second Pull = (%q, %v), want nothing pending", payload, found)
	}
}

// TestPullMarksDeliveredRoutePull is the plan's required case for §5.4:
// `relay pull` marks the entry delivered with route=pull, which is what the
// background wait's pull half does for a tools-mode planner.
func TestPullMarksDeliveredRoutePull(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	if _, _, err := Pull(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed {
		t.Error("the entry must be confirmed")
	}
	if last.Route != "pull" {
		t.Errorf("Route = %q, want pull", last.Route)
	}
}
