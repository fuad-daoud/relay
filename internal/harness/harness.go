package harness

import "sort"

// RoleShape distinguishes persistent writers from ephemeral consults.
type RoleShape string

const (
	// ShapeBuilder is a persistent writer in the binding tree.
	ShapeBuilder RoleShape = "builder"
	// ShapeConsult is a one-shot read-only agent beside the builder.
	ShapeConsult RoleShape = "consult"
)

// RoleSpec describes one role relay can run.
type RoleSpec struct {
	Name       string
	Shape      RoleShape
	Definition string
	Preamble   string
}

// roleTable defines relay's built-in roles: a role is relay's name for a job
// (builder, reviewer), with a shape relay's loop depends on and the harness
// agent definition that implements it. The candidate that runs it is a separate
// choice (#80).
var roleTable = []RoleSpec{
	{
		Name:       "builder",
		Shape:      ShapeBuilder,
		Definition: "plan-executor",
		Preamble:   "Activate your 'plan-executor' skill and act as the Plan Execution Specialist. Execute exactly as specified in the skill.",
	},
	{
		Name:       "reviewer",
		Shape:      ShapeConsult,
		Definition: "reviewer",
		Preamble:   "Activate your 'reviewer' skill and act exactly as it specifies.",
	},
	{
		Name:       "researcher",
		Shape:      ShapeConsult,
		Definition: "researcher",
		Preamble:   "Activate your 'researcher' skill and act exactly as it specifies.",
	},
}

// RoleByName returns the specification for the named role. ok is false for an
// unknown name, which is not an error: the caller decides.
func RoleByName(name string) (RoleSpec, bool) {
	for _, r := range roleTable {
		if r.Name == name {
			return r, true
		}
	}
	return RoleSpec{}, false
}

// RoleNames returns every known role name in table order.
func RoleNames() []string {
	names := make([]string, 0, len(roleTable))
	for _, r := range roleTable {
		names = append(names, r.Name)
	}
	return names
}

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
	// SelectsRoleByPreamble is true for a kind with no --agent flag. Its
	// Roles slice is nil and every role in the table is servable.
	SelectsRoleByPreamble bool
}

var knownHarnesses = map[string]Harness{
	"agy": {
		Kind:                  "agy",
		Binary:                "agy",
		Integration:           "antigravity-cli",
		Roles:                 nil, // no --agent flag; the alias preamble selects the role
		SelectsRoleByPreamble: true,
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

// CanServe reports whether this harness can serve the named role. Returns false
// when the role is unknown or when the harness cannot run the agent definition
// implementing the role.
func (h Harness) CanServe(role string) bool {
	spec, ok := RoleByName(role)
	if !ok {
		return false
	}
	if h.SelectsRoleByPreamble {
		return true
	}
	_, found := h.Role(spec.Definition)
	return found
}

// Launch describes how to start an agent process for a specific role and model.
type Launch struct {
	Kind     string
	Args     []string
	Preamble string
}

// Launch renders the command-line arguments and prompt preamble needed to run
// the given role on this harness. Relay renders the argv because model and the
// role are now fields (spec §1 point 2); with a verbatim args list in config,
// the model would be a label relay could not check against what it launched.
func (h Harness) Launch(provider, model string, extra []string, role RoleSpec) Launch {
	var base []string
	var preamble string

	switch h.Kind {
	case "claude":
		base = []string{"--model", model, "--agent", role.Definition}
	case "opencode":
		base = []string{"--agent", role.Definition, "-m", provider + "/" + model}
	case "agy":
		base = []string{"--model", model}
		preamble = role.Preamble
	}

	args := append(append([]string(nil), base...), extra...)
	return Launch{
		Kind:     h.Kind,
		Args:     args,
		Preamble: preamble,
	}
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
