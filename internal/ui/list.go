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
	// top is the index of the first rendered row. It moves only as far as
	// it must to keep cursor visible, so the list scrolls a row at a time
	// at either edge rather than re-centring on every keystroke.
	top int
}

// listWindow returns where the first rendered row must be for cursor to be
// visible in a window of rows rows over n items, moving top as little as
// possible. rows <= 0 means no limit: the answer is 0 and every row renders.
func listWindow(top, cursor, rows, n int) int {
	// Rule 1: no row budget or everything fits — render from the top.
	if rows <= 0 || n <= rows {
		return 0
	}
	// Rule 2: clamp top into [0, n-rows].
	if top > n-rows {
		top = n - rows
	}
	if top < 0 {
		top = 0
	}
	// Rule 3: cursor scrolled above the window — pull the window up to it.
	if cursor < top {
		top = cursor
	}
	// Rule 4: cursor scrolled below the window — push the window down to it.
	if cursor >= top+rows {
		top = cursor - rows + 1
	}
	// Rule 5: top now keeps cursor visible and moved as little as possible.
	return top
}

// listRows is how many binding rows the list screen can show: the terminal
// height less the header, the footer, and whatever the error block takes.
// Zero before the first WindowSizeMsg, which listWindow reads as no limit.
// Never less than one once a height is known, so the cursor row is always
// drawn even on an absurdly short terminal.
func (m Model) listRows() int {
	if m.height <= 0 {
		return 0
	}
	errLines := 0
	if m.err != nil {
		errLines = strings.Count(renderError(m.err, m.width), "\n") + 1
	}
	if rows := m.height - 2 - errLines; rows >= 1 {
		return rows
	}
	return 1
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
	b.WriteString(headerStyle.Render(renderBorder("relay", m.width)))
	b.WriteByte('\n')

	if m.err != nil {
		b.WriteString(renderError(m.err, m.width))
		b.WriteByte('\n')
	}

	if !m.statusLoaded && m.err != nil {
		b.WriteString("cannot reach herdr — see the error above\n")
	} else if !m.statusLoaded {
		b.WriteString("loading…\n")
	} else if len(m.report.Bindings) == 0 {
		b.WriteString("no bindings\n")
	} else {
		// Recompute the window here on purpose: a render can never hide the cursor even if an Update path forgets to re-window.
		n := len(m.report.Bindings)
		rows := m.listRows()
		start := listWindow(m.list.top, m.list.cursor, rows, n)
		end := n
		if rows > 0 && start+rows < n {
			end = start + rows
		}
		for i := start; i < end; i++ {
			selected := i == m.list.cursor
			b.WriteString(renderListRow(m.report.Bindings[i], selected))
			b.WriteByte('\n')
		}
	}

	b.WriteString(footerStyle.Render(renderBorder(m.footer(), m.width)))
	return b.String()
}
