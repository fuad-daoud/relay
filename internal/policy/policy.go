// Package policy loads policy.json: where the planner tells relay how to
// choose among candidates. This step carries order[role] only; later #61
// steps add the scoring knobs -- weights, floor, providers[].peak,
// max_switches, cooldown -- each arriving with the step that reads it
// (spec §1 "Why this is not #61's step 2 as written").
package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
)

// ErrBadPolicy reports a policy.json that does not validate.
var ErrBadPolicy = errors.New("bad policy")

// Policy is the planner's candidate preferences, loaded from policy.json.
type Policy struct {
	// Order maps a role to its preferred candidate tokens, most preferred
	// first. A role absent here is unordered, and the resolver refuses to
	// choose among several candidates that serve it.
	Order map[string][]string `json:"order,omitempty"`

	// MaxSwitches is how many times the daemon may replace a builder within
	// one round before the binding goes NEEDS YOU; nil is DefaultMaxSwitches;
	// 0 disables switching.
	MaxSwitches *int `json:"max_switches,omitempty"`
}

// DefaultMaxSwitches is the switch limit used when MaxSwitches is nil: two
// replacements cover "the first pick was gated and the second failed to
// spawn"; a third in one round is a pattern a human should see.
const DefaultMaxSwitches = 2

// SwitchLimit is MaxSwitches with the default applied.
func (p Policy) SwitchLimit() int {
	if p.MaxSwitches == nil {
		return DefaultMaxSwitches
	}
	return *p.MaxSwitches
}

// Load reads and validates a policy file. A missing file is the zero Policy
// and no error, so every machine without a policy.json behaves exactly as it
// did before this file existed. A present file that does not validate is an
// error wrapping ErrBadPolicy: Load checks only the file's own shape -- it
// never opens candidates.json, so a token naming no configured candidate is
// tolerated here and caught later, by the resolver and by PolicyWarnings.
func Load(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Policy{}, nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("read %s: %w", path, err)
	}

	var p Policy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("%s: %v: %w", path, err, ErrBadPolicy)
	}

	if p.MaxSwitches != nil && *p.MaxSwitches < 0 {
		return Policy{}, fmt.Errorf("%s: max_switches: must be >= 0, got %d: %w", path, *p.MaxSwitches, ErrBadPolicy)
	}

	roles := make([]string, 0, len(p.Order))
	for role := range p.Order {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	for _, role := range roles {
		if _, ok := harness.RoleByName(role); !ok {
			return Policy{}, fmt.Errorf("%s: order.%s: unknown role (known: %v): %w", path, role, harness.RoleNames(), ErrBadPolicy)
		}

		tokens := p.Order[role]
		if tokens == nil {
			return Policy{}, fmt.Errorf("%s: order.%s: must be an array: %w", path, role, ErrBadPolicy)
		}

		seen := make(map[string]bool, len(tokens))
		for i, tok := range tokens {
			if _, err := candidate.ParseRef(tok); err != nil {
				return Policy{}, fmt.Errorf("%s: order.%s[%d]: %v: %w", path, role, i, err, ErrBadPolicy)
			}
			if seen[tok] {
				return Policy{}, fmt.Errorf("%s: order.%s[%d]: duplicate token %q: %w", path, role, i, tok, ErrBadPolicy)
			}
			seen[tok] = true
		}
	}

	return p, nil
}

// OrderFor returns role's preferred candidate tokens, most preferred first,
// or nil when the role has no entry. The result is a copy, so a caller
// cannot reorder the loaded policy by accident.
func (p Policy) OrderFor(role string) []string {
	return append([]string(nil), p.Order[role]...)
}
