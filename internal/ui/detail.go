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

// bodyOf renders a tab's content for the viewport. Only the diff tab
// colours its body (colourDiff); the other three return c.body as before.
func bodyOf(t tab, c tabContent) string {
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
	return st.Render(c.body)
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
