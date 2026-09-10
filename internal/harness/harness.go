package harness

import "sort"

// Role is one agent definition relay ships for a harness.
type Role struct {
	Name string // as the user types it: "plan-executor", "researcher"
	Path string // home-relative install path
	Doc  string // basename stem of the embedded definition, "<role>.<kind>"
}

// Harness describes how one herdr agent kind appears on the local machine.
// Harness is not comparable: Roles is a slice, so use reflect.DeepEqual rather
// than == on two Harness values.
type Harness struct {
	Kind        string // herdr agent kind, as passed to `herdr agent start --kind`
	Binary      string // executable name looked up on PATH
	Integration string // herdr integration target; "" when the harness has none
	// Roles are the definitions relay ships for this kind, ordered with
	// plan-executor first so doctor reports the role relay's loop depends on
	// before the rest. Empty means the harness selects its role with a
	// preamble on the first prompt rather than with a file, which is agy.
	Roles []Role
}

var knownHarnesses = map[string]Harness{
	"agy": {
		Kind:        "agy",
		Binary:      "agy",
		Integration: "antigravity-cli",
		Roles:       nil, // no --agent flag; the alias preamble selects the role
	},
	"claude": {
		Kind:        "claude",
		Binary:      "claude",
		Integration: "claude",
		Roles: []Role{
			{Name: "plan-executor", Path: ".claude/agents/plan-executor.md", Doc: "plan-executor.claude"},
			{Name: "researcher", Path: ".claude/agents/researcher.md", Doc: "researcher.claude"},
			{Name: "reviewer", Path: ".claude/agents/reviewer.md", Doc: "reviewer.claude"},
		},
	},
	"opencode": {
		Kind:        "opencode",
		Binary:      "opencode",
		Integration: "opencode",
		Roles: []Role{
			{Name: "plan-executor", Path: ".config/opencode/agents/plan-executor.md", Doc: "plan-executor.opencode"},
			{Name: "researcher", Path: ".config/opencode/agents/researcher.md", Doc: "researcher.opencode"},
			{Name: "reviewer", Path: ".config/opencode/agents/reviewer.md", Doc: "reviewer.opencode"},
		},
	},
}

// Role returns the named role for this harness.
func (h Harness) Role(name string) (Role, bool) {
	for _, r := range h.Roles {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// RoleNames returns this harness's role names in table order, for error text.
func (h Harness) RoleNames() []string {
	names := make([]string, 0, len(h.Roles))
	for _, r := range h.Roles {
		names = append(names, r.Name)
	}
	return names
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
