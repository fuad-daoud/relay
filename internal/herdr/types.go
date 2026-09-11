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

// MaxAgentNameLen is the longest agent name herdr accepts. Binding names
// become agent names with a suffix appended ("x-builder", "x-role-<8 hex>"),
// so a binding name the store still accepts can produce a name herdr refuses;
// the composition sites in relay check the derived name against this constant.
const MaxAgentNameLen = 32

// ErrInvalidAgentName reports an agent name herdr would refuse at
// `herdr agent start`. Callers wrap it so a refusal can be told apart from a
// herdr failure that came after something was spawned -- a name herdr would
// refuse is detectable before anything exists.
var ErrInvalidAgentName = errors.New("invalid agent name")

// agentNameRule is herdr 0.9.0's refusal sentence, transcribed verbatim; it
// must follow herdr, or relay would accept a name herdr then refuses (or
// refuse one herdr accepts). Relay does not compose or truncate names around
// it: since #66 identity is by name, and a truncated prefix collision would
// make two builders look like one.
const agentNameRule = "agent name must start with a lowercase letter and contain only lowercase letters, digits, '-' or '_' (1-32 characters)"

// ValidateAgentName reports whether herdr would accept name at
// `herdr agent start`: nil for a name herdr accepts, otherwise an error
// wrapping ErrInvalidAgentName whose text is herdr's own sentence, prefixed by
// the offending name and, when too long, its length.
func ValidateAgentName(name string) error {
	if name == "" || !isAgentName(name) {
		return fmt.Errorf("agent name %q: %s: %w", name, agentNameRule, ErrInvalidAgentName)
	}
	if len(name) > MaxAgentNameLen {
		return fmt.Errorf("agent name %q is %d characters: %s: %w", name, len(name), agentNameRule, ErrInvalidAgentName)
	}
	return nil
}

// isAgentName applies herdr's character rule: a lowercase letter first, then
// only lowercase letters, digits, '-' or '_'. Callers check the length
// separately so the too-long message can carry the count.
func isAgentName(name string) bool {
	if name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

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
	Name        string  `json:"name"` // the name given at `herdr agent start`; empty for agents herdr did not start
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
