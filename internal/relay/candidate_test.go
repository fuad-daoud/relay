package relay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/candidate"
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

func TestResolveCandidate(t *testing.T) {
	tests := []struct {
		name            string
		setBody         string
		token           string
		role            string
		wantRef         string
		wantErr         error
		messageContains []string
	}{
		{
			name:    "named, serves",
			setBody: testCandidatesJSON,
			token:   "claude/test/m",
			role:    "builder",
			wantRef: "claude/test/m",
		},
		{
			name:    "named, serves consult",
			setBody: testCandidatesJSON,
			token:   "claude/test/m",
			role:    "reviewer",
			wantRef: "claude/test/m",
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
		},
		{
			name:            "ambiguous, no token",
			setBody:         testCandidatesJSON,
			token:           "",
			role:            "builder",
			wantErr:         ErrAmbiguousCandidate,
			messageContains: []string{`3 candidates serve "builder"`, "name one with"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := candidateSet(t, tt.setBody)
			got, err := resolveCandidate(set, tt.token, tt.role)
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
			if got.Ref().String() != tt.wantRef {
				t.Errorf("resolveCandidate() ref = %q, want %q", got.Ref().String(), tt.wantRef)
			}
		})
	}
}

func TestResolveCandidateIsDeterministic(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)

	c1, err1 := resolveCandidate(set, "", "reviewer")
	c2, err2 := resolveCandidate(set, "", "reviewer")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if c1.Ref().String() != c2.Ref().String() {
		t.Errorf("got %q and %q, want identical ref", c1.Ref().String(), c2.Ref().String())
	}

	_, errA1 := resolveCandidate(set, "", "builder")
	_, errA2 := resolveCandidate(set, "", "builder")
	if errA1 == nil || errA2 == nil {
		t.Fatalf("expected errors for ambiguous case")
	}
	if errA1.Error() != errA2.Error() {
		t.Errorf("got %q and %q, want identical error string", errA1.Error(), errA2.Error())
	}
}

func TestCandidateKind(t *testing.T) {
	rt := Runtime{Candidates: candidateSet(t, testCandidatesJSON), LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	if got := CandidateKind(rt, testClaudeRef); got != "claude" {
		t.Errorf("CandidateKind(%q) = %q, want claude", testClaudeRef, got)
	}
	if got := CandidateKind(rt, "claude/test/nope"); got != "" {
		t.Errorf("CandidateKind(unknown) = %q, want empty", got)
	}
}
