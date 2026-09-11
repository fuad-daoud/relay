package relay

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/ledger"
)

func TestFormatCandidates(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	got := FormatCandidates(set, nil)
	want := "agy/test/m       builder   [--dangerously-skip-permissions]\n" +
		"claude/test/m    builder, reviewer\n" +
		"opencode/test/m  builder\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatCandidatesEmpty(t *testing.T) {
	set := candidateSet(t, "[]")
	got := FormatCandidates(set, nil)
	want := "no candidates configured; write ~/.config/relay/candidates.json (see README \"Candidates\")\n"
	if got != want {
		t.Errorf("FormatCandidates(empty) = %q, want %q", got, want)
	}
}

func TestFormatCandidatesMarksGated(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	gates := []ledger.Gate{
		{Token: testClaudeRef, Kind: ledger.RateLimited, Until: time.Time{}},
	}
	got := FormatCandidates(set, gates)
	want := "agy/test/m       builder   [--dangerously-skip-permissions]\n" +
		"claude/test/m    builder, reviewer   unavailable: rate-limited until cleared\n" +
		"opencode/test/m  builder\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}
