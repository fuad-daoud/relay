package transcript

import "encoding/json"

// FirstOutput reports whether one raw line of a harness's stream is the
// first kind of event that means the model produced output (#324 part 1).
//
// It is pure and never errors: it decodes the line and inspects its shape
// only, so a caller measuring time to first output needs no rendering and no
// state beyond "have I seen one yet". A non-JSON line -- the relevo-exit
// trailer, say -- and an unknown kind are false.
func FirstOutput(kind string, line []byte) bool {
	var obj map[string]any
	if json.Unmarshal(line, &obj) != nil || obj == nil {
		return false
	}

	switch kind {
	case "claude":
		return str(obj["type"]) == "assistant"
	case "opencode":
		switch str(obj["type"]) {
		case "text", "reasoning", "tool_use":
			return true
		}
		return false
	case "agy":
		switch str(obj["event"]) {
		case "result":
			return true
		case "step_update":
			return str(asMap(obj["step_update"])["step_type"]) == "agent_response"
		}
		return false
	case "codex":
		switch str(obj["type"]) {
		case "item.started", "item.completed":
			switch str(asMap(obj["item"])["type"]) {
			case "agent_message", "reasoning", "command_execution", "file_change":
				return true
			}
		}
		return false
	}

	return false
}
