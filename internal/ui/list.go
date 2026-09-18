package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
)

type listModel struct {
	cursor int    // index into Model.report.Bindings
	sticky string // binding NAME the cursor is on
	// top is the index of the first rendered row. It moves only as far as
	// it must to keep cursor visible, so the list scrolls a row at a time
	// at either edge rather than re-centring on every keystroke.
	top int
}

// listWindow is railWindow for a one-line cursor; kept for its tests.
func listWindow(top, cursor, rows, n int) int {
	return railWindow(top, cursor, cursor, rows, n)
}

// errorRows is the line count of renderError(m.err, m.width) when m.err !=
// nil, else 0.
func (m Model) errorRows() int {
	if m.err == nil {
		return 0
	}
	return strings.Count(renderError(m.err, m.width), "\n") + 1
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

const maxErrorLines = 8

// renderError wraps err across the terminal width, preserving the newlines the
// message already has, capped at maxErrorLines with a trailing "…" when it
// overflows. Errors from herdr name the command that fixes them, so truncating
// mid-message defeats the point of showing one.
func renderError(err error, width int) string {
	if err == nil {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	rawLines := strings.Split(err.Error(), "\n")
	var wrapped []string
	for _, l := range rawLines {
		wrapped = append(wrapped, wrapLine(l, width)...)
	}
	if len(wrapped) == 0 {
		return ""
	}
	if len(wrapped) > maxErrorLines {
		wrapped = wrapped[:maxErrorLines]
		last := wrapped[maxErrorLines-1]
		ellipsis := "…"
		ew := lipgloss.Width(ellipsis)
		for lipgloss.Width(last)+ew > width && len(last) > 0 {
			runes := []rune(last)
			last = string(runes[:len(runes)-1])
		}
		wrapped[maxErrorLines-1] = last + ellipsis
	}
	return strings.Join(wrapped, "\n")
}

func wrapLine(line string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	words := strings.Split(line, " ")
	var lines []string
	var cur strings.Builder
	curW := 0

	for _, w := range words {
		ww := lipgloss.Width(w)
		if cur.Len() == 0 {
			if ww > width {
				for _, r := range w {
					rw := lipgloss.Width(string(r))
					if curW+rw > width && cur.Len() > 0 {
						lines = append(lines, cur.String())
						cur.Reset()
						curW = 0
					}
					cur.WriteRune(r)
					curW += rw
				}
			} else {
				cur.WriteString(w)
				curW = ww
			}
		} else {
			if curW+1+ww <= width {
				cur.WriteByte(' ')
				cur.WriteString(w)
				curW += 1 + ww
			} else {
				lines = append(lines, cur.String())
				cur.Reset()
				curW = 0
				if ww > width {
					for _, r := range w {
						rw := lipgloss.Width(string(r))
						if curW+rw > width && cur.Len() > 0 {
							lines = append(lines, cur.String())
							cur.Reset()
							curW = 0
						}
						cur.WriteRune(r)
						curW += rw
					}
				} else {
					cur.WriteString(w)
					curW = ww
				}
			}
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

func (m Model) listView() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	if m.err != nil {
		b.WriteString(renderError(m.err, m.width))
		b.WriteByte('\n')
	}
	b.WriteString(m.railView(m.width))
	b.WriteByte('\n')
	b.WriteString(m.footerView())
	return b.String()
}
