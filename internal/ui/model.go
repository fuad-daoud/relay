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
	// now is the clock every age on screen is measured against. time.Now
	// in production; fixed in tests so "2m ago" is deterministic.
	now func() time.Time
	// statusAt is when the newest good statusMsg arrived; the footer's
	// "refreshed Ns ago".
	statusAt time.Time
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

// tick re-arms the poll. It is the ONLY timer; there is no goroutine.
func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchStatus(m.ctx, m.rt),
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
	if t == tabTerminal {
		return fetchFor(m.ctx, m.rt, tabTerminal, m.detail.name, m.detail.round, lines)
	}
	if !m.detail.cache[t].loaded {
		return fetchFor(m.ctx, m.rt, t, m.detail.name, m.detail.round, lines)
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
	if !m.paneVisible() || m.detail.name == "" {
		return m, nil
	}
	r := row(m.report, m.detail.name)
	if r == nil {
		m.screen = screenList
		m.notice = fmt.Sprintf("%s is gone", m.detail.name)
		if m.layout() == layoutSplit && len(m.rows()) > 0 {
			return m.pointDetailAt(m.rows()[m.list.cursor].Name)
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
	for _, t := range []tab{tabReport, tabDiff, tabLog} {
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

// setRail stores a new rail width, clamped, and re-fits the pane to the
// width that leaves. Task 3 adds the prefs save here.
func (m Model) setRail(cols int) (tea.Model, tea.Cmd) {
	m.railCols = m.clampRail(cols)
	m.detail.vp.Width = m.paneWidth()
	m.fillViewport()
	m.list.top = m.railTop()
	return m, nil
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
		if m.layout() == layoutSplit && m.statusLoaded && len(m.rows()) > 0 {
			return m.pointDetailAt(m.rows()[m.list.cursor].Name)
		}
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{tick(m.opts.Interval)}

		if !m.statusInFlight {
			m.statusInFlight = true
			cmds = append(cmds, fetchStatus(m.ctx, m.rt))
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
		m.list.resolveSticky(relay.Report{Bindings: m.rows()})
		m.list.top = m.railTop()
		var cmds []tea.Cmd
		if m.layout() == layoutSplit && len(m.rows()) > 0 && m.detail.name != m.rows()[m.list.cursor].Name {
			var cmd tea.Cmd
			m, cmd = m.pointDetailAt(m.rows()[m.list.cursor].Name)
			cmds = append(cmds, cmd)
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
		// Only the diff tab is round-keyed. The report tab reports the round of the
		// entry it read, which may legitimately lag; the terminal shows the builder's
		// screen right now; the log shows every round at once. Testing round equality
		// against those three discards every reply they will ever send.
		if msg.t == tabDiff && msg.round != m.detail.round {
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
	var keys []string
	switch {
	case m.layout() == layoutSplit && m.screen == screenList:
		keys = []string{key("↑↓", "move"), key("⏎", "focus pane"), key("tab", "next pane"), key("1-4", "pane"), key("s", "sort: "+order), key("q", "quit")}
	case m.layout() == layoutSplit:
		keys = []string{key("↑↓", "scroll"), key("esc", "back to rail"), key("tab", "next pane"), key("1-4", "pane"), key("s", "sort: "+order), key("q", "quit")}
	case m.screen == screenList:
		keys = []string{key("↑↓", "move"), key("⏎", "open"), key("s", "sort: "+order), key("q", "quit")}
	default:
		keys = []string{key("esc", "back"), key("tab", "next pane"), key("1-4", "pane"), key("q", "quit")}
	}
	left := strings.Join(keys, "   ")

	var notes []string
	if m.notice != "" {
		notes = append(notes, stateNeedsYouStyle.Render(m.notice))
	}
	if m.err != nil {
		notes = append(notes, errorStyle.Render("! refresh failed (retrying)"))
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
