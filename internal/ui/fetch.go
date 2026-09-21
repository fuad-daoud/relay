package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	screenDash
)

type tab int

const (
	tabPlan tab = iota
	tabReport
	tabTerminal
	tabDiff
	tabLog
	tabCount
)

// tabTitles indexes by tab and is used by both the tab bar and the tests.
var tabTitles = [tabCount]string{"plan", "report", "terminal", "diff", "log"}

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

	// transcript is true when the body is a rendered round log (headless
	// stream or pane session record, #184): colour markers, show the log
	// source line instead of the capture one.
	transcript bool
	// logName is the base name of that log ("003-builder.log"); "" for a
	// capture.
	logName string
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
	// dbRows and dbErr are scope all's addition: dbRows is nil (no error)
	// in scope live, dbErr is the database's failure to answer when
	// scope is all (§6 ErrNoDatabase or any other Bindings error).
	dbRows []relay.HistoryBinding
	dbErr  error
}

// tabMsg carries a row key (BindingStatus.Key()) and round so a late reply
// that no longer matches the current selection is discarded rather than
// shown.
type tabMsg struct {
	name    string
	round   int
	t       tab
	content tabContent
}

// unresolvedKey is the error a fetcher reports when the source cannot
// resolve a row key: the same prose a gone binding's own store read
// produces, so an unknown owner's tab reads exactly like a vanished one.
func unresolvedKey(key string) error {
	return fmt.Errorf("%s: %w", key, store.ErrNotFound)
}

// fetchStatus calls src.Status(ctx) and returns statusMsg{report, err}. In
// scope all it also calls relay.Bindings(ctx, src.Base(), here), carrying
// its rows or its error alongside the (always live) report -- a database
// failure never blocks the live report from refreshing. It never returns a
// partial report alongside a report-level error.
func fetchStatus(ctx context.Context, src Source, sc scope, here string) tea.Cmd {
	return func() tea.Msg {
		rep, err := src.Status(ctx)
		if err != nil {
			return statusMsg{err: err}
		}
		msg := statusMsg{report: rep}
		if sc == scopeAll {
			rows, berr := relay.Bindings(ctx, src.Base(), here)
			if berr != nil {
				msg.dbErr = berr
			} else {
				msg.dbRows = rows
			}
		}
		return msg
	}
}

// fetchPlan reads round's plan file. It is small enough not to need
// relay.ReadDiff's stored-patch indirection: the file is either there or it
// is not.
func fetchPlan(ctx context.Context, src Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabPlan,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					err:    unresolvedKey(key),
				},
			}
		}
		if round < 1 {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabPlan,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					empty:  "no completed round yet",
				},
			}
		}
		data, err := os.ReadFile(rt.Store.PlanPath(name, round))
		if err != nil {
			if os.IsNotExist(err) {
				return tabMsg{
					name:  key,
					round: round,
					t:     tabPlan,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						round:  round,
						empty:  fmt.Sprintf("no plan for round %d", round),
					},
				}
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabPlan,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					err:    err,
				},
			}
		}
		return tabMsg{
			name:  key,
			round: round,
			t:     tabPlan,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
				round:  round,
				body:   string(data),
			},
		}
	}
}

// fetchReport reads round's own report (or question/findings) entry from
// the binding log -- round-scoped, not "the newest one logged": stepping to
// an earlier round must show that round's report, not a later one's. Found
// nothing for round: an empty log at round 1 keeps the familiar "round 1 in
// flight" prose; anything else reads as round being the open round, not yet
// closed (#183).
func fetchReport(ctx context.Context, src Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabReport,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    unresolvedKey(key),
				},
			}
		}
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabReport,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    err,
				},
			}
		}

		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.Round != round {
				continue
			}
			if e.Direction == store.DirToPlanner &&
				(e.Kind == store.KindReport || e.Kind == store.KindQuestion || e.Kind == store.KindFindings) {
				if e.Payload == "" {
					return tabMsg{
						name:  key,
						round: round,
						t:     tabReport,
						content: tabContent{
							loaded: true,
							at:     time.Now(),
							round:  round,
							empty:  fmt.Sprintf("round %d report has no payload", round),
						},
					}
				}
				return tabMsg{
					name:  key,
					round: round,
					t:     tabReport,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						round:  round,
						body:   e.Payload,
					},
				}
			}
		}

		if len(entries) == 0 && round == 1 {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabReport,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					empty:  "round 1 in flight; no report yet",
				},
			}
		}

		return tabMsg{
			name:  key,
			round: round,
			t:     tabReport,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
				round:  round,
				empty:  fmt.Sprintf("round %d is open; report arrives when it closes", round),
			},
		}
	}
}

// logTab reads a round log for the terminal tab: the last headlessLogLines
// lines, transcript true, logName the file's base name. ok is false when
// the file cannot be read (missing or otherwise), so the caller decides
// what the tab says instead: the pane branch falls back to the capture, the
// headless branch keeps its own "log not written yet" prose. (Extracted
// from the headless branch of fetchTerminal; that branch now calls it.)
// key names the reply's row and name the store path the log was read from.
func logTab(key, name, logPath string) (tabMsg, bool) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return tabMsg{}, false
	}
	body := strings.TrimRight(string(data), "\n")
	if all := strings.Split(body, "\n"); len(all) > headlessLogLines {
		body = strings.Join(all[len(all)-headlessLogLines:], "\n")
	}
	return tabMsg{
		name: key,
		t:    tabTerminal,
		content: tabContent{
			loaded:     true,
			at:         time.Now(),
			body:       body,
			transcript: true,
			logName:    filepath.Base(logPath),
		},
	}, true
}

// fetchTerminal resolves the builder agent and reads its recent output, or --
// for a headless builder, or a claude pane builder whose session record is
// located (#184) -- the tail of its round log. round is the round being
// viewed; only the binding's current round (b.Round) has a live pane to
// read, so a pane builder's non-current round with no round log of its own
// reads as the empty prose "terminal is live; round N left no log" (#183).
func fetchTerminal(ctx context.Context, src Source, key string, round, lines int) tea.Cmd {
	if lines < 1 {
		lines = 1
	}
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    unresolvedKey(key),
				},
			}
		}
		b, err := rt.Store.Load(name)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    err,
				},
			}
		}

		// A headless builder (#99) has no pane; its output is a round's log
		// file. Shown, never parsed.
		if b.Builder.Headless() {
			if round != b.Round {
				// A past round: its own file, canonically named, is the
				// only place it could be.
				if msg, ok := logTab(key, name, rt.Store.BuilderLogPath(name, round)); ok {
					msg.round = round
					return msg
				}
				return tabMsg{
					name:  key,
					round: round,
					t:     tabTerminal,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						empty:  fmt.Sprintf("terminal is live; round %d left no log", round),
					},
				}
			}
			// The current round: the cursor names the exact file the
			// process is writing (or, between rounds, last wrote) --
			// between rounds clearProcess blanks LogPath, but the cursor
			// still names the last round that ran (transcript spec §4.7),
			// so fall back to that rather than a blank tab.
			logPath := b.Builder.LogPath
			if logPath == "" && b.Builder.StreamRound != 0 {
				logPath = rt.Store.BuilderLogPath(name, b.Builder.StreamRound)
			}
			if logPath == "" {
				return tabMsg{
					name:  key,
					round: round,
					t:     tabTerminal,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						empty:  "headless builder; no round has run yet, so there is no log",
					},
				}
			}
			if msg, ok := logTab(key, name, logPath); ok {
				msg.round = round
				return msg
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					empty:  "log not written yet: " + logPath,
				},
			}
		}

		// A claude pane builder's own session record is rendered into the
		// round log the same way (#184), for whichever round is being
		// viewed; any other pane builder has no record relay can read.
		if msg, ok := logTab(key, name, rt.Store.BuilderLogPath(name, round)); ok {
			msg.round = round
			return msg
		}

		// No round log for round. The live pane only ever shows the
		// binding's current round.
		if round != b.Round {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					empty:  fmt.Sprintf("terminal is live; round %d left no log", round),
				},
			}
		}

		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
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
				name:  key,
				round: round,
				t:     tabTerminal,
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
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    err,
				},
			}
		}

		return tabMsg{
			name:  key,
			round: round,
			t:     tabTerminal,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
				body:   out,
			},
		}
	}
}

// fetchDiff retrieves the stored patch for the specified round.
func fetchDiff(ctx context.Context, src Source, key string, round int) tea.Cmd {
	if round < 1 {
		return func() tea.Msg {
			return tabMsg{
				name:  key,
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
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					err:    unresolvedKey(key),
				},
			}
		}
		patch, ok, err := relay.ReadDiff(rt, name, round)
		if err != nil {
			return tabMsg{
				name:  key,
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
			empty := fmt.Sprintf("no diff recorded for round %d — no baseline captured", round)
			if b, berr := rt.Store.Load(name); berr == nil && round == b.Round {
				// round is the binding's current, not-yet-closed round: no
				// diff is missing, none has been captured yet (#183).
				empty = fmt.Sprintf("diff is captured when round %d closes", round)
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					empty:  empty,
				},
			}
		}

		return tabMsg{
			name:  key,
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

// fetchLog formats round's log entries with the cmdLog layout.
func fetchLog(ctx context.Context, src Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabLog,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    unresolvedKey(key),
				},
			}
		}
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabLog,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    err,
				},
			}
		}

		var b strings.Builder
		n := 0
		for _, e := range entries {
			if e.Round != round {
				continue
			}
			b.WriteString(relay.LogLine(e))
			b.WriteByte('\n')
			n++
		}

		if n == 0 {
			empty := fmt.Sprintf("no entries for round %d", round)
			if len(entries) == 0 {
				empty = "no entries yet"
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabLog,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					empty:  empty,
				},
			}
		}

		return tabMsg{
			name:  key,
			round: round,
			t:     tabLog,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
				body:   b.String(),
			},
		}
	}
}

// sectionForTab maps a ui tab to the relay.ShowSection fetchShow reads for
// it -- terminal -> transcript, everything else its own name (§5.8).
func sectionForTab(t tab) relay.ShowSection {
	switch t {
	case tabPlan:
		return relay.ShowPlan
	case tabReport:
		return relay.ShowReport
	case tabTerminal:
		return relay.ShowTranscript
	case tabDiff:
		return relay.ShowDiff
	case tabLog:
		return relay.ShowLog
	default:
		return relay.ShowPlan
	}
}

// tabForSection is sectionForTab's inverse, so fetchShow's tabMsg carries
// the ui tab a reply routes to rather than the relay.ShowSection it read.
func tabForSection(s relay.ShowSection) tab {
	switch s {
	case relay.ShowPlan:
		return tabPlan
	case relay.ShowReport:
		return tabReport
	case relay.ShowTranscript:
		return tabTerminal
	case relay.ShowDiff:
		return tabDiff
	case relay.ShowLog:
		return tabLog
	default:
		return tabPlan
	}
}

// fetchShow wraps relay.Show for a non-live (hist) binding's detail tabs:
// every tab of an archived or otherwise not-live binding reads the
// database through it. Missing renders as tabContent.empty prose ("no
// <section> for round N"), never as an error; a Show error renders as
// tabContent.err exactly like a failed file read (§6).
func fetchShow(ctx context.Context, rt relay.Runtime, name string, round int, section relay.ShowSection) tea.Cmd {
	t := tabForSection(section)
	return func() tea.Msg {
		res, err := relay.Show(ctx, rt, relay.ShowOptions{Name: name, Round: round, Section: section})
		if err != nil {
			return tabMsg{
				name:  name,
				round: round,
				t:     t,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					err:    err,
				},
			}
		}

		content := tabContent{loaded: true, at: time.Now(), round: round}
		switch {
		case section == relay.ShowLog:
			if len(res.Events) == 0 {
				content.empty = fmt.Sprintf("no %s for round %d", section, round)
				break
			}
			var b strings.Builder
			for _, e := range res.Events {
				b.WriteString(relay.LogLine(e))
				b.WriteByte('\n')
			}
			content.body = b.String()
		case res.Missing:
			content.empty = fmt.Sprintf("no %s for round %d", section, round)
		case section == relay.ShowTranscript:
			content.body = res.Text
			content.transcript = true
		default:
			content.body = res.Text
		}

		return tabMsg{name: name, round: round, t: t, content: content}
	}
}

// fetchFor dispatches to the right command for a tab, so switchTab and
// visibleTabFetch share one mapping instead of two switches that can drift.
// live is false for a hist row's detail (#172, §5.8): every tab goes
// through fetchShow instead of live's own fetchers. A hist row's key is
// its bare name (hist rows are planner-only; the server never has them),
// read through src.Base().
//
// It takes explicit parameters rather than a Model: fetch.go is built in
// Step 2 and Model does not exist until Step 3, so a Model parameter here
// would not compile in the step that introduces it.
func fetchFor(ctx context.Context, src Source, t tab, key string, round, lines int, live bool) tea.Cmd {
	if !live {
		return fetchShow(ctx, src.Base(), key, round, sectionForTab(t))
	}
	switch t {
	case tabPlan:
		return fetchPlan(ctx, src, key, round)
	case tabReport:
		return fetchReport(ctx, src, key, round)
	case tabTerminal:
		return fetchTerminal(ctx, src, key, round, lines)
	case tabDiff:
		return fetchDiff(ctx, src, key, round)
	case tabLog:
		return fetchLog(ctx, src, key, round)
	default:
		return nil
	}
}
