// Package candidate loads the harness/provider/model triples relevo may start,
// and nothing else: which one to start is the caller's choice (#80).
package candidate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// ErrBadRef reports a candidate reference that is not a harness/provider/model triple.
var ErrBadRef = errors.New("bad candidate reference")

// ErrUnknownCandidate reports a candidate reference not present in the set.
var ErrUnknownCandidate = errors.New("unknown candidate")

// Ref identifies one candidate as a harness/provider/model triple.
type Ref struct {
	Harness  string
	Provider string
	Model    string
}

// ParseRef splits a candidate reference string on its first two slashes into its
// harness, provider, and model parts. Fewer than 3 parts, or any empty part, is an error.
func ParseRef(s string) (Ref, error) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Ref{}, fmt.Errorf("%q: want harness/provider/model: %w", s, ErrBadRef)
	}
	return Ref{
		Harness:  parts[0],
		Provider: parts[1],
		Model:    parts[2],
	}, nil
}

// String returns the canonical harness/provider/model reference string.
func (r Ref) String() string {
	return r.Harness + "/" + r.Provider + "/" + r.Model
}

// Candidate describes one concrete way to fill a role.
type Candidate struct {
	Harness       string   `json:"harness"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Roles         []string `json:"roles"`
	Tree          string   `json:"tree,omitempty"`
	ExtraArgs     []string `json:"extra_args,omitempty"`
	LimitPatterns []string `json:"limit_patterns,omitempty"`
	// DialogPatterns is ignored since #303: pane dialogs and `relevo answer`
	// were deleted. The field stays decodable so existing candidates.json
	// files still load.
	DialogPatterns []string `json:"dialog_patterns,omitempty"`
	// Tier is the candidate's default permission tier (#141); "" means "use the
	// role default from policy, else harness". Validated by ParseTier.
	Tier string `json:"tier,omitempty"`
	// DenialPatterns replace the harness's default denial regexes for this
	// candidate (#141), like LimitPatterns and DialogPatterns.
	DenialPatterns []string `json:"denial_patterns,omitempty"`

	// Plan marks a subscription lane (#142): the round's cost is a quota
	// draw, and printers say "plan", never "$0" and never "free".
	Plan bool `json:"plan,omitempty"`
}

// Ref returns the candidate's canonical reference triple.
func (c Candidate) Ref() Ref {
	return Ref{
		Harness:  c.Harness,
		Provider: c.Provider,
		Model:    c.Model,
	}
}

// Serves reports whether this candidate lists the given role.
func (c Candidate) Serves(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Set is an immutable collection of validated candidates keyed by their
// canonical reference strings.
type Set struct {
	byRef map[string]Candidate
}

func newSet() *Set {
	return &Set{
		byRef: make(map[string]Candidate),
	}
}

// Lookup finds a candidate by its reference, returning ErrUnknownCandidate on a miss.
func (s *Set) Lookup(ref Ref) (Candidate, error) {
	c, ok := s.byRef[ref.String()]
	if !ok {
		return Candidate{}, fmt.Errorf("candidate %q not found (configured: %v): %w", ref.String(), s.Refs(), ErrUnknownCandidate)
	}
	return c, nil
}

// ForRole returns every candidate configured to serve the given role, sorted
// by their canonical reference strings.
func (s *Set) ForRole(role string) []Candidate {
	var matches []Candidate
	for _, c := range s.byRef {
		if c.Serves(role) {
			matches = append(matches, c)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Ref().String() < matches[j].Ref().String()
	})
	return matches
}

// Refs returns every configured candidate reference string, sorted for stable output.
func (s *Set) Refs() []string {
	refs := make([]string, 0, len(s.byRef))
	for k := range s.byRef {
		refs = append(refs, k)
	}
	sort.Strings(refs)
	return refs
}

// Len returns the number of configured candidates in the set.
func (s *Set) Len() int {
	return len(s.byRef)
}

// Providers returns every distinct provider the configured candidates use,
// sorted. A nil *Set returns nil: callers hold a possibly-nil set (a server
// with no candidates.json), and this must not panic on one.
func (s *Set) Providers() []string {
	if s == nil {
		return nil
	}

	seen := make(map[string]bool, len(s.byRef))
	var out []string
	for _, c := range s.byRef {
		if seen[c.Provider] {
			continue
		}
		seen[c.Provider] = true
		out = append(out, c.Provider)
	}
	sort.Strings(out)
	return out
}

// Load reads and validates candidate definitions from a JSON file, discarding
// the warnings LoadWithWarnings returns. A missing file is zero candidates and
// not an error, because relevo ships none (spec §1 point 3); a present file that
// does not validate is an error at startup for every subcommand, because a
// daemon running on config it cannot parse is worse than one that refuses to
// start (spec §6).
func Load(path string) (*Set, error) {
	set, _, err := LoadWithWarnings(path)
	return set, err
}

// LoadWithWarnings reads and validates candidate definitions, returning a
// warning for every candidate this relevo drops because its harness or one of
// its roles is unknown (#372 §4.4). A newer relevo's candidate must not stop
// this relevo, and the drop surfaces in `relevo doctor`.
//
// Only an unknown harness and an unknown role are skipped: duplicates, a bad
// tree, tier or pattern, and every other validation failure still fail the load.
func LoadWithWarnings(path string) (*Set, []string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newSet(), nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read candidates %s: %w", path, err)
	}

	var entries []Candidate
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, nil, fmt.Errorf("decode candidates %s: %w", path, err)
	}

	set := newSet()
	seen := make(map[string]int)
	base := filepath.Base(path)
	var warnings []string

	for i, c := range entries {
		if c.Harness == "" || c.Provider == "" || c.Model == "" {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: harness, provider and model are required", path, i)
		}
		if strings.Contains(c.Provider, "/") {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: provider must be a single segment", path, i)
		}
		h, ok := harness.Lookup(c.Harness)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: %s: unknown harness %q (skipped)", base, c.Ref().String(), c.Harness))
			continue
		}
		unknownRole := ""
		for _, r := range c.Roles {
			if _, ok := harness.RoleByName(r); !ok {
				unknownRole = r
				break
			}
			if !h.CanServe(r) {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: harness %q has no definition for role %q", path, i, c.Harness, r)
			}
		}
		if unknownRole != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s: unknown role %q (skipped)", base, c.Ref().String(), unknownRole))
			continue
		}
		if c.Tree != "" && c.Tree != "binding" && c.Tree != "none" {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: tree must be \"binding\" or \"none\"", path, i)
		}
		for j, pat := range c.LimitPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: limit_patterns[%d]: %w", path, i, j, err)
			}
		}
		for j, pat := range c.DialogPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: dialog_patterns[%d]: %w", path, i, j, err)
			}
		}
		if c.Tier != "" {
			if _, err := harness.ParseTier(c.Tier); err != nil {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: tier: %v", path, i, err)
			}
		}
		for j, pat := range c.DenialPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: denial_patterns[%d]: %w", path, i, j, err)
			}
		}
		key := c.Ref().String()
		if first, exists := seen[key]; exists {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: duplicate candidate %s at index %d and %d", path, i, key, first, i)
		}
		seen[key] = i
		set.byRef[key] = c
	}

	return set, warnings, nil
}
