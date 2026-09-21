package ui

import (
	"fmt"
	"strconv"
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
	case "PAUSED":
		what = "paused"
		if b.Last != nil && b.Last.Kind == "pause" {
			age = ago(b.Last.TS, now)
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

// histStateText is a hist row's state-slot text (in place of the state
// word a live row shows): "archived <date>" when the binding is
// archived, "done" otherwise -- a binding the database has recorded but
// the live report no longer carries, never tarred (e.g. `relay done`
// released it without `gc` archiving it yet).
func histStateText(h relay.HistoryBinding) string {
	if h.Archived {
		return "archived " + h.ArchivedAt.Format("2006-01-02")
	}
	return "done"
}

// histFacts is a hist row's facts line: round count, last-activity age,
// and its feature label when one is set.
func histFacts(h relay.HistoryBinding, now time.Time) string {
	parts := []string{fmt.Sprintf("r%d", h.Rounds), ago(h.LastActivity, now)}
	if h.Feature != "" {
		parts = append(parts, "feature "+h.Feature)
	}
	return strings.Join(parts, " · ")
}

// histCardLines renders one hist row's card: name (archivedStyle, dim)
// and its state-slot text on line 1, the facts line on line 2 -- never
// attention-grouped, never re-ordered by attention (§5.8).
func histCardLines(h relay.HistoryBinding, selected, focused bool, now time.Time, width int) []string {
	gutter := " "
	if selected && focused {
		gutter = accentStyle.Render("▎")
	} else if selected {
		gutter = dimStyle.Render("▎")
	}
	const unreadSlot = "  "
	state := histStateText(h)
	nameWidth := width - 1 - 2 - lipgloss.Width(state) - 1
	name := archivedStyle.Render(fit(h.Name, nameWidth))
	l1 := gutter + unreadSlot + name + dimStyle.Render(state) + " "

	indent := gutter + "  "
	l2 := indent + dimStyle.Render(histFacts(h, now))

	lines := []string{fit(l1, width), fit(l2, width)}
	if selected {
		for i, l := range lines {
			lines[i] = selectedBg.Render(l)
		}
	}
	return lines
}

// histCompactLine is histCardLines' one-line form, mirroring compactLine.
func histCompactLine(h relay.HistoryBinding, selected, focused bool, width int) string {
	gutter := " "
	if selected {
		if focused {
			gutter = accentStyle.Render("▎")
		} else {
			gutter = dimStyle.Render("▎")
		}
	}
	const unreadSlot = "  "
	name := archivedStyle.Render(fit(h.Name, width-1-2-1))
	l := fit(gutter+unreadSlot+name+" ", width)
	if selected {
		l = selectedBg.Render(l)
	}
	return l
}

// railLinesAll is railLines generalised to the rail's row set in either
// scope: live rows attention-grouped exactly as railLines groups them
// today, then (scope all) hist rows appended after -- dim, never grouped
// by attention and never re-ordered (§5.8). cursor indexes rows itself
// (the union), matching railSpan/railWindow's existing binding-index
// contract, so a live-only rows slice renders identically to railLines.
func railLinesAll(rows []railRow, cursor int, attention bool, now time.Time, focused bool, width int, compact bool) []railLine {
	var out []railLine
	card := func(i int) {
		rr := rows[i]
		switch {
		case rr.live != nil:
			if compact {
				out = append(out, railLine{text: compactLine(*rr.live, i == cursor, !attention, now, focused, width), binding: i})
				return
			}
			for _, l := range cardLines(*rr.live, i == cursor, !attention, now, focused, width) {
				out = append(out, railLine{text: l, binding: i})
			}
		case rr.hist != nil:
			if compact {
				out = append(out, railLine{text: histCompactLine(*rr.hist, i == cursor, focused, width), binding: i})
				return
			}
			for _, l := range histCardLines(*rr.hist, i == cursor, focused, now, width) {
				out = append(out, railLine{text: l, binding: i})
			}
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
			if r.live != nil && r.live.Display == state {
				n++
			}
		}
		if n == 0 {
			continue
		}
		header := " " + stateStyle(state).Render(state) + faintStyle.Render(fmt.Sprintf("  %d", n))
		out = append(out, railLine{text: fit(header, width), binding: -1})
		for i, r := range rows {
			if r.live != nil && r.live.Display == state {
				card(i)
			}
		}
		out = append(out, railLine{text: fit("", width), binding: -1})
	}
	// A live state outside groupOrder (none today) would vanish; append it
	// so nothing is ever hidden.
	for i, r := range rows {
		if r.live == nil {
			continue
		}
		known := false
		for _, s := range groupOrder {
			if r.live.Display == s {
				known = true
			}
		}
		if !known {
			card(i)
		}
	}
	// Hist rows: dim, always after, never grouped, never re-ordered.
	for i, r := range rows {
		if r.hist != nil {
			card(i)
		}
	}
	return out
}

// groupOrder is the attention order of the rail's headers.
var groupOrder = []string{"NEEDS YOU", "HELD", "ACTIVE", "PAUSED", "DONE"}

// railLines lays out every card. Rows arrive owner-sorted (SortRows's
// OwnerLabel is the primary key), so they partition into maximal runs of
// equal OwnerLabel. One run with an empty label is a planner: today's
// body, unchanged. Otherwise each owner run gets a header -- label, the
// fingerprint, the count -- then the run's cards through today's logic
// (including the per-state sub-headers when attention), then a blank line.
// cursor is the index in rows of the selected binding. focused is "the
// rail has focus" (m.screen == screenList), threaded through to cardLines
// so the selected card's gutter dims when the pane has focus. compact
// emits compactLine's single tagged line per binding instead.
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
	// cards renders rows [lo, hi) through today's logic, with every card
	// tagged its global row index. The state walk covers groupOrder first
	// and appends any state outside it, so nothing is ever hidden.
	cards := func(lo, hi int) {
		if !attention {
			for i := lo; i < hi; i++ {
				card(i)
			}
			return
		}
		for _, state := range groupOrder {
			n := 0
			for _, r := range rows[lo:hi] {
				if r.Display == state {
					n++
				}
			}
			if n == 0 {
				continue
			}
			header := " " + stateStyle(state).Render(state) + faintStyle.Render(fmt.Sprintf("  %d", n))
			out = append(out, railLine{text: fit(header, width), binding: -1})
			for i := lo; i < hi; i++ {
				if rows[i].Display == state {
					card(i)
				}
			}
			out = append(out, railLine{text: fit("", width), binding: -1})
		}
		for i := lo; i < hi; i++ {
			known := false
			for _, s := range groupOrder {
				if rows[i].Display == s {
					known = true
				}
			}
			if !known {
				card(i)
			}
		}
	}

	// Partition into maximal runs of equal OwnerLabel, in slice order.
	type run struct {
		lo, hi int
		label  string
	}
	var runs []run
	for i := 0; i < len(rows); {
		j := i
		for j < len(rows) && rows[j].OwnerLabel == rows[i].OwnerLabel {
			j++
		}
		runs = append(runs, run{lo: i, hi: j, label: rows[i].OwnerLabel})
		i = j
	}

	if len(runs) == 1 && runs[0].label == "" {
		cards(0, len(rows))
		return out
	}

	for _, r := range runs {
		header := " " + r.label + "  (" + faintStyle.Render(relay.ShortOwner(rows[r.lo].Owner)) + ")  " +
			faintStyle.Render(strconv.Itoa(r.hi-r.lo))
		out = append(out, railLine{text: fit(header, width), binding: -1})
		cards(r.lo, r.hi)
		out = append(out, railLine{text: fit("", width), binding: -1})
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
		lines = []string{"status unavailable — see the error above"}
	case !m.statusLoaded:
		lines = []string{"loading…"}
	case m.empty() && m.scope == scopeLive && m.layout() == layoutStack:
		// The stack list screen is the whole terminal at zero rows, so it
		// reads the same prose block the pane shows in split layout,
		// instead of the bare "no bindings" line below (that line stays
		// the split layout's narrow rail summary). Scope all keeps this
		// branch only when there is truly nothing to show (below): a
		// live-empty fleet with archived history still has a rail to draw.
		return strings.Join(emptyPaneBlock(width, rows), "\n")
	case len(m.railRows()) == 0:
		lines = []string{"no bindings"}
	default:
		all := railLinesAll(m.railRows(), m.list.cursor, m.sort, m.now(), m.screen == screenList, cardWidth, m.compact)
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
	all := railLinesAll(m.railRows(), m.list.cursor, m.sort, m.now(), m.screen == screenList, m.railWidth(), m.compact)
	first, last := railSpan(all, m.list.cursor)
	return railWindow(m.list.top, first, last, m.bodyRows(), len(all))
}
