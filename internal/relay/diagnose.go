package relay

import (
	"github.com/fuad-daoud/relay/internal/store"
)

// BuilderDiagnosis explains a broken binding: what was at stake when the
// builder went away, and whether relay can trust that it is really gone.
//
// store.StateBroken is documented as "builder pane is gone" and covers three
// situations relay does not otherwise distinguish -- a builder that exited
// after a clean round, one that exited mid-round, and one whose pane merely
// moved between workspaces while the agent kept running. The first needs no
// action; the third is destroyed by the action the second needs.
//
// Both fields are derived from the stored binding alone: no herdr call and no
// log read, so a diagnosis costs nothing and cannot disagree with the binding
// it came from.
type BuilderDiagnosis struct {
	// RoundOpen reports that a round was handed to the builder and no report
	// came back, so that round's work is unaccounted for.
	RoundOpen bool

	// SessionIdentified reports that herdr has recorded a session for the
	// builder. When false, SameAgent has been matching on pane plus kind, and
	// a workspace move changes the pane id -- so a failed match cannot be
	// distinguished from a moved pane, and the builder may still be alive.
	SessionIdentified bool
}

// DiagnoseBuilder derives the diagnosis for a binding. Pure.
//
// RoundStartedAt is stamped only by Send at handoff and cleared only by
// queueReport once the round's report is logged, so a zero value means no
// round is in flight. SessionID is backfilled by refreshEndpoint the first
// time the builder is located carrying a session, and never overwritten, so
// an empty value means herdr has never reported one.
func DiagnoseBuilder(b store.Binding) BuilderDiagnosis {
	return BuilderDiagnosis{
		RoundOpen:         !b.RoundStartedAt.IsZero(),
		SessionIdentified: b.Builder.SessionID != "",
	}
}
