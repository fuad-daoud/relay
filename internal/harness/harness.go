package harness

import "sort"

// Harness describes how one herdr agent kind appears on the local machine.
type Harness struct {
	Kind        string // herdr agent kind, as passed to `herdr agent start --kind`
	Binary      string // executable name looked up on PATH
	Integration string // herdr integration target; "" when the harness has none
	RolePath    string // home-relative path to the role file; "" when the role
	// is selected by preamble rather than by a file
	RoleDoc string // key into the embedded definitions; "" when RolePath is ""
}

var knownHarnesses = map[string]Harness{
	"agy": {
		Kind:        "agy",
		Binary:      "agy",
		Integration: "antigravity-cli",
		RolePath:    "",
		RoleDoc:     "",
	},
	"claude": {
		Kind:        "claude",
		Binary:      "claude",
		Integration: "claude",
		RolePath:    ".claude/agents/plan-executor.md",
		RoleDoc:     "claude",
	},
	"opencode": {
		Kind:        "opencode",
		Binary:      "opencode",
		Integration: "opencode",
		RolePath:    ".config/opencode/agents/plan-executor.md",
		RoleDoc:     "opencode",
	},
}

// Lookup returns the entry for a kind. ok is false for a kind relay was not
// taught, which is not an error: callers degrade to what they can check.
func Lookup(kind string) (Harness, bool) {
	h, ok := knownHarnesses[kind]
	return h, ok
}

// All returns every known entry, sorted by Kind for stable output.
func All() []Harness {
	all := make([]Harness, 0, len(knownHarnesses))
	for _, h := range knownHarnesses {
		all = append(all, h)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Kind < all[j].Kind
	})
	return all
}
