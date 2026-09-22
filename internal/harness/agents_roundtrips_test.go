package harness

import (
	"strings"
	"testing"
)

// TestPlanExecutorRoundTripSectionIsSharedAcrossKinds pins the "every model
// step is a round trip" guidance into all four shipped plan-executor
// definitions. Deleting it from any one kind (the mutation check deletes it
// from plan-executor.agy.md) must fail this test: a round's cost is its
// number of steps, and every kind has to say so for the maker to save any.
func TestPlanExecutorRoundTripSectionIsSharedAcrossKinds(t *testing.T) {
	const heading = "EVERY MODEL STEP IS A ROUND TRIP"
	const cost = "A round's cost is its number of steps, not its tokens."

	for _, kind := range []string{"claude", "opencode", "agy", "codex"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		s := string(doc)
		if !strings.Contains(collapseWhitespace(s), heading) {
			t.Errorf("plan-executor.%s must contain %q", kind, heading)
		}
		// The section is hard-wrapped, so match the sentence with its line
		// breaks flattened to single spaces.
		if !strings.Contains(collapseWhitespace(s), cost) {
			t.Errorf("plan-executor.%s must contain %q", kind, cost)
		}
	}

	// The section text is identical across the three .md kinds. The codex
	// profile carries the same prose inside a TOML string literal, so only
	// the markdown kinds are compared byte for byte.
	const end = "costs more round trips than reading them yourself in one batched step."

	var first, firstKind string
	for _, kind := range []string{"claude", "opencode", "agy"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		s := string(doc)
		start := strings.Index(s, heading)
		if start < 0 {
			t.Fatalf("plan-executor.%s: no %q to compare", kind, heading)
		}
		rest := s[start:]
		stop := strings.Index(rest, end)
		if stop < 0 {
			t.Fatalf("plan-executor.%s: round-trip section never reaches %q", kind, end)
		}
		got := rest[:stop+len(end)]
		if first == "" {
			first, firstKind = got, kind
			continue
		}
		if got != first {
			t.Errorf("plan-executor.%s round-trip section differs from plan-executor.%s:\ngot:\n%s\nwant:\n%s", kind, firstKind, got, first)
		}
	}
}

// collapseWhitespace flattens every run of whitespace to a single space so a
// hard-wrapped section matches a sentence searched with its own spaces.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
