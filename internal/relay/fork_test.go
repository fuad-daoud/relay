package relay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/store"
)

type recordHookDispatcher struct {
	events []hooks.Event
}

func (r *recordHookDispatcher) Dispatch(ctx context.Context, event hooks.Event) {
	r.events = append(r.events, event)
}

func seedFourRoundBinding(t *testing.T, rt Runtime, name, cwd string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:             name,
		CWD:              cwd,
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: name + "-builder", PaneID: "w2:p4", Kind: "opencode"},
		BuilderCandidate: testOpencodeRef,
		Round:            4,
		State:            store.StateActive,
		RoundCap:         20,
		RoundTimeoutMS:   1800000,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	srcDir := rt.Store.Dir(name)
	for r := 1; r <= 4; r++ {
		planEntry := store.LogEntry{
			Round:     r,
			Direction: store.DirToBuilder,
			Kind:      store.KindPlan,
			Path:      rt.Store.PlanPath(name, r),
			Confirmed: true,
		}
		if err := rt.Store.AppendLog(name, planEntry); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.Store.PlanPath(name, r), []byte(fmt.Sprintf("plan %d", r)), 0o644); err != nil {
			t.Fatal(err)
		}

		if r < 4 {
			repEntry := store.LogEntry{
				Round:     r,
				Direction: store.DirToPlanner,
				Kind:      store.KindReport,
				Path:      rt.Store.ReportPath(name, r),
				Confirmed: true,
			}
			if err := rt.Store.AppendLog(name, repEntry); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rt.Store.ReportPath(name, r), []byte(fmt.Sprintf("report %d", r)), 0o644); err != nil {
				t.Fatal(err)
			}
			diffPath := filepath.Join(srcDir, fmt.Sprintf("%03d-diff.patch", r))
			if err := os.WriteFile(diffPath, []byte(fmt.Sprintf("diff %d", r)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	loaded, err := rt.Store.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

// TestForkInheritsFeatureAndRecordsParent pins #172's inheritance rule: a
// fork with no --feature inherits the source's, and copies the source's
// RepoRef rather than re-capturing it.
//
// NOTE (round 3, #172): the plan for this test also asked for a check of a
// structured `ForkedFrom{Name: src, Round: 2}` (store.ForkRef) on the
// child. Binding already has a same-named ForkedFrom (string) field plus
// ForkedAtRound (int) -- the existing free-text pair Fork already writes
// and internal/relay/status.go and internal/ui/rail.go read -- so this
// round could not add a second Go field of the same name beside it (see
// the round 3 report for the halt). This test checks the existing pair
// instead, which Fork continues to write unchanged.
// TestForkFeatureOverride pins that an explicit --feature on fork wins over
// the source binding's.
// TestForkRefusesALongNameBeforeCuttingAWorktree pins #64: a 25-character
// name builds a 33-character builder agent name, and Fork must refuse it
// before the worktree is cut -- a refused name leaves nothing behind.
// TestForkRecordsBaseRef pins #136: a fork cut from the source's checkout
// records the branch that checkout (src.Repo) had checked out, so `relay
// land` knows what to rebase the fork's branch onto.
// TestForkRecordsNoBaseRefForACWDFork pins the escape hatch: a --cwd fork
// cuts nothing, so it records no base ref and land asks for --onto.
