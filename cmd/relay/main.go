// Command relay automates plan and report handoff between a planner agent pane
// and a builder agent pane running under herdr.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "relay: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: relay <bind|send|answer|pull|status|log|watch|done|unbind|daemon>")
	}

	switch args[0] {
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}
