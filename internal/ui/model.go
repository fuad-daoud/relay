package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

type Model struct {
	rt   relay.Runtime
	ctx  context.Context
	opts Options

	screen screen

	report relay.Report // newest GOOD snapshot; survives a failed refresh
	err    error        // last refresh error, shown in the footer

	// Two guards, not one. statusInFlight and tabInFlight are separate
	// because a terminal read is a 30s-timeout herdr call: a single shared
	// guard would let one slow ReadAgent stall every list refresh behind it,
	// freezing the fleet view for half a minute. Each is cleared by its own
	// message.
	statusInFlight bool
	tabInFlight    bool

	list   listModel
	detail detailModel

	width, height int
	ready         bool // set on the first WindowSizeMsg
}

func newModel(ctx context.Context, rt relay.Runtime, opts Options) Model {
	return Model{
		rt:     rt,
		ctx:    ctx,
		opts:   opts,
		screen: screenList,
	}
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

func (m Model) visibleTabFetch() tea.Cmd {
	if m.screen != screenDetail {
		return nil
	}
	t := m.detail.active
	if t == tabTerminal {
		return fetchFor(m.ctx, m.rt, tabTerminal, m.detail.name, m.detail.round, m.detail.vp.Height)
	}
	if !m.detail.cache[t].loaded {
		return fetchFor(m.ctx, m.rt, t, m.detail.name, m.detail.round, m.detail.vp.Height)
	}
	return nil
}

func (m Model) maybeInvalidate() (Model, tea.Cmd) {
	if m.screen != screenDetail {
		return m, nil
	}
	r := row(m.report, m.detail.name)
	if r == nil {
		m.screen = screenList
		m.err = fmt.Errorf("%s is gone", m.detail.name)
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
	}
	cmd := m.visibleTabFetch()
	if cmd != nil {
		m.tabInFlight = true
	}
	return m, cmd
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
		return m.updateKeys(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.detail.vp.Width = msg.Width
		vpHeight := msg.Height - chromeHeight
		if vpHeight < 0 {
			vpHeight = 0
		}
		m.detail.vp.Height = vpHeight
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
		m.err = nil
		m.report = msg.report
		m.list.resolveSticky(m.report)
		var cmd tea.Cmd
		m, cmd = m.maybeInvalidate()
		return m, cmd

	case tabMsg:
		m.tabInFlight = false
		if m.screen != screenDetail {
			return m, nil
		}
		if msg.name != m.detail.name {
			return m, nil
		}
		if msg.t != m.detail.active {
			m.detail.cache[msg.t] = msg.content
			return m, nil
		}
		m.detail.cache[msg.t] = msg.content
		m.detail.vp.SetContent(bodyOf(msg.content))
		m.detail.vp.YOffset = m.detail.scroll[msg.t]
		return m, nil
	}

	return m, nil
}

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	switch m.screen {
	case screenList:
		return m.listView()
	case screenDetail:
		return m.detailView()
	default:
		return ""
	}
}

func (m Model) footer() string {
	var keys string
	switch m.screen {
	case screenList:
		keys = "enter open · q quit"
	case screenDetail:
		keys = "esc back · tab next pane · q quit"
	}
	if m.err != nil {
		keys += "  ! refresh failed: " + m.err.Error() + " (retrying)"
	}
	if m.screen == screenDetail {
		for _, b := range m.report.Bindings {
			if b.Name != m.detail.name && b.Display == "NEEDS YOU" {
				keys += "  ! " + b.Name + " NEEDS YOU"
			}
		}
	}
	return keys
}
