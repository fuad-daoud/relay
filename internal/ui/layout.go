package ui

import "github.com/charmbracelet/lipgloss"

// Geometry of the two layouts (spec §3.1). Every number that shapes the
// screen is here and nowhere else.
const (
	splitMinWidth = 110 // columns; at or above, rail + pane
	railGap       = 2   // separator column + 1 pad
	headerRows    = 2   // header bar + blank
	footerRows    = 1
	paneHeadRows  = 5 // title, planner, builder, tree, blank
	tabRows       = 2 // tab bar + rule
	sourceRows    = 2 // source line + blank

	railDefault = 34 // columns, including the 1-column selection gutter
	railMin     = 20
	paneMin     = 80 // a hunk's width; the rail never eats into it
	railStep    = 2  // < and > move the divider this much

	railCompact = 18 // the collapsed rail, like a terminal sidebar
)

// layout is which of the two screens the terminal width earns.
type layout int

const (
	layoutSplit layout = iota // rail beside pane
	layoutStack               // drill-down: list, then full-width detail
)

func (m Model) layout() layout {
	if m.width >= splitMinWidth {
		return layoutSplit
	}
	return layoutStack
}

// paneWidth is the columns the pane (and its viewport) gets.
func (m Model) paneWidth() int {
	if m.layout() == layoutSplit {
		return m.width - m.railWidth() - railGap
	}
	return m.width
}

// clampRail keeps the rail between railMin and what leaves the pane its
// paneMin, on the current terminal. On a terminal too narrow for both,
// the floor wins: a rail is useless below railMin, and the split has a
// width threshold anyway.
func (m Model) clampRail(cols int) int {
	max := m.width - railGap - paneMin
	if cols > max {
		cols = max
	}
	if cols < railMin {
		cols = railMin
	}
	return cols
}

// railWidth is the rail's drawn width: the stored preference, clamped to
// the terminal it is drawn on. Zero (a fresh model before prefs) reads as
// the default. Compact collapses the rail to railCompact columns, below
// railMin by design, so it never runs through clampRail.
func (m Model) railWidth() int {
	if m.compact {
		return railCompact
	}
	if m.railCols == 0 {
		return m.clampRail(railDefault)
	}
	return m.clampRail(m.railCols)
}

// railWidthStored is the unclamped preference: railCols, or railDefault
// when zero. A width set on a wide terminal is kept, not squashed by a
// narrower one just because it happened to be read there.
func (m Model) railWidthStored() int {
	if m.railCols == 0 {
		return railDefault
	}
	return m.railCols
}

// bodyRows is the rows between the header and the footer, less whatever
// the error block takes. Zero before the first WindowSizeMsg.
func (m Model) bodyRows() int {
	if m.height <= 0 {
		return 0
	}
	rows := m.height - headerRows - footerRows - m.errorRows()
	if rows < 0 {
		return 0
	}
	return rows
}

// viewportHeight is bodyRows less the pane's own furniture, floored at 0.
// The same in both layouts: the pane head is drawn full-width on the
// detail screen too, so a resize across the threshold changes the
// viewport's width and nothing else.
func (m Model) viewportHeight() int {
	h := m.bodyRows() - paneHeadRows - tabRows - sourceRows
	if h < 0 {
		return 0
	}
	return h
}

// clipName is name when it fits width cells, else its first width-1
// cells and an ellipsis -- the compact rail's one truncation.
func clipName(name string, width int) string {
	if lipgloss.Width(name) <= width {
		return name
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(name) + "…"
}
