package ui

import (
	"fmt"
	"strings"

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
	if len(prefix) >= width {
		return prefix
	}
	return prefix + strings.Repeat("-", width-len(prefix)-1) + "+"
}

func renderListRow(b relay.BindingStatus, selected bool) string {
	cursorChar := ' '
	if selected {
		cursorChar = '>'
	}
	pending := "--"
	if b.Pending != nil {
		if b.Pending.Kind == store.KindReport {
			pending = fmt.Sprintf("report r%d", b.Pending.Round)
		} else {
			pending = string(b.Pending.Kind)
		}
	}
	return strings.TrimRight(fmt.Sprintf("%c%-14s %-3s r%-2d %-9s builder %-3s %-7s pending %s",
		cursorChar, b.Name, b.Workspace, b.Round, b.Display, b.BuilderAlias, b.BuilderStatus, pending), " ")
}

func (m Model) listView() string {
	var b strings.Builder
	b.WriteString(renderBorder("relay", m.width))
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

	b.WriteString(renderBorder(m.footer(), m.width))
	return b.String()
}
