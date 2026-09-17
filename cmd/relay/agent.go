package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/harness"
)

func cmdAgent(args []string) error {
	const usage = `usage: relay agent print   --kind <agy|claude|opencode> [--role <plan-executor|researcher|reviewer|architect>]
       relay agent install [--kind <agy|claude|opencode>] [--role <name>] [--force] [--dry-run]`
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
	switch args[0] {
	case "print":
		return cmdAgentPrint(args[1:])
	case "install":
		return cmdAgentInstall(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relay agent: unknown command %q\n", args[0])
		return exitCodeErr{code: 2}
	}
}

func cmdAgentPrint(args []string) error {
	fs := flag.NewFlagSet("relay agent print", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	kind := fs.String("kind", "", "harness kind to print plan-executor definition for")
	role := fs.String("role", "plan-executor", "role definition to print (plan-executor, researcher, reviewer, architect)")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if *kind == "" {
		fmt.Fprintln(os.Stderr, "usage: relay agent print --kind <agy|claude|opencode> [--role <plan-executor|researcher|reviewer|architect>]")
		return exitCodeErr{code: 2}
	}

	doc, err := harness.AgentDoc(*role, *kind)
	if err != nil {
		if h, ok := harness.Lookup(*kind); ok {
			fmt.Fprintf(os.Stderr, "relay: kind %q has no role %q (known: %v)\n",
				*kind, *role, h.RoleNames())
		} else {
			fmt.Fprintf(os.Stderr, "relay: unknown kind %q (known: agy, claude, opencode)\n", *kind)
		}
		return exitCodeErr{code: 2}
	}

	if _, err := os.Stdout.Write(doc); err != nil {
		return err
	}
	return nil
}

func cmdAgentInstall(args []string) error {
	fs := flag.NewFlagSet("relay agent install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	kind := fs.String("kind", "", "harness kind")
	role := fs.String("role", "", "role name")
	force := fs.Bool("force", false, "force overwrite")
	dryRun := fs.Bool("dry-run", false, "dry run")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	opts := harness.InstallOptions{
		Kind:   *kind,
		Role:   *role,
		Force:  *force,
		DryRun: *dryRun,
	}

	results, err := harness.Install(harness.OSInstallEnv(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: %v\n", err)
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
