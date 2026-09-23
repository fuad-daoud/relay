package harness

// IsShipped reports whether name is one of the agent definitions kind ships.
// It is the question a resolved definition's Custom flag asks: a name relevo
// does not ship resolves to the kind's path convention instead (#374 §5).
//
// An unknown kind gives false.
func IsShipped(kind, name string) bool {
	h, ok := Lookup(kind)
	if !ok {
		return false
	}
	_, ok = h.Role(name)
	return ok
}

// DefinitionPath returns the home-relative path of the agent definition name
// for kind, and false for an unknown kind.
//
// A shipped name returns that row's Path exactly (for example
// ".claude/agents/plan-executor.md"), because the shipped file is the one
// relevo installs and refreshes (#371). Any other name follows the per-kind
// convention route: a custom definition relevo never writes, but a caller must
// still know where its file lives.
//
//	name already passed the roles name rule; this function does not
//	validate it (roles §5.1 rejects a name a path could not carry).
func DefinitionPath(kind, name string) (string, bool) {
	h, ok := Lookup(kind)
	if !ok {
		return "", false
	}
	if r, ok := h.Role(name); ok {
		return r.Path, true
	}
	switch kind {
	case "claude":
		return ".claude/agents/" + name + ".md", true
	case "opencode":
		return ".config/opencode/agents/" + name + ".md", true
	case "agy":
		return ".gemini/config/agents/" + name + ".md", true
	case "codex":
		return ".codex/" + name + ".config.toml", true
	}
	return "", false
}
