package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/harness"
)

func cmdAgent(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: relay agent print --kind <agy|claude|opencode> [--role <plan-executor|researcher|reviewer>]")
		return exitCodeErr{code: 2}
	}
	switch args[0] {
	case "print":
		return cmdAgentPrint(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, "usage: relay agent print --kind <agy|claude|opencode> [--role <plan-executor|researcher|reviewer>]")
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
	role := fs.String("role", "plan-executor", "role definition to print (plan-executor, researcher, reviewer)")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if *kind == "" {
		fmt.Fprintln(os.Stderr, "usage: relay agent print --kind <agy|claude|opencode> [--role <plan-executor|researcher|reviewer>]")
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
