package ui

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestEmptyContentNotStyledAsError(t *testing.T) {
	c := tabContent{
		loaded: true,
		empty:  "no diff recorded for round 1 — no baseline captured",
	}

	st := styleFor(c)
	if reflect.DeepEqual(st, errorStyle) {
		t.Fatalf("empty content style must not be errorStyle")
	}
	if st.GetForeground() == errorStyle.GetForeground() {
		t.Fatalf("empty content foreground must not match errorStyle foreground")
	}

	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)
	rendered := bodyOf(tabDiff, c, false)
	errRendered := errorStyle.Render(c.empty)
	if rendered == errRendered {
		t.Fatalf("empty prose must NOT be styled with errorStyle")
	}
}
