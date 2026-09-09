package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/harness"
)

func cmdAgent(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: relay agent print --kind <claude|opencode>")
		return exitCodeErr{code: 2}
	}
	switch args[0] {
	case "print":
		return cmdAgentPrint(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, "usage: relay agent print --kind <claude|opencode>")
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
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if *kind == "" {
		fmt.Fprintln(os.Stderr, "usage: relay agent print --kind <claude|opencode>")
		return exitCodeErr{code: 2}
	}

	if *kind == "agy" {
		fmt.Fprintln(os.Stderr, "agy selects its role with a preamble on the first prompt, not an agent file; see the abuilder alias in ~/.config/relay/aliases.json")
		return exitCodeErr{code: 2}
	}

	doc, err := harness.AgentDoc(*kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: unknown kind %q (known with definitions: claude, opencode)\n", *kind)
		return exitCodeErr{code: 2}
	}

	if _, err := os.Stdout.Write(doc); err != nil {
		return err
	}
	return nil
}
