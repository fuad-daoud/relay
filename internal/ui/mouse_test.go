package ui

import (
	"errors"
	"strings"
	"testing"
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
	if r, _, _ := m.hit(railWidth, top+1); r != hitNone {
		t.Errorf("the separator column hits nothing, got %v", r)
	}
	b := m.rows()[m.list.cursor]
	head := len(m.paneHead(&b))
	px := railWidth + railGap
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
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), true)
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
