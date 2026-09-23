package relay

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/latency"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/roles"
)

// FormatCandidates renders the configured candidates without latency: the
// listing FormatCandidatesLatency prints when no probe history is loaded.
func FormatCandidates(set *candidate.Set, gates []ledger.Gate) string {
	return FormatCandidatesLatency(set, gates, nil)
}

// FormatCandidatesLatency renders the configured candidates for `relay
// candidates`: one line per token, sorted, with the roles it serves and any
// extra args in brackets, and -- when gated -- a trailing note naming why and
// until when. A token with successful probes in lat also carries its p50 time
// to first output, so the planner can see what a candidate costs to start. It
// is a listing, not a check -- zero candidates prints the same sentence the
// bind refusal uses, so the planner learns the file name once.
func FormatCandidatesLatency(set *candidate.Set, gates []ledger.Gate, lat map[string]latency.Summary) string {
	return formatCandidatesLatency(set, gates, lat, func(c candidate.Candidate) string {
		return strings.Join(c.Roles, ", ")
	}, true)
}

// FormatCandidatesLatencyFor is FormatCandidatesLatency with the roles column
// read from reg (#374 §3.2). Legacy mode delegates, so the output stays
// byte-identical to today's; in file mode the column lists the registry roles
// that serve the candidate -- "(no role)" when none does -- and the tier
// segment is omitted, because in file mode the tier belongs to the role, not
// the candidate.
func FormatCandidatesLatencyFor(reg *roles.Registry, set *candidate.Set, gates []ledger.Gate, lat map[string]latency.Summary) string {
	if reg.Source() == roles.SourceLegacy {
		return FormatCandidatesLatency(set, gates, lat)
	}
	return formatCandidatesLatency(set, gates, lat, func(c candidate.Candidate) string {
		var served []string
		for _, name := range reg.Names() {
			if reg.Serves(name, c.Ref()) {
				served = append(served, name)
			}
		}
		if len(served) == 0 {
			return "(no role)"
		}
		return strings.Join(served, ", ")
	}, false)
}

// formatCandidatesLatency is the one line renderer behind both forms: rolesFor
// renders the roles column, and withTier prints the candidate's own tier
// segment, which only legacy mode does (#374 §3.2).
func formatCandidatesLatency(set *candidate.Set, gates []ledger.Gate, lat map[string]latency.Summary, rolesFor func(candidate.Candidate) string, withTier bool) string {
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
		sb.WriteString(fmt.Sprintf("%-*s  %s", width, ref, rolesFor(c)))
		if withTier && c.Tier != "" {
			sb.WriteString("   tier: " + c.Tier)
		}
		if len(c.ExtraArgs) > 0 {
			sb.WriteString("   [" + strings.Join(c.ExtraArgs, " ") + "]")
		}
		if s, ok := lat[ref]; ok && s.N > 0 {
			sb.WriteString(fmt.Sprintf("   ttft p50 %s (n=%d, 30d)", probeMS(s.TTFTP50MS), s.N))
		}
		h, _ := harness.Lookup(c.Harness)
		if flag := h.ExtraArgsPermissionFlag(c.ExtraArgs); flag != "" {
			sb.WriteString(fmt.Sprintf(`   note: extra_args carries %s; launches at tier harness only -- move it to "tier"`, flag))
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
