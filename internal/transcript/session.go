package transcript

import (
	"bytes"
	"encoding/json"
)

// SessionID is the harness's own session id as one line of its stream
// announces it (#147), or "" when the line announces none. It is pure and
// never errors: the stream is read for one string, nothing else.
//
// Each harness names the id differently, and it is not always on the first
// line, so the caller feeds the lines in order and takes the first that
// answers:
//
//   - claude:   session_id, on any event type;
//   - opencode: sessionID, on every event;
//   - agy:      conversation_id, top-level, or nested in the event's own
//     object (init's init, step_update's step_update) when the top-level
//     one is absent;
//   - codex:    thread_id, on the thread.started event only.
//
// Any other kind, a line that is not a JSON object (the relevo-exit trailer),
// and an id that is not a string all answer "". A missing id is never an
// error and never a guess.
func SessionID(kind string, line []byte) string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return ""
	}
	switch kind {
	case "claude":
		return str(obj["session_id"])
	case "opencode":
		return str(obj["sessionID"])
	case "agy":
		if id := str(obj["conversation_id"]); id != "" {
			return id
		}
		// The id also rides nested in the event's own object: agy's init
		// line carries init.conversation_id, and a step_update line carries
		// step_update.conversation_id (#147).
		if ev := str(obj["event"]); ev != "" {
			return str(asMap(obj[ev])["conversation_id"])
		}
		return ""
	case "codex":
		if str(obj["type"]) == "thread.started" {
			return str(obj["thread_id"])
		}
		return ""
	}
	return ""
}
