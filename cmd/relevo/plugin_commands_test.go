package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// commandVerbs collects every verb `run` dispatches, from main.go's `case
// "..."` labels: a label may list several strings (`case "help", "-h",
// "--help"`). Reading and regexing only -- nothing executes, so the cmd/relevo
// CI rule (no harness, no network) holds.
func commandVerbs(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	caseLine := regexp.MustCompile(`(?m)^\s*case\s+(.+):\s*$`)
	quoted := regexp.MustCompile(`"([^"]+)"`)

	verbs := map[string]bool{}
	for _, line := range caseLine.FindAllStringSubmatch(string(raw), -1) {
		for _, m := range quoted.FindAllStringSubmatch(line[1], -1) {
			verbs[m[1]] = true
		}
	}
	if len(verbs) == 0 {
		t.Fatal("no case labels found in main.go")
	}
	return verbs
}

// frontmatterField returns the trimmed value of a frontmatter key, and whether
// the key was present.
func frontmatterField(front, key string) (string, bool) {
	for _, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, key)), true
		}
	}
	return "", false
}

// TestPluginCommandFiles pins the slash-command files in
// claude-plugin/commands: each has a leading frontmatter block naming it and
// allowing exactly Bash(relevo:*), and exactly one line in the file starts
// `relevo `, whose verb is one the CLI actually dispatches. The realistic
// failure is a verb renamed in the CLI and not in the plugin. Files only, so
// no harness, no network and no Claude Code.
func TestPluginCommandFiles(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "claude-plugin", "commands", "*.md"))
	if err != nil {
		t.Fatalf("glob claude-plugin/commands/*.md: %v", err)
	}
	if len(paths) < 2 {
		t.Fatalf("found %d command file(s), want at least 2: %v", len(paths), paths)
	}

	verbs := commandVerbs(t)
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lines := strings.Split(string(raw), "\n")

		if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
			t.Errorf("%s: must start with a `---` frontmatter block", path)
			continue
		}
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			t.Errorf("%s: frontmatter block is not closed", path)
			continue
		}
		front := strings.Join(lines[1:end], "\n")

		if desc, ok := frontmatterField(front, "description:"); !ok || desc == "" {
			t.Errorf("%s: frontmatter needs a non-empty `description:` (got %q, present=%v)", path, desc, ok)
		}
		if tools, ok := frontmatterField(front, "allowed-tools:"); !ok || tools != "Bash(relevo:*)" {
			t.Errorf("%s: allowed-tools must be exactly `Bash(relevo:*)` (got %q, present=%v)", path, tools, ok)
		}

		// Exactly one line in the file starts `relevo `: the bang-fenced
		// preamble that Claude Code runs.
		var preambles []string
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "relevo ") {
				preambles = append(preambles, strings.TrimSpace(line))
			}
		}
		if len(preambles) != 1 {
			t.Errorf("%s: %d line(s) start with `relevo `, want exactly 1: %v", path, len(preambles), preambles)
			continue
		}
		fields := strings.Fields(preambles[0])
		if len(fields) < 2 {
			t.Errorf("%s: preamble %q has no verb", path, preambles[0])
			continue
		}
		if verb := fields[1]; !verbs[verb] {
			t.Errorf("%s: preamble verb %q is not one of main.go's commands", path, verb)
		}
	}
}
