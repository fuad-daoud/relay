package herdr

import (
	"errors"
	"strings"
	"testing"
)

const agentListFixture = `{"id":"cli:agent:list","result":{"agents":[
{"name":"architect-pane","agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"c6b59b8f-e80a-48ef-a3de-d2d15ec90e24"},
"agent_status":"blocked","cwd":"/home/dev/projects/webshop","focused":true,"pane_id":"w2:p7",
"tab_id":"w2:t7","terminal_title_stripped":"architect","workspace_id":"w2"},
{"agent":"opencode","agent_session":{"agent":"opencode","kind":"id","source":"herdr:opencode","value":"d483cf1e"},
"agent_status":"working","cwd":"/home/dev/projects/api","focused":false,"pane_id":"w4:pA",
"tab_id":"w4:tA","terminal_title_stripped":"builder","workspace_id":"w4"}],"type":"agent_list"}}`

func TestParseAgentList(t *testing.T) {
	agents, err := ParseAgentList([]byte(agentListFixture))
	if err != nil {
		t.Fatalf("ParseAgentList: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	if agents[0].Name != "architect-pane" {
		t.Errorf("agent 0 name = %q, want %q", agents[0].Name, "architect-pane")
	}
	if agents[1].Name != "" {
		t.Errorf("agent 1 name = %q, want empty", agents[1].Name)
	}
	if agents[0].Status != StatusBlocked {
		t.Errorf("agent 0 status = %q, want %q", agents[0].Status, StatusBlocked)
	}
	if !agents[0].Focused {
		t.Error("agent 0 should be focused")
	}
	if agents[0].Session.Value != "c6b59b8f-e80a-48ef-a3de-d2d15ec90e24" {
		t.Errorf("agent 0 session = %q", agents[0].Session.Value)
	}
	if agents[1].Kind != "opencode" || agents[1].PaneID != "w4:pA" {
		t.Errorf("agent 1 = %+v", agents[1])
	}
}

func TestParseAgentListRejectsEmpty(t *testing.T) {
	if _, err := ParseAgentList(nil); err == nil {
		t.Fatal("want error for empty response, got nil")
	}
}

// TestValidateAgentName pins the rule as transcribed from herdr 0.9.0: the
// boundary at exactly 32 characters, and every refusal wrapping
// ErrInvalidAgentName so call sites can errors.Is it.
func TestValidateAgentName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
		wantLen bool // the message must carry the offending length
	}{
		{"a", false, false},
		{"reviewer-2b", false, false},
		{strings.Repeat("a", 32), false, false}, // exactly the limit
		{strings.Repeat("a", 33), true, true},   // one over
		{"Abc", true, false},                    // uppercase start
		{"1abc", true, false},                   // leading digit
		{"a b", true, false},                    // a space
		{"a.b", true, false},                    // a dot
		{"", true, false},                       // empty
		{"-abc", true, false},                   // '-' is not a lowercase letter
	}

	for _, tt := range tests {
		err := ValidateAgentName(tt.name)
		if !tt.wantErr {
			if err != nil {
				t.Errorf("ValidateAgentName(%q) = %v, want nil", tt.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("ValidateAgentName(%q) = nil, want an error", tt.name)
			continue
		}
		if !errors.Is(err, ErrInvalidAgentName) {
			t.Errorf("ValidateAgentName(%q) err = %v, want one wrapping ErrInvalidAgentName", tt.name, err)
		}
		if tt.wantLen && !strings.Contains(err.Error(), "33 characters") {
			t.Errorf("ValidateAgentName(%q) err = %v, want the length in the message", tt.name, err)
		}
	}
}
