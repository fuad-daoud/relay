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

func TestPauseRefusesOpenRound(t *testing.T) {
	f := &fakePanes{}
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

func TestPauseHeadlessClosesNoPane(t *testing.T) {
	t.Run("between rounds", func(t *testing.T) {
		f := &fakePanes{}
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
		f := &fakePanes{}
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
	f := &fakePanes{}
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
