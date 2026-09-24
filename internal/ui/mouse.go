package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// region is what a terminal cell shows, for hit-testing (spec §6.1).
type region int

const (
	hitNone region = iota
	hitRail        // a rail line; row is the index into the drawn rail
	hitTabs        // the tab row; col is the column within the pane
	hitPane        // the pane body: head, source, viewport
)

// tabSpans is each tab word's [start, end) column in the tab row, in tab
// order. tabBar draws from it and hit reads it, so a click lands where the
// word is drawn by construction.
func tabSpans() [tabCount][2]int {
	var out [tabCount][2]int
	col := 0
	for i, t := range tabTitles {
		w := lipgloss.Width(" " + t + " ")
		out[i] = [2]int{col, col + w}
		col += w + 2 // the two-space join
	}
	return out
}

// tabAt is the tab whose word covers col, or -1.
func tabAt(col int) tab {
	for i, sp := range tabSpans() {
		if col >= sp[0] && col < sp[1] {
			return tab(i)
		}
	}
	return -1
}

// hitBody reports whether y falls in the body rows, between the header
// (and error block) and the footer. Shared by hit and the divider's
// press detection.
func (m Model) hitBody(y int) bool {
	top := headerRows + m.errorRows()
	return y >= top && y < top+m.bodyRows()
}

// hit maps a terminal cell to what is drawn there, with the same numbers
// the views use: the body starts under the header and the error block;
// in split layout the rail is the first railWidth() columns and the pane
// begins after the separator; in stack layout the screen is one or the
// other. row and col are relative to the region.
func (m Model) hit(x, y int) (region, int, int) {
	if !m.hitBody(y) {
		return hitNone, 0, 0
	}
	top := headerRows + m.errorRows()
	bodyRow := y - top
	switch {
	case m.layout() == layoutSplit && x < m.railWidth():
		return hitRail, bodyRow, x
	case m.layout() == layoutSplit && x < m.railWidth()+railGap:
		return hitNone, 0, 0
	case m.layout() == layoutStack && m.screen == screenList:
		return hitRail, bodyRow, x
	}
	col := x
	if m.layout() == layoutSplit {
		col = x - m.railWidth() - railGap
	}
	head := paneHeadRows
	if b := row(m.report, m.pane.detail.name); b != nil {
		head = len(m.paneHead(b))
	}
	if bodyRow == head {
		return hitTabs, bodyRow, col
	}
	return hitPane, bodyRow, col
}

// railBindingAt is the index into railRows() of the card drawn on rail
// line row (0 = the first drawn line, m.list.top), or -1 for a header, a
// gap or past the end.
func (m Model) railBindingAt(row int) int {
	lines := railLinesAll(m.railRows(), m.list.cursor, m.sort, m.now(), m.screen == screenList, m.railWidth(), m.compact)
	i := m.list.top + row
	if i < 0 || i >= len(lines) {
		return -1
	}
	return lines[i].binding
}

// wheelLines is how far one wheel notch scrolls the pane -- the viewport's
// own MouseWheelDelta, so a wheel feels the same here as in any bubbles
// pager.
const wheelLines = 3

// updateMouse is spec §6.1's table: the wheel scrolls what is under the
// pointer, a click selects what is under it, focus follows the click.
func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Action {
	case tea.MouseActionMotion:
		if m.drag {
			return m.setRail(msg.X)
		}
		return m, nil
	case tea.MouseActionRelease:
		if m.drag {
			m.drag = false
			return m, m.save()
		}
		return m, nil
	case tea.MouseActionPress:
	default:
		return m, nil
	}
	r, row, col := m.hit(msg.X, msg.Y)
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		down := msg.Button == tea.MouseButtonWheelDown
		switch r {
		case hitRail:
			if down {
				return m.moveCursor(+1)
			}
			return m.moveCursor(-1)
		case hitPane, hitTabs:
			if !m.paneVisible() {
				return m, nil
			}
			if down {
				m.pane.detail.vp.LineDown(wheelLines)
			} else {
				m.pane.detail.vp.LineUp(wheelLines)
			}
			if m.pane.detail.active == tabTerminal {
				m.pane.detail.follow = m.pane.detail.vp.AtBottom()
			}
			return m, nil
		}
		return m, nil

	case tea.MouseButtonLeft:
		if !m.compact && m.layout() == layoutSplit && msg.X >= m.railWidth() && msg.X < m.railWidth()+railGap && m.hitBody(msg.Y) {
			m.drag = true
			return m, nil
		}
		switch r {
		case hitRail:
			i := m.railBindingAt(row)
			if i < 0 {
				return m, nil
			}
			m.screen = screenList
			return m.moveCursor(i - m.list.cursor)
		case hitTabs:
			if m.empty() {
				return m, nil
			}
			if !m.paneVisible() {
				return m, nil
			}
			m.screen = screenDetail
			if t := tabAt(col); t >= 0 && t != m.pane.detail.active {
				return m.switchTab(t)
			}
			return m, nil
		case hitPane:
			if m.empty() {
				return m, nil
			}
			if m.paneVisible() {
				m.screen = screenDetail
			}
			return m, nil
		}
	}
	return m, nil
}
