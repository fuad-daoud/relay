package relay

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/policy"
)

// PolicyWarning is one inconsistency between order[role] and the configured
// candidates: a listed token that is not configured, a listed token that
// does not serve the role it is listed under, or a candidate that serves a
// role but is missing from that role's order. policy.Load deliberately does
// not check any of this against candidates.json (spec §3.1): an order entry
// naming a removed candidate must not stop every subcommand from starting.
// It is tolerated at resolve time instead, and reported here for `relay
// policy` and `doctor` to render.
type PolicyWarning struct {
	Role  string
	Index int // index into order[role]; -1 for an unlisted-candidate warning
	Token string
	Text  string // the rendered line
}

// PolicyWarnings is the one source of the three findings: relay policy's
// warnings block and doctor's policy rows both render from this function;
// neither computes its own.
func PolicyWarnings(set *candidate.Set, pol policy.Policy) []PolicyWarning {
	var out []PolicyWarning

	for _, role := range harness.RoleNames() {
		order := pol.OrderFor(role)
		if len(order) == 0 {
			continue
		}

		listed := make(map[string]bool)
		for i, tok := range order {
			ref, err := candidate.ParseRef(tok)
			if err != nil {
				out = append(out, PolicyWarning{
					Role:  role,
					Index: i,
					Token: tok,
					Text:  fmt.Sprintf("order.%s[%d] %q is not a configured candidate", role, i, tok),
				})
				continue
			}
			c, err := set.Lookup(ref)
			if err != nil {
				out = append(out, PolicyWarning{
					Role:  role,
					Index: i,
					Token: tok,
					Text:  fmt.Sprintf("order.%s[%d] %q is not a configured candidate", role, i, tok),
				})
				continue
			}
			if !c.Serves(role) {
				out = append(out, PolicyWarning{
					Role:  role,
					Index: i,
					Token: tok,
					Text:  fmt.Sprintf("order.%s[%d] %q does not serve %s (its roles: %v)", role, i, tok, role, c.Roles),
				})
				continue
			}
			listed[tok] = true
		}

		for _, c := range set.ForRole(role) {
			tok := c.Ref().String()
			if listed[tok] {
				continue
			}
			out = append(out, PolicyWarning{
				Role:  role,
				Index: -1,
				Token: tok,
				Text:  fmt.Sprintf("%s: %s serves the role but is not in order.%s", role, tok, role),
			})
		}
	}

	return out
}

// FormatPolicy renders, per role, what resolveCandidate would do right now
// and why -- computed by calling it, so the marker here can never disagree
// with what bind actually picks (spec §4.7). It is a listing, not a check:
// `relay policy` prints this and always exits 0.
func FormatPolicy(set *candidate.Set, pol policy.Policy, gates []ledger.Gate) string {
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
	for _, role := range harness.RoleNames() {
		serving := set.ForRole(role)
		ordered := len(pol.OrderFor(role)) > 0

		header := role
		if ordered {
			header += "  (order set in ~/.config/relay/policy.json)"
		} else {
			header += "  (no order set)"
		}
		sb.WriteString(header + "\n")

		if len(serving) == 0 {
			sb.WriteString("  no candidate serves this role\n")
			continue
		}

		var rows []rankedEntry
		if ordered {
			rows = rankedList(set, pol, role)
		} else {
			for _, c := range serving {
				rows = append(rows, rankedEntry{Candidate: c})
			}
		}

		res, err := resolveCandidate(set, pol, gates, "", role)

		for i, r := range rows {
			tag := string(r.How)
			if len(serving) == 1 {
				tag = "sole"
			}
			tok := r.Candidate.Ref().String()

			var gateTexts []string
			for _, g := range byToken[tok] {
				gateTexts = append(gateTexts, GateKindText(g.Kind)+" "+GateUntilText(g.Until))
			}
			tail := strings.Join(gateTexts, "; ")

			// This combination cannot occur: a gated row is never picked.
			if err == nil && tok == res.Token() {
				if tail != "" {
					tail += "  "
				}
				tail += "<- would pick"
			}

			row := fmt.Sprintf("  %d  %-*s  %-8s  %s", i+1, width, tok, tag, tail)
			sb.WriteString(strings.TrimRight(row, " ") + "\n")
		}

		switch {
		case errors.Is(err, ErrAmbiguousCandidate):
			sb.WriteString(fmt.Sprintf("  would refuse: %d candidates serve %s and no order is set\n", len(serving), role))
		case errors.Is(err, ErrAllGated):
			sb.WriteString(fmt.Sprintf("  would refuse: every candidate serving %s is gated\n", role))
		}
	}

	if warnings := PolicyWarnings(set, pol); len(warnings) > 0 {
		sb.WriteString("\nwarnings\n")
		for _, w := range warnings {
			sb.WriteString("  " + w.Text + "\n")
		}
	}

	if len(pol.Order) == 0 {
		sb.WriteString("no policy configured; write ~/.config/relay/policy.json (see README \"Policy\")\n")
	}

	return sb.String()
}
