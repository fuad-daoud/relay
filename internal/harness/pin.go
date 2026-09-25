package harness

import "strings"

// frontmatterModel returns the value of a `model:` key in the leading `---`
// fenced block, or "" when there is none.
//
// Deliberately shallow: this reports a fact about an installed file for a
// human to read, so a malformed file yields "" rather than an error. An
// absent model pin is not a fault, and a parse failure must never mask the
// fact that the file exists.
func frontmatterModel(raw []byte) string {
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	// Locate the closing fence first: a `model:` line inside frontmatter
	// that never terminates is not a pin.
	end := -1
	for i, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "" // unterminated frontmatter
	}
	for _, line := range lines[1 : end+1] {
		rest, ok := strings.CutPrefix(line, "model:")
		if !ok {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return "" // frontmatter ended with no model key
}

// PinnedModel returns the model pin recorded in an installed role
// definition's raw bytes, dispatching on the kind's shipped format: a TOML
// profile's top-level `model` key, or a Markdown definition's frontmatter
// `model:` key.
func PinnedModel(kind string, raw []byte) string {
	if h, ok := Lookup(kind); ok && h.DocExt == "toml" {
		return tomlTopLevelModel(raw)
	}
	return frontmatterModel(raw)
}

// tomlTopLevelModel returns the value of a top-level `model = "..."` key,
// or "" when there is none. The first `[table]` header ends the top level,
// so a model key inside a table (e.g. [agents.researcher]) is not a pin
// for the profile itself. `model_reasoning_effort` must not match: after
// trimming the "model" prefix, the remainder starts with "_reasoning_effort",
// which does not begin with "=" once trimmed, so the line is skipped.
func tomlTopLevelModel(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			return ""
		}
		rest, ok := strings.CutPrefix(line, "model")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		rest, ok = strings.CutPrefix(rest, "=")
		if !ok {
			continue
		}
		v := strings.TrimSpace(rest)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			return v[1 : len(v)-1]
		}
	}
	return ""
}
