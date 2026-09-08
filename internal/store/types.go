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
	RoundBaselineTree string `json:"round_baseline_tree,omitempty"`
	// BuilderScreen is a fingerprint of the builder's terminal as relay last
	// observed it, and BuilderScreenAt is when that observation was taken. They
	// exist to tell a builder that has STOPPED from one that is merely quiet:
	// herdr's idle status means "not currently emitting", which a builder waiting
	// on its own subagents satisfies while very much alive.
	//
	// Both are transient per-round state, written when relay nudges and refreshed
	// whenever the screen is seen to move. queueReport clears them with the round.
	BuilderScreen   string    `json:"builder_screen,omitempty"`
	BuilderScreenAt time.Time `json:"builder_screen_at,omitempty"`

	// Worktree is the git worktree RELAY created for this binding, and is therefore
	// the only directory relay may ever remove. Empty for every binding relay did
	// not create a tree for -- including a fork bound to a directory the human
	// supplied. Never infer ownership from the path.
	Worktree string `json:"worktree,omitempty"`

	// ForkedFrom is the binding this one was forked from, for provenance only.
	// Nothing reads it to make a decision: a fork is an ordinary binding the
	// moment it exists, and the source may be unbound while the fork runs on.
	ForkedFrom string `json:"forked_from,omitempty"`

	// ForkedAtRound is the source round this binding's history was copied through.
	ForkedAtRound int       `json:"forked_at_round,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
