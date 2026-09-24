package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNotAWriterRole reports a role given to add/bind/fork that is a reader.
// A reader runs through `relevo ask --actor`, never as a binding's writer actor.
var ErrNotAWriterRole = errors.New("not a writer actor")

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

// NormRole is normRole for callers outside this package. It is exported for
// internal/serve, which resolves a remote add's role against the server's own
// roles.json and must store the same "" for builder (#382).
func NormRole(role string) string {
	return normRole(role)
}

// checkWriterRole returns nil when role (after normRole; "" means builder) is a
// writer in reg. Otherwise it reports an unknown role wrapping ErrUnknownRole,
// or a reader wrapping ErrNotAWriterRole.
func checkWriterRole(reg *roles.Registry, role string) error {
	r, ok := reg.Role(bindingRole(store.Binding{Role: normRole(role)}))
	if !ok {
		return fmt.Errorf("unknown actor %q (known: %v): %w", role, reg.Names(), ErrUnknownRole)
	}
	if r.Shape != harness.ShapeBuilder {
		return fmt.Errorf("--actor %s: a reader actor runs through relevo ask --actor %s: %w", role, role, ErrNotAWriterRole)
	}
	return nil
}

// CheckWriterRole reports whether role (after normRole; "" means builder) is a
// writer role in rt's registry (#382). A name no role has is an error wrapping
// ErrUnknownRole; a reader is one wrapping ErrNotAWriterRole. It is exported
// for internal/serve, which resolves a remote add's role against the server's
// own roles.json, never the client's.
func CheckWriterRole(rt Runtime, role string) error {
	return checkWriterRole(rt.RoleRegistry(), role)
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
		return harness.RoleSpec{}, fmt.Errorf("binding %s runs actor %q, which config roles no longer defines: %w", b.Name, role, ErrUnknownRole)
	}
	return rt.RoleRegistry().Spec(role, kind)
}
