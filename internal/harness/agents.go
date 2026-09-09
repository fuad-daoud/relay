package harness

import (
	"embed"
	"errors"
)

//go:embed agents/plan-executor.claude.md agents/plan-executor.opencode.md
var agentFS embed.FS

// ErrNoAgentDoc reports a key with no embedded definition.
var ErrNoAgentDoc = errors.New("no embedded agent definition")

// AgentDoc returns the embedded plan-executor definition for a RoleDoc key.
// ErrNoAgentDoc reports a key with no embedded definition.
func AgentDoc(key string) ([]byte, error) {
	switch key {
	case "claude":
		return agentFS.ReadFile("agents/plan-executor.claude.md")
	case "opencode":
		return agentFS.ReadFile("agents/plan-executor.opencode.md")
	default:
		return nil, ErrNoAgentDoc
	}
}
