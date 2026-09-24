package main

import (
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

// cmdInit seeds the candidates and policy sections from the harness binaries
// on PATH, then installs the role definitions for those harnesses.
func cmdInit(args []string) error {
	fs := flag.NewFlagSet("relevo init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "overwrite existing candidates.json / policy.json")
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
	if !*force && (hasCandidates || hasPolicy) {
		return errors.New("candidates or policy already configured; pass --force to overwrite")
	}

	if _, err := rt.Config.Put(config.Candidates, files.Candidates); err != nil {
		return err
	}
	if _, err := rt.Config.Put(config.Policy, files.Policy); err != nil {
		return err
	}

	fmt.Printf("wrote candidates (%d: %s)\n", len(files.Kinds), strings.Join(files.Kinds, ", "))
	fmt.Printf("wrote policy (order.builder: %s)\n", strings.Join(orderTokens(files.Kinds), ", "))

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
