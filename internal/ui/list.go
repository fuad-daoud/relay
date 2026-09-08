package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

type listModel struct {
	cursor int    // index into Model.report.Bindings
	sticky string // binding NAME the cursor is on
}

// resolveSticky re-points cursor at the binding named by sticky after the
// list has changed, clamping and re-pointing sticky if it is gone.
func (l *listModel) resolveSticky(rep relay.Report) {
	if len(rep.Bindings) == 0 {
		l.cursor = 0
		l.sticky = ""
		return
	}

	if l.sticky != "" {
		for i, b := range rep.Bindings {
			if b.Name == l.sticky {
				l.cursor = i
				return
			}
		}
	}

	if l.cursor >= len(rep.Bindings) {
		l.cursor = len(rep.Bindings) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	l.sticky = rep.Bindings[l.cursor].Name
}

func renderBorder(title string, width int) string {
	if width <= 0 {
		width = 80
	}
	prefix := "+- " + title + " "
	prefixWidth := lipgloss.Width(prefix)
	if prefixWidth >= width {
		if width <= 5 {
			return strings.Repeat("-", width)
		}
		maxTitleWidth := width - 5
		var tr strings.Builder
		curW := 0
		for _, r := range title {
			rw := lipgloss.Width(string(r))
			if curW+rw > maxTitleWidth {
				break
			}
			tr.WriteRune(r)
			curW += rw
		}
		rem := width - 5 - curW
		return "+- " + tr.String() + " " + strings.Repeat("-", rem) + "+"
	}
	return prefix + strings.Repeat("-", width-prefixWidth-1) + "+"
}

func styleDisplay(display string) string {
	switch display {
	case "NEEDS YOU":
		return stateNeedsYouStyle.Render(fmt.Sprintf("%-9s", display))
	case "DONE":
		return stateDoneStyle.Render(fmt.Sprintf("%-9s", display))
	case "ACTIVE":
		return stateActiveStyle.Render(fmt.Sprintf("%-9s", display))
	default:
		return fmt.Sprintf("%-9s", display)
	}
}

func renderListRow(b relay.BindingStatus, selected bool) string {
	var cursorStr string
	if selected {
		cursorStr = cursorStyle.Render(">")
	} else {
		cursorStr = " "
	}
	pending := "--"
	if b.Pending != nil {
		if b.Pending.Kind == store.KindReport {
			pending = fmt.Sprintf("report r%d", b.Pending.Round)
		} else {
			pending = string(b.Pending.Kind)
		}
	}
	disp := styleDisplay(b.Display)
	return strings.TrimRight(fmt.Sprintf("%s%-14s %-3s r%-2d %s builder %-3s %-7s pending %s",
		cursorStr, b.Name, b.Workspace, b.Round, disp, b.BuilderAlias, b.BuilderStatus, pending), " ")
}

func (m Model) listView() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(renderBorder("relay", m.width)))
	b.WriteByte('\n')

	if len(m.report.Bindings) == 0 {
		b.WriteString("no bindings\n")
	} else {
		for i, binding := range m.report.Bindings {
			selected := i == m.list.cursor
			b.WriteString(renderListRow(binding, selected))
			b.WriteByte('\n')
		}
	}

	b.WriteString(footerStyle.Render(renderBorder(m.footer(), m.width)))
	return b.String()
}
