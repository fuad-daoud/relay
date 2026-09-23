package transcript

import (
	"encoding/json"
	"strings"
)

// ErrorText reports whether one raw line of a harness's stream is a fatal
// error event, and returns the harness's own message for it, trimmed of
// surrounding whitespace.
//
// It is pure, and it is the probe's view of failure: a probe reports the
// harness's own reason instead of the stderr tail, because the real reason
// (a usage limit, say) arrives as a JSON event on stdout. It returns
// ("", false) for anything else: a non-JSON line, an unknown kind, or an
// empty message. A non-fatal warning a harness logs mid-run is not a run
// failure and is false here too.
func ErrorText(kind string, line []byte) (string, bool) {
	var obj map[string]any
	if json.Unmarshal(line, &obj) != nil || obj == nil {
		return "", false
	}

	var msg string
	switch kind {
	case "codex":
		// item.completed of an item type "error" is a warning the model
		// sees (e.g. "Exceeded skills context budget"), not a fatal error.
		switch str(obj["type"]) {
		case "error":
			msg = str(obj["message"])
		case "turn.failed":
			msg = str(asMap(obj["error"])["message"])
		}

	case "opencode":
		// A tool_use part in an error state is a tool failure the model
		// sees, not a run failure.
		if str(obj["type"]) == "error" {
			msg = str(asMap(obj["error"])["message"])
		}

	case "claude":
		switch str(obj["type"]) {
		case "result":
			if isErr, _ := obj["is_error"].(bool); isErr {
				msg = str(obj["result"])
				if msg == "" {
					msg = "error result"
				}
			}
		case "error":
			msg = str(obj["message"])
			if msg == "" {
				msg = str(asMap(obj["error"])["message"])
			}
		}

	case "agy":
		if str(obj["event"]) == "result" {
			r := asMap(obj["result"])
			if st := str(r["status"]); st != "" && st != "SUCCESS" {
				if e, ok := r["error"].(string); ok {
					msg = e
				} else {
					msg = str(asMap(r["error"])["message"])
				}
				if msg == "" {
					msg = "result status " + st
				}
			}
		}

	default:
		return "", false
	}

	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", false
	}
	return msg, true
}
