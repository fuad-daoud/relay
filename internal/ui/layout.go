package ui

// Geometry of the two layouts (spec §3.1). Every number that shapes the
// screen is here and nowhere else.
const (
	splitMinWidth = 110 // columns; at or above, rail + pane
	railWidth     = 34  // columns, including the 1-column selection gutter
	railGap       = 2   // separator column + 1 pad
	headerRows    = 2   // header bar + blank
	footerRows    = 1
	paneHeadRows  = 5 // title, planner, builder, tree, blank
	tabRows       = 2 // tab bar + rule
	sourceRows    = 2 // source line + blank
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
		return m.width - railWidth - railGap
	}
	return m.width
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
