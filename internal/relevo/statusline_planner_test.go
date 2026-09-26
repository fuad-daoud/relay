package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func setupPlannerStatusStore(t *testing.T) Runtime {
	t.Helper()
	rt := newRuntime(t)
	bindings := []store.Binding{
		{
			Name:             "zeta",
			CWD:              "/a",
			PlannerID:        testClaimPlanner,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "alpha",
			CWD:              "/b",
			PlannerID:        testClaimPlanner,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "other",
			CWD:              "/c",
			PlannerID:        otherClaimPlanner,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "finished",
			CWD:              "/d",
			PlannerID:        testClaimPlanner,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateDone,
		},
	}
	for _, b := range bindings {
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", b.Name, err)
		}
	}
	return rt
}

func TestPlannerStatusFiltersToOnePlanner(t *testing.T) {
	rt := setupPlannerStatusStore(t)
	ctx := context.Background()

	rep, err := PlannerStatus(ctx, rt, testClaimPlanner)
	if err != nil {
		t.Fatalf("PlannerStatus: %v", err)
	}
	if len(rep.Bindings) != 2 {
		t.Fatalf("got %d bindings, want 2", len(rep.Bindings))
	}
	if rep.Bindings[0].Name != "alpha" || rep.Bindings[1].Name != "zeta" {
		t.Errorf("got bindings [%s, %s], want [alpha, zeta]", rep.Bindings[0].Name, rep.Bindings[1].Name)
	}
	for i, b := range rep.Bindings {
		if b.PlannerID != testClaimPlanner {
			t.Errorf("row %d PlannerID = %q, want %q", i, b.PlannerID, testClaimPlanner)
		}
	}

	repOther, err := PlannerStatus(ctx, rt, otherClaimPlanner)
	if err != nil {
		t.Fatalf("PlannerStatus(other): %v", err)
	}
	if len(repOther.Bindings) != 1 || repOther.Bindings[0].Name != "other" {
		t.Errorf("got %d bindings for the other planner, want only 'other'", len(repOther.Bindings))
	}
}

func TestPlannerStatusEmptyPlannerIsEmpty(t *testing.T) {
	rt := setupPlannerStatusStore(t)
	ctx := context.Background()

	rep, err := PlannerStatus(ctx, rt, "")
	if err != nil {
		t.Fatalf("PlannerStatus: %v", err)
	}
	if len(rep.Bindings) != 0 {
		t.Errorf("got %d bindings, want 0", len(rep.Bindings))
	}
}
