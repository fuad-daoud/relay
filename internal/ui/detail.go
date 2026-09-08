package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// chromeHeight is the rows the detail screen spends on furniture: header,
// tab bar, and footer. The viewport gets whatever is left.
const chromeHeight = 4

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

func bodyOf(c tabContent) string {
	if !c.loaded {
		return "loading…"
	}
	if c.err != nil {
		return errorStyle.Render("error: " + c.err.Error())
	}
	if c.empty != "" {
		return emptyStyle.Render(c.empty) // prose, NOT styled as an error
	}
	return normalStyle.Render(c.body)
}

func (m Model) detailView() string {
	var b strings.Builder
	r := row(m.report, m.detail.name)
	round := m.detail.round + 1
	display := ""
	if r != nil {
		round = r.Round
		display = r.Display
	}
	title := fmt.Sprintf("%s · round %d", m.detail.name, round)
	if display != "" {
		title += " · " + display
	}
	b.WriteString(renderBorder(title, m.width))
	b.WriteByte('\n')

	for i, t := range tabTitles {
		if i > 0 {
			b.WriteString("  ")
		}
		if tab(i) == m.detail.active {
			b.WriteString(activeTabStyle.Render("[" + t + "]"))
		} else {
			b.WriteString(inactiveTabStyle.Render(t))
		}
	}
	b.WriteByte('\n')

	b.WriteString(m.detail.vp.View())
	b.WriteByte('\n')

	b.WriteString(renderBorder(m.footer(), m.width))
	return b.String()
}
