package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// cmdEdge dispatches `relevo edge add|list|rm` (#37): a planner-declared
// handoff between two bindings, evaluated once at the source's round close.
func cmdEdge(args []string) error {
	const usage = `usage: relevo edge add <source> --when report|diff|done|gate --then send --target <binding> --prompt <file> [--mode queue|fire] [--round N]
       relevo edge list <source>
       relevo edge rm <source> <id>`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "add":
		return cmdEdgeAdd(args[1:])
	case "list":
		return cmdEdgeList(args[1:])
	case "rm":
		return cmdEdgeRm(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// cmdEdgeAdd validates its flags before touching a runtime, so a CI runner
// with no harness binary still fails on a missing flag rather than on the
// environment. --prompt may be relative;
// it is resolved against the CWD before AddEdge's absolute-path check.
func cmdEdgeAdd(args []string) error {
	fs := flag.NewFlagSet("relevo edge add", flag.ContinueOnError)
	when := fs.String("when", "", "artifact that fires the edge: report|diff|done|gate")
	then := fs.String("then", "", "what happens once --when's artifact exists; only \"send\" exists today")
	target := fs.String("target", "", "binding --then hands the prompt to")
	prompt := fs.String("prompt", "", "plan file handed to --target")
	mode := fs.String("mode", "", "queue (default) or fire")
	round := fs.Int("round", 0, "source round to watch (default: the source's current round)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: relevo edge add <source> --when report|diff|done|gate --then send --target <binding> --prompt <file> [--mode queue|fire] [--round N]\n" +
			"edge add needs the source binding name")
	}
	source := rest[0]
	if *when == "" {
		return fmt.Errorf("relevo edge add requires --when report|diff|done|gate")
	}
	if *then == "" {
		return fmt.Errorf("relevo edge add requires --then send")
	}
	if *target == "" {
		return fmt.Errorf("relevo edge add requires --target <binding>")
	}
	if *prompt == "" {
		return fmt.Errorf("relevo edge add requires --prompt <file>")
	}
	absPrompt, err := filepath.Abs(*prompt)
	if err != nil {
		return fmt.Errorf("resolve --prompt %s: %w", *prompt, err)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	added, err := relevo.AddEdge(context.Background(), rt, source, store.Edge{
		Round:  *round,
		When:   *when,
		Then:   *then,
		Target: *target,
		Prompt: absPrompt,
		Mode:   *mode,
	})
	if err != nil {
		return err
	}

	fmt.Printf("edge %s: when %s round %d %s -> send %s (%s)\n",
		added.ID, source, added.Round, added.When, added.Target, added.Mode)
	return nil
}

// cmdEdgeList prints one line per edge declared on source, with its
// Fired/Result outcome.
func cmdEdgeList(args []string) error {
	fs := flag.NewFlagSet("relevo edge list", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: relevo edge list <source>\n" +
			"edge list needs the source binding name")
	}
	source := rest[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	edges, err := relevo.ListEdges(rt, source)
	if err != nil {
		return err
	}
	for _, e := range edges {
		fmt.Printf("%s: when %s round %d %s -> send %s (%s) fired=%v result=%q\n",
			e.ID, source, e.Round, e.When, e.Target, e.Mode, e.Fired, e.Result)
	}
	return nil
}

// cmdEdgeRm drops one edge from source. Removing a fired edge is fine; an
// unknown id is refused by RemoveEdge.
func cmdEdgeRm(args []string) error {
	fs := flag.NewFlagSet("relevo edge rm", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("usage: relevo edge rm <source> <id>\n" +
			"edge rm needs the source binding name and the edge id")
	}
	source, id := rest[0], rest[1]

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if err := relevo.RemoveEdge(context.Background(), rt, source, id); err != nil {
		return err
	}
	fmt.Printf("removed edge %s from %s\n", id, source)
	return nil
}
