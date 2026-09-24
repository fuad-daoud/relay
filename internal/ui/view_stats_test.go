package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ledger"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/store"
)

// statsFixture builds a rich stats.Report by hand, so the view tests and the
// goldens never need a database (§7).
func statsFixture() stats.Report {
	at := func(day int) time.Time {
		return time.Date(2026, time.September, day, 12, 0, 0, 0, time.UTC)
	}
	var counts [24]int
	counts[9], counts[14] = 2, 1
	return stats.Report{
		Since: at(16), Until: at(24),
		Totals: stats.Totals{
			Rounds: 6, Bindings: 2, Candidates: 2, Unrecorded: 1,
			CostUSD: 1.40, PlanRounds: 1, Tokens: 2_500_000, MedianMS: 27 * 60_000,
		},
		Scorecard: []stats.ScoreRow{
			{
				Token:  "opencode/cline-pass/deepseek-v4.1-flash",
				Rounds: 5, Closed: 4, Reported: 3, Halted: 1,
				DonePct: 75, HaltPct: 25,
				MedianMS: 27 * 60_000, HasMedian: true,
				TTFTMS: 2500, HasTTFT: true,
				CostPerRound: 0.04, HasCost: true,
				CommitsPerRound: 1.2, HasCommits: true,
			},
			{
				Token:  "agy/antigravity/claude-sonnet-4-6",
				Rounds: 2, Closed: 2, Reported: 2, DonePct: 100, HaltPct: 0,
				MedianMS: 9 * 60_000, HasMedian: true, Plan: true, Few: true,
			},
		},
		Spend: stats.Spend{
			Days: []stats.DayCost{
				{Day: "2026-09-16", USD: 0.40, ByProvider: map[string]float64{"cline-pass": 0.40}},
				{Day: "2026-09-17", USD: 0.20, ByProvider: map[string]float64{"cline-pass": 0.20}},
				{Day: "2026-09-18", USD: 0.00, ByProvider: map[string]float64{}},
				{Day: "2026-09-19", USD: 0.80, ByProvider: map[string]float64{"google": 0.80}},
			},
			ThisWeek: 1.40, LastWeek: 0.55,
		},
		Reliability: stats.Reliability{
			Switches: 2, RoundsSwitched: 1, SwitchPct: 33, RateLimits: 3, SpawnFailures: 1,
			ByHour: []stats.HourRow{{Provider: "google", Counts: counts}},
			Active: []ledger.Gate{{
				Token: "gemini-3.8-flash-high", Kind: ledger.RateLimited,
				Since: at(24), Until: at(19).AddDate(0, 1, 0),
			}},
		},
		Repos: []stats.GroupRow{
			{Key: "https://github.com/fuad-daoud/relevo", Rounds: 5, Halted: 1, Landed: 1, CostUSD: 1.30, RoundsPerLand: 3.0},
			{Key: "(none)", Rounds: 1, CostUSD: 0.10},
		},
		Features: []stats.GroupRow{
			{Key: "cockpit", Rounds: 3, CostUSD: 0.90, Landed: 1, RoundsPerLand: 2.0},
		},
		Outcomes: stats.Outcomes{
			ByRound: map[string]int{
				db.OutcomeReported: 4, db.OutcomeHalted: 1, db.OutcomeOpen: 1,
			},
			ByReport: map[string]int{"done": 3, "halted": 1, "no outcome": 1},
		},
	}
}

// statsTestView is a loaded stats view over the fixture.
func statsTestView(window string) statsView {
	return statsView{window: window, loaded: true, rep: statsFixture()}
}

// statsTestEnv is a view Env with a store but no database.
func statsTestEnv(t *testing.T, width, height int) Env {
	t.Helper()
	return testEnv(plannerSource{relevo.Runtime{Store: store.New(t.TempDir())}}, relevo.Report{}, width, height)
}

// statsKey builds a rune key.
func statsKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// statsShell builds the shell over a temp DB with the stats view at window
// pushed and its first fetch drained.
func statsShell(t *testing.T, width, height int, window string) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	m := newModel(context.Background(), plannerSource{relevo.Runtime{Store: st, DB: d}},
		Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: relevo.Report{}})
	m = res.(Model)
	m = drain(t, m, execLine("stats "+window, m.env(), m.prefs))
	if _, ok := m.top().(statsView); !ok {
		t.Fatalf(":stats must replace the stack, top is %T", m.top())
	}
	return m
}

// TestStatsWindowCycle: `w` cycles 7d → 30d → 90d → all → 7d, each returns a
// fetch, and a statsMsg for another window is dropped (§4.3, §7).
func TestStatsWindowCycle(t *testing.T) {
	env := statsTestEnv(t, 160, 40)
	v := View(statsTestView("30d"))

	for i, want := range []string{"90d", "all", "7d", "30d"} {
		next, cmd := v.Update(statsKey('w'), env)
		v = next
		if got := v.(statsView).window; got != want {
			t.Fatalf("w #%d: window = %q, want %q", i+1, got, want)
		}
		if cmd == nil {
			t.Fatalf("w #%d must return a fetch command", i+1)
		}
	}

	// A stale reply for another window must not touch the report.
	sv := v.(statsView)
	next, _ := sv.Update(statsMsg{window: "7d", rep: stats.Report{Totals: stats.Totals{Rounds: 99}}}, env)
	if got := next.(statsView).rep.Totals.Rounds; got != 6 {
		t.Errorf("stale statsMsg changed rep.Rounds to %d, want the fixture's", got)
	}
}

// TestStatsEnterOpensFilteredRounds: enter pushes `:rounds` filtered to the
// selected row, with the window's since term unless the window is all, and
// the (none) repo row notices (§4.3, §7).
func TestStatsEnterOpensFilteredRounds(t *testing.T) {
	const tok = "opencode/cline-pass/deepseek-v4.1-flash"

	t.Run("candidates row", func(t *testing.T) {
		m := statsShell(t, 160, 40, "30d")
		res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
		m = res.(Model)
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		if got := rv.dash.QueryText(); got != "candidate:"+tok+" since:30d" {
			t.Errorf("QueryText = %q, want %q", got, "candidate:"+tok+" since:30d")
		}
	})

	t.Run("under all there is no since", func(t *testing.T) {
		m := statsShell(t, 160, 40, "all")
		res, _ := m.Update(statsMsg{window: "all", rep: statsFixture()})
		m = res.(Model)
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		if got := rv.dash.QueryText(); got != "candidate:"+tok {
			t.Errorf("QueryText = %q, want no since term", got)
		}
	})

	t.Run("repos row", func(t *testing.T) {
		m := statsShell(t, 160, 40, "30d")
		res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
		m = res.(Model)
		res, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = res.(Model)
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		want := `repo:"https://github.com/fuad-daoud/relevo" since:30d`
		if got := rv.dash.QueryText(); got != want {
			t.Errorf("QueryText = %q, want %q", got, want)
		}
	})

	t.Run("none row notices", func(t *testing.T) {
		m := statsShell(t, 160, 40, "30d")
		res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
		m = res.(Model)
		res, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = res.(Model)
		res, _ = m.Update(statsKey('j'))
		m = res.(Model)
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		if _, ok := m.top().(roundsView); ok {
			t.Fatal("the (none) row must not push a rounds view")
		}
		if !strings.Contains(m.notice, "rounds with no repo cannot be filtered") {
			t.Errorf("notice = %q", m.notice)
		}
	})
}

// TestStatsNoDatabase: `:stats` against a source with a nil DB is refused
// with the notice and the stack is unchanged (§4.6, §7).
func TestStatsNoDatabase(t *testing.T) {
	m := splitModel(t, 140, 40)
	cmd := execLine("stats", m.env(), m.prefs)
	m = drain(t, m, cmd)
	if len(m.stack) != 1 {
		t.Errorf("stack depth = %d, want 1 (unchanged)", len(m.stack))
	}
	if !strings.Contains(m.notice, "no database") {
		t.Errorf("notice = %q, want the no-database notice", m.notice)
	}

	if _, _, err := newStatsView(m.env(), "30d"); !errors.Is(err, relevo.ErrNoDatabase) {
		t.Errorf("newStatsView err = %v, want ErrNoDatabase", err)
	}
}

// TestStatsDayBarsWidenWhenDaysAreFew pins C2b W5: with fewer days than the
// spend panel has columns, each day takes statsDayCols columns -- here three,
// a two-cell bar then its one-cell gap -- instead of bunching one-column bars
// at the left.
func TestStatsDayBarsWidenWhenDaysAreFew(t *testing.T) {
	v := statsTestView("30d")
	// 72 is the spend panel's width in the wide (160-column) layout.
	lines := v.spendLines(72)
	row := stripANSI(lines[3]) // the bottom bar row
	bar := row[strings.Index(row, "┼")+len("┼"):]
	runes := []rune(bar)
	if len(runes) != 12 {
		t.Fatalf("the bar area is %d columns, want 12 (4 days × 3 per day): %q", len(runes), bar)
	}
	if got := statsDayCols(statsPlotW(72, len(stats.Money(0.80))), 4); got != 3 {
		t.Errorf("statsDayCols = %d, want 3", got)
	}
	// Every two-cell bar is followed by its gap cell, so the day groups are
	// three columns wide.
	if runes[0] != '█' || runes[1] != '█' {
		t.Errorf("bars = %q, want the first day's two cells filled", bar)
	}
	if runes[2] != ' ' || runes[5] != ' ' {
		t.Errorf("bars = %q, want a gap cell after every two-cell bar", bar)
	}
}

// TestStatsRefreshThrottle: a tick within 30s does not refetch; one after
// does (§4.3, §7).
func TestStatsRefreshThrottle(t *testing.T) {
	sv := statsTestView("30d")
	sv.fetchedAt = railNow

	soon := testEnv(plannerSource{relevo.Runtime{Store: store.New(t.TempDir())}}, relevo.Report{}, 160, 40)
	soon.Now = railNow.Add(10 * time.Second)
	next, cmd := sv.Update(tickMsg(soon.Now), soon)
	if cmd != nil {
		t.Error("a tick within 30s must not refetch")
	}

	late := soon
	late.Now = railNow.Add(31 * time.Second)
	next, cmd = next.Update(tickMsg(late.Now), late)
	if cmd == nil {
		t.Error("a tick after 30s must refetch")
	}
	if !next.(statsView).fetching {
		t.Error("a refetch must mark the view fetching")
	}
}

// TestStatsFocusAndCursor: tab moves between the two panels and j/k move the
// focused cursor within bounds (§4.3, §7).
func TestStatsFocusAndCursor(t *testing.T) {
	env := statsTestEnv(t, 100, 30)
	v := View(statsTestView("30d"))

	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}, env)
	v = next
	if got := v.(statsView).focus; got != 1 {
		t.Fatalf("tab: focus = %d, want 1", got)
	}
	// Two repos: j twice clamps at the last row.
	for i := 0; i < 2; i++ {
		next, _ = v.Update(statsKey('j'), env)
		v = next
	}
	if got := v.(statsView).cursor[1]; got != 1 {
		t.Errorf("cursor[1] = %d, want 1 (clamped)", got)
	}
	next, _ = v.Update(statsKey('k'), env)
	v = next
	if got := v.(statsView).cursor[1]; got != 0 {
		t.Errorf("cursor[1] = %d, want 0", got)
	}
	// Back to candidates: two rows, k clamps at 0.
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}, env)
	v = next
	if got := v.(statsView).focus; got != 0 {
		t.Fatalf("tab back: focus = %d, want 0", got)
	}
	next, _ = v.Update(statsKey('k'), env)
	v = next
	if got := v.(statsView).cursor[0]; got != 0 {
		t.Errorf("cursor[0] = %d, want 0 (clamped)", got)
	}
}
