package relay

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/planner"
	"github.com/fuad-daoud/relay/internal/store"
)

// baseTime is the instant newRuntime's fixed clock reports.
var baseTime = time.Unix(1757000000, 0).UTC()

// newRuntime builds a Runtime for tests: a temp store, the test candidate
// set, a fake runner and a fixed clock. No harness is faked: a local builder
// is a process relay runs, and the tests drive it through fakeRunner.
func newRuntime(t *testing.T) Runtime {
	t.Helper()
	return Runtime{
		Store:            store.New(t.TempDir()),
		Candidates:       candidateSet(t, testCandidatesJSON),
		LedgerPath:       filepath.Join(t.TempDir(), "ledger.json"),
		AvailabilityPath: filepath.Join(t.TempDir(), "availability.json"),
		Now:              func() time.Time { return baseTime },
		// Every local builder is headless since #303, so every Send needs a
		// Runner. A test that wants "no runner" sets rt.Runner = nil.
		Runner: newFakeRunner(),
	}
}

// TestBindRecordsRepoFeatureAndLocator pins #172: a fresh bind captures the
// git repo identity (normalised), the human-given --feature label, and the
// planner's own transcript file path (via rt.Sessions), and stamps CreatedAt.
// TestBindRepoFactsFailureIsNil pins that a git failure never fails a bind:
// captureRepo swallows it and RepoRef stays nil.
// testPlannerRegistry seeds a registry holding rec and returns it with the
// record as the registry stamped it (created_at and seen_at filled in).
func testPlannerRegistry(t *testing.T, rec planner.Record) (*planner.FileRegistry, planner.Record) {
	t.Helper()
	reg := &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}
	created, err := reg.Create(rec)
	if err != nil {
		t.Fatalf("create planner record: %v", err)
	}
	return reg, created
}

// TestBindRecordsPlannerFromRegistry is the plan's required case (§3.2,
// §5.3): with a registry configured, Binding.PlannerID, Planner.Kind and
// Planner.SessionID come from the record -- not from the herdr agent list --
// while Planner.PaneID comes from the pane env the caller passes.
// TestBindNoPlannerIsHardError is the plan's required case for §4.3: with a
// registry configured and nothing resolving -- no --planner, no
// $RELAY_PLANNER, no host and no detectable session -- a verb fails with
// exactly the CLI's no-planner line. The old "no planner pane" error is gone.
// TestBindRejectsBadFeature pins that a bad --feature is refused before
// anything is spawned, with the same error store.ValidFeature reports.
// TestBindRefusesABuilderNameHerdrWouldRefuse pins #64: a 25-character binding
// name passes the store's own limit but builds a 33-character agent name, and
// Bind must refuse it before any pane is split, any agent started or any
// binding saved.
// slicesContains reports whether want is one of args.
func slicesContains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// launchArgs renders the headless launch for b's builder candidate and tier,
// the way Send would, so a test can assert the argv a bind resolved to.
func launchArgs(t *testing.T, rt Runtime, b store.Binding, tier harness.Tier) []string {
	t.Helper()
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		t.Fatalf("builder candidate %q: %v", b.BuilderCandidate, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		t.Fatalf("lookup %q: %v", ref, err)
	}
	role, _ := harness.RoleByName("builder")
	argv, err := headlessLaunch(c, role, tier, 0, "", b.CWD, rt.Store.Dir(b.Name))
	if err != nil {
		t.Fatalf("headlessLaunch: %v", err)
	}
	return argv
}

// TestResumeKeepsFieldsAndRefreshesLocator pins the resume rule for #172's
// new fields: a planner-only resume leaves RepoRef and Feature exactly as
// the binding already had them, and refreshes Planner.TranscriptLocator
// (which endpointOf wipes along with the rest of the old Planner endpoint)
// since it was previously empty.
// TestResumeKeepsExistingLocatorWhenAlreadySet is the other half of the
// refresh rule: when the binding already has a TranscriptLocator, resume
// must not overwrite it with whatever rt.Sessions resolves for the new
// planner pane's session.
func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"webshop":    "webshop",
		"money/ai":   "money-ai",
		"My.Repo":    "my-repo",
		"2024-thing": "b2024-thing",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBindRefusesExistingName is the regression test for a silently broken
// second session: Save only rewrites bind.json, so the previous session's
// log.jsonl and NNN-*.md files survive and a fresh round 1 collides with the
// old round 1. Reconcile then reads the old report entry as "already handled"
// and the binding stalls with no error and no notification.
// TestBindResumeStillAdoptsAnExistingName guards the exit the refusal offers.
// TestResumePausedRestoresAndRebinds pins #137: resuming a PAUSED binding
// restores the released worktree and rebinds a fresh builder even though the
// caller passed neither --rebind nor --builder, because a paused binding has
// no builder identity left to keep.
func TestUnbindTeardown(t *testing.T) {
	ctx := context.Background()

	t.Run("ordinary binding unbinds with all-zero result", func(t *testing.T) {
		rt := newRuntime(t)
		b := store.Binding{
			Name: "webshop", CWD: "/repo", State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "webshop", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.ArchivedTo != "" || res.WorktreeRemoved != "" || res.WorktreeKept != "" || res.KeptReason != "" {
			t.Errorf("expected all-zero result for ordinary binding unbind, got %+v", res)
		}
		if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("binding state still exists after Unbind: %v", err)
		}
	})

	t.Run("clean worktree is removed", func(t *testing.T) {
		fg := &fakeGit{}
		rt := newRuntime(t)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-clean", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-clean", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != wt {
			t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
		}
		if res.WorktreeKept != "" {
			t.Errorf("WorktreeKept = %q, want empty", res.WorktreeKept)
		}
		if len(fg.removeWorktreeCalls) != 1 {
			t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
		}
		if fg.removeWorktreeCalls[0].Force {
			t.Error("teardown must pass force: false")
		}
		if _, err := rt.Store.Load("fork-clean"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should be removed")
		}
	})

	t.Run("dirty worktree is kept and says why", func(t *testing.T) {
		fg := &fakeGit{dirtyResult: true}
		rt := newRuntime(t)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-dirty", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-dirty", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != "" {
			t.Errorf("WorktreeRemoved = %q, want empty", res.WorktreeRemoved)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "uncommitted changes" {
			t.Errorf("KeptReason = %q, want 'uncommitted changes'", res.KeptReason)
		}
		if len(fg.removeWorktreeCalls) != 0 {
			t.Error("RemoveWorktree must NOT be called for dirty tree")
		}
		if _, err := rt.Store.Load("fork-dirty"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})

	t.Run("dirty check error keeps worktree with honest reason", func(t *testing.T) {
		fg := &fakeGit{dirtyErr: errors.New("git lock busy\ndetails")}
		rt := newRuntime(t)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-dirty-err", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-dirty-err", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "dirty check failed: git lock busy" {
			t.Errorf("KeptReason = %q, want 'dirty check failed: git lock busy'", res.KeptReason)
		}
		if len(fg.removeWorktreeCalls) != 0 {
			t.Errorf("RemoveWorktree should not be called on dirty check error, got %d calls", len(fg.removeWorktreeCalls))
		}
	})

	t.Run("git unavailable keeps worktree", func(t *testing.T) {
		rt := newRuntime(t)
		rt.Git = nil

		b := store.Binding{
			Name: "fork-nogit", CWD: "/state/.worktrees/fork-nogit",
			Worktree: "/state/.worktrees/fork-nogit", State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-nogit", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != "/state/.worktrees/fork-nogit" || res.KeptReason != "git unavailable" {
			t.Errorf("kept mismatch: %+v", res)
		}
		if _, err := rt.Store.Load("fork-nogit"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})

	t.Run("git remove failure keeps worktree and completes unbind", func(t *testing.T) {
		fg := &fakeGit{removeWorktreeErr: errors.New("git lock locked\ndetails")}
		rt := newRuntime(t)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-fail", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-fail", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "git lock locked" {
			t.Errorf("KeptReason = %q, want 'git lock locked' (brief)", res.KeptReason)
		}
		if _, err := rt.Store.Load("fork-fail"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})
}

func TestUnbindReportsAnAlreadyGoneWorktree(t *testing.T) {
	ctx := context.Background()
	fg := &fakeGit{}
	rt := newRuntime(t)
	rt.Git = fg

	missingWT := filepath.Join(t.TempDir(), "already-gone-worktree")
	b := store.Binding{
		Name: "fork-gone", CWD: "/repo",
		Worktree: missingWT, State: store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Unbind(ctx, rt, "fork-gone", false)
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if res.WorktreeGone != missingWT {
		t.Errorf("WorktreeGone = %q, want %q", res.WorktreeGone, missingWT)
	}
	if res.WorktreeKept != "" || res.WorktreeRemoved != "" {
		t.Errorf("kept=%q removed=%q, want both empty", res.WorktreeKept, res.WorktreeRemoved)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("removeWorktreeCalls = %d, want 0", len(fg.removeWorktreeCalls))
	}
	if _, err := rt.Store.Load("fork-gone"); !errors.Is(err, store.ErrNotFound) {
		t.Error("binding state should still be deleted")
	}
}

// A session-less builder that cannot be located may be dead or may be alive in
// a pane that moved workspaces. relay cannot tell, so it must not spawn a
// replacement on the guess -- that is how a live builder gets orphaned.
// A builder with a recorded session is unambiguous: if no live agent carries
// that session it really is gone, so the gate must not fire.
// A builder with an agent name is unambiguous: it can be identified by name, so
// the gate must not fire even when SessionID is empty and a round was open.
// --assume-dead releases only the unverifiable case. A builder relay can
// positively see is alive is still refused: that is #20's guarantee.
// #20's recovery (PR #22): a session-less builder with no round in flight is
// unambiguous enough to rebind without ceremony. The gate must not broaden to
// catch this case -- if it ever does, this test fails loudly.
// A binding's mode is fixed at creation. Rebinding a headless binding whose
// process is gone must produce another headless endpoint, not a pane (#119).
// herdr's agent list cannot see a process, so a headless binding's liveness
// is the Runner's answer. A live process refuses the rebind (§4.3).
// A pane cannot replace a process builder; the mode is fixed at creation.
// TestBindGateFlagStored pins #132: an explicit --gate is stored on the
// binding as given.
// TestBindGatePolicyDefaultApplied pins #132: with no --gate, policy.json's
// gate.default is used.
// TestBindNoGateOverridesPolicyDefault pins #132: --no-gate opts a binding
// out of policy.json's gate.default.
// TestForkInheritsSourceGate pins #132: a fork with no --gate/--no-gate
// inherits the source binding's Gate.
// TestBindRegateFlagStored pins #132 part 2: an explicit --regate is stored on
// the binding as given.
// TestBindRegatePolicyDefaultApplied pins #132 part 2: with no --regate,
// policy.json's gate.regate becomes the binding's budget.
// TestBindRegateFlagOverridesPolicy pins #132 part 2: an explicit --regate 0
// turns the policy default off for this binding.
// TestForkInheritsSourceRegate pins #132 part 2: a fork with no --regate
// inherits the source binding's budget.
// TestForkRegateFlagOverridesSource pins #132 part 2: an explicit --regate on
// a fork wins over the source's budget.
