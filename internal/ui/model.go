package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
)

type Model struct {
	rt   relay.Runtime
	ctx  context.Context
	opts Options

	screen screen

	report relay.Report // newest GOOD snapshot; survives a failed refresh
	err    error        // last refresh error, shown in the footer
	notice string       // sticky note (e.g. "webshop is gone"), cleared on keypress

	// Two guards, not one. statusInFlight and tabInFlight are separate
	// because a terminal read is a 30s-timeout herdr call: a single shared
	// guard would let one slow ReadAgent stall every list refresh behind it,
	// freezing the fleet view for half a minute. Each is cleared by its own
	// message.
	statusInFlight bool
	tabInFlight    bool

	list   listModel
	detail detailModel

	// statusLoaded is false until the first successful statusMsg. It separates
	// "no bindings" -- a fact relay.Status returned -- from "not yet asked" and
	// "could not ask", which are not the same fact and must not read as one.
	statusLoaded bool

	width, height int
	ready         bool // set on the first WindowSizeMsg

	// sort is true for attention order (the default); s toggles it. Lives
	// for the process only -- persisting it is #143's sidecar question.
	sort bool
	// railCols is the rail's stored width preference, in columns; 0 (a
	// fresh model) reads as railDefault. railWidth() clamps it to the
	// current terminal.
	railCols int
	// drag is true while a press on the rail│pane divider is held down.
	drag bool
	// compact is true for the one-line-per-binding rail; c toggles it.
	compact bool
	// now is the clock every age on screen is measured against. time.Now
	// in production; fixed in tests so "2m ago" is deterministic.
	now func() time.Time
	// statusAt is when the newest good statusMsg arrived; the footer's
	// "refreshed Ns ago".
	statusAt time.Time

	// scope is the rail's breadth: live (today's bindings only, the
	// default) or all (every binding the database has ever recorded);
	// "a" toggles it (docs/specs/2026-09-20-persistence-design.md §5.8).
	scope scope
	// dbRows is the database's rows for scope all, fetched alongside the
	// live report; nil in scope live, and nil (with a notice) when the
	// database is unavailable.
	dbRows []relay.HistoryBinding
}

func newModel(ctx context.Context, rt relay.Runtime, opts Options) Model {
	return Model{
		rt:             rt,
		ctx:            ctx,
		opts:           opts,
		screen:         screenList,
		statusInFlight: true,
		sort:           true,
		railCols:       railDefault,
		now:            time.Now,
	}
}

// rows is the report's bindings in display order. Every index in the
// model -- list.cursor, list.top -- indexes THIS slice, never
// report.Bindings directly.
func (m Model) rows() []relay.BindingStatus {
	return relay.SortRows(m.report.Bindings, m.sort)
}

// empty reports whether the fleet has no rows to show. It is the one place
// the zero-binding rule lives; paneView, footerView, the key and mouse
// handlers and the statusMsg arm all ask it, never len(m.rows()) directly.
// It is about the live report specifically -- scope all's archived rows
// are browsable even when it is true; railRows(), not rows(), is what the
// rail actually draws.
func (m Model) empty() bool {
	return m.statusLoaded && len(m.rows()) == 0
}

// railRows is the rail's current row set: live rows only in scope live
// (each wrapped so railRows() behaves identically to rows() for every
// existing, live-only call site), or the live+hist union in scope all
// (scopeRows, §5.8). list.cursor always indexes this slice.
func (m Model) railRows() []railRow {
	live := m.rows()
	if m.scope != scopeAll {
		out := make([]railRow, len(live))
		for i := range live {
			out[i] = railRow{live: &live[i]}
		}
		return out
	}
	return scopeRows(live, m.dbRows)
}

// tick re-arms the poll. It is the ONLY timer; there is no goroutine.
func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchStatus(m.ctx, m.rt, m.scope, m.opts.Here),
		tick(m.opts.Interval),
	)
}

func row(rep relay.Report, name string) *relay.BindingStatus {
	for i := range rep.Bindings {
		if rep.Bindings[i].Name == name {
			return &rep.Bindings[i]
		}
	}
	return nil
}

// paneVisible: the pane is on screen in split layout always, and in
// stack layout only on the detail screen. visibleTabFetch and the tick
// both defer to it (old spec §6 rule 2).
func (m Model) paneVisible() bool {
	return m.layout() == layoutSplit || m.screen == screenDetail
}

func (m Model) visibleTabFetch() tea.Cmd {
	if !m.paneVisible() {
		return nil
	}
	lines := m.detail.vp.Height
	if lines < 1 {
		lines = 1
	}
	t := m.detail.active
	if t == tabTerminal && m.detail.live {
		// A live terminal shows the builder's screen right now, so it
		// refetches on every visible tick regardless of cache; a hist
		// row's terminal is transcript rows already in the database --
		// static, fetched once like every other tab (not tail-following).
		return fetchFor(m.ctx, m.rt, tabTerminal, m.detail.name, m.detail.round, lines, m.detail.live)
	}
	if !m.detail.cache[t].loaded {
		return fetchFor(m.ctx, m.rt, t, m.detail.name, m.detail.round, lines, m.detail.live)
	}
	return nil
}

// pointDetailAt re-targets the pane at the binding named: name, round
// (row.Round - 1), lastLogTS from row.Last, every cache cleared, every
// parked scroll zeroed. The active tab is kept -- a human reading diffs
// across bindings stays on diff. It issues the visible-tab fetch only if
// tabInFlight is clear; a fetch already in flight for the previous
// binding is discarded on arrival by tabMsg's name check, which exists
// for exactly this. A no-op when the pane already shows name.
func (m Model) pointDetailAt(name string) (Model, tea.Cmd) {
	if m.detail.name == name {
		return m, nil
	}
	r := row(m.report, name)
	if r == nil {
		return m, nil
	}
	vp := viewport.New(m.paneWidth(), m.viewportHeight())
	m.detail = detailModel{
		name:     name,
		round:    r.Round - 1,
		rounds:   r.Round,
		live:     true,
		active:   m.detail.active,
		vp:       vp,
		headless: r.Headless != nil,
		follow:   true,
	}
	if r.Last != nil {
		m.detail.lastLogTS = r.Last.TS
	}
	m.fillViewport()
	if m.tabInFlight {
		return m, nil
	}
	if cmd := m.visibleTabFetch(); cmd != nil {
		m.tabInFlight = true
		return m, cmd
	}
	return m, nil
}

// pointDetailAtHist re-targets the pane at h, a database row not in the
// live report (#172, §5.8): live false, round the newest -- every one of
// h's rounds is closed, unlike a live row's round-1 rule -- rounds h.Rounds,
// archivedAt from h, follow false (a hist row's terminal is transcript
// rows, never tailed). Every cache cleared, the active tab kept, exactly
// like pointDetailAt. A no-op when the pane already shows h.Name.
func (m Model) pointDetailAtHist(h relay.HistoryBinding) (Model, tea.Cmd) {
	if m.detail.name == h.Name {
		return m, nil
	}
	vp := viewport.New(m.paneWidth(), m.viewportHeight())
	m.detail = detailModel{
		name:       h.Name,
		bindingID:  h.ID,
		live:       false,
		round:      h.Rounds,
		rounds:     h.Rounds,
		archivedAt: h.ArchivedAt,
		active:     m.detail.active,
		vp:         vp,
		follow:     false,
	}
	m.fillViewport()
	if m.tabInFlight {
		return m, nil
	}
	if cmd := m.visibleTabFetch(); cmd != nil {
		m.tabInFlight = true
		return m, cmd
	}
	return m, nil
}

// pointAtRow dispatches rr to pointDetailAt or pointDetailAtHist, whichever
// of its two fields is set -- the one place selection (moveCursor, enter,
// a fresh statusMsg's cursor row) picks live vs hist so the two never
// drift apart.
func (m Model) pointAtRow(rr railRow) (Model, tea.Cmd) {
	switch {
	case rr.live != nil:
		return m.pointDetailAt(rr.live.Name)
	case rr.hist != nil:
		return m.pointDetailAtHist(*rr.hist)
	}
	return m, nil
}

// fillViewport sets the viewport to the active tab's body, wrapped to the
// viewport's width, keeping the current offset (the viewport clamps it).
// Every SetContent goes through here so a resize re-wraps.
func (m *Model) fillViewport() {
	y := m.detail.vp.YOffset
	if m.detail.name == "" {
		// Nothing is pointed at, so nothing is loading: an empty fleet's
		// pane stays blank rather than promising content.
		m.detail.vp.SetContent("")
		return
	}
	c := m.detail.cache[m.detail.active]
	m.detail.vp.SetContent(wrapBody(bodyOf(m.detail.active, c, m.detail.headless), m.detail.vp.Width))
	m.detail.vp.SetYOffset(y)
}

func (m Model) maybeInvalidate() (Model, tea.Cmd) {
	if !m.paneVisible() || m.detail.name == "" || !m.detail.live {
		// A hist row is never live: it is not in m.report to begin with,
		// and its data never changes underneath a human reading it, so
		// there is nothing here to invalidate against.
		return m, nil
	}
	r := row(m.report, m.detail.name)
	if r == nil {
		m.screen = screenList
		m.notice = fmt.Sprintf("%s is gone", m.detail.name)
		if m.layout() == layoutSplit {
			if rows := m.railRows(); len(rows) > 0 {
				return m.pointAtRow(rows[m.list.cursor])
			}
		}
		return m, nil
	}
	if r.Last == nil {
		return m, nil
	}
	if r.Last.TS.Equal(m.detail.lastLogTS) {
		return m, nil
	}

	m.detail.lastLogTS = r.Last.TS
	m.detail.round = r.Round - 1
	m.detail.rounds = r.Round
	for _, t := range []tab{tabPlan, tabReport, tabDiff, tabLog} {
		m.detail.cache[t] = tabContent{} // loaded=false
		m.detail.scroll[t] = 0           // reset parked offset on invalidation
	}
	if !m.tabInFlight {
		cmd := m.visibleTabFetch()
		if cmd != nil {
			m.tabInFlight = true
			return m, cmd
		}
	}
	return m, nil
}

// stepRound moves detail.round by delta, clamped to [1, detail.rounds] --
// "[" and "]" step a binding's rounds, live and archived alike (#183). At
// either edge it is a no-op with no notice. Every tab's cache is
// invalidated (a round-keyed fetch is meaningless against the old round's
// reply) and the active tab is re-fetched.
func (m Model) stepRound(delta int) (tea.Model, tea.Cmd) {
	next := m.detail.round + delta
	if next < 1 || next > m.detail.rounds {
		return m, nil
	}
	m.detail.round = next
	for t := tab(0); t < tabCount; t++ {
		m.detail.cache[t] = tabContent{} // loaded=false
		m.detail.scroll[t] = 0           // reset parked offset on invalidation
	}
	m.fillViewport()
	if m.tabInFlight {
		return m, nil
	}
	if cmd := m.visibleTabFetch(); cmd != nil {
		m.tabInFlight = true
		return m, cmd
	}
	return m, nil
}

// setRail stores a new rail width, clamped, and re-fits the pane to the
// width that leaves.
func (m Model) setRail(cols int) (tea.Model, tea.Cmd) {
	m.railCols = m.clampRail(cols)
	m.detail.vp.Width = m.paneWidth()
	m.fillViewport()
	m.list.top = m.railTop()
	return m, m.save()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		m.notice = ""
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
		return m.updateKeys(msg)

	case tea.MouseMsg:
		m.notice = ""
		return m.updateMouse(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.detail.vp.Width = m.paneWidth()
		m.detail.vp.Height = m.viewportHeight()
		m.fillViewport()
		m.list.top = m.railTop()
		if m.layout() == layoutSplit && m.statusLoaded {
			if rows := m.railRows(); len(rows) > 0 {
				return m.pointAtRow(rows[m.list.cursor])
			}
		}
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{tick(m.opts.Interval)}

		if !m.statusInFlight {
			m.statusInFlight = true
			cmds = append(cmds, fetchStatus(m.ctx, m.rt, m.scope, m.opts.Here))
		}

		if !m.tabInFlight {
			if c := m.visibleTabFetch(); c != nil {
				m.tabInFlight = true
				cmds = append(cmds, c)
			}
		}

		return m, tea.Batch(cmds...)

	case statusMsg:
		m.statusInFlight = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.statusLoaded = true
		m.err = nil
		m.report = msg.report
		m.statusAt = m.now()
		if m.scope == scopeAll {
			if msg.dbErr != nil {
				// db.ErrOpen or any other Bindings failure: the ui stays
				// on live, never exits (§6). The notice is sticky until
				// the next keypress, same as every other notice.
				m.scope = scopeLive
				m.dbRows = nil
				m.notice = fmt.Sprintf("no database: %v", msg.dbErr)
			} else {
				m.dbRows = msg.dbRows
			}
		}
		m.list.resolveStickyRows(m.railRows())
		m.list.top = m.railTop()
		if m.empty() && m.screen == screenDetail {
			m.screen = screenList
		}
		var cmds []tea.Cmd
		if m.layout() == layoutSplit {
			if rows := m.railRows(); len(rows) > 0 && m.detail.name != rows[m.list.cursor].name() {
				var cmd tea.Cmd
				m, cmd = m.pointAtRow(rows[m.list.cursor])
				cmds = append(cmds, cmd)
			}
		}
		var cmd tea.Cmd
		m, cmd = m.maybeInvalidate()
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)

	case tabMsg:
		m.tabInFlight = false
		if !m.paneVisible() {
			return m, nil
		}
		if msg.name != m.detail.name {
			return m, nil
		}
		// Every tab is round-keyed now (#183): plan, report, terminal and
		// log all read the specific round fetchFor was called with, the
		// same way diff always has. A reply for a round that is no longer
		// the one on screen -- a slow fetch outlived by two presses of "]"
		// -- is stale and must never land in the cache.
		if msg.round != m.detail.round {
			return m, nil
		}
		if msg.t != m.detail.active {
			m.detail.cache[msg.t] = msg.content
			return m, nil
		}
		m.detail.cache[msg.t] = msg.content
		m.fillViewport()
		if msg.t == tabTerminal && m.detail.follow {
			m.detail.vp.GotoBottom()
		}
		return m, nil

	case prefsSavedMsg:
		return m, nil
	}

	return m, nil
}

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	if m.layout() == layoutSplit {
		return m.splitView()
	}
	switch m.screen {
	case screenDetail:
		return m.detailView()
	default:
		return m.listView()
	}
}

// splitView is the one screen: header, error block, rail │ pane, footer.
func (m Model) splitView() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	if m.err != nil {
		b.WriteString(renderError(m.err, m.width))
		b.WriteByte('\n')
	}
	rail := strings.Split(m.railView(m.railWidth()), "\n")
	pane := strings.Split(m.paneView(m.paneWidth()), "\n")
	sepStyle := ruleStyle
	if m.screen == screenDetail {
		sepStyle = accentStyle
	}
	bar := sepStyle.Render("│") + " "
	for i := 0; i < m.bodyRows(); i++ {
		r, p := "", ""
		if i < len(rail) {
			r = rail[i]
		}
		if i < len(pane) {
			p = pane[i]
		}
		b.WriteString(fit(r, m.railWidth()) + bar + p)
		b.WriteByte('\n')
	}
	b.WriteString(m.footerView())
	return b.String()
}

// headerView is the reversed bar and the blank under it (headerRows).
func (m Model) headerView() string {
	left := lipgloss.NewStyle().Bold(true).Render(" relay ")
	switch {
	case !m.statusLoaded:
		left += "  "
	case len(m.report.Bindings) == 0:
		left += "  no bindings"
	default:
		counts := map[string]int{}
		for _, b := range m.report.Bindings {
			counts[b.Display]++
		}
		left += fmt.Sprintf("  %d bindings", len(m.report.Bindings))
		if n := counts["NEEDS YOU"]; n > 0 {
			left += " · " + stateNeedsYouStyle.Render(fmt.Sprintf("%d needs you", n))
		}
		if n := counts["HELD"]; n > 0 {
			left += fmt.Sprintf(" · %d held", n)
		}
	}
	var right []string
	for _, g := range m.report.Gated {
		right = append(right, stateNeedsYouStyle.Render(fmt.Sprintf("%s gated %s", g.Token, relay.GateUntilText(g.Until))))
	}
	right = append(right, dimStyle.Render(m.now().Local().Format("15:04")+" "))
	bar := headerBar.Render(fit(spread(left, strings.Join(right, "  ·  "), m.width), m.width))
	return bar + "\n"
}

// footerView: keys for the current focus on the left, notices and the
// refresh age on the right. The right side wins when they would overlap:
// a notice is the part a human must not miss.
func (m Model) footerView() string {
	key := func(k, v string) string { return fgStyle.Render(k) + " " + dimStyle.Render(v) }
	order := "attention"
	if !m.sort {
		order = "name"
	}
	compactLabel := "compact"
	if m.compact {
		compactLabel = "cards"
	}
	compactKey := key("c", compactLabel)
	var keys []string
	switch {
	case m.empty():
		keys = []string{key("s", "sort: "+order), compactKey, key("q", "quit")}
	case m.layout() == layoutSplit && m.screen == screenList:
		keys = []string{key("↑↓", "move"), key("⏎", "focus pane"), key("tab", "next pane"), key("1-5", "pane"), key("s", "sort: "+order), compactKey, key("q", "quit")}
	case m.layout() == layoutSplit:
		keys = []string{key("↑↓", "scroll"), key("esc", "back to rail"), key("tab", "next pane"), key("1-5", "pane"), key("s", "sort: "+order), compactKey, key("q", "quit")}
	case m.screen == screenList:
		keys = []string{key("↑↓", "move"), key("⏎", "open"), key("s", "sort: "+order), compactKey, key("q", "quit")}
	default:
		keys = []string{key("esc", "back"), key("tab", "next pane"), key("1-5", "pane"), key("q", "quit")}
	}
	left := strings.Join(keys, "   ")

	var notes []string
	if m.notice != "" {
		notes = append(notes, stateNeedsYouStyle.Render(m.notice))
	}
	if m.err != nil {
		notes = append(notes, errorStyle.Render("! refresh failed (retrying)"))
	}
	if m.report.HerdrError != "" {
		notes = append(notes, errorStyle.Render("! herdr unreachable"))
	}
	if m.paneVisible() {
		for _, b := range m.report.Bindings {
			if b.Name != m.detail.name && b.Display == "NEEDS YOU" {
				notes = append(notes, stateNeedsYouStyle.Render(b.Name+" NEEDS YOU"))
			}
		}
	}
	if !m.statusAt.IsZero() {
		notes = append(notes, faintStyle.Render("refreshed "+ago(m.statusAt, m.now())+" ago"))
	}
	right := strings.Join(notes, "   ")
	if lipgloss.Width(left)+1+lipgloss.Width(right) > m.width {
		left = lipgloss.NewStyle().MaxWidth(m.width - lipgloss.Width(right) - 1).Render(left)
	}
	return fit(spread(left, right, m.width), m.width)
}
