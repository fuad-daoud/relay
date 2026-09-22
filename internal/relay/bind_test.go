package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

// baseTime is the instant newRuntime's fixed clock reports.
var baseTime = time.Unix(1757000000, 0).UTC()

func newRuntime(t *testing.T, f *fakePanes) Runtime {
	t.Helper()
	return Runtime{
		// SPIKE(#303): every builder is headless now, so every test runtime
		// needs a Runner.
		Runner:           newFakeRunner(),
		Store:            store.New(t.TempDir()),
		Candidates:       candidateSet(t, testCandidatesJSON),
		LedgerPath:       filepath.Join(t.TempDir(), "ledger.json"),
		AvailabilityPath: filepath.Join(t.TempDir(), "availability.json"),
		Now:              func() time.Time { return baseTime },
	}
}

func plannerAgent() stubAgent {
	return stubAgent{
		Kind: "claude", Status: stubWorking, CWD: "/repo",
		PaneID: "w2:p3", Session: stubSession{Value: "planner-sess"},
	}
}

// TestBindRepoFactsFailureIsNil pins that a git failure never fails a bind:
// captureRepo swallows it and RepoRef stays nil.
func TestBindRepoFactsFailureIsNil(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Git = &fakeGit{repoFactsErr: errors.New("boom")}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.RepoRef != nil {
		t.Errorf("RepoRef = %+v, want nil when RepoFacts errors", b.RepoRef)
	}
}

// TestBindRejectsBadFeature pins that a bad --feature is refused before
// anything is spawned, with the same error store.ValidFeature reports.
func TestBindRejectsBadFeature(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Feature: "a/b",
	})
	if err == nil || !strings.Contains(err.Error(), "feature:") {
		t.Fatalf("Bind err = %v, want one containing %q", err, "feature:")
	}
	if len(f.starts) != 0 {
		t.Errorf("a rejected feature must spawn no builder, got %d starts", len(f.starts))
	}
}

func TestBindRefusesACandidateThatDoesNotServeBuilder(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"test","model":"m","roles":["reviewer"]}]`)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testClaudeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})

	if !errors.Is(err, ErrRoleNotServed) {
		t.Fatalf("want ErrRoleNotServed, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
	// Also assert no pane was SPLIT. Checking only f.starts cannot tell a
	// refusal that happened before anything was created from one that ran after
	// builderPane and left a pane behind with nothing pointing at it -- the
	// ~800 MB leak CLAUDE.md warns about. This is what pins the refusal's
	// placement ahead of builderPane.
	if len(f.tabs) != 0 {
		t.Errorf("created %d tabs; a refused bind must not create a pane it then abandons", len(f.tabs))
	}
}

func TestBindSpawnUnknownAliasFails(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/test/nope", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, candidate.ErrUnknownCandidate) {
		t.Fatalf("got %v, want ErrUnknownCandidate", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("an unknown alias must not start an agent, got %+v", f.starts)
	}
}

func TestBindRefusesAnAmbiguousCandidate(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrAmbiguousCandidate) {
		t.Fatalf("want ErrAmbiguousCandidate, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
	if len(f.tabs) != 0 {
		t.Errorf("created %d tabs; a refused bind must not create a pane it then abandons", len(f.tabs))
	}
}

func TestBindWithNoCandidatesSaysSo(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, "[]")

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("want ErrNoCandidates, got %v", err)
	}
}

func TestResumeWithoutABuilderDoesNotSpawn(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/test/m", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("seed Bind: %v", err)
	}
	f.starts = nil

	_, err = Bind(context.Background(), rt, BindOptions{
		Name: b.Name, Resume: true, Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("resume without candidate must not spawn, got starts = %+v", f.starts)
	}
}

func TestBindRefusesSecondBindingOnSameTree(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	opts := BindOptions{Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo"}
	if _, err := Bind(context.Background(), rt, opts); err != nil {
		t.Fatalf("first Bind: %v", err)
	}

	opts.Name = "webshop2"
	_, err := Bind(context.Background(), rt, opts)
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

// TestResumeKeepsExistingLocatorWhenAlreadySet is the other half of the
// refresh rule: when the binding already has a TranscriptLocator, resume
// must not overwrite it with whatever rt.Sessions resolves for the new
// planner pane's session.
func TestResumeKeepsExistingLocatorWhenAlreadySet(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateOrphaned,
		Planner: store.Endpoint{PaneID: "w1:p1", TranscriptLocator: "/already/set.jsonl"},
		Builder: store.Endpoint{AgentName: "webshop-builder", PaneID: "w1:p2", Kind: "opencode"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	rt.Sessions = func(kind, sessionID string) (string, bool) {
		return "/would/overwrite.jsonl", true
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if got.Planner.TranscriptLocator != "/already/set.jsonl" {
		t.Errorf("Planner.TranscriptLocator = %q, want the existing value kept, not re-resolved", got.Planner.TranscriptLocator)
	}
}

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
func TestBindRefusesExistingName(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 4, State: store.StateDone,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("binding an existing name must be refused")
	}

	// The refusal has to happen before anything is spawned, or it strands a
	// live builder pane with nothing pointing at it.
	if len(f.starts) != 0 {
		t.Errorf("no agent may be started, got %+v", f.starts)
	}
	if len(f.tabs) != 0 {
		t.Errorf("no tab may be created, got %d", len(f.tabs))
	}

	if !strings.Contains(err.Error(), "relay unbind webshop") {
		t.Errorf("error must name the unbind exit, got %q", err)
	}
	if !strings.Contains(err.Error(), "--resume") {
		t.Errorf("error must name the resume exit, got %q", err)
	}

	// The existing binding must be untouched by the refusal.
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Round != 4 || got.Builder.PaneID != "w1:p2" {
		t.Errorf("refused bind must not rewrite the binding, got %+v", got)
	}
}

// TestBindResumeStillAdoptsAnExistingName guards the exit the refusal offers.
func TestBindResumeStillAdoptsAnExistingName(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 4, State: store.StateOrphaned,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume must still adopt an existing binding: %v", err)
	}
	if got.Round != 4 || got.Planner.PaneID != "w2:p3" || got.State != store.StateActive {
		t.Errorf("resume = %+v", got)
	}
	if len(f.starts) != 0 {
		t.Errorf("resume must not start an agent, got %+v", f.starts)
	}
}

func TestBindResumeDoneBindingScope(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateDone,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2", SessionID: "builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	t.Run("planner-only resume of DONE binding succeeds", func(t *testing.T) {
		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("planner-only resume on done binding must succeed: %v", err)
		}
		if got.State != store.StateActive {
			t.Errorf("state = %s, want active", got.State)
		}
		if got.Planner.PaneID != "w2:p3" {
			t.Errorf("Planner.PaneID = %q, want w2:p3", got.Planner.PaneID)
		}
		if got.Builder != existing.Builder {
			t.Errorf("Builder = %+v, want %+v (untouched)", got.Builder, existing.Builder)
		}
		if len(f.tabs) != 0 || len(f.starts) != 0 {
			t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
		}
	})

	t.Run("rebind of DONE binding is refused", func(t *testing.T) {
		// Reset state to Done for this subtest
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("reset existing binding: %v", err)
		}
		_, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err == nil {
			t.Fatal("rebind on done binding must be refused")
		}
		wantMsg := `binding "webshop" is done: ` + "`relay bind` to start fresh"
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("error = %q, want containing %q", err.Error(), wantMsg)
		}
		if len(f.tabs) != 0 || len(f.starts) != 0 {
			t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
		}
	})
}

func TestResumeRefusesRestoreWithoutBranch(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "no branch is recorded") {
		t.Fatalf("err = %v, want containing 'no branch is recorded'", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateDone {
		t.Errorf("loaded.State = %s, want done (unchanged)", loaded.State)
	}
}

func TestResumeRefusesRestoreFromWrongRepo(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{} // branchExists stays false: the caller's cwd has no relay/webshop
	rt.Git = fg

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt,
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/elsewhere",
	})
	if err == nil || !strings.Contains(err.Error(), "run resume from the repository") {
		t.Fatalf("err = %v, want 'run resume from the repository'", err)
	}
	if fg.lastBranchDir != "/elsewhere" || fg.lastBranchName != "relay/webshop" {
		t.Errorf("BranchExists asked (%q, %q), want (/elsewhere, relay/webshop)", fg.lastBranchDir, fg.lastBranchName)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateDone {
		t.Errorf("state = %s, want done (unchanged)", loaded.State)
	}
}

func TestResumeSurfacesBranchCheckedOut(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{checkoutWorktreeErr: git.ErrBranchCheckedOut}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "git worktree list") {
		t.Fatalf("err = %v, want containing 'git worktree list'", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs=%d starts=%+v, want none", len(f.tabs), f.starts)
	}
}

func TestResumePresentWorktreeIsNotRestored(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := t.TempDir()
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateActive,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	if res.RestoredWorktree != "" {
		t.Errorf("RestoredWorktree = %q, want empty", res.RestoredWorktree)
	}
}

func TestBindRebindNotFound(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got err = %v, want store.ErrNotFound", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
	}
}

func TestBindTimeoutOverrideAndDefault(t *testing.T) {
	t.Run("override is stored", func(t *testing.T) {
		f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
			RoundTimeout: 90 * time.Minute,
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if got, want := b.RoundTimeoutMS, int((90 * time.Minute).Milliseconds()); got != want {
			t.Errorf("RoundTimeoutMS = %d, want %d", got, want)
		}
	})

	t.Run("default is a day, not half an hour", func(t *testing.T) {
		f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "kobe", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo2",
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		// A builder working a real stage runs for hours; 30m flagged healthy
		// work as needing a human on the first live run.
		if got, want := b.RoundTimeoutMS, int((24 * time.Hour).Milliseconds()); got != want {
			t.Errorf("default RoundTimeoutMS = %d, want %d (24h)", got, want)
		}
	})
}

func TestUnbindTeardown(t *testing.T) {
	ctx := context.Background()

	t.Run("ordinary binding unbinds with all-zero result", func(t *testing.T) {
		f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
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
		f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
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
		f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
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
		f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
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
		f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
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
		f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
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
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
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

// A builder with a recorded session is unambiguous: if no live agent carries
// that session it really is gone, so the gate must not fire.
func TestBindResumeUnaffectedWhenSessionRecorded(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy", SessionID: "dead-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
}

// A builder with an agent name is unambiguous: it can be identified by name, so
// the gate must not fire even when SessionID is empty and a round was open.
func TestBindResumeUnaffectedWhenNamedWithoutSession(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:           "webshop",
		CWD:            "/repo",
		Round:          3,
		State:          store.StateBroken,
		Planner:        store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:        store.Endpoint{PaneID: "w2:p4", Kind: "agy", AgentName: "webshop-builder"},
		RoundStartedAt: time.Now().UTC(),
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
}

func TestResumePlannerOnlyPreservesRoundClosedTree(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{
		{Kind: "claude", Status: stubWorking, CWD: "/repo",
			PaneID: "w7:pB", Session: stubSession{Value: "planner-sess-2"}},
	}}
	rt := newRuntime(t, f)

	const closedTree = "tree-closed-123"
	existing := store.Binding{
		Name:            "webshop",
		CWD:             "/repo",
		Round:           3,
		RoundClosedTree: closedTree,
		State:           store.StateOrphaned,
		Planner:         store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:         store.Endpoint{PaneID: "w2:p4", SessionID: "builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w7:pB", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind resume planner only: %v", err)
	}
	if got.RoundClosedTree != closedTree {
		t.Errorf("returned RoundClosedTree = %q, want %q", got.RoundClosedTree, closedTree)
	}

	saved, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.RoundClosedTree != closedTree {
		t.Errorf("loaded RoundClosedTree = %q, want %q", saved.RoundClosedTree, closedTree)
	}
}

func TestBindHeadlessRecordsAnEndpointAndSpawnsNothing(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Fatalf("headless must open no tab and start no agent: tabs=%d starts=%d", len(f.tabs), len(f.starts))
	}
	ep := b.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.AgentName != "webshop-builder" || ep.Kind != "opencode" {
		t.Errorf("AgentName/Kind = %q/%q, want webshop-builder/opencode", ep.AgentName, ep.Kind)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("spec §3.1 invariants broken: %+v", ep)
	}
	if b.BuilderCandidate != testOpencodeRef || res.Token() != testOpencodeRef {
		t.Errorf("candidate = %q / %q, want %q", b.BuilderCandidate, res.Token(), testOpencodeRef)
	}
	if b.Round != 1 || b.State != store.StateActive {
		t.Errorf("round/state = %d/%s, want 1/active", b.Round, b.State)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil || len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("a fresh headless binding logs its pick and nothing else: %+v (%v)", entries, err)
	}
	// The stored binding reads back headless too.
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

// A binding's mode is fixed at creation. Rebinding a headless binding whose
// process is gone must produce another headless endpoint, not a pane (#119).
func TestBindResumeRebindKeepsAHeadlessBindingHeadless(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p9"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, false) // the old process is gone
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Fatalf("a headless rebind must open no tab and start no agent: tabs=%+v starts=%+v", f.tabs, f.starts)
	}
	ep := got.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("rebound endpoint must be a fresh headless endpoint with nothing running: %+v", ep)
	}
	if got.BuilderCandidate != testAgyRef || res.How != HowOrder {
		t.Errorf("candidate = %q (%+v), want the order's first, %s", got.BuilderCandidate, res, testAgyRef)
	}
	if got.State != store.StateActive || got.Round != 4 {
		t.Errorf("state/round = %s/%d, want active/4", got.State, got.Round)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

// herdr's agent list cannot see a process, so a headless binding's liveness
// is the Runner's answer. A live process refuses the rebind (§4.3).
func TestBindResumeRebindRefusesALiveHeadlessProcess(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p9"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, true) // still running
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("err = %v, want ErrBuilderAlive", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("a refused rebind must spawn nothing: tabs=%+v starts=%+v", f.tabs, f.starts)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || stored.Builder.PID != 4321 || !stored.Builder.Headless() {
		t.Errorf("a refused rebind must leave the binding untouched: %+v (%v)", stored.Builder, err)
	}
}

func TestBindWithTierYoloWithoutAllowYoloRefused(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testClaudeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Tier:        "yolo",
	})
	if !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("err = %v, want ErrTierAboveMax", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs = %d, starts = %d; want 0", len(f.tabs), len(f.starts))
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", loadErr)
	}
}

// TestBindGateFlagStored pins #132: an explicit --gate is stored on the
// binding as given.
func TestBindGateFlagStored(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		Gate: "make check",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("b.Gate = %q, want %q", b.Gate, "make check")
	}
}

// TestBindGatePolicyDefaultApplied pins #132: with no --gate, policy.json's
// gate.default is used.
func TestBindGatePolicyDefaultApplied(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("b.Gate = %q, want the policy default %q", b.Gate, "make check")
	}
}

// TestBindNoGateOverridesPolicyDefault pins #132: --no-gate opts a binding
// out of policy.json's gate.default.
func TestBindNoGateOverridesPolicyDefault(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		NoGate: true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "" {
		t.Errorf("b.Gate = %q, want empty despite the policy default", b.Gate)
	}
}

// TestForkInheritsSourceGate pins #132: a fork with no --gate/--no-gate
// inherits the source binding's Gate.
func TestForkInheritsSourceGate(t *testing.T) {
	ctx := context.Background()
	fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	src, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Gate = "make check"
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Gate != "make check" {
		t.Errorf("Binding.Gate = %q, want inherited from source", res.Binding.Gate)
	}

	stored, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Gate != "make check" {
		t.Errorf("stored Gate = %q, want inherited from source", stored.Gate)
	}
}

// TestBindRegateFlagStored pins #132 part 2: an explicit --regate is stored on
// the binding as given.
func TestBindRegateFlagStored(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		Regate: ptr(3),
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 3 {
		t.Errorf("b.Regate = %d, want 3", b.Regate)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Regate != 3 {
		t.Errorf("stored Regate = %d, want 3", stored.Regate)
	}
}

// TestBindRegatePolicyDefaultApplied pins #132 part 2: with no --regate,
// policy.json's gate.regate becomes the binding's budget.
func TestBindRegatePolicyDefaultApplied(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Regate: ptr(2)}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 2 {
		t.Errorf("b.Regate = %d, want the policy default 2", b.Regate)
	}
}

// TestBindRegateFlagOverridesPolicy pins #132 part 2: an explicit --regate 0
// turns the policy default off for this binding.
func TestBindRegateFlagOverridesPolicy(t *testing.T) {
	f := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Regate: ptr(2)}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		Regate: ptr(0),
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 0 {
		t.Errorf("b.Regate = %d, want 0 despite the policy default", b.Regate)
	}
}

// TestForkInheritsSourceRegate pins #132 part 2: a fork with no --regate
// inherits the source binding's budget.
func TestForkInheritsSourceRegate(t *testing.T) {
	ctx := context.Background()
	fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	src, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Regate = 2
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Regate != 2 {
		t.Errorf("Binding.Regate = %d, want inherited from source", res.Binding.Regate)
	}

	stored, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Regate != 2 {
		t.Errorf("stored Regate = %d, want inherited from source", stored.Regate)
	}
}

// TestForkRegateFlagOverridesSource pins #132 part 2: an explicit --regate on
// a fork wins over the source's budget.
func TestForkRegateFlagOverridesSource(t *testing.T) {
	ctx := context.Background()
	fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	src, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Regate = 2
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
		Regate:      ptr(0),
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Regate != 0 {
		t.Errorf("Binding.Regate = %d, want the explicit 0", res.Binding.Regate)
	}
}
