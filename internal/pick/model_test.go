package pick

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// wantQuit asserts the command is tea.Quit and the model carries outcome.
func wantQuit(t *testing.T, m Model, cmd func() interface{}, outcome error) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected tea.Quit, got no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected tea.Quit")
	}
	if !errors.Is(m.outcome, outcome) {
		t.Fatalf("outcome = %v, want %v", m.outcome, outcome)
	}
}

func TestEmptyListEndsOnResultScreenWithNothingToPick(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg())
	if m.screen != screenResult || m.result.text != "no bindings to mark done" {
		t.Fatalf("screen=%v text=%q", m.screen, m.result.text)
	}
	m, cmd := update(t, m, key("x"))
	wantQuit(t, m, cmd, ErrNothingToPick)
}

func TestStatusErrorEndsOnResultScreen(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, statusMsg{err: errors.New("herdr: connection refused")})
	if m.screen != screenResult || m.result.err == nil {
		t.Fatalf("screen=%v err=%v", m.screen, m.result.err)
	}
	m, cmd := update(t, m, key("enter"))
	wantQuit(t, m, cmd, ErrVerbFailed)
}

func TestListCursorStaysInBounds(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working"), row("b", "ACTIVE", "working")))
	m, _ = update(t, m, key("up"))
	if m.cursor != 0 {
		t.Fatalf("cursor above the top: %d", m.cursor)
	}
	m, _ = update(t, m, key("j"))
	m, _ = update(t, m, key("down"))
	if m.cursor != 1 {
		t.Fatalf("cursor past the end: %d", m.cursor)
	}
	m, _ = update(t, m, key("k"))
	if m.cursor != 0 {
		t.Fatalf("k did not move up: %d", m.cursor)
	}
}

func TestEscCancelsFromTheList(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working")))
	m, cmd := update(t, m, key("esc"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestCtrlCCancelsFromAnyScreen(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg())
	m, cmd := update(t, m, key("ctrl+c"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestEnterRunsDoneAndShowsItsText(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, cmd := update(t, m, key("enter"))
	if m.screen != screenResult || !m.result.pending {
		t.Fatalf("enter should move to a pending result screen: screen=%v pending=%v", m.screen, m.result.pending)
	}
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
	m, _ = update(t, m, cmd())
	if m.result.pending || m.result.err != nil {
		t.Fatalf("result: pending=%v err=%v", m.result.pending, m.result.err)
	}
	if m.result.text != relay.DoneText("webshop") {
		t.Fatalf("text = %q, want DoneText", m.result.text)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil || b.State != store.StateDone {
		t.Fatalf("binding after done: state=%v err=%v", b.State, err)
	}
	m, quit := update(t, m, key("enter"))
	wantQuit(t, m, quit, nil)
}

func TestEnterRunsUnbindWithArchive(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbUnbind, Archive: true})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, cmd := update(t, m, key("enter"))
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("unbind: %v", m.result.err)
	}
	if !contains(m.result.text, "archived webshop to ") {
		t.Fatalf("text = %q, want the archive line", m.result.text)
	}
	if _, err := rt.Store.Load("webshop"); err == nil {
		t.Fatal("binding still loads after unbind")
	}
}

func TestVerbErrorShowsAndExitsOne(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh) // no binding named webshop
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, cmd := update(t, m, key("enter"))
	m, _ = update(t, m, cmd())
	if m.result.err == nil {
		t.Fatal("done on a missing binding should fail")
	}
	m, quit := update(t, m, key("x"))
	wantQuit(t, m, quit, ErrVerbFailed)
}

func TestKeysAreIgnoredWhileTheVerbRuns(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("enter"))
	if cmd != nil {
		t.Fatal("a key during a pending verb must not quit or re-run")
	}
	if m.outcome != nil {
		t.Fatalf("outcome set early: %v", m.outcome)
	}
}
