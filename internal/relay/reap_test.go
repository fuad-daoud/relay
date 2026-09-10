package relay

import (
	"context"
	"errors"
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
	if len(b.Consults) != 1 || b.Consults[0].ID != "cccccccc" {
		t.Errorf("consults = %+v, want only the running one", b.Consults)
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
	if len(b.Consults) != 3 {
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
	if len(b.Consults) != 3 {
		t.Errorf("a failed close dropped the record, so a retry is impossible: %+v", b.Consults)
	}
}
