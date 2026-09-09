package relay

import (
	"fmt"

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

// movedPaneWarning is the clause relay adds when it cannot tell a dead builder
// from one whose pane moved between workspaces. It is the case where the
// documented recovery -- `relay bind --resume --builder` -- is the harm, so it
// is stated wherever a human is about to choose one.
const movedPaneWarning = "the builder was never session-identified, " +
	"so it may be alive in a moved pane -- verify before rebinding"

// Detail renders the sentence `relay status` shows beneath a broken binding.
//
// The sentence is composed from two independent clauses rather than
// enumerated, because RoundOpen and SessionIdentified vary independently.
//
// Detail is total: every combination yields a non-empty sentence, so a caller
// never has to treat an empty return as a special case.
//
// round is the binding's current round. queueReport increments Round after
// logging a report, so a closed round's report belongs to round-1 -- and a
// binding that has never been sent has no delivered report to name at all.
func (d BuilderDiagnosis) Detail(round int) string {
	var stake string
	switch {
	case d.RoundOpen:
		stake = fmt.Sprintf("round %d was open -- that work is unaccounted for", round)
	case round > 1:
		stake = fmt.Sprintf("round %d report delivered; nothing outstanding", round-1)
	default:
		stake = "no round has been sent yet; nothing outstanding"
	}

	if !d.SessionIdentified {
		return stake + ", and " + movedPaneWarning
	}
	if d.RoundOpen {
		// Only safe to advise when the builder is positively identifiable:
		// rebinding an unidentified one is what orphans a live builder.
		return stake + "; rebind and resend the round"
	}
	if round > 1 {
		return stake + " -- unless you want another round"
	}
	return stake + " -- unless you want to send one"
}
