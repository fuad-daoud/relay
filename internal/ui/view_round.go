package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// envNow is the clock a view hands the roundPane: the shell reads time once
// per call (Env.Now).
func envNow(env Env) func() time.Time { return func() time.Time { return env.Now } }

// newRoundPane builds a pane lent everything Env carries (§4.4, R2.4).
func newRoundPane(env Env) roundPane {
	return roundPane{
		src:    env.Src,
		ctx:    env.Ctx,
		now:    envNow(env),
		report: env.Report,
		width:  env.Width,
		rows:   bodyHeight(env),
	}
}

// newRoundView targets a live binding's round. round == 0 means the
// default: today's pointDetailAt rule (Round-1). A non-zero round is set
// after the point, with the caches cleared like stepRound does, and its
// fetch is issued (§4.5, R2.4).
func newRoundView(env Env, key string, round int) (View, tea.Cmd) {
	p := newRoundPane(env)
	var cmd tea.Cmd
	p, cmd = p.pointDetailAt(key)
	if round > 0 {
		p.detail.round = round
		for t := tab(0); t < tabCount; t++ {
			p.detail.cache[t] = tabContent{}
			p.detail.scroll[t] = 0
		}
		p.fillViewport()
		if !p.tabInFlight {
			if c := p.visibleTabFetch(); c != nil {
				p.tabInFlight = true
				cmd = tea.Batch(cmd, c)
			}
		}
	}
	return roundView{pane: p}, cmd
}

// newHistRoundView targets an archived binding's round through
// pointDetailAtHist. round == 0 means the default (Rounds) (§4.5, R2.4).
func newHistRoundView(env Env, h relevo.HistoryBinding, round int) (View, tea.Cmd) {
	p := newRoundPane(env)
	var cmd tea.Cmd
	p, cmd = p.pointDetailAtHist(h)
	if round > 0 {
		p.detail.round = round
		for t := tab(0); t < tabCount; t++ {
			p.detail.cache[t] = tabContent{}
			p.detail.scroll[t] = 0
		}
		p.fillViewport()
		if !p.tabInFlight {
			if c := p.visibleTabFetch(); c != nil {
				p.tabInFlight = true
				cmd = tea.Batch(cmd, c)
			}
		}
	}
	return roundView{pane: p}, cmd
}

// row finds a report row by key (BindingStatus.Key()): a planner row keys
// by Name, a server row by owner/name, so two clients' same-named bindings
// never collide. Moved from model.go (R2.8).
func row(rep relevo.Report, key string) *relevo.BindingStatus {
	for i := range rep.Bindings {
		if rep.Bindings[i].Key() == key {
			return &rep.Bindings[i]
		}
	}
	return nil
}

// roundView is the full-screen round detail: today's pane, hosted as a
// view (§4.5).
type roundView struct {
	pane roundPane
}

func (r roundView) Crumbs() []string {
	return []string{r.pane.detail.name, fmt.Sprintf("r%d", r.pane.detail.round)}
}

// Context is the pane's identity line on the left, nothing on the right.
func (r roundView) Context(env Env) (string, string) {
	return r.pane.detailHeader(), ""
}

func (r roundView) Keys() []KeyHelp {
	return []KeyHelp{
		{"tab", "next tab"},
		{"1-5", "tab"},
		{"[ ]", "round"},
		{"↑↓", "scroll"},
	}
}

func (r roundView) Capturing() bool { return false }

func (r roundView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	// Every call lends the pane the shell's current report and box (§4.4):
	// invalidate, the ages and the fetch all read these.
	r.pane.report = env.Report
	r.pane.now = envNow(env)
	r.pane.width = env.Width
	r.pane.rows = bodyHeight(env)
	switch msg := msg.(type) {
	case tickMsg:
		if !r.pane.tabInFlight {
			if cmd := r.pane.visibleTabFetch(); cmd != nil {
				r.pane.tabInFlight = true
				return r, cmd
			}
		}
		return r, nil

	case statusMsg:
		var cmd tea.Cmd
		var gone bool
		r.pane, cmd, gone = r.pane.invalidate()
		if gone {
			return r, tea.Batch(pop(), notice(r.pane.detail.name+" is gone"))
		}
		return r, cmd

	case tabMsg:
		// A reply always ends the fetch it answers, stale or not (today's
		// tabMsg arm); onTab then drops a mismatched one.
		r.pane.tabInFlight = false
		r.pane = r.pane.onTab(msg)
		return r, nil

	case tea.WindowSizeMsg:
		r.pane.width = env.Width
		r.pane.rows = bodyHeight(env)
		r.pane.detail.vp.Width = env.Width
		r.pane.detail.vp.Height = r.pane.viewportHeight()
		r.pane.fillViewport()
		return r, nil

	case tea.KeyMsg:
		return r.updateKey(msg)
	}
	return r, nil
}

func (r roundView) updateKey(msg tea.KeyMsg) (View, tea.Cmd) {
	switch msg.String() {
	case "tab", "shift+tab", "back_tab":
		var cmd tea.Cmd
		r.pane, cmd = r.pane.cycleTab(msg)
		return r, cmd
	case "1", "2", "3", "4", "5":
		t := tab(msg.String()[0] - '1')
		if t != r.pane.detail.active {
			var cmd tea.Cmd
			r.pane, cmd = r.pane.switchTab(t)
			return r, cmd
		}
		return r, nil
	case "[", "]":
		delta := 1
		if msg.String() == "[" {
			delta = -1
		}
		var cmd tea.Cmd
		r.pane, cmd = r.pane.stepRound(delta)
		return r, cmd
	}
	var cmd tea.Cmd
	r.pane.detail.vp, cmd = r.pane.detail.vp.Update(msg)
	if r.pane.detail.active == tabTerminal {
		r.pane.detail.follow = r.pane.detail.vp.AtBottom()
	}
	return r, cmd
}

// Body is the pane, given the shell's full body box (§5.4).
func (r roundView) Body(env Env, width, height int) string {
	r.pane.width = width
	r.pane.rows = height
	r.pane.report = env.Report
	r.pane.now = envNow(env)
	return r.pane.view(width)
}
