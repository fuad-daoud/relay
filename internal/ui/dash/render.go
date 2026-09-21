package dash

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/histq"
)

// dashHeaderRows is the screen's fixed furniture: the identity line, the
// tiles, and the editor or column header. Everything under them is the
// grid, which is what scrolls (§5).
const dashHeaderRows = 3

// Column widths §5 fixes. `builder` is the widest, and the one that shrinks
// when the terminal cannot hold the whole line (§5's "builder (36, …)").
const (
	colStarted  = 16
	colBinding  = 12
	colRound    = 3
	colBuilder  = 36
	colOutcome  = 13
	colCommits  = 7
	colTree     = 6
	colGate     = 5
	colTokens   = 7
	colCost     = 7
	colDuration = 8

	colKey = 36 // the group key column, expand marker included
)

// Width tiers §5: < 140 drops gate; < 120 drops tree and commits; < 100
// drops tokens; < 80 drops duration. cost is never dropped.
const (
	tierGate    = 140
	tierTree    = 120
	tierTokens  = 100
	tierDur     = 80
	tierHintBar = 120 // the line-1 key hints
)

// lineKind separates the grid's two row shapes.
type lineKind int

const (
	lineGroup lineKind = iota
	lineRound
)

// line is one visible grid row: a group, or a round (the query's rows when
// ungrouped, or an expanded group's rows when grouped).
type line struct {
	kind  lineKind
	group histq.GroupRow
	row   db.RoundRow
}

// col is one grid column: its header label, its width, and its value.
type col struct {
	label string
	width int
	value string
	style lipgloss.Style
}

// visible is the flattened list the cursor addresses: one line per row
// when By == none (sorted), else one line per group (sorted) with an
// expanded group's rows -- newest first, or the round sort -- indented
// beneath it. Rebuilt on every call, exactly as render does (§3).
func (m Model) visible() []line {
	if !m.grouped() {
		rows := m.sortedRows(m.rows)
		out := make([]line, len(rows))
		for i := range rows {
			out[i] = line{kind: lineRound, row: rows[i]}
		}
		return out
	}
	groups := m.sortedGroups(m.groups)
	var out []line
	for _, g := range groups {
		out = append(out, line{kind: lineGroup, group: g})
		if m.expanded[g.Key] {
			for _, r := range m.sortedRows(g.Rows) {
				out = append(out, line{kind: lineRound, row: r})
			}
		}
	}
	return out
}

// gridContent is the grid's whole text, one line per visible row; the
// empty state is prose, never an error.
func (m Model) gridContent() string {
	lines := m.visible()
	if len(lines) == 0 {
		return m.styles.Empty.Render("no rounds")
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = m.renderLine(l, i == m.cursor)
	}
	return strings.Join(out, "\n")
}

// windowTop is the first grid line to draw so the cursor stays visible: it
// sits mid-window, clamped to the ends. The grid scrolls, the header does
// not (§5).
func (m Model) windowTop() int {
	n := len(m.visible())
	h := m.gridHeight()
	if h <= 0 || n <= h {
		return 0
	}
	top := m.cursor - h/2
	if top < 0 {
		top = 0
	}
	if top > n-h {
		top = n - h
	}
	return top
}

// renderLine draws one grid row, the cursor line in the rail's selected
// style.
func (m Model) renderLine(l line, cursor bool) string {
	if l.kind == lineGroup {
		return m.groupLine(l.group, cursor)
	}
	return m.roundLine(l.row, cursor, m.grouped())
}

// roundColumns is the round grid's columns for this terminal width: §5's
// columns in §5's order, the tier columns present, builder fitted to what
// is left.
func (m Model) roundColumns(r db.RoundRow) []col {
	outcome, outcomeStyle := m.outcomeCell(r)
	cols := []col{
		{"started", colStarted, r.StartedAt.In(m.loc).Format("2006-01-02 15:04"), m.styles.Fg},
		{"binding", colBinding, r.BindingName, m.styles.Fg},
		{"rnd", colRound, fmt.Sprintf("r%d", r.Number), m.styles.Dim},
		{"builder", m.builderWidth(), deref(r.BuilderCandidate), m.styles.Fg},
		{"outcome", colOutcome, outcome, outcomeStyle},
	}
	if m.width >= tierTree {
		cols = append(cols,
			col{"commits", colCommits, commitsCell(r), m.styles.Fg},
			col{"tree", colTree, treeCell(r), m.styles.Fg})
	}
	if m.width >= tierGate {
		cols = append(cols, col{"gate", colGate, gateCell(r), m.styles.Fg})
	}
	if m.width >= tierTokens {
		cols = append(cols, col{"tokens", colTokens, tokensCell(r), m.styles.Fg})
	}
	cols = append(cols, col{"cost", colCost, costCell(r), m.styles.Fg})
	if m.width >= tierDur {
		cols = append(cols, col{"duration", colDuration, durationCell(r), m.styles.Fg})
	}
	return cols
}

// roundLine is one round (§5):
//
//	2026-09-20 22:01  persist  r5  claude/anthropic/sonnet  reported  +1  clean  pass  1.2M  $0.42  27m
//
// An expanded group's rounds are indented two cells.
func (m Model) roundLine(r db.RoundRow, cursor, indented bool) string {
	cells := make([]string, 0, 11)
	for _, c := range m.roundColumns(r) {
		cells = append(cells, c.style.Render(pad(c.value, c.width)))
	}
	line := strings.Join(cells, "  ")
	if indented {
		line = "  " + line
	}
	if r.Archived {
		line = m.styles.Archived.Render(line)
	}
	if cursor {
		line = m.styles.Selected.Render(fit(line, m.width))
	}
	return line
}

// groupLine is one group row (§5), sums right-aligned under the header.
func (m Model) groupLine(g histq.GroupRow, cursor bool) string {
	marker := "▸ "
	if m.expanded[g.Key] {
		marker = "▾ "
	}
	parts := []string{pad(marker+clip(g.Key, colKey-2), colKey)}
	parts = append(parts, fmt.Sprintf("%6d", g.Rounds))
	parts = append(parts, fmt.Sprintf("%8d", g.Reported))
	parts = append(parts, fmt.Sprintf("%6d", g.Halted))
	if m.width >= tierTree {
		parts = append(parts, fmt.Sprintf("%7d", g.Commits))
	}
	if m.width >= tierTokens {
		parts = append(parts, fmt.Sprintf("%7s", shortTokens(g.Tokens)))
	}
	parts = append(parts, fmt.Sprintf("%7s", groupCostCell(g)))
	parts = append(parts, fmt.Sprintf("%10s", g.Last.In(m.loc).Format("2006-01-02")))
	line := strings.Join(parts, "  ")
	if cursor {
		line = m.styles.Selected.Render(fit(line, m.width))
	}
	return line
}

// builderWidth is the builder column's drawn width: 36 where it fits, less
// when the terminal cannot hold the whole line, so nothing is silently cut
// off at the right edge and the header stays aligned with the rows.
func (m Model) builderWidth() int {
	if m.width <= 0 {
		return colBuilder
	}
	room := m.width - (colStarted + 2 + colBinding + 2 + colRound + 2 + colOutcome) - 2 - m.tailWidth()
	if room < 10 {
		room = 10
	}
	if room > colBuilder {
		room = colBuilder
	}
	return room
}

// tailWidth is the columns after builder, widths and separators, for the
// tier this terminal earns. It is the sum builderWidth leaves room for.
func (m Model) tailWidth() int {
	w := 2 + colCost
	if m.width >= tierDur {
		w += 2 + colDuration
	}
	if m.width >= tierTokens {
		w += 2 + colTokens
	}
	if m.width >= tierTree {
		w += 2 + colCommits + 2 + colTree
	}
	if m.width >= tierGate {
		w += 2 + colGate
	}
	return w
}

// headerLine is §5's line 1: identity, the applied query, the axis, and the
// key hints, which drop below 120 columns. The applied text's own `by:`
// token is dropped from the display: the axis is shown in its own clause.
func (m Model) headerLine() string {
	q := stripBy(m.text)
	if q == "" {
		q = "(all rounds)"
	}
	axis := string(m.query.By)
	if axis == "" {
		axis = string(histq.AxisNone)
	}
	left := " relay · dashboard   "
	right := "/ filter  b regroup  s sort  d fleet  r refresh"

	avail := m.width - lipgloss.Width(left) - lipgloss.Width("   by:"+axis)
	if m.width >= tierHintBar {
		avail -= lipgloss.Width(right) + 1
	}
	left += clip(q, avail) + "   by:" + axis

	if m.width >= tierHintBar {
		return fit(spread(left, right, m.width), m.width)
	}
	return fit(left, m.width)
}

// tilesLine is §5's line 2. A host notice and a fetch failure both replace
// it, in the error style; "fetching…" is appended while a fetch is out.
func (m Model) tilesLine() string {
	if m.Notice != "" {
		return m.styles.Error.Render(fit(m.Notice, m.width))
	}
	if m.fetchErr != "" {
		return m.styles.Error.Render(fit("query failed: "+m.fetchErr, m.width))
	}
	t := m.tiles
	cost := fmt.Sprintf("$%.2f", t.CostUSD)
	if t.Unknown > 0 {
		cost += fmt.Sprintf(" (%d unknown)", t.Unknown)
	}
	s := fmt.Sprintf("rounds %d   cost %s   tokens %s   halted %d · exited %d   median %s   bindings %d · builders %d",
		t.Rounds, cost, shortTokens(t.Tokens), t.Halted, t.Exited,
		shortDuration(t.MedianDurationMS), t.Bindings, t.Builders)
	if m.fetching {
		s += "   fetching…"
	}
	return m.styles.Fg.Render(fit(s, m.width))
}

// thirdLine is §5's line 3: the editor while editing (with any parse error
// beside it), the column header otherwise.
func (m Model) thirdLine() string {
	if m.editing {
		// textinput.View pads to its Width, which would push any parse
		// error past the right edge; trim it back first.
		line := "/ " + strings.TrimRight(m.input.View(), " ")
		if m.parseErr != "" {
			room := m.width - lipgloss.Width(line) - 3
			if room > 0 {
				line += "   " + m.styles.Error.Render(clip(m.parseErr, room))
			}
		}
		return fit(line, m.width)
	}
	if m.grouped() {
		return m.groupHeader()
	}
	return m.roundHeader()
}

// roundHeader labels the round grid's columns, matching roundLine's tiers.
func (m Model) roundHeader() string {
	cols := m.roundColumns(db.RoundRow{})
	labels := make([]string, len(cols))
	for i, c := range cols {
		labels[i] = pad(c.label, c.width)
	}
	return fit(m.styles.Faint.Render(strings.Join(labels, "  ")), m.width)
}

// groupHeader labels the group grid's columns, matching groupLine's tiers.
func (m Model) groupHeader() string {
	cells := []string{
		pad(string(m.query.By), colKey),
		fmt.Sprintf("%6s", "rounds"),
		fmt.Sprintf("%8s", "reported"),
		fmt.Sprintf("%6s", "halted"),
	}
	if m.width >= tierTree {
		cells = append(cells, fmt.Sprintf("%7s", "commits"))
	}
	if m.width >= tierTokens {
		cells = append(cells, fmt.Sprintf("%7s", "tokens"))
	}
	cells = append(cells, fmt.Sprintf("%7s", "cost"), fmt.Sprintf("%10s", "last"))
	return fit(m.styles.Faint.Render(strings.Join(cells, "  ")), m.width)
}

// outcomeCell is a round's outcome word and the colour §5 asks for:
// halted and exited are attention, open is live, the rest normal.
func (m Model) outcomeCell(r db.RoundRow) (string, lipgloss.Style) {
	switch r.Outcome {
	case db.OutcomeHalted, db.OutcomeExited:
		return r.Outcome, m.styles.Attention
	case db.OutcomeOpen:
		return r.Outcome, m.styles.Live
	}
	return r.Outcome, m.styles.Fg
}

// commitsCell is "+N" or "-" when the round recorded no commit facts.
func commitsCell(r db.RoundRow) string {
	if r.Commits == nil {
		return "-"
	}
	return fmt.Sprintf("+%d", *r.Commits)
}

func treeCell(r db.RoundRow) string { return deref(r.Tree) }

func gateCell(r db.RoundRow) string { return deref(r.GateResult) }

// tokensCell is the four token columns summed and shortened, "-" when the
// round recorded none.
func tokensCell(r db.RoundRow) string {
	if r.InTokens == nil && r.CacheTokens == nil && r.WriteTokens == nil && r.OutTokens == nil {
		return "-"
	}
	return shortTokens(tokenValue(r))
}

// costCell is "$X.XX" measured, "~$X.XX" estimated, "?" unknown, and "-"
// when the round recorded no usage at all (§6).
func costCell(r db.RoundRow) string {
	switch {
	case r.CostUSD == nil:
		return "-"
	case r.CostBasis != nil && *r.CostBasis == "unknown":
		return "?"
	case r.CostBasis != nil && *r.CostBasis == "estimated":
		return fmt.Sprintf("~$%.2f", *r.CostUSD)
	}
	return fmt.Sprintf("$%.2f", *r.CostUSD)
}

// groupCostCell is a group's summed cost, "?" beside it when some of its
// rounds' costs were unknown and so not summed.
func groupCostCell(g histq.GroupRow) string {
	if g.Unknown > 0 {
		return fmt.Sprintf("$%.2f?", g.CostUSD)
	}
	return fmt.Sprintf("$%.2f", g.CostUSD)
}

// durationCell is "Nm"/"1h05m", "-" when the round has no closed_at.
func durationCell(r db.RoundRow) string {
	if r.DurationMS == nil {
		return "-"
	}
	return shortDuration(*r.DurationMS)
}

// shortTokens is usage.ShortTokens's rule, spelled here because dash does
// not import internal/usage (§2): < 1000 as-is, < 1M "182k", else "2.3M".
func shortTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 999_500:
		return fmt.Sprintf("%dk", int64(math.Round(float64(n)/1000)))
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// shortDuration is "-" for 0, "<1m" under a minute, "14m", "1h05m".
func shortDuration(ms int64) string {
	switch {
	case ms <= 0:
		return "-"
	case ms < 60_000:
		return "<1m"
	}
	minutes := ms / 60_000
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
}

// stripBy drops a `by:<axis>` token from a query's text, so the header can
// show the filter and the axis as separate clauses without repeating it.
func stripBy(text string) string {
	fields := strings.Fields(text)
	out := fields[:0]
	for _, f := range fields {
		if strings.HasPrefix(f, "by:") {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// deref is a nullable string column, "-" when null.
func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// pad pads s to width cells, truncating with an ellipsis past it.
func pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return clip(s, width)
}

// clip truncates s to width cells, ellipsis included.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(s) + "…"
}

// fit pads or truncates s to width cells (the host's fit, spelled here
// because dash may not import internal/ui).
func fit(s string, width int) string {
	if width <= 0 {
		return s
	}
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// spread puts right at the right edge of a width-wide line, after left,
// dropping the gap when they would overlap.
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}
