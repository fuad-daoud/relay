package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/store"
)

type recordHookDispatcher struct {
	events []hooks.Event
}

func (r *recordHookDispatcher) Dispatch(ctx context.Context, event hooks.Event) {
	r.events = append(r.events, event)
}

func newForkRuntime(t *testing.T, f *fakePanes, fg *fakeGit, hd hooks.Dispatcher) Runtime {
	t.Helper()
	var g Git
	if fg != nil {
		g = fg
	}
	return Runtime{
		Git:              g,
		Store:            store.New(t.TempDir()),
		Candidates:       candidateSet(t, testCandidatesJSON),
		LedgerPath:       filepath.Join(t.TempDir(), "ledger.json"),
		AvailabilityPath: filepath.Join(t.TempDir(), "availability.json"),
		Now:              func() time.Time { return baseTime },
		Hooks:            hd,
	}
}

func seedFourRoundBinding(t *testing.T, rt Runtime, name, cwd string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:             name,
		CWD:              cwd,
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: name + "-builder", PaneID: "w2:p4", Kind: "opencode"},
		BuilderCandidate: testOpencodeRef,
		Round:            4,
		State:            store.StateActive,
		RoundCap:         20,
		RoundTimeoutMS:   1800000,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	srcDir := rt.Store.Dir(name)
	for r := 1; r <= 4; r++ {
		planEntry := store.LogEntry{
			Round:     r,
			Direction: store.DirToBuilder,
			Kind:      store.KindPlan,
			Path:      rt.Store.PlanPath(name, r),
			Confirmed: true,
		}
		if err := rt.Store.AppendLog(name, planEntry); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.Store.PlanPath(name, r), []byte(fmt.Sprintf("plan %d", r)), 0o644); err != nil {
			t.Fatal(err)
		}

		if r < 4 {
			repEntry := store.LogEntry{
				Round:     r,
				Direction: store.DirToPlanner,
				Kind:      store.KindReport,
				Path:      rt.Store.ReportPath(name, r),
				Confirmed: true,
			}
			if err := rt.Store.AppendLog(name, repEntry); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rt.Store.ReportPath(name, r), []byte(fmt.Sprintf("report %d", r)), 0o644); err != nil {
				t.Fatal(err)
			}
			diffPath := filepath.Join(srcDir, fmt.Sprintf("%03d-diff.patch", r))
			if err := os.WriteFile(diffPath, []byte(fmt.Sprintf("diff %d", r)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	loaded, err := rt.Store.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

// TestForkInheritsFeatureAndRecordsParent pins #172's inheritance rule: a
// fork with no --feature inherits the source's, and copies the source's
// RepoRef rather than re-capturing it.
//
// NOTE (round 3, #172): the plan for this test also asked for a check of a
// structured `ForkedFrom{Name: src, Round: 2}` (store.ForkRef) on the
// child. Binding already has a same-named ForkedFrom (string) field plus
// ForkedAtRound (int) -- the existing free-text pair Fork already writes
// and internal/relay/status.go and internal/ui/rail.go read -- so this
// round could not add a second Go field of the same name beside it (see
// the round 3 report for the halt). This test checks the existing pair
// instead, which Fork continues to write unchanged.
func TestForkInheritsFeatureAndRecordsParent(t *testing.T) {
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
	src.Feature = "auth"
	src.RepoRef = &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"}
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

	if res.Binding.Feature != "auth" {
		t.Errorf("Feature = %q, want inherited %q", res.Binding.Feature, "auth")
	}
	if !reflect.DeepEqual(res.Binding.RepoRef, src.RepoRef) {
		t.Errorf("RepoRef = %+v, want the source's %+v", res.Binding.RepoRef, src.RepoRef)
	}
	if res.Binding.ForkedFrom != "source" || res.Binding.ForkedAtRound != 2 {
		t.Errorf("ForkedFrom/ForkedAtRound = %q/%d, want source/2", res.Binding.ForkedFrom, res.Binding.ForkedAtRound)
	}

	altLog, err := rt.Store.ReadLog("alt")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range altLog {
		if e.Kind == store.KindFork && e.Note == "forked from source at round 2" {
			found = true
		}
	}
	if !found {
		t.Errorf("alt log is missing the %q note", "forked from source at round 2")
	}
}

// TestForkFeatureOverride pins that an explicit --feature on fork wins over
// the source binding's.
func TestForkFeatureOverride(t *testing.T) {
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
	src.Feature = "auth"
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
		Feature:     "auth-2",
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Feature != "auth-2" {
		t.Errorf("Feature = %q, want the override %q", res.Binding.Feature, "auth-2")
	}
}

func TestForkRefusalsLeaveNoWorktreeAndNoPane(t *testing.T) {
	ctx := context.Background()

	setup := func(t *testing.T) (*fakePanes, *fakeGit, Runtime, string) {
		fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
		fg := &fakeGit{headCommitID: "commit-123"}
		rt := newForkRuntime(t, fh, fg, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(srcCWD, 0o755); err != nil {
			t.Fatal(err)
		}
		seedFourRoundBinding(t, rt, "source", srcCWD)
		return fh, fg, rt, srcCWD
	}

	t.Run("round 0 out of range", func(t *testing.T) {
		fh, fg, rt, _ := setup(t)
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 0, NewName: "alt", PlannerPane: "w2:p3",
		})
		if !errors.Is(err, ErrRoundOutOfRange) {
			t.Fatalf("got %v, want ErrRoundOutOfRange", err)
		}
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || len(fh.tabs) != 0 {
			t.Error("worktree or pane created on refusal")
		}
	})

	t.Run("round 99 out of range", func(t *testing.T) {
		fh, fg, rt, _ := setup(t)
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 99, NewName: "alt", PlannerPane: "w2:p3",
		})
		if !errors.Is(err, ErrRoundOutOfRange) {
			t.Fatalf("got %v, want ErrRoundOutOfRange", err)
		}
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || len(fh.tabs) != 0 {
			t.Error("worktree or pane created on refusal")
		}
	})

	t.Run("taken binding name", func(t *testing.T) {
		fh, fg, rt, _ := setup(t)
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "source", PlannerPane: "w2:p3",
		})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("got %v, want already exists error", err)
		}
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || len(fh.tabs) != 0 {
			t.Error("worktree or pane created on refusal")
		}
	})

	t.Run("taken working tree", func(t *testing.T) {
		fh, fg, rt, srcCWD := setup(t)
		// Try to fork to the same CWD as source
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3", CWD: srcCWD,
		})
		if !errors.Is(err, store.ErrCWDTaken) {
			t.Fatalf("got %v, want ErrCWDTaken", err)
		}
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || len(fh.tabs) != 0 {
			t.Error("worktree or pane created on refusal")
		}
	})

	t.Run("nil Runtime.Git without CWD", func(t *testing.T) {
		fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
		rt := newForkRuntime(t, fh, nil, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(srcCWD, 0o755)
		seedFourRoundBinding(t, rt, "source", srcCWD)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
		})
		if !errors.Is(err, ErrGitRequired) {
			t.Fatalf("got %v, want ErrGitRequired", err)
		}
		if len(fh.starts) != 0 || len(fh.tabs) != 0 {
			t.Error("pane created on refusal")
		}
	})

	t.Run("branch exists", func(t *testing.T) {
		fh, fg, rt, _ := setup(t)
		fg.branchExists = true
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
		})
		if !errors.Is(err, git.ErrBranchExists) {
			t.Fatalf("got %v, want ErrBranchExists", err)
		}
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || len(fh.tabs) != 0 {
			t.Error("worktree or pane created on refusal")
		}
	})

	t.Run("no builder candidate", func(t *testing.T) {
		_, _, rt, srcCWD := setup(t)
		// Update source to have no builder candidate
		src, _ := rt.Store.Load("source")
		src.BuilderCandidate = ""
		_ = rt.Store.Save(src)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
		})
		if !errors.Is(err, ErrNoBuilderCandidate) {
			t.Fatalf("got %v, want ErrNoBuilderCandidate", err)
		}
		_ = srcCWD
	})

	t.Run("no builder candidate inherits via single candidate", func(t *testing.T) {
		_, _, rt, _ := setup(t)
		rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)
		src, _ := rt.Store.Load("source")
		src.BuilderCandidate = ""
		_ = rt.Store.Save(src)

		res, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
		})
		if err != nil {
			t.Fatalf("Fork: %v", err)
		}
		if res.Binding.BuilderCandidate != "agy/test/m" {
			t.Errorf("BuilderCandidate = %q, want agy/test/m", res.Binding.BuilderCandidate)
		}
	})
}

func TestForkWithCustomCWD(t *testing.T) {
	ctx := context.Background()
	fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	rt := newForkRuntime(t, fh, nil, nil) // nil Git allowed with --cwd
	srcCWD := filepath.Join(t.TempDir(), "repo")
	_ = os.MkdirAll(srcCWD, 0o755)
	seedFourRoundBinding(t, rt, "source", srcCWD)

	customCWD := filepath.Join(t.TempDir(), "custom-cwd")
	_ = os.MkdirAll(customCWD, 0o755)

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
		CWD:         customCWD,
	})
	if err != nil {
		t.Fatalf("Fork with CWD: %v", err)
	}
	if res.Worktree != "" {
		t.Errorf("Worktree = %q, want empty", res.Worktree)
	}
	if res.Branch != "" {
		t.Errorf("Branch = %q, want empty", res.Branch)
	}
	if res.Base != "" {
		t.Errorf("Base = %q, want empty", res.Base)
	}
	if res.Binding.CWD != customCWD {
		t.Errorf("Binding.CWD = %q, want %q", res.Binding.CWD, customCWD)
	}
	if res.Binding.Worktree != "" {
		t.Errorf("Binding.Worktree = %q, want empty", res.Binding.Worktree)
	}
	if res.Binding.Branch != "" || res.Binding.Base != "" {
		t.Errorf("--cwd created no branch, so none may be recorded: (%q, %q)", res.Binding.Branch, res.Binding.Base)
	}
}

func TestForkWriteForkCleanupOnSaveFailure(t *testing.T) {
	fh := &fakePanes{agents: []stubAgent{plannerAgent()}}
	fg := &fakeGit{headCommitID: "commit-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	srcCWD := filepath.Join(t.TempDir(), "repo")
	_ = os.MkdirAll(srcCWD, 0o755)
	seedFourRoundBinding(t, rt, "source", srcCWD)

	dstName := "alt"
	b := store.Binding{
		Name:  dstName,
		CWD:   "", // empty CWD causes tx.Save to fail ("binding has no working directory")
		Round: 3,
		State: store.StateActive,
	}

	ref, err := candidate.ParseRef(testOpencodeRef)
	if err != nil {
		t.Fatal(err)
	}
	oc, err := rt.Candidates.Lookup(ref)
	if err != nil {
		t.Fatal(err)
	}

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		return writeFork(tx, rt.Store, "source", b, 2, time.Now().UTC(),
			pickEntry(time.Now().UTC(), b.Round, "builder", Resolution{How: HowExplicit, Candidate: oc}))
	})
	if err == nil {
		t.Fatal("expected error from writeFork with empty CWD, got nil")
	}
	if !strings.Contains(err.Error(), "binding has no working directory") {
		t.Fatalf("expected error containing 'binding has no working directory', got: %v", err)
	}

	dstDir := rt.Store.Dir(dstName)
	if _, err := os.Stat(dstDir); !os.IsNotExist(err) {
		t.Errorf("destination directory %s should not exist after failed writeFork, got err: %v", dstDir, err)
	}
}

func TestForkHeadlessSpawnsNothing(t *testing.T) {
	fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)

	res, err := Fork(context.Background(), rt, ForkOptions{
		Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Fork --headless: %v", err)
	}
	if len(fh.tabs) != 0 || len(fh.starts) != 0 {
		t.Fatalf("headless fork must open no tab and start no agent: tabs=%d starts=%d", len(fh.tabs), len(fh.starts))
	}
	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("the worktree is still cut: %+v", fg.addWorktreeCalls)
	}
	ep := res.Binding.Builder
	if !ep.Headless() || ep.AgentName != "alt-builder" || ep.PaneID != "" || ep.PID != 0 {
		t.Errorf("builder = %+v, want a headless alt-builder", ep)
	}
	if res.Binding.Round != 3 || res.Binding.ForkedFrom != "source" {
		t.Errorf("fork bookkeeping: round=%d from=%q", res.Binding.Round, res.Binding.ForkedFrom)
	}
	// The source's pane builder is untouched: a fork inherits the candidate,
	// not the mode.
	src, _ := rt.Store.Load("source")
	if src.Builder.Headless() || src.Builder.PaneID != "w2:p4" {
		t.Errorf("source builder changed: %+v", src.Builder)
	}
}

// TestForkRecordsBaseRef pins #136: a fork cut from the source's checkout
// records the branch that checkout (src.Repo) had checked out, so `relay
// land` knows what to rebase the fork's branch onto.
func TestForkRecordsBaseRef(t *testing.T) {
	ctx := context.Background()
	fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123", currentBranchResult: "main"}
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
	src.Repo = "/repo"
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	if res.Binding.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want %q", res.Binding.BaseRef, "main")
	}
	stored, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatal(err)
	}
	if stored.BaseRef != "main" {
		t.Errorf("stored BaseRef = %q, want %q", stored.BaseRef, "main")
	}
	if len(fg.currentBranchCalls) != 1 {
		t.Fatalf("CurrentBranch calls = %+v, want 1", fg.currentBranchCalls)
	}
	if fg.currentBranchCalls[0].Dir != "/repo" {
		t.Errorf("CurrentBranch asked about %q, want the source checkout src.Repo", fg.currentBranchCalls[0].Dir)
	}
}

// TestForkRecordsNoBaseRefForACWDFork pins the escape hatch: a --cwd fork
// cuts nothing, so it records no base ref and land asks for --onto.
func TestForkRecordsNoBaseRefForACWDFork(t *testing.T) {
	ctx := context.Background()
	fh := &fakePanes{agents: []stubAgent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123", currentBranchResult: "main"}
	rt := newForkRuntime(t, fh, fg, nil)

	seedFourRoundBinding(t, rt, "source", t.TempDir())
	cwd := t.TempDir()

	res, err := Fork(ctx, rt, ForkOptions{
		Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3", CWD: cwd,
	})
	if err != nil {
		t.Fatalf("Fork --cwd: %v", err)
	}
	if res.Binding.BaseRef != "" {
		t.Errorf("BaseRef = %q, want \"\" for a --cwd fork", res.Binding.BaseRef)
	}
	if len(fg.currentBranchCalls) != 0 {
		t.Errorf("a --cwd fork cuts nothing, so it must not ask for a branch: %+v", fg.currentBranchCalls)
	}
}
