package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// TestBlockedDialogToastsOnce checks the blocked-builder toast (#129): it
// fires once, when the question is queued, naming the dialog's first line.
func TestReconcileFlagsRoundTimeout(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	// Pin the budget explicitly: this exercises the timeout mechanism, not
	// whatever the store's default happens to be.
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
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
	if want := "round 1 has run past 30m0s"; got.Halt != want {
		t.Errorf("Halt = %q, want %q", got.Halt, want)
	}
	if !got.HaltAt.Equal(rt.Now().UTC()) {
		t.Errorf("HaltAt = %v, want %v", got.HaltAt, rt.Now().UTC())
	}
	haltAt := got.HaltAt

	// A second tick against the same already-halted binding must not notify
	// again: haltBinding's guard is per-transition, not per-tick. (#135's stall
	// notice fires on the first tick too, so both are counted by their text.)
	second, err := reconcile(t, rt, got, agents)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	var halts int
	for _, n := range f.notices {
		if strings.Contains(n, "run past") {
			halts++
		}
	}
	if halts != 1 {
		t.Errorf("got %d timeout notices, want 1; a still-timed-out binding must not renotify: %v", halts, f.notices)
	}
	// A headless builder has no herdr screen to read.
	if len(f.reads) != 0 {
		t.Errorf("reads = %d, want 0: a local builder is headless", len(f.reads))
	}
	if !second.HaltAt.Equal(haltAt) {
		t.Errorf("HaltAt = %v after a second tick, want unchanged %v", second.HaltAt, haltAt)
	}

	// Even once the clock has moved on, a still-halted binding's HaltAt must
	// stay pinned to the halting tick, not drift to a later poll.
	third, err := reconcile(t, at(rt, time.Minute), second, agents)
	if err != nil {
		t.Fatalf("third Reconcile: %v", err)
	}
	if !third.HaltAt.Equal(haltAt) {
		t.Errorf("HaltAt = %v after a later tick, want unchanged %v", third.HaltAt, haltAt)
	}
}

// TestReconcileTimeoutOnLimitSwitchesInsteadOfHalting checks the pane
// budget's decision point (spec §5): the halting tick scans first, and a
// match switches the builder instead of halting.
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
	if len(f.sounds) == 0 || f.sounds[0] != herdr.SoundRequest {
		t.Errorf("sounds = %+v, want first sound %q", f.sounds, herdr.SoundRequest)
	}
}

// timedOutBinding is a binding whose round is well past its budget, with a
// payload already waiting on the planner. The waiting payload is what made the
// halt and the held-payload notice overwrite each other's state and each
// re-notify on every poll.
func timedOutBinding(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	rt, b := sentBinding(t, f)
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)
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

			// #135 adds one stall notice on the first tick (the round has been
			// quiet past stall_after_ms); the halt itself must still notify
			// exactly once over all five ticks, which is what guards against
			// the storm this test exists for.
			var halts int
			for _, n := range f.notices {
				if strings.Contains(n, "run past") {
					halts++
				}
			}
			if halts != 1 {
				t.Errorf("got %d timeout notices over 5 ticks, want 1: %v", halts, f.notices)
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
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		TS: baseTime, Round: b.Round, Direction: store.DirToBuilder, Kind: store.KindPlan,
		Path: rt.Store.PlanPath(b.Name, b.Round), Confirmed: true,
	}); err != nil {
		t.Fatalf("seed round %d plan: %v", b.Round, err)
	}

	if _, err := reconcile(t, rt, b, agents); err != nil {
		t.Fatalf("second round Reconcile: %v", err)
	}
	// One timeout notice per timed-out round, not one per binding. (#135's
	// stall notice is separate and fires once for the episode.)
	var halts int
	for _, n := range f.notices {
		if strings.Contains(n, "run past") {
			halts++
		}
	}
	if halts != 2 {
		t.Errorf("got %d timeout notices, want one per timed-out round: %v", halts, f.notices)
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
