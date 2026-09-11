package relay

import (
	"strings"
	"testing"
	"time"

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

	got := FormatPolicy(set, pol, gates)

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

	got := FormatPolicy(set, policy.Policy{}, nil)

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

	got := FormatPolicy(set, pol, gates)

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

	got := FormatPolicy(set, orderOf("builder", testAgyRef), nil)

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

	got := FormatPolicy(set, pol, nil)

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
