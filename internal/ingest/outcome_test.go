package ingest

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestOutcomeReportedBeatsDone pins §5.3's rule order: a report entry wins
// even when a done marker is also present. Mutation check: swapping the
// report and done_no_report rules must fail this test.
func TestOutcomeReportedBeatsDone(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindReport}}
	members := map[string]bool{donePathBase(1): true}
	b := store.Binding{Round: 1, State: store.StateActive}

	got := deriveOutcome(events, 1, b, members)
	if got != db.OutcomeReported {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeReported)
	}
}

func TestOutcomeDoneNoReport(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPlan}}
	members := map[string]bool{donePathBase(1): true}
	b := store.Binding{Round: 1, State: store.StateActive}

	got := deriveOutcome(events, 1, b, members)
	if got != db.OutcomeDoneNoReport {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeDoneNoReport)
	}
}

func TestOutcomeExited(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPlan}, {Round: 1, Kind: store.KindExit}}
	members := map[string]bool{}
	b := store.Binding{Round: 1, State: store.StateNeedsYou}

	got := deriveOutcome(events, 1, b, members)
	if got != db.OutcomeExited {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeExited)
	}
}

func TestOutcomeHaltedByState(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPlan}}
	members := map[string]bool{}
	b := store.Binding{Round: 1, State: store.StateNeedsYou}

	got := deriveOutcome(events, 1, b, members)
	if got != db.OutcomeHalted {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeHalted)
	}
}

func TestOutcomeHaltedByHaltText(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPlan}}
	members := map[string]bool{}
	b := store.Binding{Round: 1, State: store.StateActive, Halt: "builder exited (code 1) without a report"}

	got := deriveOutcome(events, 1, b, members)
	if got != db.OutcomeHalted {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeHalted)
	}
}

func TestOutcomeSwitched(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPlan}, {Round: 1, Kind: store.KindSwitch}}
	members := map[string]bool{}
	// Round 2 is current, so none of the round-1-scoped rules above fire.
	b := store.Binding{Round: 2, State: store.StateActive}

	got := deriveOutcome(events, 1, b, members)
	if got != db.OutcomeSwitched {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeSwitched)
	}
}

func TestOutcomeOpen(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPlan}}
	members := map[string]bool{}
	b := store.Binding{Round: 2, State: store.StateActive}

	got := deriveOutcome(events, 1, b, members)
	if got != db.OutcomeOpen {
		t.Errorf("deriveOutcome = %q, want %q", got, db.OutcomeOpen)
	}
}

func TestBuilderForRoundFromPick(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPick, Note: "picked opencode/openrouter/z-ai/glm-5.3-flash on host1: initial spawn"}}
	b := store.Binding{}

	tok, ref, ok := builderForRound(events, 1, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if tok != "opencode/openrouter/z-ai/glm-5.3-flash" {
		t.Errorf("token = %q", tok)
	}
	want := candidateRef(t, "opencode", "openrouter", "z-ai/glm-5.3-flash")
	if ref != want {
		t.Errorf("ref = %+v, want %+v", ref, want)
	}
}

func TestBuilderForRoundFromSwitch(t *testing.T) {
	events := []store.LogEntry{{Round: 2, Kind: store.KindSwitch, Note: "switched opencode/openrouter/z-ai/glm-5.3-flash -> agy/google/gemini-3.8-flash-high"}}
	b := store.Binding{}

	tok, ref, ok := builderForRound(events, 2, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if tok != "agy/google/gemini-3.8-flash-high" {
		t.Errorf("token = %q", tok)
	}
	want := candidateRef(t, "agy", "google", "gemini-3.8-flash-high")
	if ref != want {
		t.Errorf("ref = %+v, want %+v", ref, want)
	}
}

func TestBuilderForRoundFallsBackToBinding(t *testing.T) {
	b := store.Binding{BuilderCandidate: "claude/anthropic/sonnet"}

	tok, ref, ok := builderForRound(nil, 1, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if tok != "claude/anthropic/sonnet" {
		t.Errorf("token = %q", tok)
	}
	want := candidateRef(t, "claude", "anthropic", "sonnet")
	if ref != want {
		t.Errorf("ref = %+v, want %+v", ref, want)
	}
}

func TestBuilderForRoundStripsEffortSuffix(t *testing.T) {
	events := []store.LogEntry{{Round: 1, Kind: store.KindPick, Note: "picked opencode/cline-pass/cline-pass/glm-5.3-flash#high on host1: spawn"}}
	b := store.Binding{}

	tok, ref, ok := builderForRound(events, 1, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if tok != "opencode/cline-pass/cline-pass/glm-5.3-flash#high" {
		t.Errorf("token = %q, want the verbatim token with its suffix", tok)
	}
	want := candidateRef(t, "opencode", "cline-pass", "cline-pass/glm-5.3-flash")
	if ref != want {
		t.Errorf("ref = %+v, want %+v", ref, want)
	}
}

func TestParsePickNoteForms(t *testing.T) {
	tests := []struct {
		note string
		want string
	}{
		{"picked claude/anthropic/sonnet for builder: order #1", "claude/anthropic/sonnet"},
		{"picked opencode/openrouter/z-ai/glm-5.3-flash on host1: spawn", "opencode/openrouter/z-ai/glm-5.3-flash"},
		{"picked agy/test/m: explicit, policy bypassed", "agy/test/m"},
		{"picked codex/openai/gpt-5.6-terra:high for builder: order #2", "codex/openai/gpt-5.6-terra:high"},
		{"picked opencode/cline-pass/cline-pass/glm-5.3-flash#high on h: s", "opencode/cline-pass/cline-pass/glm-5.3-flash#high"},
		{"picked x/y/z", "x/y/z"},
		{"nothing picked", ""},
	}
	for _, tt := range tests {
		if got := parsePickNote(tt.note); got != tt.want {
			t.Errorf("parsePickNote(%q) = %q, want %q", tt.note, got, tt.want)
		}
	}
}

func TestParseSwitchNoteForms(t *testing.T) {
	tests := []struct {
		note string
		want string
	}{
		{"switched builder (exited (code 1) without a report): picked claude/anthropic/sonnet for builder: order #5", "claude/anthropic/sonnet"},
		{"switched a/b/c -> d/e/f", "d/e/f"},
		{"switched builder (reason): cannot switch", ""},
	}
	for _, tt := range tests {
		if got := parseSwitchNote(tt.note); got != tt.want {
			t.Errorf("parseSwitchNote(%q) = %q, want %q", tt.note, got, tt.want)
		}
	}
}

func TestBuilderForRoundFromSwitchEntry(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPick, Note: "picked a/b/c for builder: order #1"},
		{Round: 1, Kind: store.KindSwitch, Note: "switched builder (exited (code 1) without a report): picked claude/anthropic/sonnet for builder: order #5"},
	}
	b := store.Binding{BuilderCandidate: "x/y/z"}

	tok, ref, ok := builderForRound(events, 1, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if tok != "claude/anthropic/sonnet" {
		t.Errorf("token = %q", tok)
	}
	want := candidateRef(t, "claude", "anthropic", "sonnet")
	if ref != want {
		t.Errorf("ref = %+v, want %+v", ref, want)
	}
}

// TestIsRolePick pins the negative rule: only the consult pick `ask` writes,
// "picked <tok> for <role>:" with role != builder, is a role pick. A builder
// pick, a remote " on <server>:" pick, an unrecognised shape and a switch
// note (which does not start with "picked ") are not.
func TestIsRolePick(t *testing.T) {
	tests := []struct {
		note string
		want bool
	}{
		{"picked claude/anthropic/sonnet for reviewer: order #1", true},
		{"picked a/b/c for verify: sole candidate", true},
		{"picked a/b/c for builder: order #1", false},
		{"picked a/b/c on host1: spawn", false},
		{"picked a/b/c", false},
		{"picked a/b/c for : x", false},
		{"switched builder (exited (code 1) without a report): picked a/b/c for builder: order #5", false},
	}
	for _, tt := range tests {
		if got := isRolePick(tt.note); got != tt.want {
			t.Errorf("isRolePick(%q) = %v, want %v", tt.note, got, tt.want)
		}
	}
}

// TestBuilderForRoundSkipsConsultPick pins bug 1's fix: a consult's pick,
// filed after the builder's, does not become the round's builder.
func TestBuilderForRoundSkipsConsultPick(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPick, Note: "picked opencode/cline-pass/cline-pass/glm-5.3-flash#high for builder: order #1"},
		{Round: 1, Kind: store.KindPick, Note: "picked claude/anthropic/sonnet for reviewer: order #1"},
	}
	b := store.Binding{}

	tok, ref, ok := builderForRound(events, 1, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if tok != "opencode/cline-pass/cline-pass/glm-5.3-flash#high" {
		t.Errorf("token = %q, want the builder pick's", tok)
	}
	want := candidateRef(t, "opencode", "cline-pass", "cline-pass/glm-5.3-flash")
	if ref != want {
		t.Errorf("ref = %+v, want %+v", ref, want)
	}
}

// TestBuilderForRoundOnlyConsultPickFallsBack pins that a round whose only
// pick is a consult's falls back to b.BuilderCandidate, as a round with no
// pick does today.
func TestBuilderForRoundOnlyConsultPickFallsBack(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPick, Note: "picked claude/anthropic/sonnet for reviewer: order #1"},
	}
	b := store.Binding{BuilderCandidate: "claude/anthropic/sonnet"}

	tok, ref, ok := builderForRound(events, 1, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if tok != "claude/anthropic/sonnet" {
		t.Errorf("token = %q, want the binding's candidate", tok)
	}
	want := candidateRef(t, "claude", "anthropic", "sonnet")
	if ref != want {
		t.Errorf("ref = %+v, want %+v", ref, want)
	}
}

func TestSwitchesForRound(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindSwitch},
		{Round: 1, Kind: store.KindSwitch},
		{Round: 2, Kind: store.KindSwitch},
		{Round: 1, Kind: store.KindPlan},
	}

	if got := switchesForRound(events, 1); got != 2 {
		t.Errorf("switchesForRound(round 1) = %d, want 2", got)
	}
	if got := switchesForRound(events, 2); got != 1 {
		t.Errorf("switchesForRound(round 2) = %d, want 1", got)
	}
	if got := switchesForRound(events, 3); got != 0 {
		t.Errorf("switchesForRound(round 3) = %d, want 0", got)
	}
}

func candidateRef(t *testing.T, harness, provider, model string) candidate.Ref {
	t.Helper()
	return candidate.Ref{Harness: harness, Provider: provider, Model: model}
}
