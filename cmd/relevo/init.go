package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/setup"
)

// cmdInit seeds the user's candidates.json and policy.json from the harness
// binaries on PATH, then installs the role definitions for those harnesses.
func cmdInit(args []string) error {
	fs := flag.NewFlagSet("relevo init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "overwrite existing candidates.json / policy.json")
	noRoles := fs.Bool("no-roles", false, "do not install role definitions")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	configDir, err := userConfigRoot()
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

	res, err := setup.Write(configDir, files, *force)
	if err != nil {
		return err
	}
	if !res.WroteCandidates && !res.WrotePolicy {
		return errors.New("~/.config/relevo/candidates.json and policy.json exist; pass --force to overwrite")
	}

	fmt.Printf("wrote %s (%d candidates: %s)\n",
		res.CandidatesPath, len(files.Kinds), strings.Join(files.Kinds, ", "))
	fmt.Printf("wrote %s (order.builder: %s)\n",
		res.PolicyPath, strings.Join(orderTokens(files.Kinds), ", "))

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

	fmt.Printf("next: edit the model names in %s, then run: relevo doctor\n", res.CandidatesPath)
	return nil
}

// orderTokens renders the policy order.builder tokens for kinds, in order.
func orderTokens(kinds []string) []string {
	tokens := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		d := setup.Defaults[kind]
		tokens = append(tokens, candidate.Ref{
			Harness:  kind,
			Provider: d.Provider,
			Model:    d.Model,
		}.String())
	}
	return tokens
}
