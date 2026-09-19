package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

// RenderRecord turns one line of a harness's own session record into the
// lines to append to the log (#184). It differs from Render in what
// "unknown" means: a record file is a superset of the stream with
// housekeeping records the stream never has, so an unknown record type,
// a non-JSON line, or a kind with no record table renders as nothing.
// Never errors, never panics.
func RenderRecord(kind string, line []byte) []string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	var obj map[string]any
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		return nil
	}
	if kind != "claude" {
		return nil
	}
	return renderClaudeRecord(obj)
}

// renderClaudeRecord is RenderRecord's table for kind "claude": an
// assistant record, and a user record carrying a tool_result block, render
// exactly as the stream renders them; a user record that is a typed prompt
// (relay's or the human's) renders as "> " plus its first line; anything
// else -- the ~18 housekeeping record types a session file carries that the
// stream never has (attachment, permission-mode, mode, last-prompt,
// atis-latch, agent-setting, queue-operation, file-history-snapshot,
// file-history-delta, system, summary, pr-link, ...) -- renders as nothing.
func renderClaudeRecord(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "assistant":
		return renderClaude(obj)
	case "user":
		if hasToolResult(obj) {
			return renderClaude(obj)
		}
		return renderClaudePrompt(obj)
	}
	return nil
}

// hasToolResult reports whether a user record carries a tool_result block.
func hasToolResult(obj map[string]any) bool {
	for _, blk := range contentBlocks(obj) {
		if str(blk["type"]) == "tool_result" {
			return true
		}
	}
	return false
}

// renderClaudePrompt renders a user record that is a typed prompt, not a
// tool result: "> " plus the first non-empty line of its text, truncated to
// maxArg runes with "…". message.content is a string or a list of
// text-only blocks; anything else (mixed or non-text blocks, or no usable
// text) is not a prompt shape and renders as nothing.
func renderClaudePrompt(obj map[string]any) []string {
	text, ok := claudePromptText(obj)
	if !ok {
		return nil
	}
	line := firstNonEmptyLine(text)
	if line == "" {
		return nil
	}
	return []string{"> " + truncateRunes(line, maxArg)}
}

// claudePromptText is a user record's typed text: message.content as a
// string, or the newline-joined text of a list whose blocks are all
// "text". ok is false for any other shape (missing content, or a
// tool_result or other non-text block anywhere in the list).
func claudePromptText(obj map[string]any) (string, bool) {
	content := asMap(obj["message"])["content"]
	if s, ok := content.(string); ok {
		return s, true
	}
	list := asList(content)
	if list == nil {
		return "", false
	}
	var parts []string
	for _, b := range list {
		blk := asMap(b)
		if str(blk["type"]) != "text" {
			return "", false
		}
		parts = append(parts, str(blk["text"]))
	}
	return strings.Join(parts, "\n"), true
}

// firstNonEmptyLine is the first line of s with non-whitespace content, its
// trailing "\r" stripped; "" when every line is blank.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// truncateRunes keeps at most n runes of s, marked with a trailing "…" when
// any were cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
