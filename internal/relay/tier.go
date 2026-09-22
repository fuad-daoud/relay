package relay

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

var (
	ErrTierAboveMax = errors.New("tier exceeds max_tier")
)

// resolveTier is the chain: explicit, candidate.Tier, policy.TierFor(role),
// TierHarness. explicit and candidate values are already validated by their
// parsers; an unparseable stored value is treated as harness.
func resolveTier(explicit string, c candidate.Candidate, pol policy.Policy, role string) harness.Tier {
	if explicit != "" {
		if t, err := harness.ParseTier(explicit); err == nil {
			return t
		}
	}
	if c.Tier != "" {
		if t, err := harness.ParseTier(c.Tier); err == nil {
			return t
		}
	}
	if t, ok := pol.TierFor(role); ok {
		return t
	}
	return harness.TierHarness
}

// checkTierCap refuses tier when tier.Above(pol.MaxTierOrDefault()) and
// !allowYolo: `tier %s exceeds max_tier %s; pass --allow-yolo or raise
// max_tier in ~/.config/relay/policy.json`. harness never refuses.
func checkTierCap(tier harness.Tier, pol policy.Policy, allowYolo bool) error {
	maxTier := pol.MaxTierOrDefault()
	if tier.Above(maxTier) && !allowYolo {
		return fmt.Errorf("%w: tier %s exceeds max_tier %s; pass --allow-yolo or raise max_tier in ~/.config/relay/policy.json", ErrTierAboveMax, tier, maxTier)
	}
	return nil
}

// effectiveTier is RoundTier if set, else Tier, else harness.
func effectiveTier(b store.Binding) harness.Tier {
	if b.RoundTier != "" {
		if t, err := harness.ParseTier(b.RoundTier); err == nil {
			return t
		}
	}
	if b.Tier != "" {
		if t, err := harness.ParseTier(b.Tier); err == nil {
			return t
		}
	}
	return harness.TierHarness
}
