package relevo

import (
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// defaultOutput words a reader's prompt when its agent names no output label.
const defaultOutput = "notes"

// ActorOutput is the output label of the agent actor plays: the shipped
// agent's label, else a custom source agent's `output:`, else defaultOutput.
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
	return defaultOutput
}

// actorOutput loads the config for ActorOutput; a missing or unreadable config
// falls back to the shipped labels, because the label only words the prompt.
func actorOutput(rt Runtime, actor, definition string) string {
	if rt.Config == nil {
		return ActorOutput(nil, nil, actor, definition)
	}
	loaded, err := rt.Config.Load()
	if err != nil {
		return ActorOutput(nil, nil, actor, definition)
	}
	return ActorOutput(loaded.Actors, loaded.Agents, actor, definition)
}
