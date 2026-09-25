package harness

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

// RoleSpec describes one role relevo can run.
type RoleSpec struct {
	Name       string
	Shape      RoleShape
	Definition string
	// Definitions is every definition a harness must have installed to
	// run the role: Definition first, then what it dispatches to. The
	// builder's plan-executor sends its research sub-agents to
	// researcher, so a builder-only harness needs both; a consult role
	// needs only its own. Doctor checks these and nothing else (#166).
	Definitions []string
}

// roleTable defines relevo's built-in roles: a role is relevo's name for a job
// (builder, reviewer), with a shape relevo's loop depends on and the harness
// agent definition that implements it. The candidate that runs it is a separate
// choice (#80).
var roleTable = []RoleSpec{
	{
		Name:        "builder",
		Shape:       ShapeBuilder,
		Definition:  "plan-executor",
		Definitions: []string{"plan-executor", "researcher"},
	},
	{
		Name:        "reviewer",
		Shape:       ShapeConsult,
		Definition:  "reviewer",
		Definitions: []string{"reviewer"},
	},
	{
		Name:        "researcher",
		Shape:       ShapeConsult,
		Definition:  "researcher",
		Definitions: []string{"researcher"},
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

// Role is one agent definition relevo ships for a harness.
type Role struct {
	Name string // as the user types it: "plan-executor", "researcher"
	Path string // home-relative install path
	Doc  string // basename stem of the embedded definition, "<role>.<kind>"
	// ExpectModel is the only model pin doctor accepts in an installed copy
	// without warning; "" means any pin is fine. It is "inherit" on every
	// agy row because agy's model key is a tier that would override the
	// --model relevo passes on the launch line (spec §7.2).
	ExpectModel string
}

// Harness describes how one harness kind is started and checked on the local
// machine.
// Harness is not comparable: Roles is a slice, so use reflect.DeepEqual rather
// than == on two Harness values.
type Harness struct {
	Kind   string // harness kind, as passed to the harness's own agent selector
	Binary string // executable name looked up on PATH
	// Roles are the definitions relevo ships for this kind, ordered with
	// plan-executor first so doctor reports the role relevo's loop depends on
	// before the rest. Never empty for a known kind: every kind relevo runs
	// selects its role with --agent. Not every row backs a roleTable entry:
	// architect is the planner's definition, shipped so the session that
	// drives relevo can be started with --agent architect, never launched
	// by relevo itself.
	Roles []Role
	// MinVersion is the semver floor doctor holds the binary to; "" means
	// unchecked. agy's floor is the release that added Markdown agent
	// definitions, without which --agent has nothing to select.
	MinVersion string
	// LimitPatterns are default regexes for the text this harness prints when
	// its provider closes the session on quota. Every default must compile;
	// TestLimitPatternsSetOnEveryKind enforces it. Case-insensitivity is
	// written into the pattern with (?i).
	LimitPatterns []string
	// DenialPatterns are default regexes for the text this harness prints when
	// a tool call was refused by its permission mode in print mode (#141).
	// Every default must compile; TestDenialPatternsSetOnEveryKind enforces it.
	DenialPatterns []string
	// DocExt is the extension of this kind's shipped definition files under
	// agents/; "" means "md". codex roles are TOML profiles (spec §5).
	DocExt string
	// Files are the non-definition files relevo ships for this kind and
	// installs under the home: the OpenCode plugin package today (#393
	// §5.4). A file absent on disk is written only when
	// InstallOptions.Files is set -- the plugin is opt-in -- and nil for
	// every kind that ships none.
	Files []ShippedFile
}

var knownHarnesses = map[string]Harness{
	"agy": {
		Kind:       "agy",
		Binary:     "agy",
		MinVersion: "1.1.6",
		// Observed 2026-09-12 in history.json ("Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.");
		// the others from Google API error strings, unverified against a pane; replace with the observed line when one is seen.
		LimitPatterns: []string{
			`(?i)individual quota reached`,
			`(?i)RESOURCE_EXHAUSTED`,
			`(?i)quota exceeded`,
		},
		// Denial patterns for agy; unverified against a real denied round; replace with the observed line when one is seen.
		DenialPatterns: []string{
			`(?i)permission (request )?(denied|rejected)`,
			`(?i)tool (call|use) (was )?rejected`,
			`(?i)not permitted in (plan|accept-edits) mode`,
		},
		Roles: []Role{
			{Name: "plan-executor", Path: ".gemini/config/agents/plan-executor.md", Doc: "plan-executor.agy", ExpectModel: "inherit"},
			{Name: "researcher", Path: ".gemini/config/agents/researcher.md", Doc: "researcher.agy", ExpectModel: "inherit"},
			{Name: "reviewer", Path: ".gemini/config/agents/reviewer.md", Doc: "reviewer.agy", ExpectModel: "inherit"},
			{Name: "architect", Path: ".gemini/config/agents/architect.md", Doc: "architect.agy", ExpectModel: "inherit"},
		},
	},
	"claude": {
		Kind:   "claude",
		Binary: "claude",
		// Claude Code's own limit banner and API error text; unverified against a pane; replace with the observed line when one is seen.
		LimitPatterns: []string{
			`(?i)you've hit your .*limit`,
			`(?i)usage limit reached`,
			`(?i)rate limit reached`,
			`(?i)limit .*resets`,
		},
		// Denial patterns for claude; unverified against a real denied round; replace with the observed line when one is seen.
		DenialPatterns: []string{
			`(?i)requested permissions to use .* but you haven't granted`,
			`(?i)permission (to use .* was )?denied`,
			`(?i)tool use was rejected`,
		},
		Roles: []Role{
			{Name: "plan-executor", Path: ".claude/agents/plan-executor.md", Doc: "plan-executor.claude"},
			{Name: "researcher", Path: ".claude/agents/researcher.md", Doc: "researcher.claude"},
			{Name: "reviewer", Path: ".claude/agents/reviewer.md", Doc: "reviewer.claude"},
			{Name: "architect", Path: ".claude/agents/architect.md", Doc: "architect.claude"},
		},
	},
	"opencode": {
		Kind:   "opencode",
		Binary: "opencode",
		// OpenRouter 429/402 bodies and the Google strings opencode relays; unverified against a pane; replace with the observed line when one is seen.
		LimitPatterns: []string{
			`(?i)rate.?limit(ed)? (reached|exceeded)`,
			`(?i)quota (exceeded|reached)`,
			`(?i)insufficient (credits|quota)`,
			`(?i)RESOURCE_EXHAUSTED`,
		},
		// Denial patterns for opencode; unverified against a real denied round; replace with the observed line when one is seen.
		DenialPatterns: []string{
			`(?i)permission.*(denied|rejected)`,
			`(?i)rejected: external_directory`,
		},
		Roles: []Role{
			{Name: "plan-executor", Path: ".config/opencode/agents/plan-executor.md", Doc: "plan-executor.opencode"},
			{Name: "researcher", Path: ".config/opencode/agents/researcher.md", Doc: "researcher.opencode"},
			{Name: "reviewer", Path: ".config/opencode/agents/reviewer.md", Doc: "reviewer.opencode"},
			{Name: "architect", Path: ".config/opencode/agents/architect.md", Doc: "architect.opencode"},
		},
		Files: []ShippedFile{
			{Name: "opencode-plugin/package.json", Path: ".config/opencode/plugins/relevo/package.json", Embed: "opencodeplugin/package.json"},
			{Name: "opencode-plugin/server.ts", Path: ".config/opencode/plugins/relevo/server.ts", Embed: "opencodeplugin/server.ts"},
			{Name: "opencode-plugin/tui.tsx", Path: ".config/opencode/plugins/relevo/tui.tsx", Embed: "opencodeplugin/tui.tsx"},
		},
	},
	"codex": {
		Kind:       "codex",
		Binary:     "codex",
		MinVersion: "0.155.0",
		// Codex CLI limit text and OpenAI 429 bodies; unverified against a pane; replace with the observed line when one is seen.
		LimitPatterns: []string{
			`(?i)usage limit`,
			`(?i)rate limit`,
			`(?i)quota`,
			`(?i)"status": 429`,
			`(?i)too many requests`,
		},
		// Denial patterns for codex: first two observed 2026-09-20 (#230), the rest unverified.
		DenialPatterns: []string{
			`(?i)patch rejected: writing outside of the project`,
			`(?i)rejected by user approval settings`,
			`(?i)sandbox.*(denied|blocked|not permitted)`,
			`(?i)permission denied`,
		},
		DocExt: "toml",
		Roles: []Role{
			{Name: "plan-executor", Path: ".codex/plan-executor.config.toml", Doc: "plan-executor.codex"},
			{Name: "researcher", Path: ".codex/researcher.config.toml", Doc: "researcher.codex", ExpectModel: "gpt-5.6-luna"},
			{Name: "reviewer", Path: ".codex/reviewer.config.toml", Doc: "reviewer.codex"},
			{Name: "architect", Path: ".codex/architect.config.toml", Doc: "architect.codex"},
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
	for _, d := range spec.Definitions {
		if _, found := h.Role(d); !found {
			return false
		}
	}
	return true
}

// Placeholders that stand in Launch.Print for the values only the caller
// knows at send time. PrintArgs replaces them; they are exported so a test
// or a caller can recognise them, never so a caller can build argv by hand.
const (
	PromptPlaceholder = "<prompt>"
	BudgetPlaceholder = "<budget>"
	DirPlaceholder    = "<dir>"
	// StatePlaceholder stands, as its own element, for the codex writable-roots
	// override: PrintArgs replaces the element with
	// `sandbox_workspace_write.writable_roots=["<state dir>"]` (#230). It is
	// only ever the element after a "-c".
	StatePlaceholder = "<state>"
)

// Launch describes how to start an agent process for a specific role and
// model.
//
// Print is the non-interactive form a process runs (#99): one prompt in, the
// process exits when it is done. Print holds PromptPlaceholder and, for kinds
// with a timeout flag, BudgetPlaceholder, as well as DirPlaceholder and
// StatePlaceholder as their own elements; PrintArgs fills them. PromptAt is the
// index of PromptPlaceholder in Print, -1 when the kind is unknown and Print is
// empty.
type Launch struct {
	Kind     string
	Print    []string
	PromptAt int
}

// Launch renders argv for role at tier. tier's PermissionArgs are appended
// after the base form and before extra. Errors:
// ErrTierUnsupported (refusal cell); ErrExtraArgsPermission when tier !=
// TierHarness and extra carries a permission flag for this kind
// (`candidate extra_args carries %s; remove it or use --tier harness`).
// At TierHarness the result is byte-identical to the pre-#141 Launch.
func (h Harness) Launch(provider, model string, extra []string, role RoleSpec, tier Tier) (Launch, error) {
	var print []string
	promptAt := -1

	switch h.Kind {
	case "claude":
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition,
			"--output-format", "stream-json", "--verbose"}
		promptAt = 1
	case "opencode":
		// --standalone (opencode 2.x, #256): without it, `run` is a thin
		// client of the one `opencode serve --service` per user, and a
		// process-group kill of the client (proc.Runner.Kill) leaves the
		// agent session running inside the service, still editing the
		// worktree relevo has switched away from. --standalone starts a
		// private server instead, so a headless round's kill is a real kill
		// again.
		print = []string{"run", PromptPlaceholder, "-m", provider + "/" + model, "--agent", role.Definition, "--format", "json", "--standalone"}
		promptAt = 1
	// 2026-09-18 probe: agy's stream `init` event reports `cwd` = the
	// process directory, yet its first `run_command` ran outside any
	// repository and the model `cd`-ed into the planner's main checkout
	// (#192). `--add-dir <cwd>` pins the workspace; `--project`/
	// `--new-project` were not used because they name agy-side project
	// records, not a directory.
	case "agy":
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition,
			"--output-format", "stream-json", "--print-timeout", BudgetPlaceholder,
			"--add-dir", DirPlaceholder}
		promptAt = 1
	case "codex":
		id, effort, err := SplitEffort(model)
		if err != nil {
			return Launch{}, fmt.Errorf("%w: codex model %q", ErrBadModel, model)
		}
		cfg := []string{"-p", role.Definition, "-m", id, "-c", "model_provider=" + provider}
		if effort != "" {
			cfg = append(cfg, "-c", "model_reasoning_effort="+effort)
		}
		print = append(append([]string{"exec", PromptPlaceholder}, cfg...), "--json", "-C", DirPlaceholder)
		promptAt = 1
	}

	perm, err := h.PermissionArgs(tier)
	if err != nil {
		return Launch{}, err
	}
	if tier != TierHarness {
		if f := h.ExtraArgsPermissionFlag(extra); f != "" {
			return Launch{}, fmt.Errorf("%w: candidate extra_args carries %s; remove it or use --tier harness", ErrExtraArgsPermission, f)
		}
	}

	if print != nil {
		print = append(append(append([]string(nil), print...), perm...), extra...)
	}
	return Launch{
		Kind:     h.Kind,
		Print:    print,
		PromptAt: promptAt,
	}, nil
}

// writableRootsArg renders the -c value for state. state is quoted as a
// TOML basic string with strconv.Quote (identical escapes for every path
// this program produces). Precondition: state is absolute and non-empty.
func writableRootsArg(state string) string {
	return "sandbox_workspace_write.writable_roots=[" + strconv.Quote(state) + "]"
}

// PrintArgs is Print with the prompt, the round budget, the round's
// working tree and the binding's state directory filled in: a fresh slice,
// so neither Print nor the caller's extra is touched. The budget is rendered
// as a Go duration ("1h30m0s"), which is what agy's --print-timeout parses.
// A kind whose Print has no BudgetPlaceholder ignores budget, one with no
// DirPlaceholder ignores dir, and one with no StatePlaceholder ignores state,
// the same rule.
//
// Precondition: dir is absolute or empty; state is absolute or empty; when
// empty and the placeholder is present, the element is filled with
// writableRootsArg("") -- which codex rejects loudly -- so callers must pass
// it; a test pins that every production caller does. Postcondition: no
// placeholder string remains in the result.
func (l Launch) PrintArgs(prompt string, budget time.Duration, dir, state string) []string {
	out := make([]string, 0, len(l.Print))
	for _, a := range l.Print {
		switch a {
		case PromptPlaceholder:
			out = append(out, prompt)
		case BudgetPlaceholder:
			out = append(out, budget.String())
		case DirPlaceholder:
			out = append(out, dir)
		case StatePlaceholder:
			out = append(out, writableRootsArg(state))
		default:
			out = append(out, a)
		}
	}
	return out
}

// ErrBadModel reports a candidate model relevo cannot render for its kind.
var ErrBadModel = errors.New("bad model")

// SplitEffort splits a codex candidate model "<id>[:<effort>]" on its last
// ':' (spec §3.1). No colon: (model, ""). It does not validate the effort
// vocabulary, which is model-dependent. Returns ErrBadModel when the id is
// empty or a colon is present with an empty effort.
func SplitEffort(model string) (id, effort string, err error) {
	if model == "" {
		return "", "", ErrBadModel
	}
	i := strings.LastIndex(model, ":")
	if i < 0 {
		return model, "", nil
	}
	id, effort = model[:i], model[i+1:]
	if id == "" || effort == "" {
		return "", "", ErrBadModel
	}
	return id, effort, nil
}

// Lookup returns the entry for a kind. ok is false for a kind relevo was not
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
