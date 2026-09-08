package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/store"
)

type recordHookDispatcher struct {
	events []hooks.Event
}

func (r *recordHookDispatcher) Dispatch(ctx context.Context, event hooks.Event) {
	r.events = append(r.events, event)
}

func newForkRuntime(t *testing.T, f *fakeHerdr, fg *fakeGit, hd hooks.Dispatcher) Runtime {
	t.Helper()
	var g Git
	if fg != nil {
		g = fg
	}
	return Runtime{
		Herdr:   f,
		Git:     g,
		Store:   store.New(t.TempDir()),
		Aliases: alias.DefaultTable(),
		Now:     func() time.Time { return baseTime },
		Hooks:   hd,
	}
}

func seedFourRoundBinding(t *testing.T, rt Runtime, name, cwd string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:           name,
		CWD:            cwd,
		Planner:        store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess", Kind: "claude"},
		Builder:        store.Endpoint{AgentName: name + "-builder", PaneID: "w2:p4", Kind: "opencode"},
		BuilderAlias:   "builder",
		Round:          4,
		State:          store.StateActive,
		RoundCap:       20,
		RoundTimeoutMS: 1800000,
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

func TestForkSuccess(t *testing.T) {
	ctx := context.Background()
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	hd := &recordHookDispatcher{}
	rt := newForkRuntime(t, fh, fg, hd)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	srcBefore, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	srcLogBefore, err := rt.Store.ReadLog("source")
	if err != nil {
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

	// Git calls
	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("AddWorktree calls = %d, want 1", len(fg.addWorktreeCalls))
	}
	wtCall := fg.addWorktreeCalls[0]
	if wtCall.Dir != srcCWD || wtCall.Path != expectedWT || wtCall.Branch != "relay/alt" || wtCall.Commit != "commit-head-123" {
		t.Errorf("AddWorktree call mismatch: %+v", wtCall)
	}

	// Builder started
	if len(fh.starts) != 1 {
		t.Fatalf("Herdr starts = %d, want 1", len(fh.starts))
	}
	if fh.starts[0].Name != "alt-builder" || fh.starts[0].Pane != "w2:p5" {
		t.Errorf("Herdr start mismatch: %+v", fh.starts[0])
	}

	// Source binding is byte-for-byte unchanged
	srcAfter, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	if srcBefore != srcAfter {
		t.Errorf("source binding was mutated: %+v != %+v", srcBefore, srcAfter)
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
	if len(altLog) != 5 { // 2 plans + 2 reports from round 1&2 + 1 KindFork
		t.Fatalf("alt log length = %d, want 5", len(altLog))
	}
	lastEntry := altLog[len(altLog)-1]
	if lastEntry.Kind != store.KindFork || lastEntry.Round != 3 || !lastEntry.Confirmed || lastEntry.Direction != store.DirToPlanner {
		t.Errorf("last log entry mismatch: %+v", lastEntry)
	}
	if lastEntry.Note != "forked from source at round 2" {
		t.Errorf("last log entry note = %q", lastEntry.Note)
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

func TestForkRefusalsLeaveNoWorktreeAndNoPane(t *testing.T) {
	ctx := context.Background()

	setup := func(t *testing.T) (*fakeHerdr, *fakeGit, Runtime, string) {
		fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
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
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || fh.splits != 0 {
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
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || fh.splits != 0 {
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
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || fh.splits != 0 {
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
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || fh.splits != 0 {
			t.Error("worktree or pane created on refusal")
		}
	})

	t.Run("nil Runtime.Git without CWD", func(t *testing.T) {
		fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
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
		if len(fh.starts) != 0 || fh.splits != 0 {
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
		if len(fg.addWorktreeCalls) != 0 || len(fh.starts) != 0 || fh.splits != 0 {
			t.Error("worktree or pane created on refusal")
		}
	})

	t.Run("no builder alias", func(t *testing.T) {
		_, _, rt, srcCWD := setup(t)
		// Update source to have no builder alias
		src, _ := rt.Store.Load("source")
		src.BuilderAlias = ""
		_ = rt.Store.Save(src)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
		})
		if !errors.Is(err, ErrNoBuilderAlias) {
			t.Fatalf("got %v, want ErrNoBuilderAlias", err)
		}
		_ = srcCWD
	})
}

func TestForkRollback(t *testing.T) {
	ctx := context.Background()

	t.Run("AddWorktree failure leaves nothing behind", func(t *testing.T) {
		fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
		fg := &fakeGit{headCommitID: "commit-123", addWorktreeErr: errors.New("worktree disk error")}
		rt := newForkRuntime(t, fh, fg, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(srcCWD, 0o755)
		seedFourRoundBinding(t, rt, "source", srcCWD)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
		})
		if err == nil || !strings.Contains(err.Error(), "worktree disk error") {
			t.Fatalf("got %v, want worktree disk error", err)
		}
		if len(fh.starts) != 0 || fh.splits != 0 {
			t.Error("builder pane spawned after AddWorktree failure")
		}
	})

	t.Run("resolveBuilder failure removes worktree", func(t *testing.T) {
		fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
		fg := &fakeGit{headCommitID: "commit-123"}
		rt := newForkRuntime(t, fh, fg, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(srcCWD, 0o755)
		seedFourRoundBinding(t, rt, "source", srcCWD)

		// Ask for unknown alias to fail resolveBuilder
		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3", Alias: "nonexistent-builder",
		})
		if err == nil {
			t.Fatal("expected error on unknown alias")
		}
		if len(fg.removeWorktreeCalls) != 1 {
			t.Fatalf("RemoveWorktree calls = %d, want 1 (rollback)", len(fg.removeWorktreeCalls))
		}
		if !fg.removeWorktreeCalls[0].Force {
			t.Error("rollback must pass force: true")
		}
	})

	t.Run("ForkState failure removes worktree and leaves pane naming it in error", func(t *testing.T) {
		fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
		fg := &fakeGit{headCommitID: "commit-123"}
		rt := newForkRuntime(t, fh, fg, nil)
		srcCWD := filepath.Join(t.TempDir(), "repo")
		_ = os.MkdirAll(srcCWD, 0o755)
		seedFourRoundBinding(t, rt, "source", srcCWD)

		// Simulate a ForkState failure inside WithLock by pre-creating the destination directory
		dstDir := rt.Store.Dir("alt")
		_ = os.MkdirAll(dstDir, 0o755)

		_, err := Fork(ctx, rt, ForkOptions{
			Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3",
		})
		if err == nil {
			t.Fatal("expected error")
		}
		// Error must name the pane id
		if !strings.Contains(err.Error(), "w2:p5") {
			t.Errorf("error %q must name builder pane w2:p5", err.Error())
		}
		// Must have rolled back the worktree
		if len(fg.removeWorktreeCalls) != 1 {
			t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
		}
		if !fg.removeWorktreeCalls[0].Force {
			t.Error("rollback must pass force: true")
		}
		// The builder pane was started and not killed
		if len(fh.starts) != 1 {
			t.Fatalf("Herdr starts = %d, want 1", len(fh.starts))
		}
	})
}

func TestForkWithCustomCWD(t *testing.T) {
	ctx := context.Background()
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
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
}

func TestForkWriteForkCleanupOnSaveFailure(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
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

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		return writeFork(tx, rt.Store, "source", b, 2, time.Now().UTC())
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
