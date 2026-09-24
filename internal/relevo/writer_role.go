package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNotAWriterRole reports a role given to add/bind/fork that is a reader.
// A reader runs through `relevo ask --role`, never as a binding's writer role.
var ErrNotAWriterRole = errors.New("not a writer role")

// BindingRole is the writer role b runs: b.Role, or "builder" when it is "".
// It is exported for internal/serve, which has a store.Binding and needs the
// same name a client-side binding would run (#382).
func BindingRole(b store.Binding) string {
	if b.Role == "" {
		return "builder"
	}
	return b.Role
}

// bindingRole is BindingRole for this package: every place that used to pass
// the literal "builder" as a role asks this instead.
func bindingRole(b store.Binding) string {
	return BindingRole(b)
}

// normRole is the stored form of a requested role: "" and "builder" both give
// "", because a builder binding never records the word "builder".
func normRole(role string) string {
	if role == "builder" {
		return ""
	}
	return role
}

// checkWriterRole returns nil when role (after normRole; "" means builder) is a
// writer in reg. Otherwise it reports an unknown role wrapping ErrUnknownRole,
// or a reader wrapping ErrNotAWriterRole.
func checkWriterRole(reg *roles.Registry, role string) error {
	r, ok := reg.Role(bindingRole(store.Binding{Role: normRole(role)}))
	if !ok {
		return fmt.Errorf("unknown role %q (known: %v): %w", role, reg.Names(), ErrUnknownRole)
	}
	if r.Shape != harness.ShapeBuilder {
		return fmt.Errorf("--role %s: a reader role runs through relevo ask --role %s: %w", role, role, ErrNotAWriterRole)
	}
	return nil
}

// roleGates reports whether a binding of role takes policy.json's gate.default
// when neither --gate nor --no-gate is given: the role's Gate, or true when the
// registry does not know the role (the caller has already refused that).
func roleGates(reg *roles.Registry, role string) bool {
	if r, ok := reg.Role(role); ok {
		return r.Gate
	}
	return true
}

// bindingSpec is the launch spec for a stored binding's round: the binding's
// role's definition for kind. A role that roles.json no longer defines is an
// error wrapping ErrUnknownRole -- never a fallback to builder (#382 §5.4).
func bindingSpec(rt Runtime, b store.Binding, kind string) (harness.RoleSpec, error) {
	role := bindingRole(b)
	if _, ok := rt.RoleRegistry().Role(role); !ok {
		return harness.RoleSpec{}, fmt.Errorf("binding %s runs role %q, which roles.json no longer defines: %w", b.Name, role, ErrUnknownRole)
	}
	return rt.RoleRegistry().Spec(role, kind)
}
