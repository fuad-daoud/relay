// Command relay automates plan and report handoff between a planner agent pane
// and a builder agent pane running under herdr.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
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
	case "bind":
		return cmdBind(args[1:])
	case "unbind":
		return cmdUnbind(args[1:])
	case "send":
		return cmdSend(args[1:])
	case "pull":
		return cmdPull(args[1:])
	case "answer":
		return cmdAnswer(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func newRuntime() (relay.Runtime, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return relay.Runtime{}, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return relay.Runtime{}, fmt.Errorf("resolve home directory: %w", err)
	}

	aliases, err := alias.LoadTable(filepath.Join(home, ".config", "relay", "aliases.json"))
	if err != nil {
		return relay.Runtime{}, err
	}

	return relay.Runtime{
		Herdr:   herdr.NewClient("herdr", 30*time.Second),
		Store:   store.New(root),
		Aliases: aliases,
		Now:     time.Now,
	}, nil
}

func cmdBind(args []string) error {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: sanitized cwd basename)")
	builderAlias := fs.String("builder", "", "builder alias, or a pane id to adopt")
	resume := fs.Bool("resume", false, "adopt an existing binding into this planner")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	opts := relay.BindOptions{
		Name:        *name,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		CWD:         cwd,
		Resume:      *resume,
	}
	// A value containing ':' is a herdr pane id, not an alias.
	if strings.Contains(*builderAlias, ":") {
		opts.BuilderPane = *builderAlias
	} else {
		opts.Alias = *builderAlias
	}

	b, err := relay.Bind(context.Background(), rt, opts)
	if err != nil {
		return err
	}

	fmt.Printf("bound %s: planner %s -> builder %s (%s), round %d\n",
		b.Name, b.Planner.PaneID, b.Builder.PaneID, b.BuilderAlias, b.Round)
	return nil
}

func cmdUnbind(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: relay unbind <name>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if err := relay.Unbind(context.Background(), rt, args[0]); err != nil {
		return err
	}

	fmt.Printf("unbound %s (panes left untouched)\n", args[0])
	return nil
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	file := fs.String("file", "", "path to the plan file to hand the builder")
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("relay send requires --file")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	target, err := resolveBinding(rt, *name)
	if err != nil {
		return err
	}

	round, err := relay.Send(context.Background(), rt, target, *file)
	if err != nil {
		return err
	}

	fmt.Printf("sent round %d to %s's builder\n", round, target)
	return nil
}

func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	target, err := resolveBinding(rt, *name)
	if err != nil {
		return err
	}

	payload, found, err := relay.Pull(context.Background(), rt, target)
	if err != nil {
		return err
	}
	if !found {
		fmt.Println("nothing pending")
		return nil
	}

	fmt.Println(payload)
	return nil
}

func cmdAnswer(args []string) error {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	keys := fs.String("keys", "", "logical key to send, e.g. enter or esc")
	text := fs.String("text", "", "literal text to send")
	choice := fs.Int("choice", 0, "numbered dialog option to pick")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	target, err := resolveBinding(rt, *name)
	if err != nil {
		return err
	}

	if err := relay.Answer(context.Background(), rt, target,
		relay.AnswerInput{Keys: *keys, Text: *text, Choice: *choice}); err != nil {
		return err
	}

	fmt.Printf("answered %s's builder\n", target)
	return nil
}

// resolveBinding falls back to the binding that owns the current directory, so
// the planner rarely has to name it.
func resolveBinding(rt relay.Runtime, name string) (string, error) {
	if name != "" {
		return name, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	b, found, err := rt.Store.FindByCWD(cwd)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("no binding for %s; run relay bind first", cwd)
	}

	return b.Name, nil
}
