package hooks

import "time"

type EventType string

const (
	EventStateChanged EventType = "state_changed"
	EventRoundStarted EventType = "round_started"
	// EventForkCreated reports that a new binding was branched from an earlier round
	// of an existing binding. OldState holds the source binding's name for provenance.
	EventForkCreated EventType = "fork_created"
	// EventBuilderStalled reports that a live headless builder's stream went
	// quiet for policy.json's stall_after_ms (#252). It fires once per
	// stall episode; clearing the stall emits nothing. It is an observation,
	// never an action: relevo never kills or switches on it.
	EventBuilderStalled EventType = "builder_stalled"
	// EventBindingStale reports that a NEEDS YOU or HELD binding sat unacted
	// for policy.json's stale_after_ms (#135). It fires once per stale
	// episode; clearing the stamp emits nothing. Like builder_stalled it is
	// an observation, never an action.
	EventBindingStale EventType = "binding_stale"
	// EventRoundQueued reports that a served round was accepted at the
	// builder cap and is waiting for a slot (#285).
	EventRoundQueued EventType = "round_queued"
	// EventRoundAdmitted reports that a queued round's builder started (#285).
	EventRoundAdmitted EventType = "round_admitted"
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
	HooksDir string // Resolved to ~/.config/relevo/hooks
	LogPath  string // Resolved to ~/.local/state/relevo/hooks.log
}
