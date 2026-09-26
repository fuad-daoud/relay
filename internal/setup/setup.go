// Package setup builds the starter candidates and policy `relevo config init`
// writes, and lands them under the user's config directory.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

type Default struct{ Provider, Model string }

// Defaults is the README's documented example: relevo's real defaults, which pass candidate.Load as written.
var Defaults = map[string]Default{
	"claude":   {"anthropic", "sonnet"},
	"opencode": {"openrouter", "z-ai/glm-5.3-flash"},
	"agy":      {"google", "gemini-3.8-flash-high"},
	"codex":    {"openai", "gpt-5.6-terra:high"},
}

// Files is the starter configuration Plan produced, ready to store.
type Files struct {
	Kinds      []string // harness kinds found on PATH, in harness.All() order
	Candidates []byte   // JSON, indented two spaces, trailing newline
	Policy     []byte   // JSON, indented two spaces, trailing newline
	Actors     []byte   // JSON, the builder actor over the plan's candidate names
}

// Plan builds starter candidates, a policy and a builder actor for every harness binary on PATH, erroring when none is found.
func Plan(env harness.InstallEnv) (Files, error) {
	var kinds []string
	var candidates []candidate.Candidate
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
		})
	}
	if len(kinds) == 0 {
		return Files{}, errors.New("no harness binaries on PATH (agy, claude, codex, opencode); install one first")
	}

	candJSON, err := json.MarshalIndent(candidates, "", "  ")
	if err != nil {
		return Files{}, fmt.Errorf("marshal candidates: %w", err)
	}
	candJSON = append(candJSON, '\n')

	pol := policy.Policy{MaxTier: "yolo"}
	polJSON, err := json.MarshalIndent(pol, "", "  ")
	if err != nil {
		return Files{}, fmt.Errorf("marshal policy: %w", err)
	}
	polJSON = append(polJSON, '\n')

	names := candidate.DeriveNames(candidates)
	entries := make([]roles.Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, roles.Entry{Candidate: name})
	}
	actorsJSON, err := roles.EncodeActors(map[string]roles.Actor{
		"builder": {Agent: "plan-executor", Candidates: entries, Tier: "yolo"},
	})
	if err != nil {
		return Files{}, fmt.Errorf("marshal actors: %w", err)
	}

	return Files{Kinds: kinds, Candidates: candJSON, Policy: polJSON, Actors: actorsJSON}, nil
}
