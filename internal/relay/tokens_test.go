package relay

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestTokenStateIsTotal(t *testing.T) {
	states := []store.State{
		store.StateActive, store.StateHeld, store.StateNeedsYou,
		store.StateBroken, store.StateOrphaned, store.StateDone,
		store.State(""),
	}
	want := map[string]bool{"active": true, "held": true, "needs-you": true, "done": true}

	for _, s := range states {
		got := tokenState(s)
		if !want[got] {
			t.Errorf("tokenState(%q) = %q, want one of active/held/needs-you/done", s, got)
		}
	}
}

func TestPaneTokensSkipsPanelessBuilders(t *testing.T) {
	cases := map[string]store.Binding{
		"headless":   {Name: "a", Builder: store.Endpoint{Mode: store.ModeHeadless}},
		"remote":     {Name: "a", Builder: store.Endpoint{Mode: store.ModeRemote}},
		"served":     {Name: "a", Serve: &store.ServeFacts{}, Builder: store.Endpoint{PaneID: "p1"}},
		"no pane id": {Name: "a", Builder: store.Endpoint{}},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := PaneTokens(b); ok {
				t.Errorf("PaneTokens(%+v) ok = true, want false", b)
			}
		})
	}

	b := store.Binding{Name: "webshop", Round: 7, State: store.StateActive, Builder: store.Endpoint{PaneID: "w2:p4"}}
	tokens, ok := PaneTokens(b)
	if !ok {
		t.Fatal("PaneTokens on a pane builder: ok = false, want true")
	}
	want := map[string]string{TokenName: "webshop", TokenRound: "007", TokenState: "active"}
	if !reflect.DeepEqual(tokens, want) {
		t.Errorf("tokens = %+v, want %+v", tokens, want)
	}
}

func TestSyncReportsOnceThenOnChange(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)

	bindings := []store.Binding{
		{Name: "a", Round: 1, State: store.StateActive, Builder: store.Endpoint{PaneID: "pA"}},
		{Name: "b", Round: 1, State: store.StateActive, Builder: store.Endpoint{PaneID: "pB"}},
	}
	applied := map[string]appliedMeta{}

	syncPaneMetadata(context.Background(), rt, applied, bindings)
	if len(f.metadata) != 2 {
		t.Fatalf("first call: got %d metadata calls, want 2", len(f.metadata))
	}

	// Same clock, unchanged tokens: nothing re-reported.
	syncPaneMetadata(context.Background(), rt, applied, bindings)
	if len(f.metadata) != 2 {
		t.Fatalf("second call (unchanged): got %d metadata calls, want 2", len(f.metadata))
	}

	// Change one binding's state: exactly one new call, for that binding.
	bindings[0].State = store.StateHeld
	syncPaneMetadata(context.Background(), rt, applied, bindings)
	if len(f.metadata) != 3 {
		t.Fatalf("after change: got %d metadata calls, want 3", len(f.metadata))
	}
	last := f.metadata[len(f.metadata)-1]
	if last.Pane != "pA" || last.Meta.Tokens[TokenState] != "held" {
		t.Errorf("last call = %+v, want pane pA with relay_state=held", last)
	}

	// Advance the clock past metadataRefresh: both re-reported even though
	// nothing changed.
	clock.Advance(metadataRefresh + time.Second)
	syncPaneMetadata(context.Background(), rt, applied, bindings)
	if len(f.metadata) != 5 {
		t.Fatalf("after the refresh window: got %d metadata calls, want 5", len(f.metadata))
	}
}

func TestSyncClearsOnDoneAndOnVanish(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	applied := map[string]appliedMeta{
		"a": {pane: "pA", fp: "stale-a", at: baseTime},
		"b": {pane: "pB", fp: "stale-b", at: baseTime},
	}
	bindings := []store.Binding{
		{Name: "a", Round: 1, State: store.StateDone, Builder: store.Endpoint{PaneID: "pA"}},
		// "b" is absent: unbound since the last sync.
	}

	syncPaneMetadata(context.Background(), rt, applied, bindings)

	if len(f.metadata) != 2 {
		t.Fatalf("got %d metadata calls, want 2 clears", len(f.metadata))
	}
	wantClear := []string{TokenName, TokenRound, TokenState}
	panes := map[string]bool{}
	for _, m := range f.metadata {
		panes[m.Pane] = true
		if len(m.Meta.Tokens) != 0 {
			t.Errorf("clear call carried tokens: %+v", m)
		}
		if !reflect.DeepEqual(m.Meta.ClearTokens, wantClear) {
			t.Errorf("ClearTokens = %v, want %v", m.Meta.ClearTokens, wantClear)
		}
	}
	if !panes["pA"] || !panes["pB"] {
		t.Errorf("expected clears on pA and pB, got %+v", f.metadata)
	}
	if len(applied) != 0 {
		t.Errorf("applied = %+v, want empty", applied)
	}
}

func TestSyncFailureIsRetried(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	bindings := []store.Binding{
		{Name: "a", Round: 1, State: store.StateActive, Builder: store.Endpoint{PaneID: "pA"}},
	}
	applied := map[string]appliedMeta{}

	f.metaErr = errors.New("boom")
	syncPaneMetadata(context.Background(), rt, applied, bindings)
	if len(applied) != 0 {
		t.Errorf("applied = %+v, want empty after a failure", applied)
	}
	if len(f.metadata) != 0 {
		t.Errorf("metadata = %+v, want none recorded on failure", f.metadata)
	}

	f.metaErr = nil
	syncPaneMetadata(context.Background(), rt, applied, bindings)
	if len(f.metadata) != 1 {
		t.Fatalf("got %d metadata calls after the retry, want 1", len(f.metadata))
	}
	if len(applied) != 1 {
		t.Errorf("applied = %+v, want one entry after success", applied)
	}
}

func TestSyncClearsOldPaneOnSwitch(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	applied := map[string]appliedMeta{
		"a": {pane: "p1", fp: "stale", at: baseTime},
	}
	bindings := []store.Binding{
		{Name: "a", Round: 1, State: store.StateActive, Builder: store.Endpoint{PaneID: "p2"}},
	}

	syncPaneMetadata(context.Background(), rt, applied, bindings)

	if len(f.metadata) != 2 {
		t.Fatalf("got %d metadata calls, want 2 (clear old, report new)", len(f.metadata))
	}
	if f.metadata[0].Pane != "p1" || len(f.metadata[0].Meta.Tokens) != 0 {
		t.Errorf("first call = %+v, want a clear on p1", f.metadata[0])
	}
	if f.metadata[1].Pane != "p2" || len(f.metadata[1].Meta.Tokens) == 0 {
		t.Errorf("second call = %+v, want tokens reported on p2", f.metadata[1])
	}
	if applied["a"].pane != "p2" {
		t.Errorf("applied[a].pane = %q, want p2", applied["a"].pane)
	}
}
