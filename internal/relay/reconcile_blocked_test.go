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

	body, err := os.ReadFile(rt.Store.QuestionPath("webshop", 1))
	if err != nil {
		t.Fatalf("dialog must be captured to the question path: %v", err)
	}
	if !strings.Contains(string(body), "Allow edit") {
		t.Errorf("question body = %q", body)
	}

	// An approval dialog is drawn on the alternate screen, which never reaches
	// the scrollback recent-unwrapped reads. Reading the wrong source would
	// capture empty or unrelated text, and the planner's whole answer decision
	// rests on this file.
	if len(f.reads) != 1 || f.reads[0].Source != DialogSource {
		t.Fatalf("dialog reads = %+v, want one %q read", f.reads, DialogSource)
	}

	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("question must be queued for the planner: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "relay answer") {
		t.Error("the payload must tell the planner how to answer")
	}

	if _, err := reconcile(t, rt, b, agents); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	entries, err := rt.Store.ReadLog("webshop")
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

// TestBlockedDialogToastsOnce checks the blocked-builder toast (#129): it
// fires once, when the question is queued, naming the dialog's first line.
func TestBlockedDialogToastsOnce(t *testing.T) {
	f := &fakeHerdr{readOut: "Allow edit to src/main.go?  1. Yes  2. No"}
	rt, b := sentBinding(t, f)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusBlocked)}

	b, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.notices) != 1 {
		t.Fatalf("got %d notices, want 1", len(f.notices))
	}
	if !strings.Contains(f.notices[0], "blocked at a dialog") {
		t.Errorf("notice title = %q, want it to contain %q", f.notices[0], "blocked at a dialog")
	}
	if !strings.Contains(f.bodies[0], "Allow edit to src/main.go?") {
		t.Errorf("notice body = %q, want it to contain the dialog's first line", f.bodies[0])
	}
	if f.sounds[0] != herdr.SoundRequest {
		t.Errorf("notice sound = %q, want %q", f.sounds[0], herdr.SoundRequest)
	}

	if _, err := reconcile(t, rt, b, agents); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(f.notices) != 1 {
		t.Errorf("got %d notices after a second tick, want 1; a still-blocked builder must not re-toast", len(f.notices))
	}
}

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
	// again: haltBinding's guard is per-transition, not per-tick.
	second, err := reconcile(t, rt, got, agents)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(f.notices) != 1 {
		t.Errorf("got %d notices, want 1; a still-timed-out binding must not renotify", len(f.notices))
	}
	if len(f.reads) != 1 {
		t.Errorf("reads = %d, want exactly one limit scan, on the halting tick", len(f.reads))
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
func TestReconcileTimeoutOnLimitSwitchesInsteadOfHalting(t *testing.T) {
	f := &fakeHerdr{readOut: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s."}
	rt, b := sentSwitchable(t, f)
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
	if len(f.closed) != 1 || f.closed[0] != "w2:p4" {
		t.Fatalf("closed = %+v, want [w2:p4]", f.closed)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts = %+v, want one", f.starts)
	}
	var switched, timedOut bool
	for _, n := range f.notices {
		if strings.Contains(n, "switched builder") {
			switched = true
		}
		if strings.Contains(n, "run past") {
			timedOut = true
		}
	}
	if !switched || timedOut {
		t.Errorf("notices = %+v, want a switch notice and no timeout notice", f.notices)
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
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
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
