// Package stats computes the cockpit's stats report -- a per-candidate
// scorecard, spend over time, reliability, repos and features, and outcomes --
// from the database's round rows, the availability history and the active
// gates. It is pure: it reads no disk, no clock and no environment. Every
// surface that shows stats (the `:stats` view and `relevo history --stats`,
// text and --json) shares this one report (cockpit spec §5, C2a plan §4.3).
package stats

import (
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/history"
	"github.com/fuad-daoud/relevo/internal/ledger"
)

// Inputs is everything Build reads. Rows are already windowed by the caller's
// query. Every optional field has a documented zero behaviour, so a nil func
// or a missing map is never a panic.
type Inputs struct {
	Rows    []db.RoundRow   // already windowed by the caller's query
	Landed  map[string]bool // binding IDs whose binding reached DONE
	History history.History // availability events (rate limits, spawn failures, clears)
	Gates   []ledger.Gate   // active now
	// TTFT maps a candidate token to its median time to first token in
	// milliseconds. nil means there is no ttft column value at all.
	TTFT func(token string) (ms int64, ok bool)
	// IsPlan reports whether a candidate token is a subscription lane. nil
	// means nothing is a plan.
	IsPlan func(token string) bool
	// Keep filters the scorecard's rows (the actor filter A4 adds). nil keeps
	// every row.
	Keep func(db.RoundRow) bool
	// Since is the report's window start; the zero time starts at the oldest
	// row.
	Since time.Time
	// Until is the "now" of the report: the window's open end.
	Until time.Time
	// Loc is the location days and hours are bucketed in; nil is time.Local.
	Loc *time.Location
}

// Report is one stats report, in the six parts the cockpit's four questions
// are answered from.
type Report struct {
	Since, Until time.Time
	Totals       Totals
	Scorecard    []ScoreRow
	Spend        Spend
	Reliability  Reliability
	Repos        []GroupRow // key = RoundRow.Repo, "(none)" when nil
	Features     []GroupRow // only rows with a Feature; empty when none
	Outcomes     Outcomes
}

// Totals is the report's headline numbers.
type Totals struct {
	Rounds, Bindings, Candidates int
	CostUSD                      float64 // known basis, non-plan
	PlanRounds                   int
	UnknownCost                  int // rounds with basis unknown or nil cost (non-plan)
	Tokens                       int64
	Halted                       int
	MedianMS                     int64 // closed rounds with DurationMS
}

// ScoreRow is one candidate's line on the scorecard.
type ScoreRow struct {
	Token            string
	Rounds, Closed   int
	Reported, Halted int
	DonePct, HaltPct float64 // of Closed; 0 when Closed == 0
	MedianMS         int64   // closed rounds with DurationMS; 0 when none
	TTFTMS           int64
	HasTTFT          bool
	Plan             bool
	CostPerRound     float64 // mean over rows with known cost; valid when HasCost
	HasCost          bool
	CommitsPerRound  float64 // mean over rows with Commits != nil; valid when HasCommits
	HasCommits       bool
	Few              bool // Rounds < 5
}

// Spend is the per-day cost series and the week-over-week totals.
type Spend struct {
	Days     []DayCost // one per local day Since..Until inclusive, oldest first; zero days included
	ThisWeek float64   // [Until-7d, Until)
	LastWeek float64   // [Until-14d, Until-7d)
}

// DayCost is one local day's known cost and its split by provider.
type DayCost struct {
	Day        string // YYYY-MM-DD in Loc
	USD        float64
	ByProvider map[string]float64 // BuilderProvider, "(none)" when nil
}

// Reliability is the switches, limits and gates part of the report.
type Reliability struct {
	Switches       int       // sum of Switches over rows
	RoundsSwitched int       // rows with Switches > 0
	SwitchPct      float64   // RoundsSwitched / Rounds * 100
	RateLimits     int       // history RateLimited events in [Since, Until)
	SpawnFailures  int       // history SpawnFailed events in [Since, Until)
	ByHour         []HourRow // one per provider with any rate limit in the window, sorted
	Active         []ledger.Gate
}

// HourRow is one provider's rate-limit events bucketed by local hour.
type HourRow struct {
	Provider string
	Counts   [24]int
}

// GroupRow is one repo or feature bucket.
type GroupRow struct {
	Key                    string
	Rounds, Halted, Landed int
	CostUSD                float64
	RoundsPerLand          float64
}

// Outcomes counts the rounds and the reports that ended them.
type Outcomes struct {
	ByRound  map[string]int // db outcome values: reported, halted, exited, switched, done_no_report, open
	ByReport map[string]int // done, halted, blocked, deferred, "no outcome" (= unstructured)
}

// closedOutcomes are the round outcomes a "closed" round is one of; the
// scorecard's DonePct and HaltPct are over these (C2a plan §4.3).
var closedOutcomes = map[string]bool{
	db.OutcomeReported:     true,
	db.OutcomeHalted:       true,
	db.OutcomeExited:       true,
	db.OutcomeSwitched:     true,
	db.OutcomeDoneNoReport: true,
}

// Build computes the report from its inputs. It never errors and never panics
// on a nil pointer: every nil field of a RoundRow is handled explicitly.
func Build(in Inputs) Report {
	loc := in.Loc
	if loc == nil {
		loc = time.Local
	}
	rows := in.Rows

	rep := Report{
		Since: in.Since,
		Until: in.Until,
	}
	if rep.Since.IsZero() {
		if len(rows) > 0 {
			rep.Since = oldest(rows)
		} else {
			rep.Since = in.Until
		}
	}

	rep.Totals = buildTotals(in, rows)
	rep.Scorecard = buildScorecard(in, rows)
	rep.Spend = buildSpend(in, rows, rep.Since, loc)
	rep.Reliability = buildReliability(in, rows, loc)
	rep.Repos = groupRows(in, rows, repoKey)
	rep.Features = groupRows(in, rows, featureKey)
	rep.Outcomes = buildOutcomes(rows)
	return rep
}

// buildTotals sums the headline numbers over every row.
func buildTotals(in Inputs, rows []db.RoundRow) Totals {
	var t Totals
	bindings := map[string]bool{}
	var durations []int64
	for _, r := range rows {
		t.Rounds++
		bindings[r.BindingID] = true
		t.Tokens += rowTokens(r)
		if r.Outcome == db.OutcomeHalted {
			t.Halted++
		}
		if closedOutcomes[r.Outcome] && r.DurationMS != nil {
			durations = append(durations, *r.DurationMS)
		}
		if r.BuilderCandidate == nil {
			// A nil candidate is the "(unrecorded)" bucket: it never gets a
			// scorecard row, and its rounds count here.
			t.Candidates++
			continue
		}
		if isPlan(in, r) {
			t.PlanRounds++
			continue
		}
		if costKnown(in, r) {
			t.CostUSD += *r.CostUSD
		} else {
			t.UnknownCost++
		}
	}
	t.Bindings = len(bindings)
	t.MedianMS = median(durations)
	return t
}

// buildScorecard groups the rows that carry a candidate and pass Keep by token,
// newest-heavy first.
func buildScorecard(in Inputs, rows []db.RoundRow) []ScoreRow {
	type acc struct {
		rounds, closed, reported, halted int
		durations                        []int64
		costSum                          float64
		costN                            int
		commitsSum                       float64
		commitsN                         int
	}
	accs := map[string]*acc{}
	var order []string
	for _, r := range rows {
		if r.BuilderCandidate == nil {
			continue
		}
		if in.Keep != nil && !in.Keep(r) {
			continue
		}
		tok := *r.BuilderCandidate
		a := accs[tok]
		if a == nil {
			a = &acc{}
			accs[tok] = a
			order = append(order, tok)
		}
		a.rounds++
		if closedOutcomes[r.Outcome] {
			a.closed++
			if r.DurationMS != nil {
				a.durations = append(a.durations, *r.DurationMS)
			}
		}
		switch r.Outcome {
		case db.OutcomeReported:
			a.reported++
		case db.OutcomeHalted:
			a.halted++
		}
		if costKnown(in, r) {
			a.costSum += *r.CostUSD
			a.costN++
		}
		if r.Commits != nil {
			a.commitsSum += float64(*r.Commits)
			a.commitsN++
		}
	}

	out := make([]ScoreRow, 0, len(order))
	for _, tok := range order {
		a := accs[tok]
		s := ScoreRow{
			Token:    tok,
			Rounds:   a.rounds,
			Closed:   a.closed,
			Reported: a.reported,
			Halted:   a.halted,
			MedianMS: median(a.durations),
			Few:      a.rounds < 5,
		}
		if a.closed > 0 {
			s.DonePct = 100 * float64(a.reported) / float64(a.closed)
			s.HaltPct = 100 * float64(a.halted) / float64(a.closed)
		}
		if in.TTFT != nil {
			if ms, ok := in.TTFT(tok); ok {
				s.TTFTMS, s.HasTTFT = ms, true
			}
		}
		s.Plan = in.IsPlan != nil && in.IsPlan(tok)
		if !s.Plan && a.costN > 0 {
			s.CostPerRound = a.costSum / float64(a.costN)
			s.HasCost = true
		}
		if a.commitsN > 0 {
			s.CommitsPerRound = a.commitsSum / float64(a.commitsN)
			s.HasCommits = true
		}
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rounds != out[j].Rounds {
			return out[i].Rounds > out[j].Rounds
		}
		return out[i].Token < out[j].Token
	})
	return out
}

// buildSpend builds one entry per local day from since to Until inclusive, the
// known cost per row's StartedAt day, and the two week windows.
func buildSpend(in Inputs, rows []db.RoundRow, since time.Time, loc *time.Location) Spend {
	var sp Spend

	if !in.Until.IsZero() {
		end := dayStart(in.Until.In(loc))
		cur := dayStart(since.In(loc))
		for !cur.After(end) {
			sp.Days = append(sp.Days, DayCost{
				Day:        cur.Format("2006-01-02"),
				ByProvider: map[string]float64{},
			})
			cur = cur.AddDate(0, 0, 1)
		}
	}

	idx := make(map[string]int, len(sp.Days))
	for i, d := range sp.Days {
		idx[d.Day] = i
	}

	for _, r := range rows {
		if !costKnown(in, r) {
			continue
		}
		usd := *r.CostUSD
		if i, ok := idx[r.StartedAt.In(loc).Format("2006-01-02")]; ok {
			sp.Days[i].USD += usd
			prov := "(none)"
			if r.BuilderProvider != nil {
				prov = *r.BuilderProvider
			}
			sp.Days[i].ByProvider[prov] += usd
		}
		if in.Until.IsZero() {
			continue
		}
		at := r.StartedAt
		week, fortnight := in.Until.Add(-7*24*time.Hour), in.Until.Add(-14*24*time.Hour)
		switch {
		case !at.Before(week) && at.Before(in.Until):
			sp.ThisWeek += usd
		case !at.Before(fortnight) && at.Before(week):
			sp.LastWeek += usd
		}
	}
	return sp
}

// buildReliability sums the rows' switches and the history's limits and spawn
// failures in the window, and passes the active gates through.
func buildReliability(in Inputs, rows []db.RoundRow, loc *time.Location) Reliability {
	rel := Reliability{Active: in.Gates}
	for _, r := range rows {
		rel.Switches += r.Switches
		if r.Switches > 0 {
			rel.RoundsSwitched++
		}
	}
	if len(rows) > 0 {
		rel.SwitchPct = 100 * float64(rel.RoundsSwitched) / float64(len(rows))
	}

	hours := map[string]*[24]int{}
	for _, e := range in.History.Events {
		if e.At.Before(in.Since) {
			continue
		}
		if !in.Until.IsZero() && !e.At.Before(in.Until) {
			continue
		}
		switch e.Kind {
		case ledger.RateLimited:
			rel.RateLimits++
			c := hours[e.Provider]
			if c == nil {
				c = &[24]int{}
				hours[e.Provider] = c
			}
			c[e.At.In(loc).Hour()]++
		case ledger.SpawnFailed:
			rel.SpawnFailures++
		}
	}

	providers := make([]string, 0, len(hours))
	for p := range hours {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		rel.ByHour = append(rel.ByHour, HourRow{Provider: p, Counts: *hours[p]})
	}
	return rel
}

// groupRows buckets rows by key, summing rounds, halts, known cost and the
// bindings that reached DONE. key's second result is false for a row the
// section does not cover (a feature-less row, for the feature section).
func groupRows(in Inputs, rows []db.RoundRow, key func(db.RoundRow) (string, bool)) []GroupRow {
	type acc struct {
		rounds, halted int
		landed         map[string]bool
		landedRounds   int
		cost           float64
	}
	accs := map[string]*acc{}
	var order []string
	for _, r := range rows {
		k, ok := key(r)
		if !ok {
			continue
		}
		a := accs[k]
		if a == nil {
			a = &acc{landed: map[string]bool{}}
			accs[k] = a
			order = append(order, k)
		}
		a.rounds++
		if r.Outcome == db.OutcomeHalted {
			a.halted++
		}
		if costKnown(in, r) {
			a.cost += *r.CostUSD
		}
		if in.Landed[r.BindingID] {
			a.landed[r.BindingID] = true
			a.landedRounds++
		}
	}

	out := make([]GroupRow, 0, len(order))
	for _, k := range order {
		a := accs[k]
		g := GroupRow{
			Key:     k,
			Rounds:  a.rounds,
			Halted:  a.halted,
			Landed:  len(a.landed),
			CostUSD: a.cost,
		}
		if g.Landed > 0 {
			g.RoundsPerLand = float64(a.landedRounds) / float64(g.Landed)
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rounds != out[j].Rounds {
			return out[i].Rounds > out[j].Rounds
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// repoKey buckets a row by its repo, "(none)" when it has none.
func repoKey(r db.RoundRow) (string, bool) {
	if r.Repo == nil {
		return "(none)", true
	}
	return *r.Repo, true
}

// featureKey buckets a row by its feature; a row with no feature is not in the
// section at all.
func featureKey(r db.RoundRow) (string, bool) {
	if r.Feature == nil {
		return "", false
	}
	return *r.Feature, true
}

// buildOutcomes counts the rounds by outcome and the reports by report outcome.
func buildOutcomes(rows []db.RoundRow) Outcomes {
	out := Outcomes{
		ByRound:  map[string]int{},
		ByReport: map[string]int{},
	}
	for _, r := range rows {
		out.ByRound[r.Outcome]++
		if r.ReportOutcome == nil {
			continue
		}
		v := *r.ReportOutcome
		if v == "unstructured" {
			v = "no outcome"
		}
		out.ByReport[v]++
	}
	return out
}

// isPlan reports whether a row ran on a plan candidate. A nil candidate is
// never a plan.
func isPlan(in Inputs, r db.RoundRow) bool {
	return r.BuilderCandidate != nil && in.IsPlan != nil && in.IsPlan(*r.BuilderCandidate)
}

// costKnown reports whether a row's cost is summed: a non-nil basis other than
// "unknown", a cost, and a non-plan candidate (C2a plan §4.3).
func costKnown(in Inputs, r db.RoundRow) bool {
	return r.CostBasis != nil && *r.CostBasis != "unknown" && r.CostUSD != nil && !isPlan(in, r)
}

// rowTokens sums a row's token columns; a nil column is zero.
func rowTokens(r db.RoundRow) int64 {
	var n int64
	for _, p := range []*int64{r.InTokens, r.CacheTokens, r.WriteTokens, r.OutTokens} {
		if p != nil {
			n += *p
		}
	}
	return n
}

// oldest is the earliest row's StartedAt.
func oldest(rows []db.RoundRow) time.Time {
	t := rows[0].StartedAt
	for _, r := range rows[1:] {
		if r.StartedAt.Before(t) {
			t = r.StartedAt
		}
	}
	return t
}

// dayStart truncates t to local midnight.
func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// median returns the median of xs, 0 when empty; an even count is the mean of
// the two middle values. It is histq's unexported median, copied here so the
// two stat surfaces agree (C2a plan §4.3).
func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]int64(nil), xs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
