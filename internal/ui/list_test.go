package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/muesli/termenv"
)

func TestListWindow(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		top, cursor, rows, n int
		want                 int
	}{
		{"no limit", 0, 0, 0, 10, 0},
		{"fits", 0, 3, 5, 3, 0},
		{"cursor below", 0, 7, 5, 10, 3},
		{"cursor above", 6, 2, 5, 10, 2},
		{"already visible", 3, 4, 5, 10, 3},
		{"clamped from past the end", 9, 4, 5, 10, 4},
		{"last row", 0, 9, 5, 10, 5},
	} {
		if got := listWindow(tc.top, tc.cursor, tc.rows, tc.n); got != tc.want {
			t.Errorf("%s: listWindow(%d, %d, %d, %d) = %d, want %d",
				tc.name, tc.top, tc.cursor, tc.rows, tc.n, got, tc.want)
		}
	}
}

// tenBindings builds a model at the given height showing b00..b09.
// bindingStatuses builds Display:"ACTIVE" bindings named b00..b{count-1}.
func bindingStatuses(count int) []relay.BindingStatus {
	bs := make([]relay.BindingStatus, count)
	for i := range bs {
		bs[i] = relay.BindingStatus{Name: fmt.Sprintf("b%02d", i), Display: "ACTIVE"}
	}
	return bs
}

// press sends r as a KeyMsg through Update and returns the updated model.
func press(t *testing.T, m Model, r rune) Model {
	t.Helper()
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return res.(Model)
}

// assertCursorVisible fails t unless the list view shows the cursor row for
// name, the header, the footer, and exactly m.height-1 newlines. The height is
// read off the model so the same helper works after a resize.
func assertCursorVisible(t *testing.T, m Model, name string) {
	t.Helper()
	view := m.View()
	if !strings.Contains(plain(view), "▎ "+name) {
		t.Errorf("view must show the cursor card for %s, got:\n%s", name, view)
	}
	if !strings.Contains(plain(view), "relay") {
		t.Errorf("view must contain the header, got:\n%s", view)
	}
	if !strings.Contains(view, "open") {
		t.Errorf("view must contain the footer, got:\n%s", view)
	}
	if got := strings.Count(m.View(), "\n"); got != m.height-1 {
		t.Errorf("view must have %d newlines at height %d, got %d:\n%s",
			m.height-1, m.height, got, view)
	}
}

func TestStateStylesDistinguishable(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	states := []string{"NEEDS YOU", "HELD", "ACTIVE", "DONE"}
	rendered := make(map[string]string, len(states))
	for _, s := range states {
		rendered[s] = stateStyle(s).Render(s)
	}
	for i, a := range states {
		for _, b := range states[i+1:] {
			if rendered[a] == rendered[b] {
				t.Fatalf("state display styles must be distinguishable:\n%s: %q\n%s: %q", a, rendered[a], b, rendered[b])
			}
		}
	}
	if stateStyle("HELD").Render("HELD") == normalStyle.Render("HELD") {
		t.Fatalf("HELD must be styled, not left as normalStyle (that was the old regression)")
	}
	// The selected-card gutter styling is pinned by TestCardLinesShapes'
	// "blocked" case ("▎ webshop"); renderListRow/cursorStyle are gone
	// (Task 3).
}
