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

// timedOutBinding is a binding whose round is well past its budget, with a
// payload already waiting on the planner. The waiting payload is what made the
// halt and the held-payload notice overwrite each other's state and each
// re-notify on every poll.
func timedOutBinding(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	rt, b := sentBinding(t, f)
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)

	entry := store.LogEntry{
		TS: baseTime, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Payload: "Builder finished round 1. Report: /x/001-report.md",
	}
	if err := rt.Store.AppendLog(b.Name, entry); err != nil {
		t.Fatalf("seed pending payload: %v", err)
	}
	return rt, b
}

// TestReconcileTimeoutNotifiesOnceInEveryPlannerState is the regression test
// for the notification storm: at a 2s poll, a halt that renotifies is a herdr
// notification every couple of seconds, forever, on a live desktop. Two of
// these three planner states used to storm, because the halt's dedup and the
// held-payload dedup each keyed on a State the other one overwrote.
func TestReconcileTimeoutNotifiesOnceInEveryPlannerState(t *testing.T) {
	cases := map[string][]herdr.Agent{
		"planner gone":      {builderAgent(herdr.StatusWorking)},
		"planner focused":   {plannerWith(herdr.StatusIdle, true), builderAgent(herdr.StatusWorking)},
		"planner unfocused": {plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)},
	}

	for name, agents := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeHerdr{}
			rt, b := timedOutBinding(t, f)

			for i := 0; i < 5; i++ {
				var err error
				b, err = reconcile(t, rt, b, agents)
				if err != nil {
					t.Fatalf("tick %d: %v", i+1, err)
				}
			}

			if len(f.notices) != 1 {
				t.Errorf("got %d notices over 5 ticks, want 1: %v", len(f.notices), f.notices)
			}
			if b.State != store.StateNeedsYou {
				t.Errorf("state = %s, want needs_you to survive every tick", b.State)
			}
			if b.HaltNotifiedRound != b.Round {
				t.Errorf("HaltNotifiedRound = %d, want %d", b.HaltNotifiedRound, b.Round)
			}
		})
	}
}

// TestReconcileTimeoutNotifiesAgainInALaterRound is the other half of the
// dedup: one notification per round that goes wrong, not one per binding.
func TestReconcileTimeoutNotifiesAgainInALaterRound(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := timedOutBinding(t, f)
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)}

	b, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The next round is sent, runs long too, and must get its own notice.
	b.Round++
	b.HaltNotifiedRound = 0
	b.State = store.StateActive
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)

	if _, err := reconcile(t, rt, b, agents); err != nil {
		t.Fatalf("second round Reconcile: %v", err)
	}
	if len(f.notices) != 2 {
		t.Errorf("got %d notices, want one per timed-out round: %v", len(f.notices), f.notices)
	}
}

// TestReconcileRoundCapNotifiesOnce guards the same dedup on the other halt.
func TestReconcileRoundCapNotifiesOnce(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.Round = b.RoundCap + 1
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, true), builderAgent(herdr.StatusIdle)}

	for i := 0; i < 5; i++ {
		var err error
		b, err = reconcile(t, rt, b, agents)
		if err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}

	if len(f.notices) != 1 {
		t.Errorf("got %d notices over 5 ticks at the cap, want 1: %v", len(f.notices), f.notices)
	}
}
