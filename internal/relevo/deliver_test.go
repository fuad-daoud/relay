package relevo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeClaimStore is the map-backed ClaimStore the plan asks for: a claim
// present for a planner id means live, absent means not.
type fakeClaimStore map[string]*Claim

func (f fakeClaimStore) Live(planner string, now time.Time) (*Claim, error) {
	return f[planner], nil
}

func (f fakeClaimStore) Write(c Claim, now time.Time) error {
	f[c.Planner] = &c
	return nil
}

func (f fakeClaimStore) Remove(planner string, pid int) error {
	delete(f, planner)
	return nil
}

// SweepPaneKeyed is a no-op here: the sweep's own rule has its own test in
// channel_test.go, and this fake is only ever read through Live.
func (f fakeClaimStore) SweepPaneKeyed() int { return 0 }

// routeRuntime is a minimal Runtime for the delivery-route tests: a temp
// store, a fixed clock and nothing else wired.
func routeRuntime(t *testing.T) Runtime {
	t.Helper()
	return Runtime{
		Store: store.New(t.TempDir()),
		Now:   func() time.Time { return baseTime },
	}
}

// seedPending saves an active binding and one unconfirmed planner-bound
// entry, which is exactly what DeliverPending and Pull work on.
func seedPending(t *testing.T, rt Runtime, name, plannerID, kind string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:      name,
		CWD:       "/repo/" + name,
		Round:     1,
		State:     store.StateActive,
		Planner:   store.Endpoint{Kind: kind, SessionID: "sess"},
		PlannerID: plannerID,
		Builder:   store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return Queue(context.Background(), rt, tx, name, store.LogEntry{
			Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
			Payload: "round 1 report", Path: "/tmp/report.md",
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return b
}

// deliverOnce runs DeliverPending under the state lock, the way the daemon
// and `relevo wait` do.
func deliverOnce(t *testing.T, rt Runtime, b store.Binding) (store.Binding, Delivery) {
	t.Helper()
	var (
		next store.Binding
		got  Delivery
	)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, got, err = DeliverPending(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	return next, got
}

// notMineDeliverer is #300's OutcomeNotMine: a deliverer that refuses this
// planner. Its payload must stay pending -- there is no pane to fall through
// to any more (#303 §5.4).
type notMineDeliverer struct{}

func (notMineDeliverer) Deliver(context.Context, store.Endpoint, string, string, time.Time) (Outcome, string, error) {
	return OutcomeNotMine, "not mine to deliver", nil
}

// TestDeliverPendingNoRouteStaysPendingAsPull is the plan's required case:
// with no live claim and no deliverer for the planner's kind, the entry
// stays pending with route=pull. For a Claude Code planner in tools mode
// that is the normal path, not a fault (D6).
func TestDeliverPendingNoRouteStaysPendingAsPull(t *testing.T) {
	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if got.Delivered {
		t.Error("nothing can deliver this payload; Delivered must be false")
	}
	if got.Route != "pull" {
		t.Errorf("Route = %q, want pull", got.Route)
	}
	if !strings.Contains(got.Reason, "awaiting pull") {
		t.Errorf("Reason = %q, want it to name the awaiting-pull route", got.Reason)
	}
	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || !found {
		t.Errorf("the entry must stay pending (found=%v err=%v)", found, err)
	}
}

// TestDeliverPendingDelivererNotMineStaysPending is the plan's required
// case for the deleted pane fallback: #300's OutcomeNotMine used to fall
// through to typing the payload into a pane. It now leaves the entry pending
// with the deliverer's own reason.
func TestDeliverPendingDelivererNotMineStaysPending(t *testing.T) {
	rt := routeRuntime(t)
	rt.Deliverers = map[string]PlannerDeliverer{"claude": notMineDeliverer{}}
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if got.Delivered {
		t.Error("OutcomeNotMine must not confirm the entry")
	}
	if got.Route != "pull" {
		t.Errorf("Route = %q, want pull", got.Route)
	}
	if got.Reason != "not mine to deliver" {
		t.Errorf("Reason = %q, want the deliverer's own reason", got.Reason)
	}
	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || !found {
		t.Errorf("the entry must stay pending (found=%v err=%v)", found, err)
	}
}

// TestDeliverPendingChannelByPlannerID keeps the surviving channel route:
// with a live claim for the binding's planner id, DeliverPending hands the
// entry to the channel -- it stays pending for the claim holder's own poll,
// which is what pushes and confirms it with route=channel.
func TestDeliverPendingChannelByPlannerID(t *testing.T) {
	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{"pl_aaaaaaaabbbb": &Claim{Planner: "pl_aaaaaaaabbbb", PID: 1}}
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if got.Route != "channel" {
		t.Errorf("Route = %q, want channel", got.Route)
	}
	if got.Delivered {
		t.Error("the channel's own drain confirms the entry; DeliverPending must not")
	}
	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || !found {
		t.Errorf("the entry must stay pending for the channel reader (found=%v err=%v)", found, err)
	}
}

// TestDeliverPendingMarksDeliveredByDeliverer keeps #300's port working: a
// deliverer that reports OutcomeDelivered confirms the entry with
// route=deliverer:<kind>.
func TestDeliverPendingMarksDeliveredByDeliverer(t *testing.T) {
	rt := routeRuntime(t)
	rt.Deliverers = map[string]PlannerDeliverer{"claude": deliveredDeliverer{}}
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if !got.Delivered || got.Route != "deliverer:claude" {
		t.Fatalf("Delivery = %+v, want delivered by deliverer:claude", got)
	}
	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || found {
		t.Errorf("a delivered entry must be confirmed (found=%v err=%v)", found, err)
	}
}

// deliveredDeliverer confirms whatever it is handed.
type deliveredDeliverer struct{}

func (deliveredDeliverer) Deliver(context.Context, store.Endpoint, string, string, time.Time) (Outcome, string, error) {
	return OutcomeDelivered, "handed to the session", nil
}

// TestDeliverPendingWithNothingPendingIsNoop keeps the empty case: nothing
// queued reads as nothing to do, never an error.
func TestDeliverPendingWithNothingPendingIsNoop(t *testing.T) {
	rt := routeRuntime(t)
	b := store.Binding{
		Name: "webshop", CWD: "/repo/webshop", Round: 1, State: store.StateActive,
		Planner: store.Endpoint{Kind: "claude"}, PlannerID: "pl_aaaaaaaabbbb",
	}

	_, got := deliverOnce(t, rt, b)

	if got.Delivered || !got.Empty {
		t.Errorf("Delivery = %+v, want Empty with nothing delivered", got)
	}
}

// stubDeliverer is the PlannerDeliverer test double deliver_test.go controls
// directly, so DeliverPending's consult step can be exercised without a
// real opencode service.
type stubDeliverer struct {
	outcome Outcome
	reason  string
	err     error
	calls   int
}

func (s *stubDeliverer) Deliver(_ context.Context, _ store.Endpoint, _, _ string, _ time.Time) (Outcome, string, error) {
	s.calls++
	return s.outcome, s.reason, s.err
}

// TestDeliverConsultsDelivererForMatchingKind proves DeliverPending routes a
// matching planner's payload through rt.Deliverers exactly once: the stub
// reports OutcomeDelivered, the entry is confirmed, and nothing else is
// consulted. Before #303 this test also proved the payload never reached a
// pane; there is no pane now, so the assertion is the deliverer's own call
// count plus the confirmed entry.
func TestDeliverConsultsDelivererForMatchingKind(t *testing.T) {
	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	stub := &stubDeliverer{outcome: OutcomeDelivered, reason: "already present"}
	rt.Deliverers = map[string]PlannerDeliverer{"opencode": stub}

	next, got := deliverOnce(t, rt, b)
	if !got.Delivered || got.Reason != "already present" {
		t.Fatalf("want delivered via the deliverer, got %+v", got)
	}
	if stub.calls != 1 {
		t.Fatalf("deliverer calls = %d, want 1", stub.calls)
	}
	if _, pending, err := rt.Store.PendingForPlanner(b.Name); err != nil || pending {
		t.Errorf("an OutcomeDelivered outcome must confirm the log entry: pending=%v err=%v", pending, err)
	}
	if next.PlannerScreen != "" {
		t.Errorf("a deliverer-routed delivery must leave the deleted fingerprint state empty, got %+v", next)
	}
}

// TestDeliverNilDeliverersBehavesAsToday proves a nil Deliverers map -- the
// zero value every existing test already runs with -- takes the pull route
// exactly as it did before this round: nothing is confirmed, and the payload
// waits for `relevo wait`.
func TestDeliverNilDeliverersBehavesAsToday(t *testing.T) {
	rt := routeRuntime(t)
	rt.Deliverers = nil
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")

	_, got := deliverOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("a nil Deliverers map must not deliver, got %+v", got)
	}
	if got.Route != "pull" {
		t.Errorf("Route = %q, want pull", got.Route)
	}
	if _, pending, err := rt.Store.PendingForPlanner(b.Name); err != nil || !pending {
		t.Errorf("the payload must stay pending: pending=%v err=%v", pending, err)
	}
}

// TestDeliverYieldsToLiveClaim is the daemon-guard test the plan requires:
// a live claim on the planner's id must produce an all-false Delivery, with
// zero prompts, zero notifies, the entry still pending, and the
// binding's State left untouched. Commenting out the guard in DeliverPending
// makes this fail on the route (verified by hand per the plan's step
// 2 instructions).
func TestDeliverYieldsToLiveClaim(t *testing.T) {
	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", testClaimPlanner, "claude")
	rt.Channels = fakeClaimStore{b.PlannerID: &Claim{Planner: b.PlannerID, PID: 1, SeenAt: rt.Now()}}
	wantState := b.State

	next, got := deliverOnce(t, rt, b)
	if got.Delivered || got.Route == "" || got.Empty {
		t.Fatalf("want the all-false channel handoff, got %+v", got)
	}
	if got.Route != "channel" {
		t.Errorf("Route = %q, want channel", got.Route)
	}
	if next.State != wantState {
		t.Errorf("State = %q, want unchanged %q", next.State, wantState)
	}
	if _, pending, err := rt.Store.PendingForPlanner(b.Name); err != nil || !pending {
		t.Errorf("the entry must stay pending: pending=%v err=%v", pending, err)
	}
}

// TestDeliverIgnoresStaleClaim proves the guard is inert when the claim
// store answers "not live" (or knows nothing about the planner): delivery
// falls through to the pull route exactly as it did before the channel
// existed.
func TestDeliverIgnoresStaleClaim(t *testing.T) {
	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", testClaimPlanner, "claude")
	rt.Channels = fakeClaimStore{} // no entry for this planner: Live returns nil, nil

	_, got := deliverOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("a stale claim must not block or confirm, got %+v", got)
	}
	if got.Route != "pull" {
		t.Fatalf("Route = %q, want the pull route", got.Route)
	}
	if _, pending, err := rt.Store.PendingForPlanner(b.Name); err != nil || !pending {
		t.Errorf("the entry must stay pending for pull: pending=%v err=%v", pending, err)
	}
}
