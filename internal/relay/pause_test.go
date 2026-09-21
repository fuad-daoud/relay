package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// pauseCloseRound clears the round-open stamp and advances the round number
// the way queueReport does, so a pause fixture is "between rounds" with the
// round it last completed recorded on the binding.
func pauseCloseRound(t *testing.T, rt Runtime, b store.Binding) store.Binding {
	t.Helper()
	b.RoundStartedAt = time.Time{}
	b.Round++
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save closed round: %v", err)
	}
	return b
}

func TestPauseReleasesWorktreeClosesPaneAndParks(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{}
	rt.Git = fg

	b.Worktree = "/wt/webshop"
	b.Branch = "relay/webshop"
	b = pauseCloseRound(t, rt, b)
	paneID := b.Builder.PaneID

	res, err := Pause(context.Background(), rt, "webshop", PauseOptions{})
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if len(fg.removeWorktreeCalls) != 1 {
		t.Fatalf("removeWorktreeCalls = %d, want 1", len(fg.removeWorktreeCalls))
	}
	want := removeWorktreeCall{Dir: b.CWD, Path: b.Worktree, Force: false}
	if fg.removeWorktreeCalls[0] != want {
		t.Errorf("removeWorktreeCall = %+v, want %+v", fg.removeWorktreeCalls[0], want)
	}
	if len(f.closed) != 1 || f.closed[0] != paneID {
		t.Errorf("closed = %v, want [%s]", f.closed, paneID)
	}
	if len(fg.deleteBranchCalls) != 0 {
		t.Errorf("the branch must survive: deleteBranchCalls = %+v", fg.deleteBranchCalls)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StatePaused {
		t.Errorf("State = %s, want paused", got.State)
	}
	if got.Builder.PaneID != "" {
		t.Errorf("Builder.PaneID = %q, want empty", got.Builder.PaneID)
	}
	if got.Builder.Kind == "" {
		t.Errorf("Builder.Kind must be kept, got empty")
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindPause {
		t.Errorf("last log kind = %s, want pause", last.Kind)
	}
	if last.Note != "paused after round 1" {
		t.Errorf("last log note = %q, want 'paused after round 1'", last.Note)
	}

	if res.Round != 1 {
		t.Errorf("res.Round = %d, want 1", res.Round)
	}
	if res.Branch != "relay/webshop" {
		t.Errorf("res.Branch = %q, want relay/webshop", res.Branch)
	}
	if res.Worktree != "/wt/webshop" {
		t.Errorf("res.Worktree = %q, want /wt/webshop", res.Worktree)
	}
	if res.PaneClosed != paneID {
		t.Errorf("res.PaneClosed = %q, want %q", res.PaneClosed, paneID)
	}
	if res.PaneCloseErr != "" {
		t.Errorf("res.PaneCloseErr = %q, want empty", res.PaneCloseErr)
	}
}

func TestPauseRefusesOpenRound(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{}
	rt.Git = fg

	b.Worktree = "/wt/webshop"
	b.Branch = "relay/webshop"
	// RoundStartedAt is set by Send and left in place: the round is open.
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	_, err := Pause(context.Background(), rt, "webshop", PauseOptions{})
	if !errors.Is(err, ErrRoundOpen) {
		t.Fatalf("err = %v, want ErrRoundOpen", err)
	}
	if fg.dirtyCalls != 0 || len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("no git call may run: dirty=%d remove=%d", fg.dirtyCalls, len(fg.removeWorktreeCalls))
	}
	if len(f.closed) != 0 {
		t.Errorf("no pane may be closed: %v", f.closed)
	}
	got, _ := rt.Store.Load("webshop")
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active (unchanged)", got.State)
	}
}

func TestPauseRefusesDirtyWithoutCommit(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{dirtyResult: true}
	rt.Git = fg

	b.Worktree = "/wt/webshop"
	b.Branch = "relay/webshop"
	b = pauseCloseRound(t, rt, b)

	_, err := Pause(context.Background(), rt, "webshop", PauseOptions{})
	if !errors.Is(err, ErrPauseDirty) {
		t.Fatalf("err = %v, want ErrPauseDirty", err)
	}
	if len(fg.commitAllCalls) != 0 {
		t.Errorf("no commit may run without --commit: %+v", fg.commitAllCalls)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("worktree must not be removed: %+v", fg.removeWorktreeCalls)
	}
	if len(f.closed) != 0 {
		t.Errorf("no pane may be closed: %v", f.closed)
	}
	got, _ := rt.Store.Load("webshop")
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active (unchanged)", got.State)
	}
}

func TestPauseCommitThenReleases(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{dirtyResult: true, commitAllSHA: "abc123abc123abc123abc123abc123abc123abcd"}
	rt.Git = fg

	b.Worktree = "/wt/webshop"
	b.Branch = "relay/webshop"
	b = pauseCloseRound(t, rt, b)

	res, err := Pause(context.Background(), rt, "webshop", PauseOptions{Commit: true})
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	want := commitAllCall{Dir: "/wt/webshop", Message: "[relay] webshop: paused after round 1"}
	if len(fg.commitAllCalls) != 1 || fg.commitAllCalls[0] != want {
		t.Errorf("commitAllCalls = %+v, want [%+v]", fg.commitAllCalls, want)
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Errorf("removeWorktreeCalls = %d, want 1", len(fg.removeWorktreeCalls))
	}
	if res.Committed != "abc123abc123abc123abc123abc123abc123abcd" {
		t.Errorf("res.Committed = %q, want the commit sha", res.Committed)
	}
}

func TestPauseHeadlessClosesNoPane(t *testing.T) {
	t.Run("between rounds", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := seedHeadless(t, f, fr)
		fg := &fakeGit{}
		rt.Git = fg

		b.Worktree = "/wt/webshop"
		b.Branch = "relay/webshop"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		if _, err := Pause(context.Background(), rt, "webshop", PauseOptions{}); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		if len(f.closed) != 0 {
			t.Errorf("a headless binding has no pane: closed = %v", f.closed)
		}
		got, _ := rt.Store.Load("webshop")
		if got.State != store.StatePaused {
			t.Errorf("State = %s, want paused", got.State)
		}
	})

	t.Run("process alive", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := seedHeadless(t, f, fr)
		fg := &fakeGit{}
		rt.Git = fg
		fr.script(4242, true)

		b.Worktree = "/wt/webshop"
		b.Branch = "relay/webshop"
		b.Builder.PID = 4242
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		_, err := Pause(context.Background(), rt, "webshop", PauseOptions{})
		if !errors.Is(err, ErrRoundOpen) {
			t.Fatalf("err = %v, want ErrRoundOpen", err)
		}
	})
}

func TestPauseRefusesRemoteCwdAndDone(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg

	remote := store.Binding{
		Name: "remote", CWD: "/repo-remote", Worktree: "/wt/remote",
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{Mode: store.ModeRemote, Kind: "agy"},
		Round:   2, State: store.StateActive,
	}
	cwd := store.Binding{
		Name: "cwd", CWD: "/repo-cwd",
		Planner: store.Endpoint{PaneID: "w1:p2"},
		Builder: store.Endpoint{PaneID: "w1:p3", Kind: "agy"},
		Round:   1, State: store.StateActive,
	}
	done := store.Binding{
		Name: "finished", CWD: "/repo-done", Worktree: "/wt/done",
		Planner: store.Endpoint{PaneID: "w1:p4"},
		Builder: store.Endpoint{PaneID: "w1:p5", Kind: "agy"},
		Round:   3, State: store.StateDone,
	}
	for _, b := range []store.Binding{remote, cwd, done} {
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("save %s: %v", b.Name, err)
		}
	}

	_, errRemote := Pause(context.Background(), rt, "remote", PauseOptions{})
	_, errCwd := Pause(context.Background(), rt, "cwd", PauseOptions{})
	_, errDone := Pause(context.Background(), rt, "finished", PauseOptions{})

	if errRemote == nil || errCwd == nil || errDone == nil {
		t.Fatalf("all three must be refused: remote=%v cwd=%v done=%v", errRemote, errCwd, errDone)
	}
	if errRemote.Error() == errCwd.Error() || errCwd.Error() == errDone.Error() || errRemote.Error() == errDone.Error() {
		t.Errorf("the three refusals must be distinct: %q / %q / %q", errRemote, errCwd, errDone)
	}
	if len(fg.removeWorktreeCalls) != 0 || len(f.closed) != 0 {
		t.Errorf("nothing may change: remove=%d closed=%v", len(fg.removeWorktreeCalls), f.closed)
	}
}

func TestPausePaneCloseFailureIsNotFatal(t *testing.T) {
	f := &fakeHerdr{closeErr: errors.New("gone")}
	rt, b := sentBinding(t, f)
	fg := &fakeGit{}
	rt.Git = fg

	b.Worktree = "/wt/webshop"
	b.Branch = "relay/webshop"
	b = pauseCloseRound(t, rt, b)
	paneID := b.Builder.PaneID

	res, err := Pause(context.Background(), rt, "webshop", PauseOptions{})
	if err != nil {
		t.Fatalf("a refused pane close must not fail pause: %v", err)
	}
	if res.PaneCloseErr != "gone" {
		t.Errorf("res.PaneCloseErr = %q, want gone", res.PaneCloseErr)
	}
	if res.PaneClosed != paneID {
		t.Errorf("res.PaneClosed = %q, want %q", res.PaneClosed, paneID)
	}
	got, _ := rt.Store.Load("webshop")
	if got.State != store.StatePaused {
		t.Errorf("State = %s, want paused", got.State)
	}
}
