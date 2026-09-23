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
	"net/url"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
)

// ErrBadPolicy reports a policy.json that does not validate.
var ErrBadPolicy = errors.New("bad policy")

// memoryMaxPattern is serve.scope.memory_max's shape: digits with an
// optional single-letter unit suffix (K, M, G, T), the systemd MemoryMax=
// grammar this value is passed straight through to.
var memoryMaxPattern = regexp.MustCompile(`^[0-9]+[KMGT]?$`)

// cpuQuotaPattern is cpu_quota's shape: digits with a percent sign, the
// systemd CPUQuota= grammar this value is passed straight through to
// ("200%" = two cores' worth).
var cpuQuotaPattern = regexp.MustCompile(`^[0-9]+%$`)

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

	// ProgressIntervalMS is how often the daemon samples a binding's progress
	// signals while its round is open (#135). nil is DefaultProgressInterval;
	// a present value must be > 0.
	ProgressIntervalMS *int `json:"progress_interval_ms,omitempty"`

	// ExploreAfterMS is how long a builder's output or screen may keep moving
	// while its tree has not before relay labels it exploring (#135). nil is
	// DefaultExploreAfter; a present value must be > 0.
	ExploreAfterMS *int `json:"explore_after_ms,omitempty"`

	// StaleAfterMS is how long a NEEDS YOU or HELD binding may sit unacted
	// before relay labels it stale (#135). nil is DefaultStaleAfter; a present
	// value must be > 0.
	StaleAfterMS *int `json:"stale_after_ms,omitempty"`

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

	// Verify configures the default for `relay send --verify` (#144): when
	// verify.default is true, a plain `relay send` marks the round for a
	// read-only reviewer at round close.
	Verify *VerifyPolicy `json:"verify,omitempty"`

	// Notify configures webhook sinks that receive lifecycle events over
	// HTTP, beside the hooks.d script dispatcher (#4).
	Notify *NotifyPolicy `json:"notify,omitempty"`

	// Serve configures relay serve (#285). nil is every default.
	Serve *ServePolicy `json:"serve,omitempty"`

	// Scope is the systemd scope template for rounds this host runs. Serve.Scope
	// replaces it entirely for served rounds (#295). nil means defaults, not off.
	Scope *ScopePolicy `json:"scope,omitempty"`
}

// ServePolicy configures relay serve (#285).
type ServePolicy struct {
	// MaxBuilders caps headless builders running at once across all
	// owners. nil = max(1, runtime.NumCPU()-1). A value below 1 is a Load error.
	MaxBuilders *int `json:"max_builders,omitempty"`
	// Scope is the per-round systemd scope (part 3 uses it). nil = defaults.
	Scope *ScopePolicy `json:"scope,omitempty"`
}

// ScopePolicy configures the per-round systemd scope a served headless
// builder runs under (part 3 uses this; defined and validated here).
type ScopePolicy struct {
	Enabled   *bool  `json:"enabled,omitempty"`    // nil = true
	Slice     string `json:"slice,omitempty"`      // "" = systemd default; else must end in ".slice"
	CPUWeight int    `json:"cpu_weight,omitempty"` // 0 = 100; else 1..10000
	MemoryMax string `json:"memory_max,omitempty"` // "" = none; else ^[0-9]+[KMGT]?$
	CPUQuota  string `json:"cpu_quota,omitempty"`  // "" = none; else ^[0-9]+%$, at least 1%
	// GateCPUQuota is the gate's own CPU ceiling (#313). "" means the gate
	// uses CPUQuota, the same as a round. Otherwise it must match
	// ^[0-9]+%$ and be at least 1%, the same grammar as CPUQuota.
	GateCPUQuota string `json:"gate_cpu_quota,omitempty"`
	TasksMax     int    `json:"tasks_max,omitempty"` // 0 = none; else >= 1
}

// NotifyPolicy configures webhook delivery of lifecycle events (#4).
type NotifyPolicy struct {
	Webhooks []Webhook `json:"webhooks,omitempty"`
}

// Webhook is one HTTP sink that receives a JSON POST for each event it
// matches (#4).
type Webhook struct {
	// URL is where the event is POSTed; required, http:// or https://.
	URL string `json:"url"`
	// Events filters which events reach this webhook. Empty means every
	// event. "state_changed:<state>" matches a state_changed event whose
	// State equals <state> (e.g. "state_changed:needs_you"); the ":<state>"
	// suffix is only valid on state_changed.
	Events []string `json:"events,omitempty"`
	// Format shapes the POST body: "json" (default) sends the event as
	// JSON; "slack" sends {"text": ...}; "discord" sends {"content": ...}.
	Format string `json:"format,omitempty"`
}

// VerifyPolicy configures the default verify flag for the rounds `relay
// send` opens (#144).
type VerifyPolicy struct {
	// Default is what Send uses when neither --verify nor --no-verify was
	// given. Absent, or false, means no reviewer: a human opts in.
	Default bool `json:"default,omitempty"`
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

// DefaultProgressInterval is how often the daemon samples a binding's progress
// signals while its round is open (#135): often enough that a label appears
// promptly, rare enough that the sampling costs one git status and one screen
// read per binding per interval.
const DefaultProgressInterval = 30 * time.Second

// DefaultExploreAfter is how long a builder's output or screen may keep moving
// while its tree has not before relay labels it exploring (#135): long enough
// that a read-heavy plan is never labelled, short enough that a plan which has
// stopped writing is.
const DefaultExploreAfter = 20 * time.Minute

// DefaultStaleAfter is how long a NEEDS YOU or HELD binding may sit unacted
// before relay labels it stale (#135): hours, not minutes, because a human's
// decision may wait on their next working day.
const DefaultStaleAfter = 4 * time.Hour

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

// ProgressInterval is ProgressIntervalMS converted to time.Duration with the
// default applied (#135).
func (p Policy) ProgressInterval() time.Duration {
	if p.ProgressIntervalMS == nil {
		return DefaultProgressInterval
	}
	return time.Duration(*p.ProgressIntervalMS) * time.Millisecond
}

// ExploreAfter is ExploreAfterMS converted to time.Duration with the default
// applied (#135).
func (p Policy) ExploreAfter() time.Duration {
	if p.ExploreAfterMS == nil {
		return DefaultExploreAfter
	}
	return time.Duration(*p.ExploreAfterMS) * time.Millisecond
}

// StaleAfter is StaleAfterMS converted to time.Duration with the default
// applied (#135).
func (p Policy) StaleAfter() time.Duration {
	if p.StaleAfterMS == nil {
		return DefaultStaleAfter
	}
	return time.Duration(*p.StaleAfterMS) * time.Millisecond
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

// VerifyDefault is verify.default with the absent-file case applied: false
// when Verify is nil, so a machine without `verify` in policy.json behaves
// exactly as it did before the key existed (#144).
func (p Policy) VerifyDefault() bool {
	if p.Verify == nil {
		return false
	}
	return p.Verify.Default
}

// MaxBuildersOrDefault is serve.max_builders with the default applied:
// max(1, runtime.NumCPU()-1) when Serve is nil or MaxBuilders is nil (#285).
func (p Policy) MaxBuildersOrDefault() int {
	if p.Serve == nil || p.Serve.MaxBuilders == nil {
		if n := runtime.NumCPU() - 1; n > 1 {
			return n
		}
		return 1
	}
	return *p.Serve.MaxBuilders
}

// ScopeFor returns the scope block that applies to a context: a served
// round takes Serve.Scope when set, else the top-level Scope. nil means
// defaults (enabled, no limits) -- never "disabled".
func (p Policy) ScopeFor(served bool) *ScopePolicy {
	if served && p.Serve != nil && p.Serve.Scope != nil {
		return p.Serve.Scope
	}
	return p.Scope
}

// validateScope checks one ScopePolicy block (top-level "scope", or
// "serve.scope") with prefix naming the block's JSON path in the error text.
// A nil block is not an error: absent means defaults (#295).
func validateScope(path, prefix string, sc *ScopePolicy) error {
	if sc == nil {
		return nil
	}
	if sc.Slice != "" && !strings.HasSuffix(sc.Slice, ".slice") {
		return fmt.Errorf("%s: %s.slice: must end in \".slice\", got %q: %w", path, prefix, sc.Slice, ErrBadPolicy)
	}
	if sc.CPUWeight != 0 && (sc.CPUWeight < 1 || sc.CPUWeight > 10000) {
		return fmt.Errorf("%s: %s.cpu_weight: must be 1..10000, got %d: %w", path, prefix, sc.CPUWeight, ErrBadPolicy)
	}
	if sc.MemoryMax != "" && !memoryMaxPattern.MatchString(sc.MemoryMax) {
		return fmt.Errorf("%s: %s.memory_max: must match ^[0-9]+[KMGT]?$, got %q: %w", path, prefix, sc.MemoryMax, ErrBadPolicy)
	}
	if sc.CPUQuota != "" {
		if !cpuQuotaPattern.MatchString(sc.CPUQuota) {
			return fmt.Errorf("%s: %s.cpu_quota: must match ^[0-9]+%%$, got %q: %w", path, prefix, sc.CPUQuota, ErrBadPolicy)
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(sc.CPUQuota, "%")); err == nil && n < 1 {
			return fmt.Errorf("%s: %s.cpu_quota: must be at least 1%%, got %q: %w", path, prefix, sc.CPUQuota, ErrBadPolicy)
		}
	}
	if sc.GateCPUQuota != "" {
		if !cpuQuotaPattern.MatchString(sc.GateCPUQuota) {
			return fmt.Errorf("%s: %s.gate_cpu_quota: must match ^[0-9]+%%$, got %q: %w", path, prefix, sc.GateCPUQuota, ErrBadPolicy)
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(sc.GateCPUQuota, "%")); err == nil && n < 1 {
			return fmt.Errorf("%s: %s.gate_cpu_quota: must be at least 1%%, got %q: %w", path, prefix, sc.GateCPUQuota, ErrBadPolicy)
		}
	}
	if sc.TasksMax != 0 && sc.TasksMax < 1 {
		return fmt.Errorf("%s: %s.tasks_max: must be at least 1, got %d: %w", path, prefix, sc.TasksMax, ErrBadPolicy)
	}
	return nil
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

	if p.ProgressIntervalMS != nil && *p.ProgressIntervalMS <= 0 {
		return Policy{}, fmt.Errorf("%s: progress_interval_ms: must be > 0, got %d: %w", path, *p.ProgressIntervalMS, ErrBadPolicy)
	}

	if p.ExploreAfterMS != nil && *p.ExploreAfterMS <= 0 {
		return Policy{}, fmt.Errorf("%s: explore_after_ms: must be > 0, got %d: %w", path, *p.ExploreAfterMS, ErrBadPolicy)
	}

	if p.StaleAfterMS != nil && *p.StaleAfterMS <= 0 {
		return Policy{}, fmt.Errorf("%s: stale_after_ms: must be > 0, got %d: %w", path, *p.StaleAfterMS, ErrBadPolicy)
	}

	if p.Gate != nil && p.Gate.TimeoutMS != nil && *p.Gate.TimeoutMS <= 0 {
		return Policy{}, fmt.Errorf("%s: gate.timeout_ms: must be > 0, got %d: %w", path, *p.Gate.TimeoutMS, ErrBadPolicy)
	}

	if p.Gate != nil && p.Gate.Regate != nil && *p.Gate.Regate < 0 {
		return Policy{}, fmt.Errorf("%s: gate.regate: must be >= 0, got %d: %w", path, *p.Gate.Regate, ErrBadPolicy)
	}

	if p.Serve != nil && p.Serve.MaxBuilders != nil && *p.Serve.MaxBuilders < 1 {
		return Policy{}, fmt.Errorf("%s: serve.max_builders: must be at least 1, got %d: %w", path, *p.Serve.MaxBuilders, ErrBadPolicy)
	}

	if err := validateScope(path, "scope", p.Scope); err != nil {
		return Policy{}, err
	}
	if p.Serve != nil {
		if err := validateScope(path, "serve.scope", p.Serve.Scope); err != nil {
			return Policy{}, err
		}
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

	if p.Notify != nil {
		knownEvents := map[string]bool{
			"state_changed":   true,
			"round_started":   true,
			"fork_created":    true,
			"builder_stalled": true,
			"binding_stale":   true,
		}

		for i, hook := range p.Notify.Webhooks {
			u, err := url.Parse(hook.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return Policy{}, fmt.Errorf("%s: notify.webhooks[%d].url: must be an http or https URL, got %q: %w", path, i, hook.URL, ErrBadPolicy)
			}

			switch hook.Format {
			case "", "json", "slack", "discord":
			default:
				return Policy{}, fmt.Errorf("%s: notify.webhooks[%d].format: unknown %q (known: json, slack, discord): %w", path, i, hook.Format, ErrBadPolicy)
			}

			for j, ev := range hook.Events {
				name, _, hasState := strings.Cut(ev, ":")
				if !knownEvents[name] {
					return Policy{}, fmt.Errorf("%s: notify.webhooks[%d].events[%d]: unknown event %q (known: state_changed, round_started, fork_created, builder_stalled, binding_stale): %w", path, i, j, ev, ErrBadPolicy)
				}
				if hasState && name != "state_changed" {
					return Policy{}, fmt.Errorf("%s: notify.webhooks[%d].events[%d]: %q: only state_changed accepts a :<state> suffix: %w", path, i, j, ev, ErrBadPolicy)
				}
			}
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
