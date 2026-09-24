package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Keys that work from either focus.
	switch msg.String() {
	case "s":
		m.sort = !m.sort
		m.list.resolveStickyRows(m.railRows())
		m.list.top = m.railTop()
		return m, m.save()
	case "a":
		// Toggle first, save, then guard: a scope that lands on all with
		// no database reverts to live and leaves a sticky notice instead
		// of ever failing the toggle or exiting (§5.8, §6).
		if m.scope == scopeLive {
			m.scope = scopeAll
		} else {
			m.scope = scopeLive
		}
		cmds := []tea.Cmd{m.save()}
		if m.scope == scopeAll && m.src.Base().DB == nil {
			m.scope = scopeLive
			m.notice = fmt.Sprintf("no database: %v", relevo.ErrNoDatabase)
			return m, tea.Batch(cmds...)
		}
		m.statusInFlight = true
		cmds = append(cmds, fetchStatus(m.ctx, m.src, m.scope, m.opts.Here))
		return m, tea.Batch(cmds...)
	case "d":
		// The dashboard screen (§4): enter, and leave again the same key
		// while it is up (updateDashKeys routes that).
		return m.enterDash()
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
		m.pane.detail.vp.Width = m.paneWidth()
		m.fillViewport()
		m.list.top = m.railTop()
		return m, m.save()
	case "1", "2", "3", "4", "5":
		if m.pane.detail.name == "" {
			return m, nil
		}
		if m.paneVisible() {
			return m.switchTab(tab(msg.String()[0] - '1'))
		}
	case "[", "]":
		if m.pane.detail.name == "" {
			return m, nil
		}
		if m.paneVisible() {
			delta := 1
			if msg.String() == "[" {
				delta = -1
			}
			return m.stepRound(delta)
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
			rows := m.railRows()
			if len(rows) == 0 {
				return m, nil
			}
			return m.pointAtRow(rows[m.list.cursor])
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
	m.pane.detail.vp, cmd = m.pane.detail.vp.Update(msg)
	if m.pane.detail.active == tabTerminal {
		// The tail rule: at the bottom means following; anywhere else
		// means the human is reading and the refresh must hold still.
		m.pane.detail.follow = m.pane.detail.vp.AtBottom()
	}
	return m, cmd
}

// moveCursor moves the rail cursor by delta, clamped, re-windows, and in
// split layout points the pane at the new binding.
func (m Model) moveCursor(delta int) (tea.Model, tea.Cmd) {
	rows := m.railRows()
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
	m.list.sticky = rows[c].name()
	m.list.top = m.railTop()
	if m.layout() == layoutSplit {
		return m.pointAtRow(rows[c])
	}
	return m, nil
}

// cycleTab and switchTab now live on roundPane (§5.1); Model keeps
// wrappers of both names in model.go.
