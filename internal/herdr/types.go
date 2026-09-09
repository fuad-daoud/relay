// Package herdr wraps the herdr CLI. It is the only channel through which
// relay observes or controls agent panes, so every herdr assumption lives here.
package herdr

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MinVersion is the herdr version floor required by relay.
const MinVersion = "0.8.2"

// MinHerdrVersion is an alias for MinVersion.
const MinHerdrVersion = MinVersion

// Agent lifecycle states as reported by herdr. StatusUnknown means herdr sees
// an agent but cannot classify it -- herdr documents that it does not prove
// completion, so relay must never treat it as done.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusUnknown = "unknown"
)

// IntegrationState describes the status of a herdr integration target.
type IntegrationState struct {
	Installed bool   // a hook file is in place
	Outdated  bool   // installed, but older than herdr expects
	Detail    string // herdr's own words, e.g. "outdated (v10 < v11)"
}

// Session identifies an agent's own conversation, reported by the herdr
// integration installed in that harness. It outlives pane id changes, so it is
// the durable half of a binding's identity.
type Session struct {
	Agent string `json:"agent"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Agent is one agent-occupied pane in the live herdr session.
type Agent struct {
	Kind        string  `json:"agent"`
	Status      string  `json:"agent_status"`
	CWD         string  `json:"cwd"`
	Focused     bool    `json:"focused"`
	PaneID      string  `json:"pane_id"`
	TabID       string  `json:"tab_id"`
	WorkspaceID string  `json:"workspace_id"`
	Title       string  `json:"terminal_title_stripped"`
	Session     Session `json:"agent_session"`
}

type agentListEnvelope struct {
	Result struct {
		Agents []Agent `json:"agents"`
	} `json:"result"`
}

// ParseAgentList decodes the envelope returned by `herdr agent list`.
func ParseAgentList(raw []byte) ([]Agent, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty herdr agent list response")
	}

	var env agentListEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode herdr agent list: %w", err)
	}

	return env.Result.Agents, nil
}
