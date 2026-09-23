package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

var (
	ErrTierAboveMax = errors.New("tier exceeds max_tier")
)

// resolveRoleTier is the chain: explicit, the registry's tier for the role,
// harness. explicit and candidate values are already validated by their
// parsers; an unparseable stored value is treated as harness. In legacy mode
// reg.TierFor is candidate.Tier then policy.TierFor(role), so the chain is
// unchanged; in file mode it is the role's row tier (#374 §4.4).
func resolveRoleTier(explicit string, c candidate.Candidate, reg *roles.Registry, role string) harness.Tier {
	if explicit != "" {
		if t, err := harness.ParseTier(explicit); err == nil {
			return t
		}
	}
	if t, ok := reg.TierFor(role, c); ok {
		return t
	}
	return harness.TierHarness
}

// resolveTier is resolveRoleTier over the legacy registry derived from pol,
// which is what every pre-roles.json caller meant. It stays for tests
// (#374 §4.4).
func resolveTier(explicit string, c candidate.Candidate, pol policy.Policy, role string) harness.Tier {
	return resolveRoleTier(explicit, c, legacyRegistry(nil, pol), role)
}

// checkTierCap refuses tier when tier.Above(pol.MaxTierOrDefault()) and
// !allowYolo: `tier %s exceeds max_tier %s; pass --allow-yolo or raise
// max_tier in ~/.config/relevo/policy.json`. harness never refuses.
func checkTierCap(tier harness.Tier, pol policy.Policy, allowYolo bool) error {
	maxTier := pol.MaxTierOrDefault()
	if tier.Above(maxTier) && !allowYolo {
		return fmt.Errorf("%w: tier %s exceeds max_tier %s; pass --allow-yolo or raise max_tier in ~/.config/relevo/policy.json", ErrTierAboveMax, tier, maxTier)
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
