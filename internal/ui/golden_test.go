package ui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ledger"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
	"github.com/fuad-daoud/relevo/internal/usage"
)

var updateGolden = flag.Bool("update", false, "update golden files")

const goldenVersion = "v0.13.0-28-gb66c6fc"

// goldenModel is a shell loaded with a full relevo.Report.
func goldenModel(t *testing.T, width, height int, rep relevo.Report) Model {
	t.Helper()
	st := store.New(t.TempDir())
	m := newModel(context.Background(), plannerSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: rep})
	return res.(Model)
}

// allStatesRows covers every display state and the row facts the fleet and
// pane can show today.
func allStatesRows() []relevo.BindingStatus {
	return []relevo.BindingStatus{
		{
			Name: "atlas", Round: 4, Display: "ACTIVE",
			PlannerName: "architect-1", PlannerKind: "claude", PlannerRoute: "channel", PlannerRouteLive: true,
			BuilderKind: "opencode", BuilderStatus: "working", Branch: "relevo/atlas",
			LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
				Tokens: usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200},
				Cost:   usage.Cost{USD: 0.04, Basis: usage.Measured}, Samples: 3},
			Spend: &usage.Spend{Rounds: 1, Measured: 0.16, Tokens: usage.Tokens{In: 3_500_000}},
		},
		{
			Name: "webshop", Round: 4, Display: "NEEDS YOU",
			PlannerName: "architect-1", PlannerKind: "claude", PlannerRoute: "pull",
			BuilderKind: "agy", BuilderStatus: "blocked", Branch: "relevo/webshop",
			Dirty: true, Consults: 2,
			LastUsage: &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 9 * 60_000,
				Tokens: usage.Tokens{In: 100, CacheRead: 15_000_000, CacheWrite: 50_000, Out: 55_000}, Cost: usage.Cost{USD: 4.71, Basis: usage.Measured}, Samples: 1},
			Spend:   &usage.Spend{Rounds: 3, Consults: 2, Measured: 9.40, Unknown: 1},
			Waiting: &relevo.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute), Line: "which branch should r4 target?", Hint: "relevo status --name webshop"},
			Last:    &relevo.LastEvent{TS: railNow.Add(-2 * time.Minute), Round: 4, Kind: store.KindQuestion},
		},
		{
			Name: "ledger", Round: 3, Display: "PAUSED",
			PlannerName: "architect-1", PlannerKind: "claude", PlannerRoute: "pull",
			BuilderKind: "agy", BuilderStatus: "idle", Branch: "relevo/ledger",
			Spend: &usage.Spend{Rounds: 3, Measured: 1.23, Estimated: 0.40, Unknown: 1},
		},
		{
			Name: "api", Round: 2, Display: "ACTIVE",
			PlannerName: "architect-1", PlannerKind: "claude", PlannerRoute: "channel", PlannerRouteLive: true,
			BuilderKind: "agy", BuilderStatus: "working", Branch: "relevo/api",
		},
		{
			Name: "worker", Round: 2, Display: "ACTIVE",
			PlannerName: "architect-1", PlannerKind: "claude", PlannerRoute: "channel", PlannerRouteLive: true,
			BuilderKind: "opencode", BuilderStatus: "working", BuilderCandidate: "opencode-1", Branch: "relevo/worker",
			Headless: &relevo.HeadlessInfo{PID: 48211, StartedAt: railNow.Add(-21 * time.Minute)},
		},
		{
			Name: "docs", Round: 1, Display: "DONE",
			PlannerName: "architect-1", PlannerKind: "claude", PlannerRoute: "pull",
			BuilderKind: "agy", BuilderStatus: "unknown", CWD: "/home/x/docs",
			Last: &relevo.LastEvent{TS: railNow.Add(-3 * time.Hour)},
		},
	}
}

// reportReadyRows is the fleet fixture for the report-ready golden: every
// state row, plus a binding whose human planner is owed a report (§4.5). Its
// display word is ACTIVE, so the header's count can only include it through
// the report-ready rule.
func reportReadyRows() []relevo.BindingStatus {
	rows := append([]relevo.BindingStatus(nil), allStatesRows()...)
	return append(rows, relevo.BindingStatus{
		Name: "inbox", Round: 3, Display: "ACTIVE",
		PlannerID: "pl_aaaaaaaabbbb", PlannerName: "you", PlannerKind: "human", PlannerRoute: "pull",
		BuilderKind: "opencode", BuilderStatus: "exited", Branch: "relevo/inbox",
		Last:    &relevo.LastEvent{TS: railNow.Add(-3 * time.Minute), Round: 3, Kind: store.KindReport},
		Pending: &relevo.PendingInfo{Round: 3, Kind: store.KindReport},
	})
}

// histRows is the archived fixture for the round-archived golden.
func histRows() []relevo.HistoryBinding {
	return []relevo.HistoryBinding{
		{
			Name: "oldapi", ID: "h1", Rounds: 3, Feature: "auth",
			LastActivity: railNow.Add(-49 * 24 * time.Hour),
			Archived:     true, ArchivedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
	}
}

func gatedGates() []ledger.Gate {
	return []ledger.Gate{
		{Token: "codex", Kind: ledger.RateLimited, Since: railNow, Until: railNow.Add(88 * time.Minute)},
	}
}

// realFleetReport builds the realistic fleet report specified in §8 / §2.3.
func realFleetReport() relevo.Report {
	// 27 DONE rows: 3 today (newest done-01 at now-38m), others on previous days.
	doneRows := make([]relevo.BindingStatus, 27)
	for i := 1; i <= 27; i++ {
		name := fmt.Sprintf("done-%02d", i)
		var ts time.Time
		switch i {
		case 1:
			ts = railNow.Add(-38 * time.Minute)
		case 2:
			ts = railNow.Add(-2 * time.Hour)
		case 3:
			ts = railNow.Add(-5 * time.Hour)
		default:
			ts = railNow.Add(-time.Duration(i-2) * 24 * time.Hour)
		}
		doneRows[i-1] = relevo.BindingStatus{
			Name:    name,
			Round:   1,
			Display: "DONE",
			Last:    &relevo.LastEvent{TS: ts},
		}
	}

	bindings := []relevo.BindingStatus{
		{
			Name:        "fix-433",
			Round:       2,
			Display:     "NEEDS YOU",
			BuilderKind: "agy",
			BuilderName: "deepseek-v4.1-flash",
			PlannerName: "architect-3",
			Branch:      "relevo/fix-433",
			LastClose:   &relevo.CloseInfo{Commits: 2},
			Spend:       &usage.Spend{Measured: 0.03},
			Waiting: &relevo.Waiting{
				Cause: "blocked",
				Line:  "“Should the dedupe also cover archived bindings, or only live ones?”",
				Since: railNow.Add(-3 * time.Minute),
			},
			Last: &relevo.LastEvent{
				TS:    railNow.Add(-3 * time.Minute),
				Kind:  store.KindQuestion,
				Round: 2,
			},
		},
		{
			Name:          "spool-db",
			Round:         1,
			Display:       "ACTIVE",
			BuilderStatus: "working",
			QuietFor:      "17s",
			RoundStart:    railNow.Add(-5 * time.Minute),
			BuilderName:   "gemini-3.8-flash-high",
			PlannerName:   "architect-2",
			Spend:         &usage.Spend{Plan: 1},
		},
		{
			Name:          "tok-seg",
			Round:         1,
			Display:       "ACTIVE",
			BuilderStatus: "working",
			QuietFor:      "16s",
			RoundStart:    railNow.Add(-6 * time.Minute),
			BuilderName:   "gemini-3.8-flash-high",
			PlannerName:   "architect-5",
			Spend:         &usage.Spend{Plan: 1},
		},
		{
			Name:          "oc-tui-a",
			Round:         6,
			Display:       "ACTIVE",
			BuilderStatus: "idle",
			LastPayload: &relevo.LastEvent{
				Kind: store.KindReport,
				TS:   railNow.Add(-14 * time.Minute),
			},
			BuilderName: "deepseek-v4.1-flash",
			PlannerName: "architect-3",
			Spend:       &usage.Spend{Measured: 0.20},
			Unread:      true,
		},
		{
			Name:          "rl-tail",
			Round:         2,
			Display:       "ACTIVE",
			BuilderStatus: "idle",
			LastPayload: &relevo.LastEvent{
				Kind: store.KindReport,
				TS:   railNow.Add(-31 * time.Minute),
			},
			BuilderName: "gemini-3.8-flash-high",
			PlannerName: "architect-5",
			Spend:       &usage.Spend{Plan: 1},
		},
		{
			Name:          "oc-tui-probe",
			Round:         4,
			Display:       "ACTIVE",
			BuilderStatus: "idle",
			LastPayload: &relevo.LastEvent{
				Kind: store.KindReport,
				TS:   railNow.Add(-48 * time.Minute),
			},
			BuilderName: "gemini-3.8-flash-high",
			PlannerName: "architect-3",
			Spend:       &usage.Spend{Plan: 1},
		},
		{
			Name:          "serve-status-json",
			Round:         2,
			Display:       "ACTIVE",
			BuilderStatus: "idle",
			LastPayload: &relevo.LastEvent{
				Kind: store.KindReport,
				TS:   railNow.Add(-1 * time.Hour),
			},
			BuilderName: "deepseek-v4.1-flash",
			PlannerName: "architect-13",
			Spend:       &usage.Spend{Measured: 0.03},
		},
	}
	bindings = append(bindings, doneRows...)

	return relevo.Report{
		Bindings: bindings,
		Gated: []ledger.Gate{
			{
				Token: "agy/antigravity/claude-sonnet-4-6",
				Kind:  ledger.RateLimited,
				Since: railNow,
				Until: railNow.Add(42 * time.Hour),
			},
			{
				Token: "codex/openai/gpt-5.6-terra:high",
				Kind:  ledger.RateLimited,
				Since: railNow,
				Until: railNow.Add(25 * 24 * time.Hour),
			},
		},
	}
}

// dashRows is the dashboard golden's fixed grid.
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

// goldenRoundModel is the fleet with the cursor's row opened, then its
// terminal tab fed so the golden shows content.
func goldenRoundModel(t *testing.T, width, height int) Model {
	t.Helper()
	terminalBody := "$ go test ./...\nok  \tgithub.com/fuad-daoud/relevo/internal/ui\t1.2s\n"
	m := goldenModel(t, width, height, relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	m = drain(t, m, cmd)
	rv, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("enter did not push a round view: %T", m.top())
	}
	name, round := rv.pane.detail.name, rv.pane.detail.round
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = res.(Model)
	res, _ = m.Update(tabMsg{name: name, round: round, t: tabTerminal, content: tabContent{loaded: true, body: terminalBody, at: railNow}})
	return res.(Model)
}

// goldenArchivedRoundModel pushes an archived round view with its plan tab
// fed.
func goldenArchivedRoundModel(t *testing.T, width, height int) Model {
	t.Helper()
	st := store.New(t.TempDir())
	m := newModel(context.Background(), plannerSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusLoaded = true
	h := histRows()[0]
	v, _ := newHistRoundView(m.env(), h, 0)
	m.stack = append(m.stack, v)
	res, _ = m.Update(tabMsg{name: h.Name, round: 3, t: tabPlan, content: tabContent{loaded: true, round: 3, at: railNow, body: "# Round 3 plan\n\nDo the thing.\n"}})
	return res.(Model)
}

// realRoundModel pushes the round view for spool-db with realistic data (§8).
func realRoundModel(t *testing.T, width, height int) Model {
	t.Helper()
	rep := realFleetReport()
	for i := range rep.Bindings {
		if rep.Bindings[i].Name == "spool-db" {
			rep.Bindings[i].Display = "ACTIVE"
			rep.Bindings[i].BuilderStatus = "working"
			rep.Bindings[i].Round = 1
			rep.Bindings[i].PlanRound = 1
			rep.Bindings[i].Spend = nil
			rep.Bindings[i].BuilderName = "gemini-3.8-flash-high"
			rep.Bindings[i].PlannerName = "architect-2"
			rep.Bindings[i].Branch = "relevo/spool-db"
			rep.Bindings[i].Headless = &relevo.HeadlessInfo{
				PID:       1401366,
				StartedAt: railNow.Add(-5 * time.Minute),
			}
			rep.Bindings[i].RoundStart = railNow.Add(-5 * time.Minute)
			rep.Bindings[i].QuietFor = "17s"
			rep.Bindings[i].LiveUsage = &usage.Usage{
				Model:      "gemini-3.8-flash-high",
				DurationMS: 5 * 60_000,
				Samples:    1,
				Tokens:     usage.Tokens{In: 718_000, CacheRead: 4_200_000, Out: 64_000},
				Cost:       usage.Cost{Basis: usage.Unknown},
				Note:       "no price",
			}
			break
		}
	}
	m := goldenActionModel(t, width, height, &fakeActions{}, rep)
	m = pointer(t, m, "spool-db")
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)

	planBody := "# Round diff and consult findings go straight into round_file\n\nDate: 2026-09-24. Base: origin/main `b66c6fcc` (#441).\nThere is **one round** in this plan. builder.log, NNN-<id>-ask.md and NNN-plan.md\nare out of scope. Do not touch them.\n\n## Scope\n\n- builder.log is out of scope.\n- Do not touch them.\n\n```go\nfunc main() {}\n```\n"
	res, _ = m.Update(tabMsg{
		name:    "spool-db",
		round:   1,
		t:       tabPlan,
		content: tabContent{loaded: true, round: 1, at: railNow.Add(-5 * time.Minute), body: planBody},
	})
	return res.(Model)
}

// realRoundNeedsYouModel pushes the round view for fix-433 in NEEDS YOU state (§8).
func realRoundNeedsYouModel(t *testing.T, width, height int) Model {
	t.Helper()
	rep := realFleetReport()
	for i := range rep.Bindings {
		if rep.Bindings[i].Name == "fix-433" {
			rep.Bindings[i].PlanRound = 2
			break
		}
	}
	m := goldenActionModel(t, width, height, &fakeActions{}, rep)
	m = pointer(t, m, "fix-433")
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)

	planBody := "# Fix 433 Plan\n\nDedupe bindings across stores.\n"
	res, _ = m.Update(tabMsg{
		name:    "fix-433",
		round:   2,
		t:       tabPlan,
		content: tabContent{loaded: true, round: 2, at: railNow.Add(-3 * time.Minute), body: planBody},
	})
	return res.(Model)
}

// goldenRoundsModel hosts the dashboard with its rows fed, reached through
// the shell's start command so the breadcrumb reads relevo › rounds, as the
// real `:rounds` does (A5).
func goldenRoundsModel(t *testing.T, width, height int) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	m := newModel(context.Background(), plannerSource{relevo.Runtime{Store: st, DB: d}},
		Options{Interval: time.Second, Start: "rounds", Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, cmd := m.Update(statusMsg{report: relevo.Report{}})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(roundsView); !ok {
		t.Fatalf("Start=rounds must replace the stack, top is %T", m.top())
	}
	res, _ = m.Update(dash.RowsMsg{Rows: dashRows(), At: railNow})
	return res.(Model)
}

// goldenStatsModel hosts the stats view, reached through the shell's start
// command so the breadcrumb reads relevo › stats, and feeds it a report.
func goldenStatsModel(t *testing.T, width, height int, rep stats.Report) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	m := newModel(context.Background(), plannerSource{relevo.Runtime{Store: st, DB: d}},
		Options{Interval: time.Second, Start: "stats", Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, cmd := m.Update(statusMsg{report: relevo.Report{}})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(statsView); !ok {
		t.Fatalf("Start=stats must replace the stack, top is %T", m.top())
	}
	res, _ = m.Update(statsMsg{window: "30d", rep: rep})
	return res.(Model)
}

// overviewRows is the stats-overview golden's synthetic rounds, shaped like the
// user's data (§5): three candidates, a deepseek-like lane with a 99% cache
// share and a gemini-like one with 88%, tokens on four of the last seven days,
// three repos, and one row with no token field at all.
func overviewRows() []db.RoundRow {
	s := func(v string) *string { return &v }
	i64 := func(v int64) *int64 { return &v }
	at := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, time.Local) }

	relevo := s("https://github.com/fuad-daoud/relevo")
	money := s("https://github.com/fuad-daoud/money")
	site := s("https://github.com/fuad-daoud/site")
	deepseek := s("deepseek-v4.1-flash")
	gemini := s("gemini-3.8-flash-high")
	glm := s("glm-5.3-flash")
	kimi := s("kimi-k3")
	qwen := s("qwen-4-coder")

	// One round's four token columns per lane.
	type tokens struct{ in, cache, write, out int64 }
	ds := tokens{in: 20_000, cache: 1_980_000, write: 5_000, out: 60_000}
	gm := tokens{in: 120_000, cache: 880_000, out: 40_000}
	gl := tokens{in: 50_000, cache: 200_000, out: 20_000}
	row := func(binding string, repo *string, started time.Time, cand *string, outcome string, t tokens, nilTokens bool) db.RoundRow {
		r := db.RoundRow{
			BindingID: binding, BindingName: binding, Repo: repo,
			StartedAt: started, Outcome: outcome, BuilderCandidate: cand,
		}
		if !nilTokens {
			r.InTokens, r.CacheTokens, r.WriteTokens, r.OutTokens =
				i64(t.in), i64(t.cache), i64(t.write), i64(t.out)
		}
		if outcome != db.OutcomeOpen {
			r.DurationMS = i64(27 * 60_000)
		}
		return r
	}

	rows := []db.RoundRow{
		// deepseek-v4.1-flash: six rounds, one of them unmeasured, 99% cache.
		row("b1", relevo, at(11, 10), deepseek, db.OutcomeReported, ds, false),
		row("b1", money, at(13, 10), deepseek, db.OutcomeReported, ds, false),
		row("b1", relevo, at(15, 10), deepseek, db.OutcomeReported, ds, false),
		row("b1", relevo, at(17, 10), deepseek, db.OutcomeHalted, ds, false),
		row("b1", relevo, at(17, 11), deepseek, db.OutcomeReported, ds, false),
		row("b1", site, at(17, 12), deepseek, db.OutcomeOpen, ds, true),

		// gemini-3.8-flash-high: five measured rounds, 88% cache.
		row("b2", money, at(11, 11), gemini, db.OutcomeReported, gm, false),
		row("b2", money, at(13, 11), gemini, db.OutcomeReported, gm, false),
		row("b2", site, at(15, 11), gemini, db.OutcomeReported, gm, false),
		row("b2", site, at(17, 10), gemini, db.OutcomeReported, gm, false),
		row("b2", money, at(17, 11), gemini, db.OutcomeReported, gm, false),

		// glm-5.3-flash: five measured rounds, 80% cache.
		row("b3", relevo, at(11, 12), glm, db.OutcomeReported, gl, false),
		row("b3", relevo, at(13, 12), glm, db.OutcomeReported, gl, false),
		row("b3", money, at(15, 12), glm, db.OutcomeReported, gl, false),
		row("b3", site, at(17, 12), glm, db.OutcomeReported, gl, false),
		row("b3", relevo, at(17, 13), glm, db.OutcomeReported, gl, false),

		// kimi-k3 and qwen-4-coder: under five rounds, so the overview dims
		// their rows.
		row("b4", relevo, at(12, 10), kimi, db.OutcomeReported, gl, false),
		row("b4", relevo, at(16, 10), kimi, db.OutcomeReported, gl, false),
		row("b5", relevo, at(14, 10), qwen, db.OutcomeReported, gl, false),
	}
	// The halted round carries a report outcome, so the ROUNDS tile and the
	// context row have a halt to count.
	rows[3].ReportOutcome = s("halted")
	return rows
}

// statsOverviewReport is the stats-overview-132 golden's report: stats.Build
// over the synthetic rows, 30 days wide so the window matches the chart's
// `30 days` heading (§7), with one active gate on the gemini-like lane so the
// candidates golden's STATUS column has a gated row.
func statsOverviewReport() stats.Report {
	return stats.Build(stats.Inputs{
		Rows: overviewRows(),
		Gates: []ledger.Gate{{
			Token: "gemini-3.8-flash-high", Kind: ledger.RateLimited,
			Since: railNow, Until: railNow.Add(26 * time.Hour),
		}},
		Since: railNow.AddDate(0, 0, -29),
		Until: railNow,
		Loc:   time.Local,
	})
}

func TestGoldenViews(t *testing.T) {
	t.Cleanup(relevo.SetGateClock(func() time.Time { return railNow }))

	cases := []struct {
		name          string
		width, height int
		build         func(t *testing.T) Model
	}{
		{
			name: "stats-overview-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return goldenStatsModel(t, 132, 34, statsOverviewReport())
			},
		},
		{
			name: "stats-candidates-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsOverviewReport())
				res, _ := m.Update(statsKey('2'))
				return res.(Model)
			},
		},
		{
			name: "stats-tokens-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsOverviewReport())
				res, _ := m.Update(statsKey('3'))
				return res.(Model)
			},
		},
		{
			name: "stats-reliability-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsOverviewReport())
				res, _ := m.Update(statsKey('4'))
				return res.(Model)
			},
		},
		{
			name: "stats-repos-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsOverviewReport())
				res, _ := m.Update(statsKey('5'))
				return res.(Model)
			},
		},
		{
			name: "stats-wide", width: 160, height: 40,
			build: func(t *testing.T) Model { return goldenStatsModel(t, 160, 40, statsFixture()) },
		},
		{
			name: "stats-narrow", width: 100, height: 30,
			build: func(t *testing.T) Model { return goldenStatsModel(t, 100, 30, statsFixture()) },
		},
		{
			name: "stats-empty", width: 160, height: 40,
			build: func(t *testing.T) Model { return goldenStatsModel(t, 160, 40, stats.Report{}) },
		},
		{
			name: "fleet", width: 140, height: 40,
			build: func(t *testing.T) Model {
				return goldenModel(t, 140, 40, relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
			},
		},
		{
			name: "fleet-narrow-80", width: 80, height: 30,
			build: func(t *testing.T) Model {
				return goldenModel(t, 80, 30, relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
			},
		},
		{
			name: "fleet-real-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
			},
		},
		{
			name: "fleet-real-100", width: 100, height: 30,
			build: func(t *testing.T) Model {
				return goldenActionModel(t, 100, 30, &fakeActions{}, realFleetReport())
			},
		},
		{
			name: "fleet-real-done", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
				return res.(Model)
			},
		},
		{
			name: "fleet-filtered", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenModel(t, 140, 40, relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
				res, _ := m.Update(key('/'))
				m = res.(Model)
				for _, r := range "web" {
					res, _ = m.Update(key(r))
					m = res.(Model)
				}
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				return res.(Model)
			},
		},
		{
			name: "fleet-empty", width: 140, height: 40,
			build: func(t *testing.T) Model {
				return goldenModel(t, 140, 40, relevo.Report{})
			},
		},
		{
			name: "fleet-error-before-load", width: 140, height: 40,
			build: func(t *testing.T) Model {
				st := store.New(t.TempDir())
				m := newModel(context.Background(), plannerSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Version: goldenVersion})
				m.now = func() time.Time { return railNow }
				res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
				m = res.(Model)
				m.statusInFlight = false
				res, _ = m.Update(statusMsg{err: errors.New("status unavailable")})
				return res.(Model)
			},
		},
		{
			name: "round", width: 140, height: 40,
			build: func(t *testing.T) Model { return goldenRoundModel(t, 140, 40) },
		},
		{
			name: "round-archived", width: 140, height: 40,
			build: func(t *testing.T) Model { return goldenArchivedRoundModel(t, 140, 40) },
		},
		{
			name: "round-real-132", width: 132, height: 34,
			build: func(t *testing.T) Model { return realRoundModel(t, 132, 34) },
		},
		{
			name: "round-real-100", width: 100, height: 30,
			build: func(t *testing.T) Model { return realRoundModel(t, 100, 30) },
		},
		{
			name: "round-needs-you", width: 132, height: 34,
			build: func(t *testing.T) Model { return realRoundNeedsYouModel(t, 132, 34) },
		},
		{
			name: "rounds", width: 160, height: 40,
			build: func(t *testing.T) Model { return goldenRoundsModel(t, 160, 40) },
		},
		{
			name: "cmdline-open", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenModel(t, 140, 40, relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
				m = res.(Model)
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
				return res.(Model)
			},
		},
		{
			name: "help", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenModel(t, 140, 40, relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
				return res.(Model)
			},
		},
		{
			name: "confirm-stop", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 140, 40, &fakeActions{},
					relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				m = pointer(t, m, "atlas")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "confirm-stop-real-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
				m = pointer(t, m, "spool-db")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "prompt-gate", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 140, 40, &fakeActions{},
					relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				m = pointer(t, m, "worker")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "command-real-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
				m = res.(Model)
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
				return res.(Model)
			},
		},
		{
			name: "retry-list-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				a := &fakeActions{candidates: []string{"gemini-3.8-flash-high", "deepseek-v4.1-flash", "claude-sonnet-5"}}
				m := goldenActionModel(t, 132, 34, a, realFleetReport())
				m = pointer(t, m, "spool-db")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "prompt-send", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 140, 40, &fakeActions{},
					relevo.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				m = pointer(t, m, "atlas")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "report-ready", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 140, 40, &fakeActions{},
					relevo.Report{Bindings: reportReadyRows(), Gated: gatedGates()})
				return pointer(t, m, "inbox")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			got := stripANSI(m.View())

			path := filepath.Join("testdata", tc.name+".golden")
			if *updateGolden {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if got != string(want) {
				t.Errorf("%s: golden mismatch\ngot:\n%s\nwant:\n%s", tc.name, got, string(want))
			}

			lines := strings.Split(got, "\n")
			if len(lines) != tc.height {
				t.Errorf("%s: %d lines, want %d (height)", tc.name, len(lines), tc.height)
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > tc.width {
					t.Errorf("%s: line %d is %d wide, want <= %d: %q", tc.name, i, w, tc.width, l)
				}
			}
		})
	}
}
