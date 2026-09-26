package relevo

import (
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// ActorOutput is the output label of the agent actor plays, the word a consult's
// prompt asks for.
func ActorOutput(actors map[string]roles.Actor, agents map[string]roles.AgentEntry, actor, definition string) string {
	agent := definition
	if a, ok := actors[actor]; ok {
		agent = a.Agent
	}
	if shipped, ok := roles.Shipped(agent); ok {
		return shipped.Output
	}
	if entry, ok := agents[agent]; ok && entry.Source != "" {
		if src, err := agentsrc.Parse([]byte(entry.Source)); err == nil && src.Output != "" {
			return src.Output
		}
	}
	return consult.DefaultOutput
}

// consultOutput resolves the output label of the actor named actor, whose
// resolved harness definition is definition. A nil Config, or a config load
// that fails, falls back to the default label: the label only words the prompt,
// so it never fails an ask.
func consultOutput(rt Runtime, actor, definition string) string {
	if rt.Config == nil {
		return ActorOutput(nil, nil, actor, definition)
	}
	loaded, err := rt.Config.Load()
	if err != nil {
		return ActorOutput(nil, nil, actor, definition)
	}
	return ActorOutput(loaded.Actors, loaded.Agents, actor, definition)
}
