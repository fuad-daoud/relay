package relay

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrNoCandidates reports that no candidates are configured.
var ErrNoCandidates = errors.New("no candidates configured")

// ErrRoleNotServed reports that no configured candidate serves the requested role.
var ErrRoleNotServed = errors.New("no candidate serves role")

// ErrAmbiguousCandidate reports that multiple candidates serve the requested role and none was specified.
var ErrAmbiguousCandidate = errors.New("more than one candidate serves role")

// ErrUnknownRole reports a role that is not in the known role table.
var ErrUnknownRole = errors.New("unknown role")

// ErrAllGated reports that every candidate serving the requested role is
// currently gated by the ledger, and no token was named to bypass the check.
var ErrAllGated = errors.New("every candidate serving the role is gated")

// How names the rule that picked a Resolution's candidate.
type How string

const (
	// HowExplicit means the token was named by the planner (or inherited by
	// fork); gates were not consulted for the decision.
	HowExplicit How = "explicit"
	// HowSole means the candidate was the only one serving the role, and it
	// was ungated.
	HowSole How = "sole"
	// HowOrder means the candidate was taken from order[role]; Position is
	// its 1-based index in that list.
	HowOrder How = "order"
	// HowUnlisted means the candidate serves the role but is not in
	// order[role], and was reached after every listed candidate was gated.
	HowUnlisted How = "unlisted"
)

// Skip is one gate that caused a resolution to pass over a candidate.
type Skip struct {
	Token string
	Kind  ledger.Kind
	Until time.Time // zero = until cleared
}

// Resolution is what resolveCandidate picked and why. T3 records it in the
// binding's log; T4 renders it in `relay policy`.
type Resolution struct {
	Candidate candidate.Candidate
	How       How
	// Position is the candidate's 1-based index in order[role] when
	// How == HowOrder, or 0 otherwise.
	Position int
	// Skipped is every gated candidate passed over, in the order they were
	// passed; nil for HowExplicit.
	Skipped []Skip
	// Gates is, for HowExplicit only, the live gates on the named candidate,
	// so a bypass is recorded even though it did not affect the decision.
	Gates []Skip
	// InheritedFrom is, for a fork that inherited its source's
	// BuilderCandidate, the source binding's name; "" otherwise.
	InheritedFrom string
}

// Token is the canonical ref of the resolved candidate, or "" when there
// was no resolution (an adopted pane has no candidate).
func (r Resolution) Token() string {
	if r.How == "" {
		return ""
	}
	return r.Candidate.Ref().String()
}

// rankedEntry is one row of the walk; T4's FormatPolicy renders the same rows.
type rankedEntry struct {
	Candidate candidate.Candidate
	How       How // HowOrder or HowUnlisted
	Position  int // 1-based index in order[role] for HowOrder; 0 for HowUnlisted
}

// rankedList is the order's configured, serving entries first, then every
// other serving candidate in ref order; an order entry relay cannot use is
// skipped here and reported by PolicyWarnings (T4), never an error, because
// removing a candidate must not break every subcommand (spec §3.1).
func rankedList(set *candidate.Set, pol policy.Policy, role string) []rankedEntry {
	var out []rankedEntry
	seen := make(map[string]bool)

	for i, tok := range pol.OrderFor(role) {
		ref, err := candidate.ParseRef(tok)
		if err != nil {
			continue
		}
		c, err := set.Lookup(ref)
		if err != nil {
			continue
		}
		if !c.Serves(role) {
			continue
		}
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, rankedEntry{Candidate: c, How: HowOrder, Position: i + 1})
	}

	for _, c := range set.ForRole(role) {
		if seen[c.Ref().String()] {
			continue
		}
		out = append(out, rankedEntry{Candidate: c, How: HowUnlisted})
	}

	return out
}

// skipsFor is one Skip per gate whose Token == token, in gates order.
func skipsFor(gates []ledger.Gate, token string) []Skip {
	var out []Skip
	for _, g := range gates {
		if g.Token != token {
			continue
		}
		out = append(out, Skip{Token: g.Token, Kind: g.Kind, Until: g.Until})
	}
	return out
}

// skipText renders one Skip as "<token> (<kind> <until>)".
func skipText(s Skip) string {
	return fmt.Sprintf("%s (%s %s)", s.Token, GateKindText(s.Kind), GateUntilText(s.Until))
}

// uniqStrings drops later duplicates, keeping first occurrences in order.
// Gate texts are de-duplicated at render time only (#93): the ledger keeps
// every `relay unavailable` entry and resolveCandidate yields one Skip per
// gate, but a row or a pick line says each distinct text once.
func uniqStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// resolveCandidate is the one rule for an omitted candidate token, shared by
// bind, add, fork and ask: a named token is looked up and must serve the
// role, and gates never refuse it -- they are only recorded. With no token,
// it is the first ungated candidate in order[role], then the unlisted ones;
// it refuses only when everything serving the role is gated, or when
// nothing is ordered and several serve (the seam #61 step 3 fills). It is
// pure and deterministic: it takes gates as a value and never opens the
// ledger itself.
func resolveCandidate(set *candidate.Set, pol policy.Policy, gates []ledger.Gate, token, role string) (Resolution, error) {
	if token != "" {
		ref, err := candidate.ParseRef(token)
		if err != nil {
			return Resolution{}, err
		}
		c, err := set.Lookup(ref)
		if err != nil {
			return Resolution{}, err
		}
		if !c.Serves(role) {
			return Resolution{}, fmt.Errorf("candidate %q does not serve role %q (its roles: %v): %w", token, role, c.Roles, ErrRoleNotServed)
		}
		return Resolution{Candidate: c, How: HowExplicit, Gates: skipsFor(gates, c.Ref().String())}, nil
	}

	if set.Len() == 0 {
		return Resolution{}, fmt.Errorf("%w; write ~/.config/relay/candidates.json (see README \"Candidates\")", ErrNoCandidates)
	}

	serving := set.ForRole(role)
	if len(serving) == 0 {
		return Resolution{}, fmt.Errorf("no configured candidate serves role %q (configured: %v): %w", role, set.Refs(), ErrRoleNotServed)
	}
	if len(serving) == 1 {
		if s := skipsFor(gates, serving[0].Ref().String()); len(s) > 0 {
			return allGated(role, s)
		}
		return Resolution{Candidate: serving[0], How: HowSole}, nil
	}

	if len(pol.OrderFor(role)) == 0 {
		refs := make([]string, 0, len(serving))
		for _, c := range serving {
			refs = append(refs, c.Ref().String())
		}
		return Resolution{}, fmt.Errorf("%d candidates serve %q: %v; name one with --builder or --candidate, or set order.%s in ~/.config/relay/policy.json: %w", len(serving), role, refs, role, ErrAmbiguousCandidate)
	}

	var skipped []Skip
	for _, r := range rankedList(set, pol, role) {
		s := skipsFor(gates, r.Candidate.Ref().String())
		if len(s) == 0 {
			return Resolution{Candidate: r.Candidate, How: r.How, Position: r.Position, Skipped: skipped}, nil
		}
		skipped = append(skipped, s...)
	}
	return allGated(role, skipped)
}

// allGated builds the ErrAllGated resolution: every candidate serving role
// was gated, in skipped.
func allGated(role string, skipped []Skip) (Resolution, error) {
	texts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		texts = append(texts, skipText(s))
	}
	return Resolution{}, fmt.Errorf("every candidate serving %q is gated: %s; name one with --builder to bypass, or clear a gate with relay available <provider>: %w", role, strings.Join(uniqStrings(texts), ", "), ErrAllGated)
}

// ExplainResolution is the one line that says what was picked and why.
// The pick log entry, the stderr line after a spawn, and `relay policy`
// all render from it, so a pick the planner reads in `relay log` is
// word-for-word what bind printed (spec §1 principle 1).
func ExplainResolution(role string, res Resolution) string {
	head := "picked " + res.Token() + " for " + role + ": "

	var body string
	switch res.How {
	case HowSole:
		body = "sole candidate"
	case HowOrder:
		body = fmt.Sprintf("order #%d", res.Position)
	case HowUnlisted:
		body = "unlisted, after order"
	case HowExplicit:
		if res.InheritedFrom != "" {
			body = "explicit, inherited from " + res.InheritedFrom + ", policy bypassed"
		} else {
			body = "explicit, policy bypassed"
		}
	}

	out := head + body

	if len(res.Skipped) > 0 {
		texts := make([]string, 0, len(res.Skipped))
		for _, s := range res.Skipped {
			texts = append(texts, skipText(s))
		}
		out += "; skipped " + strings.Join(uniqStrings(texts), ", ")
	}

	if res.How == HowExplicit && len(res.Gates) > 0 {
		texts := make([]string, 0, len(res.Gates))
		for _, g := range res.Gates {
			texts = append(texts, GateKindText(g.Kind)+" "+GateUntilText(g.Until))
		}
		out += "; gated: " + strings.Join(uniqStrings(texts), ", ")
	}

	return out
}

// pickEntry is the log record of one resolution. Confirmed and bound for
// the planner so it is never mistaken for an undelivered payload; the
// note is ExplainResolution, so `relay log` reads exactly what bind
// printed (spec §3.2, §4.4).
func pickEntry(now time.Time, round int, role string, res Resolution) store.LogEntry {
	return store.LogEntry{
		TS: now.UTC(), Round: round, Direction: store.DirToPlanner,
		Kind: store.KindPick, Confirmed: true, Note: ExplainResolution(role, res),
	}
}

// CandidateKind returns the harness kind a bind with this token would start,
// for advisory preflight only; every error is reported as "" because the real
// resolution happens inside Bind and says why.
func CandidateKind(rt Runtime, token string) string {
	res, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), token, "builder")
	if err != nil {
		return ""
	}
	return res.Candidate.Harness
}
