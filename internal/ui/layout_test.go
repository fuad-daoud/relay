package ui

import "testing"

func TestLayoutThreshold(t *testing.T) {
	m := Model{height: 40}
	m.width = splitMinWidth - 1
	if m.layout() != layoutStack {
		t.Errorf("%d columns: want stack", m.width)
	}
	m.width = splitMinWidth
	if m.layout() != layoutSplit {
		t.Errorf("%d columns: want split", m.width)
	}
}

func TestPaneGeometry(t *testing.T) {
	m := Model{width: 140, height: 40}
	if got := m.paneWidth(); got != 140-railWidth-railGap {
		t.Errorf("split paneWidth = %d", got)
	}
	m.width = 80
	if got := m.paneWidth(); got != 80 {
		t.Errorf("stack paneWidth = %d", got)
	}
	// 40 rows - header 2 - footer 1 - pane head 5 - tabs 2 - source 2 = 28
	if got := m.viewportHeight(); got != 28 {
		t.Errorf("viewportHeight = %d, want 28", got)
	}
	m.height = 5
	if got := m.viewportHeight(); got != 0 {
		t.Errorf("viewportHeight on a tiny terminal = %d, want 0", got)
	}
}
