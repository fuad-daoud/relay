package ui

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenList:
		switch msg.String() {
		case "up", "k":
			if m.list.cursor > 0 {
				m.list.cursor--
				if len(m.report.Bindings) > 0 {
					m.list.sticky = m.report.Bindings[m.list.cursor].Name
				}
			}
			return m, nil
		case "down", "j":
			if m.list.cursor < len(m.report.Bindings)-1 {
				m.list.cursor++
				m.list.sticky = m.report.Bindings[m.list.cursor].Name
			}
			return m, nil
		case "enter":
			if len(m.report.Bindings) == 0 {
				return m, nil
			}
			row := m.report.Bindings[m.list.cursor]
			vpHeight := m.height - chromeHeight
			if vpHeight < 0 {
				vpHeight = 0
			}
			vp := viewport.New(m.width, vpHeight)
			vp.SetContent(bodyOf(tabContent{}))
			m.detail = detailModel{
				name:   row.Name,
				round:  row.Round - 1,
				active: tabReport,
				vp:     vp,
			}
			if row.Last != nil {
				m.detail.lastLogTS = row.Last.TS
			}
			m.screen = screenDetail
			if !m.tabInFlight {
				m.tabInFlight = true
				return m, fetchReport(m.ctx, m.rt, row.Name)
			}
			return m, nil
		}

	case screenDetail:
		if msg.Type == tea.KeyEsc || msg.Type == tea.KeyBackspace || msg.String() == "esc" || msg.String() == "backspace" {
			m.screen = screenList
			return m, nil
		}
		if msg.Type == tea.KeyShiftTab || msg.String() == "shift+tab" || msg.String() == "back_tab" {
			next := (m.detail.active - 1 + tabCount) % tabCount
			return m.switchTab(next)
		}
		if msg.Type == tea.KeyTab || msg.String() == "tab" {
			next := (m.detail.active + 1) % tabCount
			return m.switchTab(next)
		}

		switch msg.String() {
		case "1":
			return m.switchTab(tabReport)
		case "2":
			return m.switchTab(tabTerminal)
		case "3":
			return m.switchTab(tabDiff)
		case "4":
			return m.switchTab(tabLog)
		default:
			var cmd tea.Cmd
			m.detail.vp, cmd = m.detail.vp.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

func (m Model) switchTab(next tab) (tea.Model, tea.Cmd) {
	m.detail.scroll[m.detail.active] = m.detail.vp.YOffset // park
	m.detail.active = next
	c := m.detail.cache[next]
	m.detail.vp.SetContent(bodyOf(c))
	m.detail.vp.SetYOffset(m.detail.scroll[next]) // restore
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
