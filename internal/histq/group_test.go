package histq

import (
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/fuad-daoud/relay/internal/db"
)

// TestGroupByBuilderSums asserts every counter and sum of the builder axis
// against the hand-checkable fixture: A = rows 1, 2, 7, 10; B = rows 3, 4, 9;
// C = rows 5, 6, 8.
func TestGroupByBuilderSums(t *testing.T) {
	groups := Group(fixtureRows(), AxisBuilder, fxLoc)
	if len(groups) != 3 {
		t.Fatalf("len(Group) = %d, want 3", len(groups))
	}

	want := []struct {
		key       string
		rounds    int
		reported  int
		halted    int
		exited    int
		switched  int
		doneNoRep int
		open      int
		commits   int
		tokens    int64
		cost      float64
		unknown   int
		lastDay   int
		lastHour  int
		numbers   []int
	}{
		{fxBuilderAgy, 4, 4, 0, 0, 0, 0, 0, 7, 6750, 7.75, 0, 20, 9, []int{1, 2, 4, 1}},
		{fxBuilderClaude, 3, 2, 1, 0, 0, 0, 0, 8, 5000, 4.00, 1, 20, 13, []int{3, 3, 1}},
		{fxBuilderOpencode, 3, 0, 1, 1, 0, 0, 1, 3, 4500, 0.25, 2, 20, 12, []int{2, 2, 3}},
	}
	for i, w := range want {
		g := groups[i]
		if g.Key != w.key {
			t.Errorf("groups[%d].Key = %q, want %q", i, g.Key, w.key)
			continue
		}
		if g.Rounds != w.rounds || g.Reported != w.reported || g.Halted != w.halted ||
			g.Exited != w.exited || g.Switched != w.switched ||
			g.DoneNoReport != w.doneNoRep || g.Open != w.open {
			t.Errorf("%s counters = rounds %d reported %d halted %d exited %d switched %d done_no_report %d open %d, want %d/%d/%d/%d/%d/%d/%d",
				g.Key, g.Rounds, g.Reported, g.Halted, g.Exited, g.Switched, g.DoneNoReport, g.Open,
				w.rounds, w.reported, w.halted, w.exited, w.switched, w.doneNoRep, w.open)
		}
		if g.Commits != w.commits {
			t.Errorf("%s Commits = %d, want %d", g.Key, g.Commits, w.commits)
		}
		if g.Tokens != w.tokens {
			t.Errorf("%s Tokens = %d, want %d", g.Key, g.Tokens, w.tokens)
		}
		if math.Abs(g.CostUSD-w.cost) > 1e-9 {
			t.Errorf("%s CostUSD = %v, want %v", g.Key, g.CostUSD, w.cost)
		}
		if g.Unknown != w.unknown {
			t.Errorf("%s Unknown = %d, want %d", g.Key, g.Unknown, w.unknown)
		}
		if wantLast := fxDay(w.lastDay, w.lastHour); !g.Last.Equal(wantLast) {
			t.Errorf("%s Last = %v, want %v", g.Key, g.Last, wantLast)
		}
		var numbers []int
		for _, r := range g.Rows {
			numbers = append(numbers, r.Number)
		}
		if fmt.Sprint(numbers) != fmt.Sprint(w.numbers) {
			t.Errorf("%s Rows numbers = %v, want %v (newest first)", g.Key, numbers, w.numbers)
		}
	}
}

// TestGroupCostSkipsUnknown pins that CostUSD sums only rows whose basis is
// not "unknown": B keeps 0.50 + 3.50 = 4.00 and skips row 3's 3.00; C keeps
// row 6's 0.25 and skips row 8's 1.50. Summing the unknown rows too would
// make B 7.00 and C 1.75, so this test fails on that mutation.
func TestGroupCostSkipsUnknown(t *testing.T) {
	groups := Group(fixtureRows(), AxisBuilder, fxLoc)
	if len(groups) != 3 {
		t.Fatalf("len(Group) = %d, want 3", len(groups))
	}
	if got := groups[1].CostUSD; math.Abs(got-4.00) > 1e-9 {
		t.Errorf("%s CostUSD = %v, want 4.00 (row 3's unknown basis skipped)", groups[1].Key, got)
	}
	if got := groups[2].CostUSD; math.Abs(got-0.25) > 1e-9 {
		t.Errorf("%s CostUSD = %v, want 0.25 (rows 5 and 8 skipped)", groups[2].Key, got)
	}
	if groups[1].Unknown != 1 || groups[2].Unknown != 2 {
		t.Errorf("Unknown = %d/%d, want 1/2", groups[1].Unknown, groups[2].Unknown)
	}
}

// TestGroupByDayNewestFirst pins the day axis: keys are the local date, and
// they come out newest first regardless of the cost order.
func TestGroupByDayNewestFirst(t *testing.T) {
	groups := Group(fixtureRows(), AxisDay, fxLoc)
	if len(groups) != 2 {
		t.Fatalf("len(Group) = %d, want 2", len(groups))
	}
	if groups[0].Key != "2026-09-20" || groups[1].Key != "2026-09-19" {
		t.Fatalf("day keys = [%s %s], want [2026-09-20 2026-09-19]", groups[0].Key, groups[1].Key)
	}
	if groups[0].Rounds != 6 || groups[1].Rounds != 4 {
		t.Errorf("day rounds = %d/%d, want 6/4", groups[0].Rounds, groups[1].Rounds)
	}
	if math.Abs(groups[0].CostUSD-6.50) > 1e-9 || math.Abs(groups[1].CostUSD-5.50) > 1e-9 {
		t.Errorf("day costs = %v/%v, want 6.50/5.50", groups[0].CostUSD, groups[1].CostUSD)
	}
}

// TestGroupOrderCostThenRoundsThenKey pins the ordering rule on a small
// fixture built for it: cost desc, then rounds desc, then key asc.
func TestGroupOrderCostThenRoundsThenKey(t *testing.T) {
	rows := []db.RoundRow{
		{BindingName: "X", CostUSD: fxFloat(1.00), CostBasis: fxStr("measured")},
		{BindingName: "Y", CostUSD: fxFloat(2.00), CostBasis: fxStr("measured")},
		{BindingName: "Z", CostUSD: fxFloat(1.00), CostBasis: fxStr("measured")},
		{BindingName: "W", CostUSD: fxFloat(0.50), CostBasis: fxStr("measured")},
		{BindingName: "W", CostUSD: fxFloat(0.50), CostBasis: fxStr("measured")},
	}
	groups := Group(rows, AxisBinding, fxLoc)
	var keys []string
	for _, g := range groups {
		keys = append(keys, g.Key)
	}
	if fmt.Sprint(keys) != fmt.Sprint([]string{"Y", "W", "X", "Z"}) {
		t.Errorf("order = %v, want [Y W X Z] (cost desc, rounds desc, key asc)", keys)
	}
}

// TestGroupNoneIsNil pins that no axis means no grouping at all.
func TestGroupNoneIsNil(t *testing.T) {
	if got := Group(fixtureRows(), AxisNone, fxLoc); got != nil {
		t.Errorf("Group(AxisNone) = %v, want nil", got)
	}
	if got := Group(fixtureRows(), Axis(""), fxLoc); got != nil {
		t.Errorf("Group(\"\") = %v, want nil", got)
	}
}

func TestGroupByBindingKeys(t *testing.T) {
	assertKeys(t, AxisBinding, []string{"api", "infra", "web"})
}

func TestGroupByRepoKeys(t *testing.T) {
	assertKeys(t, AxisRepo, []string{fxRepoAPI, fxRepoWeb})
}

func TestGroupByFeatureKeys(t *testing.T) {
	assertKeys(t, AxisFeature, []string{"checkout", "search"})
}

func TestGroupByHarnessKeys(t *testing.T) {
	assertKeys(t, AxisHarness, []string{"agy", "claude", "opencode"})
}

func TestGroupByProviderKeys(t *testing.T) {
	assertKeys(t, AxisProvider, []string{"antigravity", "anthropic", "openai"})
}

func TestGroupByModelKeys(t *testing.T) {
	assertKeys(t, AxisModel, []string{"gpt", "opus", "sonnet"})
}

func TestGroupByOutcomeKeys(t *testing.T) {
	assertKeys(t, AxisOutcome, []string{"exited", "halted", "open", "reported"})
}

// assertKeys pins the key set of one axis, order-insensitively: the ordering
// rules have their own tests.
func assertKeys(t *testing.T, by Axis, want []string) {
	t.Helper()
	groups := Group(fixtureRows(), by, fxLoc)
	got := make([]string, len(groups))
	for i, g := range groups {
		got[i] = g.Key
	}
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if fmt.Sprint(got) != fmt.Sprint(sorted) {
		t.Errorf("Group(%s) keys = %v, want %v", by, got, sorted)
	}
}

// TestTotalsCounts pins every Tiles field against the fixture: 10 rounds,
// $12.00 of known cost, three unknown rows (3, 5 and 8), 16250 tokens, two
// halted and one exited, three bindings and three builders, and the median
// duration over the nine rows that have one.
func TestTotalsCounts(t *testing.T) {
	got := Totals(fixtureRows())
	want := Tiles{
		Rounds:           10,
		CostUSD:          12.00,
		Unknown:          3,
		Tokens:           16250,
		Halted:           2,
		Exited:           1,
		MedianDurationMS: 3_000_000, // 50 minutes, the middle of 10..90
		Bindings:         3,
		Builders:         3,
	}
	if got.Rounds != want.Rounds || got.Unknown != want.Unknown || got.Tokens != want.Tokens ||
		got.Halted != want.Halted || got.Exited != want.Exited ||
		got.MedianDurationMS != want.MedianDurationMS || got.Bindings != want.Bindings ||
		got.Builders != want.Builders {
		t.Errorf("Totals = %+v, want %+v", got, want)
	}
	if math.Abs(got.CostUSD-want.CostUSD) > 1e-9 {
		t.Errorf("Totals.CostUSD = %v, want %v", got.CostUSD, want.CostUSD)
	}
}

// TestTotalsMedianOddEven pins the median rule over Totals: 0 when no row
// has a duration, the middle value for an odd count, the mean of the two
// middles for an even one.
func TestTotalsMedianOddEven(t *testing.T) {
	rows := func(ms ...int64) []db.RoundRow {
		out := make([]db.RoundRow, len(ms))
		for i, v := range ms {
			out[i] = db.RoundRow{BindingID: fmt.Sprintf("b%d", i), DurationMS: fxInt64(v)}
		}
		return out
	}
	cases := []struct {
		name string
		ms   []int64
		want int64
	}{
		{"none", nil, 0},
		{"odd", []int64{600_000}, 600_000},
		{"even sorted", []int64{600_000, 1_200_000}, 900_000},
		{"even unsorted", []int64{1_200_000, 600_000}, 900_000},
		{"odd many", []int64{5, 1, 3}, 3},
		{"even many", []int64{2_400_000, 600_000, 1_800_000, 1_200_000}, 1_500_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Totals(rows(c.ms...)).MedianDurationMS; got != c.want {
				t.Errorf("MedianDurationMS = %d, want %d", got, c.want)
			}
		})
	}
}
