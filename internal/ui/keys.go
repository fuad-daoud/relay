package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Keys that work from either focus.
	switch msg.String() {
	case "s":
		m.sort = !m.sort
		m.list.resolveSticky(relay.Report{Bindings: m.rows()})
		m.list.top = m.railTop()
		return m, m.save()
	case "<", ">":
		if m.compact {
			return m, nil
		}
		d := railStep
		if msg.String() == "<" {
			d = -railStep
		}
		return m.setRail(m.railCols + d)
	case "c":
		m.compact = !m.compact
		m.detail.vp.Width = m.paneWidth()
		m.fillViewport()
		m.list.top = m.railTop()
		return m, m.save()
	case "1", "2", "3", "4":
		if m.empty() {
			return m, nil
		}
		if m.paneVisible() {
			return m.switchTab(tab(msg.String()[0] - '1'))
		}
	}
	railFocused := m.screen == screenList
	if railFocused {
		switch msg.String() {
		case "up", "k":
			return m.moveCursor(-1)
		case "down", "j":
			return m.moveCursor(+1)
		case "enter":
			if m.empty() {
				return m, nil
			}
			m.screen = screenDetail
			return m.pointDetailAt(m.rows()[m.list.cursor].Name)
		}
		if m.layout() == layoutSplit {
			if msg.Type == tea.KeyTab || msg.Type == tea.KeyShiftTab || msg.String() == "tab" || msg.String() == "shift+tab" || msg.String() == "back_tab" {
				if m.empty() {
					return m, nil
				}
				return m.cycleTab(msg)
			}
		}
		return m, nil
	}
	// Pane focused (split) or detail screen (stack).
	if msg.Type == tea.KeyEsc || msg.Type == tea.KeyBackspace || msg.String() == "esc" || msg.String() == "backspace" {
		m.screen = screenList
		return m, nil
	}
	if msg.Type == tea.KeyTab || msg.Type == tea.KeyShiftTab || msg.String() == "tab" || msg.String() == "shift+tab" || msg.String() == "back_tab" {
		if m.empty() {
			return m, nil
		}
		return m.cycleTab(msg)
	}
	var cmd tea.Cmd
	m.detail.vp, cmd = m.detail.vp.Update(msg)
	if m.detail.active == tabTerminal {
		// The tail rule: at the bottom means following; anywhere else
		// means the human is reading and the refresh must hold still.
		m.detail.follow = m.detail.vp.AtBottom()
	}
	return m, cmd
}

// moveCursor moves the rail cursor by delta, clamped, re-windows, and in
// split layout points the pane at the new binding.
func (m Model) moveCursor(delta int) (tea.Model, tea.Cmd) {
	rows := m.rows()
	if len(rows) == 0 {
		return m, nil
	}
	c := m.list.cursor + delta
	if c < 0 {
		c = 0
	}
	if c > len(rows)-1 {
		c = len(rows) - 1
	}
	m.list.cursor = c
	m.list.sticky = rows[c].Name
	m.list.top = m.railTop()
	if m.layout() == layoutSplit {
		return m.pointDetailAt(rows[c].Name)
	}
	return m, nil
}

// cycleTab is tab / shift+tab.
func (m Model) cycleTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyShiftTab || msg.String() == "shift+tab" || msg.String() == "back_tab" {
		return m.switchTab((m.detail.active - 1 + tabCount) % tabCount)
	}
	return m.switchTab((m.detail.active + 1) % tabCount)
}

func (m Model) switchTab(next tab) (tea.Model, tea.Cmd) {
	m.detail.scroll[m.detail.active] = m.detail.vp.YOffset // park
	m.detail.active = next
	c := m.detail.cache[next]
	m.fillViewport()
	m.detail.vp.SetYOffset(m.detail.scroll[next]) // restore
	if next == tabTerminal && m.detail.follow {
		m.detail.vp.GotoBottom()
	}
	if !c.loaded && !m.tabInFlight {
		m.tabInFlight = true
		lines := m.detail.vp.Height
		if lines < 1 {
			lines = 1
		}
		return m, fetchFor(m.ctx, m.rt, next, m.detail.name,
			m.detail.round, lines)
	}
	return m, nil
}
