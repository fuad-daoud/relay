package harness

import (
	"errors"
	"regexp"
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

	for _, kind := range []string{"claude", "opencode", "agy"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		if !strings.Contains(string(doc), oneWriter) {
			t.Errorf("plan-executor.%s.md must contain %q", kind, oneWriter)
		}
	}
}

// agy enforces what the other kinds only say: a tools allowlist with
// no write tool, and subagent: false on the one writer (spec §7.3, §7.4).
func TestAgyDefinitionsFrontmatter(t *testing.T) {
	forbidden := regexp.MustCompile(`(?m)^\s*-\s*(write_to_file|replace_file_content|create_file|delete_file|notebook_edit|invoke_subagent|send_command_input)\s*$`)
	for _, role := range []string{"plan-executor", "researcher", "reviewer"} {
		doc, err := AgentDoc(role, "agy")
		if err != nil {
			t.Fatalf("AgentDoc(%s, agy): %v", role, err)
		}
		fm := frontmatter(t, doc)
		if !strings.Contains(fm, "\nname: "+role+"\n") {
			t.Errorf("%s: frontmatter must carry name: %s", role, role)
		}
		if !strings.Contains(fm, "\nmodel: inherit\n") {
			t.Errorf("%s: frontmatter must pin model: inherit", role)
		}
		switch role {
		case "plan-executor":
			if !strings.Contains(fm, "\nsubagent: false\n") {
				t.Errorf("plan-executor must be subagent: false")
			}
			if strings.Contains(fm, "\ntools:") {
				t.Errorf("plan-executor must not restrict tools; it is the writer")
			}
		default:
			if !strings.Contains(fm, "\ntools:\n") {
				t.Errorf("%s must carry a tools allowlist", role)
			}
			if m := forbidden.FindString(fm); m != "" {
				t.Errorf("%s allowlist contains a writing tool: %q", role, strings.TrimSpace(m))
			}
		}
	}
}

// frontmatter returns the text between the first two --- fences,
// with a leading newline so callers can match "\nkey: value\n".
func frontmatter(t *testing.T, doc []byte) string {
	t.Helper()
	s := string(doc)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatal("definition does not open with a --- fence")
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatal("definition frontmatter never closes")
	}
	return "\n" + rest[:end] + "\n"
}
