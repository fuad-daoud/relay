package relay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/policy"
)

const (
	testOpencodeRef = "opencode/test/m"
	testClaudeRef   = "claude/test/m"
	testAgyRef      = "agy/test/m"
)

// testCandidatesJSON mirrors the shape of the three aliases DefaultTable
// used to ship, plus a reviewer on claude, so migrated tests keep their
// meaning: opencode and agy serve builder only; claude serves both.
const testCandidatesJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}
]`

// testTwoProviderJSON has builders on two providers, so a rate limit on
// one leaves the other ungated.
const testTwoProviderJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"other","model":"m","roles":["builder"]}
]`

// testTwoReviewerJSON: claude and opencode serve reviewer; agy does not.
const testTwoReviewerJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"]}
]`

// candidateSet loads a candidate set from a JSON body, for tests that need
// a specific configuration without a file in the repo.
func candidateSet(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write candidate set fixture: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("load candidate set fixture: %v", err)
	}
	return set
}

// orderOf builds a Policy ordering role by toks, for tests that need one
// role's order without a policy.json file on disk.
func orderOf(role string, toks ...string) policy.Policy {
	return policy.Policy{Order: map[string][]string{role: toks}}
}

// spawn is a SpawnFailed gate on token, for tests.
func spawn(token string) ledger.Gate {
	return ledger.Gate{Token: token, Kind: ledger.SpawnFailed, Until: baseTime.Add(10 * time.Minute)}
}

// limit is a RateLimited gate on token with no Until, for tests.
func limit(token string) ledger.Gate {
	return ledger.Gate{Token: token, Kind: ledger.RateLimited}
}

func TestResolveCandidate(t *testing.T) {
	tests := []struct {
		name            string
		setBody         string
		pol             policy.Policy
		gates           []ledger.Gate
		token           string
		role            string
		wantRef         string
		wantHow         How
		wantPosition    int
		wantSkipped     []string
		wantErr         error
		messageContains []string
	}{
		{
			name:    "named, serves",
			setBody: testCandidatesJSON,
			token:   "claude/test/m",
			role:    "builder",
			wantRef: "claude/test/m",
			wantHow: HowExplicit,
		},
		{
			name:    "named, serves consult",
			setBody: testCandidatesJSON,
			token:   "claude/test/m",
			role:    "reviewer",
			wantRef: "claude/test/m",
			wantHow: HowExplicit,
		},
		{
			name:            "named, does not serve",
			setBody:         testCandidatesJSON,
			token:           "agy/test/m",
			role:            "reviewer",
			wantErr:         ErrRoleNotServed,
			messageContains: []string{`does not serve role "reviewer"`, "[builder]"},
		},
		{
			name:            "named, unknown",
			setBody:         testCandidatesJSON,
			token:           "claude/test/opus",
			role:            "builder",
			wantErr:         candidate.ErrUnknownCandidate,
			messageContains: []string{"configured:"},
		},
		{
			name:            "named, malformed",
			setBody:         testCandidatesJSON,
			token:           "claude/test",
			role:            "builder",
			wantErr:         candidate.ErrBadRef,
			messageContains: []string{"harness/provider/model"},
		},
		{
			name:            "empty set, no token",
			setBody:         "[]",
			token:           "",
			role:            "builder",
			wantErr:         ErrNoCandidates,
			messageContains: []string{"candidates.json"},
		},
		{
			name:            "none serve, no token",
			setBody:         testCandidatesJSON,
			token:           "",
			role:            "researcher",
			wantErr:         ErrRoleNotServed,
			messageContains: []string{`no configured candidate serves role "researcher"`},
		},
		{
			name:    "exactly one, no token",
			setBody: testCandidatesJSON,
			token:   "",
			role:    "reviewer",
			wantRef: "claude/test/m",
			wantHow: HowSole,
		},
		{
			name:            "ambiguous, no token",
			setBody:         testCandidatesJSON,
			token:           "",
			role:            "builder",
			wantErr:         ErrAmbiguousCandidate,
			messageContains: []string{`3 candidates serve "builder"`, "name one with", "policy.json"},
		},
		{
			name:         "order, first ungated",
			setBody:      testCandidatesJSON,
			pol:          orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef),
			token:        "",
			role:         "builder",
			wantRef:      testAgyRef,
			wantHow:      HowOrder,
			wantPosition: 1,
		},
		{
			name:         "order, first gated",
			setBody:      testCandidatesJSON,
			pol:          orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef),
			gates:        []ledger.Gate{spawn(testAgyRef)},
			token:        "",
			role:         "builder",
			wantRef:      testClaudeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
			wantSkipped:  []string{testAgyRef},
		},
		{
			name:        "order, two gated, unlisted wins",
			setBody:     testCandidatesJSON,
			pol:         orderOf("builder", testAgyRef, testClaudeRef),
			gates:       []ledger.Gate{spawn(testAgyRef), spawn(testClaudeRef)},
			token:       "",
			role:        "builder",
			wantRef:     testOpencodeRef,
			wantHow:     HowUnlisted,
			wantSkipped: []string{testAgyRef, testClaudeRef},
		},
		{
			name:    "order, all gated",
			setBody: testCandidatesJSON,
			pol:     orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef),
			gates:   []ledger.Gate{limit(testAgyRef), limit(testClaudeRef), limit(testOpencodeRef)},
			token:   "",
			role:    "builder",
			wantErr: ErrAllGated,
			messageContains: []string{
				`every candidate serving "builder" is gated`,
				"agy/test/m (rate-limited until cleared)",
				"--builder",
				"relay available",
			},
		},
		{
			name:         "order, unknown entry skipped",
			setBody:      testCandidatesJSON,
			pol:          orderOf("builder", "claude/test/nope", testOpencodeRef),
			token:        "",
			role:         "builder",
			wantRef:      testOpencodeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
		},
		{
			name:         "order, non-serving entry skipped",
			setBody:      testTwoReviewerJSON,
			pol:          orderOf("reviewer", testAgyRef, testOpencodeRef),
			token:        "",
			role:         "reviewer",
			wantRef:      testOpencodeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
		},
		{
			name:    "sole beats order",
			setBody: testCandidatesJSON,
			pol:     orderOf("reviewer", testClaudeRef),
			token:   "",
			role:    "reviewer",
			wantRef: testClaudeRef,
			wantHow: HowSole,
		},
		{
			name:         "order, two gates on one token",
			setBody:      testCandidatesJSON,
			pol:          orderOf("builder", testAgyRef, testClaudeRef),
			gates:        []ledger.Gate{spawn(testAgyRef), limit(testAgyRef)},
			token:        "",
			role:         "builder",
			wantRef:      testClaudeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
			wantSkipped:  []string{testAgyRef, testAgyRef},
		},
		{
			name:    "sole, gated",
			setBody: testCandidatesJSON,
			gates:   []ledger.Gate{limit(testClaudeRef)},
			token:   "",
			role:    "reviewer",
			wantErr: ErrAllGated,
		},
		{
			name:    "explicit, gated, proceeds",
			setBody: testCandidatesJSON,
			gates:   []ledger.Gate{limit(testAgyRef)},
			token:   testAgyRef,
			role:    "builder",
			wantRef: testAgyRef,
			wantHow: HowExplicit,
		},
		{
			name:    "no order, two serve, refuses",
			setBody: testTwoProviderJSON,
			token:   "",
			role:    "builder",
			wantErr: ErrAmbiguousCandidate,
		},
		{
			name:         "order with gate on other provider",
			setBody:      testTwoProviderJSON,
			pol:          orderOf("builder", "agy/other/m", testClaudeRef),
			gates:        []ledger.Gate{limit("agy/other/m")},
			token:        "",
			role:         "builder",
			wantRef:      testClaudeRef,
			wantHow:      HowOrder,
			wantPosition: 2,
			wantSkipped:  []string{"agy/other/m"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := candidateSet(t, tt.setBody)
			got, err := resolveCandidate(set, tt.pol, tt.gates, tt.token, tt.role)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolveCandidate() err = %v, want %v", err, tt.wantErr)
				}
				for _, sub := range tt.messageContains {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("resolveCandidate() error %q does not contain %q", err.Error(), sub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCandidate() unexpected error: %v", err)
			}
			if got.Candidate.Ref().String() != tt.wantRef {
				t.Errorf("resolveCandidate() ref = %q, want %q", got.Candidate.Ref().String(), tt.wantRef)
			}
			if got.How != tt.wantHow {
				t.Errorf("resolveCandidate() how = %q, want %q", got.How, tt.wantHow)
			}
			if got.Position != tt.wantPosition {
				t.Errorf("resolveCandidate() position = %d, want %d", got.Position, tt.wantPosition)
			}
			gotSkipped := make([]string, len(got.Skipped))
			for i, s := range got.Skipped {
				gotSkipped[i] = s.Token
			}
			if len(gotSkipped) != len(tt.wantSkipped) {
				t.Errorf("resolveCandidate() skipped = %v, want %v", gotSkipped, tt.wantSkipped)
			} else {
				for i := range gotSkipped {
					if gotSkipped[i] != tt.wantSkipped[i] {
						t.Errorf("resolveCandidate() skipped = %v, want %v", gotSkipped, tt.wantSkipped)
						break
					}
				}
			}
			if tt.name == "explicit, gated, proceeds" && len(got.Gates) != 1 {
				t.Errorf("resolveCandidate() gates = %v, want len 1", got.Gates)
			}
		})
	}
}

func TestResolveCandidateIsDeterministic(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)

	c1, err1 := resolveCandidate(set, policy.Policy{}, nil, "", "reviewer")
	c2, err2 := resolveCandidate(set, policy.Policy{}, nil, "", "reviewer")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if c1.Candidate.Ref().String() != c2.Candidate.Ref().String() {
		t.Errorf("got %q and %q, want identical ref", c1.Candidate.Ref().String(), c2.Candidate.Ref().String())
	}

	_, errA1 := resolveCandidate(set, policy.Policy{}, nil, "", "builder")
	_, errA2 := resolveCandidate(set, policy.Policy{}, nil, "", "builder")
	if errA1 == nil || errA2 == nil {
		t.Fatalf("expected errors for ambiguous case")
	}
	if errA1.Error() != errA2.Error() {
		t.Errorf("got %q and %q, want identical error string", errA1.Error(), errA2.Error())
	}
}

func TestExplainResolution(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	lookup := func(tok string) candidate.Candidate {
		ref, err := candidate.ParseRef(tok)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", tok, err)
		}
		c, err := set.Lookup(ref)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", tok, err)
		}
		return c
	}
	claude := lookup(testClaudeRef)
	agy := lookup(testAgyRef)
	opencode := lookup(testOpencodeRef)

	until := baseTime.Add(10 * time.Minute)
	untilText := GateUntilText(until)

	tests := []struct {
		name string
		role string
		res  Resolution
		want string
	}{
		{
			name: "sole",
			role: "reviewer",
			res:  Resolution{How: HowSole, Candidate: claude},
			want: "picked claude/test/m for reviewer: sole candidate",
		},
		{
			name: "order",
			role: "builder",
			res: Resolution{
				How:       HowOrder,
				Position:  2,
				Candidate: claude,
				Skipped:   []Skip{{Token: testAgyRef, Kind: ledger.SpawnFailed, Until: until}},
			},
			want: "picked claude/test/m for builder: order #2; skipped " + testAgyRef + " (spawn failed " + untilText + ")",
		},
		{
			name: "unlisted",
			role: "builder",
			res: Resolution{
				How:       HowUnlisted,
				Candidate: opencode,
				Skipped: []Skip{
					{Token: testAgyRef, Kind: ledger.SpawnFailed, Until: until},
					{Token: testClaudeRef, Kind: ledger.RateLimited},
				},
			},
			want: "picked opencode/test/m for builder: unlisted, after order; skipped " + testAgyRef + " (spawn failed " + untilText + "), " + testClaudeRef + " (rate-limited until cleared)",
		},
		{
			// #93: three `relay unavailable` calls on one provider are three
			// ledger entries and three Skips, but one sentence.
			name: "duplicate gates on one token render once",
			role: "builder",
			res: Resolution{
				How:       HowOrder,
				Position:  2,
				Candidate: claude,
				Skipped: []Skip{
					{Token: testAgyRef, Kind: ledger.RateLimited},
					{Token: testAgyRef, Kind: ledger.RateLimited},
					{Token: testAgyRef, Kind: ledger.RateLimited},
				},
			},
			want: "picked claude/test/m for builder: order #2; skipped " + testAgyRef + " (rate-limited until cleared)",
		},
		{
			name: "explicit with duplicate gates renders once",
			role: "builder",
			res: Resolution{
				How:       HowExplicit,
				Candidate: agy,
				Gates: []Skip{
					{Token: testAgyRef, Kind: ledger.RateLimited},
					{Token: testAgyRef, Kind: ledger.RateLimited},
				},
			},
			want: "picked agy/test/m for builder: explicit, policy bypassed; gated: rate-limited until cleared",
		},
		{
			name: "explicit",
			role: "builder",
			res:  Resolution{How: HowExplicit, Candidate: agy},
			want: "picked agy/test/m for builder: explicit, policy bypassed",
		},
		{
			name: "explicit inherited gated",
			role: "builder",
			res: Resolution{
				How:           HowExplicit,
				Candidate:     agy,
				InheritedFrom: "source",
				Gates:         []Skip{{Token: testAgyRef, Kind: ledger.RateLimited}},
			},
			want: "picked agy/test/m for builder: explicit, inherited from source, policy bypassed; gated: rate-limited until cleared",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExplainResolution(tt.role, tt.res); got != tt.want {
				t.Errorf("ExplainResolution() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCandidateKind(t *testing.T) {
	rt := Runtime{
		Candidates:  candidateSet(t, testCandidatesJSON),
		LedgerPath:  filepath.Join(t.TempDir(), "ledger.json"),
		HistoryPath: filepath.Join(t.TempDir(), "history.json"),
		Now:         func() time.Time { return baseTime },
	}
	if got := CandidateKind(rt, testClaudeRef); got != "claude" {
		t.Errorf("CandidateKind(%q) = %q, want claude", testClaudeRef, got)
	}
	if got := CandidateKind(rt, "claude/test/nope"); got != "" {
		t.Errorf("CandidateKind(unknown) = %q, want empty", got)
	}
}
