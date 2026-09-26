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

// PlannerDefault is a reader actor config init seeds beside the builder: the
// actor name, the harness kind whose binary must be on PATH, and its one
// candidate's provider and model.
type PlannerDefault struct{ Actor, Kind, Provider, Model string }

// PlannerDefaults are those actors in written order: planner, then lite-planner.
var PlannerDefaults = []PlannerDefault{
	{"planner", "claude", "anthropic", "opus:medium"},
	{"lite-planner", "opencode", "openrouter", "deepseek/deepseek-v4.1-flash#max"},
}

// Files is the starter configuration Plan produced, ready to store.
type Files struct {
	Kinds      []string // harness kinds found on PATH, in harness.All() order
	Candidates []byte   // JSON, indented two spaces, trailing newline
	Policy     []byte   // JSON, indented two spaces, trailing newline
	Actors     []byte   // JSON, the builder actor plus the planner actors whose harness is on PATH
	ActorOrder []string // the actor names written: builder, then PlannerDefaults order (only those written)
}

// Plan builds starter candidates, a policy and starter actors for every harness binary on PATH, erroring when none is found.
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
	builderCount := len(candidates)

	order := []string{"builder"}
	var planners []PlannerDefault
	for _, p := range PlannerDefaults {
		h, ok := harness.Lookup(p.Kind)
		if !ok {
			continue
		}
		if _, err := env.LookPath(h.Binary); err != nil {
			continue
		}
		planners = append(planners, p)
		order = append(order, p.Actor)
		candidates = append(candidates, candidate.Candidate{
			Harness:  p.Kind,
			Provider: p.Provider,
			Model:    p.Model,
		})
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
	actorsJSON, err := roles.EncodeActors(starterActors(names[:builderCount], planners, names[builderCount:]))
	if err != nil {
		return Files{}, fmt.Errorf("marshal actors: %w", err)
	}

	return Files{Kinds: kinds, Candidates: candJSON, Policy: polJSON, Actors: actorsJSON, ActorOrder: order}, nil
}

// starterActors assembles the builder actor and one reader actor per written
// planner, each with the candidate names DeriveNames produced.
func starterActors(builderNames []string, planners []PlannerDefault, plannerNames []string) map[string]roles.Actor {
	actors := map[string]roles.Actor{
		"builder": {Agent: "plan-executor", Tier: "yolo", Candidates: candidateEntries(builderNames)},
	}
	for i, p := range planners {
		actors[p.Actor] = roles.Actor{Agent: "architect", Candidates: candidateEntries(plannerNames[i : i+1])}
	}
	return actors
}

// candidateEntries wraps names as an actor's candidates, each on.
func candidateEntries(names []string) []roles.Entry {
	entries := make([]roles.Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, roles.Entry{Candidate: name})
	}
	return entries
}
