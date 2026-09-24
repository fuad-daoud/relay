package stats

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Render is the report as text: the six sections the cockpit's stats view and
// `relevo history --stats` share (C2a plan §4.4). name maps a candidate token
// to its display name; pass the identity until A1 lands.
func Render(r Report, name func(token string) string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("relevo stats · %s → %s · %d rounds in %d bindings · %s + %d on plan · %s tok · median %s\n",
		r.Since.Format("2006-01-02"), r.Until.Format("2006-01-02"),
		r.Totals.Rounds, r.Totals.Bindings, money(r.Totals.CostUSD), r.Totals.PlanRounds,
		usage.ShortTokens(r.Totals.Tokens), duration(r.Totals.MedianMS)))
	if r.Totals.Rounds == 0 {
		sb.WriteString("no rounds in this window\n")
		return sb.String()
	}

	sb.WriteString("\n")
	renderScorecard(&sb, r, name)
	sb.WriteString("\n")
	renderSpend(&sb, r)
	sb.WriteString("\n")
	renderReliability(&sb, r)
	sb.WriteString("\n")
	renderGroups(&sb, "repos", r.Repos)
	if len(r.Features) > 0 {
		renderGroups(&sb, "features", r.Features)
	}
	sb.WriteString("\n")
	renderOutcomes(&sb, r)
	return sb.String()
}

// scorecardHeader and rowHeader are the two tables' column widths, in one
// place so the labels stay over the values.
const (
	scorecardHeader = "%-33s%5s  %4s  %4s  %4s  %5s  %5s  %7s\n"
	scorecardRow    = "  %-31s%5d  %4s  %4s  %4s  %5s  %5s  %7s\n"
	groupHeader     = "%-33s%4s  %6s  %4s  %9s\n"
	groupRowFmt     = "  %-31s%4d  %6s  %4d  %9s\n"
)

// renderScorecard writes the per-candidate scorecard and the unrecorded
// footnote.
func renderScorecard(sb *strings.Builder, r Report, name func(string) string) {
	sb.WriteString(fmt.Sprintf(scorecardHeader,
		"candidates", "RNDS", "DONE", "HALT", "MED", "TTFT", "$/RND", "COMMITS"))
	for _, s := range r.Scorecard {
		label := name(s.Token)
		if s.Few {
			label += " *"
		}
		sb.WriteString(fmt.Sprintf(scorecardRow,
			label, s.Rounds,
			pctText(s.DonePct, s.Closed),
			pctText(s.HaltPct, s.Closed),
			medianText(s),
			ttftText(s),
			costPerRoundText(s),
			commitsText(s)))
	}
	sb.WriteString(fmt.Sprintf("  (* fewer than 5 rounds)%9sunrecorded: %d rounds\n", "", r.Totals.Unrecorded))
}

// renderSpend writes the day sparkline and the week-over-week line.
func renderSpend(sb *strings.Builder, r Report) {
	sb.WriteString("spend per day (known cost; plan rounds excluded)\n")
	line := "  " + sparkline(r.Spend.Days)
	if n := len(r.Spend.Days); n > 0 {
		line += "   " + monthDay(r.Spend.Days[0].Day) + " … " + monthDay(r.Spend.Days[n-1].Day)
	}
	sb.WriteString(line + "\n")
	sb.WriteString(fmt.Sprintf("  this week %s · last week %s · %s\n",
		money(r.Spend.ThisWeek), money(r.Spend.LastWeek), weekDelta(r.Spend)))
}

// renderReliability writes the switch line, the by-hour limit table and the
// active gates.
func renderReliability(sb *strings.Builder, r Report) {
	rel := r.Reliability
	sb.WriteString("reliability\n")
	sb.WriteString(fmt.Sprintf("  switches %d in %d rounds (%.0f%%) · rate limits %d · spawn failures %d\n",
		rel.Switches, rel.RoundsSwitched, rel.SwitchPct, rel.RateLimits, rel.SpawnFailures))

	header := "  limits by local hour"
	for h := 0; h < 24; h++ {
		header += fmt.Sprintf(" %02d", h)
	}
	sb.WriteString(header + "\n")
	for _, row := range rel.ByHour {
		sb.WriteString(fmt.Sprintf("    %-19s", row.Provider))
		for h := 0; h < 24; h++ {
			cell := "."
			if row.Counts[h] > 0 {
				cell = strconv.Itoa(row.Counts[h])
			}
			sb.WriteString(fmt.Sprintf(" %2s", cell))
		}
		sb.WriteString("\n")
	}

	if len(rel.Active) == 0 {
		sb.WriteString("  active  none\n")
		return
	}
	parts := make([]string, 0, len(rel.Active))
	for _, g := range rel.Active {
		parts = append(parts, fmt.Sprintf("%s %s until %s",
			g.Token, strings.ReplaceAll(string(g.Kind), "_", "-"), untilText(g.Until)))
	}
	sb.WriteString("  active  " + strings.Join(parts, " · ") + "\n")
}

// renderGroups writes one repos- or features-shaped section: its header and
// its rows, Rounds desc then key.
func renderGroups(sb *strings.Builder, label string, rows []GroupRow) {
	sb.WriteString(fmt.Sprintf(groupHeader, label, "RNDS", "COST", "HALT", "RNDS/LAND"))
	for _, g := range rows {
		sb.WriteString(fmt.Sprintf(groupRowFmt,
			g.Key, g.Rounds, money(g.CostUSD), g.Halted, roundsPerLandText(g)))
	}
}

// outcomeCountKeys are the outcome counts, in the order the report prints them;
// done_no_report reads "no report".
var outcomeCountKeys = []struct{ key, label string }{
	{db.OutcomeReported, "reported"},
	{db.OutcomeHalted, "halted"},
	{db.OutcomeSwitched, "switched"},
	{db.OutcomeExited, "exited"},
	{db.OutcomeDoneNoReport, "no report"},
	{db.OutcomeOpen, "open"},
}

// reportCountKeys are the report outcomes in print order.
var reportCountKeys = []struct{ key, label string }{
	{"done", "done"},
	{"halted", "halted"},
	{"blocked", "blocked"},
	{"deferred", "deferred"},
	{"no outcome", "no outcome"},
}

// renderOutcomes writes the two rounds/reports count lines.
func renderOutcomes(sb *strings.Builder, r Report) {
	sb.WriteString("outcomes\n")
	sb.WriteString("  rounds   " + countPairs(outcomeCountKeys, r.Outcomes.ByRound) + "\n")
	sb.WriteString("  reports  " + countPairs(reportCountKeys, r.Outcomes.ByReport) + "\n")
}

// countPairs renders "label n" for every key, joined by " · ".
func countPairs(keys []struct{ key, label string }, counts map[string]int) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k.label, counts[k.key]))
	}
	return strings.Join(parts, " · ")
}

// pctText is a percentage of the closed rounds, or "-" when none is closed.
func pctText(pct float64, closed int) string {
	if closed == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", pct)
}

// medianText is the median duration, or "-" when no closed round carried a
// duration.
func medianText(s ScoreRow) string {
	if s.Closed == 0 || !s.HasMedian {
		return "-"
	}
	return duration(s.MedianMS)
}

// roundsPerLandText is a group's rounds per landed binding, or "-" when
// nothing landed.
func roundsPerLandText(g GroupRow) string {
	if g.Landed == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", g.RoundsPerLand)
}

// ttftText is the time to first token, or "-" when there is none.
func ttftText(s ScoreRow) string {
	if !s.HasTTFT {
		return "-"
	}
	return fmt.Sprintf("%.1fs", float64(s.TTFTMS)/1000)
}

// costPerRoundText is "plan" for a plan lane, "-" when no cost is known, and
// the mean otherwise.
func costPerRoundText(s ScoreRow) string {
	switch {
	case s.Plan:
		return "plan"
	case !s.HasCost:
		return "-"
	default:
		return money(s.CostPerRound)
	}
}

// commitsText is the mean commits per round, or "-" when none is recorded.
func commitsText(s ScoreRow) string {
	if !s.HasCommits {
		return "-"
	}
	return fmt.Sprintf("%.1f", s.CommitsPerRound)
}

// money renders dollars: "$%.2f", and "<$0.01" for anything between 0 and 0.01.
func money(v float64) string {
	if v > 0 && v < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", v)
}

// duration renders milliseconds as minutes, or "%dh%02dm" at an hour and over.
func duration(ms int64) string {
	hour := int64(time.Hour / time.Millisecond)
	minute := int64(time.Minute / time.Millisecond)
	if ms >= hour {
		return fmt.Sprintf("%dh%02dm", ms/hour, (ms%hour)/minute)
	}
	return fmt.Sprintf("%dm", ms/minute)
}

// sparkRunes are the sparkline's levels; index 0 is a zero day.
var sparkRunes = []rune(" ▁▂▃▄▅▆▇█")

// sparkline is one rune per day, scaled to the window's maximum, with a space
// for a zero day.
func sparkline(days []DayCost) string {
	var max float64
	for _, d := range days {
		if d.USD > max {
			max = d.USD
		}
	}
	out := make([]rune, 0, len(days))
	for _, d := range days {
		if d.USD <= 0 || max <= 0 {
			out = append(out, sparkRunes[0])
			continue
		}
		out = append(out, sparkRunes[1+int(math.Round(7*d.USD/max))])
	}
	return string(out)
}

// weekDelta is the week-over-week change, "new" when last week was zero.
func weekDelta(sp Spend) string {
	if sp.LastWeek == 0 {
		return "new"
	}
	return fmt.Sprintf("%+d%%", int(math.Round(100*(sp.ThisWeek-sp.LastWeek)/sp.LastWeek)))
}

// monthDay trims a YYYY-MM-DD day to MM-DD.
func monthDay(day string) string {
	if len(day) >= len("2006-01-02") {
		return day[5:]
	}
	return day
}

// untilText is a gate's expiry, "cleared" when it has none.
func untilText(t time.Time) string {
	if t.IsZero() {
		return "cleared"
	}
	return t.Format("Jan 2 15:04")
}
