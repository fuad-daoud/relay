package ui

import "github.com/charmbracelet/lipgloss"

// The palette is spec §7: every colour an xterm-256 index so a terminal
// theme renders it consistently. Nothing outside this file names a colour.
var (
	fgStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	faintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("239"))
	ruleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	selectedBg = lipgloss.NewStyle().Background(lipgloss.Color("235"))
	headerBar  = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))

	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))

	activeTabStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	inactiveTabStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	emptyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	normalStyle = lipgloss.NewStyle()

	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	diffFileStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))

	stateNeedsYouStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	stateHeldStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	stateActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stateDoneStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	// archivedStyle renders a non-live rail row (scope all, #172): dim and
	// italic, so an archived binding reads as history rather than
	// something a human could act on right now.
	archivedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Italic(true)
)

// stateStyle colours a display word; the four states have four colours and
// anything else renders plain, so a new state is visible before it is
// styled.
func stateStyle(display string) lipgloss.Style {
	switch display {
	case "NEEDS YOU":
		return stateNeedsYouStyle
	case "HELD":
		return stateHeldStyle
	case "ACTIVE":
		return stateActiveStyle
	case "DONE":
		return stateDoneStyle
	}
	return normalStyle
}

// pillStyle is the pane title's state badge: black on the state colour.
func pillStyle(display string) lipgloss.Style {
	fg := stateStyle(display).GetForeground()
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("16")).Background(fg).Padding(0, 1)
}
