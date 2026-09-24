package stats

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/history"
	"github.com/fuad-daoud/relevo/internal/ledger"
)

// stNow is the fixture's "now": every window below is relative to it.
var stNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func stStr(v string) *string   { return &v }
func stInt(v int) *int         { return &v }
func stI64(v int64) *int64     { return &v }
func stF64(v float64) *float64 { return &v }

// TestScorecardRates pins DonePct and HaltPct against Closed (an open round is
// not closed) and the median for both an even and an odd count.
func TestScorecardRates(t *testing.T) {
	rows := []db.RoundRow{
		{BuilderCandidate: stStr("a"), Outcome: db.OutcomeReported, DurationMS: stI64(600_000)},
		{BuilderCandidate: stStr("a"), Outcome: db.OutcomeOpen},
		{BuilderCandidate: stStr("a"), Outcome: db.OutcomeHalted, DurationMS: stI64(1_200_000)},
		{BuilderCandidate: stStr("b"), Outcome: db.OutcomeReported, DurationMS: stI64(100_000)},
		{BuilderCandidate: stStr("b"), Outcome: db.OutcomeReported, DurationMS: stI64(300_000)},
		{BuilderCandidate: stStr("b"), Outcome: db.OutcomeReported, DurationMS: stI64(500_000)},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	byTok := map[string]ScoreRow{}
	for _, s := range rep.Scorecard {
		byTok[s.Token] = s
	}

	a := byTok["a"]
	if a.Closed != 2 || a.Reported != 1 || a.Halted != 1 {
		t.Fatalf("a: closed/reported/halted = %d/%d/%d, want 2/1/1", a.Closed, a.Reported, a.Halted)
	}
	if a.DonePct != 50 || a.HaltPct != 50 {
		t.Errorf("a: done/halt = %v/%v, want 50/50", a.DonePct, a.HaltPct)
	}
	if a.MedianMS != 900_000 {
		t.Errorf("a: median = %d, want 900000 (mean of the two middles)", a.MedianMS)
	}

	b := byTok["b"]
	if b.Closed != 3 || b.DonePct != 100 {
		t.Fatalf("b: closed/done = %d/%v, want 3/100", b.Closed, b.DonePct)
	}
	if b.MedianMS != 300_000 {
		t.Errorf("b: median = %d, want 300000 (the odd middle)", b.MedianMS)
	}
}

// TestScorecardUnrecordedAndKeep pins that a nil candidate never gets a
// scorecard row but is counted in Totals, and that Keep drops a row.
func TestScorecardUnrecordedAndKeep(t *testing.T) {
	rows := []db.RoundRow{
		{BuilderCandidate: stStr("a"), Outcome: db.OutcomeReported},
		{BuilderCandidate: nil, Outcome: db.OutcomeReported},
		{BuilderCandidate: stStr("b"), Outcome: db.OutcomeReported},
	}
	rep := Build(Inputs{
		Rows:  rows,
		Until: stNow,
		Loc:   time.UTC,
		Keep: func(r db.RoundRow) bool {
			return r.BuilderCandidate != nil && *r.BuilderCandidate != "b"
		},
	})

	if rep.Totals.Candidates != 1 {
		t.Errorf("Totals.Candidates = %d, want 1: the nil candidate's rounds", rep.Totals.Candidates)
	}
	if rep.Totals.Rounds != 3 {
		t.Errorf("Totals.Rounds = %d, want 3: Totals counts every row", rep.Totals.Rounds)
	}
	if len(rep.Scorecard) != 1 || rep.Scorecard[0].Token != "a" {
		t.Fatalf("scorecard = %+v, want only a", rep.Scorecard)
	}
}

// TestScorecardPlanAndCost pins that a plan token reports no cost, that an
// unknown basis and a missing cost are excluded from the mean, and that Few is
// set under five rounds.
func TestScorecardPlanAndCost(t *testing.T) {
	rows := []db.RoundRow{
		{BuilderCandidate: stStr("plan/x"), Outcome: db.OutcomeReported, CostUSD: stF64(9), CostBasis: stStr("measured")},
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(1), CostBasis: stStr("measured")},
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(3), CostBasis: stStr("measured")},
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(2), CostBasis: stStr("unknown")},
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported},
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported},
	}
	rep := Build(Inputs{
		Rows:   rows,
		Until:  stNow,
		Loc:    time.UTC,
		IsPlan: func(tok string) bool { return tok == "plan/x" },
	})
	byTok := map[string]ScoreRow{}
	for _, s := range rep.Scorecard {
		byTok[s.Token] = s
	}

	p := byTok["plan/x"]
	if !p.Plan || p.HasCost {
		t.Errorf("plan row = plan %v / hasCost %v, want true/false", p.Plan, p.HasCost)
	}
	if !p.Few {
		t.Errorf("plan row Few = false, want true (1 round)")
	}

	y := byTok["paid/y"]
	if !y.HasCost {
		t.Fatalf("paid row HasCost = false, want true")
	}
	if y.CostPerRound != 2 {
		t.Errorf("paid row cost = %v, want 2 (the two measured rows)", y.CostPerRound)
	}
	if y.Few {
		t.Errorf("paid row Few = true, want false (5 rounds)")
	}
}

// TestSpendDaysAndWeeks pins that zero days are present, that each day's
// provider split sums to its USD, and that the week windows are exact at their
// boundaries.
func TestSpendDaysAndWeeks(t *testing.T) {
	at := func(y int, mo time.Month, d, h, mi int) time.Time {
		return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	}
	rows := []db.RoundRow{
		// Exactly Until-7d: this week.
		{StartedAt: stNow.Add(-7 * 24 * time.Hour), CostUSD: stF64(1), CostBasis: stStr("measured")},
		// One millisecond earlier: last week.
		{StartedAt: stNow.Add(-7*24*time.Hour - time.Millisecond), CostUSD: stF64(2), CostBasis: stStr("measured")},
		// Exactly Until-14d: last week.
		{StartedAt: stNow.Add(-14 * 24 * time.Hour), CostUSD: stF64(4), CostBasis: stStr("measured")},
		// One millisecond earlier: neither week.
		{StartedAt: stNow.Add(-14*24*time.Hour - time.Millisecond), CostUSD: stF64(8), CostBasis: stStr("measured")},
		// Inside the day window, with a provider.
		{StartedAt: at(2026, time.September, 23, 5, 0), BuilderProvider: stStr("p1"), CostUSD: stF64(16), CostBasis: stStr("measured")},
		// Inside the day window, with no provider.
		{StartedAt: at(2026, time.September, 24, 6, 0), CostUSD: stF64(32), CostBasis: stStr("measured")},
	}
	since := at(2026, time.September, 22, 0, 0)
	rep := Build(Inputs{Rows: rows, Since: since, Until: stNow, Loc: time.UTC})

	if rep.Spend.ThisWeek != 49 {
		t.Errorf("ThisWeek = %v, want 49 (1 on the boundary plus the two in-window days)", rep.Spend.ThisWeek)
	}
	if rep.Spend.LastWeek != 6 {
		t.Errorf("LastWeek = %v, want 6", rep.Spend.LastWeek)
	}

	if len(rep.Spend.Days) != 3 {
		t.Fatalf("days = %d, want 3 (22nd, 23rd, 24th)", len(rep.Spend.Days))
	}
	wantDays := []string{"2026-09-22", "2026-09-23", "2026-09-24"}
	for i, want := range wantDays {
		if rep.Spend.Days[i].Day != want {
			t.Errorf("day %d = %q, want %q", i, rep.Spend.Days[i].Day, want)
		}
	}
	if rep.Spend.Days[0].USD != 0 || len(rep.Spend.Days[0].ByProvider) != 0 {
		t.Errorf("zero day = %+v, want 0 and no providers", rep.Spend.Days[0])
	}
	for _, d := range rep.Spend.Days {
		var sum float64
		for _, v := range d.ByProvider {
			sum += v
		}
		if sum != d.USD {
			t.Errorf("day %s: provider split %v != USD %v", d.Day, sum, d.USD)
		}
	}
	if got := rep.Spend.Days[1].ByProvider["p1"]; got != 16 {
		t.Errorf("23rd p1 = %v, want 16", got)
	}
	if got := rep.Spend.Days[2].ByProvider["(none)"]; got != 32 {
		t.Errorf("24th (none) = %v, want 32", got)
	}
}

// TestReliabilityWindowAndHours pins that history events outside the window are
// excluded, that the by-hour buckets use Loc, and that the active gates pass
// through.
func TestReliabilityWindowAndHours(t *testing.T) {
	loc := time.FixedZone("X", 2*3600)
	rows := []db.RoundRow{
		{BuilderCandidate: stStr("a"), Outcome: db.OutcomeReported, Switches: 2},
		{BuilderCandidate: stStr("b"), Outcome: db.OutcomeReported},
	}
	gates := []ledger.Gate{{Token: "a"}}
	hist := history.History{Events: []history.Event{
		// In the window: 01:30 UTC is 03:30 local.
		{At: time.Date(2026, 9, 24, 1, 30, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "google"},
		// After Until: excluded.
		{At: time.Date(2026, 9, 24, 23, 30, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "google"},
		// Before Since: excluded.
		{At: time.Date(2026, 9, 19, 23, 0, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "google"},
		// In the window: 08:00 UTC is 10:00 local.
		{At: time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "openai"},
		// In the window.
		{At: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC), Kind: ledger.SpawnFailed, Provider: "google"},
		// Before Since: excluded.
		{At: time.Date(2026, 9, 19, 23, 0, 0, 0, time.UTC), Kind: ledger.SpawnFailed, Provider: "openai"},
	}}
	rep := Build(Inputs{
		Rows:    rows,
		History: hist,
		Gates:   gates,
		Since:   time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		Until:   stNow,
		Loc:     loc,
	})

	if rep.Reliability.RateLimits != 2 {
		t.Errorf("rate limits = %d, want 2", rep.Reliability.RateLimits)
	}
	if rep.Reliability.SpawnFailures != 1 {
		t.Errorf("spawn failures = %d, want 1", rep.Reliability.SpawnFailures)
	}
	if rep.Reliability.Switches != 2 || rep.Reliability.RoundsSwitched != 1 {
		t.Errorf("switches = %d in %d rounds, want 2 in 1", rep.Reliability.Switches, rep.Reliability.RoundsSwitched)
	}
	if rep.Reliability.SwitchPct != 50 {
		t.Errorf("switch pct = %v, want 50", rep.Reliability.SwitchPct)
	}
	if len(rep.Reliability.ByHour) != 2 {
		t.Fatalf("by hour = %+v, want google and openai", rep.Reliability.ByHour)
	}
	if rep.Reliability.ByHour[0].Provider != "google" || rep.Reliability.ByHour[0].Counts[3] != 1 {
		t.Errorf("google = %+v, want one at local hour 3", rep.Reliability.ByHour[0])
	}
	if rep.Reliability.ByHour[1].Provider != "openai" || rep.Reliability.ByHour[1].Counts[10] != 1 {
		t.Errorf("openai = %+v, want one at local hour 10", rep.Reliability.ByHour[1])
	}
	if len(rep.Reliability.Active) != 1 || rep.Reliability.Active[0].Token != "a" {
		t.Errorf("active = %+v, want the gate passed through", rep.Reliability.Active)
	}
}

// TestReposFeaturesLanded pins RoundsPerLand, and that a group with no landed
// binding reports 0.
func TestReposFeaturesLanded(t *testing.T) {
	rows := []db.RoundRow{
		{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Outcome: db.OutcomeReported},
		{BindingID: "b2", Repo: stStr("A"), Feature: stStr("f1"), Outcome: db.OutcomeReported},
		{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Outcome: db.OutcomeReported},
		{BindingID: "b3", Repo: stStr("A"), Outcome: db.OutcomeHalted},
		{BindingID: "b9", Repo: stStr("B"), Feature: stStr("f2"), Outcome: db.OutcomeHalted},
	}
	rep := Build(Inputs{
		Rows:   rows,
		Landed: map[string]bool{"b1": true, "b2": true},
		Until:  stNow,
		Loc:    time.UTC,
	})

	if len(rep.Repos) != 2 {
		t.Fatalf("repos = %+v, want A and B", rep.Repos)
	}
	a := rep.Repos[0]
	if a.Key != "A" || a.Rounds != 4 || a.Halted != 1 || a.Landed != 2 {
		t.Fatalf("repo A = %+v, want 4 rounds, 1 halted, 2 landed", a)
	}
	if a.RoundsPerLand != 1.5 {
		t.Errorf("repo A rounds/land = %v, want 1.5 (3 rounds over 2 landed)", a.RoundsPerLand)
	}
	b := rep.Repos[1]
	if b.Landed != 0 || b.RoundsPerLand != 0 {
		t.Errorf("repo B = %+v, want 0 landed and 0 rounds/land", b)
	}

	if len(rep.Features) != 2 {
		t.Fatalf("features = %+v, want f1 and f2", rep.Features)
	}
	f1 := rep.Features[0]
	if f1.Key != "f1" || f1.Rounds != 3 || f1.Landed != 2 || f1.RoundsPerLand != 1.5 {
		t.Errorf("feature f1 = %+v, want 3 rounds, 2 landed, 1.5 rounds/land", f1)
	}
}

// TestOutcomes pins that the unstructured report outcome is counted as
// "no outcome".
func TestOutcomes(t *testing.T) {
	rows := []db.RoundRow{
		{Outcome: db.OutcomeReported, ReportOutcome: stStr("done")},
		{Outcome: db.OutcomeReported, ReportOutcome: stStr("unstructured")},
		{Outcome: db.OutcomeOpen},
		{Outcome: db.OutcomeHalted, ReportOutcome: stStr("halted")},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	if got := rep.Outcomes.ByReport["done"]; got != 1 {
		t.Errorf("reports done = %d, want 1", got)
	}
	if got := rep.Outcomes.ByReport["no outcome"]; got != 1 {
		t.Errorf("reports no outcome = %d, want 1 (the unstructured row)", got)
	}
	if got := rep.Outcomes.ByRound[db.OutcomeReported]; got != 2 {
		t.Errorf("rounds reported = %d, want 2", got)
	}
	if got := rep.Outcomes.ByRound[db.OutcomeOpen]; got != 1 {
		t.Errorf("rounds open = %d, want 1", got)
	}
}

// TestBuildEmpty pins that no rows builds a zero report without a panic.
func TestBuildEmpty(t *testing.T) {
	rep := Build(Inputs{Until: stNow, Loc: time.UTC})

	if rep.Totals.Rounds != 0 || rep.Totals.CostUSD != 0 || len(rep.Scorecard) != 0 {
		t.Errorf("empty report = %+v, want zero totals and no scorecard", rep)
	}
	if !rep.Since.Equal(stNow) {
		t.Errorf("Since = %v, want Until when there is no row", rep.Since)
	}
	if len(rep.Outcomes.ByRound) != 0 || len(rep.Outcomes.ByReport) != 0 {
		t.Errorf("outcomes = %+v, want empty maps", rep.Outcomes)
	}
}
