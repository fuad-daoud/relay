package ui

import (
	"context"
	"fmt"
	"os"
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
	body   string    // rendered content, ready for the viewport
	loaded bool      // false until the first fetch returns
	err    error     // fetch failure, scoped to this tab alone
	empty  string    // prose explaining expected emptiness
	round  int       // the round the body belongs to (report, diff); 0 when not round-keyed
	at     time.Time // when the body was read; the source line's "13:38" and "captured 1s ago"
}

// headlessLogLines caps how much of a round log the terminal tab holds:
// the whole log for any round a human would read, a bounded body for a
// runaway one. The pane-builder branch still reads viewport-height lines
// -- that one is a screen, this one is a file.
const headlessLogLines = 5000

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
					at:     time.Now(),
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
							at:     time.Now(),
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
						at:     time.Now(),
						round:  e.Round,
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
				at:     time.Now(),
				empty:  "round 1 in flight; no report yet",
			},
		}
	}
}

// fetchTerminal resolves the builder agent and reads its recent output, or --
// for a headless builder -- the tail of its round log.
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
					at:     time.Now(),
					err:    err,
				},
			}
		}

		// A headless builder (#99) has no pane; its output is the round's
		// log file. Shown, never parsed.
		if b.Builder.Headless() {
			// Between rounds clearProcess blanks LogPath, but the cursor
			// still names the last round that ran (transcript spec §4.7):
			// keep showing that log rather than a blank tab.
			logPath := b.Builder.LogPath
			if logPath == "" && b.Builder.StreamRound != 0 {
				logPath = rt.Store.BuilderLogPath(name, b.Builder.StreamRound)
			}
			if logPath == "" {
				return tabMsg{
					name: name,
					t:    tabTerminal,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						empty:  "headless builder; no round has run yet, so there is no log",
					},
				}
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				return tabMsg{
					name: name,
					t:    tabTerminal,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						empty:  "log not written yet: " + logPath,
					},
				}
			}
			body := strings.TrimRight(string(data), "\n")
			if all := strings.Split(body, "\n"); len(all) > headlessLogLines {
				body = strings.Join(all[len(all)-headlessLogLines:], "\n")
			}
			return tabMsg{
				name: name,
				t:    tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					body:   body,
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
					at:     time.Now(),
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
					at:     time.Now(),
					empty:  fmt.Sprintf("builder gone (`%s`); pane %s no longer exists", b.BuilderCandidate, b.Builder.PaneID),
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
					at:     time.Now(),
					err:    err,
				},
			}
		}

		return tabMsg{
			name: name,
			t:    tabTerminal,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
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
					at:     time.Now(),
					round:  round,
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
					at:     time.Now(),
					round:  round,
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
					at:     time.Now(),
					round:  round,
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
				at:     time.Now(),
				round:  round,
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
					at:     time.Now(),
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
					at:     time.Now(),
					empty:  "no entries yet",
				},
			}
		}

		var b strings.Builder
		for _, e := range entries {
			b.WriteString(relay.LogLine(e))
			b.WriteByte('\n')
		}

		return tabMsg{
			name: name,
			t:    tabLog,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
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
