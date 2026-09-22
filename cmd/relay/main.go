// Command relay automates plan and report handoff between a planner agent pane
// and a builder agent pane running under herdr.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/classify"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/history"
	"github.com/fuad-daoud/relay/internal/hooks"
	diffpatch "github.com/fuad-daoud/relay/internal/patch"
	"github.com/fuad-daoud/relay/internal/pick"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/proc"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/release"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/serve"
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
  bind      bind this planner pane to a builder over the current working tree [--tier]
  add       attach an additional builder to this planner, on its own worktree [--tier] [--branch B]
  fork      branch a new binding from an earlier round with its own worktree [--tier]
  send      stage a plan file as the current round and start the builder [--tier] [--dry-run] [--verify|--no-verify]
  ask       spawn a one-shot consult and record it on the binding
  pull      print the oldest pending payload to stdout, without typing anywhere
  diff      print a round's captured patch to stdout [--anchors]
  review    turn a path:line comments file into a follow-up plan quoting each anchored hunk [--round N] [--out path] [--send]
  status    one row per binding: round, state, live pane status, what is pending [--all]
  statusline  this planner's builders, one row each, for Claude Code's statusLine setting
  log       print a binding's append-only round log
  history   one line per round across every binding, live or archived, newest first [--here] [--since 7d] [--json]
  show      one round's plan, report, diff, drift, log or transcript, live or archived [--round N] [--json]
  tab       tokens and cost across bindings, archived ones included [--since 7d] [--by binding|model|provider] [--json]
  wait      block until a round closes or needs you; exit 0 closed, 2 unmarked, 5 halted/blocked per report, 3 needs you, 4 done/unbound, 124 timeout
  ui        interactive reader: report, terminal, diff and log tabs
  done      mark a binding done; relaying stops (--pick to choose it on screen)
  pause     release a binding's worktree between rounds; branch and log stay; bind --resume brings it back [--commit]
  stop      kill the builder process and close its round without a report unless one is already on disk
  land      rebase a binding's branch onto its base, run the gate, push, and open or print the PR [--onto] [--pr] [--merge]
  edge      add|list|rm a planner-declared handoff to another binding, fired at the source's round close: relay edge add <source> --when report --then send --target <binding> --prompt <file> [--mode queue|fire]
  unbind    forget a binding, deleting or archiving its directory (--pick to choose it on screen)
  gc        clear every binding the planner marked DONE
  daemon    run the long-running reconciler
  mcp       run an MCP server over stdio for a Claude Code planner pane: status/send/done
            as tools; in channel mode (auto-detected, or --mode channel) also pushes reports and
            NEEDS YOU into the session instead of typing them into its pane
  doctor    preflight check: herdr, daemon, harness binaries, integrations, roles
  candidates   list the configured harness/provider/model candidates
  policy       show, per role, which candidate relay would pick right now and why
  planner      register this planner (or re-attach an existing one), and list, rename or forget records
  unavailable  record a provider rate limit: relay unavailable <token> [--for D] [--reason S]
  available    clear a recorded rate limit locally and on every server your bindings name: relay available <provider|token>
  agent     print or install embedded agent role definitions (e.g. relay agent install --kind claude)
  db        path|migrate|stats for relay's sqlite database
  init      write starter candidates.json and policy.json from the harnesses on PATH, and install role definitions

  serve                     run the remote-builder server (listener + daemon)
  serve init|enroll|clients|revoke|fingerprint|status|gc|unbind|ui|gates|available|unavailable
                            server administration, on the server host

  client init|add-server|rm-server
                            this machine's remote-builder identity and server list
  servers   list configured remote servers and this client's enrollment on each
  add --server <name> [--base <ref>]  attach a builder that runs on a configured remote server

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

// releaseInputs gathers what release.Detect needs about the running binary:
// the version buildVersion chose, whether the module rather than an ldflags
// stamp supplied it, and the herdr-plugin.toml sitting beside the executable
// (the marker of either plugin install, since both leave ./relay in that
// directory). Disk reads only -- the release check never touches the network
// to learn who it is.
func releaseInputs() release.Inputs {
	in := release.Inputs{Version: buildVersion()}

	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			in.ExeDir = filepath.Dir(resolved)
			in.ManifestVersion = manifestVersion(filepath.Join(in.ExeDir, "herdr-plugin.toml"))
		}
	}

	// FromModule is true only when the ldflags stamp was empty and the module
	// version was not: exactly buildVersion's own fallback order.
	if version == "" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
			in.FromModule = true
		}
	}
	return in
}

// manifestVersion reads the version line of a plugin manifest the way
// scripts/plugin-fetch.sh does (sed -n 's/^version = "\(.*\)"$/\1/p'): a
// missing file or a manifest without the line is "", which is what makes a
// plugin install unrecognisable -- not an error.
func manifestVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), `version = "`)
		if !ok {
			continue
		}
		if v, ok := strings.CutSuffix(v, `"`); ok {
			return v
		}
	}
	return ""
}

// statusNotice is the line `relay status` prints above the rows when the
// cached check says a newer release exists, and "" whenever it does not.
// Pure: every input is an argument, so it is table-tested without a
// store, a daemon or a network (CI has no herdr).
//
// It returns "" for every SevOK row of the doctor's release table, so the
// statusline stays quiet exactly where `relay doctor` says "not checked",
// "nothing to update to" or "is current" -- and never claims an update
// relay cannot prove.
func statusNotice(running, latest string, ok bool, kind release.Kind) string {
	if !ok || kind == release.KindUnknown || kind == release.KindLocalBuild {
		return ""
	}
	if !release.NewerStrings(running, latest) {
		return ""
	}
	return fmt.Sprintf("relay %s is behind %s -- run relay doctor", running, latest)
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

// regateFlag turns --regate into the *int the relay package takes (#132 part
// 2). The flag's default is -1, meaning "not given", which becomes nil so the
// policy default or the existing binding value stands; any value the human
// actually typed must be >= 0, and a bad one exits 2 -- before any runtime is
// built, so nothing touches the state directory or herdr.
func regateFlag(fs *flag.FlagSet, regate *int) (*int, error) {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "regate" {
			given = true
		}
	})
	if !given {
		return nil, nil
	}
	if *regate < 0 {
		fmt.Fprintf(os.Stderr, "relay: --regate must be >= 0, got %d\n", *regate)
		return nil, exitCodeErr{code: 2}
	}
	return regate, nil
}

// noteRegateNoGate says when a repair budget landed on a binding that has no
// gate to fail (#132 part 2): accepted and inert, because a binding with no
// gate never produces gate=fail, but not worth leaving unexplained.
func noteRegateNoGate(b store.Binding) {
	if b.Regate > 0 && b.Gate == "" {
		fmt.Printf("  regate %d (no gate configured)\n", b.Regate)
	}
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
	case "send":
		return cmdSend(args[1:])
	case "ask":
		return cmdAsk(args[1:])
	case "pull":
		return cmdPull(args[1:])
	case "diff":
		return cmdDiff(args[1:])
	case "review":
		return cmdReview(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "statusline":
		return cmdStatusline(args[1:])
	case "log":
		return cmdLog(args[1:])
	case "history":
		return cmdHistory(args[1:])
	case "show":
		return cmdShow(args[1:])
	case "tab":
		return cmdTab(args[1:])
	case "wait":
		return cmdWait(args[1:])
	case "ui":
		return cmdUI(args[1:])
	case "done":
		return cmdDone(args[1:])
	case "pause":
		return cmdPause(args[1:])
	case "stop":
		return cmdStop(args[1:])
	case "land":
		return cmdLand(args[1:])
	case "edge":
		return cmdEdge(args[1:])
	case "daemon":
		return cmdDaemon(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "candidates":
		return cmdCandidates(args[1:])
	case "policy":
		return cmdPolicy(args[1:])
	case "planner":
		return cmdPlanner(args[1:])
	case "unavailable":
		return cmdUnavailable(args[1:])
	case "available":
		return cmdAvailable(args[1:])
	case "agent":
		return cmdAgent(args[1:])
	case "db":
		return cmdDB(args[1:])
	case "init":
		return cmdInit(args[1:])
	case "serve":
		return cmdServe(args[1:])
	case "client":
		return cmdClient(args[1:])
	case "servers":
		return cmdServers(args[1:])
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

// newHooksDispatcher wires the local hooks.d script dispatcher, and, when
// policy.json's notify.webhooks is non-empty, a WebhookSink beside it (#4):
// both receive every event, fanned out by a MultiDispatcher.
func newHooksDispatcher(hooksCfg hooks.Config, pol policy.Policy) hooks.Dispatcher {
	sinks := []hooks.Dispatcher{hooks.NewLocalDispatcher(hooksCfg, hooks.NewOSExecutor(hooksCfg.LogPath))}

	if pol.Notify != nil && len(pol.Notify.Webhooks) > 0 {
		sinks = append(sinks, &hooks.WebhookSink{
			Hooks: pol.Notify.Webhooks,
			Log:   openHooksLogAppend(hooksCfg.LogPath),
		})
	}

	return hooks.MultiDispatcher(sinks)
}

// openHooksLogAppend opens hooksCfg.LogPath for append, creating its parent
// directory if needed, for the WebhookSink to log delivery failures to
// (alongside hook script failures). A failure to open falls back to
// io.Discard: a webhook is best-effort, and losing its failure log is not a
// reason to fail the caller.
func openHooksLogAppend(path string) io.Writer {
	if path == "" {
		return io.Discard
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return io.Discard
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return io.Discard
	}
	return f
}

// opencodeStateFile resolves $XDG_STATE_HOME/opencode/service.json, falling
// back to ~/.local/state/opencode/service.json, the same way store.DefaultRoot
// resolves $XDG_STATE_HOME.
func opencodeStateFile() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode", "service.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "opencode", "service.json")
}

// opencodeDBPath resolves $XDG_DATA_HOME/opencode/opencode.db, falling back
// to ~/.local/share/opencode/opencode.db.
func opencodeDBPath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode", "opencode.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// newDeliverers builds Runtime.Deliverers: an OpencodeDeliverer keyed by
// "opencode" when sqlite3 is on PATH, nil otherwise (docs/specs/2026-09-22-opencode-delivery-design.md).
// No sqlite3 means the deliverer could never confirm a delivery, so relay
// falls back to today's pane injection for every opencode planner.
func newDeliverers() map[string]relay.PlannerDeliverer {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}
	return map[string]relay.PlannerDeliverer{
		"opencode": &relay.OpencodeDeliverer{
			Exec:      binExec{},
			StateFile: opencodeStateFile(),
			DBPath:    opencodeDBPath(),
		},
	}
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

	cls, _ := classify.Resolve(pol.Classify, configDir, os.Getenv)

	reader, prices := newUsageReader(configDir)
	home, _ := os.UserHomeDir()

	hooksCfg, err := resolveHooksConfig()
	if err != nil {
		return relay.Runtime{}, err
	}
	dispatcher := newHooksDispatcher(hooksCfg, pol)

	st := store.New(root)
	gitClient := git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes)

	remoteClient, transport, err := newRemoteClient(configDir, gitClient)
	if err != nil {
		return relay.Runtime{}, err
	}

	rt := relay.Runtime{
		Herdr:            herdr.NewClient("herdr", 30*time.Second),
		Git:              gitClient,
		Runner:           proc.New(),
		Store:            st,
		Candidates:       candidates,
		LedgerPath:       st.LedgerPath(),
		AvailabilityPath: st.AvailabilityPath(),
		Policy:           pol,
		Scope:            scopeFromPolicy(pol.ScopeFor(false)),
		Classify:         cls,
		Usage:            reader,
		Sessions:         relay.HomeSessionLocator(home),
		Prices:           prices,
		Fetcher:          release.NewHTTPFetcher(release.Source(), 5*time.Second),
		Now:              time.Now,
		Hooks:            dispatcher,
		Remote:           remoteClient,
		Transport:        transport,
		Roles:            harness.OSRoleChecker(),
		Channels:         &relay.FileClaims{Root: st.ChannelsDir()},
		ProcStart:        procStartUnix,
		Deliverers:       newDeliverers(),
	}
	// The registry needs the runtime's own store and clock, so it is wired
	// here rather than in the literal above.
	rt.Planners = plannerRegistry(rt)
	return rt, nil
}

// procStartUnix reads a process's start time in Unix seconds, the pid-reuse
// defence planner.Resolve's host step and relay mcp's claim need. A read
// failure is returned, not swallowed: Resolve treats the error as "the host
// step cannot run" and falls through to the session.
func procStartUnix(pid int) (int64, error) {
	started, err := proc.StartTime(context.Background(), pid)
	if err != nil {
		return 0, err
	}
	return started.Unix(), nil
}

// newRemoteClient wires Runtime.Remote and Runtime.Transport (§4.7): both nil
// when servers.json is absent or empty. A servers.json with no client key is
// not fatal -- every remote path already reports ErrRemoteUnavailable on a
// nil Runtime.Remote -- but it prints once, since a configured server the
// client cannot reach is a setup mistake worth naming immediately rather
// than only when a remote command is next run.
func newRemoteClient(configDir string, gitClient *git.Client) (relay.RemoteClient, remote.TreeTransport, error) {
	servers, err := client.LoadServers(client.ServersPath(configDir))
	if err != nil {
		return nil, nil, err
	}
	if len(servers) == 0 {
		return nil, nil, nil
	}

	privPath, _ := client.KeyPaths(configDir)
	key, err := client.LoadKey(privPath)
	if err != nil {
		if errors.Is(err, client.ErrNoKey) {
			fmt.Fprintln(os.Stderr, "relay: servers.json present but no client key; run relay client init")
			return nil, nil, nil
		}
		return nil, nil, err
	}

	return client.New(servers, key, time.Now), remote.NewBundleTransport(gitClient, ""), nil
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

// headlessFlagNote is the one line relay prints when a local-builder verb is
// passed --headless: the flag is accepted and ignored (#303).
const headlessFlagNote = "relay: --headless is the default and only local mode; the flag is ignored"

// headlessNoOpLines is what bind/add/fork print to stderr when --headless is
// passed. Pure, so the rule is testable without running a subcommand that
// reaches herdr.
func headlessNoOpLines(headless bool) []string {
	if !headless {
		return nil
	}
	return []string{headlessFlagNote}
}

// askHeadlessFlagNote is what `relay ask --headless` prints: every consult is
// headless since #303, so the flag is accepted and ignored.
const askHeadlessFlagNote = "relay: --headless is the default and only consult mode; the flag is ignored"

// askHeadlessNoOpLines is what ask prints to stderr when --headless is
// passed. Pure, so the rule is testable without running a subcommand that
// reaches herdr.
func askHeadlessNoOpLines(headless bool) []string {
	if !headless {
		return nil
	}
	return []string{askHeadlessFlagNote}
}

// printHeadlessNoOp prints headlessNoOpLines to stderr.
func printHeadlessNoOp(headless bool) {
	for _, line := range headlessNoOpLines(headless) {
		fmt.Fprintln(os.Stderr, line)
	}
}

// builderWhere is how the bound/added lines name the builder's place: a local
// builder is always headless (a process relay runs itself, #99, #303).
func builderWhere(ep store.Endpoint) string {
	if ep.Headless() {
		return "headless"
	}
	return ep.PaneID
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
	h, err := history.Load(rt.AvailabilityPath)
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

	for _, line := range relay.ForwardUnavailable(context.Background(), rt, token, *reason) {
		fmt.Fprintln(os.Stderr, line)
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

	provider, removed, err := relay.Available(rt, subject, relay.ClearedByPlanner)
	if err != nil {
		return err
	}

	if removed == 0 {
		fmt.Printf("nothing was gating %s\n", provider)
	} else {
		fmt.Printf("cleared %s (%d entries)\n", provider, removed)
	}

	// The local clear is done either way; every server the bindings name is
	// asked, and each answer is printed -- these are answers, not warnings.
	ctx := context.Background()
	for _, line := range relay.ForwardAvailable(ctx, rt, subject) {
		fmt.Println(line)
	}

	// On a box that also runs a serve daemon, the client ledger just cleared
	// is not the ledger that gates anything: the daemon reads its own.
	if defRoot, err := store.DefaultRoot(); err == nil {
		if p, ok, _ := serve.ReadPointer(defRoot); ok && pidAlive(p.PID) {
			fmt.Printf("note: a relay serve daemon runs here with its own ledger (%s); use relay serve gates / relay serve available\n", filepath.Join(p.Root, "ledger.json"))
		}
	}

	return nil
}

func cmdBind(args []string) error {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: sanitized cwd basename)")
	builderAlias := fs.String("builder", "", "candidate harness/provider/model to spawn; omit to take the first ungated candidate in policy.json order[builder]")
	plannerFlag := fs.String("planner", "", "act as this planner (id or name; default: $RELAY_PLANNER, else this session's host)")
	resume := fs.Bool("resume", false, "adopt an existing binding into this planner")
	rebind := fs.Bool("rebind", false,
		"with --resume: replace a gone builder, picking it by policy.json order and the ledger (like bind with --builder omitted)")
	timeout := fs.Duration("timeout", 0, "round budget before relay flags the binding (default 24h)")
	headless := fs.Bool("headless", false,
		"accepted and ignored: a local builder is always a headless process per round")
	tier := fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then policy tier.<role>, then harness)")
	allowYolo := fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	gate := fs.String("gate", "", "acceptance command relay runs on the round's completion marker (default: policy.json gate.default)")
	noGate := fs.Bool("no-gate", false, "opt this binding out of policy.json's gate.default")
	regate := fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: policy.json gate.regate)")
	feature := fs.String("feature", "", "label grouping this binding with others (fork inherits it)")
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
	printHeadlessNoOp(*headless)
	if *feature != "" {
		if err := store.ValidFeature(*feature); err != nil {
			fmt.Fprintf(os.Stderr, "relay: %v\n", err)
			return exitCodeErr{code: 2}
		}
	}
	regateOpt, err := regateFlag(fs, regate)
	if err != nil {
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
		Name:         *name,
		PlannerID:    *plannerFlag,
		PlannerPane:  os.Getenv("HERDR_PANE_ID"),
		CWD:          cwd,
		Resume:       *resume,
		Rebind:       *rebind,
		RoundTimeout: *timeout,
		Headless:     *headless,
		Tier:         *tier,
		AllowYolo:    *allowYolo,
		Gate:         *gate,
		NoGate:       *noGate,
		Regate:       regateOpt,
		Feature:      *feature,
	}
	opts.Candidate = *builderAlias

	adopted := *resume
	kind := ""
	switch {
	case *rebind:
		kind = relay.CandidateKind(rt, opts.Candidate)
	case adopted:
		if *name != "" {
			if existing, err := rt.Store.Load(*name); err == nil {
				kind = existing.Builder.Kind
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
	if t := relay.RestoreText(res); t != "" {
		fmt.Println(t)
	}
	if res.WasPaused {
		fmt.Printf("resumed %s after pause\n", b.Name)
	}

	if *resume && (*builderAlias != "" || *rebind) {
		builderDesc := builderWhere(b.Builder)
		if b.BuilderCandidate != "" {
			builderDesc = fmt.Sprintf("%s (%s)", builderWhere(b.Builder), b.BuilderCandidate)
		}
		fmt.Printf("rebound %s: builder %s, still on round %d\n"+
			"hand it the round with:\n"+
			"  relay send --name %s --file %s\n",
			b.Name, builderDesc, b.Round, b.Name, rt.Store.PlanPath(b.Name, b.Round))
		noteRegateNoGate(b)
		notePick("builder", res)
		warnWaitingOnYou(rt, b.Name)
		return nil
	}

	fmt.Printf("bound %s: planner %s -> builder %s (%s), round %d\n",
		b.Name, b.Planner.PaneID, builderWhere(b.Builder), b.BuilderCandidate, b.Round)
	noteRegateNoGate(b)
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
	warnWaitingOnYou(rt, b.Name)
	return nil
}

func cmdFork(args []string) error {
	fs := flag.NewFlagSet("fork", flag.ContinueOnError)
	name := fs.String("name", "", "source binding to fork from")
	round := fs.Int("round", 0, "source round to copy history through")
	newName := fs.String("new-name", "", "name for the new binding")
	builderAlias := fs.String("builder", "", "candidate harness/provider/model to spawn (default: inherits source; else the first ungated in policy.json order[builder])")
	cwd := fs.String("cwd", "", "bind the fork to an existing directory instead of creating a git worktree")
	headless := fs.Bool("headless", false, "accepted and ignored: a local builder is always a headless process per round")
	tier := fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then policy tier.<role>, then harness)")
	allowYolo := fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	gate := fs.String("gate", "", "acceptance command relay runs on the round's completion marker (default: inherits the source binding's gate)")
	noGate := fs.Bool("no-gate", false, "opt this fork out of a gate even when the source binding has one")
	regate := fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: inherits the source binding's regate)")
	feature := fs.String("feature", "", "label grouping this binding with others (default: inherits the source binding's feature)")
	plannerFlag := fs.String("planner", "", "act as this planner (id or name; default: $RELAY_PLANNER, else this session's host)")
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
	if *feature != "" {
		if err := store.ValidFeature(*feature); err != nil {
			fmt.Fprintf(os.Stderr, "relay: %v\n", err)
			return exitCodeErr{code: 2}
		}
	}
	regateOpt, err := regateFlag(fs, regate)
	if err != nil {
		return err
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
		PlannerID:   *plannerFlag,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		CWD:         *cwd,
		Headless:    *headless,
		Tier:        *tier,
		AllowYolo:   *allowYolo,
		Gate:        *gate,
		NoGate:      *noGate,
		Regate:      regateOpt,
		Feature:     *feature,
	}
	printHeadlessNoOp(*headless)

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
	warnWaitingOnYou(rt, res.Binding.Name)

	return nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "name for the new binding")
	builderAlias := fs.String("builder", "", "candidate harness/provider/model to spawn; omit to take the first ungated candidate in policy.json order[builder]")
	cwd := fs.String("cwd", "", "bind the peer to an existing directory instead of creating a git worktree")
	branch := fs.String("branch", "", "existing local or origin/ branch to check out instead of cutting relay/<name>")
	headless := fs.Bool("headless", false, "accepted and ignored: a local builder is always a headless process per round")
	server := fs.String("server", "", "run the builder on this configured remote server instead of a local process (relay servers)")
	base := fs.String("base", "", "commit or ref to branch from with --server; defaults to HEAD")
	tier := fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then policy tier.<role>, then harness)")
	allowYolo := fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	gate := fs.String("gate", "", "acceptance command relay runs on the round's completion marker (default: policy.json gate.default)")
	noGate := fs.Bool("no-gate", false, "opt this binding out of policy.json's gate.default")
	regate := fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: policy.json gate.regate)")
	feature := fs.String("feature", "", "label grouping this binding with others (fork inherits it)")
	plannerFlag := fs.String("planner", "", "act as this planner (id or name; default: $RELAY_PLANNER, else this session's host)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// Before newRuntime, in this order: the flag pair, then a name that is
	// either given or derivable from the branch.
	if *branch != "" && *cwd != "" {
		fmt.Fprintf(os.Stderr, "relay: relay add --branch and --cwd are exclusive\n")
		return fmt.Errorf("relay add --branch and --cwd are exclusive: %w", exitCodeErr{code: 2})
	}
	if *name == "" {
		if *branch == "" {
			return fmt.Errorf("relay add requires --name NAME")
		}
		derived, err := relay.DefaultBindingName(*branch)
		if err != nil {
			return fmt.Errorf("relay add --branch %s: cannot derive a binding name (%v); pass --name", *branch, err)
		}
		*name = derived
	}
	if *feature != "" {
		if err := store.ValidFeature(*feature); err != nil {
			fmt.Fprintf(os.Stderr, "relay: %v\n", err)
			return exitCodeErr{code: 2}
		}
	}
	regateOpt, err := regateFlag(fs, regate)
	if err != nil {
		return err
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
		PlannerID:   *plannerFlag,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		Repo:        repo,
		CWD:         *cwd,
		Branch:      *branch,
		Headless:    *headless,
		Server:      *server,
		Base:        *base,
		Tier:        *tier,
		AllowYolo:   *allowYolo,
		Gate:        *gate,
		NoGate:      *noGate,
		Regate:      regateOpt,
		Feature:     *feature,
	})
	if err != nil {
		return err
	}
	printHeadlessNoOp(*headless)

	switch {
	case res.Binding.Builder.Remote():
		fmt.Printf("added %s: builder %s on %s\n", res.Binding.Name, res.Binding.BuilderCandidate, res.Binding.Builder.Server)
		if res.Binding.Tier != "" {
			fmt.Printf("  tier %s (server)\n", res.Binding.Tier)
		} else {
			fmt.Printf("  tier server's choice (pre-tier server)\n")
		}
	case res.Binding.Builder.Headless():
		fmt.Printf("added %s: builder %s (headless)\n", res.Binding.Name, res.Binding.BuilderCandidate)
	default:
		fmt.Printf("added %s: builder %s in %s\n",
			res.Binding.Name, res.Binding.BuilderCandidate, builderWhere(res.Binding.Builder))
	}
	noteRegateNoGate(res.Binding)
	if n := relay.GatedNote(rt, res.Binding.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick("builder", res.Resolution)
	switch {
	case res.Binding.Builder.Remote() && res.Binding.ExistingBranch:
		fmt.Printf("  branch %s (existing, tip %s) on %s\n", res.Binding.Branch, res.Base, res.Binding.Builder.Server)
	case res.Binding.Builder.Remote():
		fmt.Printf("  branch %s (from %s)\n", res.Binding.Branch, res.Base)
	case res.Worktree != "" && res.Binding.ExistingBranch:
		fmt.Printf("  worktree %s on existing branch %s (tip %s)\n", res.Worktree, res.Branch, res.Base)
	case res.Worktree != "":
		fmt.Printf("  worktree %s on %s (from %s)\n", res.Worktree, res.Branch, res.Base)
	default:
		fmt.Printf("  tree %s\n", res.Binding.CWD)
	}
	fmt.Printf("  relay send --name %s --file <plan.md>\n", res.Binding.Name)
	noteConsultRolesTooLong(res.Binding.Name)
	warnWaitingOnYou(rt, res.Binding.Name)

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
	warnWaitingOnYou(rt, target)

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

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	file := fs.String("file", "", "path to the plan file to hand the builder")
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	tier := fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then policy tier.<role>, then harness)")
	allowYolo := fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	dryRun := fs.Bool("dry-run", false, "check every precondition and print what send would do, without sending")
	regate := fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: policy.json gate.regate)")
	verify := fs.Bool("verify", false, "run a read-only reviewer in a throwaway worktree when the round closes")
	noVerify := fs.Bool("no-verify", false, "do not run a reviewer when the round closes (default: policy.json verify.default)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// Before newRuntime, like add's flag pair: the refusal must not depend on
	// argv order and must touch neither the state directory nor herdr.
	if *verify && *noVerify {
		fmt.Fprintf(os.Stderr, "relay: relay send --verify and --no-verify are exclusive\n")
		return fmt.Errorf("relay send --verify and --no-verify are exclusive: %w", exitCodeErr{code: 2})
	}
	if *file == "" {
		return fmt.Errorf("relay send requires --file")
	}
	regateOpt, err := regateFlag(fs, regate)
	if err != nil {
		return err
	}

	var verifyOpt *bool
	if *verify || *noVerify {
		v := *verify
		verifyOpt = &v
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	target, err := resolveBinding(rt, *name, fs.Args())
	if err != nil {
		return err
	}

	opts := relay.SendOptions{
		Tier:      *tier,
		AllowYolo: *allowYolo,
		Regate:    regateOpt,
		Verify:    verifyOpt,
	}

	if *dryRun {
		d, err := relay.SendDryRun(context.Background(), rt, target, *file, opts)
		if err != nil {
			return err
		}
		fmt.Print(relay.RenderDryRun(d))
		return nil
	}

	res, err := relay.Send(context.Background(), rt, target, *file, opts)
	if err != nil {
		return err
	}

	if res.Drift != "" {
		fmt.Println(res.Drift)
	}
	fmt.Printf("sent round %d to %s's builder\n", res.Round, target)
	warnWaitingOnYou(rt, target)
	return nil
}

func cmdAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	role := fs.String("role", "", "consult role: reviewer, researcher")
	cand := fs.String("candidate", "", "candidate harness/provider/model; omit to take the first ungated in policy.json order[<role>]")
	file := fs.String("file", "", "file containing the question")
	question := fs.String("question", "", "the question itself; with --round, exactly one of --file and -q")
	fs.StringVar(question, "q", "", "the question itself (shorthand for --question)")
	round := fs.Int("round", 0, "ask the builder that built this closed round: resumes its session, headless and read-only")
	nameFlag := fs.String("name", "", "binding name")
	headless := fs.Bool("headless", false, "accepted and ignored: every consult is a one-shot process since #303")
	plannerFlag := fs.String("planner", "", "act as this planner (id or name; default: $RELAY_PLANNER, else this session's host)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// --headless is the default and only consult mode; the flag is accepted
	// for compatibility and says so once.
	for _, line := range askHeadlessNoOpLines(*headless) {
		fmt.Fprintln(os.Stderr, line)
	}
	if *round > 0 {
		// The round's recorded session fixes the role and the candidate, so
		// neither is required; --role is only worth a note.
		if *role != "" {
			fmt.Fprintln(os.Stderr, "note: relay ask --round ignores --role; the resumed session fixes the role")
		}
		if (*file != "") == (*question != "") {
			return fmt.Errorf("relay ask --round needs --file or -q")
		}
	} else {
		if *role == "" {
			return fmt.Errorf("relay ask needs --role ROLE")
		}
		if *file == "" {
			return fmt.Errorf("relay ask needs --file PATH")
		}
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
		Question:    *question,
		Round:       *round,
		Name:        name,
		PlannerID:   *plannerFlag,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
	})
	if err != nil {
		return err
	}

	if *round > 0 {
		fmt.Printf("asked round %d's builder (%s session %s) on %s (pid %d)\nfindings will appear at: %s\n",
			*round, res.Consult.Endpoint.Kind, res.Consult.Endpoint.SessionID, res.Binding,
			res.Consult.Endpoint.PID, res.Consult.FindingsPath)
		return nil
	}

	fmt.Printf("asked %s consult %s on %s (pid %d)\nfindings will appear at: %s\n",
		res.Consult.Role, res.Consult.ID, res.Binding, res.Consult.Endpoint.PID, res.Consult.FindingsPath)
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

	if rt.Remote != nil {
		if _, serr := relay.SyncRemote(context.Background(), rt); serr != nil {
			fmt.Fprintf(os.Stderr, "relay: sync remote bindings: %v\n", serr)
		}
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
	anchors := fs.Bool("anchors", false, "prefix each hunk and line with its path:line, for a review comments file")
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
		// #143: a successful print is what "viewed" means; the stamp is
		// best-effort and must never fail a read command.
		_ = rt.Store.MarkViewed(target, time.Now())
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

	if *anchors {
		patch, err = diffpatch.Annotate(patch)
		if err != nil {
			return err
		}
	}

	if _, err := os.Stdout.Write(patch); err != nil {
		return err
	}
	// #143: a successful print is what "viewed" means; the stamp is
	// best-effort and must never fail a read command.
	_ = rt.Store.MarkViewed(target, time.Now())
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

// filterReportPlanner narrows a status report to one planner's bindings. It
// is a pure function so the rule is testable without a herdr. An empty
// planner id keeps every row, which is what a runtime with no registry gets.
func filterReportPlanner(rep relay.Report, plannerID string) relay.Report {
	if plannerID == "" {
		return rep
	}
	kept := rep.Bindings[:0:0]
	for _, b := range rep.Bindings {
		if b.PlannerID == plannerID {
			kept = append(kept, b)
		}
	}
	rep.Bindings = kept
	return rep
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
	if rt.Remote != nil {
		if _, serr := relay.SyncRemote(context.Background(), rt); serr != nil {
			fmt.Fprintf(os.Stderr, "relay: sync remote bindings: %v\n", serr)
		}
	}
	rep, err := relay.Status(context.Background(), rt)
	if err != nil {
		return err
	}

	// §3.3: a bare `relay status` shows the calling planner's bindings. A
	// session with no planner -- no registry, no match -- keeps the old
	// behaviour and lists everything.
	if target == "" {
		if rec, ok := plannerFilter(rt); ok {
			rep = filterReportPlanner(rep, rec.ID)
		}
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

	// #293: one line above the rows, only when the daemon's cached check has
	// seen a newer release. Read through the doctor's own Env so `status` and
	// `doctor` can never disagree about the same file -- only ReleaseState is
	// called here, which is why the nil herdr client is harmless. JSON output
	// above stays notice-free.
	env := doctor.NewEnv(nil, rt.Store, releaseInputs())
	running, latest, ok, kind := env.ReleaseState()
	if notice := statusNotice(running, latest, ok, kind); notice != "" {
		fmt.Println(notice)
	}

	fmt.Print(relay.RenderStatus(rep))
	return nil
}

func cmdStatusline(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: relay statusline")
	}
	if fi, err := os.Stdin.Stat(); err != nil || relay.ShouldDrainStdin(fi.Mode()) {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	columns, err := strconv.Atoi(os.Getenv("COLUMNS"))
	if err != nil || columns <= 0 {
		columns = 0
	}
	columns = relay.StatusLineWidth(columns, os.Getenv("RELAY_STATUSLINE_MARGIN"))
	rt, err := newRuntime()
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay statusline: %v\n", err)
		return nil
	}
	// §3.3: the row set is the calling planner's bindings. A session with no
	// planner renders nothing, the same as an unset $HERDR_PANE_ID did
	// before #303.
	rec, ok := plannerFilter(rt)
	if !ok {
		return nil
	}
	rep, err := relay.PlannerStatus(context.Background(), rt, rec.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay statusline: %v\n", err)
		return nil
	}
	fmt.Print(relay.RenderStatusLine(rep, rt.Now(), columns))
	return nil
}

func cmdLog(args []string) error {
	const usage = "usage: relay log <name> [--round N] [--after N] [--json] [--follow]"

	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), usage) }
	after := fs.Int("after", 0, "show only entries with a Seq greater than this (0 = all)")
	asJSON := fs.Bool("json", false, "one compact JSON object per line (NDJSON)")
	follow := fs.Bool("follow", false, "keep printing new entries until the binding is DONE or removed")
	round := fs.Int("round", 0, "show only this round (default: all rounds)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *after < 0 {
		fmt.Fprintln(os.Stderr, "relay: --after must be >= 0")
		return exitCodeErr{code: 2}
	}
	args = fs.Args()
	if len(args) != 1 {
		return fmt.Errorf("%s", usage)
	}
	name := args[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	// A binding that does not exist is named at once, the way every other
	// command reports it; only a binding that disappears mid-follow (below)
	// ends the loop quietly.
	if _, err := rt.Store.Load(name); err != nil {
		return err
	}

	// emit applies the --round filter, so the initial batch and every
	// followed entry render identically.
	emit := func(e store.LogEntry) {
		if *round != 0 && e.Round != *round {
			return
		}
		if *asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(e)
			return
		}
		fmt.Println(relay.LogLine(e))
	}

	entries, err := rt.Store.ReadLogAfter(name, *after)
	if err != nil {
		return err
	}
	last := *after
	for _, e := range entries {
		emit(e)
		last = e.Seq
	}

	if !*follow {
		// #143: a successful print is what "viewed" means; the stamp is
		// best-effort and must never fail a read command.
		_ = rt.Store.MarkViewed(name, time.Now())
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relay.FollowLog(ctx, rt, name, last, time.Second, emit); err != nil {
		if ctx.Err() != nil {
			// Interrupted: what was already printed is the answer, and the
			// stamp is #143's the same as any other exit.
			_ = rt.Store.MarkViewed(name, time.Now())
			return nil
		}
		return err
	}
	_ = rt.Store.MarkViewed(name, time.Now())
	return nil
}

// cmdWait blocks until a round closes or needs a human, per spec
// docs/specs/2026-09-14-wait-and-waiting-on-you-design.md §4.8. It reads
// relay's own state only: newRuntime's herdr client is constructed but never
// called.
func cmdWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	anyFlag := fs.Bool("any", false, "wait on every named binding; the first to close or need you wins, its name printed first")
	round := fs.Int("round", 0, "round to wait on (default: the newest round sent; an earlier round answers from the log)")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait before giving up")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("relay wait --timeout must be positive")
	}
	if *round < 0 {
		return fmt.Errorf("relay wait --round must be >= 0")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	var names []string
	if *anyFlag {
		if *name != "" || len(fs.Args()) == 0 {
			return fmt.Errorf("usage: relay wait --any NAME [NAME...]  (--any takes one or more positional names, not --name)")
		}
		names = fs.Args()
	} else {
		target, err := resolveBinding(rt, *name, fs.Args())
		if err != nil {
			return err
		}
		names = []string{target}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	resName, res, err := relay.Wait(ctx, rt, relay.WaitOptions{
		Names: names, Round: *round, Timeout: *timeout, Interval: time.Second,
	})
	if err != nil {
		return err
	}

	if *anyFlag && resName != "" {
		fmt.Println(resName)
	}
	if res.Line != "" {
		fmt.Println(res.Line)
	}
	if res.Code == 0 {
		return nil
	}
	return exitCodeErr{res.Code}
}

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	interval := fs.Duration("interval", 0, "refresh interval")
	dashboard := fs.Bool("dashboard", false, "start on the dashboard screen")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// An open failure never blocks the ui from starting -- it runs in
	// live scope, with a sticky notice, exactly as pressing "a" with no
	// database does (docs/specs/2026-09-20-persistence-design.md §6).
	var notice string
	if d, dbErr := openDB(rt.Store.DBPath()); dbErr != nil {
		notice = fmt.Sprintf("no database: %v", dbErr)
	} else {
		rt.DB = d
		defer d.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}

	here, _ := os.Getwd()

	return ui.Run(ctx, rt, ui.Options{
		Interval:  *interval,
		PrefsPath: filepath.Join(root, "ui.json"),
		Here:      here,
		Notice:    notice,
		Dashboard: *dashboard,
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
	res, err := relay.Done(context.Background(), rt, target)
	if err != nil && !errors.Is(err, relay.ErrStopFailed) {
		return err
	}

	fmt.Println(relay.DoneText(target, res))
	if err != nil {
		return err
	}
	warnWaitingOnYou(rt, target)
	return nil
}

// cmdPause releases a binding's worktree and pane between rounds. It takes
// the binding from --name or a positional and never from the current
// directory: pause is not undoable without a resume, so it must not guess.
func cmdPause(args []string) error {
	fs := flag.NewFlagSet("pause", flag.ContinueOnError)
	name := fs.String("name", "", "binding to pause")
	commit := fs.Bool("commit", false, "commit the worktree's changes on the binding branch before releasing it")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay pause <name> | --name <name>\n" +
			"pause releases a binding's worktree and pane between rounds; it is not undoable without a resume, so it must not guess")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	res, err := relay.Pause(context.Background(), rt, target, relay.PauseOptions{Commit: *commit})
	if err != nil {
		return err
	}

	fmt.Println(relay.PauseText(target, res))
	return nil
}

// cmdStop ends a binding's open round on purpose (#138): a headless builder
// is killed now and the round closes without a report unless one is already
// on disk. It takes the binding from --name or a positional and never from
// the current directory -- a stop ends a round, so it must not guess.
func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	name := fs.String("name", "", "binding whose open round to stop")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay stop <name> | --name <name>\n" +
			"stop kills the builder process and closes its round; it must not guess")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	res, err := relay.Stop(context.Background(), rt, target, relay.StopOptions{})
	if errors.Is(err, relay.ErrNothingToStop) {
		// Nothing to stop is an answer, not a failure.
		fmt.Printf("nothing to stop: %s has no open round\n", target)
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Println(relay.StopText(target, res))
	return nil
}

// cmdLand integrates a binding's branch with its base and publishes it: it
// rebases onto origin/<base> (or merges it in with --merge), runs the gate on
// the rebased tree, pushes, and opens or prints the PR (#136). It never
// merges a PR, never deletes a branch or a worktree, and is never triggered
// by a marker -- a human types it.
//
// It takes the binding from --name or a positional and never from the current
// directory: land pushes, so it must not guess.
func cmdLand(args []string) error {
	fs := flag.NewFlagSet("land", flag.ContinueOnError)
	name := fs.String("name", "", "binding to land")
	onto := fs.String("onto", "", "base ref to rebase onto; required when the binding recorded no base branch")
	pr := fs.Bool("pr", false, "open the PR with gh pr create when gh is on PATH, else print the command")
	noGate := fs.Bool("no-gate", false, "skip the binding's gate for this land")
	force := fs.Bool("force", false, "land even though the round is still open")
	merge := fs.Bool("merge", false, "merge origin/<base> instead of rebasing onto it")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay land <name> | --name <name> [--onto <ref>] [--pr] [--no-gate] [--force] [--merge]\n" +
			"land rebases the binding's branch onto its base, runs the gate and pushes it; it must not guess which binding")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	res, err := relay.Land(context.Background(), rt, target, relay.LandOptions{
		Onto:   *onto,
		PR:     *pr,
		NoGate: *noGate,
		Force:  *force,
		Merge:  *merge,
	})
	if err != nil {
		return landFailure(target, err)
	}

	fmt.Println(relay.LandText(res))
	return nil
}

// landFailure prints land's error and maps it onto the documented exit codes
// (#136): a dirty worktree or a failed gate is the human's to fix (2), a
// conflict is a rebase they must resolve (3), and anything else is an
// ordinary failure. The message is printed here because main exits silently
// on an exitCodeErr -- the code is the whole answer only when stderr already
// carries the reason.
func landFailure(target string, err error) error {
	switch {
	case errors.Is(err, relay.ErrLandConflict):
		fmt.Fprintf(os.Stderr, "relay: land %s: %v\n", target, err)
		return exitCodeErr{code: 3}
	case errors.Is(err, relay.ErrLandDirty), errors.Is(err, relay.ErrLandGate):
		fmt.Fprintf(os.Stderr, "relay: land %s: %v\n", target, err)
		return exitCodeErr{code: 2}
	default:
		return err
	}
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
	// Nowhere else: a CLI one-shot (any other command) must not relaunch a
	// builder it merely happens to observe as "exited, code unknown" (#244).
	rt.StartedAt = time.Now()
	rt.HeldGrace = *heldGrace
	// The scope template newRuntime filled is logged once here, in the same
	// shape `relay serve` uses (#295). "off" is scope.enabled: false; the
	// local daemon does not probe at startup, so there is no "unavailable"
	// here -- a later probe failure is the runner's one-line warning (§3.1).
	slog.Info(fmt.Sprintf("scopes=%s", scopeStatusText(rt.Scope)))

	// The database is opened only here (and by `relay db *`): the daemon
	// is the process that writes it every tick; `relay serve`'s state root
	// is its own and stays out of scope. An open failure never blocks the
	// daemon from starting -- every ingest call site treats DB == nil like
	// a machine with no database.
	if d, derr := openDB(rt.Store.DBPath()); derr != nil {
		slog.Warn("relay daemon: db unavailable; ingest disabled", "err", derr)
	} else {
		rt.DB = d
		defer d.Close()
	}

	configDir, err := userConfigRoot()
	if err != nil {
		return err
	}
	watcher := relay.NewConfigWatcher(relay.ConfigPaths{
		Candidates: filepath.Join(configDir, "relay", "candidates.json"),
		Policy:     filepath.Join(configDir, "relay", "policy.json"),
		ConfigDir:  configDir,
		Getenv:     os.Getenv,
	})

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
	rt.Hooks = newHooksDispatcher(hooksCfg, rt.Policy)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("relay daemon starting", "interval", *interval, "held_grace", *heldGrace)
	return relay.NewDaemon(rt, *interval).WithRefresh(watcher.Refresh).Run(ctx)
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

// warnWaitingOnYou prints one stderr line per other binding that is waiting
// on a human, per spec §4.9. except is the binding the verb just acted on.
// It never changes the caller's return value or exit code: a WaitingOnYou
// error is itself only a stderr warning.
func warnWaitingOnYou(rt relay.Runtime, except string) {
	lines, err := relay.WaitingOnYou(rt, except)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: waiting-on-you check: %v\n", err)
		return
	}
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
}
