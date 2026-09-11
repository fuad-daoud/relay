package relay

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/history"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/policy"
)

func TestPolicyWarningsNoneWhenConsistent(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	got := PolicyWarnings(set, pol)
	if len(got) != 0 {
		t.Fatalf("PolicyWarnings = %+v, want empty", got)
	}
}

func TestPolicyWarningsNoneWhenNoOrder(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)

	got := PolicyWarnings(set, policy.Policy{})
	if len(got) != 0 {
		t.Fatalf("PolicyWarnings = %+v, want empty", got)
	}
}

func TestPolicyWarningsMatrix(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := policy.Policy{Order: map[string][]string{
		"builder":  {testAgyRef, "claude/test/nope"},
		"reviewer": {testAgyRef},
	}}

	want := []PolicyWarning{
		{Role: "builder", Index: 1, Token: "claude/test/nope", Text: `order.builder[1] "claude/test/nope" is not a configured candidate`},
		{Role: "builder", Index: -1, Token: testClaudeRef, Text: `builder: claude/test/m serves the role but is not in order.builder`},
		{Role: "builder", Index: -1, Token: testOpencodeRef, Text: `builder: opencode/test/m serves the role but is not in order.builder`},
		{Role: "reviewer", Index: 0, Token: testAgyRef, Text: `order.reviewer[0] "agy/test/m" does not serve reviewer (its roles: [builder])`},
		{Role: "reviewer", Index: -1, Token: testClaudeRef, Text: `reviewer: claude/test/m serves the role but is not in order.reviewer`},
	}

	got := PolicyWarnings(set, pol)
	if len(got) != len(want) {
		t.Fatalf("PolicyWarnings = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("warning[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFormatPolicyOrderWithGatedFirst(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	until := baseTime.Add(10 * time.Minute)
	gates := []ledger.Gate{{Token: testAgyRef, Kind: ledger.SpawnFailed, Until: until}}

	got := FormatPolicy(set, pol, gates, history.History{}, baseTime, time.UTC)

	want := strings.Join([]string{
		"builder  (order set in ~/.config/relay/policy.json)",
		"  1  agy/test/m       order     spawn failed " + GateUntilText(until),
		"  2  claude/test/m    order     <- would pick",
		"  3  opencode/test/m  unlisted",
		"reviewer  (no order set)",
		"  1  claude/test/m    sole      <- would pick",
		"researcher  (no order set)",
		"  no candidate serves this role",
		"",
		"warnings",
		"  builder: opencode/test/m serves the role but is not in order.builder",
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPolicyNoOrderTwoServeRefuses(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)

	got := FormatPolicy(set, policy.Policy{}, nil, history.History{}, baseTime, time.UTC)

	want := strings.Join([]string{
		"builder  (no order set)",
		"  1  agy/test/m",
		"  2  claude/test/m",
		"  3  opencode/test/m",
		"  would refuse: 3 candidates serve builder and no order is set",
		"reviewer  (no order set)",
		"  1  claude/test/m    sole      <- would pick",
		"researcher  (no order set)",
		"  no candidate serves this role",
		`no policy configured; write ~/.config/relay/policy.json (see README "Policy")`,
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPolicyAllGated(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)
	gates := []ledger.Gate{limit(testAgyRef), limit(testClaudeRef), limit(testOpencodeRef)}

	got := FormatPolicy(set, pol, gates, history.History{}, baseTime, time.UTC)

	want := strings.Join([]string{
		"builder  (order set in ~/.config/relay/policy.json)",
		"  1  agy/test/m       order     rate-limited until cleared",
		"  2  claude/test/m    order     rate-limited until cleared",
		"  3  opencode/test/m  order     rate-limited until cleared",
		"  would refuse: every candidate serving builder is gated",
		"reviewer  (no order set)",
		"  1  claude/test/m    sole      rate-limited until cleared",
		"  would refuse: every candidate serving reviewer is gated",
		"researcher  (no order set)",
		"  no candidate serves this role",
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPolicyEmptySet(t *testing.T) {
	set := candidateSet(t, "[]")

	got := FormatPolicy(set, orderOf("builder", testAgyRef), nil, history.History{}, baseTime, time.UTC)

	want := "no candidates configured; write ~/.config/relay/candidates.json (see README \"Candidates\")\n"
	if got != want {
		t.Errorf("FormatPolicy = %q, want %q", got, want)
	}
}

// testClaudeOnlyJSON has only the claude candidate, serving both builder
// and reviewer, so a role with an order set still resolves to a sole pick.
const testClaudeOnlyJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]}
]`

func TestFormatPolicySoleWithOrder(t *testing.T) {
	set := candidateSet(t, testClaudeOnlyJSON)
	pol := orderOf("reviewer", testClaudeRef)

	got := FormatPolicy(set, pol, nil, history.History{}, baseTime, time.UTC)

	want := strings.Join([]string{
		"builder  (no order set)",
		"  1  claude/test/m  sole      <- would pick",
		"reviewer  (order set in ~/.config/relay/policy.json)",
		"  1  claude/test/m  sole      <- would pick",
		"researcher  (no order set)",
		"  no candidate serves this role",
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

// wantHourRuler renders formatHistory's hour ruler line, matching its exact
// column widths, so tests build the expected history block from counts
// rather than hand-counted spacing.
func wantHourRuler() string {
	var labels [24]string
	for h := range labels {
		labels[h] = fmt.Sprintf("%02d", h)
	}
	return strings.Repeat(" ", 27) + strings.Join(labels[:], " ")
}

// wantHourRow renders one formatHistory data row from a provider, kind and
// a [24]int of hour counts, matching formatHistory's exact cell format, so
// tests state counts, not spacing.
func wantHourRow(provider string, kind ledger.Kind, counts [24]int) string {
	row := fmt.Sprintf("  %-10s %-13s", provider, GateKindText(kind))
	for _, n := range counts {
		cell := "."
		if n != 0 {
			cell = strconv.Itoa(n)
		}
		row += fmt.Sprintf(" %2s", cell)
	}
	return row
}

func TestFormatPolicyPeakColumn(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	now := time.Date(2026, 9, 11, 21, 15, 0, 0, time.UTC)

	hist := history.History{Events: []history.Event{
		{At: time.Date(2026, 9, 11, 20, 10, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 11, 22, 5, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 11, 21, 0, 0, 0, time.UTC).AddDate(0, 0, -31), Kind: ledger.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 11, 21, 30, 0, 0, time.UTC), Kind: ledger.SpawnFailed, Provider: "test"},
	}}.Prune(now)

	got := FormatPolicy(set, pol, nil, hist, now, time.UTC)

	var rlCounts, sfCounts [24]int
	rlCounts[20], rlCounts[21], rlCounts[22] = 1, 1, 1
	sfCounts[21] = 1

	want := strings.Join([]string{
		"builder  (order set in ~/.config/relay/policy.json)",
		"  1  agy/test/m       order     limited 3x around 21:00 (30d)  <- would pick",
		"  2  claude/test/m    order     limited 3x around 21:00 (30d)",
		"  3  opencode/test/m  unlisted  limited 3x around 21:00 (30d)",
		"reviewer  (no order set)",
		"  1  claude/test/m    sole      limited 3x around 21:00 (30d)  <- would pick",
		"researcher  (no order set)",
		"  no candidate serves this role",
		"",
		"warnings",
		"  builder: opencode/test/m serves the role but is not in order.builder",
		"",
		"history (30d, local hours)",
		wantHourRuler(),
		wantHourRow("test", ledger.RateLimited, rlCounts),
		wantHourRow("test", ledger.SpawnFailed, sfCounts),
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPolicyPeakWrapsMidnight(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	now := time.Date(2026, 9, 11, 23, 50, 0, 0, time.UTC)

	hist := history.History{Events: []history.Event{
		{At: time.Date(2026, 9, 11, 23, 30, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 12, 0, 20, 0, 0, time.UTC), Kind: ledger.RateLimited, Provider: "test"},
	}}.Prune(now)

	got := FormatPolicy(set, pol, nil, hist, now, time.UTC)

	want := "limited 2x around 23:00 (30d)"
	if !strings.Contains(got, want) {
		t.Errorf("FormatPolicy =\n%s\nwant a row containing %q", got, want)
	}
}

func TestFormatPolicyNoHistoryNoBlock(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)

	got := FormatPolicy(set, pol, nil, history.History{}, baseTime, time.UTC)

	if strings.Contains(got, "limited") {
		t.Errorf("FormatPolicy with empty history contains %q:\n%s", "limited", got)
	}
	if strings.Contains(got, "history (") {
		t.Errorf("FormatPolicy with empty history contains %q:\n%s", "history (", got)
	}
}

func TestFormatPolicyGateAndPeakOrder(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	now := time.Date(2026, 9, 11, 21, 0, 0, 0, time.UTC)
	until := now.Add(10 * time.Minute)

	gates := []ledger.Gate{{Token: testAgyRef, Kind: ledger.SpawnFailed, Until: until}}
	hist := history.History{Events: []history.Event{
		{At: now, Kind: ledger.RateLimited, Provider: "test"},
	}}

	got := FormatPolicy(set, pol, gates, hist, now, time.UTC)

	wantTail := "limited 1x around 21:00 (30d); spawn failed " + GateUntilText(until)
	if !strings.Contains(got, wantTail) {
		t.Errorf("FormatPolicy =\n%s\nwant a row containing %q", got, wantTail)
	}
}

func TestFormatPolicyRepeatedGateRendersOnce(t *testing.T) {
	// #93: `relay unavailable` three times without an `available` between
	// leaves three live ledger entries on one token. The row says it once.
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	gates := []ledger.Gate{
		{Token: testAgyRef, Kind: ledger.RateLimited},
		{Token: testAgyRef, Kind: ledger.RateLimited},
		{Token: testAgyRef, Kind: ledger.RateLimited},
	}

	got := FormatPolicy(set, pol, gates, history.History{}, baseTime, time.UTC)

	wantRow := "  1  agy/test/m       order     rate-limited until cleared\n"
	if !strings.Contains(got, wantRow) {
		t.Errorf("FormatPolicy =\n%s\nwant a row exactly %q", got, wantRow)
	}
	if strings.Contains(got, "until cleared; rate-limited") {
		t.Errorf("gate text repeated:\n%s", got)
	}
}

func TestAllGatedErrorNamesEachGateOnce(t *testing.T) {
	// The refusal text goes through the same renderer as the pick line.
	set := candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)
	gates := []ledger.Gate{
		{Token: testAgyRef, Kind: ledger.RateLimited},
		{Token: testAgyRef, Kind: ledger.RateLimited},
	}

	_, err := resolveCandidate(set, policy.Policy{}, gates, "", "builder")
	if !errors.Is(err, ErrAllGated) {
		t.Fatalf("got %v, want ErrAllGated", err)
	}
	if n := strings.Count(err.Error(), "rate-limited until cleared"); n != 1 {
		t.Errorf("gate text appears %d times in %q, want 1", n, err.Error())
	}
}
