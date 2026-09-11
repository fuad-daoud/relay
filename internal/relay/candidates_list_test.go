package relay

import (
	"testing"
)

func TestFormatCandidates(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	got := FormatCandidates(set)
	want := "agy/test/m       builder   [--dangerously-skip-permissions]\n" +
		"claude/test/m    builder, reviewer\n" +
		"opencode/test/m  builder\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatCandidatesEmpty(t *testing.T) {
	set := candidateSet(t, "[]")
	got := FormatCandidates(set)
	want := "no candidates configured; write ~/.config/relay/candidates.json (see README \"Candidates\")\n"
	if got != want {
		t.Errorf("FormatCandidates(empty) = %q, want %q", got, want)
	}
}
