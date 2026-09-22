package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyD is the `d` key, which enters and leaves the dashboard screen.
func keyD() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}} }

// dashHostModel is splitModel with a database behind the source, so `d` and
// --dashboard can open the dashboard screen. The db is empty: what these
// tests read is the host's wiring, not the rows.
// drain runs cmds through the model, unwrapping tea.BatchMsg as the
// bubbletea loop does, until nothing is left. Commands the model returns
// are drained too, so a save and its prefsSavedMsg do not leak.
func drain(t *testing.T, m Model, cmds ...tea.Cmd) Model {
	t.Helper()
	queue := append([]tea.Cmd(nil), cmds...)
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if b, ok := msg.(tea.BatchMsg); ok {
			queue = append(append([]tea.Cmd(nil), b...), queue...)
			continue
		}
		res, next := m.Update(msg)
		m = res.(Model)
		if next != nil {
			queue = append(queue, next)
		}
	}
	return m
}

// TestJumpFromDashTurnsScopeAll proves the live-scope half: a hist-only
// binding jumps only if scope all is turned on first, exactly as `a` does.
// TestOptionsDashboardWithoutDBNotices: --dashboard with no database is the
// same refusal `d` shows, and the fleet screen stays.
// TestDashSavePrefsOnChange: a query and a sort change on the dashboard are
// persisted on the same path Scope is.
