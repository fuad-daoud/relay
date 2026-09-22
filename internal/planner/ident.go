package planner

import "strconv"

// Ident is which harness process is calling, as far as relay can tell without
// shelling out or reading a file (§4.2).
type Ident struct {
	Kind      string
	SessionID string
	// HostPID is the harness process: for Claude, $CLAUDE_PID in a Bash tool,
	// else the caller's parent pid (inside `relay mcp` and the SessionStart
	// hook, whose parent IS the Claude process).
	HostPID int
}

// claudeEnvMarker is the variable that says "this process is inside Claude
// Code" (§1.1, verified).
const claudeEnvMarker = "CLAUDECODE"

// Detect reports which harness process is calling. Only `claude` is detected:
// opencode tools see no session id and `agy` has no deliverer yet, so both
// register explicitly with `relay planner init --kind ... --session ...`
// (§1.1, §4.2). It never shells out and never reads a file -- the host's start
// time is the caller's business (ProcStart) -- so it is a pure function of its
// arguments and is table-tested.
//
// ppid is the caller's parent pid (os.Getppid()), used as HostPID when
// CLAUDE_PID is unset or unparseable.
func Detect(env func(string) string, ppid int) (Ident, bool) {
	if env == nil || env(claudeEnvMarker) != "1" {
		return Ident{}, false
	}

	ident := Ident{
		Kind:      "claude",
		SessionID: env("CLAUDE_CODE_SESSION_ID"),
	}
	// CLAUDE_PID is set in the Bash tool and names the Claude process; inside
	// `relay mcp` and the hook it is unset, and there the parent pid is that
	// same process. Both spellings land on one pid for one session (§1.1).
	if pid, err := strconv.Atoi(env("CLAUDE_PID")); err == nil && pid > 0 {
		ident.HostPID = pid
	} else {
		ident.HostPID = ppid
	}
	return ident, true
}
