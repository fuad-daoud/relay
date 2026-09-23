package relay

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrBadBuilder wraps every refusal of a `relay send --builder` token itself:
// an unknown candidate, a bad ref, a role it does not serve, roles_missing,
// or a tier above the cap. It exists so the server can map these to 422
// rather than 500 (#318).
var ErrBadBuilder = errors.New("send --builder")

// ResolveSendBuilder turns --builder's token into the resolution to apply, or
// nil for a no-op. An explicit token is resolved exactly as `relay add
// --builder` resolves one, so a gated candidate still resolves (its gates are
// recorded on the Resolution) and only roles_missing refuses (#238).
//
// It returns nil, nil when the token canonicalises to the binding's current
// candidate: naming the builder a binding already has changes nothing. Every
// error is wrapped with ErrBadBuilder, naming the token.
//
// Precondition: token != "". It is a pure read of the ledger and candidates.
func ResolveSendBuilder(rt Runtime, current, token string) (*Resolution, error) {
	res, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), token, "builder")
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrBadBuilder, token, err)
	}
	if res.Token() == current {
		return nil, nil
	}
	return &res, nil
}

// applyBuilder is the persist half of a `relay send --builder`: it moves the
// binding to res's candidate for this round and every later one, re-derives
// the binding's stored tier for the new candidate through the normal chain,
// and clears any round exclusion, which belongs to the old builder's round.
//
// It leaves Builder.Mode, AgentName and Server as they are. A candidate whose
// re-derived tier is above max_tier without allowYolo is refused with
// ErrBadBuilder, leaving the binding unchanged. Pure.
func applyBuilder(b store.Binding, res Resolution, pol policy.Policy, allowYolo bool) (store.Binding, error) {
	tier := resolveTier("", res.Candidate, pol, "builder")
	if err := checkTierCap(tier, pol, allowYolo); err != nil {
		return b, fmt.Errorf("%w %s: %w", ErrBadBuilder, res.Token(), err)
	}
	b.BuilderCandidate = res.Token()
	b.Builder.Kind = res.Candidate.Harness
	b.Tier = string(tier)
	b.RoundExcluded = nil
	return b, nil
}

// roundOpenIn reports whether the binding's round is open: a plan entry for
// the round with no report entry. It is the --builder refusal's own copy of
// the condition reconcile and Send check inline.
func roundOpenIn(entries []store.LogEntry, round int) bool {
	return HasEntry(entries, round, store.DirToBuilder, store.KindPlan) &&
		!HasEntry(entries, round, store.DirToPlanner, store.KindReport)
}
