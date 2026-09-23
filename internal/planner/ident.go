package planner

import (
	"regexp"
	"strconv"
)

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

// agyConversationEnv is the variable agy injects into every command it runs:
// the id of the conversation whose agentapi inbox that command may push to
// (#349). Unlike CLAUDECODE it is present inside agy whether or not a session
// hook ran, and it is what names the planner record.
const agyConversationEnv = "ANTIGRAVITY_CONVERSATION_ID"

// conversationIDRe is the shape of an agy conversation id: a lower-case
// 8-4-4-4-12 hex UUID. internal/relay carries its own copy of this pattern
// (agy_creds.go): the rule is two lines, and relay's deliverer must not depend
// on planner's internals for it.
var conversationIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validConversationID reports whether id is an agy conversation id.
func validConversationID(id string) bool { return conversationIDRe.MatchString(id) }

// Detect reports which harness process is calling. `claude` is detected from
// CLAUDECODE and `agy` from a valid ANTIGRAVITY_CONVERSATION_ID; opencode tools
// see no session id, so opencode alone registers explicitly with
// `relay planner init --kind ... --session ...` (§1.1, §4.2). Claude is checked
// first, so CLAUDECODE=1 wins even when agy's variables are in the same
// environment. It never shells out and never reads a file -- the host's start
// time is the caller's business (ProcStart) -- so it is a pure function of its
// arguments and is table-tested.
//
// ppid is the caller's parent pid (os.Getppid()), used as HostPID when
// CLAUDE_PID is unset or unparseable.
func Detect(env func(string) string, ppid int) (Ident, bool) {
	if env == nil {
		return Ident{}, false
	}

	if env(claudeEnvMarker) == "1" {
		ident := Ident{
			Kind:      "claude",
			SessionID: env("CLAUDE_CODE_SESSION_ID"),
		}
		// CLAUDE_PID is set in the Bash tool and names the Claude process;
		// inside `relay mcp` and the hook it is unset, and there the parent
		// pid is that same process. Both spellings land on one pid for one
		// session (§1.1).
		if pid, err := strconv.Atoi(env("CLAUDE_PID")); err == nil && pid > 0 {
			ident.HostPID = pid
		} else {
			ident.HostPID = ppid
		}
		return ident, true
	}

	// agy names no process relay can follow: the pid a command runs under is
	// not a stable host for the session, so HostPID stays 0. That is exactly
	// what Record.Validate allows -- host_started_at must be 0 whenever
	// host_pid is 0 -- and plannerHostStart(0) returns 0 for it.
	if conv := env(agyConversationEnv); validConversationID(conv) {
		return Ident{Kind: "agy", SessionID: conv, HostPID: 0}, true
	}

	return Ident{}, false
}
