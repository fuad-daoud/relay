package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// agentInstallEnv is the InstallEnv `relevo config agents` uses -- and the
// same one the daemon's once-per-image role refresh uses (#371 §4.10): the
// OS-backed env with the role manifest in the machine database's kv row
// "agents-manifest", importing a legacy <state root>/agents-manifest.json on
// first read (P3b plan §4.4). The state root is composed here through
// store.DefaultRoot, the one path relevo's state always resolves through
// (CLAUDE.md, #42), and its database is opened here because internal/harness
// cannot import internal/store.
func agentInstallEnv() (harness.InstallEnv, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	d, err := store.New(root).DB()
	if err != nil {
		return nil, err
	}
	return harness.OSInstallEnvKV(d, filepath.Join(root, "agents-manifest.json")), nil
}

// cmdAgentInstall installs embedded agent role definitions, the body
// `relevo config agents` had (now `relevo config agents`).
func cmdAgentInstall(args []string) error {
	fs := flag.NewFlagSet("relevo config agents", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	kind := fs.String("kind", "", "harness kind")
	role := fs.String("role", "", "role name")
	force := fs.Bool("force", false, "force overwrite")
	dryRun := fs.Bool("dry-run", false, "dry run")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	opts := harness.InstallOptions{
		Kind:   *kind,
		Role:   *role,
		Force:  *force,
		DryRun: *dryRun,
	}

	env, err := agentInstallEnv()
	if err != nil {
		return err
	}

	results, err := harness.Install(env, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
		return exitCodeErr{code: 2}
	}

	if len(results) == 0 {
		fmt.Println("no harness binaries on PATH (agy, claude, opencode); nothing to install")
		return nil
	}

	failed := false
	for _, r := range results {
		fmt.Println(r.Line())
		if r.Outcome == harness.OutcomeError {
			failed = true
		}
	}
	if failed {
		return exitCodeErr{code: 1}
	}
	return nil
}
