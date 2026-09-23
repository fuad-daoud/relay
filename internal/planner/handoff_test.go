package planner

// The tests for #374 round 6 step 1: one embedded "Handing off" source,
// handoff.md, and the planner hook that carries it. The shipped architect
// definitions stay untouched -- this file pins them to handoffRules instead.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// handingOffSection returns doc from the line "## Handing off" to the end, or
// to the first line that is exactly delim when delim is non-empty. The result
// keeps each line's trailing newline, so a section that ends the file compares
// byte-for-byte with handoffRules.
func handingOffSection(t *testing.T, doc []byte, delim string) []byte {
	t.Helper()
	i := bytes.Index(doc, []byte("## Handing off"))
	if i < 0 {
		t.Fatal("the definition has no '## Handing off' heading")
	}
	rest := doc[i:]
	if delim == "" {
		return rest
	}
	var out []byte
	for _, line := range bytes.Split(rest, []byte("\n")) {
		if string(line) == delim {
			break
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out
}

// TestHandoffRulesMatchShippedArchitectCopies pins #374 step 1: handoff.md is
// byte-identical to the "Handing off" section of every shipped architect
// copy. A change to any copy without handoff.md -- or the reverse -- fails
// here.
func TestHandoffRulesMatchShippedArchitectCopies(t *testing.T) {
	for _, kind := range []string{"claude", "opencode", "agy"} {
		doc, err := harness.AgentDoc("architect", kind)
		if err != nil {
			t.Fatalf("AgentDoc(architect, %s): %v", kind, err)
		}
		got := handingOffSection(t, doc, "")
		if !bytes.Equal(got, []byte(handoffRules)) {
			t.Errorf("%s: architect's Handing off section differs from handoffRules", kind)
		}
	}

	// codex wraps its definition in a TOML literal string: the section runs to
	// the line that is exactly ''' and stops there.
	doc, err := harness.AgentDoc("architect", "codex")
	if err != nil {
		t.Fatalf("AgentDoc(architect, codex): %v", err)
	}
	got := handingOffSection(t, doc, "'''")
	if !bytes.Equal(got, []byte(handoffRules)) {
		t.Errorf("codex: architect's Handing off section up to the delimiter differs from handoffRules")
	}
}

// TestHookOutputCarriesHandoffRules pins #374 step 1's hook half: decoded, the
// hook's additionalContext starts with the planner sentence and ends with the
// handoff rules.
func TestHookOutputCarriesHandoffRules(t *testing.T) {
	rec := Record{ID: "pl_aaaaaaaaaaaa", Name: "architect-1"}

	var env hookEnvelope
	if err := json.Unmarshal(HookOutput(rec), &env); err != nil {
		t.Fatalf("HookOutput is not the hook envelope: %v", err)
	}
	ctx := env.HookSpecificOutput.AdditionalContext
	if !strings.HasPrefix(ctx, hookContext(rec)) {
		t.Errorf("additionalContext %q does not start with %q", ctx, hookContext(rec))
	}
	if !strings.HasSuffix(ctx, handoffRules) {
		t.Error("additionalContext does not end with the handoff rules")
	}
}
