package relay

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/history"
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
func FormatPolicy(set *candidate.Set, pol policy.Policy, gates []ledger.Gate, hist history.History, now time.Time, loc *time.Location) string {
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

			var tailParts []string
			if pt := peakText(hist, r.Candidate.Ref().Provider, now, loc); pt != "" {
				tailParts = append(tailParts, pt)
			}
			for _, g := range byToken[tok] {
				tailParts = append(tailParts, GateKindText(g.Kind)+" "+GateUntilText(g.Until))
			}
			tail := strings.Join(tailParts, "; ")

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

	if s := formatHistory(hist, loc); s != "" {
		sb.WriteString("\n" + s)
	}

	if len(pol.Order) == 0 {
		sb.WriteString("no policy configured; write ~/.config/relay/policy.json (see README \"Policy\")\n")
	}

	return sb.String()
}

// peakText is a candidate row's cue that its provider was recently
// rate-limited: "limited <n>x around <HH>:00 (30d)" for the local hour
// around now, or "" when the window around now saw none (spec §4.2).
// History never changes a pick -- resolveCandidate is untouched -- this is
// display only.
func peakText(hist history.History, provider string, now time.Time, loc *time.Location) string {
	c := history.HourCounts(hist, provider, ledger.RateLimited, loc)
	h := now.In(loc).Hour()
	n := c[(h+23)%24] + c[h] + c[(h+1)%24]
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("limited %dx around %02d:00 (30d)", n, h)
}

// formatHistory renders the "history (30d, local hours)" block: one row per
// (provider, kind) with at least one event in hist, providers sorted and
// RateLimited before SpawnFailed within a provider (spec §4.2). It returns
// "" when hist has no events, so a fresh install's `relay policy` prints
// nothing extra.
func formatHistory(hist history.History, loc *time.Location) string {
	if len(hist.Events) == 0 {
		return ""
	}

	type pair struct {
		provider string
		kind     ledger.Kind
	}
	seen := make(map[pair]bool)
	providerSeen := make(map[string]bool)
	var providers []string
	for _, e := range hist.Events {
		seen[pair{e.Provider, e.Kind}] = true
		if !providerSeen[e.Provider] {
			providerSeen[e.Provider] = true
			providers = append(providers, e.Provider)
		}
	}
	sort.Strings(providers)

	var sb strings.Builder
	sb.WriteString("history (30d, local hours)\n")

	var labels [24]string
	for h := range labels {
		labels[h] = fmt.Sprintf("%02d", h)
	}
	sb.WriteString(strings.Repeat(" ", 27) + strings.Join(labels[:], " ") + "\n")

	kinds := []ledger.Kind{ledger.RateLimited, ledger.SpawnFailed}
	for _, p := range providers {
		for _, k := range kinds {
			if !seen[pair{p, k}] {
				continue
			}
			counts := history.HourCounts(hist, p, k, loc)
			row := fmt.Sprintf("  %-10s %-13s", p, GateKindText(k))
			for _, n := range counts {
				cell := "."
				if n != 0 {
					cell = strconv.Itoa(n)
				}
				row += fmt.Sprintf(" %2s", cell)
			}
			sb.WriteString(row + "\n")
		}
	}

	return sb.String()
}
