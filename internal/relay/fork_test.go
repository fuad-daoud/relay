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

// newForkRuntime is newRuntime with the fork tests' Git and hook dispatcher:
// a local builder is headless (#303), so there is no herdr dependency left to
// thread through, and fg may be nil for the tests that prove --cwd needs no
// git.
func newForkRuntime(t *testing.T, fg *fakeGit, hd hooks.Dispatcher) Runtime {
	t.Helper()
	rt := newRuntime(t)
	if fg != nil {
		rt.Git = fg
	}
	rt.Hooks = hd
	return rt
}

func seedFourRoundBinding(t *testing.T, rt Runtime, name, cwd string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:             name,
		CWD:              cwd,
		Planner:          store.Endpoint{Kind: "claude", SessionID: "planner-sess"},
		Builder:          store.Endpoint{AgentName: name + "-builder", Kind: "opencode", Mode: store.ModeHeadless},
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
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fg, nil)

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
		Source:    "source",
		Round:     2,
		NewName:   "alt",
		PlannerID: testPlannerName,
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
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fg, nil)

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
		Source:    "source",
		Round:     2,
		NewName:   "alt",
		PlannerID: testPlannerName,
		Feature:   "auth-2",
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Feature != "auth-2" {
		t.Errorf("Feature = %q, want the override %q", res.Binding.Feature, "auth-2")
	}
}

func TestForkSuccess(t *testing.T) {
	ctx := context.Background()
	fg := &fakeGit{headCommitID: "commit-head-123"}
	hd := &recordHookDispatcher{}
	rt := newForkRuntime(t, fg, hd)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	// Give "source" a Repo distinct from its CWD, so the fork's inheritance
	// of it (#192) is a real assertion rather than a same-value coincidence.
	srcWithRepo, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	srcWithRepo.Repo = "/original/repo"
	if err := rt.Store.Save(srcWithRepo); err != nil {
		t.Fatal(err)
	}
	srcBefore, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	srcLogBefore, err := rt.Store.ReadLog("source")
	if err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:    "source",
		Round:     2,
		NewName:   "alt",
		PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}

	// Result assertions
	if res.Binding.Name != "alt" {
		t.Errorf("Binding.Name = %q, want alt", res.Binding.Name)
	}
	if res.Binding.Round != 3 {
		t.Errorf("Binding.Round = %d, want 3 (round 2 + 1)", res.Binding.Round)
	}
	if res.Binding.State != store.StateActive {
		t.Errorf("Binding.State = %s, want active", res.Binding.State)
	}
	if res.Binding.ForkedFrom != "source" {
		t.Errorf("Binding.ForkedFrom = %q, want source", res.Binding.ForkedFrom)
	}
	if res.Binding.ForkedAtRound != 2 {
		t.Errorf("Binding.ForkedAtRound = %d, want 2", res.Binding.ForkedAtRound)
	}
	expectedWT := rt.Store.WorktreePath("alt")
	if res.Worktree != expectedWT {
		t.Errorf("Worktree = %q, want %q", res.Worktree, expectedWT)
	}
	if res.Binding.Worktree != expectedWT {
		t.Errorf("Binding.Worktree = %q, want %q", res.Binding.Worktree, expectedWT)
	}
	if res.Branch != "relay/alt" {
		t.Errorf("Branch = %q, want relay/alt", res.Branch)
	}
	if res.Base != "commit-head-123" {
		t.Errorf("Base = %q, want commit-head-123", res.Base)
	}
	if res.Binding.Repo != "/original/repo" {
		t.Errorf("Binding.Repo = %q, want inherited from source (#192)", res.Binding.Repo)
	}

	storedFork, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatalf("Load alt: %v", err)
	}
	if storedFork.Branch != "relay/alt" || storedFork.Base != "commit-head-123" {
		t.Errorf("stored branch/base = (%q, %q), want (relay/alt, commit-head-123)", storedFork.Branch, storedFork.Base)
	}

	// Git calls
	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("AddWorktree calls = %d, want 1", len(fg.addWorktreeCalls))
	}
	wtCall := fg.addWorktreeCalls[0]
	if wtCall.Dir != srcCWD || wtCall.Path != expectedWT || wtCall.Branch != "relay/alt" || wtCall.Commit != "commit-head-123" {
		t.Errorf("AddWorktree call mismatch: %+v", wtCall)
	}

	// Builder endpoint is headless (#303): fork records it, it starts nothing.
	alt, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatal(err)
	}
	if !alt.Builder.Headless() || alt.Builder.AgentName != "alt-builder" {
		t.Errorf("fork builder = %+v, want a headless alt-builder endpoint", alt.Builder)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("fork started %d processes, want 0: a fork records a builder, it does not run one", got)
	}

	// Source binding is byte-for-byte unchanged
	srcAfter, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	if !store.SameBinding(srcBefore, srcAfter) {
		t.Errorf("source binding was mutated:\n got %+v\nwant %+v", srcAfter, srcBefore)
	}
	srcLogAfter, err := rt.Store.ReadLog("source")
	if err != nil {
		t.Fatal(err)
	}
	if len(srcLogBefore) != len(srcLogAfter) {
		t.Errorf("source log length changed: %d -> %d", len(srcLogBefore), len(srcLogAfter))
	}

	// Forked history copied through round 2, with KindFork as last entry
	altLog, err := rt.Store.ReadLog("alt")
	if err != nil {
		t.Fatal(err)
	}
	if len(altLog) != 6 { // 2 plans + 2 reports from round 1&2 + 1 KindFork + 1 KindPick
		t.Fatalf("alt log length = %d, want 6", len(altLog))
	}
	forkEntry := altLog[len(altLog)-2]
	if forkEntry.Kind != store.KindFork || forkEntry.Round != 3 || !forkEntry.Confirmed || forkEntry.Direction != store.DirToPlanner {
		t.Errorf("fork log entry mismatch: %+v", forkEntry)
	}
	if forkEntry.Note != "forked from source at round 2" {
		t.Errorf("fork log entry note = %q", forkEntry.Note)
	}
	pickLogEntry := altLog[len(altLog)-1]
	if pickLogEntry.Kind != store.KindPick || pickLogEntry.Round != 3 || !pickLogEntry.Confirmed || pickLogEntry.Direction != store.DirToPlanner {
		t.Errorf("pick log entry mismatch: %+v", pickLogEntry)
	}
	if pickLogEntry.Note != "picked opencode/test/m for builder: explicit, inherited from source, policy bypassed" {
		t.Errorf("pick log entry note = %q", pickLogEntry.Note)
	}

	// Round files copied
	if _, err := os.Stat(rt.Store.PlanPath("alt", 1)); err != nil {
		t.Errorf("plan 1 not copied: %v", err)
	}
	if _, err := os.Stat(rt.Store.PlanPath("alt", 2)); err != nil {
		t.Errorf("plan 2 not copied: %v", err)
	}
	if _, err := os.Stat(rt.Store.PlanPath("alt", 3)); !os.IsNotExist(err) {
		t.Errorf("plan 3 should NOT exist in fork")
	}

	// Hook dispatched
	if len(hd.events) != 1 {
		t.Fatalf("hook events = %d, want 1", len(hd.events))
	}
	ev := hd.events[0]
	if ev.Type != hooks.EventForkCreated || ev.BindingID != "alt" || ev.OldState != "source" || ev.Round != 3 || ev.State != "active" {
		t.Errorf("hook event mismatch: %+v", ev)
	}
}

// refusedCleanup asserts a refusal left nothing behind: no worktree cut, no
// process started and no binding saved. #303 removed the pane, so the pane
// assertions that used to stand here are now the runner's own process list.
func refusedCleanup(t *testing.T, rt Runtime, fg *fakeGit, name string) {
	t.Helper()
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("a refusal must not cut a worktree: %+v", fg.addWorktreeCalls)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refusal must not start a process, got %d", got)
	}
	if _, err := rt.Store.Load(name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refusal must save no binding, Load = %v", err)
	}
}

func TestForkRefusalsLeaveNoWorktreeAndNoPane(t *testing.T) {
	ctx := context.Background()

	setup := func(t *testing.T) (*fakeGit, Runtime, string) {
		fg := &fakeGit{headCommitID: "commit-123"}
		rt := newForkRuntime(t, fg, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(srcCWD, 0o755); err != nil {
			t.Fatal(err)
		}
		seedFourRoundBinding(t, rt, "source", srcCWD)
		return fg, rt, srcCWD
	}

	t.Run("round 0 out of range", func(t *testing.T) {
		fg, rt, _ := setup(t)
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 0, NewName: "alt", PlannerID: testPlannerName,
		})
		if !errors.Is(err, ErrRoundOutOfRange) {
			t.Fatalf("got %v, want ErrRoundOutOfRange", err)
		}
		refusedCleanup(t, rt, fg, "alt")
	})

	t.Run("round 99 out of range", func(t *testing.T) {
		fg, rt, _ := setup(t)
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 99, NewName: "alt", PlannerID: testPlannerName,
		})
		if !errors.Is(err, ErrRoundOutOfRange) {
			t.Fatalf("got %v, want ErrRoundOutOfRange", err)
		}
		refusedCleanup(t, rt, fg, "alt")
	})

	t.Run("taken binding name", func(t *testing.T) {
		fg, rt, _ := setup(t)
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "source", PlannerID: testPlannerName,
		})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("got %v, want already exists error", err)
		}
		if len(fg.addWorktreeCalls) != 0 || len(runnerOf(t, rt).specs) != 0 {
			t.Error("worktree or process created on refusal")
		}
		if _, err := rt.Store.Load("source"); err != nil {
			t.Errorf("the source binding must survive a refused fork: %v", err)
		}
		if _, err := rt.Store.Load("alt"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a refusal must save no binding, Load alt = %v", err)
		}
	})

	t.Run("taken working tree", func(t *testing.T) {
		fg, rt, srcCWD := setup(t)
		// Try to fork to the same CWD as source
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName, CWD: srcCWD,
		})
		if !errors.Is(err, store.ErrCWDTaken) {
			t.Fatalf("got %v, want ErrCWDTaken", err)
		}
		refusedCleanup(t, rt, fg, "alt")
	})

	t.Run("nil Runtime.Git without CWD", func(t *testing.T) {
		rt := newForkRuntime(t, nil, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(srcCWD, 0o755)
		seedFourRoundBinding(t, rt, "source", srcCWD)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName,
		})
		if !errors.Is(err, ErrGitRequired) {
			t.Fatalf("got %v, want ErrGitRequired", err)
		}
		if got := len(runnerOf(t, rt).specs); got != 0 {
			t.Errorf("a refusal must start no process, got %d", got)
		}
		if _, err := rt.Store.Load("alt"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a refusal must save no binding, Load = %v", err)
		}
	})

	t.Run("branch exists", func(t *testing.T) {
		fg, rt, _ := setup(t)
		fg.branchExists = true
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName,
		})
		if !errors.Is(err, git.ErrBranchExists) {
			t.Fatalf("got %v, want ErrBranchExists", err)
		}
		refusedCleanup(t, rt, fg, "alt")
	})

	t.Run("no builder candidate", func(t *testing.T) {
		_, rt, srcCWD := setup(t)
		// Update source to have no builder candidate
		src, _ := rt.Store.Load("source")
		src.BuilderCandidate = ""
		_ = rt.Store.Save(src)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName,
		})
		if !errors.Is(err, ErrNoBuilderCandidate) {
			t.Fatalf("got %v, want ErrNoBuilderCandidate", err)
		}
		_ = srcCWD
	})

	t.Run("no builder candidate inherits via single candidate", func(t *testing.T) {
		_, rt, _ := setup(t)
		rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)
		src, _ := rt.Store.Load("source")
		src.BuilderCandidate = ""
		_ = rt.Store.Save(src)

		res, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName,
		})
		if err != nil {
			t.Fatalf("Fork: %v", err)
		}
		if res.Binding.BuilderCandidate != "agy/test/m" {
			t.Errorf("BuilderCandidate = %q, want agy/test/m", res.Binding.BuilderCandidate)
		}
	})
}

// TestForkRefusesALongNameBeforeCuttingAWorktree pins #64: a 25-character
// name builds a 33-character builder agent name, and Fork must refuse it
// before the worktree is cut -- a refused name leaves nothing behind.
func TestForkRefusesALongNameBeforeCuttingAWorktree(t *testing.T) {
	ctx := context.Background()
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fg, nil)
	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)

	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33

	_, err := Fork(ctx, rt, ForkOptions{
		Source: "source", Round: 2, NewName: name, PlannerID: testPlannerName,
	})
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("a refused name must not cut a worktree, calls = %+v", fg.addWorktreeCalls)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refused name must start no process, got %d", got)
	}
	// #303 deleted herdr.ErrInvalidAgentName with the herdr client; the
	// refusal is now store.ValidName's own text, wrapped by
	// builderAgentName with the length budget in it.
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Fork err = %v, want the agent-name length refusal", err)
	}
	if _, loadErr := rt.Store.Load(name); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound: a refused name saves no binding", loadErr)
	}
}

func TestForkRollback(t *testing.T) {
	ctx := context.Background()

	t.Run("AddWorktree failure leaves nothing behind", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "commit-123", addWorktreeErr: errors.New("worktree disk error")}
		rt := newForkRuntime(t, fg, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(srcCWD, 0o755)
		seedFourRoundBinding(t, rt, "source", srcCWD)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName,
		})
		if err == nil || !strings.Contains(err.Error(), "worktree disk error") {
			t.Fatalf("got %v, want worktree disk error", err)
		}
		if got := len(runnerOf(t, rt).specs); got != 0 {
			t.Errorf("a failed fork must start no process, got %d", got)
		}
		if _, err := rt.Store.Load("alt"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a failed fork must save no binding, Load = %v", err)
		}
	})

	t.Run("unknown candidate cuts no worktree", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "commit-123"}
		rt := newForkRuntime(t, fg, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(srcCWD, 0o755)
		seedFourRoundBinding(t, rt, "source", srcCWD)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName, Candidate: "claude/test/nope",
		})
		if !errors.Is(err, candidate.ErrUnknownCandidate) {
			t.Fatalf("got %v, want ErrUnknownCandidate", err)
		}
		if len(fg.addWorktreeCalls) != 0 {
			t.Fatalf("AddWorktree calls = %d, want 0", len(fg.addWorktreeCalls))
		}
	})

}

func TestForkWithCustomCWD(t *testing.T) {
	ctx := context.Background()
	rt := newForkRuntime(t, nil, nil) // nil Git allowed with --cwd
	srcCWD := filepath.Join(t.TempDir(), "repo")
	_ = os.MkdirAll(srcCWD, 0o755)
	seedFourRoundBinding(t, rt, "source", srcCWD)

	customCWD := filepath.Join(t.TempDir(), "custom-cwd")
	_ = os.MkdirAll(customCWD, 0o755)

	res, err := Fork(ctx, rt, ForkOptions{
		Source:    "source",
		Round:     2,
		NewName:   "alt",
		PlannerID: testPlannerName,
		CWD:       customCWD,
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
	fg := &fakeGit{headCommitID: "commit-123"}
	rt := newForkRuntime(t, fg, nil)
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
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)

	res, err := Fork(context.Background(), rt, ForkOptions{
		Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName, Headless: true,
	})
	if err != nil {
		t.Fatalf("Fork --headless: %v", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Fatalf("a fork starts no process, got %d", got)
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
	// The source's builder is untouched: a fork inherits the candidate, not
	// the mode, and a local builder is headless either way (#303).
	src, _ := rt.Store.Load("source")
	if !src.Builder.Headless() || src.Builder.PaneID != "" {
		t.Errorf("source builder changed: %+v", src.Builder)
	}
}

// TestForkRecordsBaseRef pins #136: a fork cut from the source's checkout
// records the branch that checkout (src.Repo) had checked out, so `relay
// land` knows what to rebase the fork's branch onto.
func TestForkRecordsBaseRef(t *testing.T) {
	ctx := context.Background()
	fg := &fakeGit{headCommitID: "commit-head-123", currentBranchResult: "main"}
	rt := newForkRuntime(t, fg, nil)

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
		Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName,
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
	fg := &fakeGit{headCommitID: "commit-head-123", currentBranchResult: "main"}
	rt := newForkRuntime(t, fg, nil)

	seedFourRoundBinding(t, rt, "source", t.TempDir())
	cwd := t.TempDir()

	res, err := Fork(ctx, rt, ForkOptions{
		Source: "source", Round: 2, NewName: "alt", PlannerID: testPlannerName, CWD: cwd,
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
