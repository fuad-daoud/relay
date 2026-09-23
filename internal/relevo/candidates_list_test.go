package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/latency"
	"github.com/fuad-daoud/relevo/internal/ledger"
)

func TestFormatCandidates(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	got := FormatCandidates(set, nil)
	want := "agy/test/m       builder   [--dangerously-skip-permissions]   note: extra_args carries --dangerously-skip-permissions; launches at tier harness only -- move it to \"tier\"\n" +
		"claude/test/m    builder, reviewer\n" +
		"opencode/test/m  builder\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatCandidatesEmpty(t *testing.T) {
	set := candidateSet(t, "[]")
	got := FormatCandidates(set, nil)
	want := "no candidates configured; write ~/.config/relevo/candidates.json (see README \"Candidates\")\n"
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
	want := "agy/test/m       builder   [--dangerously-skip-permissions]   note: extra_args carries --dangerously-skip-permissions; launches at tier harness only -- move it to \"tier\"\n" +
		"claude/test/m    builder, reviewer   unavailable: rate-limited until cleared\n" +
		"opencode/test/m  builder\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatCandidatesTier(t *testing.T) {
	json := `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"],"tier":"yolo"},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}
]`
	set := candidateSet(t, json)
	got := FormatCandidates(set, nil)
	want := "agy/test/m     builder   [--dangerously-skip-permissions]   note: extra_args carries --dangerously-skip-permissions; launches at tier harness only -- move it to \"tier\"\n" +
		"claude/test/m  builder   tier: yolo\n"
	if got != want {
		t.Errorf("FormatCandidates() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatCandidatesLatencySuffix(t *testing.T) {
	const twoCandidates = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]},
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
]`
	set := candidateSet(t, twoCandidates)
	lat := map[string]latency.Summary{"claude/test/m": {N: 3, TTFTP50MS: 640}}

	got := FormatCandidatesLatency(set, nil, lat)
	want := "claude/test/m    builder   ttft p50 640ms (n=3, 30d)\n" +
		"opencode/test/m  builder\n"
	if got != want {
		t.Errorf("FormatCandidatesLatency() =\n%q\nwant:\n%q", got, want)
	}

	// The token with no samples renders exactly as the plain listing does.
	other := got[strings.Index(got, "\n")+1:]
	single := candidateSet(t, `[{"harness":"opencode","provider":"test","model":"m","roles":["builder"]}]`)
	if plain := FormatCandidates(single, nil); other != plain {
		t.Errorf("unprobed line = %q, want FormatCandidates's %q", other, plain)
	}

	// With no history at all, the two render identically.
	if a, b := FormatCandidatesLatency(set, nil, nil), FormatCandidates(set, nil); a != b {
		t.Errorf("FormatCandidatesLatency(no history) = %q, want %q", a, b)
	}
}
