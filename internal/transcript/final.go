package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

// FinalText returns the last assistant text a harness stream carries: the
// message a one-shot process leaves behind that reads as its answer. It is a
// pure function of the stream and the kind, "" when the stream carries none.
//
// A headless consult asks the model for its findings as its final message
// rather than a file (`relevo ask --headless`), because a read-tier process
// may not be able to write one; relevo extracts that message here. Rules per
// kind:
//
//	claude:   the last assistant event's concatenated text blocks; if the
//	          stream carries no assistant text at all, the result event's
//	          result string.
//	opencode: the last {"type":"text", part.text} event's text.
//	agy:      the result event's result.response; when it is empty, the last
//	          step_update's text.
//	codex:    the last item.completed whose item.type is agent_message,
//	          item.text.
//
// The relevo-exit trailer and any line that is not a JSON object are ignored,
// and the result is trimmed. An unknown kind yields "".
func FinalText(kind string, stream []byte) string {
	var last, fallback string

	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
			continue
		}

		switch kind {
		case "claude":
			switch str(obj["type"]) {
			case "assistant":
				if t := strings.Join(claudeTextBlocks(obj), ""); t != "" {
					last = t
				}
			case "result":
				if r := str(obj["result"]); r != "" {
					fallback = r
				}
			}
		case "opencode":
			if str(obj["type"]) == "text" {
				if t := str(asMap(obj["part"])["text"]); t != "" {
					last = t
				}
			}
		case "agy":
			switch str(obj["event"]) {
			case "result":
				if r := str(asMap(obj["result"])["response"]); r != "" {
					last = r
				}
			case "step_update":
				if t := str(asMap(obj["step_update"])["text"]); t != "" {
					fallback = t
				}
			}
		case "codex":
			if str(obj["type"]) == "item.completed" {
				item := asMap(obj["item"])
				if str(item["type"]) == "agent_message" {
					if t := str(item["text"]); t != "" {
						last = t
					}
				}
			}
		}
	}

	if last == "" {
		last = fallback
	}
	return strings.TrimSpace(last)
}

// claudeTextBlocks is an assistant event's message.content text blocks, in
// order. It is final.go's own reader of the shape renderClaude walks: a
// change to the renderer must not silently change what FinalText returns.
func claudeTextBlocks(obj map[string]any) []string {
	var out []string
	for _, b := range asList(asMap(obj["message"])["content"]) {
		blk := asMap(b)
		if str(blk["type"]) != "text" {
			continue
		}
		if t := str(blk["text"]); t != "" {
			out = append(out, t)
		}
	}
	return out
}
