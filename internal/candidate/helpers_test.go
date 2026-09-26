package candidate

import (
	"os"
	"path/filepath"
	"testing"
)

// threeSetBody is the set the query tests read: a two-role candidate, a
// multi-segment model and a provider that shares its name with nothing.
const threeSetBody = `[
	{
		"harness": "claude",
		"provider": "anthropic",
		"model": "sonnet",
		"roles": ["builder", "reviewer"]
	},
	{
		"harness": "opencode",
		"provider": "openrouter",
		"model": "z-ai/glm-5.3-flash",
		"roles": ["builder"]
	},
	{
		"harness": "agy",
		"provider": "google",
		"model": "gemini-3.8-flash-high",
		"roles": ["builder"]
	}
]`

// twoSetBody is the set the name and resolve tests read: names must be derived
// for both entries.
const twoSetBody = `[
	{"harness":"claude","provider":"anthropic","model":"sonnet"},
	{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high"}
]`

// oneSetBody is a single candidate whose derived name is "sonnet".
const oneSetBody = `[{"harness":"claude","provider":"anthropic","model":"sonnet"}]`

// writeTemp writes body to a candidates.json under t.TempDir and returns its
// path.
func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeCandidates writes body to a temp candidates.json and loads it with
// Load, failing on any error.
func writeCandidates(t *testing.T, body string) *Set {
	t.Helper()
	set, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return set
}

// parseSet validates body with Parse, failing on any error.
func parseSet(t *testing.T, body string) *Set {
	t.Helper()
	set, _, err := Parse("candidates.json", []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return set
}

// parseWarnings validates body with Parse and returns its warning list,
// failing on any error.
func parseWarnings(t *testing.T, body string) (*Set, []string) {
	t.Helper()
	set, warnings, err := Parse("candidates.json", []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return set, warnings
}

// loadWarnings writes body to a temp candidates.json and loads it with
// LoadWithWarnings, failing on any error.
func loadWarnings(t *testing.T, body string) (*Set, []string) {
	t.Helper()
	set, warnings, err := LoadWithWarnings(writeTemp(t, body))
	if err != nil {
		t.Fatalf("LoadWithWarnings(%s): %v", body, err)
	}
	return set, warnings
}
