package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestRunStopsOnContextCancel(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedBound(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := NewDaemon(rt, 10*time.Millisecond).Run(ctx); err != nil {
		t.Fatalf("Run must exit cleanly on cancel, got %v", err)
	}
}

func TestTickSkipsDoneBindings(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	b.State = store.StateDone
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = []stubAgent{plannerWith(stubIdle, false), builderAgent(stubIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(f.prompts) != 0 {
		t.Error("a done binding must be left entirely alone")
	}
}

// TestRunSurvivesFailingTick guards Run's half of resilience: a tick that
// keeps failing must not stop the loop or bubble the tick error out of Run.
// Guarded by a timeout so a regression that makes Run return the tick error
// (or hang) fails the test loudly instead of wedging the suite, the same
// shape as store.TestNestedAccessDoesNotDeadlock.
func TestRunSurvivesFailingTick(t *testing.T) {
	f := &fakePanes{}
	rt, _ := sentBinding(t, f)
	f.listErr = errors.New("herdr socket unavailable") // every tick's own
	// ListAgents call fails from here on.

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewDaemon(rt, 10*time.Millisecond).Run(ctx)
	}()

	time.Sleep(1100 * time.Millisecond) // a few floored (500ms) tick intervals
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run must survive repeated tick failures and exit clean on cancel, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel (timeout) -- a failing tick must not wedge it")
	}
}

// TestNewDaemonFloorsInterval guards the floor by inspection made concrete:
// a misconfigured (zero or negative) interval must not spin the herdr socket.
func TestNewDaemonFloorsInterval(t *testing.T) {
	rt := Runtime{LedgerPath: filepath.Join(t.TempDir(), "ledger.json"), AvailabilityPath: filepath.Join(t.TempDir(), "availability.json")}

	if d := NewDaemon(rt, 0); d.interval != minInterval {
		t.Errorf("zero interval -> %s, want floor %s", d.interval, minInterval)
	}
	if d := NewDaemon(rt, -time.Second); d.interval != minInterval {
		t.Errorf("negative interval -> %s, want floor %s", d.interval, minInterval)
	}
	if d := NewDaemon(rt, time.Minute); d.interval != time.Minute {
		t.Errorf("an interval already above the floor must pass through unchanged, got %s", d.interval)
	}
}

// TestTickIgnoresBindingUnboundMidTick covers the window between Tick's
// binding list and its per-binding load: a `relay unbind` landing in it is
// normal use, not a failure, and must not be logged as one.
func TestTickIgnoresBindingUnboundMidTick(t *testing.T) {
	f := &fakePanes{}
	rt, _ := sentBinding(t, f)
	f.agents = []stubAgent{plannerWith(stubIdle, false), builderAgent(stubIdle)}
	f.onList = func() {
		if err := rt.Store.Delete("webshop"); err != nil {
			t.Errorf("unbind mid-tick: %v", err)
		}
	}

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(previous)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if strings.Contains(logged.String(), "reconcile failed") {
		t.Errorf("an unbind mid-tick must not be logged as a failure: %s", logged.String())
	}
}

func TestTickDoesNotRestampAnUnchangedBinding(t *testing.T) {
	// The next == fresh short-circuit this replaces was never tested. save()
	// stamps UpdatedAt unconditionally, so without the short-circuit every tick
	// rewrites every bind.json and UpdatedAt stops meaning "last change".
	f := &fakePanes{}
	rt, b := seedBound(t, f)

	before, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	after, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("UpdatedAt moved %v -> %v on a tick that changed nothing",
			before.UpdatedAt, after.UpdatedAt)
	}
}

// TestTickRefreshesRuntimeBeforeReconcile confirms Tick calls d.refresh
// before it reconciles, so the round the reconcile pass sees is whatever the
// refresh just swapped in -- not last tick's copy.
func TestTickRefreshesRuntimeBeforeReconcile(t *testing.T) {
	f := &fakePanes{}
	rt, _ := sentBinding(t, f)
	f.agents = []stubAgent{plannerWith(stubIdle, false), builderAgent(stubIdle)}

	const marker = "refreshed-marker"
	refreshCalls := 0
	d := NewDaemon(rt, time.Second).WithRefresh(func(in Runtime) Runtime {
		refreshCalls++
		in.Policy = policy.Policy{Order: map[string][]string{"builder": {marker}}}
		return in
	})

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1", refreshCalls)
	}
	if got := d.rt.Policy.Order["builder"]; len(got) != 1 || got[0] != marker {
		t.Errorf("d.rt.Policy not swapped by refresh, got %v", got)
	}
}

// TestTickIngestsLiveBindings guards the daemon's end-of-tick ingest hook
// (docs/specs/2026-09-20-persistence-design.md §5.5): with a db configured,
// a tick over a live, sent binding must leave a matching binding and round
// row behind.
func TestTickIngestsLiveBindings(t *testing.T) {
	f := &fakePanes{}
	rt, _ := sentBinding(t, f)

	d, err := db.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	rt.DB = d

	f.agents = []stubAgent{plannerWith(stubIdle, false), builderAgent(stubIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, found, err := d.Binding("webshop")
	if err != nil {
		t.Fatalf("Binding: %v", err)
	}
	if !found {
		t.Fatal("Binding(webshop) not found after Tick")
	}
	rounds, err := d.Rounds(b.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if len(rounds) != 1 {
		t.Errorf("len(rounds) = %d, want 1", len(rounds))
	}
}

// TestTickWithoutDBIsUnchanged guards the nil-DB path: every call site
// (here, the ingest hook) must treat Runtime.DB == nil exactly like a
// machine with no database -- no panic, and no relay.db file conjured into
// existence by the mere act of ticking.
func TestTickWithoutDBIsUnchanged(t *testing.T) {
	f := &fakePanes{}
	rt, _ := sentBinding(t, f)
	f.agents = []stubAgent{plannerWith(stubIdle, false), builderAgent(stubIdle)}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if _, err := os.Stat(rt.Store.DBPath()); !os.IsNotExist(err) {
		t.Errorf("relay.db stat = %v, want os.ErrNotExist (DB == nil must write nothing)", err)
	}
}

// waitListCall drains one signal from a channel fed by fakePanes.onList,
// which is the race-safe way these event-loop tests observe a ListAgents
// call: fakePanes has no lock of its own, so reading its listCalls counter
// directly from the test goroutine while Run's goroutine (or a reconnect
// goroutine it spawned) may still be writing it would be a data race. The
// channel receive is the synchronisation point instead, and it also gives a
// happens-before edge for anything that goroutine did earlier in the same
// call (e.g. appending to subscribeCalls before making the ListAgents call
// that follows it in d.subscribe).
func waitListCall(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}

// waitForState busy-polls the store (itself lock-synchronised, unlike
// fakePanes) until name reaches want or the deadline passes.
func waitForState(t *testing.T, rt Runtime, name string, want store.State) store.Binding {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		b, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		if b.State == want {
			return b
		}
		select {
		case <-deadline:
			t.Fatalf("%s never reached state %s, last seen %s", name, want, b.State)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
