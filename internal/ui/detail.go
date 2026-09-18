package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

type detailModel struct {
	name      string // binding under inspection
	round     int    // newest COMPLETED round: row.Round - 1
	active    tab
	vp        viewport.Model // the live viewport; only ever shows `active`
	scroll    [tabCount]int  // parked offsets for INACTIVE tabs
	cache     [tabCount]tabContent
	lastLogTS time.Time // invalidation key -- see §5 rule 3

	// headless is true when the builder is headless (row.Headless != nil).
	// It selects the terminal tab's source (the round log rather than a
	// captured pane) and, from #180, its styling.
	headless bool

	// follow is the terminal tab's tail rule (spec §3.4): while true the
	// viewport is pinned to the bottom on every refresh; scrolling up clears
	// it, scrolling back to the bottom sets it. True on every re-point.
	follow bool
}

func styleFor(c tabContent) lipgloss.Style {
	if c.err != nil {
		return errorStyle
	}
	if c.empty != "" {
		return emptyStyle
	}
	return normalStyle
}

// bodyOf renders a tab's content for the viewport. The diff tab colours its
// body (colourDiff); a headless builder's terminal tab colours transcript
// markers (colourTranscript, #180); everything else returns c.body as
// before.
func bodyOf(t tab, c tabContent, headless bool) string {
	if !c.loaded {
		return "loading…"
	}
	st := styleFor(c)
	if c.err != nil {
		return st.Render("error: " + c.err.Error())
	}
	if c.empty != "" {
		return st.Render(c.empty) // prose, NOT styled as an error
	}
	if t == tabDiff {
		return colourDiff(c.body)
	}
	if t == tabTerminal && headless {
		return colourTranscript(c.body)
	}
	return st.Render(c.body)
}

// wrapBody word-wraps body to width so the viewport's logical lines are its
// visual lines. The viewport pads with lipgloss.Width itself, which wraps
// anything wider and then cuts the overflow with MaxHeight -- rows under a
// long line fall off the bottom where no scroll reaches them. Wrapping
// first, to the same width, is the whole fix. width <= 0 returns body.
func wrapBody(body string, width int) string {
	if width <= 0 || body == "" {
		return body
	}
	return lipgloss.NewStyle().Width(width).Render(body)
}

func (m Model) detailView() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	b.WriteString(m.paneView(m.width))
	b.WriteByte('\n')
	b.WriteString(m.footerView())
	return b.String()
}
