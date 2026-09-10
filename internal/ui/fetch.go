package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

type screen int

const (
	screenList screen = iota
	screenDetail
)

type tab int

const (
	tabReport tab = iota
	tabTerminal
	tabDiff
	tabLog
	tabCount
)

// tabTitles indexes by tab and is used by both the tab bar and the tests.
var tabTitles = [tabCount]string{"report", "terminal", "diff", "log"}

// tabContent is one tab's rendered body plus why it might be empty.
//
// err and empty are distinct and must never be collapsed into one field. err
// means the read failed. empty means the read succeeded and there is
// legitimately nothing to show -- a state that must render as prose, never as
// an error. See §6.
type tabContent struct {
	body   string // rendered content, ready for the viewport
	loaded bool   // false until the first fetch returns
	err    error  // fetch failure, scoped to this tab alone
	empty  string // prose explaining expected emptiness
}

type tickMsg time.Time

type statusMsg struct {
	report relay.Report
	err    error
}

// tabMsg carries name and round so a late reply that no longer matches the
// current selection is discarded rather than shown.
type tabMsg struct {
	name    string
	round   int
	t       tab
	content tabContent
}

// fetchStatus calls relay.Status(ctx, rt) and returns statusMsg{report, err}.
// It never returns a partial report alongside an error.
func fetchStatus(ctx context.Context, rt relay.Runtime) tea.Cmd {
	return func() tea.Msg {
		rep, err := relay.Status(ctx, rt)
		if err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{report: rep}
	}
}

// fetchReport walks the binding log backwards for the newest planner-bound
// report or question entry and returns its Payload.
func fetchReport(ctx context.Context, rt relay.Runtime, name string) tea.Cmd {
	return func() tea.Msg {
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return tabMsg{
				name: name,
				t:    tabReport,
				content: tabContent{
					loaded: true,
					err:    err,
				},
			}
		}

		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.Direction == store.DirToPlanner &&
				(e.Kind == store.KindReport || e.Kind == store.KindQuestion || e.Kind == store.KindFindings) {
				if e.Payload == "" {
					return tabMsg{
						name:  name,
						round: e.Round,
						t:     tabReport,
						content: tabContent{
							loaded: true,
							empty:  fmt.Sprintf("round %d report has no payload", e.Round),
						},
					}
				}
				return tabMsg{
					name:  name,
					round: e.Round,
					t:     tabReport,
					content: tabContent{
						loaded: true,
						body:   e.Payload,
					},
				}
			}
		}

		return tabMsg{
			name: name,
			t:    tabReport,
			content: tabContent{
				loaded: true,
				empty:  "round 1 in flight; no report yet",
			},
		}
	}
}

// fetchTerminal resolves the builder agent and reads its recent output.
func fetchTerminal(ctx context.Context, rt relay.Runtime, name string, lines int) tea.Cmd {
	if lines < 1 {
		lines = 1
	}
	return func() tea.Msg {
		b, err := rt.Store.Load(name)
		if err != nil {
			return tabMsg{
				name: name,
				t:    tabTerminal,
				content: tabContent{
					loaded: true,
					err:    err,
				},
			}
		}

		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return tabMsg{
				name: name,
				t:    tabTerminal,
				content: tabContent{
					loaded: true,
					err:    err,
				},
			}
		}

		agent, ok := relay.FindAgent(agents, b.Builder)
		if !ok {
			return tabMsg{
				name: name,
				t:    tabTerminal,
				content: tabContent{
					loaded: true,
					empty:  fmt.Sprintf("builder gone (`%s`); pane %s no longer exists", b.BuilderAlias, b.Builder.PaneID),
				},
			}
		}

		// Address the agent FindAgent just located rather than replaying the
		// recorded AgentName. herdr can forget a spawned agent's name across a
		// server restart while its pane stays perfectly addressable, and the
		// name then resolves to nothing -- which showed up as the terminal tab
		// rendering `agent target relay-ui-builder not found` against a live
		// builder. The located agent's pane id is current by construction.
		// See relay#20 for the same hazard on the Send and Answer paths.
		out, err := rt.Herdr.ReadAgent(ctx, agent.PaneID, lines)
		if err != nil {
			return tabMsg{
				name: name,
				t:    tabTerminal,
				content: tabContent{
					loaded: true,
					err:    err,
				},
			}
		}

		return tabMsg{
			name: name,
			t:    tabTerminal,
			content: tabContent{
				loaded: true,
				body:   out,
			},
		}
	}
}

// fetchDiff retrieves the stored patch for the specified round.
func fetchDiff(ctx context.Context, rt relay.Runtime, name string, round int) tea.Cmd {
	if round < 1 {
		return func() tea.Msg {
			return tabMsg{
				name:  name,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					empty:  "no completed round yet",
				},
			}
		}
	}

	return func() tea.Msg {
		patch, ok, err := relay.ReadDiff(rt, name, round)
		if err != nil {
			return tabMsg{
				name:  name,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					err:    err,
				},
			}
		}
		if !ok {
			return tabMsg{
				name:  name,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					empty:  fmt.Sprintf("no diff recorded for round %d — no baseline captured", round),
				},
			}
		}

		return tabMsg{
			name:  name,
			round: round,
			t:     tabDiff,
			content: tabContent{
				loaded: true,
				body:   string(patch),
			},
		}
	}
}

// fetchLog formats each log entry with the cmdLog layout.
func fetchLog(ctx context.Context, rt relay.Runtime, name string) tea.Cmd {
	return func() tea.Msg {
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return tabMsg{
				name: name,
				t:    tabLog,
				content: tabContent{
					loaded: true,
					err:    err,
				},
			}
		}

		if len(entries) == 0 {
			return tabMsg{
				name: name,
				t:    tabLog,
				content: tabContent{
					loaded: true,
					empty:  "no entries yet",
				},
			}
		}

		var b strings.Builder
		for _, e := range entries {
			fmt.Fprintf(&b, "%s  round %-3d %-10s %-9s %s %s\n",
				e.TS.Local().Format("2006-01-02 15:04:05"),
				e.Round, e.Direction, e.Kind, e.Path, e.Note)
		}

		return tabMsg{
			name: name,
			t:    tabLog,
			content: tabContent{
				loaded: true,
				body:   b.String(),
			},
		}
	}
}

// fetchFor dispatches to the right command for a tab, so switchTab and
// visibleTabFetch share one mapping instead of two switches that can drift.
//
// It takes explicit parameters rather than a Model: fetch.go is built in
// Step 2 and Model does not exist until Step 3, so a Model parameter here
// would not compile in the step that introduces it.
func fetchFor(ctx context.Context, rt relay.Runtime, t tab, name string, round, lines int) tea.Cmd {
	switch t {
	case tabReport:
		return fetchReport(ctx, rt, name)
	case tabTerminal:
		return fetchTerminal(ctx, rt, name, lines)
	case tabDiff:
		return fetchDiff(ctx, rt, name, round)
	case tabLog:
		return fetchLog(ctx, rt, name)
	default:
		return nil
	}
}
