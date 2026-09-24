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
	// Name is the candidate's short name (cockpit A1 §3.1): unique among
	// candidates, at most 24 characters, and never containing "/". It is
	// optional in stored JSON: a missing name is derived at parse time.
	Name          string   `json:"name,omitempty"`
	Harness       string   `json:"harness"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Roles         []string `json:"roles"`
	Tree          string   `json:"tree,omitempty"`
	ExtraArgs     []string `json:"extra_args,omitempty"`
	LimitPatterns []string `json:"limit_patterns,omitempty"`
	// DialogPatterns is ignored since #303: pane dialogs were deleted. The
	// field stays decodable so existing candidates.json files still load.
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
	// byName maps every candidate's name to its canonical token. Parse fills
	// it, so every candidate in a parsed set has exactly one entry.
	byName map[string]string
}

func newSet() *Set {
	return &Set{
		byRef:  make(map[string]Candidate),
		byName: make(map[string]string),
	}
}

// Lookup finds a candidate by its reference, returning ErrUnknownCandidate on a miss.
func (s *Set) Lookup(ref Ref) (Candidate, error) {
	c, ok := s.byRef[ref.String()]
	if !ok {
		return Candidate{}, fmt.Errorf("candidate %q not found (configured: %s): %w", ref.String(), strings.Join(s.Names(), ", "), ErrUnknownCandidate)
	}
	return c, nil
}

// Resolve takes a user string to a candidate (cockpit A1 §3.1): a string
// containing "/" is parsed as a token and looked up by its triple; anything
// else is looked up by name. An unknown string errors with the known names
// listed, wrapping ErrUnknownCandidate.
func (s *Set) Resolve(str string) (Candidate, error) {
	if s == nil {
		return Candidate{}, fmt.Errorf("unknown candidate %q (no candidates configured): %w", str, ErrUnknownCandidate)
	}
	if strings.Contains(str, "/") {
		ref, err := ParseRef(str)
		if err != nil {
			return Candidate{}, err
		}
		return s.Lookup(ref)
	}
	if key, ok := s.byName[str]; ok {
		return s.byRef[key], nil
	}
	return Candidate{}, fmt.Errorf("unknown candidate %q (known: %s): %w", str, strings.Join(s.Names(), ", "), ErrUnknownCandidate)
}

// NameOf returns the name of the candidate whose canonical token is token, or
// token unchanged when the set does not hold it (including a nil set). It
// never errors.
func (s *Set) NameOf(token string) string {
	if s == nil {
		return token
	}
	c, ok := s.byRef[token]
	if !ok {
		return token
	}
	return c.Name
}

// NameFor returns the name of the candidate whose canonical token is token.
// ok reports whether the set holds that token: it is false for a token no
// longer configured, and for a nil set (A1 §4.4, round 3 F3). A caller uses
// it to leave a name field unset rather than carry a token in it. NameOf
// stays the never-failing display form.
func (s *Set) NameFor(token string) (string, bool) {
	if s == nil {
		return "", false
	}
	c, ok := s.byRef[token]
	if !ok {
		return "", false
	}
	return c.Name, true
}

// Names returns every candidate's name, sorted.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.byName))
	for n := range s.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
	return Parse(path, raw)
}

// Parse validates candidate definitions from data, naming them by name in
// every message. name is the file path when LoadWithWarnings calls it, so an
// existing message is unchanged; internal/config passes the stored section's
// file name. It returns the same warnings LoadWithWarnings documents.
//
// Every entry gets a name: an explicit one kept verbatim when valid, else the
// deterministic one DeriveNames computes. A skipped entry (unknown harness or
// role) still takes part in DeriveNames, so the names of the others do not
// shift when it is fixed.
func Parse(name string, data []byte) (*Set, []string, error) {
	var entries []Candidate
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, nil, fmt.Errorf("decode candidates %s: %w", name, err)
	}

	set := newSet()
	seen := make(map[string]int)
	seenNames := make(map[string]int)
	base := filepath.Base(name)
	var warnings []string
	names := DeriveNames(entries)

	providers := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Provider != "" {
			providers[e.Provider] = true
		}
	}

	for i, c := range entries {
		if c.Name != "" && !IsName(c.Name) {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: name %q: want ^[a-z0-9][a-z0-9.-]{0,23}$", name, i, c.Name)
		}
		c.Name = names[i]

		if c.Harness == "" || c.Provider == "" || c.Model == "" {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: harness, provider and model are required", name, i)
		}
		if strings.Contains(c.Provider, "/") {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: provider must be a single segment", name, i)
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
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: harness %q has no definition for role %q", name, i, c.Harness, r)
			}
		}
		if unknownRole != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s: unknown role %q (skipped)", base, c.Ref().String(), unknownRole))
			continue
		}
		if c.Tree != "" && c.Tree != "binding" && c.Tree != "none" {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: tree must be \"binding\" or \"none\"", name, i)
		}
		for j, pat := range c.LimitPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: limit_patterns[%d]: %w", name, i, j, err)
			}
		}
		for j, pat := range c.DialogPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: dialog_patterns[%d]: %w", name, i, j, err)
			}
		}
		if c.Tier != "" {
			if _, err := harness.ParseTier(c.Tier); err != nil {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: tier: %v", name, i, err)
			}
		}
		for j, pat := range c.DenialPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return nil, nil, fmt.Errorf("candidates %s: candidate %d: denial_patterns[%d]: %w", name, i, j, err)
			}
		}
		key := c.Ref().String()
		if first, exists := seen[key]; exists {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: duplicate candidate %s at index %d and %d", name, i, key, first, i)
		}
		seen[key] = i
		if first, exists := seenNames[c.Name]; exists {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: duplicate name %q at index %d and %d", name, i, c.Name, first, i)
		}
		seenNames[c.Name] = i
		if providers[c.Name] {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: name %q is also a provider name", name, i, c.Name)
		}
		set.byRef[key] = c
		set.byName[c.Name] = key
	}

	return set, warnings, nil
}
