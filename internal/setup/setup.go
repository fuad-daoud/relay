// Package setup builds the starter candidates.json and policy.json relay init
// writes, and lands them under the user's config directory.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/policy"
)

// Default is the provider and model relay init seeds a harness with.
type Default struct{ Provider, Model string }

// Defaults is the README's documented example, one per harness kind. The values
// are relay's real defaults: they are the placeholders a clean machine edits,
// and they pass candidate.Load as written.
var Defaults = map[string]Default{
	"claude":   {"anthropic", "sonnet"},
	"opencode": {"openrouter", "z-ai/glm-5.3-flash"},
	"agy":      {"google", "gemini-3.8-flash-high"},
	"codex":    {"openai", "gpt-5.6-terra:high"},
}

// Files is the starter configuration Plan produced, ready to write.
type Files struct {
	Kinds      []string // harness kinds found on PATH, in harness.All() order
	Candidates []byte   // JSON, indented two spaces, trailing newline
	Policy     []byte   // JSON, indented two spaces, trailing newline
}

// WriteResult reports where the config files live and which of them landed.
type WriteResult struct {
	CandidatesPath, PolicyPath   string
	WroteCandidates, WrotePolicy bool // false = existed and !force
}

// Plan builds starter candidates and a policy for every harness binary on PATH,
// in harness.All() order. It is an error when none is found: relay init has
// nothing to seed.
func Plan(env harness.InstallEnv) (Files, error) {
	var kinds []string
	var candidates []candidate.Candidate
	var order []string
	for _, h := range harness.All() {
		if _, err := env.LookPath(h.Binary); err != nil {
			continue
		}
		d := Defaults[h.Kind]
		kinds = append(kinds, h.Kind)
		candidates = append(candidates, candidate.Candidate{
			Harness:  h.Kind,
			Provider: d.Provider,
			Model:    d.Model,
			Roles:    []string{"builder"},
			Tier:     "yolo",
		})
		order = append(order, candidate.Ref{
			Harness:  h.Kind,
			Provider: d.Provider,
			Model:    d.Model,
		}.String())
	}
	if len(kinds) == 0 {
		return Files{}, errors.New("no harness binaries on PATH (agy, claude, codex, opencode); install one first")
	}

	candJSON, err := json.MarshalIndent(candidates, "", "  ")
	if err != nil {
		return Files{}, fmt.Errorf("marshal candidates: %w", err)
	}
	candJSON = append(candJSON, '\n')

	pol := policy.Policy{
		Order:   map[string][]string{"builder": order},
		Tier:    map[string]string{"builder": "yolo"},
		MaxTier: "yolo",
	}
	polJSON, err := json.MarshalIndent(pol, "", "  ")
	if err != nil {
		return Files{}, fmt.Errorf("marshal policy: %w", err)
	}
	polJSON = append(polJSON, '\n')

	return Files{Kinds: kinds, Candidates: candJSON, Policy: polJSON}, nil
}

// Write lands f under configDir/relay. Both files are decided before either is
// written, so a refused pair is never half-written: when force is false and
// either file exists, neither is written and both Wrote flags stay false.
func Write(configDir string, f Files, force bool) (WriteResult, error) {
	dir := filepath.Join(configDir, "relay")
	res := WriteResult{
		CandidatesPath: filepath.Join(dir, "candidates.json"),
		PolicyPath:     filepath.Join(dir, "policy.json"),
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}

	if !force && (fileExists(res.CandidatesPath) || fileExists(res.PolicyPath)) {
		return res, nil
	}

	if err := os.WriteFile(res.CandidatesPath, f.Candidates, 0o644); err != nil {
		return res, err
	}
	res.WroteCandidates = true

	if err := os.WriteFile(res.PolicyPath, f.Policy, 0o644); err != nil {
		return res, err
	}
	res.WrotePolicy = true

	return res, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
