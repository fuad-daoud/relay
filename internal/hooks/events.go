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

// RunLog is where hook runs are recorded (P3b round 2 §4.4): OSExecutor
// appends one entry per run, and WebhookSink appends a delivery failure. The
// production implementation is KVLog, over the machine database's kv row. A
// nil RunLog records nothing.
type RunLog interface {
	Append(HookRun) error
}

// HookRun is one recorded hook run (§3): the event that fired it, the argv it
// ran, the exit code, the error when it failed and the run's combined output,
// capped. Unknown keys survive a rewrite because the whole document is stored.
type HookRun struct {
	At       time.Time `json:"at"`
	Event    string    `json:"event"`
	Argv     []string  `json:"argv"`
	ExitCode int       `json:"exit_code"`
	Error    string    `json:"error"`
	Output   string    `json:"output"`
}

// Config configures the hook dispatcher environment.
type Config struct {
	// Hooks maps an event type to the argv lists run for it, in order. It is
	// stored in the config section, not scanned from a directory (#4.4).
	Hooks map[string][][]string
	// Log is where hook runs are recorded: the machine database's kv row, or
	// nil when no database is open (P3b round 2 §4.4).
	Log RunLog
}
