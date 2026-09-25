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
// scorecard row but counts in Totals.Unrecorded, that Candidates counts the
// distinct non-nil tokens, and that Keep drops a row.
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

	if rep.Totals.Unrecorded != 1 {
		t.Errorf("Totals.Unrecorded = %d, want 1: the nil candidate's rounds", rep.Totals.Unrecorded)
	}
	if rep.Totals.Candidates != 2 {
		t.Errorf("Totals.Candidates = %d, want 2: the distinct non-nil tokens", rep.Totals.Candidates)
	}
	if rep.Totals.Rounds != 3 {
		t.Errorf("Totals.Rounds = %d, want 3: Totals counts every row", rep.Totals.Rounds)
	}
	if len(rep.Scorecard) != 1 || rep.Scorecard[0].Token != "a" {
		t.Fatalf("scorecard = %+v, want only a", rep.Scorecard)
	}
}

// TestScorecardPlanAndCost pins that a plan token reports no cost, that an
// unknown basis and a missing cost are excluded from the mean, that a nil
// basis counts as known, and that Few is set under five rounds.
func TestScorecardPlanAndCost(t *testing.T) {
	rows := []db.RoundRow{
		{BuilderCandidate: stStr("plan/x"), Outcome: db.OutcomeReported, CostUSD: stF64(9), CostBasis: stStr("measured")},
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(1), CostBasis: stStr("measured")},
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(3), CostBasis: stStr("measured")},
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(2), CostBasis: stStr("unknown")},
		// A cost with no basis at all: histq's costKnown rule counts it.
		{BuilderCandidate: stStr("paid/y"), Outcome: db.OutcomeReported, CostUSD: stF64(5)},
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
	if y.CostPerRound != 3 {
		t.Errorf("paid row cost = %v, want 3 (the two measured rows plus the nil-basis row)", y.CostPerRound)
	}
	if y.Few {
		t.Errorf("paid row Few = true, want false (6 rounds)")
	}
}

// TestScorecardBindingsHaltsSwitches pins §3.1: a scorecard row counts the
// distinct binding IDs among the candidate's rows, the rows whose report
// outcome is "halted", and the sum of their switches.
func TestScorecardBindingsHaltsSwitches(t *testing.T) {
	rows := []db.RoundRow{
		{BindingID: "b1", BuilderCandidate: stStr("a"), Outcome: db.OutcomeReported, Switches: 0},
		{BindingID: "b2", BuilderCandidate: stStr("a"), Outcome: db.OutcomeReported, Switches: 2, ReportOutcome: stStr("halted")},
		{BindingID: "b2", BuilderCandidate: stStr("a"), Outcome: db.OutcomeHalted, Switches: 1, ReportOutcome: stStr("done")},
		{BindingID: "b3", BuilderCandidate: stStr("a"), Outcome: db.OutcomeReported, Switches: 0},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	if len(rep.Scorecard) != 1 {
		t.Fatalf("scorecard = %+v, want one row", rep.Scorecard)
	}
	s := rep.Scorecard[0]
	if s.Bindings != 3 {
		t.Errorf("Bindings = %d, want 3 (b1, b2, b3)", s.Bindings)
	}
	if s.ReportHalted != 1 {
		t.Errorf("ReportHalted = %d, want 1 (the one harnessed halted report)", s.ReportHalted)
	}
	if s.Switches != 3 {
		t.Errorf("Switches = %d, want 3 (0+2+1+0)", s.Switches)
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

// TestSpendDayBreakdowns pins §3.1's day fields: each day carries its tokens by
// kind, its tokens per candidate and its tokens per provider, with Tokens still
// equal to the day's kinds' total.
func TestSpendDayBreakdowns(t *testing.T) {
	at := func(d, h int) time.Time {
		return time.Date(2026, time.September, d, h, 0, 0, 0, time.UTC)
	}
	rows := []db.RoundRow{
		{StartedAt: at(23, 9), BuilderCandidate: stStr("A"), BuilderProvider: stStr("p1"),
			InTokens: stI64(10), CacheTokens: stI64(100), OutTokens: stI64(5)},
		{StartedAt: at(23, 10), BuilderCandidate: stStr("B"), BuilderProvider: stStr("p2"),
			OutTokens: stI64(7)},
		{StartedAt: at(24, 9), BuilderCandidate: stStr("A"), InTokens: stI64(1)},
	}
	rep := Build(Inputs{
		Rows:  rows,
		Since: at(23, 0),
		Until: at(24, 12),
		Loc:   time.UTC,
	})

	if len(rep.Spend.Days) != 2 {
		t.Fatalf("days = %d, want 2", len(rep.Spend.Days))
	}
	d1, d2 := rep.Spend.Days[0], rep.Spend.Days[1]

	if d1.Kinds.In != 10 || d1.Kinds.Cache != 100 || d1.Kinds.Out != 12 {
		t.Errorf("day 1 Kinds = %+v, want In 10, Cache 100, Out 12", d1.Kinds)
	}
	if got := d1.ByCandidate; got["A"] != 115 || got["B"] != 7 || len(got) != 2 {
		t.Errorf("day 1 ByCandidate = %v, want A 115, B 7", got)
	}
	if got := d1.TokensByProvider; got["p1"] != 115 || got["p2"] != 7 || len(got) != 2 {
		t.Errorf("day 1 TokensByProvider = %v, want p1 115, p2 7", got)
	}
	if got := d2.ByCandidate; got["A"] != 1 || len(got) != 1 {
		t.Errorf("day 2 ByCandidate = %v, want A 1", got)
	}
	for i, d := range rep.Spend.Days {
		if d.Tokens != d.Kinds.Total() {
			t.Errorf("day %d Tokens = %d, want Kinds.Total() = %d", i, d.Tokens, d.Kinds.Total())
		}
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

// TestNoFeatureGroup pins §2.3: NoFeature buckets the feature-less rows into
// one "(none)" group, and it does not touch Features.
func TestNoFeatureGroup(t *testing.T) {
	rows := []db.RoundRow{
		{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), Outcome: db.OutcomeReported},
		{BindingID: "b2", Repo: stStr("A"), Outcome: db.OutcomeReported},
		{BindingID: "b3", Repo: stStr("A"), Outcome: db.OutcomeHalted},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	if rep.NoFeature.Rounds != 2 || rep.NoFeature.Key != "(none)" {
		t.Fatalf("NoFeature = %+v, want 2 rounds keyed (none)", rep.NoFeature)
	}
	if len(rep.Features) != 1 || rep.Features[0].Key != "f1" {
		t.Fatalf("Features = %+v, want only f1", rep.Features)
	}
}

// TestGroupRowsBreakdowns pins §3.1's new group breakdowns: the distinct
// bindings among a group's rows, the rows whose report outcome is done or
// halted, the sum of the recorded commits, and the round count per candidate,
// which skips a nil one.
func TestGroupRowsBreakdowns(t *testing.T) {
	rows := []db.RoundRow{
		{BindingID: "b1", Repo: stStr("A"), ReportOutcome: stStr("done"), Commits: stInt(2), BuilderCandidate: stStr("A")},
		{BindingID: "b2", Repo: stStr("A"), ReportOutcome: stStr("done"), BuilderCandidate: stStr("A")},
		{BindingID: "b3", Repo: stStr("A"), ReportOutcome: stStr("halted"), Commits: stInt(1), BuilderCandidate: stStr("B")},
		{BindingID: "b1", Repo: stStr("A"), Commits: stInt(0)},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	if len(rep.Repos) != 1 {
		t.Fatalf("repos = %+v, want one row", rep.Repos)
	}
	g := rep.Repos[0]
	if g.Bindings != 3 {
		t.Errorf("Bindings = %d, want 3 (b1, b2, b3)", g.Bindings)
	}
	if g.Done != 2 {
		t.Errorf("Done = %d, want 2 (the two done reports)", g.Done)
	}
	if g.ReportHalted != 1 {
		t.Errorf("ReportHalted = %d, want 1", g.ReportHalted)
	}
	if g.Commits != 3 {
		t.Errorf("Commits = %d, want 3 (2 + nil + 1 + 0)", g.Commits)
	}
	if got := g.ByCandidate; got["A"] != 2 || got["B"] != 1 || len(got) != 2 {
		t.Errorf("ByCandidate = %v, want A:2 B:1 (the nil candidate skipped)", got)
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

// TestTokenKindsSumAndCache pins §2's TokenCounts: the four kinds are summed
// over every row, Measured counts only the rows with a token field, Tokens
// stays equal to Total, and CachePct is cache over input.
func TestTokenKindsSumAndCache(t *testing.T) {
	rows := []db.RoundRow{
		{InTokens: stI64(100), CacheTokens: stI64(900), WriteTokens: stI64(50), OutTokens: stI64(10)},
		{InTokens: stI64(100), CacheTokens: stI64(900)},
		// No token field at all: not measured.
		{BuilderCandidate: stStr("a")},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	k := rep.Totals.TokenKinds
	if k.In != 200 || k.Cache != 1800 || k.Write != 50 || k.Out != 10 {
		t.Errorf("TokenKinds = %+v, want In 200, Cache 1800, Write 50, Out 10", k)
	}
	if k.Measured != 2 {
		t.Errorf("Measured = %d, want 2 (the two rows with a token field)", k.Measured)
	}
	if k.Total() != 2060 {
		t.Errorf("Total = %d, want 2060", k.Total())
	}
	if rep.Totals.Tokens != k.Total() {
		t.Errorf("Totals.Tokens = %d, want TokenKinds.Total() = %d", rep.Totals.Tokens, k.Total())
	}
	if pct, ok := k.CachePct(); !ok || pct != 90 {
		t.Errorf("CachePct = %v, %v; want 90, true (1800 of 2000 input)", pct, ok)
	}
	if per, ok := k.PerRound(k.Out); !ok || per != 5 {
		t.Errorf("PerRound(Out) = %d, %v; want 5, true (10 over 2 measured)", per, ok)
	}

	// No input at all: the cache share is unknown, and neither is a per-round
	// mean without a measured round.
	if _, ok := (TokenCounts{Write: 5, Out: 5}).CachePct(); ok {
		t.Error("CachePct with zero input = ok true, want false")
	}
	if _, ok := (TokenCounts{}).PerRound(10); ok {
		t.Error("PerRound with zero measured = ok true, want false")
	}
}

// TestScoreRowTokens pins that a ScoreRow's TokenKinds sums that candidate's
// rows only, and leaves the unrecorded bucket out.
func TestScoreRowTokens(t *testing.T) {
	rows := []db.RoundRow{
		{BuilderCandidate: stStr("a"), InTokens: stI64(100), CacheTokens: stI64(900), OutTokens: stI64(10)},
		{BuilderCandidate: stStr("a"), OutTokens: stI64(90)},
		{BuilderCandidate: stStr("b"), InTokens: stI64(7)},
		{BuilderCandidate: nil, InTokens: stI64(1000)},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})
	byTok := map[string]ScoreRow{}
	for _, s := range rep.Scorecard {
		byTok[s.Token] = s
	}

	a := byTok["a"].TokenKinds
	if a.In != 100 || a.Cache != 900 || a.Out != 100 || a.Measured != 2 || a.Total() != 1100 {
		t.Errorf("a TokenKinds = %+v, want In 100, Cache 900, Out 100, Measured 2, Total 1100", a)
	}
	b := byTok["b"].TokenKinds
	if b.In != 7 || b.Measured != 1 || b.Total() != 7 {
		t.Errorf("b TokenKinds = %+v, want In 7, Measured 1, Total 7", b)
	}
	if rep.Totals.TokenKinds.In != 1107 {
		t.Errorf("Totals In = %d, want 1107 (the unrecorded row's 1000 included)", rep.Totals.TokenKinds.In)
	}
}

// TestDayTokensBucketByLocalDay pins that a day's Tokens are bucketed by the
// same local day as its USD, and that a plan or unrecorded row still counts.
func TestDayTokensBucketByLocalDay(t *testing.T) {
	loc := time.FixedZone("X", 2*3600)
	rows := []db.RoundRow{
		// 23:30 UTC on the 23rd is 01:30 local on the 24th.
		{StartedAt: time.Date(2026, 9, 23, 23, 30, 0, 0, time.UTC), InTokens: stI64(100)},
		// 22:30 UTC on the 24th is 00:30 local on the 25th.
		{StartedAt: time.Date(2026, 9, 24, 22, 30, 0, 0, time.UTC), InTokens: stI64(7)},
		// 21:30 UTC on the 23rd is still the 23rd locally.
		{StartedAt: time.Date(2026, 9, 23, 21, 30, 0, 0, time.UTC), InTokens: stI64(3)},
	}
	rep := Build(Inputs{
		Rows:  rows,
		Since: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		Loc:   loc,
	})

	want := map[string]int64{"2026-09-23": 3, "2026-09-24": 100, "2026-09-25": 7}
	for _, d := range rep.Spend.Days {
		if got, ok := want[d.Day]; ok && d.Tokens != got {
			t.Errorf("%s Tokens = %d, want %d", d.Day, d.Tokens, got)
		}
	}
	if got := rep.Spend.Days[0].Tokens; got != 0 {
		t.Errorf("the zero day's Tokens = %d, want 0", got)
	}
}

// TestRepoTokens pins that a GroupRow's Tokens sums the group's rows, for
// repos and for features.
func TestRepoTokens(t *testing.T) {
	rows := []db.RoundRow{
		{BindingID: "b1", Repo: stStr("A"), Feature: stStr("f1"), InTokens: stI64(10), OutTokens: stI64(5)},
		{BindingID: "b2", Repo: stStr("A"), Feature: stStr("f1"), CacheTokens: stI64(100)},
		{BindingID: "b3", Repo: stStr("B"), Feature: stStr("f2"), WriteTokens: stI64(2)},
		{BindingID: "b4"},
	}
	rep := Build(Inputs{Rows: rows, Until: stNow, Loc: time.UTC})

	byKey := map[string]GroupRow{}
	for _, g := range rep.Repos {
		byKey[g.Key] = g
	}
	if got := byKey["A"].Tokens; got != 115 {
		t.Errorf("repo A Tokens = %d, want 115", got)
	}
	if got := byKey["B"].Tokens; got != 2 {
		t.Errorf("repo B Tokens = %d, want 2", got)
	}
	if got := byKey["(none)"].Tokens; got != 0 {
		t.Errorf("(none) Tokens = %d, want 0", got)
	}

	byFeature := map[string]GroupRow{}
	for _, g := range rep.Features {
		byFeature[g.Key] = g
	}
	if got := byFeature["f1"].Tokens; got != 115 {
		t.Errorf("feature f1 Tokens = %d, want 115", got)
	}
	if got := byFeature["f2"].Tokens; got != 2 {
		t.Errorf("feature f2 Tokens = %d, want 2", got)
	}
}
