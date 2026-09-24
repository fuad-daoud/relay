package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/setup"
)

// cmdInit seeds the candidates, policy and actors sections from the harness
// binaries on PATH, then installs the role definitions for those harnesses.
func cmdInit(args []string) error {
	fs := flag.NewFlagSet("relevo config init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "overwrite the existing candidates / policy sections")
	noRoles := fs.Bool("no-roles", false, "do not install role definitions")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	env, err := agentInstallEnv()
	if err != nil {
		return err
	}

	files, err := setup.Plan(env)
	if err != nil {
		return err
	}

	hasCandidates, err := rt.Config.Has(config.Candidates)
	if err != nil {
		return err
	}
	hasPolicy, err := rt.Config.Has(config.Policy)
	if err != nil {
		return err
	}
	hasActors, err := rt.Config.Has(config.Actors)
	if err != nil {
		return err
	}
	if !*force && (hasCandidates || hasPolicy || hasActors) {
		return errors.New("candidates, policy or actors already configured; pass --force to overwrite")
	}

	// One PutDoc writes all three sections as one revision (A2 round 2 R5).
	doc := map[config.Section]json.RawMessage{
		config.Candidates: files.Candidates,
		config.Policy:     files.Policy,
		config.Actors:     files.Actors,
	}
	if _, err := rt.Config.As("init", "config init").PutDoc(doc); err != nil {
		return err
	}

	fmt.Printf("wrote candidates (%d: %s)\n", len(files.Kinds), strings.Join(files.Kinds, ", "))
	names, err := actorNames(files.Candidates)
	if err != nil {
		return err
	}
	fmt.Printf("wrote actors (builder: %s)\n", strings.Join(names, ", "))

	if !*noRoles {
		failed := false
		for _, kind := range files.Kinds {
			results, err := harness.Install(env, harness.InstallOptions{Kind: kind})
			if err != nil {
				return err
			}
			for _, r := range results {
				fmt.Println(r.Line())
				if r.Outcome == harness.OutcomeError {
					failed = true
				}
			}
		}
		if failed {
			return exitCodeErr{code: 1}
		}
	}

	fmt.Printf("next: edit the model names, then run: relevo doctor\n")
	return nil
}

// actorNames renders the candidate names setup.Plan's builder actor wrote, in
// order. It is the same derivation Plan uses (candidate.DeriveNames), so init
// reports the names a planner will see.
func actorNames(candidates []byte) ([]string, error) {
	var cs []candidate.Candidate
	if err := json.Unmarshal(candidates, &cs); err != nil {
		return nil, err
	}
	return candidate.DeriveNames(cs), nil
}
