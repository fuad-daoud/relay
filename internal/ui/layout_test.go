package ui

import "testing"

// TestLayoutThreshold pins spec §3.1's number with literals on purpose:
// a test that reads splitMinWidth cannot notice it changing.
func TestLayoutThreshold(t *testing.T) {
	m := Model{height: 40}
	m.width = 109
	if m.layout() != layoutStack {
		t.Errorf("109 columns: want stack")
	}
	m.width = 110
	if m.layout() != layoutSplit {
		t.Errorf("110 columns: want split")
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
