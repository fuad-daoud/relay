package relay

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relay/internal/candidate"
)

// ErrNoCandidates reports that no candidates are configured.
var ErrNoCandidates = errors.New("no candidates configured")

// ErrRoleNotServed reports that no configured candidate serves the requested role.
var ErrRoleNotServed = errors.New("no candidate serves role")

// ErrAmbiguousCandidate reports that multiple candidates serve the requested role and none was specified.
var ErrAmbiguousCandidate = errors.New("more than one candidate serves role")

// ErrUnknownRole reports a role that is not in the known role table.
var ErrUnknownRole = errors.New("unknown role")

// resolveCandidate is the one rule for an omitted candidate token, shared by
// bind, add, fork and ask: a named token is looked up and must serve the
// role; with no token, exactly one configured candidate serving the role is
// used, and anything else is refused with the list. It is deterministic and
// makes no judgement -- the refusal is the seam #61 fills with a scorer.
func resolveCandidate(set *candidate.Set, token, role string) (candidate.Candidate, error) {
	if token != "" {
		ref, err := candidate.ParseRef(token)
		if err != nil {
			return candidate.Candidate{}, err
		}
		c, err := set.Lookup(ref)
		if err != nil {
			return candidate.Candidate{}, err
		}
		if !c.Serves(role) {
			return candidate.Candidate{}, fmt.Errorf("candidate %q does not serve role %q (its roles: %v): %w", token, role, c.Roles, ErrRoleNotServed)
		}
		return c, nil
	}

	if set.Len() == 0 {
		return candidate.Candidate{}, fmt.Errorf("%w; write ~/.config/relay/candidates.json (see README \"Candidates\")", ErrNoCandidates)
	}

	cs := set.ForRole(role)
	if len(cs) == 0 {
		return candidate.Candidate{}, fmt.Errorf("no configured candidate serves role %q (configured: %v): %w", role, set.Refs(), ErrRoleNotServed)
	}
	if len(cs) == 1 {
		return cs[0], nil
	}

	refs := make([]string, 0, len(cs))
	for _, c := range cs {
		refs = append(refs, c.Ref().String())
	}
	return candidate.Candidate{}, fmt.Errorf("%d candidates serve %q: %v; name one with --builder or --candidate: %w", len(cs), role, refs, ErrAmbiguousCandidate)
}
