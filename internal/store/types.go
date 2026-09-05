// Package store owns relay's on-disk state: the bindings and the append-only
// round log. Everything else relay knows is queried live from herdr.
package store

import "time"

// State is a binding's display and control state.
type State string

const (
	StateActive   State = "active"    // someone is working
	StateHeld     State = "held"      // payload ready, human is in the planner pane
	StateNeedsYou State = "needs_you" // stalled on a human decision
	StateBroken   State = "broken"    // builder pane is gone
	StateOrphaned State = "orphaned"  // planner session is gone
	StateDone     State = "done"      // planner declared the work verified
)

// Endpoint is one side of a binding. PaneID moves when a pane is moved between
// workspaces; SessionID does not, so it is the durable identity.
type Endpoint struct {
	AgentName string `json:"agent_name,omitempty"`
	PaneID    string `json:"pane_id"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind"`
}

// Binding ties one planner pane to one builder pane over one working tree.
type Binding struct {
	Name           string    `json:"name"`
	CWD            string    `json:"cwd"`
	Planner        Endpoint  `json:"planner"`
	Builder        Endpoint  `json:"builder"`
	BuilderAlias   string    `json:"builder_alias"`
	Round          int       `json:"round"`
	State          State     `json:"state"`
	RoundCap       int       `json:"round_cap"`
	RoundTimeoutMS int       `json:"round_timeout_ms"`
	RoundStartedAt time.Time `json:"round_started_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
