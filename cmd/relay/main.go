// Command relay automates plan and report handoff between a planner agent pane
// and a builder agent pane running under herdr.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/ui"
)

// version is stamped at build time with -ldflags "-X main.version=v1.2.3".
// A `go install`ed binary carries no such stamp, so buildVersion falls back to
// the module version the toolchain records in the build info.
var version = ""

const usage = `relay automates the plan/report handoff between two AI coding agent
panes running under herdr: a planner hands work to a builder, and relay moves
the files between them.

Usage:
  relay <command> [flags]

Commands:
  bind      bind this planner pane to a builder over the current working tree
  fork      branch a new binding from an earlier round with its own worktree
  send      stage a plan file as the current round and prompt the builder
  pull      print the newest pending payload to stdout, without typing anywhere
  diff      print a round's captured patch to stdout
  answer    answer a builder that is blocked at a dialog
  status    one row per binding: round, state, live pane status, what is pending
  log       print a binding's append-only round log
  watch     status, redrawn on a timer
  ui        interactive reader: report, terminal, diff and log tabs
  done      mark a binding done; relaying stops
  unbind    forget a binding, deleting or archiving its directory
  gc        clear every binding the planner marked DONE
  daemon    run the long-running reconciler
  doctor    preflight check: herdr, daemon, harness binaries, integrations, roles
  agent     print embedded agent role definitions (e.g. relay agent print --kind claude)
  help      print this message
  version   print the relay version

Run "relay <command> -h" for that command's flags.

relay drives herdr, which must be on PATH: https://github.com/herdrdev/herdr
State lives in $XDG_STATE_HOME/relay (default ~/.local/state/relay).
`

type exitCodeErr struct {
	code int
}

func (e exitCodeErr) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

// errHelpShown reports that help was printed on request, so main exits 0
// without adding an error line. errUsagePrinted is its failure twin: usage is
// already on stderr and main should exit 1 silently.
var (
	errHelpShown    = errors.New("help shown")
	errUsagePrinted = errors.New("usage printed")
)

func main() {
	err := run(os.Args[1:])
	var ec exitCodeErr
	switch {
	case err == nil, errors.Is(err, errHelpShown):
		return
	case errors.As(err, &ec):
		os.Exit(ec.code)
	case errors.Is(err, errUsagePrinted):
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "relay: %v\n", err)
		os.Exit(1)
	}
}

// buildVersion prefers the ldflags stamp a release build carries, then the
// module version `go install` records, and admits to being an untagged build
// rather than inventing a number.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

// parseFlags parses one subcommand's flags. It turns `-h` into a clean exit:
// the flag package has already printed the usage, and asking for help is a
// request that succeeded, not a command that failed.
func parseFlags(fs *flag.FlagSet, args []string) error {
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return errHelpShown
	}
	return err
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errUsagePrinted
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "-v", "--version":
		fmt.Printf("relay %s\n", buildVersion())
		return nil
	case "bind":
		return cmdBind(args[1:])
	case "fork":
		return cmdFork(args[1:])
	case "unbind":
		return cmdUnbind(args[1:])
	case "gc":
		return cmdGC(args[1:])
	case "send":
		return cmdSend(args[1:])
	case "pull":
		return cmdPull(args[1:])
	case "diff":
		return cmdDiff(args[1:])
	case "answer":
		return cmdAnswer(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "log":
		return cmdLog(args[1:])
	case "watch":
		return cmdWatch(args[1:])
	case "ui":
		return cmdUI(args[1:])
	case "done":
		return cmdDone(args[1:])
	case "daemon":
		return cmdDaemon(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "agent":
		return cmdAgent(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q; run \"relay help\" for the command list", args[0])
	}
}

func resolveHooksConfig() (hooks.Config, error) {
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return hooks.Config{}, fmt.Errorf("resolve user home directory: %w", err)
		}
		configDir = filepath.Join(home, ".config")
	}

	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return hooks.Config{}, fmt.Errorf("resolve user home directory: %w", err)
		}
		stateDir = filepath.Join(home, ".local", "state")
	}

	return hooks.Config{
		HooksDir: filepath.Join(configDir, "relay", "hooks"),
		LogPath:  filepath.Join(stateDir, "relay", "hooks.log"),
	}, nil
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

	hooksCfg, err := resolveHooksConfig()
	if err != nil {
		return relay.Runtime{}, err
	}
	dispatcher := hooks.NewLocalDispatcher(hooksCfg, hooks.NewOSExecutor(hooksCfg.LogPath))

	return relay.Runtime{
		Herdr:   herdr.NewClient("herdr", 30*time.Second),
		Git:     git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes),
		Store:   store.New(root),
		Aliases: aliases,
		Now:     time.Now,
		Hooks:   dispatcher,
	}, nil
}

func cmdBind(args []string) error {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: sanitized cwd basename)")
	builderAlias := fs.String("builder", "", "builder alias, or a pane id to adopt")
	resume := fs.Bool("resume", false, "adopt an existing binding into this planner")
	newTab := fs.Bool("tab", false, "open the builder in its own tab instead of splitting this pane")
	timeout := fs.Duration("timeout", 0, "round budget before relay flags the binding (default 24h)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// --resume takes the name from --name, so a positional one is dropped on
	// the floor and the binding lookup then fails on the empty name.
	if *resume && *name == "" {
		return fmt.Errorf("relay bind --resume needs --name NAME (a positional name is ignored)")
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
		Name:         *name,
		PlannerPane:  os.Getenv("HERDR_PANE_ID"),
		CWD:          cwd,
		Resume:       *resume,
		NewTab:       *newTab,
		WorkspaceID:  os.Getenv("HERDR_WORKSPACE_ID"),
		RoundTimeout: *timeout,
	}
	// A value containing ':' is a herdr pane id, not an alias.
	if strings.Contains(*builderAlias, ":") {
		opts.BuilderPane = *builderAlias
	} else {
		opts.Alias = *builderAlias
	}

	adopted := *resume || opts.BuilderPane != ""
	kind := ""
	if adopted {
		if *resume && *name != "" {
			if existing, err := rt.Store.Load(*name); err == nil {
				kind = existing.Builder.Kind
			}
		}
		if kind == "" && opts.BuilderPane != "" {
			if agents, err := rt.Herdr.ListAgents(context.Background()); err == nil {
				if a, ok := relay.FindAgent(agents, store.Endpoint{PaneID: opts.BuilderPane}); ok {
					kind = a.Kind
				}
			}
		}
	} else if opts.Alias != "" {
		if spec, err := rt.Aliases.Lookup(opts.Alias); err == nil {
			kind = spec.Kind
		}
	}

	// Preflight is advisory only: it never blocks the bind, and any probe
	// failure is dropped rather than printed. See bindPreflight.
	if hc, ok := rt.Herdr.(doctor.HerdrClient); ok && kind != "" {
		env := doctor.NewEnv(hc, rt.Store)
		for _, line := range bindPreflight(context.Background(), env, kind, adopted) {
			fmt.Fprintln(os.Stderr, line)
		}
	}

	b, err := relay.Bind(context.Background(), rt, opts)
	if err != nil {
		return err
	}

	if *resume && *builderAlias != "" {
		builderDesc := b.Builder.PaneID
		if b.BuilderAlias != "" {
			builderDesc = fmt.Sprintf("%s (%s)", b.Builder.PaneID, b.BuilderAlias)
		}
		fmt.Printf("rebound %s: builder %s, still on round %d\n"+
			"hand it the round with:\n"+
			"  relay send --name %s --file %s\n",
			b.Name, builderDesc, b.Round, b.Name, rt.Store.PlanPath(b.Name, b.Round))
		return nil
	}

	fmt.Printf("bound %s: planner %s -> builder %s (%s), round %d\n",
		b.Name, b.Planner.PaneID, b.Builder.PaneID, b.BuilderAlias, b.Round)
	return nil
}

func cmdFork(args []string) error {
	fs := flag.NewFlagSet("fork", flag.ContinueOnError)
	name := fs.String("name", "", "source binding to fork from")
	round := fs.Int("round", 0, "source round to copy history through")
	newName := fs.String("new-name", "", "name for the new binding")
	builderAlias := fs.String("builder", "", "builder alias, or a pane id to adopt (default: inherits source)")
	newTab := fs.Bool("tab", false, "open the builder in its own tab instead of splitting this pane")
	cwd := fs.String("cwd", "", "bind the fork to an existing directory instead of creating a git worktree")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	source, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay fork <source> --round N --new-name NAME [--builder ALIAS] [--tab] [--cwd DIR]%s\n"+
			"fork branches a new binding from an earlier round; it needs the source binding name", bindingHint("fork"))
	}

	if *round < 1 {
		return fmt.Errorf("relay fork requires --round N (where N >= 1)")
	}
	if *newName == "" {
		return fmt.Errorf("relay fork requires --new-name NAME")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	opts := relay.ForkOptions{
		Source:      source,
		Round:       *round,
		NewName:     *newName,
		Alias:       *builderAlias,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		NewTab:      *newTab,
		WorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"),
		CWD:         *cwd,
	}

	res, err := relay.Fork(context.Background(), rt, opts)
	if err != nil {
		return err
	}

	if res.Worktree != "" {
		fmt.Printf("forked %s to %s (round %d) at %s on branch %s\n",
			source, res.Binding.Name, res.Binding.Round, res.Worktree, res.Branch)
	} else {
		fmt.Printf("forked %s to %s (round %d) at %s\n",
			source, res.Binding.Name, res.Binding.Round, res.Binding.CWD)
	}

	return nil
}

func cmdUnbind(args []string) error {
	fs := flag.NewFlagSet("unbind", flag.ContinueOnError)
	name := fs.String("name", "", "binding to unbind")
	archive := fs.Bool("archive", false, "move the binding aside instead of deleting it, keeping its round log")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay unbind <name> [--archive]  (or --name <name>)%s\n"+
			"unbind removes a binding; it will not guess which one you meant", bindingHint("unbind"))
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	res, err := relay.Unbind(context.Background(), rt, target, *archive)
	if err != nil {
		return err
	}

	if res.ArchivedTo != "" {
		fmt.Printf("archived %s to %s (panes left untouched)\n", target, res.ArchivedTo)
	} else {
		fmt.Printf("unbound %s (panes left untouched)\n", target)
	}

	if res.WorktreeRemoved != "" {
		fmt.Printf("removed worktree %s\n", res.WorktreeRemoved)
	} else if res.WorktreeKept != "" {
		fmt.Printf("kept worktree %s (%s)\n  remove by hand: git -C %s worktree remove %s\n",
			res.WorktreeKept, res.KeptReason, res.WorktreeKept, res.WorktreeKept)
	}

	return nil
}

func cmdGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	archive := fs.Bool("archive", false, "archive each finished binding instead of deleting it")
	dryRun := fs.Bool("dry-run", false, "list what would be cleared, change nothing")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	done, err := relay.GC(context.Background(), rt, relay.GCOptions{
		Archive: *archive,
		DryRun:  *dryRun,
	})
	if err != nil {
		return err
	}

	if len(done) == 0 {
		fmt.Println("no finished bindings to clear")
		return nil
	}

	for _, r := range done {
		switch {
		case *dryRun:
			wtMsg := ""
			if r.WorktreeRemoved != "" {
				wtMsg = fmt.Sprintf(" (worktree %s would be removed)", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				wtMsg = fmt.Sprintf(" (worktree %s kept: %s)", r.WorktreeKept, r.KeptReason)
			}
			fmt.Printf("would clear %-10s %s (%d rounds)%s\n", r.Name, r.CWD, r.Rounds, wtMsg)
		case r.ArchivedTo != "":
			fmt.Printf("archived    %-10s -> %s\n", r.Name, r.ArchivedTo)
			if r.WorktreeRemoved != "" {
				fmt.Printf("            removed worktree %s\n", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				fmt.Printf("            kept worktree %s (%s)\n              remove by hand: git -C %s worktree remove %s\n",
					r.WorktreeKept, r.KeptReason, r.WorktreeKept, r.WorktreeKept)
			}
		default:
			fmt.Printf("deleted     %-10s %s (%d rounds)\n", r.Name, r.CWD, r.Rounds)
			if r.WorktreeRemoved != "" {
				fmt.Printf("            removed worktree %s\n", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				fmt.Printf("            kept worktree %s (%s)\n              remove by hand: git -C %s worktree remove %s\n",
					r.WorktreeKept, r.KeptReason, r.WorktreeKept, r.WorktreeKept)
			}
		}
	}

	if *dryRun {
		fmt.Printf("\n%d binding(s) would be cleared; re-run without --dry-run\n", len(done))
	}

	return nil
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	file := fs.String("file", "", "path to the plan file to hand the builder")
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	if err := parseFlags(fs, args); err != nil {
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
	if err := parseFlags(fs, args); err != nil {
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

func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	round := fs.Int("round", 0, "round to diff (default: newest completed round)")
	stat := fs.Bool("stat", false, "print summary line instead of patch body")
	if err := parseFlags(fs, args); err != nil {
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

	b, err := rt.Store.Load(target)
	if err != nil {
		return err
	}

	targetRound := *round
	if targetRound == 0 {
		targetRound = b.Round - 1
	}
	if targetRound < 1 {
		return fmt.Errorf("binding %q has no completed round yet", target)
	}

	if *stat {
		entries, err := rt.Store.ReadLog(target)
		if err != nil {
			return err
		}
		var found *store.LogEntry
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Round == targetRound && entries[i].Kind == store.KindDiff {
				found = &entries[i]
				break
			}
		}
		if found == nil || found.Note == "" {
			return fmt.Errorf("no diff recorded for round %d of %q", targetRound, target)
		}
		fmt.Println(found.Note)
		return nil
	}

	patch, ok, err := relay.ReadDiff(rt, target, targetRound)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no diff recorded for round %d of %q", targetRound, target)
	}

	_, err = os.Stdout.Write(patch)
	return err
}

func cmdAnswer(args []string) error {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	keys := fs.String("keys", "", "logical key to send, e.g. enter or esc")
	text := fs.String("text", "", "literal text to send")
	choice := fs.Int("choice", 0, "numbered dialog option to pick")
	if err := parseFlags(fs, args); err != nil {
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

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	rep, err := relay.Status(context.Background(), rt)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	fmt.Print(relay.RenderStatus(rep))
	return nil
}

func cmdLog(args []string) error {
	// A flag set with no flags, purely so `relay log -h` behaves like every
	// other subcommand instead of being read as a binding name.
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: relay log <name>") }
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) != 1 {
		return fmt.Errorf("usage: relay log <name>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	entries, err := rt.Store.ReadLog(args[0])
	if err != nil {
		return err
	}

	for _, e := range entries {
		fmt.Printf("%s  round %-3d %-10s %-9s %s %s\n",
			e.TS.Local().Format("2006-01-02 15:04:05"), e.Round, e.Direction, e.Kind, e.Path, e.Note)
	}
	return nil
}

func cmdWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	interval := fs.Duration("interval", 2*time.Second, "refresh interval")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		rep, err := relay.Status(ctx, rt)
		if err != nil {
			if ctx.Err() != nil {
				return nil // Ctrl-C landed mid-query; that is a clean exit
			}
			return err
		}
		fmt.Print("\033[H\033[2J", relay.RenderStatus(rep))

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	interval := fs.Duration("interval", 0, "refresh interval")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return ui.Run(ctx, rt, ui.Options{
		Interval: *interval,
	})
}

func cmdDone(args []string) error {
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	name := fs.String("name", "", "binding to mark done")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay done <name>  (or --name <name>)%s\n"+
			"done stops relaying for a binding; it will not guess which one you meant",
			bindingHint("done"))
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if err := relay.Done(context.Background(), rt, target); err != nil {
		return err
	}

	fmt.Printf("%s marked done; relaying stopped\n", target)
	return nil
}

// explicitBinding takes the binding from --name or a single positional, and
// never from the current directory. done is destructive and not undoable
// except through `relay bind --resume`, so a bare invocation must fail rather
// than guess -- guessing has already ended one live loop by accident.
func explicitBinding(nameFlag string, positional []string) (string, bool) {
	switch {
	case nameFlag != "" && len(positional) == 0:
		return nameFlag, true
	case nameFlag == "" && len(positional) == 1:
		return positional[0], true
	default:
		return "", false
	}
}

// bindingHint names the binding for the current directory, if there is one, so
// a usage line can say what the caller probably meant. verb is the command
// being refused, so the suggestion is the one they actually wanted.
func bindingHint(verb string) string {
	rt, err := newRuntime()
	if err != nil {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	b, found, err := rt.Store.FindByCWD(cwd)
	if err != nil || !found {
		return ""
	}

	return fmt.Sprintf("\nthis directory is bound as %q, so you probably want: relay %s %s", b.Name, verb, b.Name)
}

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	interval := fs.Duration("interval", 2*time.Second, "poll interval")
	check := fs.Bool("check", false, "exit 0 if a daemon is running, 1 if not; print nothing")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// --check is the plugin startup hook's probe. It prints nothing on either
	// path: the exit status is the whole answer, and a hook that printed would
	// only fill herdr's plugin log with noise on every server start.
	if *check {
		running, err := rt.Store.DaemonRunning()
		if err != nil {
			return err
		}
		if !running {
			os.Exit(1)
		}
		return nil
	}

	lock, err := rt.Store.AcquireDaemonLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	hooksCfg, err := resolveHooksConfig()
	if err != nil {
		return err
	}
	rt.Hooks = hooks.NewLocalDispatcher(hooksCfg, hooks.NewOSExecutor(hooksCfg.LogPath))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("relay daemon starting", "interval", *interval)
	return relay.NewDaemon(rt, *interval).Run(ctx)
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
