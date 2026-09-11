package relay

import (
	"sort"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/herdr"
)

// ConsultRolesTooLong returns, sorted, the consult roles in t for which
// bindingName + "-" + role + "-" + <8 hex> would exceed herdr's agent-name
// limit and so be refused at `relay ask`. It is pure, and empty when every
// configured consult fits or the table has no consults.
//
// It backs the advisory note printed after a successful bind, add or fork:
// a binding that can build but cannot take a configured reviewer is still
// useful, so relay warns when the name is chosen rather than refusing and
// letting the alias table dictate binding names.
func ConsultRolesTooLong(t *alias.Table, bindingName string) []string {
	var tooLong []string
	for _, name := range t.Names() {
		spec, err := t.Lookup(name)
		if err != nil || !spec.IsConsult() {
			continue
		}
		// A consult agent name is <binding>-<role>-<8 hex>: one separator on
		// each side of the role, plus the 8-hex id.
		if len(bindingName)+1+len(spec.Name)+1+8 > herdr.MaxAgentNameLen {
			tooLong = append(tooLong, spec.Name)
		}
	}
	sort.Strings(tooLong)
	return tooLong
}
