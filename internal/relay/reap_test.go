package relay

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

func seedReapable(t *testing.T, rt Runtime) {
	t.Helper()
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Consults = []store.Consult{
		{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultDone, Endpoint: store.Endpoint{PaneID: "w2:p9"}},
		{ID: "bbbbbbbb", Role: "reviewer", State: store.ConsultSilent, Endpoint: store.Endpoint{PaneID: "w2:pA"}},
		{ID: "cccccccc", Role: "reviewer", State: store.ConsultRunning, Endpoint: store.Endpoint{PaneID: "w2:pB"}},
		{ID: "dddddddd", Role: "reviewer", State: store.ConsultSpawning},
		{ID: "eeeeeeee", Role: "reviewer", State: store.ConsultSilent},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestReapClosesTerminalConsultsAndKeepsRunningOnes(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	res, err := Reap(context.Background(), rt, ReapOptions{Name: "webshop"})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res) != 1 || len(res[0].Closed) != 2 {
		t.Fatalf("closed %+v, want the two terminal consults", res)
	}

	if len(f.closed) != 2 {
		t.Fatalf("closed %d panes, want 2", len(f.closed))
	}
	// Assert the exact set. Counting two and rejecting the running one would
	// still pass if reap closed the same terminal pane twice and missed the
	// other, which leaks a pane while looking correct.
	closed := map[string]bool{}
	for _, pane := range f.closed {
		closed[pane] = true
	}
	if !closed["w2:p9"] || !closed["w2:pA"] {
		t.Errorf("closed %v, want exactly the two terminal panes w2:p9 and w2:pA", f.closed)
	}
	if closed["w2:pB"] {
		t.Error("reap closed a RUNNING consult's pane")
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 2 {
		t.Errorf("consults = %+v, want the running and spawning ones", b.Consults)
	}
}

func TestReapDryRunClosesNothing(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	res, err := Reap(context.Background(), rt, ReapOptions{Name: "webshop", DryRun: true})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res[0].Closed) != 2 {
		t.Errorf("dry run reported %d, want the 2 it would close", len(res[0].Closed))
	}
	if len(f.closed) != 0 {
		t.Errorf("dry run closed %d panes", len(f.closed))
	}

	b, _ := rt.Store.Load("webshop")
	if len(b.Consults) != 5 {
		t.Errorf("dry run dropped records: %+v", b.Consults)
	}
}

func TestReapKeepsTheRecordWhenTheCloseFails(t *testing.T) {
	f := &fakeHerdr{closeErr: errors.New("no such pane")}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	res, err := Reap(context.Background(), rt, ReapOptions{Name: "webshop"})
	if err != nil {
		t.Fatalf("Reap must not fail the whole sweep over one pane: %v", err)
	}
	if len(res[0].Failed) != 2 || len(res[0].Closed) != 0 {
		t.Fatalf("result = %+v", res[0])
	}

	b, _ := rt.Store.Load("webshop")
	if len(b.Consults) != 4 {
		t.Errorf("a failed close dropped the record, so a retry is impossible: %+v", b.Consults)
	}
}

func TestReapDropsATerminalRecordWithNoPane(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	// Dry run: eeeeeeee is listed in Dropped and still saved.
	res, err := Reap(context.Background(), rt, ReapOptions{Name: "webshop", DryRun: true})
	if err != nil {
		t.Fatalf("Reap (dry run): %v", err)
	}
	if len(res) != 1 || len(res[0].Dropped) != 1 || res[0].Dropped[0].ID != "eeeeeeee" {
		t.Fatalf("dry run Dropped = %+v, want eeeeeeee", res[0].Dropped)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var foundE bool
	for _, c := range b.Consults {
		if c.ID == "eeeeeeee" {
			foundE = true
		}
	}
	if !foundE {
		t.Error("dry run dropped eeeeeeee from disk")
	}

	// Real run: eeeeeeee in Dropped, not in f.closed, gone from saved binding; dddddddd kept.
	f.closed = nil
	res, err = Reap(context.Background(), rt, ReapOptions{Name: "webshop"})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res) != 1 || len(res[0].Dropped) != 1 || res[0].Dropped[0].ID != "eeeeeeee" {
		t.Fatalf("Dropped = %+v, want eeeeeeee", res[0].Dropped)
	}
	for _, p := range f.closed {
		if p == "" {
			t.Error("ClosePane was called with an empty pane id")
		}
	}
	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var foundD bool
	foundE = false
	for _, c := range b.Consults {
		if c.ID == "eeeeeeee" {
			foundE = true
		}
		if c.ID == "dddddddd" {
			foundD = true
		}
	}
	if foundE {
		t.Error("eeeeeeee still saved in binding after real reap")
	}
	if !foundD {
		t.Error("dddddddd (spawning) was not kept in binding")
	}
}

func TestReapAllSurvivesABindingVanishingMidSweep(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	// A second binding, sorted after "webshop" so the first close comes from
	// webshop. Store.List reads the state root with os.ReadDir, which returns
	// entries sorted by name, so this ordering is deterministic.
	zlast := store.Binding{
		Name:    "zlast",
		CWD:     "/zlast",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{PaneID: "w2:pC", Kind: "opencode"},
		Round:   1,
		State:   store.StateActive,
		Consults: []store.Consult{{
			ID: "dddddddd", Role: "reviewer", State: store.ConsultDone,
			Endpoint: store.Endpoint{PaneID: "w2:pD"},
		}},
	}
	if err := rt.Store.Save(zlast); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A `relay unbind zlast` that has already completed, observed from inside
	// the sweep. Deliberately os.RemoveAll and not rt.Store.Delete: Delete
	// re-enters WithLock, Reap already holds it, and Go mutexes are not
	// reentrant (see status.go:229), so the test would deadlock rather than
	// fail. Removing the directory reproduces the same end state -- zlast is
	// gone by the time its own closure loads it.
	//
	// The race is real despite the hook firing inside a closure: Reap takes the
	// lock per binding, not per sweep, so a real unbind can land between two
	// bindings' closures.
	var once bool
	f.onClose = func() {
		if once {
			return
		}
		once = true
		if err := os.RemoveAll(rt.Store.Dir("zlast")); err != nil {
			t.Fatalf("RemoveAll: %v", err)
		}
	}

	res, err := Reap(context.Background(), rt, ReapOptions{All: true})
	if err != nil {
		t.Fatalf("a binding unbound mid-sweep aborted the whole sweep: %v", err)
	}

	var closed int
	for _, r := range res {
		closed += len(r.Closed)
	}
	if closed != 2 {
		t.Errorf("closed %d panes, want webshop's 2; the sweep did not finish", closed)
	}
}

func TestReapANamedBindingThatDoesNotExistIsAnError(t *testing.T) {
	// The --all skip must not swallow a genuine mistake: when the human names a
	// binding, a missing one is a typo, not a race.
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	if _, err := Reap(context.Background(), rt, ReapOptions{Name: "nope"}); err == nil {
		t.Fatal("reaping a binding that does not exist returned nil")
	}
}
