package harness

// RoleChecker reports which of a harness kind's shipped role definitions are
// missing from disk, so a candidate can be gated before it is picked rather
// than spawned and left to die within seconds (#238).
type RoleChecker interface {
	// Missing returns the home-relative paths (harness.Role.Path) of the
	// definitions the builder role needs for this harness kind that are not
	// readable on disk. nil means every definition is present.
	Missing(kind string) []string
}

// MissingDefinitions checks each of definitions -- role names such as
// "plan-executor", "researcher" -- against kind's shipped role files on
// disk through env, returning the home-relative path of every one that is
// not readable. An unknown kind returns nil: the candidate loader already
// refuses it, so there is nothing new to gate here.
func MissingDefinitions(env InstallEnv, kind string, definitions []string) []string {
	h, ok := Lookup(kind)
	if !ok {
		return nil
	}
	var missing []string
	for _, def := range definitions {
		r, ok := h.Role(def)
		if !ok {
			continue
		}
		path, err := env.HomePath(r.Path)
		if err != nil {
			missing = append(missing, r.Path)
			continue
		}
		if _, err := env.ReadFile(path); err != nil {
			missing = append(missing, r.Path)
		}
	}
	return missing
}

// osRoleChecker is RoleChecker backed by the real filesystem.
type osRoleChecker struct{ env InstallEnv }

// OSRoleChecker returns a RoleChecker backed by the OS filesystem and the
// user's real home directory.
func OSRoleChecker() RoleChecker {
	return osRoleChecker{OSInstallEnv()}
}

// Missing checks kind against the builder role's definitions -- what
// resolveCandidate cares about, since the builder is the only role relay
// picks a candidate for through the gated pick path.
func (c osRoleChecker) Missing(kind string) []string {
	role, ok := RoleByName("builder")
	if !ok {
		return nil
	}
	return MissingDefinitions(c.env, kind, role.Definitions)
}
