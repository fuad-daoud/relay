package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

func TestTabSpansMatchTheDrawnRow(t *testing.T) {
	m := paneModel(t, threeRows()[0], tabReport)
	words := stripANSI(m.tabBar()[0])
	for i, title := range tabTitles {
		sp := tabSpans()[i]
		if got := words[sp[0]:sp[1]]; got != " "+title+" " {
			t.Errorf("span %d = %q, want %q", i, got, " "+title+" ")
		}
	}
	if tabAt(tabSpans()[tabDiff][0]+1) != tabDiff {
		t.Error("a column inside diff's word must resolve to tabDiff")
	}
	if tabAt(tabSpans()[tabReport][1]) != -1 {
		t.Error("the gap after a word resolves to no tab")
	}
	if tabAt(500) != -1 {
		t.Error("past the words resolves to no tab")
	}
}

func TestHitRegionsSplit(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	top := headerRows // no error block
	if r, _, _ := m.hit(3, 0); r != hitNone {
		t.Errorf("header row hits nothing, got %v", r)
	}
	if r, _, _ := m.hit(3, 39); r != hitNone {
		t.Errorf("footer row hits nothing, got %v", r)
	}
	if r, row, _ := m.hit(3, top+1); r != hitRail || row != 1 {
		t.Errorf("rail cell -> %v row %d", r, row)
	}
	if r, _, _ := m.hit(m.railWidth(), top+1); r != hitNone {
		t.Errorf("the separator column hits nothing, got %v", r)
	}
	b := m.rows()[m.list.cursor]
	head := len(m.paneHead(&b))
	px := m.railWidth() + railGap
	if r, row, col := m.hit(px+5, top+head); r != hitTabs || col != 5 || row != head {
		t.Errorf("tab row -> %v row %d col %d", r, row, col)
	}
	if r, row, _ := m.hit(px+5, top+head+3); r != hitPane || row != head+3 {
		t.Errorf("pane body -> %v row %d", r, row)
	}
	if r, _, _ := m.hit(px+5, top+2); r != hitPane {
		t.Errorf("pane head rows are pane, got %v", r)
	}
}

func TestHitRegionsStack(t *testing.T) {
	m := splitModel(t, 80, 30, threeRows()...)
	if r, _, _ := m.hit(60, headerRows+1); r != hitRail {
		t.Errorf("stack list screen is all rail, got %v", r)
	}
	m.screen = screenDetail
	m.detail.name = m.rows()[0].Name
	b := m.rows()[0]
	head := len(m.paneHead(&b))
	if r, _, _ := m.hit(3, headerRows+head); r != hitTabs {
		t.Errorf("stack detail tab row, got %v", r)
	}
	if r, _, _ := m.hit(3, headerRows+head+4); r != hitPane {
		t.Errorf("stack detail body, got %v", r)
	}
}

func TestHitShiftsUnderAnErrorBlock(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.err = errors.New("herdr: connection refused")
	e := m.errorRows()
	if e < 1 {
		t.Fatal("fixture needs an error block")
	}
	if r, row, _ := m.hit(3, headerRows+e+1); r != hitRail || row != 1 {
		t.Errorf("rail under the error block -> %v row %d", r, row)
	}
}

func TestRailBindingAt(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), true, m.railWidth(), false)
	for i, l := range lines {
		if got := m.railBindingAt(i); got != l.binding {
			t.Errorf("line %d: railBindingAt = %d, tag = %d (%q)", i, got, l.binding, strings.TrimSpace(stripANSI(l.text)))
		}
	}
	if m.railBindingAt(len(lines)+5) != -1 {
		t.Error("past the end resolves to no binding")
	}
	// A scrolled rail: row 0 is m.list.top.
	m.list.top = 3
	if got := m.railBindingAt(0); got != lines[3].binding {
		t.Errorf("scrolled: row 0 -> %d, want %d", got, lines[3].binding)
	}
}

func wheel(x, y int, down bool) tea.MouseMsg {
	b := tea.MouseButtonWheelUp
	if down {
		b = tea.MouseButtonWheelDown
	}
	return tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress}
}

func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

func longBody(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return strings.TrimRight(b.String(), "\n")
}

func TestWheelOverPaneScrollsWithoutMovingTheCursor(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.tabInFlight = false
	m.detail.cache[tabReport] = tabContent{loaded: true, body: longBody(200)}
	m.fillViewport()
	cursor := m.list.cursor
	px, py := m.railWidth()+railGap+10, headerRows+20
	res, _ := m.Update(wheel(px, py, true))
	m = res.(Model)
	if m.detail.vp.YOffset != 3 {
		t.Errorf("one notch down scrolls three lines, got offset %d", m.detail.vp.YOffset)
	}
	if m.list.cursor != cursor || m.screen != screenList {
		t.Error("a wheel over the pane must move neither the cursor nor the focus")
	}
	res, _ = m.Update(wheel(px, py, false))
	if res.(Model).detail.vp.YOffset != 0 {
		t.Error("one notch up scrolls back")
	}
}

func TestWheelOverPaneKeepsTheTerminalFollowRule(t *testing.T) {
	rows := threeRows()
	rows[0].Headless = &relay.HeadlessInfo{PID: 1, LogPath: "/x/001-builder.log"}
	m := splitModel(t, 140, 40, rows...)
	m.tabInFlight = false
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = res.(Model)
	m.tabInFlight = false
	res, _ = m.Update(tabMsg{name: m.detail.name, t: tabTerminal, content: tabContent{loaded: true, body: longBody(200)}})
	m = res.(Model)
	px, py := m.railWidth()+railGap+10, headerRows+20
	res, _ = m.Update(wheel(px, py, false))
	m = res.(Model)
	if m.detail.follow {
		t.Error("a wheel up over the terminal tab stops following")
	}
	for i := 0; i < 100 && !m.detail.vp.AtBottom(); i++ {
		res, _ = m.Update(wheel(px, py, true))
		m = res.(Model)
	}
	if !m.detail.follow {
		t.Error("wheeling back to the bottom resumes following")
	}
}

func TestWheelOverRailMovesTheCursor(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.tabInFlight = false
	res, _ := m.Update(wheel(3, headerRows+1, true))
	m = res.(Model)
	if m.list.cursor != 1 || m.detail.name != m.rows()[1].Name {
		t.Errorf("wheel down over the rail: cursor %d pane %q", m.list.cursor, m.detail.name)
	}
	res, _ = m.Update(wheel(3, headerRows+1, false))
	if res.(Model).list.cursor != 0 {
		t.Error("wheel up over the rail moves back")
	}
}

func TestClickSelectsCardAndTab(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.tabInFlight = false
	// Find the rail line of the third binding's name and click it.
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), true, m.railWidth(), false)
	target := -1
	for i, l := range lines {
		if l.binding == 2 {
			target = i
			break
		}
	}
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // focus the pane first
	m = res.(Model)
	res, cmd := m.Update(click(3, headerRows+target))
	m = res.(Model)
	if m.list.cursor != 2 || m.detail.name != m.rows()[2].Name || cmd == nil {
		t.Errorf("click on a card: cursor %d pane %q cmd %v", m.list.cursor, m.detail.name, cmd != nil)
	}
	if m.screen != screenList {
		t.Error("a click on the rail focuses the rail")
	}
	// Click a header line: nothing changes.
	res, _ = m.Update(click(3, headerRows+0))
	if res.(Model).list.cursor != 2 {
		t.Error("a click on a group header selects nothing")
	}
	// Click the diff tab.
	b := m.rows()[2]
	head := len(m.paneHead(&b))
	sp := tabSpans()[tabDiff]
	m.tabInFlight = false
	res, _ = m.Update(click(m.railWidth()+railGap+sp[0]+1, headerRows+head))
	m = res.(Model)
	if m.detail.active != tabDiff || m.screen != screenDetail {
		t.Errorf("click on the diff tab: active %v screen %v", m.detail.active, m.screen)
	}
	// Click the pane body: focus only.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = res.(Model)
	res, _ = m.Update(click(m.railWidth()+railGap+10, headerRows+head+6))
	if res.(Model).screen != screenDetail {
		t.Error("a click in the pane body focuses the pane")
	}
}

func TestClickOnStackListSelectsWithoutOpening(t *testing.T) {
	m := splitModel(t, 80, 30, threeRows()...)
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), true, m.railWidth(), false)
	target := -1
	for i, l := range lines {
		if l.binding == 1 {
			target = i
			break
		}
	}
	res, _ := m.Update(click(10, headerRows+target))
	m = res.(Model)
	if m.list.cursor != 1 || m.screen != screenList {
		t.Errorf("stack click: cursor %d screen %v", m.list.cursor, m.screen)
	}
}

func TestDragDividerResizesRail(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	sep := m.railWidth()
	y := headerRows + 5
	press := tea.MouseMsg{X: sep, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	res, _ := m.Update(press)
	m = res.(Model)
	if !m.drag {
		t.Fatal("a press on the separator starts a drag")
	}
	res, _ = m.Update(tea.MouseMsg{X: sep + 10, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = res.(Model)
	if m.railCols != sep+10 {
		t.Errorf("motion moves the divider: %d, want %d", m.railCols, sep+10)
	}
	res, _ = m.Update(tea.MouseMsg{X: 3, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = res.(Model)
	if m.railCols != railMin {
		t.Errorf("a drag past the floor clamps: %d", m.railCols)
	}
	res, _ = m.Update(tea.MouseMsg{X: 3, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	m = res.(Model)
	if m.drag {
		t.Error("release ends the drag")
	}
	// Motion without a drag in progress does nothing.
	res, _ = m.Update(tea.MouseMsg{X: 60, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	if res.(Model).railCols != railMin {
		t.Error("stray motion must not move the divider")
	}
	// A press elsewhere is still a click, not a drag.
	res, _ = m.Update(click(railWidthOf(m)+railGap+10, headerRows+20))
	if res.(Model).drag {
		t.Error("a press in the pane is a click")
	}
}

func railWidthOf(m Model) int { return m.railWidth() }
