package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestTickReconcilesAndPersists(t *testing.T) {
	rt, _ := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Errorf("tick must persist the advanced round, got %d", b.Round)
	}
	// #303 deleted the pane injection; the report is queued for the planner
	// and, with no live channel claim and no deliverer, stays pending for
	// `relevo pull`.
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("the closed round's report must be queued for the planner: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindReport {
		t.Errorf("pending kind = %s, want report", pending.Kind)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	rt, _ := seedBound(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := NewDaemon(rt, 10*time.Millisecond).Run(ctx); err != nil {
		t.Fatalf("Run must exit cleanly on cancel, got %v", err)
	}
}

func TestTickSkipsDoneBindings(t *testing.T) {
	rt, b := sentBinding(t)
	b.State = store.StateDone
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !store.SameBinding(before, after) {
		t.Error("a done binding must be left entirely alone")
	}
}

// TestDaemonBackfillsPlannerID is the plan's required case for §5.6 (last
// paragraph): a tick back-fills PlannerID from (Planner.Kind,
// Planner.SessionID) when the registry knows that session, and leaves it
// empty when it does not.
func TestDaemonBackfillsPlannerID(t *testing.T) {
	// A binding written before PlannerID existed: its planner endpoint names
	// the session, and PlannerID is empty.
	legacy := func(t *testing.T, rt Runtime, session string) store.Binding {
		t.Helper()
		b := store.Binding{
			Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive,
			Planner: store.Endpoint{Kind: "claude", SessionID: session},
			Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("seed legacy binding: %v", err)
		}
		return b
	}

	rt := newRuntime(t)
	b := legacy(t, rt, "sess-architect") // the record newRuntime seeds
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.PlannerID != testPlannerID {
		t.Errorf("PlannerID = %q, want the record's %q", got.PlannerID, testPlannerID)
	}

	// A miss does nothing: no record for this session, no PlannerID.
	rt2 := newRuntime(t)
	rt2.Planners = &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}
	b2 := legacy(t, rt2, "sess-nobody")
	if err := NewDaemon(rt2, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick (miss): %v", err)
	}
	got2, err := rt2.Store.Load(b2.Name)
	if err != nil {
		t.Fatalf("Load (miss): %v", err)
	}
	if got2.PlannerID != "" {
		t.Errorf("a miss must leave PlannerID empty, got %q", got2.PlannerID)
	}
}

// TestTickSurfacesListAgentsFailure guarded the resilience contract at the
// boundary where the daemon actually talked to a pane; #303 deleted that
// boundary (closed-list item 6: the pane client and its agent list). The
// daemon's one list call is now the store's, so the same contract is pinned
// there: a tick whose binding list fails is visible to the caller.
func TestTickSurfacesListAgentsFailure(t *testing.T) {
	rt, _ := sentBinding(t)
	// A state root that cannot be prepared: the "directory" is a regular
	// file, so MkdirAll fails and every store call with it.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rt.Store = store.New(notADir)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err == nil {
		t.Fatal("Tick must surface a failing binding list")
	}
}

// TestTickContinuesPastFailingBinding guards the other half of resilience:
// one binding's reconcile error must not abort the rest of the tick; here the
// error is the tick's own list call, and Run's half is
// TestRunSurvivesFailingTick below.
// TestRunSurvivesFailingTick guards Run's half of resilience: a tick that
// keeps failing must not stop the loop or bubble the tick error out of Run.
// Guarded by a timeout so a regression that makes Run return the tick error
// (or hang) fails the test loudly instead of wedging the suite, the same
// shape as store.TestNestedAccessDoesNotDeadlock.
func TestRunSurvivesFailingTick(t *testing.T) {
	rt, _ := sentBinding(t)
	// Every tick's first step fails: the state root cannot be prepared.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rt.Store = store.New(notADir)

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
// a misconfigured (zero or negative) interval must not spin the tick.
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
// binding list and its per-binding load: a `relevo unbind` landing in it is
// normal use, not a failure, and must not be logged as one. #303 deleted
// the old fake's onList hook, which is what used to interpose the unbind inside
// the tick, so the test drives tickOne -- the exact function holding the
// guard -- directly.
func TestTickIgnoresBindingUnboundMidTick(t *testing.T) {
	rt, _ := sentBinding(t)
	if err := rt.Store.Delete("webshop"); err != nil {
		t.Fatalf("unbind mid-tick: %v", err)
	}

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(previous)

	if err := NewDaemon(rt, time.Second).tickOne(context.Background(), "webshop"); err != nil {
		t.Fatalf("a binding unbound mid-tick must be skipped, got %v", err)
	}
	if strings.Contains(logged.String(), "reconcile failed") {
		t.Errorf("an unbind mid-tick must not be logged as a failure: %s", logged.String())
	}
}

func TestTickDoesNotRestampAnUnchangedBinding(t *testing.T) {
	// The next == fresh short-circuit this replaces was never tested. save()
	// stamps UpdatedAt unconditionally, so without the short-circuit every tick
	// rewrites every bind.json and UpdatedAt stops meaning "last change".
	rt, b := seedBound(t)

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
	rt, _ := sentBinding(t)

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

// TestTickWithoutRefreshIsUnchanged confirms a Daemon with no WithRefresh
// call behaves exactly as before #209 -- the same fixture and assertions as
// TestTickReconcilesAndPersists, the test this one relies on to prove the
// nil-refresh path is untouched.
func TestTickWithoutRefreshIsUnchanged(t *testing.T) {
	rt, _ := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Errorf("tick must persist the advanced round, got %d", b.Round)
	}
	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || !found {
		t.Errorf("the closed round's report must be queued: found=%v err=%v", found, err)
	}
}

// TestTickSyncsMetadataAndFinishedAfterReconcile checks that Tick calls both
// syncPaneMetadata and notifyFinished after the binding loop (#129, #182).
// The finished-toast decision itself is covered by finished_test.go; here
// only that Tick wires both into the pass, using a binding with a closed
// round, no pending payload, and an idle planner so the toast fires within
// this one tick.
// TestTickIngestsLiveBindings guards the daemon's end-of-tick ingest hook
// (docs/specs/2026-09-20-persistence-design.md §5.5): with a db configured,
// a tick over a live, sent binding must leave a matching binding and round
// row behind.
func TestTickIngestsLiveBindings(t *testing.T) {
	rt, _ := sentBinding(t)

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	rt.DB = d

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
// machine with no database -- no panic, and no relevo.db file conjured into
// existence by the mere act of ticking.
func TestTickWithoutDBIsUnchanged(t *testing.T) {
	rt, _ := sentBinding(t)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if _, err := os.Stat(rt.Store.DBPath()); !os.IsNotExist(err) {
		t.Errorf("relevo.db stat = %v, want os.ErrNotExist (DB == nil must write nothing)", err)
	}
}

// waitForState busy-polls the store (itself lock-synchronised) until name
// reaches want or the deadline passes.
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

// fakeFetcher counts calls so a test can prove the tick asked the endpoint --
// or, on a fresh cache, never asked at all.
type fakeFetcher struct {
	calls int
	tag   string
	err   error
}

func (f *fakeFetcher) Latest(ctx context.Context) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.tag, nil
}

// releaseStateRoot points store.DefaultRoot() at a temp root, so no test in
// this package ever reads or writes the user's real state. The release check
// is the one daemon path that composes its own path from that root (#293).
func releaseStateRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	return root
}

// TestTickRefreshesOncePastTTL counts fetches: none while the cached answer is
// fresh, one once it is stale. Drop the Stale guard and the fresh case fails.
func TestTickRefreshesOncePastTTL(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		seed      bool
		checkedAt time.Time
		wantCalls int
	}{
		{
			name:      "fresh cache is left alone",
			seed:      true,
			checkedAt: now.Add(-(release.TTL - time.Second)),
			wantCalls: 0,
		},
		{
			name:      "stale cache refetches",
			seed:      true,
			checkedAt: now.Add(-(release.TTL + time.Second)),
			wantCalls: 1,
		},
		{
			name:      "no cache at all fetches",
			wantCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := releaseStateRoot(t)
			if tc.seed {
				if err := release.Save(root, release.Cache{
					Latest:    "v0.7.0",
					CheckedAt: tc.checkedAt,
					Source:    "test",
				}); err != nil {
					t.Fatalf("seed cache: %v", err)
				}
			}

			ff := &fakeFetcher{tag: "v0.8.0"}
			rt, _ := sentBinding(t)
			rt.Fetcher = ff
			rt.Now = func() time.Time { return now }

			if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}

			if ff.calls != tc.wantCalls {
				t.Errorf("fetch calls = %d, want %d", ff.calls, tc.wantCalls)
			}

			c, ok, err := release.Load(root)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := "v0.8.0"
			if tc.wantCalls == 0 {
				want = "v0.7.0" // the fresh answer stays exactly as it was
			}
			if !ok || c.Latest != want {
				t.Errorf("cache = (%+v, ok %v), want latest %s", c, ok, want)
			}
			if tc.wantCalls == 0 && !c.CheckedAt.Equal(tc.checkedAt) {
				t.Errorf("fresh cache checked_at = %s, want it untouched at %s", c.CheckedAt, tc.checkedAt)
			}
		})
	}
}

// TestTickSurvivesFetchError pins §4.4's failure rule: a fetch error is
// swallowed, Tick still returns nil, and the cache is not written -- so an
// offline machine retries next tick instead of recording a wrong answer.
func TestTickSurvivesFetchError(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	t.Run("no cache yet", func(t *testing.T) {
		root := releaseStateRoot(t)

		ff := &fakeFetcher{err: errors.New("dial tcp: network is unreachable")}
		rt, _ := sentBinding(t)
		rt.Fetcher = ff
		rt.Now = func() time.Time { return now }

		if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
			t.Errorf("Tick = %v, want nil: a failed release check must not stop the daemon", err)
		}
		if ff.calls != 1 {
			t.Errorf("fetch calls = %d, want 1 (the stale check tried)", ff.calls)
		}
		if _, err := os.Stat(filepath.Join(root, "release-check.json")); !os.IsNotExist(err) {
			t.Errorf("cache stat = %v, want os.ErrNotExist: a failed fetch saves nothing", err)
		}
	})

	t.Run("stale cache is left alone", func(t *testing.T) {
		root := releaseStateRoot(t)
		stale := release.Cache{Latest: "v0.7.0", CheckedAt: now.Add(-2 * release.TTL), Source: "test"}
		if err := release.Save(root, stale); err != nil {
			t.Fatalf("seed cache: %v", err)
		}

		ff := &fakeFetcher{err: errors.New("504 gateway timeout")}
		rt, _ := sentBinding(t)
		rt.Fetcher = ff
		rt.Now = func() time.Time { return now }

		if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
			t.Errorf("Tick = %v, want nil", err)
		}

		c, ok, err := release.Load(root)
		if err != nil || !ok {
			t.Fatalf("Load = (%+v, ok %v, %v), want the seeded cache", c, ok, err)
		}
		if c.Latest != stale.Latest || !c.CheckedAt.Equal(stale.CheckedAt) {
			t.Errorf("cache = %+v, want the stale answer untouched at %+v", c, stale)
		}
	})
}

// panickingRunner is the fake Runner with one pid whose Alive panics, so a
// test can prove the daemon contains a panic raised under one binding
// (#370, spec §4.6).
type panickingRunner struct {
	*fakeRunner
	panicPID int
}

func (p *panickingRunner) Alive(ctx context.Context, h ProcHandle) (bool, error) {
	if h.PID == p.panicPID {
		panic("alive exploded")
	}
	return p.fakeRunner.Alive(ctx, h)
}

// TestTickSurvivesAPanickingReconcile pins #370, spec §4.6: a panic raised
// anywhere under one binding is recovered in tickOne, logged, and returned as
// an error, so the tick still reconciles every other binding and returns nil.
//
// Mutation check: remove the recover from tickOne and this test crashes the
// test binary instead of passing.
func TestTickSurvivesAPanickingReconcile(t *testing.T) {
	fr := newFakeRunner()
	rt, first := sentHeadless(t, fr)

	// A second, healthy binding on the same store, reconciled by the same
	// tick. Its own working tree, since a tree carries one binding.
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "other", Candidate: testAgyRef, PlannerID: testPlannerName, CWD: "/repo-other",
	}); err != nil {
		t.Fatalf("Bind other: %v", err)
	}
	if _, err := Send(context.Background(), rt, "other", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send other: %v", err)
	}
	other, err := rt.Store.Load("other")
	if err != nil {
		t.Fatalf("Load other: %v", err)
	}

	// From here on, observing the first binding's builder panics.
	rt.Runner = &panickingRunner{fakeRunner: fr, panicPID: first.Builder.PID}

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(previous)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick = %v, want nil: one binding's panic must not fail the tick", err)
	}
	if !strings.Contains(logged.String(), "reconcile panicked") {
		t.Errorf("no 'reconcile panicked' error line:\n%s", logged.String())
	}

	// The healthy binding was reconciled in that same tick and its process is
	// untouched: the panic ended nothing but the panicking binding's tick.
	after, err := rt.Store.Load("other")
	if err != nil {
		t.Fatalf("Load other after tick: %v", err)
	}
	if after.Builder.PID != other.Builder.PID {
		t.Errorf("other.Builder.PID = %d, want it left at %d", after.Builder.PID, other.Builder.PID)
	}
	if after.State != store.StateActive {
		t.Errorf("other.State = %s, want active", after.State)
	}
}

// TestTickBacksOffAfterAFailedReleaseFetch pins §4.10's backoff: one failed
// fetch stops the daemon asking again for an hour, and once the hour has
// passed it asks again.
//
// Mutation: drop the `now().Before(d.releaseRetryAt)` early return and the
// second Tick fetches, so calls reaches 2 early.
func TestTickBacksOffAfterAFailedReleaseFetch(t *testing.T) {
	releaseStateRoot(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	ff := &fakeFetcher{err: errors.New("504 gateway timeout")}
	rt, _ := sentBinding(t)
	rt.Fetcher = ff
	rt.Now = func() time.Time { return now }

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if ff.calls != 1 {
		t.Fatalf("fetch calls = %d, want 1 on the failing tick", ff.calls)
	}

	now = now.Add(30 * time.Minute)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (inside the backoff): %v", err)
	}
	if ff.calls != 1 {
		t.Errorf("fetch calls = %d, want 1 inside the backoff window", ff.calls)
	}

	now = now.Add(31 * time.Minute) // 61 minutes after the failure
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (after the backoff): %v", err)
	}
	if ff.calls != 2 {
		t.Errorf("fetch calls = %d, want 2 once the hour has passed", ff.calls)
	}
}

// TestRefreshReleaseSuccessClearsTheBackoff pins the other half of §4.10's
// contract: a successful save clears the retry deadline, so the next failure
// backs off from its own moment.
func TestRefreshReleaseSuccessClearsTheBackoff(t *testing.T) {
	root := releaseStateRoot(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	ff := &fakeFetcher{err: errors.New("network is unreachable")}
	rt, _ := sentBinding(t)
	rt.Fetcher = ff
	rt.Now = func() time.Time { return now }

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if d.releaseRetryAt.IsZero() {
		t.Fatal("a failed fetch must set the retry deadline")
	}

	now = now.Add(releaseRetryAfter + time.Minute)
	ff.err = nil
	ff.tag = "v0.9.0"
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (after the backoff): %v", err)
	}
	if !d.releaseRetryAt.IsZero() {
		t.Errorf("releaseRetryAt = %v after a successful fetch, want the zero time", d.releaseRetryAt)
	}
	cached, ok, err := release.Load(root)
	if err != nil || !ok {
		t.Fatalf("release.Load = (ok %v, err %v), want the fetched answer saved", ok, err)
	}
	if cached.Latest != "v0.9.0" {
		t.Errorf("cache latest = %q, want v0.9.0", cached.Latest)
	}
}

// TestBackfillLeavesDoneBindingsAlone pins the DONE guard in
// backfillPlannerID: a finished binding is history, and a tick must not
// rewrite it even when its planner session now has a record.
func TestBackfillLeavesDoneBindingsAlone(t *testing.T) {
	reg := &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}
	if _, err := reg.Create(planner.Record{
		ID:          "pl_aaaaaaaacccc",
		Name:        "architect-1",
		HarnessKind: "claude",
		SessionID:   "sess-done",
		CWD:         "/repo",
	}); err != nil {
		t.Fatalf("create planner record: %v", err)
	}
	b := store.Binding{Name: "old", State: store.StateDone}
	b.Planner.Kind, b.Planner.SessionID = "claude", "sess-done"

	if got := backfillPlannerID(Runtime{Planners: reg}, b); got.PlannerID != "" {
		t.Errorf("DONE binding back-filled with %q; history must not be rewritten", got.PlannerID)
	}
	b.State = store.StateActive
	if got := backfillPlannerID(Runtime{Planners: reg}, b); got.PlannerID != "pl_aaaaaaaacccc" {
		t.Errorf("ACTIVE binding PlannerID = %q, want pl_aaaaaaaacccc", got.PlannerID)
	}
}

// TestBackfillLeavesPlannerlessBindingsAlone pins the Planner.SessionID guard
// in backfillPlannerID: a non-DONE binding with no id and an empty
// Planner.SessionID -- the shape a remote binding written before the add
// fix has -- names no session for the registry to look up, so a tick leaves it
// exactly as it was. relevo never guesses a planner for it.
func TestBackfillLeavesPlannerlessBindingsAlone(t *testing.T) {
	reg := &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}
	if _, err := reg.Create(planner.Record{
		ID:          "pl_aaaaaaaacccc",
		Name:        "architect-1",
		HarnessKind: "claude",
		SessionID:   "sess-remote",
		CWD:         "/repo",
	}); err != nil {
		t.Fatalf("create planner record: %v", err)
	}

	b := store.Binding{Name: "api", State: store.StateActive}
	if got := backfillPlannerID(Runtime{Planners: reg}, b); got.PlannerID != "" {
		t.Errorf("plannerless binding back-filled with %q; nothing names its planner", got.PlannerID)
	}
}

// TestTickSkipsANewerFormatBinding pins #372 R1's daemon rule: a binding
// written by a newer relevo is left alone -- no reconcile, no save -- so its
// file keeps every field this binary cannot understand.
//
// The fixture's planner session names newRuntime's seeded record, so an
// unguarded tick would back-fill PlannerID and save, changing the bytes.
func TestTickSkipsANewerFormatBinding(t *testing.T) {
	rt := newRuntime(t)
	fr := rt.Runner.(*fakeRunner)

	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	b := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive,
		Planner: store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		Format:  store.BindingFormat + 1,
	}
	if err := os.MkdirAll(rt.Store.Dir(b.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"format": 2`)) {
		t.Fatalf("the fixture must carry format 2, got:\n%s", raw)
	}
	path := filepath.Join(rt.Store.Dir(b.Name), "bind.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Errorf("Tick rewrote a newer-format binding:\nbefore:\n%s\nafter:\n%s", raw, after)
	}
	if len(fr.specs) != 0 {
		t.Errorf("Tick started %d processes for a newer-format binding", len(fr.specs))
	}

	// The skip must be visible: a silent skip would leave a human with no
	// reason why the binding stopped moving. Dropping the tickOne check
	// leaves the warning unlogged, so this is what the guard's own mutation
	// check catches.
	wanted := "binding webshop is format 2; this relevo knows 1; leaving it to a newer relevo"
	for _, r := range h.snapshot() {
		if r.Message == wanted {
			return
		}
	}
	t.Errorf("Tick did not log %q; captured %d records", wanted, len(h.snapshot()))
}
