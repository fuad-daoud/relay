package pick

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
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

func TestConfirmViewNamesTheBindingItsStateAndRound(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	v := m.confirmView()
	for _, want := range []string{"mark webshop done?", "ACTIVE", "round 2", "y", "any other key cancels"} {
		if !contains(v, want) {
			t.Errorf("confirm view lacks %q:\n%s", want, v)
		}
	}
	m = newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("webshop", "NEEDS YOU", "blocked")))
	m, _ = update(t, m, key("enter"))
	v = m.confirmView()
	for _, want := range []string{"unbind webshop?", "NEEDS YOU", "round 2"} {
		if !contains(v, want) {
			t.Errorf("unbind confirm view lacks %q:\n%s", want, v)
		}
	}
}

func TestCtrlCCancelsFromConfirm(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("ctrl+c"))
	wantQuit(t, m, cmd, ErrCancelled)
}
