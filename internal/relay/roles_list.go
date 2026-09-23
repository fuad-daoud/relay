package relay

import (
	"sort"
	"strings"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/roles"
)

// FormatRoles renders the registry for `relay roles` (#374 §3.2): one block per
// role in reg.Names() order, with a blank line between blocks --
//
//	<name>  <writer|reader>[  gate]  tier <tier or ->  (<reg.Source()>)
//	  candidates  <tok>, <tok>      or   candidates  (none)
//	  <kind>  <agent>[ + <req> ...][  (custom)]
//
// It is pure and never fails: `relay roles` lists what is there.
func FormatRoles(reg *roles.Registry) string {
	blocks := make([]string, 0, len(reg.Names()))
	for _, name := range reg.Names() {
		role, ok := reg.Role(name)
		if !ok {
			continue
		}
		blocks = append(blocks, formatRole(reg, role))
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// formatRole renders one role's block, without its trailing newline.
func formatRole(reg *roles.Registry, role roles.Role) string {
	var b strings.Builder

	b.WriteString(role.Name + "  ")
	if role.Shape == harness.ShapeBuilder {
		b.WriteString("writer")
	} else {
		b.WriteString("reader")
	}
	if role.Gate {
		b.WriteString("  gate")
	}
	tier := "-"
	if t, ok := reg.RoleTier(role.Name); ok {
		tier = string(t)
	}
	b.WriteString("  tier " + tier + "  (" + reg.Source() + ")")

	// #374 §2.3: file mode lists the row's tokens as written. Legacy mode
	// lists the resolved Ranked list instead, so a role served only by
	// candidates no order names still shows them, marked (unlisted).
	b.WriteString("\n  candidates  ")
	if reg.Source() == roles.SourceLegacy {
		b.WriteString(legacyCandidates(role.Ranked))
	} else {
		b.WriteString(fileCandidates(role.Candidates))
	}

	for _, kind := range sortedDefinitionKinds(role.Definitions) {
		d := role.Definitions[kind]
		b.WriteString("\n  " + kind + "  " + d.Agent)
		if len(d.Requires) > 0 {
			b.WriteString(" + " + strings.Join(d.Requires, " + "))
		}
		if d.Custom {
			b.WriteString("  (custom)")
		}
	}

	return b.String()
}

// legacyCandidates renders a role's ranked list for the legacy candidates
// line: bare tokens in order, a token whose Position is 0 -- listed by no
// order entry -- suffixed " (unlisted)", and "(none)" when the list is empty.
func legacyCandidates(ranked []roles.Ranked) string {
	if len(ranked) == 0 {
		return "(none)"
	}
	tokens := make([]string, 0, len(ranked))
	for _, r := range ranked {
		if r.Position == 0 {
			tokens = append(tokens, r.Token+" (unlisted)")
		} else {
			tokens = append(tokens, r.Token)
		}
	}
	return strings.Join(tokens, ", ")
}

// fileCandidates renders a role's candidates as written, or "(none)".
func fileCandidates(candidates []string) string {
	if len(candidates) == 0 {
		return "(none)"
	}
	return strings.Join(candidates, ", ")
}

// sortedDefinitionKinds returns a role's resolved definition kinds, sorted, so
// the block's kind lines never move between runs.
func sortedDefinitionKinds(defs map[string]roles.Definition) []string {
	kinds := make([]string, 0, len(defs))
	for kind := range defs {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
