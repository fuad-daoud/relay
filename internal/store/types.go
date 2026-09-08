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
	// HaltNotifiedRound is the round a halt notification has already been sent
	// for. It is deliberately NOT derived from State: every earlier attempt to
	// dedupe halt notices on State was defeated by a later step in the same
	// tick rewriting State, which turned one notice into one per poll.
	HaltNotifiedRound int `json:"halt_notified_round,omitempty"`
	// RoundBaselineTree is the git tree object the CURRENT round started from,
	// written by Send and consumed (then cleared) when the round's report is
	// queued. Empty means no baseline was captured for this round -- a non-git
	// tree, an unavailable git binary, or a binding created before diff capture
	// existed -- and the round simply produces no diff.
	RoundBaselineTree string    `json:"round_baseline_tree,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
