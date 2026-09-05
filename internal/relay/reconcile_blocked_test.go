package relay

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestReconcileCapturesBlockingDialogOnce(t *testing.T) {
	f := &fakeHerdr{readOut: "Allow edit to src/main.go?  1. Yes  2. No"}
	rt, b := sentBinding(t, f)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusBlocked)}

	b, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if b.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", b.State)
	}

	body, err := os.ReadFile(rt.Store.QuestionPath("upjo", 1))
	if err != nil {
		t.Fatalf("dialog must be captured to the question path: %v", err)
	}
	if !strings.Contains(string(body), "Allow edit") {
		t.Errorf("question body = %q", body)
	}

	pending, found, err := rt.Store.PendingForPlanner("upjo")
	if err != nil || !found {
		t.Fatalf("question must be queued for the planner: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "relay answer") {
		t.Error("the payload must tell the planner how to answer")
	}

	if _, err := reconcile(t, rt, b, agents); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	entries, err := rt.Store.ReadLog("upjo")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	questions := 0
	for _, e := range entries {
		if e.Kind == store.KindQuestion {
			questions++
		}
	}
	if questions != 1 {
		t.Errorf("got %d question entries, want 1; a still-blocked builder must not re-queue", questions)
	}
}

func TestReconcileFlagsRoundTimeout(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.RoundStartedAt = time.Unix(1757000000, 0).UTC().Add(-31 * time.Minute)
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you after the round timeout", got.State)
	}
	if len(f.notices) == 0 {
		t.Error("a timeout must notify")
	}

	// A second tick against the same already-halted binding must not notify
	// again: haltBinding's guard is per-transition, not per-tick.
	if _, err := reconcile(t, rt, got, agents); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(f.notices) != 1 {
		t.Errorf("got %d notices, want 1; a still-timed-out binding must not renotify", len(f.notices))
	}
}

func TestReconcileStopsAtRoundCap(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.Round = b.RoundCap + 1
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you at the cap", got.State)
	}
	if len(f.prompts) != 0 {
		t.Error("nothing may be relayed past the round cap")
	}
	if len(f.notices) == 0 {
		t.Error("hitting the round cap must notify")
	}
}
