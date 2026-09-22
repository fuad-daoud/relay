package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

func railWidthOf(m Model) int { return m.railWidth() }

// TestEmptyFleetClicksDoNotFocusPane pins the guards in mouse.go that keep
// focus out of the pane at zero rows: a click on the old tab-bar row and a
// click in the pane body must both leave the rail focused.
