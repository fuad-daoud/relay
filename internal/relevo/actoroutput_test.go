package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/roles"
)

// agentSourceText is a valid agentsrc single-source whose output label is
// output, the format internal/agentsrc's own tests use.
func agentSourceText(name, output string) string {
	return "---\n" +
		"name: " + name + "\n" +
		"description: Answers one question from a plan.\n" +
		"shape: reader\n" +
		"output: " + output + "\n" +
		"requires: []\n" +
		"kinds: []\n" +
		"---\n\n" +
		"Answer the question.\n"
}

// TestActorOutput pins the lookup: the actor's agent's shipped label, a custom
// source agent's own output, the resolved definition for an actor outside the
// actors section, and the default label for anything unknown.
//
// Mutation check: making the roles.Shipped lookup always miss fails the
// lite-planner case (plan becomes findings).
func TestActorOutput(t *testing.T) {
	t.Parallel()

	design := agentSourceText("designer-agent", "design")
	cases := []struct {
		name       string
		actors     map[string]roles.Actor
		agents     map[string]roles.AgentEntry
		actor      string
		definition string
		want       string
	}{
		{
			name:       "shipped architect agent asks for a plan",
			actors:     map[string]roles.Actor{"lite-planner": {Agent: "architect"}},
			actor:      "lite-planner",
			definition: "architect",
			want:       "plan",
		},
		{
			name:       "shipped reviewer agent asks for findings",
			actors:     map[string]roles.Actor{"reviewer": {Agent: "reviewer"}},
			actor:      "reviewer",
			definition: "reviewer",
			want:       "findings",
		},
		{
			name:       "shipped researcher agent asks for notes",
			actors:     map[string]roles.Actor{"researcher": {Agent: "researcher"}},
			actor:      "researcher",
			definition: "researcher",
			want:       "notes",
		},
		{
			name:       "custom source agent names its own output",
			actors:     map[string]roles.Actor{"designer": {Agent: "designer-agent"}},
			agents:     map[string]roles.AgentEntry{"designer-agent": {Source: design}},
			actor:      "designer",
			definition: "designer-agent",
			want:       "design",
		},
		{
			name: "native agent with no source falls back to findings",
			actors: map[string]roles.Actor{
				"native-user": {Agent: "their-agent"},
			},
			agents: map[string]roles.AgentEntry{
				"their-agent": {Shape: "reader", Native: map[string]roles.DefRow{
					"claude": {Agent: "their-native"},
				}},
			},
			actor:      "native-user",
			definition: "their-agent",
			want:       "findings",
		},
		{
			name:       "actor outside the actors section uses its definition",
			actor:      "legacy-planner",
			definition: "architect",
			want:       "plan",
		},
		{
			name:       "unknown actor and definition fall back to findings",
			actor:      "ghost",
			definition: "nope",
			want:       "findings",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ActorOutput(tc.actors, tc.agents, tc.actor, tc.definition); got != tc.want {
				t.Errorf("ActorOutput(%q, %q) = %q, want %q", tc.actor, tc.definition, got, tc.want)
			}
		})
	}
}
