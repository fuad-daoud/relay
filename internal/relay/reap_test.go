package relay

import (
	"context"
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

func TestReapANamedBindingThatDoesNotExistIsAnError(t *testing.T) {
	// The --all skip must not swallow a genuine mistake: when the human names a
	// binding, a missing one is a typo, not a race.
	f := &fakePanes{}
	rt, _ := seedBound(t, f)

	if _, err := Reap(context.Background(), rt, ReapOptions{Name: "nope"}); err == nil {
		t.Fatal("reaping a binding that does not exist returned nil")
	}
}
