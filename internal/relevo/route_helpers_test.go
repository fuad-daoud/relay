package relevo

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeClaimStore is the map-backed ClaimStore the route tests use: a claim
// present for a planner id means live, absent means not.
type fakeClaimStore map[string]*delivery.Claim

func (f fakeClaimStore) Live(planner string, now time.Time) (*delivery.Claim, error) {
	return f[planner], nil
}

func (f fakeClaimStore) Write(c delivery.Claim, now time.Time) error {
	f[c.Planner] = &c
	return nil
}

func (f fakeClaimStore) Remove(planner string, pid int) error {
	delete(f, planner)
	return nil
}

// routeRuntime is a minimal Runtime for the delivery-route tests: a temp
// store, a fixed clock and nothing else wired.
func routeRuntime(t *testing.T) Runtime {
	t.Helper()
	return Runtime{
		Store: store.New(t.TempDir()),
		Now:   func() time.Time { return baseTime },
	}
}

// seedPending saves an active binding and one unconfirmed planner-bound
// entry, which is exactly what delivery.DeliverPending and Pull work on.
func seedPending(t *testing.T, rt Runtime, name, plannerID, kind string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:      name,
		CWD:       "/repo/" + name,
		Round:     1,
		State:     store.StateActive,
		Planner:   store.Endpoint{Kind: kind, SessionID: "sess"},
		PlannerID: plannerID,
		Builder:   store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return delivery.Queue(context.Background(), deliveryDeps(rt), tx, name, store.LogEntry{
			Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
			Payload: "round 1 report", Path: "/tmp/report.md",
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return b
}

// testClaimPlanner is a valid planner id (pl_ plus 12 characters of
// [a-z2-7]), the shape the claim store keys on.
const testClaimPlanner = "pl_aaaaaaaabbbb"

// otherClaimPlanner is a second valid id, for the "a different planner's
// claim is not this one's" cases.
const otherClaimPlanner = "pl_ccccccccdddd"

// alwaysAlive reports every pid as live, so a claim's fake pid does not
// depend on which pids exist on the test machine.
func alwaysAlive(int) bool { return true }
