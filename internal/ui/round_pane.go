package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// The pane's own furniture, in rows. Moved here from layout.go (X1), whose
// only surviving users are the pane's viewport arithmetic and the round
// view's geometry.
const (
	paneHeadRows = 5 // title, planner, builder, tree, blank
	tabRows      = 2 // tab bar + rule
	sourceRows   = 2 // source line + blank
)

// roundPane is one binding's round detail: its state, its fetch
// orchestration and its rendering, lifted out of Model (§4.4, §5.1). Model
// holds exactly one and lends it the fields it cannot own -- src, ctx, now,
// report, width and rows -- through syncPane on every call.
type roundPane struct {
	src    Source
	ctx    context.Context
	now    func() time.Time
	report relevo.Report
	detail detailModel

	// tabInFlight is the pane's own fetch guard, moved from
	// Model.tabInFlight: true while a fetch is in flight for the tab on
	// screen. It is separate from statusInFlight because a terminal read
	// can block.
	tabInFlight bool

	// width and rows are the pane's geometry: today's Model.paneWidth()
	// and Model.bodyRows().
	width int
	rows  int
}

// viewportHeight is the rows left for the viewport after the pane's own
// furniture, floored at 0. The same formula as Model.viewportHeight
// (layout.go:103-109).
func (p roundPane) viewportHeight() int {
	h := p.rows - paneHeadRows - tabRows - sourceRows
	if h < 0 {
		return 0
	}
	return h
}

func (p roundPane) visibleTabFetch() tea.Cmd {
	lines := p.detail.vp.Height
	if lines < 1 {
		lines = 1
	}
	t := p.detail.active
	if t == tabTerminal && p.detail.live {
		// A live terminal shows the builder's screen right now, so it
		// refetches on every visible tick regardless of cache; a hist
		// row's terminal is transcript rows already in the database --
		// static, fetched once like every other tab (not tail-following).
		return fetchFor(p.ctx, p.src, tabTerminal, p.detail.name, p.detail.round, lines, p.detail.live)
	}
	if !p.detail.cache[t].loaded {
		return fetchFor(p.ctx, p.src, t, p.detail.name, p.detail.round, lines, p.detail.live)
	}
	return nil
}

// pointDetailAt re-targets the pane at the row keyed: key, round
// (paneRound), lastLogTS from row.Last, every cache cleared, every
// parked scroll zeroed. The active tab is kept -- a human reading diffs
// across bindings stays on diff. It issues the visible-tab fetch only if
// tabInFlight is clear; a fetch already in flight for the previous
// binding is discarded on arrival by tabMsg's name check, which exists
// for exactly this. A no-op when the pane already shows key.
func (p roundPane) pointDetailAt(key string) (roundPane, tea.Cmd) {
	if p.detail.name == key {
		return p, nil
	}
	r := row(p.report, key)
	if r == nil {
		return p, nil
	}
	// #143: opening a binding's detail pane is what "viewed" means; the
	// stamp is best-effort (each Source swallows its own errors) and must
	// never block re-targeting the pane.
	p.src.MarkViewed(key)
	vp := viewport.New(p.width, p.viewportHeight())
	p.detail = detailModel{
		name:     key,
		round:    paneRound(*r),
		rounds:   r.Round,
		live:     true,
		active:   p.detail.active,
		vp:       vp,
		headless: r.Headless != nil,
		follow:   true,
	}
	if r.Last != nil {
		p.detail.lastLogTS = r.Last.TS
	}
	p.fillViewport()
	if p.tabInFlight {
		return p, nil
	}
	if cmd := p.visibleTabFetch(); cmd != nil {
		p.tabInFlight = true
		return p, cmd
	}
	return p, nil
}

// pointDetailAtHist re-targets the pane at h, a database row not in the
// live report (#172, §5.8): live false, round the newest -- every one of
// h's rounds is closed, unlike a live row's round-1 rule -- rounds h.Rounds,
// archivedAt from h, follow false (a hist row's terminal is transcript
// rows, never tailed). Every cache cleared, the active tab kept, exactly
// like pointDetailAt. A no-op when the pane already shows h.Name.
func (p roundPane) pointDetailAtHist(h relevo.HistoryBinding) (roundPane, tea.Cmd) {
	if p.detail.name == h.Name {
		return p, nil
	}
	vp := viewport.New(p.width, p.viewportHeight())
	p.detail = detailModel{
		name:       h.Name,
		bindingID:  h.ID,
		live:       false,
		round:      h.Rounds,
		rounds:     h.Rounds,
		archivedAt: h.ArchivedAt,
		active:     p.detail.active,
		vp:         vp,
		follow:     false,
	}
	p.fillViewport()
	if p.tabInFlight {
		return p, nil
	}
	if cmd := p.visibleTabFetch(); cmd != nil {
		p.tabInFlight = true
		return p, cmd
	}
	return p, nil
}

// fillViewport sets the viewport to the active tab's body, wrapped to the
// viewport's width, keeping the current offset (the viewport clamps it).
// Every SetContent goes through here so a resize re-wraps.
func (p *roundPane) fillViewport() {
	y := p.detail.vp.YOffset
	if p.detail.name == "" {
		// Nothing is pointed at, so nothing is loading: an empty fleet's
		// pane stays blank rather than promising content.
		p.detail.vp.SetContent("")
		return
	}
	c := p.detail.cache[p.detail.active]
	p.detail.vp.SetContent(wrapBody(bodyOf(p.detail.active, c, p.detail.headless), p.detail.vp.Width))
	p.detail.vp.SetYOffset(y)
}

// invalidate re-reads the row: when the newest log timestamp moved it drops
// the caches, re-points the round and re-fetches the active tab. gone is
// true when the row is no longer in the report; the pane is then left
// untouched and Model handles the screen, the notice and the re-point. This
// is the part of Model.maybeInvalidate after its first guard; Model keeps
// that guard and the gone branch.
func (p roundPane) invalidate() (roundPane, tea.Cmd, bool) {
	r := row(p.report, p.detail.name)
	if r == nil {
		return p, nil, true
	}
	if r.Last == nil {
		return p, nil, false
	}
	if r.Last.TS.Equal(p.detail.lastLogTS) {
		return p, nil, false
	}

	p.detail.lastLogTS = r.Last.TS
	p.detail.round = paneRound(*r)
	p.detail.rounds = r.Round
	for _, t := range []tab{tabPlan, tabReport, tabDiff, tabLog} {
		p.detail.cache[t] = tabContent{} // loaded=false
		p.detail.scroll[t] = 0           // reset parked offset on invalidation
	}
	if !p.tabInFlight {
		cmd := p.visibleTabFetch()
		if cmd != nil {
			p.tabInFlight = true
			return p, cmd, false
		}
	}
	return p, nil, false
}

// stepRound moves detail.round by delta, clamped to [1, detail.rounds] --
// "[" and "]" step a binding's rounds, live and archived alike (#183). At
// either edge it is a no-op with no notice. Every tab's cache is
// invalidated (a round-keyed fetch is meaningless against the old round's
// reply) and the active tab is re-fetched.
func (p roundPane) stepRound(delta int) (roundPane, tea.Cmd) {
	next := p.detail.round + delta
	if next < 1 || next > p.detail.rounds {
		return p, nil
	}
	p.detail.round = next
	for t := tab(0); t < tabCount; t++ {
		p.detail.cache[t] = tabContent{} // loaded=false
		p.detail.scroll[t] = 0           // reset parked offset on invalidation
	}
	p.fillViewport()
	if p.tabInFlight {
		return p, nil
	}
	if cmd := p.visibleTabFetch(); cmd != nil {
		p.tabInFlight = true
		return p, cmd
	}
	return p, nil
}

// onTab is the tabMsg arm's work: a reply for a name, round or tab the pane
// is no longer showing is stale and is dropped. Model keeps the
// paneVisible() early return.
func (p roundPane) onTab(msg tabMsg) roundPane {
	if msg.name != p.detail.name {
		return p
	}
	// Every tab is round-keyed now (#183): plan, report, terminal and
	// log all read the specific round fetchFor was called with, the
	// same way diff always has. A reply for a round that is no longer
	// the one on screen -- a slow fetch outlived by two presses of "]"
	// -- is stale and must never land in the cache.
	if msg.round != p.detail.round {
		return p
	}
	if msg.t != p.detail.active {
		p.detail.cache[msg.t] = msg.content
		return p
	}
	p.detail.cache[msg.t] = msg.content
	p.fillViewport()
	if msg.t == tabTerminal && p.detail.follow {
		p.detail.vp.GotoBottom()
	}
	return p
}

// cycleTab is tab / shift+tab.
func (p roundPane) cycleTab(msg tea.KeyMsg) (roundPane, tea.Cmd) {
	if msg.Type == tea.KeyShiftTab || msg.String() == "shift+tab" || msg.String() == "back_tab" {
		return p.switchTab((p.detail.active - 1 + tabCount) % tabCount)
	}
	return p.switchTab((p.detail.active + 1) % tabCount)
}

func (p roundPane) switchTab(next tab) (roundPane, tea.Cmd) {
	p.detail.scroll[p.detail.active] = p.detail.vp.YOffset // park
	p.detail.active = next
	c := p.detail.cache[next]
	p.fillViewport()
	p.detail.vp.SetYOffset(p.detail.scroll[next]) // restore
	if next == tabTerminal && p.detail.follow {
		p.detail.vp.GotoBottom()
	}
	if !c.loaded && !p.tabInFlight {
		p.tabInFlight = true
		lines := p.detail.vp.Height
		if lines < 1 {
			lines = 1
		}
		return p, fetchFor(p.ctx, p.src, next, p.detail.name,
			p.detail.round, lines, p.detail.live)
	}
	return p, nil
}

// detailHeader is the detail pane's identity line (#183): name and round N
// of M, plus "archived <date>" for a hist row the database recorded as
// archived, plus "live" when the round on screen is a live binding's own
// open (not yet closed) round.
func (p roundPane) detailHeader() string {
	s := fmt.Sprintf("%s · round %d of %d", p.detail.name, p.detail.round, p.detail.rounds)
	if !p.detail.archivedAt.IsZero() {
		s += " · archived " + p.detail.archivedAt.Format("2006-01-02")
	}
	if p.detail.live && p.detail.round == p.detail.rounds {
		s += " · live"
	}
	return s
}

// paneHead is the pane's first rows: title with the state pill and the
// last event, planner, builder, tree, blank -- plus a usage and a spend row
// when the binding has them, which is why the caller measures it rather than
// assuming paneHeadRows. Each row is unpadded; view fits them.
func (p roundPane) paneHead(b *relevo.BindingStatus) []string {
	label := func(s string) string { return dimStyle.Render(fmt.Sprintf("%-9s", s)) }
	if b == nil {
		return []string{"", "", "", "", ""}
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render(b.Name) +
		dimStyle.Render(fmt.Sprintf("  round %d  ", b.Round)) + pillStyle(b.Display).Render(b.Display)
	if b.Last != nil {
		right := dimStyle.Render(fmt.Sprintf("%s r%d · %s ago", b.Last.Kind, b.Last.Round, ago(b.Last.TS, p.now())))
		title = spread(title, right, p.width)
	}

	plannerName := b.PlannerName
	if plannerName == "" {
		plannerName = b.PlannerID
	}
	planner := label("planner") + fmt.Sprintf("%-4s %-9s route %s", plannerName, b.PlannerKind, b.PlannerRoute)
	// On a serve box the pane belongs to a client, not to this planner: the
	// client line replaces the planner line (empty OwnerLabel is a planner
	// row, which renders today's line above).
	if b.OwnerLabel != "" {
		planner = label("client") + b.OwnerLabel + "  (" + dimStyle.Render(relevo.ShortOwner(b.Owner)) + ")"
	}

	var bparts []string
	if b.Headless != nil {
		bparts = append(bparts, stateStyle(b.Display).Render(b.BuilderStatus))
		if b.Headless.PID != 0 {
			bparts = append(bparts, dimStyle.Render(fmt.Sprintf("pid %d since %s", b.Headless.PID, b.Headless.StartedAt.Local().Format("15:04"))))
		}
		bparts = append(bparts, fgStyle.Render("`"+b.BuilderCandidate+"`"))
	} else {
		bparts = append(bparts, builderStatusStyle(b.BuilderStatus).Render(b.BuilderStatus))
	}
	if b.Consults > 0 {
		bparts = append(bparts, dimStyle.Render(fmt.Sprintf("%d consults", b.Consults)))
	}
	if b.Switches > 0 {
		bparts = append(bparts, dimStyle.Render(fmt.Sprintf("switched %dx", b.Switches)))
	}
	builder := label("builder") + fmt.Sprintf("%-9s ", b.BuilderKind) + strings.Join(bparts, sep)

	rows := []string{title, planner, builder}

	var tparts []string
	if b.Branch != "" {
		tparts = append(tparts, fgStyle.Render(b.Branch))
	} else {
		tparts = append(tparts, fgStyle.Render(b.CWD))
	}
	if b.Dirty {
		tparts = append(tparts, stateNeedsYouStyle.Render("dirty"))
	}
	if lc := b.LastClose; lc != nil {
		unit := "commits"
		if lc.Commits == 1 {
			unit = "commit"
		}
		s := fmt.Sprintf("last close r%d: %d %s", lc.Round, lc.Commits, unit)
		if lc.Tree != "" {
			s += ", " + lc.Tree
		}
		tparts = append(tparts, dimStyle.Render(s))
	}
	rows = append(rows, label("tree")+strings.Join(tparts, sep))
	// usage and spend mirror `relevo status`'s rows (#142): the newest
	// round's line, then the binding's total. A running round shows its
	// live figure instead of the last closed one's (#234); spend is
	// closed rounds only and never shares a cell with the live figure.
	if b.LiveUsage != nil {
		parts := usage.LiveParts(*b.LiveUsage)
		styled := make([]string, len(parts))
		for i, p := range parts {
			switch {
			case i == 0:
				styled[i] = accentStyle.Render(p) // the word "live" is the point
			case i == len(parts)-1:
				styled[i] = fgStyle.Render(p) // the cost word is the point
			default:
				styled[i] = dimStyle.Render(p)
			}
		}
		rows = append(rows, label("usage")+strings.Join(styled, sep))
	} else if b.LastUsage != nil {
		parts := usage.Parts(*b.LastUsage)
		styled := make([]string, len(parts))
		for i, p := range parts {
			styled[i] = dimStyle.Render(p)
		}
		styled[len(styled)-1] = fgStyle.Render(parts[len(parts)-1]) // the cost word is the point
		rows = append(rows, label("usage")+strings.Join(styled, sep))
	}
	if b.Spend != nil {
		rows = append(rows, label("spend")+usage.SpendLine(*b.Spend))
	}
	rows = append(rows, "")
	return rows
}

// histPaneHead is paneHead for a hist (archived) detail: the identity line
// (detailHeader) alone, padded to paneHead's row budget -- an archived
// binding carries no live planner/builder/tree facts to show.
func (p roundPane) histPaneHead() []string {
	return []string{fgStyle.Render(p.detailHeader()), "", "", "", ""}
}

// tabBar is the five tab words and, under them, a rule whose heavy accent
// segment sits under the active word (spec §3.4). No numbers: 1-5 still
// switch, the footer says so.
func (p roundPane) tabBar() []string {
	var words, rule []string
	for i, t := range tabTitles {
		label := " " + t + " "
		if tab(i) == p.detail.active {
			words = append(words, activeTabStyle.Render(label))
			rule = append(rule, accentStyle.Render(strings.Repeat("━", lipgloss.Width(label))))
		} else {
			words = append(words, inactiveTabStyle.Render(label))
			rule = append(rule, ruleStyle.Render(strings.Repeat("─", lipgloss.Width(label))))
		}
	}
	gap := ruleStyle.Render("──")
	line := strings.Join(rule, gap)
	if pad := p.width - lipgloss.Width(line); pad > 0 {
		line += ruleStyle.Render(strings.Repeat("─", pad))
	}
	// tabSpans (mouse.go) assumes this two-space join to compute each
	// word's column span; change both together.
	return []string{strings.Join(words, "  "), line}
}

// sourceLine says, in one faint line, what the viewport is showing.
func (p roundPane) sourceLine() string {
	if p.detail.name == "" {
		return ""
	}
	c := p.detail.cache[p.detail.active]
	if !c.loaded {
		return faintStyle.Render("loading…")
	}
	var s string
	switch p.detail.active {
	case tabPlan:
		s = fmt.Sprintf("plan r%d · %s", c.round, c.at.Local().Format("15:04"))
	case tabReport:
		s = fmt.Sprintf("report r%d · %s", c.round, c.at.Local().Format("15:04"))
	case tabTerminal:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		r := row(p.report, p.detail.name)
		if c.transcript {
			src := "pane"
			if p.detail.headless {
				src = "headless"
				if r != nil && r.Server != "" {
					src = "remote"
				}
			}
			if c.logName != "" {
				src += " · " + c.logName
			}
			mode := "following"
			if !p.detail.follow {
				mode = "scrolled"
			}
			s = fmt.Sprintf("%s · %d lines · %s", src, n, mode)
		} else {
			pane := ""
			if r != nil {
				pane = "remote"
				if r.Headless != nil {
					pane = r.ProcessWord()
				}
			}
			s = fmt.Sprintf("%s · captured %s ago · %d lines", pane, ago(c.at, p.now()), n)
		}
	case tabDiff:
		files, add, del := diffStat(c.body)
		unit := "files"
		if files == 1 {
			unit = "file"
		}
		s = fmt.Sprintf("round %d · %d %s · +%d −%d", p.detail.round, files, unit, add, del)
	case tabLog:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		s = fmt.Sprintf("%d entries", n)
	}
	if c.err != nil || c.empty != "" {
		// The viewport carries the prose; the source line says only where
		// it looked.
		switch p.detail.active {
		case tabPlan:
			s = fmt.Sprintf("round %d", p.detail.round)
		case tabReport:
			s = "report"
		case tabDiff:
			s = fmt.Sprintf("round %d", p.detail.round)
		case tabLog:
			s = "log"
		}
	}
	return faintStyle.Render(s)
}

// hintLine is the one rendered line under a blocked builder's dialog on
// the terminal tab: the verb that resolves it (spec §3.4). The ui runs
// nothing; it names the command.
func (p roundPane) hintLine(b *relevo.BindingStatus) (string, bool) {
	if b == nil || b.Waiting == nil || b.Waiting.Cause != "blocked" || p.detail.active != tabTerminal {
		return "", false
	}
	return accentStyle.Render("relevo: ") + fgStyle.Render(b.Waiting.Hint), true
}

// view draws exactly rows rows at width: head, tabs, source, blank,
// viewport, with the hint replacing the last viewport row when it applies.
// The viewport is resized to what is left after a head that grew by foreign
// rows. Model.paneView keeps the empty-fleet branch.
func (p roundPane) view(width int) string {
	b := row(p.report, p.detail.name)
	rows := []string{}
	if p.detail.name != "" && !p.detail.live {
		rows = append(rows, p.histPaneHead()...)
	} else {
		rows = append(rows, p.paneHead(b)...)
	}
	rows = append(rows, p.tabBar()...)
	rows = append(rows, p.sourceLine(), "")
	budget := p.rows - len(rows)
	if budget < 0 {
		budget = 0
	}
	hint, hasHint := p.hintLine(b)
	vpRows := budget
	if hasHint && vpRows > 0 {
		vpRows--
	}
	vp := p.detail.vp
	vp.Width = width
	vp.Height = vpRows
	if vpRows > 0 {
		rows = append(rows, strings.Split(vp.View(), "\n")...)
	}
	if hasHint && budget > 0 {
		rows = append(rows, hint)
	}
	for len(rows) < p.rows {
		rows = append(rows, "")
	}
	rows = rows[:p.rows]
	for i := range rows {
		rows[i] = fit(rows[i], width)
	}
	return strings.Join(rows, "\n")
}
