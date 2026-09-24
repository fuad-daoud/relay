package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeView is a minimal View the routing tests can push and interrogate.
type fakeView struct {
	crumbs  []string
	capture bool
	last    tea.KeyMsg
}

func (f fakeView) Crumbs() []string {
	if len(f.crumbs) == 0 {
		return []string{"fake"}
	}
	return f.crumbs
}
func (f fakeView) Context(Env) (string, string) { return "fake", "" }
func (f fakeView) Keys() []KeyHelp              { return nil }
func (f fakeView) Capturing() bool              { return f.capture }
func (f fakeView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		f.last = k
	}
	return f, nil
}
func (f fakeView) Body(env Env, w, h int) string {
	return strings.Repeat("\n", h-1)
}

// TestKeyRoutingRules is §5.2's key routing, one case per rule.
func TestKeyRoutingRules(t *testing.T) {
	newShell := func() Model {
		return splitModel(t, 140, 40,
			relevo.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"},
			relevo.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE"},
		)
	}
	key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

	t.Run("1 ctrl+c quits anywhere", func(t *testing.T) {
		m := newShell()
		m.stack = append(m.stack, fakeView{capture: true})
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil {
			t.Fatal("ctrl+c must quit")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Error("ctrl+c must return tea.Quit")
		}
	})

	t.Run("2 cmd.open owns the key", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key(':'))
		m = res.(Model)
		if !m.cmd.open {
			t.Fatal(": must open the command line")
		}
		res, _ = m.Update(key('x'))
		m = res.(Model)
		if !m.cmd.open || !strings.Contains(m.cmd.typed(), "x") {
			t.Errorf("while open the cmdline owns the key: open=%v typed=%q", m.cmd.open, m.cmd.typed())
		}
	})

	t.Run("3 help closes on esc, ?, q and ignores others", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key('?'))
		m = res.(Model)
		if !m.help {
			t.Fatal("? must open the help overlay")
		}
		res, _ = m.Update(key('j'))
		m = res.(Model)
		if !m.help {
			t.Error("another key must be ignored while help is up")
		}
		res, _ = m.Update(key('q'))
		m = res.(Model)
		if m.help {
			t.Error("q must close the help overlay")
		}
	})

	t.Run("4 a capturing top view owns every key", func(t *testing.T) {
		m := newShell()
		fv := fakeView{capture: true}
		m.stack = append(m.stack, fv)
		res, _ := m.Update(key('?'))
		m = res.(Model)
		if m.help {
			t.Error("? must be forwarded to a capturing view, not open help")
		}
		if m.cmd.open {
			t.Error("the cmdline must stay closed")
		}
		if got := m.top().(fakeView).last.String(); got != "?" {
			t.Errorf("the capturing view must receive the key, got %q", got)
		}
	})

	t.Run("5 colon opens the command line", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key(':'))
		if !res.(Model).cmd.open {
			t.Error(": must open the command line")
		}
	})

	t.Run("6 question opens help", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key('?'))
		if !res.(Model).help {
			t.Error("? must open help")
		}
	})

	t.Run("7 esc pops only above the root", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if len(res.(Model).stack) != 1 {
			t.Error("esc at depth 1 must do nothing")
		}
		m.stack = append(m.stack, fakeView{})
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = drain(t, res.(Model), cmd)
		if len(m.stack) != 1 {
			t.Errorf("esc above the root must pop, depth = %d", len(m.stack))
		}
	})

	t.Run("8 q quits at the root and pops above it", func(t *testing.T) {
		m := newShell()
		_, cmd := m.Update(key('q'))
		if cmd == nil {
			t.Fatal("q at depth 1 must quit")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Error("q at depth 1 must return tea.Quit")
		}
		m.stack = append(m.stack, fakeView{})
		res, cmd := m.Update(key('q'))
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				t.Error("q above the root must not quit")
			}
		}
		m = drain(t, res.(Model), cmd)
		if len(m.stack) != 1 {
			t.Errorf("q above the root must pop, depth = %d", len(m.stack))
		}
	})

	t.Run("9 other keys reach the top view", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key('j'))
		m = res.(Model)
		if fleet(m).cursor != 1 || fleet(m).sticky != "webshop" {
			t.Errorf("j must move the fleet cursor, got cursor %d sticky %q", fleet(m).cursor, fleet(m).sticky)
		}
	})
}

// TestSnapshotRoutingForwardsToEveryView: a tabMsg reaches a round view
// that is under a pushed view (§5.2's bottom-to-top forwarding).
func TestSnapshotRoutingForwardsToEveryView(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	m := splitModel(t, 140, 40, relevo.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"})
	v, _ := newRoundView(m.env(), "webshop", 0)
	rv := v.(roundView)
	m.stack = []View{rv, fakeView{}}

	res, _ := m.Update(tabMsg{name: "webshop", round: rv.pane.detail.round, t: tabReport, content: tabContent{loaded: true, body: "snap"}})
	m = res.(Model)

	got := m.stack[0].(roundView)
	if !got.pane.detail.cache[tabReport].loaded || got.pane.detail.cache[tabReport].body != "snap" {
		t.Errorf("the round view under the pushed view must receive the tabMsg: %+v", got.pane.detail.cache[tabReport])
	}
}

// TestHelpListsGlobalAndViewKeys: the help overlay names the globals and
// the top view's own keys.
func TestHelpListsGlobalAndViewKeys(t *testing.T) {
	m := splitModel(t, 140, 40, relevo.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"})
	rv, _ := newRoundView(m.env(), "webshop", 0)
	m.stack = append(m.stack, rv)

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = res.(Model)
	if !m.help {
		t.Fatal("? must open help")
	}

	body := plain(m.helpBody(m.env(), bodyHeight(m.env())))
	for _, want := range []string{"global", "view", ": command", "? help", "esc back", "[ ] round", "1-5 tab"} {
		if !strings.Contains(body, want) {
			t.Errorf("help must list %q:\n%s", want, body)
		}
	}
}
