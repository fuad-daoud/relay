package harness

// RoleChecker reports which of the given role definitions are missing from
// disk, so a candidate can be gated before it is picked rather than spawned
// and left to die within seconds (#238).
type RoleChecker interface {
	// Missing returns the home-relative paths of the given definitions that
	// are not readable on disk for this harness kind. nil means every
	// definition is present.
	Missing(kind string, definitions []string) []string
}

// MissingDefinitions checks each of definitions -- role names such as
// "plan-executor" and "researcher", or a custom name a roles.json row names --
// against kind's definitions on disk through env, returning the home-relative
// path of every one that is not readable. Each name resolves through
// DefinitionPath, so a shipped name keeps its table path and a custom name
// follows its kind's path convention. An unknown kind returns nil: the
// candidate loader already refuses it, so there is nothing new to gate here.
func MissingDefinitions(env InstallEnv, kind string, definitions []string) []string {
	if _, ok := Lookup(kind); !ok {
		return nil
	}
	var missing []string
	for _, def := range definitions {
		rel, ok := DefinitionPath(kind, def)
		if !ok {
			continue
		}
		path, err := env.HomePath(rel)
		if err != nil {
			missing = append(missing, rel)
			continue
		}
		if _, err := env.ReadFile(path); err != nil {
			missing = append(missing, rel)
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

// Missing checks the given definitions for kind against disk, whatever role
// they were resolved for. The caller passes the definitions of the role it is
// gating, so a custom definition a roles.json row names is checked here too.
func (c osRoleChecker) Missing(kind string, definitions []string) []string {
	return MissingDefinitions(c.env, kind, definitions)
}
