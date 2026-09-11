package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// addRepo makes a directory to stand in for the planner's repository.
func addRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAddCreatesAWorktreeBindingAtRoundOne(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, PlannerPane: "w2:p3", Repo: repo,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got.Binding.Round != 1 {
		t.Errorf("a peer builder starts at round 1, got %d", got.Binding.Round)
	}
	if got.Binding.State != store.StateActive {
		t.Errorf("state = %q, want active", got.Binding.State)
	}
	if got.Binding.ForkedFrom != "" || got.Binding.ForkedAtRound != 0 {
		t.Errorf("a peer builder was never forked from anything, got %+v", got.Binding)
	}
	if got.Branch != "relay/frontend" {
		t.Errorf("branch = %q, want relay/frontend", got.Branch)
	}
	if got.Base != "commit-head-123" {
		t.Errorf("base = %q, want the repo HEAD", got.Base)
	}

	want := rt.Store.WorktreePath("frontend")
	if got.Worktree != want || got.Binding.CWD != want {
		t.Errorf("worktree = %q, cwd = %q, want both %q", got.Worktree, got.Binding.CWD, want)
	}
	if got.Binding.Worktree != want {
		t.Errorf("relay must record the tree it created so it may remove it, got %q", got.Binding.Worktree)
	}

	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("addWorktreeCalls = %+v", fg.addWorktreeCalls)
	}
	call := fg.addWorktreeCalls[0]
	if call.Dir != repo || call.Path != want || call.Branch != "relay/frontend" || call.Commit != "commit-head-123" {
		t.Errorf("worktree cut wrongly: %+v", call)
	}

	// The round log holds nothing relayed yet, only the pick entry recording
	// why relay chose the builder it spawned (#61 step 2).
	entries, err := rt.Store.ReadLog("frontend")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("a fresh peer binding has relayed nothing but its pick, got %+v", entries)
	}
}

func TestAddRefusesAnAmbiguousCandidateBeforeCuttingAWorktree(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: "", PlannerPane: "w2:p3", Repo: addRepo(t),
	})
	if !errors.Is(err, ErrAmbiguousCandidate) {
		t.Fatalf("want ErrAmbiguousCandidate, got %v", err)
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("expected 0 addWorktreeCalls, got %d", len(fg.addWorktreeCalls))
	}
}

func TestAddResolvesTheOnlyBuilderCandidate(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)

	res, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: "", PlannerPane: "w2:p3", Repo: addRepo(t),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Binding.BuilderCandidate != "agy/test/m" {
		t.Errorf("BuilderCandidate = %q, want agy/test/m", res.Binding.BuilderCandidate)
	}
}

func TestAddRefusesADuplicateName(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	repo := addRepo(t)

	if _, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, PlannerPane: "w2:p3", Repo: repo,
	}); err != nil {
		t.Fatalf("first Add: %v", err)
	}

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, PlannerPane: "w2:p3", Repo: repo,
	})
	if err == nil {
		t.Fatal("a name already in use must be refused")
	}
}

func TestAddRollsBackTheWorktreeWhenTheBuilderFailsToStart(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	// No newPane, so splitting the planner's pane yields nothing to start in.
	fh.newPane = ""
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, PlannerPane: "w2:p3", Repo: addRepo(t),
	})
	if err == nil {
		t.Fatal("expected the add to fail")
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Errorf("a failed add must not leave its worktree behind, calls = %+v", fg.removeWorktreeCalls)
	}
	if _, err := rt.Store.Load("frontend"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a failed add must leave no binding, got %v", err)
	}
}

// TestAddRefusesALongNameBeforeCuttingAWorktree pins #64: a 25-character name
// builds a 33-character builder agent name, and Add must refuse it before the
// worktree is cut -- a refused name leaves nothing behind.
func TestAddRefusesALongNameBeforeCuttingAWorktree(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33

	_, err := Add(context.Background(), rt, AddOptions{
		Name: name, Candidate: testAgyRef, PlannerPane: "w2:p3", Repo: addRepo(t),
	})
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("a refused name must not cut a worktree, calls = %+v", fg.addWorktreeCalls)
	}
	if fh.splits != 0 || len(fh.starts) != 0 {
		t.Errorf("a refused name must touch no pane: splits = %d, starts = %d", fh.splits, len(fh.starts))
	}
	if !errors.Is(err, herdr.ErrInvalidAgentName) {
		t.Fatalf("Add err = %v, want one wrapping herdr.ErrInvalidAgentName", err)
	}
	if _, loadErr := rt.Store.Load(name); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound: a refused name saves no binding", loadErr)
	}
}

func TestAddBindsAPreparedDirectoryWithCWD(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	prepared := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "legacy", Candidate: testAgyRef, PlannerPane: "w2:p3",
		Repo: addRepo(t), CWD: prepared,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.Binding.CWD != prepared {
		t.Errorf("cwd = %q, want the prepared directory %q", got.Binding.CWD, prepared)
	}
	if got.Worktree != "" || got.Binding.Worktree != "" {
		t.Error("relay did not create this tree, so it must never record ownership of it")
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("--cwd must not cut a worktree, calls = %+v", fg.addWorktreeCalls)
	}
}

func TestAddRefusesATreeAnotherBindingDrives(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	prepared := addRepo(t)

	if err := rt.Store.Save(store.Binding{
		Name: "incumbent", CWD: prepared, Round: 1, State: store.StateActive,
		Builder: store.Endpoint{PaneID: "w2:p4"},
	}); err != nil {
		t.Fatalf("seed incumbent: %v", err)
	}

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, PlannerPane: "w2:p3",
		Repo: addRepo(t), CWD: prepared,
	})
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("want ErrCWDTaken, got %v", err)
	}
}
