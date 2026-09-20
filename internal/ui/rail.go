package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/usage"
)

// railLine is one rendered rail row tagged with the binding it belongs
// to, so the window can keep a whole card visible. Headers and gap lines
// carry -1.
type railLine struct {
	text    string
	binding int
}

// sep joins card facts with a faint middle dot.
var sep = faintStyle.Render(" · ")

// fit pads or truncates s to width cells. Truncation is by cell, no
// ellipsis: at 34 columns an ellipsis costs more than it says.
func fit(s string, width int) string {
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// ago is a coarse age for a card: minutes under an hour, hours under a
// day, then days. "" for a zero time, so a caller can omit the fact.
func ago(since, now time.Time) string {
	if since.IsZero() {
		return ""
	}
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// whatAge is a card's second line: what the binding is on, and for how
// long, from the fields Status has today (spec §3.3 table). #135's labels
// and #137's PAUSED join here.
func whatAge(b relay.BindingStatus, now time.Time) (what, age string) {
	switch b.Display {
	case "NEEDS YOU":
		if b.Waiting != nil {
			what = b.Waiting.Cause
			if what == "blocked" {
				what = "question"
			}
			age = ago(b.Waiting.Since, now)
		} else if b.Detail != "" {
			what = b.Detail
		} else {
			what = "needs you"
		}
	case "HELD":
		if b.Pending != nil {
			what = fmt.Sprintf("%s r%d", b.Pending.Kind, b.Pending.Round)
		}
		age = relay.HoldText(b)
	case "ACTIVE":
		what = b.BuilderStatus
		if b.Nudge != nil {
			age = relay.NudgeText(*b.Nudge)
		}
	case "DONE":
		what = "done"
		if b.Last != nil {
			age = ago(b.Last.TS, now)
		}
	}
	return what, age
}

// facts is a card's optional third line: tree and round facts, in a fixed
// order, only those that apply. #143's live +N −M goes first here.
func facts(b relay.BindingStatus) []string {
	var out []string
	if b.Dirty {
		out = append(out, stateNeedsYouStyle.Render("dirty"))
	}
	if b.Consults > 0 {
		out = append(out, dimStyle.Render(fmt.Sprintf("%d consults", b.Consults)))
	}
	if b.Switches > 0 {
		out = append(out, dimStyle.Render(fmt.Sprintf("switched %dx", b.Switches)))
	}
	if b.ForkedFrom != "" {
		out = append(out, dimStyle.Render(fmt.Sprintf("forked from %s r%d", b.ForkedFrom, b.ForkedAtRound)))
	}
	if b.Spend != nil {
		if s := usage.MoneyShort(*b.Spend); s != "" {
			out = append(out, dimStyle.Render(s))
		}
	}
	// A running round's figure is its own fact, last (#234): separate from
	// the spend, never summed into it, cut by the width rule if at all.
	if b.LiveUsage != nil {
		if s := usage.LiveShort(*b.LiveUsage); s != "" {
			out = append(out, dimStyle.Render(s))
		}
	}
	return out
}

// cardLines renders one binding's card: three or four lines, each exactly
// railWidth wide. selected paints the gutter and background; showState
// puts the state word on line 2 (name order has no group headers). focused
// is "the rail has focus": the gutter is lit accent only when the card is
// both selected and the rail is focused, dim when selected but the pane
// has focus instead, so a human can tell which side is listening.
func cardLines(b relay.BindingStatus, selected, showState bool, now time.Time, focused bool, width int) []string {
	gutter := " "
	if selected && focused {
		gutter = accentStyle.Render("▎")
	} else if selected {
		gutter = dimStyle.Render("▎")
	}
	const unreadSlot = "  " // reserved for #143's ● -- keep the width
	nameStyle := fgStyle.Bold(true)
	if selected {
		nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	}
	round := dimStyle.Render(fmt.Sprintf("r%d", b.Round))
	nameWidth := width - 1 - 2 - lipgloss.Width(round) - 1 // gutter, slot, round, pad
	name := nameStyle.Render(fit(b.Name, nameWidth))
	l1 := gutter + unreadSlot + name + round + " "

	what, age := whatAge(b, now)
	var l2 []string
	if showState {
		l2 = append(l2, stateStyle(b.Display).Render(b.Display))
	}
	if what != "" {
		if showState {
			l2 = append(l2, dimStyle.Render(what))
		} else {
			l2 = append(l2, fgStyle.Render(what))
		}
	}
	if age != "" {
		l2 = append(l2, dimStyle.Render(age))
	}

	var l4 []string
	if b.BuilderKind != "" {
		l4 = append(l4, dimStyle.Render(b.BuilderKind))
	}
	if b.Headless != nil {
		l4 = append(l4, dimStyle.Render("headless"))
	}
	if b.Branch != "" {
		l4 = append(l4, dimStyle.Render(b.Branch))
	}

	// The gutter runs down every line of the selected card, so the bar
	// marks the card and not just its name.
	indent := gutter + "  "
	lines := []string{l1, indent + strings.Join(l2, sep)}
	if f := facts(b); len(f) > 0 {
		lines = append(lines, indent+strings.Join(f, sep))
	}
	lines = append(lines, indent+strings.Join(l4, sep))

	for i, l := range lines {
		l = fit(l, width)
		if selected {
			l = selectedBg.Render(l)
		}
		lines[i] = l
	}
	return lines
}

// compactLine is a binding's one-line card (spec §6.0, amended round 6):
// gutter, unread slot, name clipped to fit, then round. No what · age
// qualifier -- that pair is cards-only.
func compactLine(b relay.BindingStatus, selected, showState bool, now time.Time, focused bool, width int) string {
	gutter := " "
	if selected {
		if focused {
			gutter = accentStyle.Render("▎")
		} else {
			gutter = dimStyle.Render("▎")
		}
	}
	const unreadSlot = "  "
	roundW := 3
	nameW := width - 1 - 2 - 1 - roundW - 1
	nameStyle := fgStyle.Bold(true)
	switch {
	case selected:
		nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	case showState:
		nameStyle = stateStyle(b.Display).Bold(true)
	}
	name := nameStyle.Render(clipName(b.Name, nameW))
	round := dimStyle.Render(fmt.Sprintf("r%d", b.Round))
	l := fit(gutter+unreadSlot+fit(name, nameW)+" "+round+" ", width)
	if selected {
		l = selectedBg.Render(l)
	}
	return l
}

// groupOrder is the attention order of the rail's headers.
var groupOrder = []string{"NEEDS YOU", "HELD", "ACTIVE", "DONE"}

// railLines lays out every card. In attention order (which SortRows has
// already applied to rows) a header opens each state group and a blank
// line closes it; in name order it is cards back to back with the state
// on each. cursor is the index in rows of the selected binding. focused is
// "the rail has focus" (m.screen == screenList), threaded through to
// cardLines so the selected card's gutter dims when the pane has focus.
// compact emits compactLine's single tagged line per binding instead.
func railLines(rows []relay.BindingStatus, cursor int, attention bool, now time.Time, focused bool, width int, compact bool) []railLine {
	var out []railLine
	card := func(i int) {
		if compact {
			out = append(out, railLine{text: compactLine(rows[i], i == cursor, !attention, now, focused, width), binding: i})
			return
		}
		for _, l := range cardLines(rows[i], i == cursor, !attention, now, focused, width) {
			out = append(out, railLine{text: l, binding: i})
		}
	}
	if !attention {
		for i := range rows {
			card(i)
		}
		return out
	}
	for _, state := range groupOrder {
		n := 0
		for _, r := range rows {
			if r.Display == state {
				n++
			}
		}
		if n == 0 {
			continue
		}
		header := " " + stateStyle(state).Render(state) + faintStyle.Render(fmt.Sprintf("  %d", n))
		out = append(out, railLine{text: fit(header, width), binding: -1})
		for i, r := range rows {
			if r.Display == state {
				card(i)
			}
		}
		out = append(out, railLine{text: fit("", width), binding: -1})
	}
	// A state outside groupOrder (none today) would vanish; append it so
	// nothing is ever hidden.
	for i, r := range rows {
		known := false
		for _, s := range groupOrder {
			if r.Display == s {
				known = true
			}
		}
		if !known {
			card(i)
		}
	}
	return out
}

// railSpan is the first and last line index of binding's card; (0, 0)
// when it has none.
func railSpan(lines []railLine, binding int) (first, last int) {
	first, last = -1, -1
	for i, l := range lines {
		if l.binding == binding {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0, 0
	}
	return first, last
}

// railWindow returns the first rail line to draw so that lines
// [first, last] -- the selected card -- are all visible in rows rows,
// moving top as little as possible. listWindow's five rules with the
// cursor widened to a span; rows <= 0 means no limit. A span taller than
// rows pins top to first: the name line wins.
func railWindow(top, first, last, rows, n int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	if top > n-rows {
		top = n - rows
	}
	if top < 0 {
		top = 0
	}
	if last >= top+rows {
		top = last - rows + 1
	}
	if first < top {
		top = first
	}
	return top
}

// railView draws bodyRows() rail lines at width, windowed on the cursor's
// card, or the three prose states when there is nothing to draw. A card's
// own content width is the split's rail width (the human's draggable
// preference) only in split layout, where width is that same value; in
// stack layout the rail is the whole screen, sized to the terminal, and
// carries no divider to drag -- its cards keep the fixed railDefault
// content width they always had, with width (here, m.width) doing what it
// always did: padding/truncating the drawn line to the terminal.
func (m Model) railView(width int) string {
	rows := m.bodyRows()
	cardWidth := railDefault
	if m.layout() == layoutSplit {
		cardWidth = width
	}
	var lines []string
	switch {
	case !m.statusLoaded && m.err != nil:
		lines = []string{"cannot reach herdr — see the error above"}
	case !m.statusLoaded:
		lines = []string{"loading…"}
	case m.empty() && m.layout() == layoutStack:
		// The stack list screen is the whole terminal at zero rows, so it
		// reads the same prose block the pane shows in split layout,
		// instead of the bare "no bindings" line below (that line stays
		// the split layout's narrow rail summary).
		return strings.Join(emptyPaneBlock(width, rows), "\n")
	case len(m.rows()) == 0:
		lines = []string{"no bindings"}
	default:
		all := railLines(m.rows(), m.list.cursor, m.sort, m.now(), m.screen == screenList, cardWidth, m.compact)
		first, last := railSpan(all, m.list.cursor)
		start := railWindow(m.list.top, first, last, rows, len(all))
		end := len(all)
		if rows > 0 && start+rows < end {
			end = start + rows
		}
		for _, l := range all[start:end] {
			lines = append(lines, l.text)
		}
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	if rows > 0 && len(lines) > rows {
		lines = lines[:rows]
	}
	for i := range lines {
		lines[i] = fit(lines[i], width)
	}
	return strings.Join(lines, "\n")
}

// railTop re-windows the rail on the cursor's card. Called wherever the
// cursor, the rows or the row budget changed.
func (m Model) railTop() int {
	all := railLines(m.rows(), m.list.cursor, m.sort, m.now(), m.screen == screenList, m.railWidth(), m.compact)
	first, last := railSpan(all, m.list.cursor)
	return railWindow(m.list.top, first, last, m.bodyRows(), len(all))
}
