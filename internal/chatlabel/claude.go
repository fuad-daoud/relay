package chatlabel

import (
	"bytes"
	"encoding/json"
	"strings"
)

// claudeEntry is the slice of a Claude Code transcript line this package
// reads. It is deliberately small: unknown fields are ignored, and a line that
// carries none of these contributes nothing.
type claudeEntry struct {
	Type            string `json:"type"`
	CustomTitle     string `json:"customTitle"`
	AITitle         string `json:"aiTitle"`
	LastPrompt      string `json:"lastPrompt"`
	BridgeSessionID string `json:"bridgeSessionId"`
}

// Claude builds the Label for a Claude Code transcript (JSONL) from its tail.
// It never errors and never panics: a line that is blank, does not start with
// `{`, or fails to decode is skipped.
//
// The order of the title sources is the one a person would expect to see: the
// name they set with /rename, then the automatic chat title, then the last
// prompt they typed.
func Claude(tail []byte) Label {
	var customTitle, aiTitle, lastPrompt, bridgeSession string

	for _, line := range bytes.Split(tail, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e claudeEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		// Each entry type re-appends through a session, so the latest
		// non-empty value of its field is the current one.
		switch e.Type {
		case "custom-title":
			if e.CustomTitle != "" {
				customTitle = e.CustomTitle
			}
		case "ai-title":
			if e.AITitle != "" {
				aiTitle = e.AITitle
			}
		case "last-prompt":
			if e.LastPrompt != "" {
				lastPrompt = e.LastPrompt
			}
		case "bridge-session":
			if e.BridgeSessionID != "" {
				bridgeSession = e.BridgeSessionID
			}
		}
	}

	label := Label{}
	switch {
	case clean(customTitle) != "":
		label.Text = clean(customTitle)
	case clean(aiTitle) != "":
		label.Text = clean(aiTitle)
	case clean(lastPrompt) != "":
		label.Text = quote(clean(lastPrompt))
	}

	// A bridged session's id carries a cse_ prefix the claude.ai URL does not.
	if rest, ok := strings.CutPrefix(bridgeSession, "cse_"); ok && rest != "" {
		label.Link = "https://claude.ai/code/session_" + rest
	}
	return label
}
