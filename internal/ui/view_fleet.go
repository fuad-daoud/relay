package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// fit pads or truncates s to width cells. Truncation is by cell, no
// ellipsis: at a narrow width an ellipsis costs more than it says. It moved
// into the fleet view in round 2 (X2).
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
// "" for a zero time, so a caller can omit the fact. It moved into the
// fleet view in round 2 (X2).
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

// reportReady is the fleet's rule for a report waiting on the human at this
// cockpit (§4.5): the binding's planner is `you`, and a payload is pending for
// it. Such a row's NOW cell says so, and opening it pulls the report.
func reportReady(b relevo.BindingStatus) bool {
	return b.PlannerName == "you" && b.Pending != nil
}

// nowStyle colours the NOW cell: a pending report is what needs the human, so
// it wears the same amber as a NEEDS YOU state (§4.5).
func nowStyle(b relevo.BindingStatus) lipgloss.Style {
	if reportReady(b) {
		return stateNeedsYouStyle
	}
	return dimStyle
}

// whatAge is a row's NOW cell: what the binding is on, and for how long,
// from the fields Status has today. It moved into the fleet view in round 2
// (X2).
func whatAge(b relevo.BindingStatus, now time.Time) (what, age string) {
	if reportReady(b) {
		// The report the human planner is owed takes the cell over: it is
		// why the row needs them (§4.5).
		what = "report ready"
		if b.Last != nil {
			age = ago(b.Last.TS, now)
		}
		if age == "" && b.Waiting != nil {
			age = ago(b.Waiting.Since, now)
		}
		return what, age
	}
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
	actions   bool   // Actions != nil at construction: the action keys are shown (r1)

	filtering  bool            // the '/' filter input is open and owns every key (A4)
	filter     textinput.Model // the filter input: the same widget as the cmdline (A4)
	filterText string          // the applied filter text, kept after the input closes (A4)
}

// newFleetView builds the table, sorted by attention or by name.
func newFleetView(attention bool) fleetView {
	return fleetView{attention: attention, filter: newFilterInput()}
}

// withActions marks whether the shell has an Actions seam, so Keys() shows
// the action keys exactly when one exists (r1).
func (f fleetView) withActions(a bool) fleetView {
	f.actions = a
	return f
}

// newFilterInput is the fleet's one-line filter input: the same textinput the
// command line uses, with a '/' prompt (A4).
func newFilterInput() textinput.Model {
	in := textinput.New()
	in.Prompt = "/"
	return in
}

// rows is the report's bindings in display order, with the applied filter
// (A4).
func (f fleetView) rows(env Env) []relevo.BindingStatus {
	sorted := relevo.SortRows(env.Report.Bindings, f.attention)
	q := f.activeFilter()
	if q == "" {
		return sorted
	}
	out := make([]relevo.BindingStatus, 0, len(sorted))
	for _, b := range sorted {
		if fleetRowMatches(b, q) {
			out = append(out, b)
		}
	}
	return out
}

// activeFilter is the filter in force: the input's text while the input is
// open, the applied text once it has closed (A4).
func (f fleetView) activeFilter() string {
	if f.filtering {
		return f.filter.Value()
	}
	return f.filterText
}

// fleetRowMatches reports whether text is a case-insensitive substring of any
// of the row's shown fields: its key, actor, candidate, planner, repo or
// state (A4).
func fleetRowMatches(b relevo.BindingStatus, text string) bool {
	q := strings.ToLower(text)
	for _, s := range []string{b.Key(), actorCell(b), candidateText(b), b.PlannerName, b.CWD, b.Display} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

func (f fleetView) Crumbs() []string { return []string{"fleet"} }

// Capturing is true while the filter input is open: the shell then forwards
// ':', '?', 'q' and 'esc' to it (A4).
func (f fleetView) Capturing() bool { return f.filtering }

func (f fleetView) Keys() []KeyHelp {
	keys := []KeyHelp{
		{"↑↓", "move"},
		{"home/end", "first/last"},
		{"enter", "open"},
		// Sort is the fleet's own key, shown whether or not an Actions seam
		// exists: `s` belongs to send once Actions is set (W2).
		KeyHelp{"a", "sort"},
	}
	if f.actions {
		// The keys the cockpit can act with, in priority order (§4.3, §4.5):
		// the footer drops from the end when a row is too narrow.
		keys = append(keys,
			KeyHelp{"/", "filter"},
			KeyHelp{"s", "send"},
			KeyHelp{"b", "bind"},
			KeyHelp{"x", "stop"},
			KeyHelp{"D", "done"},
			KeyHelp{"u", "unbind"},
			KeyHelp{"g", "gate"},
			KeyHelp{"r", "retry"},
			KeyHelp{"o", "shell"},
			KeyHelp{"E", "edit+send"},
		)
		return keys
	}
	keys = append(keys, KeyHelp{"/", "filter"})
	return keys
}

// Context is the counts line on the left and the sort, filter and refresh
// state on the right (§5.4, A3, A4).
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
	bindingsLabel := fmt.Sprintf("%d bindings", len(rows))
	if len(rows) == 1 {
		bindingsLabel = "1 binding"
	}
	left := fmt.Sprintf("%s · %s · %d active", bindingsLabel, needsYouCount(need), active)
	order := "attention"
	if !f.attention {
		order = "name"
	}
	right := "sort " + order
	if q := f.activeFilter(); q != "" {
		right = fmt.Sprintf("filter %q · ", q) + right
	}
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

// Update handles the fleet's own keys (§5.4, A4).
func (f fleetView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case statusMsg:
		rows := f.rows(env)
		f.resolveSticky(rows)
		f.top = f.windowTop(rows, env)
		return f, nil
	case tea.KeyMsg:
		if f.filtering {
			return f.updateFilter(msg, env)
		}
		rows := f.rows(env)
		sel := relevo.BindingStatus{}
		if len(rows) > 0 {
			sel = rows[clampCursor(f.cursor, len(rows))]
		}
		if cmd, ok := fleetActionKey(env, sel, msg.String()); ok {
			return f, cmd
		}
		switch msg.String() {
		case "/":
			f.filtering = true
			f.filter = newFilterInput()
			f.filter.Focus()
			return f, nil
		case "esc":
			// esc clears an applied filter before anything else (A4).
			if f.filterText != "" {
				f.filterText = ""
				rows = f.rows(env)
				f.resolveSticky(rows)
				f.top = f.windowTop(rows, env)
			}
			return f, nil
		case "up", "k":
			f.moveCursor(rows, -1)
		case "down", "j":
			f.moveCursor(rows, 1)
		case "home":
			f.moveCursor(rows, -len(rows))
		case "end":
			f.moveCursor(rows, len(rows))
		case "a":
			// `a` ("attention") flips the sort order. `s` is send whenever an
			// Actions seam exists and does nothing otherwise (W2): it never
			// reaches this switch with Actions set, and has no case without.
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

// updateFilter owns every key while the filter input is open: enter keeps the
// filter and closes the input, esc clears it and closes, everything else goes
// to the input (A4).
func (f fleetView) updateFilter(msg tea.KeyMsg, env Env) (View, tea.Cmd) {
	switch msg.String() {
	case "enter":
		f.filterText = f.filter.Value()
		f.filtering = false
		f.filter.Blur()
		return f.repointed(env), nil
	case "esc":
		f.filterText = ""
		f.filter.SetValue("")
		f.filtering = false
		f.filter.Blur()
		return f.repointed(env), nil
	}
	var cmd tea.Cmd
	f.filter, cmd = f.filter.Update(msg)
	return f.repointed(env), cmd
}

// repointed re-resolves the cursor and the window after the filter changed.
func (f fleetView) repointed(env Env) fleetView {
	rows := f.rows(env)
	f.resolveSticky(rows)
	f.top = f.windowTop(rows, env)
	return f
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

// fleetColumns is the table's fixed columns in order, each with the header
// name it draws. The header and every row are built from the same plan, so
// they always show the same columns (A1).
var fleetColumns = []struct {
	name  string
	width int
}{
	{"NAME", colNameW},
	{"ACTOR", colActorW},
	{"ON", colOnW},
	{"RND", colRndW},
	{"STATE", colStateW},
	{"NOW", colNowW},
	{"SPEND", colSpendW},
	{"PLANNER", colPlannerW},
}

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
	if b.BuilderName != "" {
		return b.BuilderName
	}
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

// spendText is the SPEND cell: money only (A2). The measured and estimated
// dollars sum, a '~' marks a sum with estimated dollars in it, a plan lane
// reads "plan", and a sub-cent sum reads "<$0.01".
func spendText(s usage.Spend) string {
	sum := s.Measured + s.Estimated
	if sum > 0 {
		text := fmt.Sprintf("$%.2f", sum)
		if sum < 0.01 {
			text = "<$0.01"
		}
		if s.Estimated > 0 {
			text = "~" + text
		}
		return text
	}
	if s.Plan > 0 {
		return "plan"
	}
	return ""
}

func spendCell(b relevo.BindingStatus) string {
	if b.Spend == nil {
		return ""
	}
	return spendText(*b.Spend)
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

// fleetLineWidth is the drawn width of the columns at idx plus a repo cell of
// repoW columns (0 for none), gutter and two-space joins included.
func fleetLineWidth(idx []int, repoW int) int {
	w := fleetGutterW
	for i, ci := range idx {
		if i > 0 {
			w += 2
		}
		w += fleetColumns[ci].width
	}
	if repoW > 0 {
		if len(idx) > 0 {
			w += 2
		}
		w += repoW
	}
	return w
}

// fleetColumnPlan is the columns to draw at width: the kept columns' indices
// and the REPO column's width, 0 when REPO is dropped. REPO drops first,
// then PLANNER, then SPEND, then ACTOR, until the fixed columns fit (§4.3).
// The header and every row share it (A1).
func fleetColumnPlan(width int) (idx []int, repoW int) {
	all := []int{0, 1, 2, 3, 4, 5, 6, 7}
	// Attempts in drop order: keep all with REPO; drop REPO; drop PLANNER
	// too; drop SPEND too; drop ACTOR too.
	attempts := [][]int{
		all,
		all,
		{0, 1, 2, 3, 4, 5, 6},
		{0, 1, 2, 3, 4, 5},
		{0, 2, 3, 4, 5},
	}
	for i, cols := range attempts {
		if i == 0 {
			// With REPO: it takes the rest, at least colRepoMin.
			w := width - fleetLineWidth(cols, 0) - 2
			if w >= colRepoMin {
				return cols, w
			}
			continue
		}
		if fleetLineWidth(cols, 0) <= width {
			return cols, 0
		}
	}
	return attempts[len(attempts)-1], 0
}

// fleetCells renders one row's cells for width, dropping REPO, then
// PLANNER, then SPEND, then ACTOR until the fixed columns fit (§4.3).
func fleetCells(b relevo.BindingStatus, now time.Time, width int) []string {
	values := []fleetCell{
		{colNameW, clipName(b.Key(), colNameW), fgStyle.Bold(true)},
		{colActorW, actorCell(b), dimStyle},
		{colOnW, clipName(candidateText(b), colOnW), fgStyle},
		{colRndW, fmt.Sprintf("r%d", b.Round), dimStyle},
		{colStateW, b.Display, stateStyle(b.Display)},
		{colNowW, nowCell(b, now), nowStyle(b)},
		{colSpendW, spendCell(b), dimStyle},
		{colPlannerW, plannerCell(b), dimStyle},
	}
	idx, repoW := fleetColumnPlan(width)
	cells := make([]fleetCell, 0, len(idx))
	for _, ci := range idx {
		cells = append(cells, values[ci])
	}
	return renderFleetCells(cells, repoCell(b), repoW)
}

// fleetHeaderCells is the header's cells, in the same columns the rows use
// at width (A1).
func fleetHeaderCells(width int) []string {
	idx, repoW := fleetColumnPlan(width)
	cells := make([]fleetCell, 0, len(idx))
	for _, ci := range idx {
		cells = append(cells, fleetCell{fleetColumns[ci].width, fleetColumns[ci].name, faintStyle.Bold(true)})
	}
	return renderFleetCells(cells, "REPO", repoW)
}

// fleetHeaderLine is the table's header body line: the same 3-cell gutter,
// the column names and REPO, fitted to width (A1).
func fleetHeaderLine(width int) string {
	return fit("   "+strings.Join(fleetHeaderCells(width), "  "), width)
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

// fixedRows is the body lines above the table's rows that never scroll: the
// header, and the filter input while it is open (A1, A4).
func (f fleetView) fixedRows() int {
	n := 1
	if f.filtering {
		n++
	}
	return n
}

// tableHeight is the rows' window height: the body box less the fixed lines.
func (f fleetView) tableHeight(env Env) int {
	h := bodyHeight(env) - f.fixedRows()
	if h < 0 {
		return 0
	}
	return h
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
	return f.windowTopLines(f.fleetLines(env, env.Width), f.tableHeight(env))
}

// Body is the header and the table, the loading prose, or the empty block
// (§5.4, A1). The header does not scroll and neither the loading nor the
// empty body has one.
func (f fleetView) Body(env Env, width, height int) string {
	if !env.Loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(env.Report.Bindings) == 0 {
		return strings.Join(emptyPaneBlock(width, height), "\n")
	}
	var fixed []string
	if f.filtering {
		fixed = append(fixed, fit(f.filter.View(), width))
	}
	fixed = append(fixed, fleetHeaderLine(width))

	rowsH := height - len(fixed)
	if rowsH < 0 {
		rowsH = 0
	}
	lines := f.fleetLines(env, width)
	start := f.windowTopLines(lines, rowsH)
	end := len(lines)
	if rowsH > 0 && start+rowsH < end {
		end = start + rowsH
	}
	out := make([]string, 0, height)
	out = append(out, fixed...)
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
