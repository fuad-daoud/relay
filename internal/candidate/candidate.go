// Package candidate loads the harness/provider/model triples relay may start,
// and nothing else: which one to start is the caller's choice (#80).
package candidate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fuad-daoud/relay/internal/harness"
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
	Harness   string   `json:"harness"`
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	Roles     []string `json:"roles"`
	Tree      string   `json:"tree,omitempty"`
	ExtraArgs []string `json:"extra_args,omitempty"`
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

// Load reads and validates candidate definitions from a JSON file. A missing
// file is zero candidates and not an error, because relay ships none (spec §1
// point 3); a present file that does not validate is an error at startup for
// every subcommand, because a daemon running on config it cannot parse is
// worse than one that refuses to start (spec §6).
func Load(path string) (*Set, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newSet(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read candidates %s: %w", path, err)
	}

	var entries []Candidate
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode candidates %s: %w", path, err)
	}

	set := newSet()
	seen := make(map[string]int)

	var knownKinds []string
	for _, h := range harness.All() {
		knownKinds = append(knownKinds, h.Kind)
	}

	for i, c := range entries {
		if c.Harness == "" || c.Provider == "" || c.Model == "" {
			return nil, fmt.Errorf("candidates %s: candidate %d: harness, provider and model are required", path, i)
		}
		if strings.Contains(c.Provider, "/") {
			return nil, fmt.Errorf("candidates %s: candidate %d: provider must be a single segment", path, i)
		}
		h, ok := harness.Lookup(c.Harness)
		if !ok {
			return nil, fmt.Errorf("candidates %s: candidate %d: unknown harness %q (known: %v)", path, i, c.Harness, knownKinds)
		}
		if len(c.Roles) == 0 {
			return nil, fmt.Errorf("candidates %s: candidate %d: roles must not be empty", path, i)
		}
		for _, r := range c.Roles {
			if _, ok := harness.RoleByName(r); !ok {
				return nil, fmt.Errorf("candidates %s: candidate %d: unknown role %q (known: %v)", path, i, r, harness.RoleNames())
			}
			if !h.CanServe(r) {
				return nil, fmt.Errorf("candidates %s: candidate %d: harness %q has no definition for role %q", path, i, c.Harness, r)
			}
		}
		if c.Tree != "" && c.Tree != "binding" && c.Tree != "none" {
			return nil, fmt.Errorf("candidates %s: candidate %d: tree must be \"binding\" or \"none\"", path, i)
		}
		key := c.Ref().String()
		if first, exists := seen[key]; exists {
			return nil, fmt.Errorf("candidates %s: candidate %d: duplicate candidate %s at index %d and %d", path, i, key, first, i)
		}
		seen[key] = i
		set.byRef[key] = c
	}

	return set, nil
}
