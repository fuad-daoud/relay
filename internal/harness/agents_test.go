package harness

import (
	"errors"
	"strings"
	"testing"
)

func TestAgentDocResolvesEveryTableRole(t *testing.T) {
	for _, h := range All() {
		for _, r := range h.Roles {
			doc, err := AgentDoc(r.Name, h.Kind)
			if err != nil {
				t.Errorf("AgentDoc(%q, %q): %v", r.Name, h.Kind, err)
				continue
			}
			if len(doc) == 0 {
				t.Errorf("AgentDoc(%q, %q) returned empty bytes", r.Name, h.Kind)
			}
		}
	}
}

func TestAgentDocRejectsUnknownPairs(t *testing.T) {
	tests := []struct{ role, kind string }{
		{role: "plan-executor", kind: "agy"}, // agy selects by preamble
		{role: "plan-executor", kind: "nosuch"},
		{role: "nosuch", kind: "claude"},
		{role: "", kind: "claude"},
		{role: "plan-executor", kind: ""},
		{role: "", kind: ""},
	}
	for _, tc := range tests {
		if _, err := AgentDoc(tc.role, tc.kind); !errors.Is(err, ErrNoAgentDoc) {
			t.Errorf("AgentDoc(%q, %q) error = %v, want ErrNoAgentDoc", tc.role, tc.kind, err)
		}
	}
}

// The filename is built from the table's Doc field, so a caller cannot steer
// the read with path syntax in the role argument.
func TestAgentDocRejectsPathTraversal(t *testing.T) {
	for _, role := range []string{
		"../../etc/passwd",
		"../plan-executor",
		"plan-executor/../plan-executor",
		"plan-executor.claude",
	} {
		if _, err := AgentDoc(role, "claude"); !errors.Is(err, ErrNoAgentDoc) {
			t.Errorf("AgentDoc(%q, \"claude\") error = %v, want ErrNoAgentDoc", role, err)
		}
	}
}

func TestPlanExecutorDefinitionsForbidWritingSubAgents(t *testing.T) {
	// The load-bearing sentence. Deleting it from either shipped definition
	// must fail this test: relay ships the role its own loop depends on, and
	// a second writer in one tree destroys work rather than stalling.
	const oneWriter = "Exactly one agent writes to this working tree, and it is you."

	for _, kind := range []string{"claude", "opencode"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		if !strings.Contains(string(doc), oneWriter) {
			t.Errorf("plan-executor.%s.md must contain %q", kind, oneWriter)
		}
	}
}
