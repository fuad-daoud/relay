package ui

import (
	"fmt"
	"sort"
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

// needsYouCount is the needs-you phrase the header and the fleet's context
// line share, so the two can never drift: "1 needs you" for one, "N need
// you" otherwise (A3).
func needsYouCount(n int) string {
	if n == 1 {
		return "1 needs you"
	}
	return fmt.Sprintf("%d need you", n)
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
		// A report ready for the human planner needs them exactly as a
		// NEEDS YOU question does, so it counts here too (§4.5).
		if b.Display == "NEEDS YOU" || reportReady(b) {
			n++
		}
	}
	if n > 0 {
		right = append(right, stateNeedsYouStyle.Render("● "+needsYouCount(n)))
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

// keysView is the last row: the global tail and as many of the top view's
// keys as fit, the notices and the refresh failure on the right (§5.3,
// §2.3b). The globals -- ':' command, '? help' and 'q quit'/'esc back' -- are
// always laid out; the view's keys follow in their Keys() order and a key that
// does not fit is dropped whole, never cut, so half a key is never shown. The
// notice side wins as it always has, and '? help' lists every key that was
// dropped.
func (m Model) keysView(env Env) string {
	key := func(k, v string) string { return fgStyle.Render(k) + " " + dimStyle.Render(v) }
	var tail []string
	tail = append(tail, key(":", "command"), key("?", "help"))
	if len(m.stack) > 1 {
		tail = append(tail, key("esc", "back"))
	} else {
		tail = append(tail, key("q", "quit"))
	}
	tailText := strings.Join(tail, "   ")

	var notes []string
	if w := m.workingText(); w != "" {
		notes = append(notes, dimStyle.Render(w))
	}
	if m.notice != "" {
		notes = append(notes, m.noticeStyle().Render(m.notice))
	}
	if m.err != nil {
		notes = append(notes, errorStyle.Render("! refresh failed (retrying)"))
	}
	right := strings.Join(notes, "   ")

	// The right side is laid out first: what is left belongs to the tail,
	// and to the view's keys in their priority order.
	room := env.Width - lipgloss.Width(right) - 1
	if room < 0 {
		room = 0
	}
	avail := room - lipgloss.Width(tailText) - 3 // 3 for the gap before the tail
	var parts []string
	used := 0
	for _, kh := range m.top().Keys() {
		p := key(kh.Key, kh.Help)
		w := lipgloss.Width(p)
		if len(parts) > 0 {
			w += 3
		}
		if used+w > avail {
			break
		}
		parts = append(parts, p)
		used += w
	}
	left := tailText
	if len(parts) > 0 {
		left = strings.Join(parts, "   ") + "   " + tailText
	}

	if lipgloss.Width(left)+1+lipgloss.Width(right) > env.Width {
		room := env.Width - lipgloss.Width(right) - 1
		if room < 0 {
			room = 0
		}
		left = lipgloss.NewStyle().MaxWidth(room).Render(left)
	}
	return fit(spread(left, right, env.Width), env.Width)
}

// body is row 4: the top view's body, or the command box, help overlay or a
// modal overlay when one is up (§5.3).
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
	if m.overlay != nil {
		box := m.overlay.view(env.Width)
		lines := strings.Split(m.top().Body(env, env.Width, bh), "\n")
		start := len(lines) - len(box)
		if start < 0 {
			start = 0
		}
		for i := 0; i < len(box) && start+i < len(lines); i++ {
			lines[start+i] = box[i]
		}
		return strings.Join(fitLines(lines, env.Width, bh), "\n")
	}
	return m.top().Body(env, env.Width, bh)
}

// noticeStyle is the sticky notice's colour: red for an action's error, faint
// for a captured stderr line, amber otherwise (§4.3, §4.4).
func (m Model) noticeStyle() lipgloss.Style {
	switch {
	case m.noticeErr:
		return errorStyle
	case m.noticeFaint:
		return faintStyle
	}
	return stateNeedsYouStyle
}

// workingText is the footer's action indicator: "working: <verb> <key>…" for
// every action in flight, in key order so the line is stable (§4.3).
func (m Model) workingText() string {
	if len(m.running) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m.running))
	for k := range m.running {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, m.running[k]+" "+k+"…")
	}
	return "working: " + strings.Join(parts, ", ")
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
