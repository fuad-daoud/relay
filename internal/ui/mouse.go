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

// hit maps a terminal cell to what is drawn there, with the same numbers
// the views use: the body starts under the header and the error block;
// in split layout the rail is the first railWidth columns and the pane
// begins after the separator; in stack layout the screen is one or the
// other. row and col are relative to the region.
func (m Model) hit(x, y int) (region, int, int) {
	top := headerRows + m.errorRows()
	if y < top || y >= top+m.bodyRows() {
		return hitNone, 0, 0
	}
	bodyRow := y - top
	switch {
	case m.layout() == layoutSplit && x < railWidth:
		return hitRail, bodyRow, x
	case m.layout() == layoutSplit && x < railWidth+railGap:
		return hitNone, 0, 0
	case m.layout() == layoutStack && m.screen == screenList:
		return hitRail, bodyRow, x
	}
	col := x
	if m.layout() == layoutSplit {
		col = x - railWidth - railGap
	}
	head := paneHeadRows
	if b := row(m.report, m.detail.name); b != nil {
		head = len(m.paneHead(b))
	}
	if bodyRow == head {
		return hitTabs, bodyRow, col
	}
	return hitPane, bodyRow, col
}

// railBindingAt is the index into rows() of the card drawn on rail line
// row (0 = the first drawn line, m.list.top), or -1 for a header, a gap
// or past the end.
func (m Model) railBindingAt(row int) int {
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), m.screen == screenList)
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
	if msg.Action != tea.MouseActionPress {
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
				m.detail.vp.LineDown(wheelLines)
			} else {
				m.detail.vp.LineUp(wheelLines)
			}
			if m.detail.active == tabTerminal {
				m.detail.follow = m.detail.vp.AtBottom()
			}
			return m, nil
		}
		return m, nil

	case tea.MouseButtonLeft:
		switch r {
		case hitRail:
			i := m.railBindingAt(row)
			if i < 0 {
				return m, nil
			}
			m.screen = screenList
			return m.moveCursor(i - m.list.cursor)
		case hitTabs:
			if !m.paneVisible() {
				return m, nil
			}
			m.screen = screenDetail
			if t := tabAt(col); t >= 0 && t != m.detail.active {
				return m.switchTab(t)
			}
			return m, nil
		case hitPane:
			if m.paneVisible() {
				m.screen = screenDetail
			}
			return m, nil
		}
	}
	return m, nil
}
