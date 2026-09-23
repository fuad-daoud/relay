package relay

import (
	"sort"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/roles"
	"github.com/fuad-daoud/relay/internal/store"
)

// ConsultRolesTooLongFor returns, sorted, the consult roles the registry knows
// for which bindingName + "-" + role + "-" + <8 hex> would exceed the
// agent-name limit and so be refused at `relay ask` (#374 §3.3). It is pure,
// and empty when every consult role fits or bindingName is empty.
func ConsultRolesTooLongFor(reg *roles.Registry, bindingName string) []string {
	var tooLong []string
	for _, name := range reg.Names() {
		role, ok := reg.Role(name)
		if !ok || role.Shape != harness.ShapeConsult {
			continue
		}
		// A consult agent name is <binding>-<role>-<8 hex>: one separator on
		// each side of the role, plus the 8-hex id.
		if len(bindingName)+1+len(role.Name)+1+8 > store.MaxAgentNameLen {
			tooLong = append(tooLong, role.Name)
		}
	}
	sort.Strings(tooLong)
	return tooLong
}

// ConsultRolesTooLong returns, sorted, the consult roles for which
// bindingName + "-" + role + "-" + <8 hex> would exceed the agent-name limit
// and so be refused at `relay ask`. It is pure, and empty when every consult
// role fits or bindingName is empty.
//
// It backs the advisory note printed after a successful bind, add or fork:
// a binding that can build but cannot take a consult role is still
// useful, so relay warns when the name is chosen rather than refusing.
//
// It is ConsultRolesTooLongFor over the built-in roles: the legacy registry's
// names are the built-ins, so the result is unchanged.
func ConsultRolesTooLong(bindingName string) []string {
	return ConsultRolesTooLongFor(legacyRegistry(nil, policy.Policy{}), bindingName)
}
