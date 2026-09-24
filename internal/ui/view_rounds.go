package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
)

// roundOpenMsg asks the rounds view to push a round detail. Exactly one of
// key (a live row's Key()) and hist (an archived row) is set.
type roundOpenMsg struct {
	key   string
	hist  *relevo.HistoryBinding
	round int
}

// dashStyles is the dashboard's slice of the ui's palette, so the screen
// never duplicates a colour (§5). Moved here from model.go:549 (R2.5).
func dashStyles() dash.Styles {
	return dash.Styles{
		Fg:        fgStyle,
		Dim:       dimStyle,
		Faint:     faintStyle,
		Error:     errorStyle,
		Empty:     emptyStyle,
		Selected:  selectedBg,
		Archived:  archivedStyle,
		Attention: stateNeedsYouStyle,
		Live:      stateActiveStyle,
	}
}

// newRoundsView hosts the dashboard as a view (§4.5). It refuses without a
// database, exactly as enterDash does today (model.go:551-570).
func newRoundsView(env Env, query, sortKey string) (View, tea.Cmd, error) {
	if env.Src.Base().DB == nil {
		return nil, nil, relevo.ErrNoDatabase
	}
	d := dash.New(env.Src.Base().DB, time.Local, envNow(env), query, sortKey)
	d.Embedded = true
	d.Names = env.Src.Base().Candidates.NameOf
	d.SetStyles(dashStyles())
	d.SetSize(env.Width, bodyHeight(env))
	return roundsView{dash: d}, d.Init(), nil
}

// roundsView is ':rounds': today's dashboard grid, hosted (§4.5).
type roundsView struct {
	dash dash.Model
}

func (r roundsView) Crumbs() []string { return []string{"rounds"} }

func (r roundsView) Capturing() bool { return r.dash.Editing() }

// Context names the applied query, or "all rounds", and the regroup axis
// when there is one (§5.4).
func (r roundsView) Context(env Env) (string, string) {
	left := "query: " + r.dash.QueryText()
	if r.dash.QueryText() == "" {
		left = "all rounds"
	}
	right := ""
	if axis := r.dash.GroupAxis(); axis != "" {
		right = "by " + axis
	}
	return left, right
}

func (r roundsView) Keys() []KeyHelp {
	return []KeyHelp{
		{"/", "filter"},
		{"b", "regroup"},
		{"s", "sort"},
		{"S", "flip"},
		{"enter", "open"},
		{"r", "refresh"},
	}
}

func (r roundsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		if r.dash.ShouldRefresh(env.Now) {
			return r, r.dash.Refresh()
		}
		return r, nil

	case tea.WindowSizeMsg:
		r.dash.SetSize(env.Width, bodyHeight(env))
		return r, nil

	case dash.JumpMsg:
		return r, openRound(env, msg)

	case roundOpenMsg:
		if msg.hist != nil {
			v, cmd := newHistRoundView(env, *msg.hist, msg.round)
			return r, push(v, cmd)
		}
		v, cmd := newRoundView(env, msg.key, msg.round)
		return r, push(v, cmd)
	}

	q0, s0 := r.dash.QueryText(), r.dash.SortKey()
	next, cmd := r.dash.Update(msg)
	r.dash = next
	q1, s1 := r.dash.QueryText(), r.dash.SortKey()
	var cmds []tea.Cmd
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if q1 != q0 {
		cmds = append(cmds, func() tea.Msg { return prefMsg{"dashboard", q1} })
	}
	if s1 != s0 {
		cmds = append(cmds, func() tea.Msg { return prefMsg{"dashboard_sort", s1} })
	}
	return r, tea.Batch(cmds...)
}

// Body is the dashboard grid, sized to the shell's body box.
func (r roundsView) Body(env Env, width, height int) string {
	r.dash.SetSize(width, height)
	return r.dash.View()
}

// openRound resolves a dashboard jump to the round it names: a live row
// first, then the database, then a notice (§5.4). Asynchronous, because the
// database read can block.
func openRound(env Env, msg dash.JumpMsg) tea.Cmd {
	return func() tea.Msg {
		for i := range env.Report.Bindings {
			if env.Report.Bindings[i].Name == msg.BindingName {
				return roundOpenMsg{key: env.Report.Bindings[i].Key(), round: msg.Round}
			}
		}
		rows, err := relevo.Bindings(env.Ctx, env.Src.Base(), "")
		if err == nil {
			for i := range rows {
				if msg.BindingID != "" && rows[i].ID == msg.BindingID {
					h := rows[i]
					return roundOpenMsg{hist: &h, round: msg.Round}
				}
			}
			for i := range rows {
				if rows[i].Name == msg.BindingName {
					h := rows[i]
					return roundOpenMsg{hist: &h, round: msg.Round}
				}
			}
		}
		return noticeMsg{text: fmt.Sprintf("%s not found", msg.BindingName)}
	}
}

// execLine runs one command line against env, as the ':' command line does
// and the shell does for Options.Start (§6).
func execLine(line string, env Env, p prefs) tea.Cmd {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	name := fields[0]
	args := fields[1:]
	switch name {
	case "fleet":
		return root(newFleetView(p.Sort != "name"))
	case "rounds":
		q := strings.Join(args, " ")
		if q == "" {
			q = p.Dashboard
		}
		v, init, err := newRoundsView(env, q, p.DashboardSort)
		if err != nil {
			return notice("no database: " + err.Error())
		}
		return tea.Batch(root(v), init)
	case "round":
		if len(args) == 0 {
			return notice(`unknown binding ""`)
		}
		key := args[0]
		n := 0
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &n)
		}
		live := false
		for i := range env.Report.Bindings {
			if env.Report.Bindings[i].Key() == key {
				live = true
				break
			}
		}
		if !live {
			return notice(fmt.Sprintf("unknown binding %q", key))
		}
		v, cmd := newRoundView(env, key, n)
		return tea.Batch(root(newFleetView(p.Sort != "name"), v), cmd)
	case "help":
		return func() tea.Msg { return helpMsg{} }
	case "quit":
		return tea.Quit
	}
	return notice(fmt.Sprintf("unknown command %q (try :help)", ":"+name))
}
