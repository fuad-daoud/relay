package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/stats"
)

// statsWindows is the `:stats` window cycle: 7d → 30d → 90d → all → 7d.
var statsWindows = []string{"7d", "30d", "90d", "all"}

// statsRefreshEvery is how stale a report may grow before a tick refetches it.
const statsRefreshEvery = 30 * time.Second

// statsWide is the width at or above which `:stats` lays its panels out in
// three side-by-side bands (§4.3).
const statsWide = 150

// statsMsg is one stats fetch's reply: the report for window, or the fetch
// error, plus any non-fatal read warnings.
type statsMsg struct {
	window   string
	rep      stats.Report
	err      error
	warnings []string
}

// statsView is ':stats': the four-question board of C2a's report (§4.3, spec
// §5). It reads the report from a fetch and never opens the database on the
// render path.
type statsView struct {
	window     string // "7d" | "30d" | "90d" | "all"
	rep        stats.Report
	loaded     bool  // a report has arrived at least once
	err        error // the last fetch error
	fetching   bool  // a fetch is in flight
	fetchedAt  time.Time
	focus      int    // 0 = candidates panel, 1 = repos panel
	cursor     [2]int // selected row per focusable panel
	byProvider bool   // spend split
	top        int    // first body line shown in the stacked layout
}

// statNameW and friends are the candidates panel's columns, in one place so
// the header and the rows always line up.
const (
	statNameW = 24
	statRndsW = 5
	statPctW  = 5
	statMedW  = 5
	statTTFTW = 6
	statCostW = 7
	statGroup = 31 // the repos/features key column
)

// cutFor turns a stats window into the instant before which rounds are
// ignored: zero ("all") for the literal all, else ParseSince's cut.
func cutFor(window string, now time.Time) time.Time {
	if window == "all" {
		return time.Time{}
	}
	cut, err := relevo.ParseSince(window, now)
	if err != nil {
		return time.Time{}
	}
	return cut
}

// newStatsView builds ':stats' on window. It refuses without a database,
// exactly as newRoundsView does; the returned command is the first fetch.
func newStatsView(env Env, window string) (View, tea.Cmd, error) {
	if env.Src.Base().DB == nil {
		return nil, nil, relevo.ErrNoDatabase
	}
	v := statsView{window: window, fetching: true}
	return v, v.fetch(env), nil
}

// fetch is the async read: StatsInputs then Build, reported as a statsMsg.
func (v statsView) fetch(env Env) tea.Cmd {
	window := v.window
	rt := env.Src.Base()
	cut := cutFor(window, env.Now)
	return func() tea.Msg {
		in, warnings, err := relevo.StatsInputs(rt, cut)
		if err != nil {
			return statsMsg{window: window, err: err}
		}
		return statsMsg{window: window, rep: stats.Build(in), warnings: warnings}
	}
}

func (v statsView) Crumbs() []string { return []string{"stats"} }

func (v statsView) Capturing() bool { return false }

func (v statsView) Keys() []KeyHelp {
	return []KeyHelp{
		{"w", "window"},
		{"tab", "panel"},
		{"↑↓", "move"},
		{"enter", "open rounds"},
		{"p", "providers"},
		{"r", "refresh"},
		{"pgup/pgdn", "scroll"},
	}
}

// Context is the totals line on the left and the window and refresh state on
// the right (§4.3).
func (v statsView) Context(env Env) (string, string) {
	t := v.rep.Totals
	left := fmt.Sprintf("%d rounds · %s + %d on plan · %s tok · median %s",
		t.Rounds, stats.Money(t.CostUSD), t.PlanRounds, stats.ShortTokens(t.Tokens), stats.Duration(t.MedianMS))
	right := "window " + v.window
	if v.fetching {
		right += " · refreshing"
	} else if v.err != nil {
		right += " · " + errorStyle.Render(v.err.Error())
	}
	return left, right
}

func (v statsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case statsMsg:
		// A reply for another window is stale after a `w`: drop it.
		if msg.window != v.window {
			return v, nil
		}
		v.fetching = false
		v.fetchedAt = env.Now
		if msg.err != nil {
			// The last good report stays on screen.
			v.err = msg.err
		} else {
			v.rep = msg.rep
			v.err = nil
			v.loaded = true
		}
		if len(msg.warnings) > 0 {
			return v, notice(msg.warnings[0])
		}
		return v, nil

	case tickMsg:
		if !v.fetching && env.Now.Sub(v.fetchedAt) > statsRefreshEvery {
			v.fetching = true
			return v, v.fetch(env)
		}
		return v, nil

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

func (v statsView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	switch k.String() {
	case "w":
		v.window = nextStatsWindow(v.window)
		v.top = 0
		v.fetching = true
		return v, v.fetch(env)
	case "r":
		v.fetching = true
		return v, v.fetch(env)
	case "tab":
		v.focus = 1 - v.focus
		return v, nil
	case "up", "k":
		v.moveCursor(-1)
		return v, nil
	case "down", "j":
		v.moveCursor(1)
		return v, nil
	case "p":
		v.byProvider = !v.byProvider
		return v, nil
	case "pgup":
		if v.top > 0 {
			v.top--
		}
		return v, nil
	case "pgdn":
		v.top++
		return v, nil
	case "enter":
		return v.enter(env)
	}
	return v, nil
}

// nextStatsWindow cycles the window list.
func nextStatsWindow(window string) string {
	for i, w := range statsWindows {
		if w == window {
			return statsWindows[(i+1)%len(statsWindows)]
		}
	}
	return statsWindows[0]
}

// panelRows is the focused panel's row count.
func (v statsView) panelRows() int {
	if v.focus == 0 {
		return len(v.rep.Scorecard)
	}
	return len(v.rep.Repos)
}

// moveCursor moves the focused panel's cursor, clamped to its rows.
func (v *statsView) moveCursor(delta int) {
	n := v.panelRows()
	if n == 0 {
		v.cursor[v.focus] = 0
		return
	}
	c := v.cursor[v.focus] + delta
	if c < 0 {
		c = 0
	}
	if c > n-1 {
		c = n - 1
	}
	v.cursor[v.focus] = c
}

// sinceTerm is the query's ` since:<window>` term, omitted for all.
func (v statsView) sinceTerm() string {
	if v.window == "all" {
		return ""
	}
	return " since:" + v.window
}

// enter opens `:rounds` filtered to the focused panel's selected row (§4.3).
func (v statsView) enter(env Env) (View, tea.Cmd) {
	if v.focus == 0 {
		if len(v.rep.Scorecard) == 0 {
			return v, nil
		}
		i := statsClamp(v.cursor[0], len(v.rep.Scorecard))
		return v.openRounds(env, "candidate:"+v.rep.Scorecard[i].Token+v.sinceTerm())
	}
	if len(v.rep.Repos) == 0 {
		return v, nil
	}
	i := statsClamp(v.cursor[1], len(v.rep.Repos))
	key := v.rep.Repos[i].Key
	if key == "(none)" {
		return v, notice("rounds with no repo cannot be filtered")
	}
	return v.openRounds(env, `repo:"`+key+`"`+v.sinceTerm())
}

// openRounds pushes a rounds view on q, or notices the error.
func (v statsView) openRounds(env Env, q string) (View, tea.Cmd) {
	rv, init, err := newRoundsView(env, q, "")
	if err != nil {
		return v, notice("no database: " + err.Error())
	}
	return v, push(rv, init)
}

// statsClamp keeps an index inside [0, n).
func statsClamp(i, n int) int {
	if i < 0 {
		return 0
	}
	if i > n-1 {
		return n - 1
	}
	return i
}

// Body is the whole board (§4.3): the wide three-band layout, the narrow
// stack, or a state line before the first report.
func (v statsView) Body(env Env, width, height int) string {
	if !v.loaded && v.err != nil {
		lines := strings.Split(renderError(v.err, width), "\n")
		return strings.Join(fitLines(lines, width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if v.rep.Totals.Rounds == 0 {
		return strings.Join(statsCentered("no rounds in this window", width, height), "\n")
	}
	if width >= statsWide {
		return strings.Join(v.wideBody(env, width, height), "\n")
	}
	return strings.Join(v.narrowBody(env, width, height), "\n")
}

// wideBody is the three-band layout: candidates|spend, then reliability full
// width, then repos+features|outcomes. Band 3 is cut at the bottom when the
// bands are taller than height; wide mode does not scroll (§4.3).
func (v statsView) wideBody(env Env, width, height int) []string {
	lw := width * 55 / 100
	rw := width - lw

	band1 := joinColumns(
		v.panel("candidates", v.candidatesLines(env), lw),
		v.panel("spend", v.spendLines(rw), rw),
		lw, rw,
	)
	band2 := v.panel("reliability", v.reliabilityLines(), width)

	left3 := v.panel("repos", v.groupsLines(v.rep.Repos, true), lw)
	if len(v.rep.Features) > 0 {
		left3 = append(left3, v.panel("features", v.groupsLines(v.rep.Features, false), lw)...)
	}
	// The outcomes text is fixed and short; give its panel the width it needs
	// (never less than the band-1 split) so no line is clipped, and the repos
	// side the rest.
	outLines := v.outcomesLines()
	outW := statsTextWidth(outLines)
	if outW < rw {
		outW = rw
	}
	if outW > width {
		outW = width
	}
	band3 := joinColumns(left3, v.panel("outcomes", outLines, outW), width-outW, outW)

	out := append(append(band1, band2...), band3...)
	if len(out) > height {
		out = out[:height]
	}
	return fitLines(out, width, height)
}

// narrowBody is the stacked layout, windowed by top (§4.3).
func (v statsView) narrowBody(env Env, width, height int) []string {
	panels := [][]string{
		v.panel("candidates", v.candidatesLines(env), width),
		v.panel("spend", v.spendLines(width), width),
		v.panel("reliability", v.reliabilityLines(), width),
		v.panel("repos", v.groupsLines(v.rep.Repos, true), width),
	}
	if len(v.rep.Features) > 0 {
		panels = append(panels, v.panel("features", v.groupsLines(v.rep.Features, false), width))
	}
	panels = append(panels, v.panel("outcomes", v.outcomesLines(), width))

	var lines []string
	for _, p := range panels {
		lines = append(lines, p...)
	}
	start := v.top
	if start > len(lines)-height {
		start = len(lines) - height
	}
	if start < 0 {
		start = 0
	}
	end := start + height
	if end > len(lines) {
		end = len(lines)
	}
	return fitLines(lines[start:end], width, height)
}

// panel is a titled rule followed by its lines, each fitted to width.
func (v statsView) panel(title string, lines []string, width int) []string {
	out := []string{statsTitleRule(title, width)}
	for _, l := range lines {
		out = append(out, fit(l, width))
	}
	return out
}

// joinColumns lays two panels side by side line by line, padding the shorter
// side to its width.
func joinColumns(left []string, right []string, leftW, rightW int) []string {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, fit(l, leftW)+fit(r, rightW))
	}
	return out
}

// statsTitleRule is a titled rule in the round view's section-rule idiom:
// "── candidates " then dashes to width.
func statsTitleRule(title string, width int) string {
	label := "── " + title + " "
	if lipgloss.Width(label) >= width {
		return ruleStyle.Render(label)
	}
	return ruleStyle.Render(label + strings.Repeat("─", width-lipgloss.Width(label)))
}

// statsTextWidth is the widest line in out, 0 for none.
func statsTextWidth(out []string) int {
	w := 0
	for _, l := range out {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	return w
}

// statsCentered centres one line in the body box.
func statsCentered(text string, width, height int) []string {
	out := fitLines(nil, width, height)
	if height <= 0 {
		return out
	}
	pad := (width - lipgloss.Width(text)) / 2
	if pad < 0 {
		pad = 0
	}
	out[height/2] = fit(strings.Repeat(" ", pad)+emptyStyle.Render(text), width)
	return out
}

// candidatesLines is the scorecard panel: a faint header, one row per
// ScoreRow and the unrecorded footnote (§4.3).
func (v statsView) candidatesLines(env Env) []string {
	var out []string
	head := fmt.Sprintf("%-*s %*s %*s %*s %*s %*s %*s",
		statNameW, "NAME", statRndsW, "RNDS", statPctW, "DONE", statPctW, "HALT",
		statMedW, "MED", statTTFTW, "TTFT", statCostW, "$/RND")
	out = append(out, "   "+faintStyle.Bold(true).Render(head))

	names := env.Src.Base().Candidates.NameOf
	for i, s := range v.rep.Scorecard {
		row := fmt.Sprintf("%-*s %*d %*s %*s %*s %*s %*s",
			statNameW, stats.FitKey(names(s.Token), statNameW, false),
			statRndsW, s.Rounds,
			statPctW, stats.PctText(s.DonePct, s.Closed),
			statPctW, stats.PctText(s.HaltPct, s.Closed),
			statMedW, stats.MedianText(s),
			statTTFTW, stats.TTFTText(s),
			statCostW, stats.CostPerRoundText(s))
		style := fgStyle
		if s.Few {
			style = faintStyle
		}
		gutter := "   "
		selected := v.focus == 0 && i == v.cursor[0]
		if selected {
			gutter = accentStyle.Render("▎") + "  "
		}
		line := gutter + style.Render(row)
		if selected {
			line = selectedBg.Render(line)
		}
		out = append(out, line)
	}
	out = append(out, faintStyle.Render(fmt.Sprintf("unrecorded: %d rounds", v.rep.Totals.Unrecorded)))
	return out
}

// spendLines is the spend panel: four rows of day bars with a dollar axis,
// the range and the week-over-week line, or one line per provider with `p`
// (§4.3).
func (v statsView) spendLines(width int) []string {
	days := v.rep.Spend.Days
	if v.byProvider {
		return v.providerLines(days)
	}

	dayMax := 0.0
	for _, d := range days {
		if d.USD > dayMax {
			dayMax = d.USD
		}
	}
	axisW := lipgloss.Width(stats.Money(dayMax))
	cols := statsBuckets(days, statsPlotW(width, axisW))
	colMax := 0.0
	for _, c := range cols {
		if c > colMax {
			colMax = c
		}
	}
	// The bucketed max can be longer than a single day's: one correction pass
	// keeps the axis and the plot in the same columns.
	axisW = lipgloss.Width(stats.Money(colMax))
	plotW := statsPlotW(width, axisW)
	cols = statsBuckets(days, plotW)
	colMax = 0.0
	for _, c := range cols {
		if c > colMax {
			colMax = c
		}
	}

	step := statsSteps(cols, colMax, plotW)
	out := make([]string, 0, 7)
	for row := 0; row < 4; row++ {
		prefix := strings.Repeat(" ", axisW) + " │"
		switch row {
		case 0:
			prefix = fit(stats.Money(colMax), axisW) + " ┤"
		case 3:
			prefix = fit(stats.Money(0), axisW) + " ┼"
		}
		out = append(out, prefix+accentStyle.Render(statsBarRow(step, row)))
	}
	out = append(out, "  "+statsRange(days))
	out = append(out, "  "+statsWeekLine(v.rep.Spend))
	return out
}

// providerLines is the `p` spend split: one line per provider, sorted by
// total, a sparkline-style one-row bar per day and the total (§4.3).
func (v statsView) providerLines(days []stats.DayCost) []string {
	type prov struct {
		name   string
		total  float64
		series []stats.DayCost
	}
	idx := map[string]*prov{}
	var order []string
	for _, d := range days {
		for name, usd := range d.ByProvider {
			p := idx[name]
			if p == nil {
				p = &prov{name: name, series: make([]stats.DayCost, len(days))}
				idx[name] = p
				order = append(order, name)
			}
			p.total += usd
		}
	}
	for i := range order {
		p := idx[order[i]]
		for j, d := range days {
			p.series[j] = stats.DayCost{Day: d.Day, USD: d.ByProvider[p.name]}
		}
	}
	sortProvs := make([]*prov, 0, len(order))
	for _, name := range order {
		sortProvs = append(sortProvs, idx[name])
	}
	for i := 1; i < len(sortProvs); i++ {
		for j := i; j > 0 && sortProvs[j].total > sortProvs[j-1].total; j-- {
			sortProvs[j], sortProvs[j-1] = sortProvs[j-1], sortProvs[j]
		}
	}

	out := make([]string, 0, len(sortProvs))
	for _, p := range sortProvs {
		out = append(out, fit(p.name, 16)+" "+accentStyle.Render(stats.Sparkline(p.series))+" "+stats.Money(p.total))
	}
	if len(out) == 0 {
		out = append(out, faintStyle.Render("no known cost in this window"))
	}
	return out
}

// statsPlotW is the bar-area width once the axis gutter is taken out.
func statsPlotW(width, axisW int) int {
	w := width - axisW - 2
	if w < 1 {
		return 1
	}
	return w
}

// statsSteps renders the day buckets as bar cells: every day takes
// statsDayCols columns -- a bar of width-1 blocks then one gap cell -- so a
// window with fewer days than the plot has columns draws wider bars instead of
// bunching one-column bars at the left (W5).
func statsSteps(cols []float64, colMax float64, plotW int) []int {
	groupW := statsDayCols(plotW, len(cols))
	barW := groupW - 1
	if barW < 1 {
		barW = 1
	}
	step := make([]int, 0, len(cols)*groupW)
	for _, c := range cols {
		s := 0
		if colMax > 0 {
			s = int(math.Round(31 * c / colMax))
		}
		for i := 0; i < barW; i++ {
			step = append(step, s)
		}
		for i := barW; i < groupW; i++ {
			step = append(step, 0)
		}
	}
	return step
}

// statsDayCols is the columns one day's bar and its gap take: at most three,
// at least one.
func statsDayCols(plotW, days int) int {
	if days <= 0 {
		return 1
	}
	w := plotW / days
	if w < 1 {
		return 1
	}
	if w > 3 {
		return 3
	}
	return w
}

// statsBuckets sums consecutive days into at most plotW columns, as few days
// per column as needed.
func statsBuckets(days []stats.DayCost, plotW int) []float64 {
	if len(days) == 0 {
		return nil
	}
	chunk := (len(days) + plotW - 1) / plotW
	if chunk < 1 {
		chunk = 1
	}
	var out []float64
	for i := 0; i < len(days); i += chunk {
		sum := 0.0
		for j := i; j < i+chunk && j < len(days); j++ {
			sum += days[j].USD
		}
		out = append(out, sum)
	}
	return out
}

// statsBarRunes are the four-row bars' levels; 0 is empty.
var statsBarRunes = []rune(" ▁▂▃▄▅▆▇█")

// statsBarRow renders one row (0 top, 3 bottom) of the step bars: 8 levels a
// row, 32 steps in all.
func statsBarRow(steps []int, row int) string {
	out := make([]rune, 0, len(steps))
	for _, s := range steps {
		v := s - (3-row)*8
		if v < 0 {
			v = 0
		}
		if v > 8 {
			v = 8
		}
		out = append(out, statsBarRunes[v])
	}
	return string(out)
}

// statsRange is the "MM-DD … MM-DD" line under the bars.
func statsRange(days []stats.DayCost) string {
	if len(days) == 0 {
		return ""
	}
	return stats.MonthDay(days[0].Day) + " … " + stats.MonthDay(days[len(days)-1].Day)
}

// statsWeekLine is "this week $X · last week $Y · ±N%".
func statsWeekLine(sp stats.Spend) string {
	return fmt.Sprintf("this week %s · last week %s · %s",
		stats.Money(sp.ThisWeek), stats.Money(sp.LastWeek), stats.WeekDelta(sp))
}

// reliabilityLines is C2a's reliability section as view lines: the switch
// line, the by-hour limit table (by-hour digits coloured by severity) and the
// active gates (§4.3).
func (v statsView) reliabilityLines() []string {
	rel := v.rep.Reliability
	var out []string
	out = append(out, fgStyle.Render(fmt.Sprintf("switches %d in %d rounds (%.0f%%) · rate limits %d · spawn failures %d",
		rel.Switches, rel.RoundsSwitched, rel.SwitchPct, rel.RateLimits, rel.SpawnFailures)))

	header := "limits by local hour"
	for h := 0; h < 24; h++ {
		header += fmt.Sprintf(" %02d", h)
	}
	out = append(out, dimStyle.Render(header))
	for _, row := range rel.ByHour {
		var b strings.Builder
		b.WriteString("    " + faintStyle.Render(fmt.Sprintf("%-19s", row.Provider)))
		for h := 0; h < 24; h++ {
			switch c := row.Counts[h]; {
			case c >= 2:
				b.WriteString(" " + errorStyle.Render(fmt.Sprintf("%2d", c)))
			case c == 1:
				b.WriteString(" " + stateNeedsYouStyle.Render(fmt.Sprintf("%2d", c)))
			default:
				b.WriteString(" " + faintStyle.Render(" ."))
			}
		}
		out = append(out, b.String())
	}

	if len(rel.Active) == 0 {
		out = append(out, faintStyle.Render("active  none"))
		return out
	}
	parts := make([]string, 0, len(rel.Active))
	for _, g := range rel.Active {
		parts = append(parts, fmt.Sprintf("%s %s until %s",
			g.Token, strings.ReplaceAll(string(g.Kind), "_", "-"), stats.UntilText(g.Until)))
	}
	out = append(out, fgStyle.Render("active  "+strings.Join(parts, " · ")))
	return out
}

// groupsLines is C2a's repos or features table as view lines: keys fitted and
// clipped from the left after their scheme is dropped. cursor is true for the
// repos panel, whose selected row carries the fleet's gutter and selectedBg
// (§4.3).
func (v statsView) groupsLines(rows []stats.GroupRow, cursor bool) []string {
	out := make([]string, 0, len(rows)+1)
	out = append(out, "   "+faintStyle.Bold(true).Render(fmt.Sprintf("%-*s%4s  %6s  %4s  %9s",
		statGroup+2, "", "RNDS", "COST", "HALT", "RNDS/LAND")))
	for i, g := range rows {
		key := stats.FitKey(stats.StripScheme(g.Key), statGroup, true)
		row := fmt.Sprintf("  %s%4d  %6s  %4d  %9s",
			key, g.Rounds, stats.Money(g.CostUSD), g.Halted, stats.RoundsPerLandText(g))
		selected := cursor && v.focus == 1 && i == v.cursor[1]
		gutter := "   "
		if selected {
			gutter = accentStyle.Render("▎") + "  "
		}
		line := gutter + fgStyle.Render(row)
		if selected {
			line = selectedBg.Render(line)
		}
		out = append(out, line)
	}
	return out
}

// outcomesLines is C2a's outcomes section as view lines.
func (v statsView) outcomesLines() []string {
	out := []string{
		fgStyle.Render("rounds   " + statsCountPairs(statsOutcomeKeys, v.rep.Outcomes.ByRound)),
		fgStyle.Render("reports  " + statsCountPairs(statsReportKeys, v.rep.Outcomes.ByReport)),
	}
	return out
}

// statsOutcomeKeys and statsReportKeys are C2a's count orders, repeated here
// because internal/stats keeps them unexported.
var statsOutcomeKeys = []struct{ key, label string }{
	{db.OutcomeReported, "reported"},
	{db.OutcomeHalted, "halted"},
	{db.OutcomeSwitched, "switched"},
	{db.OutcomeExited, "exited"},
	{db.OutcomeDoneNoReport, "no report"},
	{db.OutcomeOpen, "open"},
}

var statsReportKeys = []struct{ key, label string }{
	{"done", "done"},
	{"halted", "halted"},
	{"blocked", "blocked"},
	{"deferred", "deferred"},
	{"no outcome", "no outcome"},
}

// statsCountPairs renders "label n" for every key, joined by " · ".
func statsCountPairs(keys []struct{ key, label string }, counts map[string]int) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k.label, counts[k.key]))
	}
	return strings.Join(parts, " · ")
}
