package harness

import (
	"sort"
	"time"
)

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
}

// SubAgentVisibility records what `herdr agent list` shows while a builder
// of this kind is running a sub-agent. It is a property of the harness and
// herdr's integration for it, observed live; the observation behind each
// value is recorded on the knownHarnesses entry that carries it. relay
// reports what herdr reports, and this says how much that covers.
type SubAgentVisibility string

const (
	// SubAgentsSeparate: a sub-agent is a separate herdr agent in its own
	// pane. ForeignAgents reports it as a foreign row.
	SubAgentsSeparate SubAgentVisibility = "separate"
	// SubAgentsForeground: a sub-agent takes over the builder pane's session
	// slot. herdr lists no extra agent; relay's name match keeps the builder
	// known (#66) but has nothing to report for the sub-agent.
	SubAgentsForeground SubAgentVisibility = "foreground"
	// SubAgentsHidden: a sub-agent runs inside the builder's process and
	// herdr lists only the pane. Nothing observable.
	SubAgentsHidden SubAgentVisibility = "hidden"
)

// roleTable defines relay's built-in roles: a role is relay's name for a job
// (builder, reviewer), with a shape relay's loop depends on and the harness
// agent definition that implements it. The candidate that runs it is a separate
// choice (#80).
var roleTable = []RoleSpec{
	{
		Name:       "builder",
		Shape:      ShapeBuilder,
		Definition: "plan-executor",
	},
	{
		Name:       "reviewer",
		Shape:      ShapeConsult,
		Definition: "reviewer",
	},
	{
		Name:       "researcher",
		Shape:      ShapeConsult,
		Definition: "researcher",
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
	// ExpectModel is the only model pin doctor accepts in an installed copy
	// without warning; "" means any pin is fine. It is "inherit" on every
	// agy row because agy's model key is a tier that would override the
	// --model relay passes on the launch line (spec §7.2).
	ExpectModel string
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
	// before the rest. Never empty for a known kind: every kind relay runs
	// selects its role with --agent.
	Roles []Role
	// MinVersion is the semver floor doctor holds the binary to; "" means
	// unchecked. agy's floor is the release that added Markdown agent
	// definitions, without which --agent has nothing to select.
	MinVersion string
	// SubAgents is what herdr shows for this kind's sub-agents. Never "" on
	// a known kind; TestSubAgentsSetOnEveryKind enforces it. The status layer
	// prints a coverage row for anything but SubAgentsSeparate.
	SubAgents SubAgentVisibility
}

var knownHarnesses = map[string]Harness{
	"agy": {
		Kind:        "agy",
		Binary:      "agy",
		Integration: "antigravity-cli",
		MinVersion:  "1.1.6",
		// Observed 2026-09-11, herdr 0.9.0, antigravity-cli integration (#66):
		// while a builder waits on a sub-agent, `herdr agent list` returns the
		// sub-agent's session id on the builder's pane and no extra agent.
		// The integration reports whichever agy session is in the foreground.
		SubAgents: SubAgentsForeground,
		Roles: []Role{
			{Name: "plan-executor", Path: ".gemini/config/agents/plan-executor.md", Doc: "plan-executor.agy", ExpectModel: "inherit"},
			{Name: "researcher", Path: ".gemini/config/agents/researcher.md", Doc: "researcher.agy", ExpectModel: "inherit"},
			{Name: "reviewer", Path: ".gemini/config/agents/reviewer.md", Doc: "reviewer.agy", ExpectModel: "inherit"},
		},
	},
	"claude": {
		Kind:        "claude",
		Binary:      "claude",
		Integration: "claude",
		// Observed 2026-09-10, herdr 0.9.0, claude integration: a researcher
		// dispatched by a plan-executor surfaced as its own herdr pane and was
		// reported as a foreign row (foreign-agent spec §7.2).
		SubAgents: SubAgentsSeparate,
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
		// Observed 2026-09-10, herdr 0.9.0, opencode integration v11: a
		// researcher dispatched by a plan-executor rendered as a card inside
		// the builder's TUI; `herdr agent list` showed no extra agent. Stable
		// by design: the integration (herdr-agent-state.js) tracks child
		// sessions by parentID and folds them into the pane's root session so
		// they "cannot replace the pane's root session"; only a child's
		// permission/question prompt bubbles up, as the root's blocked state.
		SubAgents: SubAgentsHidden,
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
	_, found := h.Role(spec.Definition)
	return found
}

// Placeholders that stand in Launch.Print for the values only the caller
// knows at send time. PrintArgs replaces them; they are exported so a test
// or a caller can recognise them, never so a caller can build argv by hand.
const (
	PromptPlaceholder = "<prompt>"
	BudgetPlaceholder = "<budget>"
)

// Launch describes how to start an agent process for a specific role and
// model, in both of its forms.
//
// Args is the interactive form herdr starts in a pane. Print is the
// non-interactive form a headless builder runs (#99): one prompt in, the
// process exits when it is done. Print holds PromptPlaceholder and, for kinds
// with a timeout flag, BudgetPlaceholder as their own elements; PrintArgs
// fills them. PromptAt is the index of PromptPlaceholder in Print, -1 when
// the kind is unknown and Print is empty.
type Launch struct {
	Kind     string
	Args     []string
	Print    []string
	PromptAt int
}

// Launch renders the command-line arguments needed to run the given role on
// this harness. Relay renders the argv because model and role are fields
// (candidates spec §1 point 2): with a verbatim args list in config, the
// model would be a label relay could not check against what it launched.
// Every kind selects its role with --agent; there is no other mechanism (#85).
//
// The print form per kind (headless spec §3.5), before extra:
//
//	agy       -p <prompt> --model M --agent <def> --output-format text --print-timeout <budget>
//	claude    -p <prompt> --model M --agent <def> --output-format text
//	opencode  run <prompt> -m P/M --agent <def>
//
// agy gets the budget because its default print timeout (5m) would kill any
// real round; claude and opencode have no such flag.
func (h Harness) Launch(provider, model string, extra []string, role RoleSpec) Launch {
	var base, print []string
	promptAt := -1

	switch h.Kind {
	case "claude":
		base = []string{"--model", model, "--agent", role.Definition}
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition, "--output-format", "text"}
		promptAt = 1
	case "opencode":
		base = []string{"--agent", role.Definition, "-m", provider + "/" + model}
		print = []string{"run", PromptPlaceholder, "-m", provider + "/" + model, "--agent", role.Definition}
		promptAt = 1
	case "agy":
		base = []string{"--model", model, "--agent", role.Definition}
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition,
			"--output-format", "text", "--print-timeout", BudgetPlaceholder}
		promptAt = 1
	}

	args := append(append([]string(nil), base...), extra...)
	if print != nil {
		print = append(print, extra...)
	}
	return Launch{
		Kind:     h.Kind,
		Args:     args,
		Print:    print,
		PromptAt: promptAt,
	}
}

// PrintArgs is Print with the prompt and the round budget filled in: a fresh
// slice, so neither Print nor the caller's extra is touched. The budget is
// rendered as a Go duration ("1h30m0s"), which is what agy's --print-timeout
// parses. A kind whose Print has no BudgetPlaceholder ignores budget.
func (l Launch) PrintArgs(prompt string, budget time.Duration) []string {
	out := make([]string, 0, len(l.Print))
	for _, a := range l.Print {
		switch a {
		case PromptPlaceholder:
			out = append(out, prompt)
		case BudgetPlaceholder:
			out = append(out, budget.String())
		default:
			out = append(out, a)
		}
	}
	return out
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
