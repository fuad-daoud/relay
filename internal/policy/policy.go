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
	"regexp"
	"sort"
	"time"

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

	// LimitGateDefaultMS is how long a matched limit gates the provider when
	// no reset time can be parsed from the matched line. nil is
	// DefaultLimitGate; a present value must be > 0.
	LimitGateDefaultMS *int `json:"limit_gate_default_ms,omitempty"`

	// StallAfterMS is how long a live headless builder's stream may go
	// without an event before relay labels it stalled (#252). nil is
	// DefaultStallAfter; a present value must be > 0.
	StallAfterMS *int `json:"stall_after_ms,omitempty"`

	// ScanPatterns is extra regular expressions appended to the built-in list
	// of instruction-shaped line patterns (#139).
	ScanPatterns []string `json:"scan_patterns,omitempty"`

	// Classify configures the optional classifier beside the regex scan (#211).
	Classify *Classify `json:"classify,omitempty"`

	// Tier maps a role name to its default tier (#141). Absent role -> no
	// default here. Every key must be a known role; every value must parse;
	// no value may be Above MaxTierOrDefault().
	Tier map[string]string `json:"tier,omitempty"`

	// MaxTier is the highest tier a command may request without --allow-yolo
	// (#141). "" is DefaultMaxTier. Must parse and must not be "harness".
	MaxTier string `json:"max_tier,omitempty"`

	// Gate configures the acceptance command relay runs on a binding's
	// completion marker when the binding itself has none (#132).
	Gate *GatePolicy `json:"gate,omitempty"`
}

// GatePolicy configures the default gate command and its timeout (#132).
type GatePolicy struct {
	Default   string `json:"default,omitempty"`    // "" = no gate unless --gate
	TimeoutMS *int   `json:"timeout_ms,omitempty"` // nil = DefaultGateTimeout; must be > 0
	// Regate is the default repair-round budget for new bindings (#132 part
	// 2): how many automatic repair rounds relay opens after a failing gate.
	// nil = 0 = no repair; must be >= 0 when present.
	Regate *int `json:"regate,omitempty"`
}

// Classify configures the optional classifier beside the regex scan (#211).
type Classify struct {
	Provider           string   `json:"provider"`                      // required; only "jev" is known
	Model              string   `json:"model,omitempty"`               // default DefaultClassifyModel
	InjectionThreshold *float64 `json:"injection_threshold,omitempty"` // default DefaultInjectionThreshold; must be > 0 and <= 1
	TimeoutMS          *int     `json:"timeout_ms,omitempty"`          // default DefaultClassifyTimeout; must be > 0
}

const DefaultClassifyModel = "jev-latest"
const DefaultInjectionThreshold = 0.7
const DefaultClassifyTimeout = 4 * time.Second

func (c *Classify) ModelName() string {
	if c == nil || c.Model == "" {
		return DefaultClassifyModel
	}
	return c.Model
}

func (c *Classify) Threshold() float64 {
	if c == nil || c.InjectionThreshold == nil {
		return DefaultInjectionThreshold
	}
	return *c.InjectionThreshold
}

func (c *Classify) Timeout() time.Duration {
	if c == nil || c.TimeoutMS == nil {
		return DefaultClassifyTimeout
	}
	return time.Duration(*c.TimeoutMS) * time.Millisecond
}

// DefaultMaxTier is the ceiling used when MaxTier is empty (#141).
const DefaultMaxTier = harness.TierEdit

// DefaultMaxSwitches is the switch limit used when MaxSwitches is nil: two
// replacements cover "the first pick was gated and the second failed to
// spawn"; a third in one round is a pattern a human should see.
const DefaultMaxSwitches = 2

// DefaultLimitGate is how long a matched limit gates the provider when no
// reset time can be parsed from the matched line and LimitGateDefaultMS is nil.
const DefaultLimitGate = time.Hour

// DefaultStallAfter is how long a live headless builder's stream may go
// without an event before relay labels it stalled (#252): long enough that a
// thinking builder is never labelled, short enough that a hung one is.
const DefaultStallAfter = 15 * time.Minute

// DefaultGateTimeout bounds one gate run when neither the binding nor
// policy.json's gate.timeout_ms sets one (#132).
const DefaultGateTimeout = 10 * time.Minute

// SwitchLimit is MaxSwitches with the default applied.
func (p Policy) SwitchLimit() int {
	if p.MaxSwitches == nil {
		return DefaultMaxSwitches
	}
	return *p.MaxSwitches
}

// LimitGateDefault is LimitGateDefaultMS converted to time.Duration with the
// default applied.
func (p Policy) LimitGateDefault() time.Duration {
	if p.LimitGateDefaultMS == nil {
		return DefaultLimitGate
	}
	return time.Duration(*p.LimitGateDefaultMS) * time.Millisecond
}

// StallAfter is StallAfterMS converted to time.Duration with the default
// applied (#252).
func (p Policy) StallAfter() time.Duration {
	if p.StallAfterMS == nil {
		return DefaultStallAfter
	}
	return time.Duration(*p.StallAfterMS) * time.Millisecond
}

// MaxTierOrDefault returns MaxTier as a harness.Tier, or DefaultMaxTier when empty (#141).
func (p Policy) MaxTierOrDefault() harness.Tier {
	if p.MaxTier == "" {
		return DefaultMaxTier
	}
	t, err := harness.ParseTier(p.MaxTier)
	if err != nil || t == harness.TierHarness {
		return DefaultMaxTier
	}
	return t
}

// TierFor returns the configured tier for role, or ok false if absent (#141).
func (p Policy) TierFor(role string) (harness.Tier, bool) {
	if p.Tier == nil {
		return "", false
	}
	val, ok := p.Tier[role]
	if !ok {
		return "", false
	}
	t, err := harness.ParseTier(val)
	if err != nil {
		return "", false
	}
	return t, true
}

// GateDefault is the gate command a binding gets when it does not set one
// itself, or "" when Gate is nil (#132).
func (p Policy) GateDefault() string {
	if p.Gate == nil {
		return ""
	}
	return p.Gate.Default
}

// GateTimeout is gate.timeout_ms converted to time.Duration with the default
// applied; safe on a nil Gate.
func (p Policy) GateTimeout() time.Duration {
	if p.Gate == nil || p.Gate.TimeoutMS == nil {
		return DefaultGateTimeout
	}
	return time.Duration(*p.Gate.TimeoutMS) * time.Millisecond
}

// GateRegate is gate.regate with the default applied: 0 (no automatic repair)
// when the key is absent, and safe on a nil Gate (#132 part 2).
func (p Policy) GateRegate() int {
	if p.Gate == nil || p.Gate.Regate == nil {
		return 0
	}
	return *p.Gate.Regate
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

	if p.LimitGateDefaultMS != nil && *p.LimitGateDefaultMS <= 0 {
		return Policy{}, fmt.Errorf("%s: limit_gate_default_ms: must be > 0, got %d: %w", path, *p.LimitGateDefaultMS, ErrBadPolicy)
	}

	if p.StallAfterMS != nil && *p.StallAfterMS <= 0 {
		return Policy{}, fmt.Errorf("%s: stall_after_ms: must be > 0, got %d: %w", path, *p.StallAfterMS, ErrBadPolicy)
	}

	if p.Gate != nil && p.Gate.TimeoutMS != nil && *p.Gate.TimeoutMS <= 0 {
		return Policy{}, fmt.Errorf("%s: gate.timeout_ms: must be > 0, got %d: %w", path, *p.Gate.TimeoutMS, ErrBadPolicy)
	}

	if p.Gate != nil && p.Gate.Regate != nil && *p.Gate.Regate < 0 {
		return Policy{}, fmt.Errorf("%s: gate.regate: must be >= 0, got %d: %w", path, *p.Gate.Regate, ErrBadPolicy)
	}

	for i, pat := range p.ScanPatterns {
		if _, err := regexp.Compile(pat); err != nil {
			return Policy{}, fmt.Errorf("%s: scan_patterns[%d]: %v: %w", path, i, err, ErrBadPolicy)
		}
	}

	if p.Classify != nil {
		if p.Classify.Provider == "" {
			return Policy{}, fmt.Errorf("%s: classify.provider: required: %w", path, ErrBadPolicy)
		}
		if p.Classify.Provider != "jev" {
			return Policy{}, fmt.Errorf("%s: classify.provider: unknown %q (known: jev): %w", path, p.Classify.Provider, ErrBadPolicy)
		}
		if p.Classify.InjectionThreshold != nil {
			v := *p.Classify.InjectionThreshold
			if v <= 0 || v > 1 {
				return Policy{}, fmt.Errorf("%s: classify.injection_threshold: must be in (0, 1], got %v: %w", path, v, ErrBadPolicy)
			}
		}
		if p.Classify.TimeoutMS != nil && *p.Classify.TimeoutMS <= 0 {
			return Policy{}, fmt.Errorf("%s: classify.timeout_ms: must be > 0, got %d: %w", path, *p.Classify.TimeoutMS, ErrBadPolicy)
		}
	}

	maxTier := DefaultMaxTier
	if p.MaxTier != "" {
		parsed, err := harness.ParseTier(p.MaxTier)
		if err != nil {
			return Policy{}, fmt.Errorf("%s: max_tier: %v: %w", path, err, ErrBadPolicy)
		}
		if parsed == harness.TierHarness {
			return Policy{}, fmt.Errorf("%s: max_tier: \"harness\" is not a cap: %w", path, ErrBadPolicy)
		}
		maxTier = parsed
	}

	tierRoles := make([]string, 0, len(p.Tier))
	for role := range p.Tier {
		tierRoles = append(tierRoles, role)
	}
	sort.Strings(tierRoles)

	for _, role := range tierRoles {
		if _, ok := harness.RoleByName(role); !ok {
			return Policy{}, fmt.Errorf("%s: tier.%s: unknown role (known: %v): %w", path, role, harness.RoleNames(), ErrBadPolicy)
		}
		val := p.Tier[role]
		parsed, err := harness.ParseTier(val)
		if err != nil {
			return Policy{}, fmt.Errorf("%s: tier.%s: %v: %w", path, role, err, ErrBadPolicy)
		}
		if parsed.Above(maxTier) {
			return Policy{}, fmt.Errorf("%s: tier.%s: %s exceeds max_tier %s; raise max_tier in the same file: %w", path, role, parsed, maxTier, ErrBadPolicy)
		}
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
