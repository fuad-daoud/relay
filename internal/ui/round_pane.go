package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// roundPane is one binding's round detail: its state, its fetch
// orchestration and its rendering, lifted out of Model (§4.4, §5.1). Model
// holds exactly one and lends it the fields it cannot own -- src, ctx, now,
// report, width and rows -- through syncPane on every call.
type roundPane struct {
	src     Source
	ctx     context.Context
	now     func() time.Time
	report  relevo.Report
	detail  detailModel
	actions bool // Actions != nil: action keys are shown

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

// headRows returns the number of furniture rows before the viewport (§2.5).
func (p roundPane) headRows() int {
	return 6
}

// contentWidth is the width allocated for viewport content, accounting for the
// 5-space left indent (§2.5).
func (p roundPane) contentWidth() int {
	cw := p.width - 6
	if cw < 20 {
		return 20
	}
	return cw
}

// viewportHeight is the rows left for the viewport after the pane's own
// furniture, floored at 0 (§2.5).
func (p roundPane) viewportHeight() int {
	h := p.rows - p.headRows()
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
	vp := viewport.New(p.contentWidth(), p.viewportHeight())
	p.detail = detailModel{
		name:     key,
		round:    paneRound(*r),
		rounds:   roundsOf(*r),
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
	vp := viewport.New(p.contentWidth(), p.viewportHeight())
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
	w := p.detail.vp.Width
	if w <= 0 {
		w = p.contentWidth()
	}
	p.detail.vp.SetContent(wrapBody(bodyOf(p.detail.active, c, p.detail.headless), w))
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
	p.detail.rounds = roundsOf(*r)
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
	} else {
		b := row(p.report, p.detail.name)
		if b != nil && p.detail.live && p.detail.round == p.detail.rounds && b.RoundEnd.IsZero() {
			s += " · live"
		}
	}
	return s
}

// tokensLine renders the tokens and facts row at the top of the body (§3.3).
func (p roundPane) tokensLine(b *relevo.BindingStatus) string {
	if b == nil || !p.detail.live {
		left := "   " + faintStyle.Render("no live facts for a released binding")
		return spread(left, "", p.width)
	}

	var rawParts []string
	if b.LiveUsage != nil {
		rawParts = usage.LiveParts(*b.LiveUsage)
	} else if b.LastUsage != nil {
		rawParts = usage.Parts(*b.LastUsage)
	}

	body := "tokens "
	if len(rawParts) > 0 {
		var filtered []string
		for i, part := range rawParts {
			isLast := i == len(rawParts)-1
			if isLast && strings.HasPrefix(part, "unknown") {
				part = "no price"
			}
			if strings.HasPrefix(part, "in ") ||
				strings.HasPrefix(part, "cache ") ||
				strings.HasPrefix(part, "out ") ||
				(strings.HasPrefix(part, "write ") && part != "write 0") ||
				isLast {
				filtered = append(filtered, part)
			}
		}
		if len(filtered) > 0 {
			body += strings.Join(filtered, " · ")
		} else {
			body += "no usage yet"
		}
	} else {
		body += "no usage yet"
	}

	if b.LastClose != nil && b.LastClose.Commits > 0 {
		unit := "commits"
		if b.LastClose.Commits == 1 {
			unit = "commit"
		}
		body += fmt.Sprintf(" · +%d %s", b.LastClose.Commits, unit)
	}
	if s := spendCell(*b); s != "" {
		body += " · spend " + s
	}

	left := "   " + faintStyle.Render(body)

	var right string
	if b.Headless != nil && b.Headless.PID != 0 {
		right = faintStyle.Render(fmt.Sprintf("pid %d since %s", b.Headless.PID, b.Headless.StartedAt.Local().Format("15:04"))) + "  "
	}

	return spread(left, right, p.width)
}

// tabsRow renders the pill tabs and the round stepper on the right (§2.4).
func (p roundPane) tabsRow() string {
	words := make([]string, len(tabTitles))
	for i, t := range tabTitles {
		if tab(i) == p.detail.active {
			words[i] = chip(chipAccentStyle, t)
		} else {
			words[i] = mutedStyle.Render(chip(normalStyle, t))
		}
	}
	left := "  " + strings.Join(words, "   ")

	rightText := fmt.Sprintf("  r%d of %d  ", p.detail.round, p.detail.rounds)
	right := faintStyle.Render("round  ") + chip(kbdStyle, "[") + textStyle.Bold(true).Render(rightText) + chip(kbdStyle, "]") + "  "

	return spread(left, right, p.width)
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
		if c.at.IsZero() {
			s = fmt.Sprintf("plan r%d", c.round)
		} else {
			s = fmt.Sprintf("plan r%d · %s", c.round, c.at.Local().Format("15:04"))
		}
	case tabReport:
		if c.at.IsZero() {
			s = fmt.Sprintf("report r%d", c.round)
		} else {
			s = fmt.Sprintf("report r%d · %s", c.round, c.at.Local().Format("15:04"))
		}
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

// view draws exactly rows rows at width (§2.5, §5).
func (p roundPane) view(width int) string {
	b := row(p.report, p.detail.name)
	out := []string{
		p.tokensLine(b),
		"",
		p.tabsRow(),
		"",
		"     " + p.sourceLine(),
		"",
	}

	budget := p.rows - len(out)
	if budget < 0 {
		budget = 0
	}
	hint, hasHint := p.hintLine(b)
	vpRows := budget
	if hasHint && vpRows > 0 {
		vpRows--
	}
	vp := p.detail.vp
	vp.Width = p.contentWidth()
	vp.Height = vpRows
	if vpRows > 0 {
		vpLines := strings.Split(vp.View(), "\n")
		for _, l := range vpLines {
			out = append(out, "     "+l)
		}
	}
	if hasHint && budget > 0 {
		out = append(out, hint)
	}
	for len(out) < p.rows {
		out = append(out, "")
	}
	out = out[:p.rows]
	for i := range out {
		out[i] = fit(out[i], width)
	}
	return strings.Join(out, "\n")
}
