package relay

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/ledger"
)

// FormatCandidates renders the configured candidates for `relay candidates`:
// one line per token, sorted, with the roles it serves and any extra args in
// brackets, and -- when gated -- a trailing note naming why and until when.
// It is a listing, not a check -- zero candidates prints the same sentence
// the bind refusal uses, so the planner learns the file name once.
func FormatCandidates(set *candidate.Set, gates []ledger.Gate) string {
	if set == nil || set.Len() == 0 {
		return "no candidates configured; write ~/.config/relay/candidates.json (see README \"Candidates\")\n"
	}

	byToken := make(map[string][]ledger.Gate)
	for _, g := range gates {
		byToken[g.Token] = append(byToken[g.Token], g)
	}

	refs := set.Refs()
	width := 0
	for _, ref := range refs {
		if len(ref) > width {
			width = len(ref)
		}
	}
	var sb strings.Builder
	for _, ref := range refs {
		parsed, _ := candidate.ParseRef(ref)
		c, err := set.Lookup(parsed)
		if err != nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("%-*s  %s", width, ref, strings.Join(c.Roles, ", ")))
		if len(c.ExtraArgs) > 0 {
			sb.WriteString("   [" + strings.Join(c.ExtraArgs, " ") + "]")
		}
		if rowGates := byToken[ref]; len(rowGates) > 0 {
			parts := make([]string, len(rowGates))
			for i, g := range rowGates {
				parts[i] = fmt.Sprintf("%s %s", GateKindText(g.Kind), GateUntilText(g.Until))
			}
			sb.WriteString("   unavailable: " + strings.Join(parts, "; "))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
