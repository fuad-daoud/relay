// Package alias maps the human's builder names onto the herdr agent kind and
// native arguments needed to start one.
package alias

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// ErrUnknownAlias reports a builder name with no entry in the table.
var ErrUnknownAlias = errors.New("unknown builder alias")

// Spec is how to start one builder. Preamble is prepended to the first prompt
// of a session for harnesses that cannot select a role at launch.
type Spec struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Args     []string `json:"args"`
	Preamble string   `json:"preamble,omitempty"`
}

// Table resolves alias names to specs.
type Table struct {
	specs map[string]Spec
}

// DefaultTable returns the three built-in aliases. Treat them as worked
// examples rather than a supported set: each one names a harness, a model and
// a role that have to already exist on the machine relay runs on. In
// particular `plan-executor` is an agent (or skill) you define in that
// harness, and each model assumes its provider is configured.
//
// Override or extend them in ~/.config/relay/aliases.json; see LoadTable.
func DefaultTable() *Table {
	defaults := []Spec{
		{
			Name: "builder",
			Kind: "opencode",
			Args: []string{"--agent", "plan-executor", "-m", "openrouter/z-ai/glm-5.3-flash"},
		},
		{
			Name: "cbuilder",
			Kind: "claude",
			Args: []string{"--agent", "plan-executor", "--model", "sonnet"},
		},
		{
			Name: "abuilder",
			Kind: "agy",
			// --dangerously-skip-permissions is what lets a builder run a
			// whole round unattended, and it is a real grant of trust: the
			// agent acts without asking. It is set here because relay's own
			// loop assumes a builder that does not stop for approvals, but it
			// belongs to a working tree you are willing to let an agent edit
			// freely. Drop it in your own aliases.json if that is not yours.
			Args: []string{"--model", "gemini-3.8-flash-high", "--dangerously-skip-permissions"},
			// agy has no --agent flag, so the role is selected in the first prompt.
			Preamble: "Activate your 'plan-executor' skill and act as the Plan Execution Specialist. Execute exactly as specified in the skill.",
		},
	}

	specs := make(map[string]Spec, len(defaults))
	for _, s := range defaults {
		specs[s.Name] = s
	}

	return &Table{specs: specs}
}

// LoadTable layers a JSON override file over the defaults. A missing file
// yields the defaults, since the override is optional by design.
func LoadTable(path string) (*Table, error) {
	tbl := DefaultTable()
	if path == "" {
		return tbl, nil
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return tbl, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read alias overrides %s: %w", path, err)
	}

	var overrides []Spec
	if err := json.Unmarshal(raw, &overrides); err != nil {
		return nil, fmt.Errorf("decode alias overrides %s: %w", path, err)
	}

	for _, s := range overrides {
		if s.Name == "" || s.Kind == "" {
			return nil, fmt.Errorf("alias override in %s needs both name and kind", path)
		}
		tbl.specs[s.Name] = s
	}

	return tbl, nil
}

// Lookup resolves one alias name.
func (t *Table) Lookup(name string) (Spec, error) {
	s, ok := t.specs[name]
	if !ok {
		return Spec{}, fmt.Errorf("alias %q not found (known: %v): %w", name, t.Names(), ErrUnknownAlias)
	}
	return s, nil
}

// Names returns every known alias, sorted for stable output.
func (t *Table) Names() []string {
	names := make([]string, 0, len(t.specs))
	for n := range t.specs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
