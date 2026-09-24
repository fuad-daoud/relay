package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// fit pads or truncates s to width cells. Truncation is by cell, no
// ellipsis: at a narrow width an ellipsis costs more than it says. Moved
// here from rail.go (X2).
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

// ago is a coarse age: minutes under an hour, hours under a day, then days.
// "" for a zero time, so a caller can omit the fact. Moved here from
// rail.go (X2).
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

// whatAge is a row's NOW cell: what the binding is on, and for how long,
// from the fields Status has today. Moved here from rail.go (X2).
func whatAge(b relevo.BindingStatus, now time.Time) (what, age string) {
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
	case "ACTIVE":
		what = b.BuilderStatus
		if b.QuietFor != "" {
			what += " · quiet " + b.QuietFor
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
	if (b.Display == "NEEDS YOU" || b.Display == "HELD") && b.Stale != "" {
		what += " · " + b.Stale
	}
	return what, age
}

// clipName is name when it fits width cells, else its first width-1 cells
// and an ellipsis. Moved here from layout.go: the fleet NAME column uses it
// (R2.10).
func clipName(name string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(name) <= width {
		return name
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(name) + "…"
}

// clipLeft truncates s to width cells from the left, a leading ellipsis
// marking what was cut: a repo path matters most at its tail.
func clipLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return "…" + string(runes[len(runes)-(width-1):])
}

// fleetView is ':fleet': every binding as a table row (§4.3, §5.4).
type fleetView struct {
	cursor    int    // index into rows(env)
	sticky    string // the selected row's Key(), so the selection survives reordering
	top       int    // first visible line index
	attention bool   // sort order: attention (SortRows true) or name; from the sort pref
}

// newFleetView builds the table, sorted by attention or by name.
func newFleetView(attention bool) fleetView { return fleetView{attention: attention} }

// rows is the report's bindings in display order.
func (f fleetView) rows(env Env) []relevo.BindingStatus {
	return relevo.SortRows(env.Report.Bindings, f.attention)
}

func (f fleetView) Crumbs() []string { return []string{"fleet"} }

func (f fleetView) Capturing() bool { return false }

func (f fleetView) Keys() []KeyHelp {
	return []KeyHelp{
		{"↑↓", "move"},
		{"home/end", "first/last"},
		{"enter", "open"},
		{"s", "sort"},
	}
}

// Context is the counts line on the left and the sort/refresh state on the
// right (§5.4).
func (f fleetView) Context(env Env) (string, string) {
	rows := f.rows(env)
	need, active := 0, 0
	for _, b := range rows {
		switch b.Display {
		case "NEEDS YOU":
			need++
		case "ACTIVE":
			active++
		}
	}
	left := fmt.Sprintf("%d bindings · %d need you · %d active", len(rows), need, active)
	order := "attention"
	if !f.attention {
		order = "name"
	}
	right := "sort " + order
	if !env.StatusAt.IsZero() {
		right += faintStyle.Render(" · refreshed " + ago(env.StatusAt, env.Now) + " ago")
	}
	return left, right
}

// resolveSticky re-points cursor at the row keyed by sticky after the rows
// have changed, clamping and re-pointing sticky at what it lands on. It is
// today's resolveStickyRows rule (list.go:64-87) applied to
// []relevo.BindingStatus keyed by Key().
func (f *fleetView) resolveSticky(rows []relevo.BindingStatus) {
	if len(rows) == 0 {
		f.cursor = 0
		f.sticky = ""
		return
	}
	if f.sticky != "" {
		for i, b := range rows {
			if b.Key() == f.sticky {
				f.cursor = i
				return
			}
		}
	}
	if f.cursor >= len(rows) {
		f.cursor = len(rows) - 1
	}
	if f.cursor < 0 {
		f.cursor = 0
	}
	f.sticky = rows[f.cursor].Key()
}

func (f *fleetView) moveCursor(rows []relevo.BindingStatus, delta int) {
	if len(rows) == 0 {
		return
	}
	c := f.cursor + delta
	if c < 0 {
		c = 0
	}
	if c > len(rows)-1 {
		c = len(rows) - 1
	}
	f.cursor = c
	f.sticky = rows[c].Key()
}

// Update handles the fleet's own keys (§5.4).
func (f fleetView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case statusMsg:
		rows := f.rows(env)
		f.resolveSticky(rows)
		f.top = f.windowTop(rows, env)
		return f, nil
	case tea.KeyMsg:
		rows := f.rows(env)
		switch msg.String() {
		case "up", "k":
			f.moveCursor(rows, -1)
		case "down", "j":
			f.moveCursor(rows, 1)
		case "home":
			f.moveCursor(rows, -len(rows))
		case "end":
			f.moveCursor(rows, len(rows))
		case "s":
			f.attention = !f.attention
			rows = f.rows(env)
			f.resolveSticky(rows)
			f.top = f.windowTop(rows, env)
			order := "attention"
			if !f.attention {
				order = "name"
			}
			return f, func() tea.Msg { return prefMsg{"sort", order} }
		case "enter":
			if len(rows) == 0 {
				return f, nil
			}
			key := rows[f.cursor].Key()
			v, cmd := newRoundView(env, key, 0)
			return f, push(v, cmd)
		}
		if len(rows) > 0 && f.cursor != clampCursor(f.cursor, len(rows)) {
			f.cursor = clampCursor(f.cursor, len(rows))
		}
		f.top = f.windowTop(rows, env)
		return f, nil
	}
	return f, nil
}

func clampCursor(c, n int) int {
	if c < 0 {
		return 0
	}
	if c > n-1 {
		return n - 1
	}
	return c
}

// Table columns (§4.3). Fixed widths, plus REPO which takes the rest.
const (
	colNameW     = 14
	colActorW    = 12
	colOnW       = 22
	colRndW      = 4
	colStateW    = 10
	colNowW      = 24
	colSpendW    = 8
	colPlannerW  = 14
	colRepoMin   = 10
	fleetGutterW = 3
)

type fleetCell struct {
	width int
	text  string
	style lipgloss.Style
}

// candidateText is the ON cell: the model part of the binding's candidate
// token, everything after the second '/'. A1 adds a per-candidate name
// (BindingStatus.BuilderName); when that field exists its non-empty value
// wins. The naming rule lives here alone.
func candidateText(b relevo.BindingStatus) string {
	// A1 round 2: use b.BuilderName here when BindingStatus gains it.
	s := b.BuilderCandidate
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "-"
	}
	return s
}

func actorCell(b relevo.BindingStatus) string {
	if b.Role == "" {
		return "builder"
	}
	return b.Role
}

func nowCell(b relevo.BindingStatus, now time.Time) string {
	what, age := whatAge(b, now)
	if age == "" {
		return what
	}
	return what + " · " + age
}

func spendCell(b relevo.BindingStatus) string {
	if b.Spend == nil {
		return ""
	}
	return usage.MoneyShort(*b.Spend)
}

func plannerCell(b relevo.BindingStatus) string {
	if b.PlannerName == "" {
		return "-"
	}
	return b.PlannerName
}

func repoCell(b relevo.BindingStatus) string {
	s := b.CWD
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.Replace(s, home, "~", 1)
	}
	return s
}

// pad pads s to width cells, truncating with an ellipsis past it.
func pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	if lipgloss.Width(s) == width {
		return s
	}
	return clipName(s, width)
}

// fleetLineWidth is the drawn width of cells plus a repo cell of repoW
// columns (0 for none), gutter and two-space joins included.
func fleetLineWidth(cells []fleetCell, repoW int) int {
	w := fleetGutterW
	for i, c := range cells {
		if i > 0 {
			w += 2
		}
		w += c.width
	}
	if repoW > 0 {
		if len(cells) > 0 {
			w += 2
		}
		w += repoW
	}
	return w
}

// fleetCells renders one row's cells for width, dropping REPO, then
// PLANNER, then SPEND, then ACTOR until the fixed columns fit (§4.3).
func fleetCells(b relevo.BindingStatus, now time.Time, width int) []string {
	name := fleetCell{colNameW, clipName(b.Key(), colNameW), fgStyle.Bold(true)}
	actor := fleetCell{colActorW, actorCell(b), dimStyle}
	on := fleetCell{colOnW, clipName(candidateText(b), colOnW), fgStyle}
	rnd := fleetCell{colRndW, fmt.Sprintf("r%d", b.Round), dimStyle}
	state := fleetCell{colStateW, b.Display, stateStyle(b.Display)}
	nowc := fleetCell{colNowW, nowCell(b, now), dimStyle}
	spend := fleetCell{colSpendW, spendCell(b), dimStyle}
	planner := fleetCell{colPlannerW, plannerCell(b), dimStyle}

	repo := repoCell(b)

	// Attempts in drop order: keep all; drop REPO; drop PLANNER too; drop
	// SPEND too; drop ACTOR too.
	attempts := [][]fleetCell{
		{name, actor, on, rnd, state, nowc, spend, planner},
		{name, actor, on, rnd, state, nowc, spend, planner},
		{name, actor, on, rnd, state, nowc, spend},
		{name, actor, on, rnd, state, nowc},
		{name, on, rnd, state, nowc},
	}
	repoW := 0
	for i, cells := range attempts {
		if i == 0 {
			// With REPO: it takes the rest, at least colRepoMin.
			w := width - fleetLineWidth(cells, 0) - 2
			if w >= colRepoMin {
				repoW = w
				break
			}
			continue
		}
		if fleetLineWidth(cells, 0) <= width {
			return renderFleetCells(cells, "", 0)
		}
	}
	if repoW > 0 {
		return renderFleetCells(attempts[0], repo, repoW)
	}
	return renderFleetCells(attempts[len(attempts)-1], "", 0)
}

func renderFleetCells(cells []fleetCell, repo string, repoW int) []string {
	out := make([]string, 0, len(cells)+1)
	for _, c := range cells {
		out = append(out, c.style.Render(pad(c.text, c.width)))
	}
	if repoW > 0 {
		out = append(out, fgStyle.Render(pad(clipLeft(repo, repoW), repoW)))
	}
	return out
}

// gutter is the 3-cell selection column: the accent bar on the selected
// row, the amber unread dot, then a space.
func fleetGutter(b relevo.BindingStatus, selected bool) string {
	bar := " "
	if selected {
		bar = accentStyle.Render("▎")
	}
	unread := " "
	if b.Unread {
		unread = stateNeedsYouStyle.Render("●")
	}
	return bar + unread + " "
}

// fleetLine is one drawn body line and the row it belongs to.
type fleetLine struct {
	text string
	row  int
}

// fleetLines renders every row (and a NEEDS YOU row's question line).
func (f fleetView) fleetLines(env Env, width int) []fleetLine {
	rows := f.rows(env)
	var out []fleetLine
	for i, b := range rows {
		cells := fleetCells(b, env.Now, width)
		text := fit(fleetGutter(b, i == f.cursor)+strings.Join(cells, "  "), width)
		if i == f.cursor {
			text = selectedBg.Render(text)
		}
		out = append(out, fleetLine{text, i})
		if b.Display == "NEEDS YOU" && b.Waiting != nil && b.Waiting.Line != "" {
			second := fit("     "+faintStyle.Render("└ ")+dimStyle.Render(b.Waiting.Line), width)
			if i == f.cursor {
				second = selectedBg.Render(second)
			}
			out = append(out, fleetLine{second, i})
		}
	}
	return out
}

// windowTop is the first line to draw so every line of the cursor's row is
// visible in height lines, moving top as little as possible.
func (f fleetView) windowTopLines(lines []fleetLine, height int) int {
	if height <= 0 || len(lines) <= height {
		return 0
	}
	first, last := -1, -1
	for i, l := range lines {
		if l.row == f.cursor {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0
	}
	top := f.top
	if top > len(lines)-height {
		top = len(lines) - height
	}
	if top < 0 {
		top = 0
	}
	if last >= top+height {
		top = last - height + 1
	}
	if first < top {
		top = first
	}
	return top
}

func (f fleetView) windowTop(rows []relevo.BindingStatus, env Env) int {
	return f.windowTopLines(f.fleetLines(env, env.Width), bodyHeight(env))
}

// Body is the table, loading prose, or the empty block (§5.4).
func (f fleetView) Body(env Env, width, height int) string {
	if !env.Loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(f.rows(env)) == 0 {
		return strings.Join(emptyPaneBlock(width, height), "\n")
	}
	lines := f.fleetLines(env, width)
	start := f.windowTopLines(lines, height)
	end := len(lines)
	if height > 0 && start+height < end {
		end = start + height
	}
	var out []string
	for _, l := range lines[start:end] {
		out = append(out, l.text)
	}
	return strings.Join(fitLines(out, width, height), "\n")
}

// blockLines renders raw prose lines fitted to width and padded to height.
func blockLines(raw []string, width, height int) []string {
	out := make([]string, 0, height)
	for _, l := range raw {
		out = append(out, fit(emptyStyle.Render(l), width))
	}
	for len(out) < height {
		out = append(out, fit("", width))
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

// fitLines pads lines to height, truncates the overflow and fits each to
// width.
func fitLines(lines []string, width, height int) []string {
	out := make([]string, 0, height)
	for _, l := range lines {
		out = append(out, fit(l, width))
	}
	for len(out) < height {
		out = append(out, fit("", width))
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}
