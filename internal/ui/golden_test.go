package ui

import (
	"context"
	"flag"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/ui/dash"
)

var updateGolden = flag.Bool("update", false, "update golden files")

// goldenModel is splitModel widened to take a full relay.Report, so a
// fixture can carry Gated alongside its rows.
func goldenModel(t *testing.T, width, height int, rep relay.Report) Model {
	t.Helper()
	st := store.New(t.TempDir())
	fh := newFakePanes(t)
	_ = fh
	m := newModel(context.Background(), plannerSource{relay.Runtime{Store: st}}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: rep})
	return res.(Model)
}

// histRows covers a hist row's rendering (§5.8): one truly archived (tarred
// by `gc`), one done but never archived -- neither shares a name with
// allStatesRows(), so scopeRows never dedupes them away.
func histRows() []relay.HistoryBinding {
	return []relay.HistoryBinding{
		{
			Name: "oldapi", Rounds: 3, Feature: "auth",
			LastActivity: railNow.Add(-49 * 24 * time.Hour),
			Archived:     true, ArchivedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			Name:         "released",
			Rounds:       1,
			LastActivity: railNow.Add(-3 * time.Hour),
		},
	}
}

// goldenAllScopeModel is goldenModel with the rail in scope all: rep's
// rows live, hist's rows the database's, exactly as a real statusMsg
// carries both once scope is all (Task 3).
func goldenAllScopeModel(t *testing.T, width, height int, rep relay.Report, hist []relay.HistoryBinding) Model {
	t.Helper()
	st := store.New(t.TempDir())
	fh := newFakePanes(t)
	_ = fh
	m := newModel(context.Background(), plannerSource{relay.Runtime{Store: st}}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	m.scope = scopeAll
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: rep, dbRows: hist})
	return res.(Model)
}

func gatedGates() []ledger.Gate {
	return []ledger.Gate{
		{Token: "codex", Kind: ledger.RateLimited, Since: railNow, Until: railNow.Add(88 * time.Minute)},
	}
}

// dashRows is the dashboard golden's fixed grid: three rounds across two
// bindings and two builders, with the columns the grid and the tiles read.
func dashRows() []db.RoundRow {
	s := func(v string) *string { return &v }
	i := func(v int) *int { return &v }
	i64 := func(v int64) *int64 { return &v }
	f := func(v float64) *float64 { return &v }
	return []db.RoundRow{
		{
			BindingID: "b1", BindingName: "persist",
			Number: 5, StartedAt: railNow.Add(-2 * time.Hour), Outcome: db.OutcomeReported,
			BuilderCandidate: s("claude/anthropic/sonnet"), Commits: i(1), Tree: s("clean"),
			GateResult: s("pass"), InTokens: i64(1_000_000), OutTokens: i64(200_000),
			CostUSD: f(0.42), CostBasis: s("measured"), DurationMS: i64(27 * 60_000),
		},
		{
			BindingID: "b1", BindingName: "persist",
			Number: 4, StartedAt: railNow.Add(-26 * time.Hour), Outcome: db.OutcomeHalted,
			BuilderCandidate: s("claude/anthropic/sonnet"), Commits: i(0), Tree: s("dirty"),
			GateResult: s("fail"), InTokens: i64(400_000), CacheTokens: i64(100_000),
			CostUSD: f(1.10), CostBasis: s("measured"), DurationMS: i64(12 * 60_000),
		},
		{
			BindingID: "b2", BindingName: "api",
			Number: 2, StartedAt: railNow.Add(-50 * time.Hour), Outcome: db.OutcomeReported,
			BuilderCandidate: s("agy/antigravity/claude-sonnet-4-6"), Commits: i(3), Tree: s("clean"),
			GateResult: s("pass"), InTokens: i64(2_000_000),
			CostUSD: f(9.10), CostBasis: s("unknown"),
		},
	}
}

// goldenDashModel is goldenModel with a database behind the source, pressing
// d to reach the dashboard and injecting its rowsMsg, as the host's loop
// would after EnterDashboard's fetch.
func goldenDashModel(t *testing.T, width, height int, rep relay.Report) Model {
	t.Helper()
	st := store.New(t.TempDir())
	fh := newFakePanes(t)
	_ = fh
	d, err := db.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	m := newModel(context.Background(), plannerSource{relay.Runtime{Store: st, DB: d}}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: rep})
	m = res.(Model)
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = res.(Model)
	res, _ = m.Update(dash.RowsMsg{Rows: dashRows(), At: railNow})
	return res.(Model)
}

// feedTerminal switches the pane to the terminal tab and delivers a
// tabMsg reply for it, so a golden fixture shows content instead of
// "loading…".
func feedTerminal(t *testing.T, m Model, name, body string) Model {
	t.Helper()
	m.detail.active = tabTerminal
	res, _ := m.Update(tabMsg{name: name, round: m.detail.round, t: tabTerminal, content: tabContent{loaded: true, body: body, at: railNow}})
	return res.(Model)
}
