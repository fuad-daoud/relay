package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

const maxErrorLines = 8

// renderError wraps err across the terminal width, preserving the newlines
// the message already has, capped at maxErrorLines with a trailing "…" when
// it overflows. Moved here from list.go (R2.7).
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

// wrapLine word-wraps one line to width. Moved here from list.go (R2.7).
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

// errorRows is the line count of renderError(err, width) when err != nil,
// else 0.
func errorRows(err error, width int) int {
	if err == nil {
		return 0
	}
	return strings.Count(renderError(err, width), "\n") + 1
}

// bodyHeight is the shell's body height: height minus the header and the
// context row, the error block, and the rule and keys rows (§5.3).
func bodyHeight(env Env) int {
	h := env.Height - 2 - env.ErrRows - 2
	if h < 0 {
		return 0
	}
	return h
}

// headerView is row 1: the breadcrumb on the left, attention and the clock
// on the right, on the header bar (§5.3).
func (m Model) headerView(env Env) string {
	left := lipgloss.NewStyle().Bold(true).Render(" relevo")
	for _, v := range m.stack {
		for _, c := range v.Crumbs() {
			left += " › " + c
		}
	}

	var right []string
	n := 0
	for _, b := range env.Report.Bindings {
		if b.Display == "NEEDS YOU" {
			n++
		}
	}
	if n == 1 {
		right = append(right, stateNeedsYouStyle.Render("● 1 needs you"))
	} else if n > 1 {
		right = append(right, stateNeedsYouStyle.Render(fmt.Sprintf("● %d need you", n)))
	}
	for _, g := range env.Report.Gated {
		right = append(right, stateNeedsYouStyle.Render(fmt.Sprintf("%s gated %s", g.Token, relevo.GateUntilText(g.Until))))
	}
	right = append(right, dimStyle.Render(env.Now.Local().Format("15:04")+" "))

	bar := headerBar.Render(fit(spread(left, strings.Join(right, "  ·  "), env.Width), env.Width))
	return bar
}

// contextView is row 2: the top view's context, spread to width (§5.3).
func (m Model) contextView(env Env) string {
	left, right := m.top().Context(env)
	return fit(spread(left, right, env.Width), env.Width)
}

// errorBlock is the optional rows under the context: the refresh error,
// wrapped and capped (§5.3).
func (m Model) errorBlock(env Env) string {
	return renderError(m.err, env.Width)
}

// ruleView is the full-width rule above the keys row (§5.3).
func (m Model) ruleView(env Env) string {
	if env.Width <= 0 {
		return ""
	}
	return ruleStyle.Render(strings.Repeat("─", env.Width))
}

// keysView is the last row: the top view's keys and the globals on the
// left, notices and the refresh failure on the right, the right winning on
// overlap (§5.3).
func (m Model) keysView(env Env) string {
	key := func(k, v string) string { return fgStyle.Render(k) + " " + dimStyle.Render(v) }
	var parts []string
	for _, kh := range m.top().Keys() {
		parts = append(parts, key(kh.Key, kh.Help))
	}
	parts = append(parts, key(":", "command"), key("?", "help"))
	if len(m.stack) > 1 {
		parts = append(parts, key("esc", "back"))
	} else {
		parts = append(parts, key("q", "quit"))
	}
	left := strings.Join(parts, "   ")

	var notes []string
	if m.notice != "" {
		notes = append(notes, stateNeedsYouStyle.Render(m.notice))
	}
	if m.err != nil {
		notes = append(notes, errorStyle.Render("! refresh failed (retrying)"))
	}
	right := strings.Join(notes, "   ")
	if lipgloss.Width(left)+1+lipgloss.Width(right) > env.Width {
		room := env.Width - lipgloss.Width(right) - 1
		if room < 0 {
			room = 0
		}
		left = lipgloss.NewStyle().MaxWidth(room).Render(left)
	}
	return fit(spread(left, right, env.Width), env.Width)
}

// body is row 4: the top view's body, or the command box or help overlay
// when either is up (§5.3).
func (m Model) body(env Env) string {
	bh := bodyHeight(env)
	if m.cmd.open {
		box := m.cmdBox(env)
		lines := strings.Split(m.top().Body(env, env.Width, bh), "\n")
		for i := 0; i < len(box) && i < len(lines); i++ {
			lines[i] = box[i]
		}
		return strings.Join(fitLines(lines, env.Width, bh), "\n")
	}
	if m.help {
		return m.helpBody(env, bh)
	}
	return m.top().Body(env, env.Width, bh)
}

// cmdBox is the command line's box: the input, then one line per match
// (§5.3). Its length is min(len(matches)+2, 10).
func (m Model) cmdBox(env Env) []string {
	ms := m.cmd.matches(env)
	n := len(ms) + 2
	if n > 10 {
		n = 10
	}
	box := make([]string, 0, n)
	box = append(box, m.cmd.input.View())
	for i, cand := range ms {
		if len(box) >= n {
			break
		}
		marker := "  "
		if i == m.cmd.sel {
			marker = accentStyle.Render("▸ ")
		}
		box = append(box, marker+accentStyle.Render(cand.name)+"  "+dimStyle.Render(cand.help))
	}
	for len(box) < n {
		box = append(box, "")
	}
	return box
}

// globalKeys are the shell's own keys, shown first in the help overlay.
var globalKeys = []KeyHelp{
	{":", "command"},
	{"/", "filter"},
	{"?", "help"},
	{"esc", "back"},
	{"q", "quit / back"},
}

// helpBody is the help overlay: the global section, then the top view's
// keys, as two columns (§5.3).
func (m Model) helpBody(env Env, height int) string {
	line := func(kh KeyHelp) string {
		return fgStyle.Render(pad(kh.Key, 12)) + " " + dimStyle.Render(kh.Help)
	}
	lines := []string{accentStyle.Render("global")}
	for _, kh := range globalKeys {
		lines = append(lines, line(kh))
	}
	lines = append(lines, "")
	lines = append(lines, accentStyle.Render("view"))
	for _, kh := range m.top().Keys() {
		lines = append(lines, line(kh))
	}
	return strings.Join(fitLines(lines, env.Width, height), "\n")
}
