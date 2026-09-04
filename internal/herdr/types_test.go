package herdr

import "testing"

const agentListFixture = `{"id":"cli:agent:list","result":{"agents":[
{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"c6b59b8f-e80a-48ef-a3de-d2d15ec90e24"},
"agent_status":"blocked","cwd":"/home/fuad/projects/uniqueperfumesjo","focused":true,"pane_id":"w2:p7",
"tab_id":"w2:t7","terminal_title_stripped":"architect","workspace_id":"w2"},
{"agent":"opencode","agent_session":{"agent":"opencode","kind":"id","source":"herdr:opencode","value":"d483cf1e"},
"agent_status":"working","cwd":"/home/fuad/projects/career","focused":false,"pane_id":"w4:pA",
"tab_id":"w4:tA","terminal_title_stripped":"builder","workspace_id":"w4"}],"type":"agent_list"}}`

func TestParseAgentList(t *testing.T) {
	agents, err := ParseAgentList([]byte(agentListFixture))
	if err != nil {
		t.Fatalf("ParseAgentList: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
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
