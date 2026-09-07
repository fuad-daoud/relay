package hooks

import "time"

type EventType string

const (
	EventStateChanged EventType = "state_changed"
	EventRoundStarted EventType = "round_started"
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
