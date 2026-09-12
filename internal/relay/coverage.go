package relay

import (
	"fmt"

	"github.com/fuad-daoud/relay/internal/harness"
)

// SubAgentCoverage returns the `coverage` row for a builder of the given
// kind, and false when no row is warranted.
//
// The row says what an empty foreign list proves. It is a fact about the
// harness (harness.Harness.SubAgents), not about the current round, so it
// prints whether or not the builder is doing anything: making it conditional
// on activity would need relay to see activity it cannot. Only
// SubAgentsSeparate prints nothing, because there herdr lists sub-agents and
// ForeignAgents reports them. An unknown kind ("" from a failed Lookup) is
// reported as unverified rather than assumed either way.
func SubAgentCoverage(kind string, vis harness.SubAgentVisibility) (string, bool) {
	const tail = "; no foreign rows above does not mean the tree is clear"
	switch vis {
	case harness.SubAgentsSeparate:
		return "", false
	case harness.SubAgentsForeground:
		return fmt.Sprintf("sub-agents hidden: %s runs them in the builder pane%s", kind, tail), true
	case harness.SubAgentsHidden:
		return fmt.Sprintf("sub-agents hidden: %s runs them in-process, herdr lists only the pane%s", kind, tail), true
	default:
		return fmt.Sprintf("sub-agents unverified for kind %q%s", kind, tail), true
	}
}
