package relay

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestRoundInFlight(t *testing.T) {
	cases := []struct {
		name    string
		b       store.Binding
		pending bool
		want    bool
	}{
		{"held", store.Binding{State: store.StateHeld}, false, true},
		{"active with open round", store.Binding{State: store.StateActive, RoundStartedAt: baseTime}, false, true},
		{"active with closed round", store.Binding{State: store.StateActive}, false, false},
		{"broken and switchable", store.Binding{State: store.StateBroken, BuilderCandidate: "agy", RoundStartedAt: baseTime}, false, true},
		{"broken, not switchable (no candidate)", store.Binding{State: store.StateBroken, RoundStartedAt: baseTime}, false, false},
		{"broken, not switchable (round not started)", store.Binding{State: store.StateBroken, BuilderCandidate: "agy"}, false, false},
		{"needs_you with an open round", store.Binding{State: store.StateNeedsYou, RoundStartedAt: baseTime}, false, false},
		{"done", store.Binding{State: store.StateDone, RoundStartedAt: baseTime}, false, false},
		{"pending overrides needs_you", store.Binding{State: store.StateNeedsYou}, true, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := roundInFlight(c.b, c.pending); got != c.want {
				t.Errorf("roundInFlight(%+v, pending=%v) = %v, want %v", c.b, c.pending, got, c.want)
			}
		})
	}
}

func TestFinishedGroup(t *testing.T) {
	inFlightNone := func(store.Binding) bool { return false }

	t.Run("idle, nothing in flight, one pending -> fires", func(t *testing.T) {
		bs := []store.Binding{
			{Name: "a", FinishPending: true},
			{Name: "b"},
		}
		res := FinishedGroup(bs, true, inFlightNone)
		if !res.Fire {
			t.Fatal("Fire = false, want true")
		}
		if len(res.Names) != 1 || res.Names[0] != "a" {
			t.Errorf("Names = %v, want [a]", res.Names)
		}
	})

	t.Run("planner working -> no fire", func(t *testing.T) {
		bs := []store.Binding{{Name: "a", FinishPending: true}}
		res := FinishedGroup(bs, false, inFlightNone)
		if res.Fire {
			t.Error("Fire = true, want false while the planner is working")
		}
	})

	t.Run("one binding in flight -> no fire", func(t *testing.T) {
		bs := []store.Binding{
			{Name: "a", FinishPending: true},
			{Name: "b", FinishPending: true},
		}
		inFlight := func(b store.Binding) bool { return b.Name == "b" }
		res := FinishedGroup(bs, true, inFlight)
		if res.Fire {
			t.Error("Fire = true, want false when a binding is in flight")
		}
	})

	t.Run("no FinishPending anywhere -> no fire even when idle", func(t *testing.T) {
		bs := []store.Binding{{Name: "a"}, {Name: "b"}}
		res := FinishedGroup(bs, true, inFlightNone)
		if res.Fire {
			t.Error("Fire = true, want false with nothing pending")
		}
	})
}

// planner returns the live agent for the shared planner endpoint
// (w2:p3 / planner-sess) at the given status.
func finishedPlanner(status string) herdr.Agent {
	return plannerWith(status, false)
}

func finishedBinding(name string, round int, roundStartedAt time.Time, finishPending bool) store.Binding {
	return store.Binding{
		Name:           name,
		CWD:            "/repo/" + name,
		Planner:        store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:        store.Endpoint{PaneID: "w2:p4-" + name},
		Round:          round,
		State:          store.StateActive,
		RoundStartedAt: roundStartedAt,
		FinishPending:  finishPending,
	}
}

func TestNotifyFinishedFiresOncePerTransition(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	a := finishedBinding("alpha", 2, time.Time{}, true)
	b := finishedBinding("bravo", 2, time.Time{}, true)
	if err := rt.Store.Save(a); err != nil {
		t.Fatalf("save alpha: %v", err)
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save bravo: %v", err)
	}

	agents := []herdr.Agent{finishedPlanner(herdr.StatusIdle)}

	notifyFinished(context.Background(), rt, []store.Binding{a, b}, agents)

	if len(f.notices) != 1 {
		t.Fatalf("got %d notices, want 1", len(f.notices))
	}
	if !strings.Contains(f.notices[0], "alpha") || !strings.Contains(f.notices[0], "bravo") {
		t.Errorf("notice title = %q, want it to name both bindings", f.notices[0])
	}
	if f.sounds[0] != herdr.SoundDone {
		t.Errorf("sound = %q, want %q", f.sounds[0], herdr.SoundDone)
	}

	got, err := rt.Store.Load("alpha")
	if err != nil {
		t.Fatalf("load alpha: %v", err)
	}
	if got.FinishPending {
		t.Error("alpha.FinishPending = true, want false after the toast")
	}
	got, err = rt.Store.Load("bravo")
	if err != nil {
		t.Fatalf("load bravo: %v", err)
	}
	if got.FinishPending {
		t.Error("bravo.FinishPending = true, want false after the toast")
	}

	// A second call against the same (now cleared) bindings must not notify
	// again.
	fresh, err := rt.Store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	notifyFinished(context.Background(), rt, fresh, agents)
	if len(f.notices) != 1 {
		t.Errorf("got %d notices after a second call, want 1", len(f.notices))
	}

	// alpha opens a new round: FinishPending set again, round in flight.
	reloaded, err := rt.Store.Load("alpha")
	if err != nil {
		t.Fatalf("load alpha: %v", err)
	}
	reloaded.FinishPending = true
	reloaded.RoundStartedAt = rt.Now().UTC()
	reloaded.Round++
	if err := rt.Store.Save(reloaded); err != nil {
		t.Fatalf("save alpha: %v", err)
	}

	// While the planner is working, no toast fires even though alpha's flag
	// is set again.
	fresh, err = rt.Store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	notifyFinished(context.Background(), rt, fresh, []herdr.Agent{finishedPlanner(herdr.StatusWorking)})
	if len(f.notices) != 1 {
		t.Errorf("got %d notices while the planner works, want 1 (no new toast)", len(f.notices))
	}

	// The planner goes idle and alpha's round closes: one more toast, naming
	// only alpha.
	reloaded, err = rt.Store.Load("alpha")
	if err != nil {
		t.Fatalf("load alpha: %v", err)
	}
	reloaded.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(reloaded); err != nil {
		t.Fatalf("save alpha: %v", err)
	}
	fresh, err = rt.Store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	notifyFinished(context.Background(), rt, fresh, agents)
	if len(f.notices) != 2 {
		t.Fatalf("got %d notices, want 2", len(f.notices))
	}
	if !strings.Contains(f.notices[1], "alpha") || strings.Contains(f.notices[1], "bravo") {
		t.Errorf("second notice = %q, want it to name only alpha", f.notices[1])
	}
}

func TestNotifyFinishedWaitsForPendingPayload(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)

	b := finishedBinding("alpha", 2, time.Time{}, true)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save alpha: %v", err)
	}

	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Payload: "Builder finished round 1. Report: /x/001-report.md",
	}
	if err := rt.Store.AppendLog("alpha", entry); err != nil {
		t.Fatalf("seed pending payload: %v", err)
	}

	agents := []herdr.Agent{finishedPlanner(herdr.StatusIdle)}
	notifyFinished(context.Background(), rt, []store.Binding{b}, agents)
	if len(f.notices) != 0 {
		t.Fatalf("got %d notices with a payload still pending, want 0", len(f.notices))
	}

	var idx int
	var found bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		_, idx, found, err = tx.PendingForPlanner("alpha")
		return err
	})
	if err != nil || !found {
		t.Fatalf("pending payload not found: found=%v err=%v", found, err)
	}
	if err := rt.Store.ConfirmIndex("alpha", idx); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	notifyFinished(context.Background(), rt, []store.Binding{b}, agents)
	if len(f.notices) != 1 {
		t.Errorf("got %d notices after the payload was confirmed, want 1", len(f.notices))
	}
}
