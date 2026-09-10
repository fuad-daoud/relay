package harness

import (
	"embed"
	"errors"
)

//go:embed agents/*.md
var agentFS embed.FS

// ErrNoAgentDoc reports a role/kind pair with no embedded definition.
var ErrNoAgentDoc = errors.New("no embedded agent definition")

// AgentDoc returns the embedded definition for one role of one harness kind.
//
// The filename is built from the TABLE's Doc field, never from the caller's
// role string, so a caller cannot steer the read with path syntax. A pair
// absent from the table returns ErrNoAgentDoc without touching the embed FS.
func AgentDoc(role, kind string) ([]byte, error) {
	h, ok := Lookup(kind)
	if !ok {
		return nil, ErrNoAgentDoc
	}
	r, ok := h.Role(role)
	if !ok {
		return nil, ErrNoAgentDoc
	}
	b, err := agentFS.ReadFile("agents/" + r.Doc + ".md")
	if err != nil {
		return nil, ErrNoAgentDoc
	}
	return b, nil
}
