package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// PlannerStatus filters stored bindings to one planner id and builds rows
// through buildReport from the store alone.
func PlannerStatus(ctx context.Context, rt Runtime, plannerID string) (view.Report, error) {
	if plannerID == "" {
		return view.Report{}, nil
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}
	var kept []store.Binding
	for _, b := range bindings {
		if b.PlannerID == plannerID && b.State != store.StateDone {
			kept = append(kept, b)
		}
	}
	return buildReport(ctx, rt, kept)
}
