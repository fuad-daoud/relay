package hooks

import "time"

type EventType string

const (
	EventStateChanged EventType = "state_changed"
	EventRoundStarted EventType = "round_started"
	// EventForkCreated reports that a new binding was branched from an earlier round
	// of an existing binding. OldState holds the source binding's name for provenance.
	EventForkCreated EventType = "fork_created"
)

// Event encapsulates the context of a state transition or action.
type Event struct {
	Type      EventType
	BindingID string
	State     string
	OldState  string
	Round     int
	Timestamp time.Time
}

// Config configures the hook dispatcher environment.
type Config struct {
	HooksDir string // Resolved to ~/.config/relay/hooks
	LogPath  string // Resolved to ~/.local/state/relay/hooks.log
}
