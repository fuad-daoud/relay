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

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/history"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/pick"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/proc"
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
  add       attach an additional builder to this planner, on its own worktree
  fork      branch a new binding from an earlier round with its own worktree
  send      stage a plan file as the current round and prompt the builder
  ask       spawn a one-shot consult and record it on the binding
  pull      print the oldest pending payload to stdout, without typing anywhere
  diff      print a round's captured patch to stdout
  answer    answer a builder that is blocked at a dialog (--pick to choose it on screen)
  status    one row per binding: round, state, live pane status, what is pending [--all]
  log       print a binding's append-only round log
  watch     status, redrawn on a timer [--all]
  ui        interactive reader: report, terminal, diff and log tabs
  done      mark a binding done; relaying stops (--pick to choose it on screen)
  unbind    forget a binding, deleting or archiving its directory (--pick to choose it on screen)
  gc        clear every binding the planner marked DONE
  reap      close the panes of terminal consults and drop their records
  daemon    run the long-running reconciler
  doctor    preflight check: herdr, daemon, harness binaries, integrations, roles
  candidates   list the configured harness/provider/model candidates
  policy       show, per role, which candidate relay would pick right now and why
  unavailable  record a provider rate limit: relay unavailable <token> [--for D] [--reason S]
  available    clear a recorded rate limit: relay available <provider|token>
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
// the flag package has already printed usage, so the caller just returns.
//
// It parses iteratively rather than once, because flag.Parse stops at the first
// non-flag argument -- which meant `relay fork webshop --round 2` silently
// dropped --round, the exact form the README documents (#48). Each pass takes
// one leftover word as a positional and re-parses the remainder, so flags are
// found wherever they appear. Letting Parse do the work is what keeps this
// correct without knowing any flag's arity: Parse has already consumed a
// flag's value before the leftovers are looked at, so `--round 2` never leaves
// a stray "2" behind.
//
// There is no `--` passthrough anywhere in this CLI, so nothing here needs to
// stop early and treat a tail as literal.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var positional []string

	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return errHelpShown
			}
			return err
		}

		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	// Re-parse the collected positionals so fs.Args() reports them, leaving
	// every caller's `fs.Args()` working exactly as before. Parse stops at the
	// first non-flag argument and every element here is one, so this consumes
	// nothing and simply reinstates the list. Flag values already set by the
	// passes above survive: Parse does not reset them.
	return fs.Parse(positional)
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
	case "add":
		return cmdAdd(args[1:])
	case "fork":
		return cmdFork(args[1:])
	case "unbind":
		return cmdUnbind(args[1:])
	case "gc":
		return cmdGC(args[1:])
	case "reap":
		return cmdReap(args[1:])
	case "send":
		return cmdSend(args[1:])
	case "ask":
		return cmdAsk(args[1:])
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
	case "candidates":
		return cmdCandidates(args[1:])
	case "policy":
		return cmdPolicy(args[1:])
	case "unavailable":
		return cmdUnavailable(args[1:])
	case "available":
		return cmdAvailable(args[1:])
	case "agent":
		return cmdAgent(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q; run \"relay help\" for the command list", args[0])
	}
}

// userConfigRoot resolves $XDG_CONFIG_HOME, falling back to ~/.config.
func userConfigRoot() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}

	return filepath.Join(home, ".config"), nil
}

func resolveHooksConfig() (hooks.Config, error) {
	configDir, err := userConfigRoot()
	if err != nil {
		return hooks.Config{}, err
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

// newRuntime constructs the production runtime; aliases.json is never read (#80).
func newRuntime() (relay.Runtime, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return relay.Runtime{}, err
	}

	configDir, err := userConfigRoot()
	if err != nil {
		return relay.Runtime{}, err
	}

	candidates, err := candidate.Load(filepath.Join(configDir, "relay", "candidates.json"))
	if err != nil {
		return relay.Runtime{}, err
	}

	pol, err := policy.Load(filepath.Join(configDir, "relay", "policy.json"))
	if err != nil {
		return relay.Runtime{}, err
	}

	hooksCfg, err := resolveHooksConfig()
	if err != nil {
		return relay.Runtime{}, err
	}
	dispatcher := hooks.NewLocalDispatcher(hooksCfg, hooks.NewOSExecutor(hooksCfg.LogPath))

	st := store.New(root)

	return relay.Runtime{
		Herdr:       herdr.NewClient("herdr", 30*time.Second),
		Git:         git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes),
		Runner:      proc.New(),
		Store:       st,
		Candidates:  candidates,
		LedgerPath:  st.LedgerPath(),
		HistoryPath: st.HistoryPath(),
		Policy:      pol,
		Now:         time.Now,
		Hooks:       dispatcher,
	}, nil
}

// noteConsultRolesTooLong prints, after a successful bind/add/fork, the one
// advisory line naming configured consult roles the binding's name is too long
// for -- so a later `relay ask` failing on the derived name is not a surprise
// a day later. It is a note, not an error: a binding that can build is still
// useful, and refusing would let the alias table dictate binding names.
func noteConsultRolesTooLong(name string) {
	roles := relay.ConsultRolesTooLong(name)
	if len(roles) == 0 {
		return
	}
	// The tightest limit is set by the longest role: relay ask needs the
	// binding name at most herdr.MaxAgentNameLen - 10 - len(role) characters.
	longest := roles[0]
	for _, r := range roles[1:] {
		if len(r) > len(longest) {
			longest = r
		}
	}
	fmt.Printf("note: %s is too long for the %s consult role(s); relay ask needs a binding name of at most %d characters for %s\n",
		name, strings.Join(roles, ", "), herdr.MaxAgentNameLen-10-len(longest), longest)
}

// notePick prints why relay chose the candidate it spawned. Silent for
// an explicit token (the planner already knows) and for adoption
// (nothing was chosen); the gated note, if any, is printed separately.
func notePick(role string, res relay.Resolution) {
	if res.How == "" || res.How == relay.HowExplicit {
		return
	}
	fmt.Fprintln(os.Stderr, relay.ExplainResolution(role, res))
}

// isPaneID tells a herdr pane id apart from a candidate token on the same
// --builder flag: a pane id has a ':' and never a '/', a candidate token
// always has a '/'. A model name may contain ':', so ':' alone is not enough.
func isPaneID(s string) bool {
	return strings.Contains(s, ":") && !strings.Contains(s, "/")
}

// parseFor turns --for into an absolute expiry. Empty means "until cleared"
// (a zero time); anything else must be a positive Go duration -- relay does
// not know a provider's reset schedule, so it never invents one (spec §1).
func parseFor(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--for %q: %w", s, err)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("--for must be a positive duration, got %q", s)
	}
	return now.Add(d), nil
}

func cmdCandidates(args []string) error {
	fs := flag.NewFlagSet("candidates", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	fmt.Print(relay.FormatCandidates(rt.Candidates, relay.Gates(rt)))
	return nil
}

// loadHistory reads the availability history for display, treating an
// unreadable file as empty after one stderr line -- the same rule Gates
// applies to the ledger.
func loadHistory(rt relay.Runtime) history.History {
	h, err := history.Load(rt.HistoryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: could not read history: %v\n", err)
		return history.History{}
	}
	return h.Prune(rt.Now())
}

func cmdPolicy(args []string) error {
	fs := flag.NewFlagSet("policy", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	fmt.Print(relay.FormatPolicy(rt.Candidates, rt.Policy, relay.Gates(rt), loadHistory(rt), rt.Now(), time.Local))
	return nil
}

func cmdUnavailable(args []string) error {
	fs := flag.NewFlagSet("unavailable", flag.ContinueOnError)
	forFlag := fs.String("for", "", "how long to gate the provider (Go duration, e.g. 2h); omit to leave it gated until `relay available`")
	reason := fs.String("reason", "", "why, for the record")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 1 {
		return fmt.Errorf("usage: relay unavailable <harness/provider/model> [--for D] [--reason S]")
	}
	token := positional[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	until, err := parseFor(*forFlag, rt.Now())
	if err != nil {
		return err
	}

	provider, err := relay.Unavailable(rt, token, until, *reason)
	if err != nil {
		return err
	}

	count := 0
	for _, ref := range rt.Candidates.Refs() {
		parsed, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		if parsed.Provider == provider {
			count++
		}
	}

	fmt.Printf("gated %s (%d candidates) %s\n", provider, count, relay.GateUntilText(until))

	if bs, err := rt.Store.List(); err == nil {
		if names := relay.BindingsOnProvider(bs, provider); len(names) > 0 {
			fmt.Printf("the daemon will switch: %s\n", strings.Join(names, ", "))
		}
	}

	return nil
}

func cmdAvailable(args []string) error {
	fs := flag.NewFlagSet("available", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 1 {
		return fmt.Errorf("usage: relay available <provider|harness/provider/model>")
	}
	subject := positional[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	provider, removed, err := relay.Available(rt, subject)
	if err != nil {
		return err
	}

	if removed == 0 {
		fmt.Printf("nothing was gating %s\n", provider)
		return nil
	}

	fmt.Printf("cleared %s (%d entries)\n", provider, removed)
	return nil
}

func cmdBind(args []string) error {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: sanitized cwd basename)")
	builderAlias := fs.String("builder", "", "candidate harness/provider/model to spawn, or a pane id to adopt; omit to take the first ungated candidate in policy.json order[builder]")
	resume := fs.Bool("resume", false, "adopt an existing binding into this planner")
	assumeDead := fs.Bool("assume-dead", false,
		"confirm a builder relay cannot verify is gone really is gone")
	rebind := fs.Bool("rebind", false,
		"with --resume: replace a gone builder, picking it by policy.json order and the ledger (like bind with --builder omitted)")
	timeout := fs.Duration("timeout", 0, "round budget before relay flags the binding (default 24h)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// --resume takes the name from --name, so a positional one is dropped on
	// the floor and the binding lookup then fails on the empty name.
	if *resume && *name == "" {
		return fmt.Errorf("relay bind --resume needs --name NAME (a positional name is ignored)")
	}
	if *rebind && !*resume {
		return fmt.Errorf("relay bind --rebind only applies with --resume (it replaces a gone builder on an existing binding)")
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
		Rebind:       *rebind,
		AssumeDead:   *assumeDead,
		WorkspaceID:  os.Getenv("HERDR_WORKSPACE_ID"),
		RoundTimeout: *timeout,
	}
	if isPaneID(*builderAlias) {
		opts.BuilderPane = *builderAlias
	} else {
		opts.Candidate = *builderAlias
	}

	adopted := *resume || opts.BuilderPane != ""
	kind := ""
	switch {
	case *rebind && opts.BuilderPane == "":
		kind = relay.CandidateKind(rt, opts.Candidate)
	case adopted:
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
	default:
		kind = relay.CandidateKind(rt, opts.Candidate)
	}

	// Preflight is advisory only: it never blocks the bind, and any probe
	// failure is dropped rather than printed. See bindPreflight.
	if hc, ok := rt.Herdr.(doctor.HerdrClient); ok && kind != "" {
		env := doctor.NewEnv(hc, rt.Store)
		for _, line := range bindPreflight(context.Background(), env, kind, adopted) {
			fmt.Fprintln(os.Stderr, line)
		}
	}

	b, res, err := relay.BindResolved(context.Background(), rt, opts)
	if err != nil {
		return err
	}

	if *resume && (*builderAlias != "" || *rebind) {
		builderDesc := b.Builder.PaneID
		if b.BuilderCandidate != "" {
			builderDesc = fmt.Sprintf("%s (%s)", b.Builder.PaneID, b.BuilderCandidate)
		}
		fmt.Printf("rebound %s: builder %s, still on round %d\n"+
			"hand it the round with:\n"+
			"  relay send --name %s --file %s\n",
			b.Name, builderDesc, b.Round, b.Name, rt.Store.PlanPath(b.Name, b.Round))
		notePick("builder", res)
		return nil
	}

	fmt.Printf("bound %s: planner %s -> builder %s (%s), round %d\n",
		b.Name, b.Planner.PaneID, b.Builder.PaneID, b.BuilderCandidate, b.Round)
	if n := relay.GatedNote(rt, b.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick("builder", res)
	// Spawn path only: an adopted pane or resumed binding has no fresh name
	// relay chose, so the note would warn about a name the human did not pick
	// here.
	if !adopted {
		noteConsultRolesTooLong(b.Name)
	}
	return nil
}

func cmdFork(args []string) error {
	fs := flag.NewFlagSet("fork", flag.ContinueOnError)
	name := fs.String("name", "", "source binding to fork from")
	round := fs.Int("round", 0, "source round to copy history through")
	newName := fs.String("new-name", "", "name for the new binding")
	builderAlias := fs.String("builder", "", "candidate harness/provider/model to spawn (default: inherits source; else the first ungated in policy.json order[builder])")
	cwd := fs.String("cwd", "", "bind the fork to an existing directory instead of creating a git worktree")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	source, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay fork <source> --round N --new-name NAME [--builder CANDIDATE] [--cwd DIR]%s\n"+
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
		Candidate:   *builderAlias,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
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
	if n := relay.GatedNote(rt, res.Binding.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick("builder", res.Resolution)
	noteConsultRolesTooLong(res.Binding.Name)

	return nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "name for the new binding")
	builderAlias := fs.String("builder", "", "candidate harness/provider/model to spawn; omit to take the first ungated candidate in policy.json order[builder]")
	cwd := fs.String("cwd", "", "bind the peer to an existing directory instead of creating a git worktree")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *name == "" {
		return fmt.Errorf("relay add requires --name NAME")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	repo, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	res, err := relay.Add(context.Background(), rt, relay.AddOptions{
		Name:        *name,
		Candidate:   *builderAlias,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		Repo:        repo,
		WorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"),
		CWD:         *cwd,
	})
	if err != nil {
		return err
	}

	fmt.Printf("added %s: builder %s in pane %s\n",
		res.Binding.Name, res.Binding.BuilderCandidate, res.Binding.Builder.PaneID)
	if n := relay.GatedNote(rt, res.Binding.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick("builder", res.Resolution)
	if res.Worktree != "" {
		fmt.Printf("  worktree %s on %s (from %s)\n", res.Worktree, res.Branch, res.Base)
	} else {
		fmt.Printf("  tree %s\n", res.Binding.CWD)
	}
	fmt.Printf("  relay send --name %s --file <plan.md>\n", res.Binding.Name)
	noteConsultRolesTooLong(res.Binding.Name)

	return nil
}

func cmdUnbind(args []string) error {
	fs := flag.NewFlagSet("unbind", flag.ContinueOnError)
	name := fs.String("name", "", "binding to unbind")
	archive := fs.Bool("archive", false, "move the binding aside instead of deleting it, keeping its round log")
	pickFlag := fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		return runPick(pick.Options{Verb: pick.VerbUnbind, Archive: *archive})
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay unbind <name> | --pick [--archive]  (or --name <name>)%s\n"+
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

	fmt.Println(relay.UnbindText(target, res))

	return nil
}

func cmdGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	delete := fs.Bool("delete", false, "remove each finished binding's directory instead of archiving it")
	// --archive is kept as an accepted, ignored flag so scripts written against the old default do not break; archiving is now the default (spec §7.3).
	archive := fs.Bool("archive", false, "no-op; archiving is now the default")
	dryRun := fs.Bool("dry-run", false, "list what would be cleared, change nothing")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relay gc [--dry-run] [--delete]")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	_ = archive

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	done, err := relay.GC(context.Background(), rt, relay.GCOptions{
		Delete: *delete,
		DryRun: *dryRun,
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
			} else if r.WorktreeGone != "" {
				wtMsg = fmt.Sprintf(" (worktree %s already gone)", r.WorktreeGone)
			}
			fmt.Printf("would clear %-10s %s (%d rounds)%s\n", r.Name, r.CWD, r.Rounds, wtMsg)
		case r.ArchivedTo != "":
			fmt.Printf("archived    %-10s -> %s\n", r.Name, r.ArchivedTo)
			if r.WorktreeRemoved != "" {
				fmt.Printf("            removed worktree %s\n", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				fmt.Printf("            kept worktree %s (%s)\n              remove by hand: git -C %s worktree remove %s\n",
					r.WorktreeKept, r.KeptReason, r.WorktreeKept, r.WorktreeKept)
			} else if r.WorktreeGone != "" {
				fmt.Printf("            worktree %s was already gone\n", r.WorktreeGone)
			}
		default:
			fmt.Printf("deleted     %-10s %s (%d rounds)\n", r.Name, r.CWD, r.Rounds)
			if r.WorktreeRemoved != "" {
				fmt.Printf("            removed worktree %s\n", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				fmt.Printf("            kept worktree %s (%s)\n              remove by hand: git -C %s worktree remove %s\n",
					r.WorktreeKept, r.KeptReason, r.WorktreeKept, r.WorktreeKept)
			} else if r.WorktreeGone != "" {
				fmt.Printf("            worktree %s was already gone\n", r.WorktreeGone)
			}
		}
	}

	if *dryRun {
		fmt.Printf("\n%d binding(s) would be cleared; re-run without --dry-run\n", len(done))
	}

	return nil
}

// workspaceOrEnv resolves the workspace a new consult tab opens in: the
// explicit --workspace flag wins, otherwise the planner's own workspace, the
// same environment variable bind, fork and add already read. Empty is a valid
// answer -- CreateTab omits --workspace entirely for it.
func workspaceOrEnv(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("HERDR_WORKSPACE_ID")
}

// reapFlags is the parsed command line of `relay reap`.
type reapFlags struct {
	all    bool
	name   string
	dryRun bool
}

// parseReapFlags exists so the wiring between what the user typed and what
// Reap is asked to do is testable on its own: the previous shape read fine and
// silently dropped two flags, and no test could reach it.
func parseReapFlags(args []string) (reapFlags, []string, error) {
	fs := flag.NewFlagSet("reap", flag.ContinueOnError)
	all := fs.Bool("all", false, "reap every binding's terminal consults, not just the named one")
	nameFlag := fs.String("name", "", "binding name (default: the binding for this cwd)")
	dryRun := fs.Bool("dry-run", false, "list what would be closed, change nothing")
	if err := parseFlags(fs, args); err != nil {
		return reapFlags{}, nil, err
	}
	// Dereference only AFTER parseFlags: flag writes through these pointers,
	// so reading them any earlier captures the defaults and silently discards
	// what the user typed.
	return reapFlags{all: *all, name: *nameFlag, dryRun: *dryRun}, fs.Args(), nil
}

func cmdReap(args []string) error {
	opts, positional, err := parseReapFlags(args)
	if err != nil {
		return err
	}
	all, dryRun := opts.all, opts.dryRun

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// --all needs no name, and resolving one from the cwd would only fail in
	// an unbound directory, where the sweep is most wanted.
	var name string
	if !all {
		name, err = resolveBinding(rt, opts.name, positional)
		if err != nil {
			return err
		}
	}

	results, err := relay.Reap(context.Background(), rt, relay.ReapOptions{
		Name:   name,
		All:    all,
		DryRun: dryRun,
	})
	if err != nil {
		return err
	}

	for _, r := range results {
		for _, c := range r.Closed {
			verb := "closed"
			if dryRun {
				verb = "would close"
			}
			fmt.Printf("%s %s consult %s (pane %s) on %s\n", verb, c.Role, c.ID, c.Endpoint.PaneID, r.Binding)
		}
		for _, c := range r.Dropped {
			verb := "dropped"
			if dryRun {
				verb = "would drop"
			}
			fmt.Printf("%s %s consult %s on %s (no pane was spawned)\n", verb, c.Role, c.ID, r.Binding)
		}
		for _, c := range r.Failed {
			fmt.Printf("could not close pane %s for consult %s on %s; record kept\n", c.Endpoint.PaneID, c.ID, r.Binding)
		}
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

	target, err := resolveBinding(rt, *name, fs.Args())
	if err != nil {
		return err
	}

	res, err := relay.Send(context.Background(), rt, target, *file)
	if err != nil {
		return err
	}

	if res.Drift != "" {
		fmt.Println(res.Drift)
	}
	fmt.Printf("sent round %d to %s's builder\n", res.Round, target)
	return nil
}

func cmdAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	role := fs.String("role", "", "consult role: reviewer, researcher")
	cand := fs.String("candidate", "", "candidate harness/provider/model; omit to take the first ungated in policy.json order[<role>]")
	file := fs.String("file", "", "file containing the question")
	nameFlag := fs.String("name", "", "binding name")
	workspace := fs.String("workspace", "", "workspace for the consult's tab (default: $HERDR_WORKSPACE_ID)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *role == "" {
		return fmt.Errorf("relay ask needs --role ROLE")
	}
	if *file == "" {
		return fmt.Errorf("relay ask needs --file PATH")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	name, err := resolveBinding(rt, *nameFlag, fs.Args())
	if err != nil {
		return err
	}

	res, err := relay.Ask(context.Background(), rt, relay.AskOptions{
		Role:        *role,
		Candidate:   *cand,
		File:        *file,
		Name:        name,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		WorkspaceID: workspaceOrEnv(*workspace),
	})
	if err != nil {
		return err
	}

	fmt.Printf("asked %s consult %s on %s (pane %s)\nfindings will appear at: %s\n",
		res.Consult.Role, res.Consult.ID, res.Binding, res.Consult.Endpoint.PaneID, res.Consult.FindingsPath)
	if n := relay.GatedNote(rt, res.Candidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(*role, res.Resolution)
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
	target, err := resolveBinding(rt, *name, fs.Args())
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
	round := fs.Int("round", 0, "round to diff (default: newest completed round, or the open round with --drift)")
	stat := fs.Bool("stat", false, "print summary line instead of patch body")
	drift := fs.Bool("drift", false, "show between-rounds drift instead of round diff (default: the open round)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	target, err := resolveBinding(rt, *name, fs.Args())
	if err != nil {
		return err
	}

	b, err := rt.Store.Load(target)
	if err != nil {
		return err
	}

	targetRound := *round
	if targetRound == 0 {
		if *drift {
			targetRound = b.Round
		} else {
			targetRound = b.Round - 1
		}
	}
	if targetRound < 1 {
		return fmt.Errorf("binding %q has no completed round yet", target)
	}

	if *stat {
		entries, err := rt.Store.ReadLog(target)
		if err != nil {
			return err
		}
		targetKind := store.KindDiff
		if *drift {
			targetKind = store.KindDrift
		}
		var found *store.LogEntry
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Round == targetRound && entries[i].Kind == targetKind {
				found = &entries[i]
				break
			}
		}
		if found == nil || found.Note == "" {
			if *drift {
				return fmt.Errorf("no drift recorded for round %d of %q (--drift)", targetRound, target)
			}
			return fmt.Errorf("no diff recorded for round %d of %q", targetRound, target)
		}
		fmt.Println(found.Note)
		return nil
	}

	var patch []byte
	var ok bool
	if *drift {
		patch, ok, err = relay.ReadDrift(rt, target, targetRound)
	} else {
		patch, ok, err = relay.ReadDiff(rt, target, targetRound)
	}
	if err != nil {
		return err
	}
	if !ok {
		if *drift {
			return fmt.Errorf("no drift recorded for round %d of %q (--drift)", targetRound, target)
		}
		return fmt.Errorf("no diff recorded for round %d of %q", targetRound, target)
	}

	_, err = os.Stdout.Write(patch)
	return err
}

func cmdAnswer(args []string) error {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	name := fs.String("name", "", "binding whose builder to answer")
	keys := fs.String("keys", "", "logical key to send, e.g. enter or esc")
	text := fs.String("text", "", "literal text to send")
	choice := fs.Int("choice", 0, "numbered dialog option to pick")
	pickFlag := fs.Bool("pick", false, "choose the blocked builder from a list and type the answer there (needs a terminal)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		if *keys != "" || *text != "" || *choice != 0 {
			return errors.New("--pick takes the answer from the screen; drop --keys, --choice and --text")
		}
		return runPick(pick.Options{Verb: pick.VerbAnswer})
	}

	// answer presses a key into a live dialog, and with peer builders the cwd
	// fallback resolves to builder #1 every time -- the planner's tree is the
	// one it owns, while peers live in their own worktrees. Guessing here
	// answers a prompt nobody read, so answer joins done and unbind in
	// refusing to guess.
	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay answer <name> (--keys K | --choice N | --text S) | --pick  (or --name <name>)%s\n"+
			"answer types into a live dialog; it will not guess which one you meant",
			bindingHint("answer"))
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if err := relay.Answer(context.Background(), rt, target,
		relay.AnswerInput{Keys: *keys, Text: *text, Choice: *choice}); err != nil {
		return err
	}

	fmt.Println(relay.AnswerText(target))
	return nil
}

// filterReport narrows a status report to one binding. An empty name keeps
// every row, because listing them all is what a bare `relay status` is for.
//
// An unknown name is an error rather than an empty report: a silent blank
// would read exactly like a healthy binding with nothing outstanding.
// scopeReport decides what a status-shaped command shows. A named binding is
// always shown, DONE or not: asking for one by name is already a request for
// that specific thing. Otherwise DONE rows are hidden unless --all, and the
// same rule applies to --json so the two formats never disagree about what
// exists. It is a pure function so the rule can be tested without a herdr.
func scopeReport(rep relay.Report, name string, all bool) relay.Report {
	if name != "" || all {
		return rep
	}
	return relay.HideDone(rep)
}

func filterReport(rep relay.Report, name string) (relay.Report, error) {
	if name == "" {
		return rep, nil
	}
	for _, b := range rep.Bindings {
		if b.Name == name {
			return relay.Report{Bindings: []relay.BindingStatus{b}}, nil
		}
	}
	return relay.Report{}, fmt.Errorf("no binding named %q", name)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	all := fs.Bool("all", false, "include bindings marked DONE (hidden by default; relay gc clears them)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	name := fs.String("name", "", "show only this binding (default: all)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, err := bindingArg(*name, fs.Args())
	if err != nil {
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

	rep, err = filterReport(rep, target)
	if err != nil {
		return err
	}
	rep = scopeReport(rep, target, *all)

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
	all := fs.Bool("all", false, "include bindings marked DONE (hidden by default; relay gc clears them)")
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
		rep = scopeReport(rep, "", *all)
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

// runPick opens the interactive picker for one verb (#15). The three
// non-zero outcomes have already been shown on screen, so they exit 1
// silently: a second "relay: ..." line would go to the plugin log, not to
// the human (spec §3). Anything else is a startup failure and prints.
func runPick(opts pick.Options) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = pick.Run(ctx, rt, opts)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pick.ErrCancelled), errors.Is(err, pick.ErrNothingToPick), errors.Is(err, pick.ErrVerbFailed):
		return exitCodeErr{code: 1}
	default:
		return err
	}
}

// pickNamesNothing is the spec §3 rule shared by the three verbs: --pick and
// a binding name are mutually exclusive.
func pickNamesNothing(nameFlag string, positional []string) error {
	if nameFlag != "" || len(positional) > 0 {
		return errors.New("--pick chooses the binding; do not also name one")
	}
	return nil
}

func cmdDone(args []string) error {
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	name := fs.String("name", "", "binding to mark done")
	pickFlag := fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		return runPick(pick.Options{Verb: pick.VerbDone})
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay done <name> | --pick  (or --name <name>)%s\n"+
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

	fmt.Println(relay.DoneText(target))
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
	heldGrace := fs.Duration("held-grace", relay.DefaultHeldGrace,
		"how long a focused planner must be quiet before a held payload is injected anyway")
	check := fs.Bool("check", false, "exit 0 if a daemon is running, 1 if not; print nothing")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	rt.HeldGrace = *heldGrace

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

	slog.Info("relay daemon starting", "interval", *interval, "held_grace", *heldGrace)
	return relay.NewDaemon(rt, *interval).Run(ctx)
}

// bindingArg picks the binding name out of a --name flag and whatever
// positionals were left over, and refuses to take two. An empty return means
// no name was given at all, which each caller interprets for itself.
//
// It refuses even when both spellings agree: a caller who wrote the name twice
// has a mistaken model of the command, and silently accepting one of them hides
// that until the day the two differ.
func bindingArg(nameFlag string, positional []string) (string, error) {
	switch {
	case nameFlag != "" && len(positional) > 0:
		return "", fmt.Errorf("binding named twice: --name %s and %q; pass it once", nameFlag, positional[0])
	case len(positional) > 1:
		return "", fmt.Errorf("too many binding names: %v; pass one", positional)
	case nameFlag != "":
		return nameFlag, nil
	case len(positional) == 1:
		return positional[0], nil
	default:
		return "", nil
	}
}

// resolveBinding names the binding a command should act on: the one given by
// --name or as a positional, else the binding that owns the current directory,
// so the planner rarely has to name it at all.
//
// The positional form is not decoration. Before #50 these commands read --name
// only and dropped a positional on the floor, which from a bound directory sent
// `relay send frontend --file p.md` to the CWD's builder instead of frontend's,
// with no error and a round log that recorded it as legitimate.
func resolveBinding(rt relay.Runtime, nameFlag string, positional []string) (string, error) {
	name, err := bindingArg(nameFlag, positional)
	if err != nil {
		return "", err
	}
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
		return "", fmt.Errorf("no binding for %s; name one with `relay <command> NAME` or run relay bind first", cwd)
	}

	return b.Name, nil
}
