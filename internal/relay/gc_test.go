package relay

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

func seedDone(t *testing.T, rt Runtime, name, cwd string) {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: cwd,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2"},
		Round:   3, State: store.StateDone,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

func TestGCClearsOnlyDoneBindings(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	seedDone(t, rt, "finished", "/repo-done")

	live := store.Binding{
		Name: "live", CWD: "/repo-live",
		Planner: store.Endpoint{PaneID: "w1:p3"},
		Builder: store.Endpoint{PaneID: "w1:p4"},
		Round:   1, State: store.StateActive,
	}
	if err := rt.Store.Save(live); err != nil {
		t.Fatalf("save live: %v", err)
	}
	// A binding needing a human must survive: removing it would throw away
	// the state that explains why it stopped.
	broken := store.Binding{
		Name: "broke", CWD: "/repo-broke",
		Planner: store.Endpoint{PaneID: "w1:p5"},
		Builder: store.Endpoint{PaneID: "w1:p6"},
		Round:   2, State: store.StateBroken,
	}
	if err := rt.Store.Save(broken); err != nil {
		t.Fatalf("save broken: %v", err)
	}

	got, err := GC(context.Background(), rt, GCOptions{})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Name != "finished" || !got[0].Deleted {
		t.Fatalf("gc result = %+v, want only the done binding deleted", got)
	}

	remaining, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("live and broken bindings must survive, got %d", len(remaining))
	}
}

func TestGCDryRunChangesNothing(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	seedDone(t, rt, "finished", "/repo-done")

	got, err := GC(context.Background(), rt, GCOptions{DryRun: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Deleted || got[0].ArchivedTo != "" {
		t.Fatalf("dry run must report without acting, got %+v", got)
	}
	if _, err := rt.Store.Load("finished"); err != nil {
		t.Errorf("dry run deleted the binding: %v", err)
	}
}

func TestGCArchiveKeepsTheRoundLog(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	seedDone(t, rt, "finished", "/repo-done")
	if err := rt.Store.AppendLog("finished", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	got, err := GC(context.Background(), rt, GCOptions{Archive: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].ArchivedTo == "" || got[0].Deleted {
		t.Fatalf("gc result = %+v, want an archive not a delete", got)
	}
	if !strings.HasSuffix(got[0].ArchivedTo, ".tar.gz") {
		t.Errorf("archive path = %q, want a .tar.gz", got[0].ArchivedTo)
	}
	if _, err := os.Stat(got[0].ArchivedTo); err != nil {
		t.Errorf("archive missing: %v", err)
	}
}

func TestGCWorktreeTeardown(t *testing.T) {
	fg := &fakeGit{}
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	rt.Git = fg

	seedDone(t, rt, "ordinary", "/repo-ordinary")

	forked := store.Binding{
		Name: "forked", CWD: "/state/.worktrees/forked",
		Worktree: "/state/.worktrees/forked",
		State:    store.StateDone, Round: 2,
		Planner: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(forked); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), rt, GCOptions{})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}

	var foundOrdinary, foundForked bool
	for _, r := range res {
		if r.Name == "ordinary" {
			foundOrdinary = true
			if r.WorktreeRemoved != "" || r.WorktreeKept != "" {
				t.Errorf("ordinary binding should have empty worktree fields: %+v", r)
			}
		}
		if r.Name == "forked" {
			foundForked = true
			if r.WorktreeRemoved != "/state/.worktrees/forked" {
				t.Errorf("WorktreeRemoved = %q, want /state/.worktrees/forked", r.WorktreeRemoved)
			}
		}
	}
	if !foundOrdinary || !foundForked {
		t.Errorf("missing results: ordinary=%v, forked=%v", foundOrdinary, foundForked)
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
	}
	if fg.removeWorktreeCalls[0].Force {
		t.Error("teardown must use force: false")
	}

	// Verify both bindings deleted from store
	list, err := rt.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("all bindings should be cleared, found %d", len(list))
	}
}

func TestGCWorktreeDryRun(t *testing.T) {
	fg := &fakeGit{}
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	rt.Git = fg

	seedDone(t, rt, "ordinary", "/repo-ordinary")

	forked := store.Binding{
		Name: "forked", CWD: "/state/.worktrees/forked",
		Worktree: "/state/.worktrees/forked",
		State:    store.StateDone, Round: 2,
		Planner: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(forked); err != nil {
		t.Fatal(err)
	}

	// Read state root before
	entriesBefore, err := os.ReadDir(rt.Store.Dir(""))
	if err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), rt, GCOptions{DryRun: true})
	if err != nil {
		t.Fatalf("GC dry run: %v", err)
	}

	for _, r := range res {
		if r.Name == "forked" && r.WorktreeRemoved != "/state/.worktrees/forked" {
			t.Errorf("dry-run should report WorktreeRemoved, got %q", r.WorktreeRemoved)
		}
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("dry-run must not call RemoveWorktree, got %d calls", len(fg.removeWorktreeCalls))
	}

	// Verify disk state unchanged
	entriesAfter, err := os.ReadDir(rt.Store.Dir(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesBefore) != len(entriesAfter) {
		t.Errorf("entries changed: before=%d, after=%d", len(entriesBefore), len(entriesAfter))
	}
	for i := range entriesBefore {
		if entriesBefore[i].Name() != entriesAfter[i].Name() {
			t.Errorf("entry mismatch at %d: %s != %s", i, entriesBefore[i].Name(), entriesAfter[i].Name())
		}
	}
}

func TestGCWorktreeDirtyCheckError(t *testing.T) {
	fg := &fakeGit{dirtyErr: errors.New("git lock busy\ndetails")}
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	rt.Git = fg

	forked := store.Binding{
		Name: "forked-dirty-err", CWD: "/state/.worktrees/forked-dirty-err",
		Worktree: "/state/.worktrees/forked-dirty-err",
		State:    store.StateDone, Round: 2,
		Planner: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(forked); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), rt, GCOptions{})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}
	r := res[0]
	if r.Name != "forked-dirty-err" {
		t.Errorf("Name = %q, want forked-dirty-err", r.Name)
	}
	if r.WorktreeRemoved != "" {
		t.Errorf("WorktreeRemoved = %q, want empty", r.WorktreeRemoved)
	}
	if r.WorktreeKept != "/state/.worktrees/forked-dirty-err" {
		t.Errorf("WorktreeKept = %q, want /state/.worktrees/forked-dirty-err", r.WorktreeKept)
	}
	if r.KeptReason != "dirty check failed: git lock busy" {
		t.Errorf("KeptReason = %q, want 'dirty check failed: git lock busy'", r.KeptReason)
	}
	if !r.Deleted {
		t.Errorf("Deleted = false, want true")
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree should not be called on dirty check error, got %d calls", len(fg.removeWorktreeCalls))
	}

	// Verify binding deleted from store
	if _, err := rt.Store.Load("forked-dirty-err"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding state should be deleted, got err = %v", err)
	}
}
