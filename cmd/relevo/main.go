// Command relevo automates plan and report handoff between a planner agent and
// a headless builder process.
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

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/history"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/latency"
	"github.com/fuad-daoud/relevo/internal/legacy"
	diffpatch "github.com/fuad-daoud/relevo/internal/patch"
	"github.com/fuad-daoud/relevo/internal/pick"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui"
	"github.com/fuad-daoud/relevo/internal/upgrade"
)

// version is stamped at build time with -ldflags "-X main.version=v1.2.3".
// A `go install`ed binary carries no such stamp, so buildVersion falls back to
// the module version the toolchain records in the build info.
var version = ""

// distribution is stamped by release.yml with
// -X main.distribution=release. No Makefile target and no `go install` sets
// it, so it is empty in every other build; release.Detect reads the zero
// value as "no claim".
var distribution = ""

const usage = `relevo automates the plan/report handoff between two AI coding agent
processes: a planner hands work to a builder, and relevo moves
the files between them.

Usage:
  relevo <command> [flags]

Commands:
  bind      bind this planner pane to a builder over the current working tree [--tier]
              (--role is now --actor)
              --worktree | --cwd DIR | --branch B | --server S
                        attach another builder to this planner, on its own worktree or tree
              --from SRC[@ROUND]
                        branch a new binding from an earlier round with its own worktree
  send      stage a plan file as the current round and start the builder [--tier] [--dry-run] [--verify|--no-verify]
  ask       spawn a one-shot consult and record it on the binding
  review    turn a path:line comments file into a follow-up plan quoting each anchored hunk [--round N] [--out path] [--send]
  status    one row per binding: round, state, live pane status, what is pending [--all] [--line]
  history   one line per round across every binding, live or archived, newest first [--here] [--since 7d] [--json]
              --tab   tokens and cost across bindings, archived ones included [--since 7d] [--by binding|model|provider] [--json]
              --stats rounds, outcomes, switches, gate and consults across bindings, archived ones included; provider blocks from the last 30d [--since 7d] [--json]
  show      one round's plan, report, diff, drift, gate, findings, log or transcript, live or archived [--round N] [--diff [--stat|--anchors]] [--log [--follow --after N]] [--json]
  wait      block until a round closes or needs you, then print the pending report; exit 0 closed, 2 unmarked, 5 halted/blocked per report, 3 needs you, 4 done/unbound, 124 timeout [--peek]
  ui [:view [args]]  the cockpit: :fleet, :rounds [query], :round <binding> [N]
  done      mark a binding done; relaying stops (--pick to choose it on screen)
  stop      kill the builder process and close its round without a report unless one is already on disk
  land      rebase a binding's branch onto its base, run the gate, push, and open or print the PR [--onto] [--pr] [--merge]
  edge      add|list|rm a planner-declared handoff to another binding, fired at the source's round close: relevo edge add <source> --when report --then send --target <binding> --prompt <file> [--mode queue|fire]
  unbind    forget a binding, deleting or archiving its directory (--pick to choose it on screen)
              --done clears every binding the planner marked DONE [--delete] [--dry-run]
  daemon    run the long-running reconciler
  mcp       run an MCP server over stdio for a Claude Code planner pane: status/send/done
            as tools; in channel mode (auto-detected, or --mode channel) also pushes reports and
            NEEDS YOU into the session instead of typing them into its pane
  doctor    preflight check: plugin, daemon, harness binaries, roles
  update    replace this release binary with the latest release, checksum-verified [--check] [--to vX.Y.Z] [--release]
  migrate   move ` + legacy.Name + `-era state, switch the client unit and remove the old binary [--dry-run] [--keep-old-binary]
  config    show the actors, the current pick and the candidates
  config edit|get|set|unset|export|import
            read and change the configuration document
  config init|agents
            write starter configuration or agent definitions
  config server add|rm|list|key
            this machine's remote-builder identity and server list
  config secret set|rm|list
            store or forget the typesafe and client.key secrets
  planner      register this planner (or re-attach an existing one), and list, rename or forget records
  gate      list this machine's active gates; gate a provider: relevo gate <token> [--for D] [--reason S];
            clear one: relevo gate --clear <provider|token>; relevo gate --serve [--state DIR] acts on the
            local serve daemon's gates instead

  serve                     run the remote-builder server (listener + daemon)
  serve init|enroll|clients|revoke|fingerprint|status|gc|unbind|ui
                            server administration, on the server host

  help      print this message
  version   print the relevo version

Run "relevo <command> -h" for that command's flags.

relevo drives the harness binaries it launches, which must be on PATH
State lives in $XDG_STATE_HOME/relevo (default ~/.local/state/relevo).
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
	// Every remote request carries this as Relevo-Client-Version, so a server's
	// journal shows which client build asked (#373 §4.4). Set before any
	// command runs, and informational only: it is never signed.
	client.Version = buildVersion()

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
		fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
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
// stamp supplied it, and where the executable lives. Disk reads only -- the
// release check never touches the network to learn who it is.
func releaseInputs() release.Inputs {
	in := release.Inputs{Version: buildVersion(), Distribution: distribution}

	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			in.ExeDir = filepath.Dir(resolved)
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

// statusNotice is the line `relevo status` prints above the rows when the
// cached check says a newer release exists, and "" whenever it does not.
// Pure: every input is an argument, so it is table-tested without a
// store, a daemon or a network (CI launches no harness).
//
// It returns "" for every SevOK row of the doctor's release table, so the
// statusline stays quiet exactly where `relevo doctor` says "not checked",
// "nothing to update to" or "is current" -- and never claims an update
// relevo cannot prove.
func statusNotice(running, latest string, ok bool, kind release.Kind) string {
	if !ok || kind == release.KindUnknown || kind == release.KindLocalBuild {
		return ""
	}
	if !release.NewerStrings(running, latest) {
		return ""
	}
	return fmt.Sprintf("relevo %s is behind %s -- run relevo doctor", running, latest)
}

// parseFlags parses one subcommand's flags. It turns `-h` into a clean exit:
// the flag package has already printed usage, so the caller just returns.
//
// It parses iteratively rather than once, because flag.Parse stops at the first
// non-flag argument -- which meant `relevo bind --from webshop --round 2` silently
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

// regateFlag turns --regate into the *int the relevo package takes (#132 part
// 2). The flag's default is -1, meaning "not given", which becomes nil so the
// policy default or the existing binding value stands; any value the human
// actually typed must be >= 0, and a bad one exits 2 -- before any runtime is
// built, so nothing touches the state directory or launches a harness.
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
		fmt.Fprintf(os.Stderr, "relevo: --regate must be >= 0, got %d\n", *regate)
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

// isTerminal reports whether f is a character device: the same
// os.ModeCharDevice check internal/ui/source.go uses for stdout.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// bareArgs is what a bare `relevo` runs: `ui` when stdin and stdout are both
// terminals, nil otherwise (round 3, step 3.1). Pure, so a test covers all
// four combinations without a terminal.
func bareArgs(stdinTTY, stdoutTTY bool) []string {
	if stdinTTY && stdoutTTY {
		return []string{"ui"}
	}
	return nil
}

func run(args []string) error {
	if len(args) == 0 {
		// A bare `relevo` on a terminal is `relevo ui` (round 3, step 3.1);
		// it then falls through every guard below exactly as `ui` does.
		if uiArgs := bareArgs(isTerminal(os.Stdin), isTerminal(os.Stdout)); uiArgs != nil {
			args = uiArgs
		} else {
			fmt.Fprint(os.Stderr, usage)
			return errUsagePrinted
		}
	}

	// #292 §4: an install that runs relevo before `relevo migrate` sees empty
	// new roots and starts creating them, which then blocks migrate. Every
	// verb that could touch state is refused except the exempt four. This runs
	// before captureAgyEnv because captureAgyEnv writes into the state root --
	// exactly what the guard exists to prevent.
	if !guardExempt(args[0]) {
		if err := refuseUnmigrated(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitCodeErr{code: 1}
		}
	}

	// Every verb captures the calling agy session's agentapi credentials
	// (#349): an agy planner runs relevo constantly (send, wait, pull,
	// status), and whichever verb it happens to run after an agy restart is
	// the one that refreshes the session's agentapi credentials.
	captureAgyEnv()

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "-v", "--version":
		fmt.Printf("relevo %s\n", buildVersion())
		return nil
	case "bind":
		return cmdBind(args[1:])
	case "unbind":
		return cmdUnbind(args[1:])
	case "send":
		return cmdSend(args[1:])
	case "ask":
		return cmdAsk(args[1:])
	case "review":
		return cmdReview(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "history":
		return cmdHistory(args[1:])
	case "show":
		return cmdShow(args[1:])
	case "wait":
		return cmdWait(args[1:])
	case "ui":
		return cmdUI(args[1:])
	case "done":
		return cmdDone(args[1:])
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
	case "update":
		return cmdUpdate(args[1:])
	case "migrate":
		return cmdMigrate(args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "planner":
		return cmdPlanner(args[1:])
	case "gate":
		return cmdGate(args[1:])
	case "serve":
		return cmdServe(args[1:])
	default:
		// The verbs P2b folded into `relevo config`, and the verbs P4a merged
		// into bind/show/unbind/status, name their replacement rather than the
		// generic unknown-subcommand error (§4.4, §4.6).
		if replacement, ok := removedVerbs[args[0]]; ok {
			fmt.Fprintf(os.Stderr, "relevo: %q was removed; use %s\n", args[0], replacement)
			return exitCodeErr{code: 2}
		}
		return fmt.Errorf("unknown subcommand %q; run \"relevo help\" for the command list", args[0])
	}
}

// removedVerbs names each removed verb and the form that replaces it: the
// seven P2b folded into `relevo config` (§4.4), the seven P4a merged into
// bind, show, unbind and status (§4.6), the P4a round 2 verbs folded into
// wait and gate (§4.1, §4.3), and the three P3d folded into history and
// doctor (P3d §4.6).
var removedVerbs = map[string]string{
	"init":       "relevo config init",
	"candidates": "relevo config",
	"policy":     "relevo config",
	"roles":      "relevo config",
	"agent":      "relevo config agents",
	"client":     "relevo config server",
	"servers":    "relevo config server list",

	"add":        "relevo bind --worktree",
	"fork":       "relevo bind --from <source>@<round>",
	"diff":       "relevo show --diff",
	"log":        "relevo show --log",
	"gc":         "relevo unbind --done",
	"pause":      "relevo done, then relevo bind --resume",
	"statusline": "relevo status --line",

	"pull":        "relevo wait (it prints the report)",
	"unavailable": "relevo gate <token>",
	"available":   "relevo gate --clear <provider>",
	"tab":         "relevo history --tab",
	"stats":       "relevo history --stats",
	"db":          "relevo doctor (the database row)",
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

// resolveHooksConfig builds the hook dispatcher environment: the hooks section
// the config store loaded, and the run log its runs are recorded in (#4.4).
// log is the machine database's kv log; a caller with no database open
// (preflight, check, serve's admin paths) passes nil, and hook output is then
// recorded nowhere.
func resolveHooksConfig(hooksMap map[string][][]string, log hooks.RunLog) (hooks.Config, error) {
	return hooks.Config{
		Hooks: hooksMap,
		Log:   log,
	}, nil
}

// hooksRunLog is the machine database's hook run log: the kv row every hook
// run and webhook failure is recorded in (P3b round 2 §4.4). A store whose
// database cannot be opened gets a nil log, which records nothing.
func hooksRunLog(st *store.Store) hooks.RunLog {
	d, err := st.DB()
	if err != nil {
		return nil
	}
	return hooks.NewKVLog(db.TxKV{DB: d}, filepath.Dir(st.DBPath()))
}

// newHooksDispatcher wires the local hooks.d script dispatcher, and, when
// config policy's notify.webhooks is non-empty, a WebhookSink beside it (#4):
// both receive every event, fanned out by a MultiDispatcher.
func newHooksDispatcher(hooksCfg hooks.Config, pol policy.Policy) hooks.Dispatcher {
	sinks := []hooks.Dispatcher{hooks.NewLocalDispatcher(hooksCfg, hooks.NewOSExecutor(hooksCfg.Log))}

	if pol.Notify != nil && len(pol.Notify.Webhooks) > 0 {
		sinks = append(sinks, &hooks.WebhookSink{
			Hooks: pol.Notify.Webhooks,
			Runs:  hooksCfg.Log,
		})
	}

	return hooks.MultiDispatcher(sinks)
}

// opencodeServiceFiles returns candidate service.json paths in preference
// order: the state dir first ($XDG_STATE_HOME/opencode/service.json or
// ~/.local/state/opencode/service.json), then the config dir
// ($XDG_CONFIG_HOME/opencode/service.json or ~/.config/opencode/service.json).
func opencodeServiceFiles() []string {
	home, _ := os.UserHomeDir()
	stateFile := filepath.Join(home, ".local", "state", "opencode", "service.json")
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		stateFile = filepath.Join(xdg, "opencode", "service.json")
	}
	configFile := filepath.Join(home, ".config", "opencode", "service.json")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		configFile = filepath.Join(xdg, "opencode", "service.json")
	}
	return []string{stateFile, configFile}
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

// captureAgyEnv persists the calling agy session's agentapi credentials, when
// the environment carries them (#349). It runs for every verb rather than a
// chosen list, and it costs nothing when ANTIGRAVITY_* is absent: the machine
// database is opened only when there is something to capture, because the
// capture itself short-circuits on the same check (P3b round 2 §4.3).
//
// Both failures are deliberately silent: capture is best-effort and must never
// change a command's exit code or output, so a root relevo cannot resolve, a
// database it cannot open, and an environment with nothing to capture all
// end the same way -- nothing written, nothing printed.
func captureAgyEnv() {
	if !relevo.AgyEnvPresent(os.Getenv) {
		return
	}
	root, err := store.DefaultRoot()
	if err != nil {
		return
	}
	st := store.New(root)
	d, err := st.DB()
	if err != nil {
		return
	}
	secrets := db.SecretStore{DB: d}
	// The credentials were files under planners/.agy before this round: a file
	// that is present is imported, then removed (§4.3).
	_ = relevo.ImportAgyCreds(secrets, st.AgyCredsDir())
	_, _ = relevo.CaptureAgyCreds(os.Getenv, secrets, time.Now().UTC())
}

// newDeliverers builds Runtime.Deliverers: the agy deliverer always, and an
// OpencodeDeliverer keyed by "opencode" when sqlite3 is on PATH
// (docs/specs/2026-09-22-opencode-delivery-design.md). No sqlite3 means the
// opencode deliverer could never confirm a delivery, so an opencode planner's
// reports stay pending for the background wait. agy needs no external tool: it reads
// the captured credential secret from the machine database and runs agy
// itself, and reports its own failure as OutcomeUnavailable.
func newDeliverers() map[string]relevo.PlannerDeliverer {
	deliverers := map[string]relevo.PlannerDeliverer{}
	// store.DefaultRoot has already succeeded once in newRuntime; the guard is
	// only for the shape of the function, and a root relevo cannot resolve means
	// every verb has failed long before a delivery is attempted.
	if root, err := store.DefaultRoot(); err == nil {
		// DBIfExists, not DB: a root with no relevo.db yet -- `daemon
		// --preflight` and `--check` run against one -- must not get a
		// database conjured into it just because a deliverer was wired.
		if d, derr := store.New(root).DBIfExists(); derr == nil && d != nil {
			deliverers["agy"] = &relevo.AgyDeliverer{
				Exec:  binEnvExec{},
				Creds: db.SecretStore{DB: d},
			}
		}
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return deliverers
	}
	deliverers["opencode"] = &relevo.OpencodeDeliverer{
		Exec:       binExec{},
		StateFiles: opencodeServiceFiles(),
		DBPath:     opencodeDBPath(),
	}
	return deliverers
}

// newRuntime constructs the production runtime. Config and secrets come from
// the machine database; any file present under <userConfigRoot>/relevo is
// imported into it first and removed (#4.6). aliases.json is never read (#80).
func newRuntime() (relevo.Runtime, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}

	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		return relevo.Runtime{}, err
	}
	cs := config.Open(d)

	configDir, err := userConfigRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}
	if !d.Newer() {
		dir := filepath.Join(configDir, "relevo")
		if _, err := cs.As("import", "imported "+dir).ImportFiles(dir, time.Now().UTC()); err != nil {
			return relevo.Runtime{}, err
		}
	} else {
		// A schema a newer relevo wrote is never written by this binary: read
		// what is there and skip the import (#4.6).
		slog.Warn("relevo.db schema is newer; config import skipped")
	}

	if !d.Newer() {
		// A1's migration: write a name for every stored candidate. Names are
		// derived in memory by candidate.Parse either way, so a failure here
		// is a warning, never a refusal to start.
		if _, err := cs.EnsureCandidateNames(); err != nil {
			slog.Warn("candidate names not written to config", "err", err)
		}
		// A2 round 2's one-time migration: a pre-actors config becomes actors
		// plus agents, as a "migration" revision. An error is a warning too,
		// and the old config keeps working.
		if migrated, err := cs.MigrateToActors(); err != nil {
			slog.Warn("config: roles not migrated to actors", "err", err)
		} else if migrated {
			slog.Info("config: roles migrated to actors (relevo config log)")
		}
	}

	L, err := cs.Load()
	if err != nil {
		return relevo.Runtime{}, err
	}

	rt, err := buildRuntime(root, L, true)
	if err != nil {
		return relevo.Runtime{}, err
	}
	rt.Config = cs
	return rt, nil
}

// openDB ensures path's directory exists (Open's precondition) and opens
// it, migrating as needed. It was `relevo db`'s helper and stays here for
// every other verb that opens the machine database directly (P3d D3).
func openDB(path string) (*db.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return db.Open(path)
}

// newRuntimePeek constructs the runtime `relevo daemon --preflight` and
// `--check` need. It never creates, migrates or writes anything: it reads the
// config files when any is present, else the database read-only when one
// exists, else nothing at all (#4.6).
func newRuntimePeek() (relevo.Runtime, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}
	configDir, err := userConfigRoot()
	if err != nil {
		return relevo.Runtime{}, err
	}
	dir := filepath.Join(configDir, "relevo")

	var L config.Loaded
	switch {
	case configFilesPresent(dir):
		L, err = config.LoadFiles(dir)
	case fileExists(filepath.Join(root, "relevo.db")):
		var d *db.DB
		d, err = db.OpenReadOnly(filepath.Join(root, "relevo.db"))
		if err == nil {
			L, err = config.Open(d).Load()
			_ = d.Close()
		}
	default:
		L, err = config.LoadFiles(dir)
	}
	if err != nil {
		return relevo.Runtime{}, err
	}

	// buildRuntime with openGates false: preflight and check open no database
	// and carry nil Gates/Latency, so they touch no gate record (P3b plan §4.5).
	return buildRuntime(root, L, false)
}

// configFilesPresent reports whether any file the import consumes, or a hooks
// directory, is present in dir.
func configFilesPresent(dir string) bool {
	for _, name := range config.Files() {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "hooks")); err == nil && info.IsDir() {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// buildRuntime wires the Runtime from one loaded config, the shape both
// newRuntime and newRuntimePeek use. openGates opens the store root's database
// for the gate, availability and latency records (P3b plan §4.5): newRuntime
// passes true, and a DB() error is fatal for the verb; newRuntimePeek passes
// false, so preflight and check leave the root with no database and nil gates.
func buildRuntime(root string, L config.Loaded, openGates bool) (relevo.Runtime, error) {
	pol := L.Policy

	// Warnings are carried, never printed: every CLI command calls newRuntime,
	// so printing here would be noise (#372 §4.4). `relevo doctor` renders them
	// and the daemon logs each once.
	configWarnings := L.Warnings

	cls, _ := classify.Resolve(pol.Classify, L.Typesafe, os.Getenv)

	reader, prices := newUsageReader(L.Prices)
	home, _ := os.UserHomeDir()

	st := store.New(root)
	gitClient := git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes)

	// Gates, availability and latency live in the store root's database; the
	// legacy directory holding ledger.json/availability.json/history.json is
	// the store root too, so LoadKV imports them on first read (P3b plan §4.5).
	// The channel claims, the planner registry and the hooks run log live in
	// the same database (P3b round 2 §4.1-§4.4), so `relevo daemon --preflight`
	// and `--check`, which pass openGates false, open no database at all.
	var (
		gates    db.KV
		gatesDir string
		claims   relevo.ClaimStore
		runLog   hooks.RunLog
	)
	if openGates {
		d, err := st.DB()
		if err != nil {
			return relevo.Runtime{}, err
		}
		gates = d
		gatesDir = root
		claims = &relevo.KVClaims{KV: db.TxKV{DB: d}, Root: st.ChannelsDir()}
		runLog = hooksRunLog(st)
	}

	hooksCfg, err := resolveHooksConfig(L.Hooks, runLog)
	if err != nil {
		return relevo.Runtime{}, err
	}
	dispatcher := newHooksDispatcher(hooksCfg, pol)

	remoteClient, transport, err := newRemoteClient(L.Servers, L.ClientKey, gitClient)
	if err != nil {
		return relevo.Runtime{}, err
	}

	var opencodeSession func(cwd string, now time.Time) (string, error)
	if _, err := exec.LookPath("sqlite3"); err == nil {
		opencodeSession = relevo.OpencodeSessionFinder{
			Exec:   binExec{},
			DBPath: opencodeDBPath(),
		}.Find
	}

	rt := relevo.Runtime{
		Git:             gitClient,
		Runner:          proc.New(),
		Store:           st,
		Candidates:      L.Candidates,
		Gates:           gates,
		GatesDir:        gatesDir,
		Latency:         gates,
		Policy:          pol,
		Registry:        L.Registry,
		ConfigWarnings:  configWarnings,
		Scope:           scopeFromPolicy(pol.ScopeFor(false)),
		Classify:        cls,
		Usage:           reader,
		Sessions:        relevo.HomeSessionLocator(home),
		Prices:          prices,
		Fetcher:         release.NewHTTPFetcher(release.Source(), 5*time.Second),
		Now:             time.Now,
		Hooks:           dispatcher,
		Remote:          remoteClient,
		Transport:       transport,
		Roles:           harness.OSRoleChecker(),
		Channels:        claims,
		ProcStart:       procStartUnix,
		OpencodeSession: opencodeSession,
		Deliverers:      newDeliverers(),
	}
	// The registry needs the runtime's own store and clock, so it is wired
	// here rather than in the literal above. A runtime with no database open
	// (preflight, check) keeps a nil registry, which every caller already
	// treats as "no planners".
	if openGates {
		reg, rerr := plannerRegistry(rt)
		if rerr != nil {
			return relevo.Runtime{}, rerr
		}
		rt.Planners = reg
	}
	return rt, nil
}

// procStartUnix reads a process's start time in Unix seconds, the pid-reuse
// defence planner.Resolve's host step and relevo mcp's claim need. A read
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
// when the servers section is absent or empty. Servers without a client key
// are not fatal -- every remote path already reports ErrRemoteUnavailable on a
// nil Runtime.Remote -- but it prints once, since a configured server the
// client cannot reach is a setup mistake worth naming immediately rather
// than only when a remote command is next run.
func newRemoteClient(servers client.Servers, key []byte, gitClient *git.Client) (relevo.RemoteClient, remote.TreeTransport, error) {
	if len(servers) == 0 {
		return nil, nil, nil
	}

	if len(key) == 0 {
		fmt.Fprintln(os.Stderr, "relevo: servers configured but no client key; run relevo config server key")
		return nil, nil, nil
	}

	kp, err := remote.ParsePrivate(key)
	if err != nil {
		return nil, nil, err
	}

	return client.New(servers, kp, time.Now), remote.NewBundleTransport(gitClient, ""), nil
}

// noteConsultRolesTooLong prints, after a successful bind/add/fork, the one
// advisory line naming configured consult roles the binding's name is too long
// for -- so a later `relevo ask` failing on the derived name is not a surprise
// a day later. It is a note, not an error: a binding that can build is still
// useful, and refusing would let the alias table dictate binding names.
func noteConsultRolesTooLong(reg *roles.Registry, name string) {
	roles := relevo.ConsultRolesTooLongFor(reg, name)
	if len(roles) == 0 {
		return
	}
	// The tightest limit is set by the longest role: relevo ask needs the
	// binding name at most store.MaxAgentNameLen - 10 - len(role) characters.
	longest := roles[0]
	for _, r := range roles[1:] {
		if len(r) > len(longest) {
			longest = r
		}
	}
	fmt.Printf("note: %s is too long for the %s consult role(s); relevo ask needs a binding name of at most %d characters for %s\n",
		name, strings.Join(roles, ", "), store.MaxAgentNameLen-10-len(longest), longest)
}

// notePick prints why relevo chose the candidate it spawned. Silent for
// an explicit token (the planner already knows) and for adoption
// (nothing was chosen); the gated note, if any, is printed separately.
// A1 §4.4: the line names the candidate by its short name.
func notePick(rt relevo.Runtime, role string, res relevo.Resolution) {
	if res.How == "" || res.How == relevo.HowExplicit {
		return
	}
	fmt.Fprintln(os.Stderr, relevo.PickText(role, res, rt.Candidates))
}

// roleOrBuilder is the role name a flag value means: "builder" for "", else
// the value. The CLI's --actor and the registry both spell the default builder
// as "".
func roleOrBuilder(r string) string {
	if r == "" {
		return "builder"
	}
	return r
}

// builderWhere is how the bound/added lines name the builder's place: a local
// builder is always headless (a process relevo runs itself, #99, #303).
func builderWhere(ep store.Endpoint) string {
	if ep.Headless() {
		return "headless"
	}
	return ep.PaneID
}

// parseFor turns --for into an absolute expiry. Empty means "until cleared"
// (a zero time); anything else must be a positive Go duration -- relevo does
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
	fs := flag.NewFlagSet("relevo config", flag.ContinueOnError)
	probe := fs.Bool("probe", false, "run each candidate once with a one-line prompt from this machine and record its time to first output")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if !*probe && len(fs.Args()) > 0 {
		return fmt.Errorf("usage: relevo config --probe [token...]")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if *probe {
		host, _ := os.Hostname()
		tokens := fs.Args()
		n := len(tokens)
		if n == 0 {
			n = rt.Candidates.Len()
		}
		fmt.Fprintf(os.Stderr, "probing %d candidate(s) from %s, one at a time\n", n, host)

		// The column is sized from the names FormatProbe prints, not from
		// argv: Probe resolves each argument and FormatProbe prints the
		// resolved candidate's short name (A1 §4.4, round 3 F2).
		var names []string
		if len(tokens) > 0 {
			for _, tok := range tokens {
				c, err := rt.Candidates.Resolve(tok)
				if err != nil {
					names = append(names, tok)
					continue
				}
				names = append(names, rt.Candidates.NameOf(c.Ref().String()))
			}
		} else {
			for _, ref := range rt.Candidates.Refs() {
				names = append(names, rt.Candidates.NameOf(ref))
			}
		}
		width := relevo.ProbeNameWidth(names)

		_, err := relevo.Probe(context.Background(), rt, lineExec{}, tokens, host, func(r relevo.ProbeResult) {
			fmt.Println(relevo.FormatProbe(r, width))
		})
		return err
	}

	fmt.Print(formatCandidates(rt))
	return nil
}

// formatCandidates renders the candidate table cmdCandidates' non-probe form
// prints -- the candidates block `relevo config` shows.
func formatCandidates(rt relevo.Runtime) string {
	h := latency.History{}
	if rt.Latency != nil {
		loaded, err := latency.LoadKV(rt.Latency, legacyGatesPath(rt.GatesDir, "latency.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo: could not read latency: %v\n", err)
		} else {
			h = loaded
		}
	}
	h = h.Prune(rt.Now())

	lat := make(map[string]latency.Summary)
	for _, ref := range rt.Candidates.Refs() {
		lat[ref] = h.Summary(ref)
	}

	return relevo.FormatCandidatesLatencyFor(rt.RoleRegistry(), rt.Candidates, relevo.Gates(rt), lat)
}

// legacyGatesPath is <dir>/<name> for the pre-kv gate documents, moved to
// internal/relevo (cockpit C2b §4.1) so the stats input assembly shares it.
func legacyGatesPath(dir, name string) string {
	return relevo.LegacyGatesPath(dir, name)
}

// loadHistory reads the availability history for display, treating an
// unreadable record as empty after one stderr line -- the same rule Gates
// applies to the ledger. The body moved to relevo.LoadHistory (cockpit C2b
// §4.1); this keeps the stderr line for its other callers.
func loadHistory(rt relevo.Runtime) history.History {
	h, err := relevo.LoadHistory(rt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read history: %v\n", err)
		return history.History{}
	}
	return h
}

// formatPolicy renders the per-role pick explanation the `pick` block of
// `relevo config` shows.
func formatPolicy(rt relevo.Runtime) string {
	return relevo.FormatPolicyFor(rt.RoleRegistry(), rt.Candidates, rt.Policy, relevo.Gates(rt), loadHistory(rt), rt.Now(), time.Local)
}

// cmdGate is one verb for the gate operations that used to be four: the
// top-level unavailable and available, and the serve gates|available|unavailable
// subverbs (§4.3). With no positional it lists the active
// gates; a positional gates a provider; --clear lifts a gate; --serve sends
// the same three forms to the local serve daemon's own ledger.
func cmdGate(args []string) error {
	const gateUsage = `usage: relevo gate
       relevo gate <token> [--for D] [--reason S]
       relevo gate --clear <provider|token>
       relevo gate --serve [--state DIR] [<token> [--for D] [--reason S] | --clear <provider|token>]`

	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	forFlag := fs.String("for", "", "how long to gate the provider (Go duration, e.g. 2h); omit to leave it gated until `relevo gate --clear`")
	reason := fs.String("reason", "", "why, for the record")
	clear := fs.String("clear", "", "clear a recorded rate limit: --clear <provider|token>")
	serveFlag := fs.Bool("serve", false, "act on the local serve daemon's gates instead of this machine's")
	_ = fs.String("state", "", "with --serve: state directory")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if *serveFlag {
		return gateServe(fs, positional, *forFlag, *reason, *clear)
	}
	switch {
	case *clear != "":
		return gateClear(*clear)
	case len(positional) == 1:
		return gateUnavailable(positional[0], *forFlag, *reason)
	case len(positional) == 0 && *forFlag == "" && *reason == "":
		return gateList()
	default:
		fmt.Fprintln(os.Stderr, gateUsage)
		return exitCodeErr{code: 2}
	}
}

// gateList prints this machine's active gates: the rendering `relevo serve
// gates` printed for the serve root's ledger (§4.3), fed by relevo.Gates.
func gateList() error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}
	fmt.Print(serve.RenderGates(relevo.Gates(rt), rt.Now()))
	return nil
}

// gateUnavailable records a provider rate limit: exactly today's
// cmdUnavailable (§4.3).
func gateUnavailable(token, forFlag, reason string) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	until, err := parseFor(forFlag, rt.Now())
	if err != nil {
		return err
	}

	// The argument may be a candidate name or a token. It is resolved here,
	// first, so the ledger and every server the bindings name see the
	// canonical token, never the raw argument (A1 §4.2).
	c, err := rt.Candidates.Resolve(token)
	if err != nil {
		return err
	}
	canonical := c.Ref().String()

	provider, err := relevo.Unavailable(rt, canonical, until, reason)
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

	fmt.Printf("gated %s (%d candidates) %s\n", provider, count, relevo.GateUntilText(until))

	if bs, err := rt.Store.List(); err == nil {
		if names := relevo.BindingsOnProvider(bs, provider); len(names) > 0 {
			fmt.Printf("the daemon will switch: %s\n", strings.Join(names, ", "))
		}
	}

	for _, line := range relevo.ForwardUnavailable(context.Background(), rt, canonical, reason) {
		fmt.Fprintln(os.Stderr, line)
	}

	return nil
}

// gateClear lifts a recorded rate limit locally and on every server the
// bindings name: exactly today's cmdAvailable (§4.3).
func gateClear(subject string) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	provider, removed, err := relevo.Available(rt, subject, relevo.ClearedByPlanner)
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
	for _, line := range relevo.ForwardAvailable(ctx, rt, subject) {
		fmt.Println(line)
	}

	// On a box that also runs a serve daemon, the client ledger just cleared
	// is not the record that gates anything: a serve daemon keeps its own
	// gates, in the serve root's database (P3b plan §4.5). The pointer lives
	// under the serve root (#372 §4.6): the client root's daemon.json is
	// DaemonInfo, which decodes as a pointer with a live pid.
	if defRoot, err := defaultServeRoot(); err == nil {
		if p, ok, _ := serve.ReadPointer(defRoot); ok && pidAlive(p.PID) {
			fmt.Print("note: a relevo serve daemon runs here with its own gates; use relevo gate --serve\n")
		}
	}

	return nil
}

// gateServe sends the three gate forms to the local serve daemon's own
// ledger: today's `relevo serve gates`, `relevo serve unavailable` and
// `relevo serve available`, via the serve root's ledgerRuntime (§4.3).
func gateServe(fs *flag.FlagSet, positional []string, forFlag, reason, clear string) error {
	switch {
	case clear != "":
		return serveGateClear(fs, clear)
	case len(positional) == 1:
		return serveGateUnavailable(fs, positional[0], forFlag, reason)
	case len(positional) == 0 && forFlag == "" && reason == "":
		return serveGateList(fs)
	default:
		return fmt.Errorf("usage: relevo gate --serve [--state DIR] [<token> [--for D] [--reason S] | --clear <provider|token>]")
	}
}

// bindFlags is the union of the flags today's bind, add and fork each accept.
// bindRouteFor chooses which of the three bodies runs; keeping the flag set in
// one place is what lets each old flag keep its name, default and help text
// (§4.1).
type bindFlags struct {
	name      string
	builder   string
	planner   string
	resume    bool
	rebind    bool
	timeout   time.Duration
	tier      string
	allowYolo bool
	gate      string
	noGate    bool
	regate    *int
	feature   string
	role      string

	// The placement flags choose add's path.
	worktree bool
	cwd      string
	branch   string
	server   string
	base     string

	// --from chooses fork's path; round is fork's --round.
	from  string
	round int
}

// bindRoute names which of the three merged paths cmdBind runs.
type bindRoute int

const (
	routeBind bindRoute = iota
	routeAdd
	routeFork
)

// bindRouteFor chooses the path from the parsed flags and refuses the
// combinations §4.1 forbids. It is a pure function, so the rules are table
// tested without a runtime, a state directory or a harness.
func bindRouteFor(f bindFlags) (bindRoute, error) {
	placement := f.worktree || f.cwd != "" || f.branch != "" || f.server != "" || f.base != ""
	switch {
	case f.from != "":
		// --cwd is fork's own, so it stays allowed here; the rest are add's.
		if f.resume || f.rebind {
			return routeBind, errors.New("--from cannot be combined with --resume/--rebind")
		}
		if f.worktree || f.branch != "" || f.server != "" || f.base != "" {
			return routeBind, errors.New("--from cannot be combined with --worktree/--branch/--server/--base")
		}
		return routeFork, nil
	case f.resume || f.rebind:
		// --resume and --rebind are bind's alone.
		if placement {
			return routeBind, errors.New("--resume/--rebind cannot be combined with --worktree/--cwd/--branch/--server/--base")
		}
		return routeBind, nil
	case placement:
		return routeAdd, nil
	default:
		return routeBind, nil
	}
}

// cmdBind binds a planner to a builder. It is the one entry point the old
// bind, add and fork merged into (§4.1): the flags choose which of the three
// bodies runs, and each body stays an unexported helper so none of its logic
// is duplicated.
// bindFlagValues holds the pointers bind's flags parse into. bindFlagSet
// defines them on fs; cmdBind and TestBindFlagsHaveActorNotRole read the same
// surface, so the flag names can never drift from what a test pins (A2 round 3
// S1).
type bindFlagValues struct {
	name         *string
	builderAlias *string
	plannerFlag  *string
	resume       *bool
	rebind       *bool
	timeout      *time.Duration
	tier         *string
	allowYolo    *bool
	gate         *string
	noGate       *bool
	regate       *int
	feature      *string
	actor        *string
	worktree     *bool
	cwd          *string
	branch       *string
	server       *string
	base         *string
	from         *string
	round        *int
}

// bindFlagSet defines bind's flags on fs and returns the values they parse
// into. It is separate from cmdBind so a test can inspect the flag surface
// without running a bind (A2 round 3 S1).
func bindFlagSet(fs *flag.FlagSet) *bindFlagValues {
	v := &bindFlagValues{}
	v.name = fs.String("name", "", "binding name (default: sanitized cwd basename)")
	v.builderAlias = fs.String("builder", "", "candidate name or harness/provider/model token to spawn; omit to take the first ungated candidate in config policy order[builder]")
	v.plannerFlag = fs.String("planner", "", "act as this planner (id or name; default: $RELEVO_PLANNER, else this session's host)")
	v.resume = fs.Bool("resume", false, "adopt an existing binding into this planner")
	v.rebind = fs.Bool("rebind", false,
		"with --resume: replace a gone builder, picking it by config policy order and the ledger (like bind with --builder omitted)")
	v.timeout = fs.Duration("timeout", 0, "round budget before relevo flags the binding (default 24h)")
	v.tier = fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then policy tier.<role>, then harness)")
	v.allowYolo = fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	v.gate = fs.String("gate", "", "acceptance command relevo runs on the round's completion marker (default: config policy gate.default)")
	v.noGate = fs.Bool("no-gate", false, "opt this binding out of config policy's gate.default")
	v.regate = fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: config policy gate.regate)")
	v.feature = fs.String("feature", "", "label grouping this binding with others (fork inherits it)")
	v.actor = fs.String("actor", "", "the writer actor this binding runs (default builder)")
	v.worktree = fs.Bool("worktree", false, "attach an additional builder to this planner, on its own worktree")
	v.cwd = fs.String("cwd", "", "bind the peer to an existing directory instead of creating a git worktree")
	v.branch = fs.String("branch", "", "existing local or origin/ branch to check out instead of cutting relevo/<name>")
	v.server = fs.String("server", "", "run the builder on this configured remote server instead of a local process (relevo config server list)")
	v.base = fs.String("base", "", "commit or ref to branch from with --server; defaults to HEAD")
	v.from = fs.String("from", "", "source binding to branch a new one from, as <source>[@<round>]")
	v.round = fs.Int("round", 0, "with --from: source round to copy history through")
	return v
}

func cmdBind(args []string) error {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	v := bindFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	regateOpt, err := regateFlag(fs, v.regate)
	if err != nil {
		return err
	}

	f := bindFlags{
		name: *v.name, builder: *v.builderAlias, planner: *v.plannerFlag,
		resume: *v.resume, rebind: *v.rebind, timeout: *v.timeout, tier: *v.tier,
		allowYolo: *v.allowYolo, gate: *v.gate, noGate: *v.noGate, regate: regateOpt,
		feature: *v.feature, role: *v.actor, worktree: *v.worktree, cwd: *v.cwd,
		branch: *v.branch, server: *v.server, base: *v.base, from: *v.from, round: *v.round,
	}

	route, rerr := bindRouteFor(f)
	if rerr != nil {
		// One line, exit 2, before any runtime is built (§4.1).
		fmt.Fprintf(os.Stderr, "relevo: %v\n", rerr)
		return fmt.Errorf("%v: %w", rerr, exitCodeErr{code: 2})
	}

	switch route {
	case routeAdd:
		return runAdd(f)
	case routeFork:
		return runFork(f)
	default:
		return runBind(f)
	}
}

// runBind is bind's own body after parsing: bind the current tree, or resume
// or rebind an existing binding (the old cmdBind).
func runBind(f bindFlags) error {
	// --resume takes the name from --name, so a positional one is dropped on
	// the floor and the binding lookup then fails on the empty name.
	if f.resume && f.name == "" {
		return fmt.Errorf("relevo bind --resume needs --name NAME (a positional name is ignored)")
	}
	if f.rebind && !f.resume {
		return fmt.Errorf("relevo bind --rebind only applies with --resume (it replaces a gone builder on an existing binding)")
	}
	if f.role != "" && f.resume {
		return fmt.Errorf("relevo bind --resume keeps the binding's actor; drop --actor")
	}

	if f.feature != "" {
		if err := store.ValidFeature(f.feature); err != nil {
			fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
			return exitCodeErr{code: 2}
		}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	opts := relevo.BindOptions{
		Name:         f.name,
		PlannerID:    f.planner,
		CWD:          cwd,
		Resume:       f.resume,
		Rebind:       f.rebind,
		RoundTimeout: f.timeout,
		Tier:         f.tier,
		AllowYolo:    f.allowYolo,
		Gate:         f.gate,
		NoGate:       f.noGate,
		Regate:       f.regate,
		Feature:      f.feature,
		Role:         f.role,
	}
	opts.Candidate = f.builder

	adopted := f.resume
	roleName := roleOrBuilder(f.role)
	specRole := roleName
	kind := ""
	switch {
	case f.rebind:
		// A rebind replaces the builder of an existing binding, so the
		// definitions come from the stored binding's actor -- --actor is
		// refused with --resume, so roleName is "builder" here (#382 round 3).
		// If the load fails, today's behaviour (roleName) stands.
		if f.name != "" {
			if existing, err := rt.Store.Load(f.name); err == nil {
				specRole = relevo.BindingRole(existing)
			}
		}
		kind = relevo.CandidateKindFor(rt, opts.Candidate, specRole)
	case adopted:
		if f.name != "" {
			if existing, err := rt.Store.Load(f.name); err == nil {
				kind = existing.Builder.Kind
				specRole = relevo.BindingRole(existing)
			}
		}
	default:
		kind = relevo.CandidateKindFor(rt, opts.Candidate, roleName)
	}

	// Preflight is advisory only: it never blocks the bind, and any probe
	// failure is dropped rather than printed. See bindPreflight.
	if kind != "" {
		env := doctor.NewEnv(rt.Store)
		// The binding's role's definitions for this kind come from the
		// registry, so a config roles that names a custom builder executor
		// preflights that file (#374 §3.4). A Spec error is data: defs stays
		// nil and the preflight falls back to the shipped builder definitions.
		var defs []string
		if spec, err := rt.RoleRegistry().Spec(specRole, kind); err == nil {
			defs = spec.Definitions
		}
		for _, line := range bindPreflightDefs(context.Background(), env, kind, adopted, defs) {
			fmt.Fprintln(os.Stderr, line)
		}
	}

	b, res, err := relevo.BindResolved(context.Background(), rt, opts)
	if err != nil {
		return err
	}
	// A resume keeps the binding's stored role, so the pick note names it.
	roleName = relevo.BindingRole(b)
	if t := relevo.RestoreText(res); t != "" {
		fmt.Println(t)
	}
	if res.WasPaused {
		fmt.Printf("resumed %s after pause\n", b.Name)
	}

	if f.resume && (f.builder != "" || f.rebind) {
		builderDesc := builderWhere(b.Builder)
		if b.BuilderCandidate != "" {
			builderDesc = fmt.Sprintf("%s (%s)", builderWhere(b.Builder), rt.Candidates.NameOf(b.BuilderCandidate))
		}
		fmt.Printf("rebound %s: builder %s, still on round %d\n"+
			"hand it the round with:\n"+
			"  relevo send --name %s --file %s\n",
			b.Name, builderDesc, b.Round, b.Name, rt.Store.PlanPath(b.Name, b.Round))
		noteRegateNoGate(b)
		notePick(rt, roleName, res)
		warnWaitingOnYou(rt, b.Name)
		return nil
	}

	fmt.Printf("bound %s: planner %s -> builder %s (%s), round %d\n",
		b.Name, b.Planner.PaneID, builderWhere(b.Builder), rt.Candidates.NameOf(b.BuilderCandidate), b.Round)
	noteRegateNoGate(b)
	if n := relevo.GatedNote(rt, b.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(rt, roleName, res)
	// Spawn path only: an adopted pane or resumed binding has no fresh name
	// relevo chose, so the note would warn about a name the human did not pick
	// here.
	if !adopted {
		noteConsultRolesTooLong(rt.RoleRegistry(), b.Name)
	}
	warnWaitingOnYou(rt, b.Name)
	return nil
}

// splitFromSource splits --from's SRC[@ROUND] into the source binding name and
// the round. An absent @ROUND returns 0, so fork then uses the --round flag's
// value (its default, exactly as the removed fork verb did).
func splitFromSource(from string) (string, int, error) {
	i := strings.LastIndex(from, "@")
	if i < 0 {
		return from, 0, nil
	}
	n, err := strconv.Atoi(from[i+1:])
	if err != nil {
		return "", 0, fmt.Errorf("relevo bind --from %q: the round after @ must be an integer", from)
	}
	return from[:i], n, nil
}

// runFork is fork's body after parsing (the old cmdFork), reached through
// `bind --from SRC[@ROUND]` (§4.1): the source and round come from --from, and
// --name is the new binding's name.
func runFork(f bindFlags) error {
	source, fromRound, err := splitFromSource(f.from)
	if err != nil {
		return err
	}
	round := fromRound
	if round == 0 {
		round = f.round
	}
	newName := f.name

	if source == "" {
		return fmt.Errorf("usage: relevo bind --from <source>[@<round>] --name NAME [--builder CANDIDATE] [--cwd DIR]%s\n"+
			"bind --from branches a new binding from an earlier round; it needs the source binding name", bindingHint("bind"))
	}

	if round < 1 {
		return fmt.Errorf("relevo bind --from requires --round N (where N >= 1)")
	}
	if newName == "" {
		return fmt.Errorf("relevo bind --from requires --name NAME (the new binding's name)")
	}
	if f.feature != "" {
		if err := store.ValidFeature(f.feature); err != nil {
			fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
			return exitCodeErr{code: 2}
		}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	opts := relevo.ForkOptions{
		Source:    source,
		Round:     round,
		NewName:   newName,
		Candidate: f.builder,
		PlannerID: f.planner,
		CWD:       f.cwd,
		Tier:      f.tier,
		AllowYolo: f.allowYolo,
		Gate:      f.gate,
		NoGate:    f.noGate,
		Regate:    f.regate,
		Feature:   f.feature,
	}

	res, err := relevo.Fork(context.Background(), rt, opts)
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
	if n := relevo.GatedNote(rt, res.Binding.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(rt, "builder", res.Resolution)
	noteConsultRolesTooLong(rt.RoleRegistry(), res.Binding.Name)
	warnWaitingOnYou(rt, res.Binding.Name)

	return nil
}

// runAdd is add's body after parsing (the old cmdAdd), reached through
// `bind --worktree` or one of the placement flags (§4.1).
func runAdd(f bindFlags) error {
	name := f.name
	branch := f.branch
	cwd := f.cwd

	// Before newRuntime, in this order: the flag pair, then a name that is
	// either given or derivable from the branch.
	if branch != "" && cwd != "" {
		fmt.Fprintf(os.Stderr, "relevo: relevo bind --branch and --cwd are exclusive\n")
		return fmt.Errorf("relevo bind --branch and --cwd are exclusive: %w", exitCodeErr{code: 2})
	}
	if name == "" {
		if branch == "" {
			return fmt.Errorf("relevo bind requires --name NAME")
		}
		derived, err := relevo.DefaultBindingName(branch)
		if err != nil {
			return fmt.Errorf("relevo bind --branch %s: cannot derive a binding name (%v); pass --name", branch, err)
		}
		name = derived
	}
	if f.feature != "" {
		if err := store.ValidFeature(f.feature); err != nil {
			fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
			return exitCodeErr{code: 2}
		}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	repo, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	res, err := relevo.Add(context.Background(), rt, relevo.AddOptions{
		Name:      name,
		Candidate: f.builder,
		PlannerID: f.planner,
		Repo:      repo,
		CWD:       cwd,
		Branch:    branch,
		Server:    f.server,
		Base:      f.base,
		Tier:      f.tier,
		AllowYolo: f.allowYolo,
		Gate:      f.gate,
		NoGate:    f.noGate,
		Regate:    f.regate,
		Feature:   f.feature,
		Role:      f.role,
	})
	if err != nil {
		return err
	}

	switch {
	case res.Binding.Builder.Remote():
		fmt.Printf("added %s: builder %s on %s\n", res.Binding.Name, rt.Candidates.NameOf(res.Binding.BuilderCandidate), res.Binding.Builder.Server)
		if res.Binding.Tier != "" {
			fmt.Printf("  tier %s (server)\n", res.Binding.Tier)
		} else {
			fmt.Printf("  tier server's choice (pre-tier server)\n")
		}
	case res.Binding.Builder.Headless():
		fmt.Printf("added %s: builder %s (headless)\n", res.Binding.Name, rt.Candidates.NameOf(res.Binding.BuilderCandidate))
	default:
		fmt.Printf("added %s: builder %s in %s\n",
			res.Binding.Name, rt.Candidates.NameOf(res.Binding.BuilderCandidate), builderWhere(res.Binding.Builder))
	}
	noteRegateNoGate(res.Binding)
	if n := relevo.GatedNote(rt, res.Binding.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(rt, roleOrBuilder(f.role), res.Resolution)
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
	fmt.Printf("  relevo send --name %s --file <plan.md>\n", res.Binding.Name)
	noteConsultRolesTooLong(rt.RoleRegistry(), res.Binding.Name)
	warnWaitingOnYou(rt, res.Binding.Name)

	return nil
}

func cmdUnbind(args []string) error {
	fs := flag.NewFlagSet("unbind", flag.ContinueOnError)
	name := fs.String("name", "", "binding to unbind")
	archive := fs.Bool("archive", false, "move the binding aside instead of deleting it, keeping its round log")
	pickFlag := fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	done := fs.Bool("done", false, "clear every binding the planner marked DONE")
	delete := fs.Bool("delete", false, "with --done: remove each finished binding's directory instead of archiving it")
	dryRun := fs.Bool("dry-run", false, "with --done or --sweep: list what would be cleared, change nothing")
	sweep := fs.Bool("sweep", false, "delete relevo/<name> branches and refs/relevo/<name>/* refs of bindings that no longer exist, once they are on a remote-tracking ref")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *sweep {
		if *done || *delete || *archive || *pickFlag || *name != "" || len(fs.Args()) > 0 {
			fmt.Fprintln(os.Stderr, "relevo: --sweep takes no binding and no other flag except --dry-run")
			return exitCodeErr{code: 2}
		}
		return runSweep(*dryRun)
	}

	// --done is gc, not unbind: it clears every DONE binding, so naming one or
	// asking to pick one contradicts it (§4.3).
	if *done {
		if *name != "" || len(fs.Args()) > 0 || *pickFlag {
			fmt.Fprintln(os.Stderr, "relevo: --done clears every DONE binding; do not also name one or pass --pick")
			return exitCodeErr{code: 2}
		}
		return runGC(*delete, *dryRun)
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		return runPick(pick.Options{Verb: pick.VerbUnbind, Archive: *archive})
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relevo unbind <name> | --pick [--archive]  (or --name <name>)%s\n"+
			"unbind removes a binding; it will not guess which one you meant", bindingHint("unbind"))
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	res, err := relevo.Unbind(context.Background(), rt, target, *archive)
	if err != nil {
		return err
	}

	fmt.Println(relevo.UnbindText(target, res))
	warnWaitingOnYou(rt, target)

	return nil
}

func runSweep(dryRun bool) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	res, err := relevo.SweepRefs(context.Background(), rt, dir, dryRun)
	if err != nil {
		return err
	}

	if len(res.Refs) == 0 {
		fmt.Printf("no relevo refs in %s\n", dir)
		return nil
	}

	for _, line := range relevo.RefLines(res.Refs) {
		fmt.Println(line)
	}

	var n, m int
	for _, r := range res.Refs {
		if (dryRun && r.WouldDelete) || (!dryRun && r.Deleted) {
			n++
		} else if r.Reason != "" {
			m++
		}
	}

	if dryRun {
		fmt.Printf("%d would be deleted, %d kept\n", n, m)
	} else {
		fmt.Printf("%d deleted, %d kept\n", n, m)
	}

	return nil
}

// runGC is gc's body (the old cmdGC), now reached through `unbind --done`
// (§4.3). It clears every binding the planner marked DONE.
func runGC(delete, dryRun bool) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	done, err := relevo.GC(context.Background(), rt, relevo.GCOptions{
		Delete: delete,
		DryRun: dryRun,
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
		case dryRun:
			wtMsg := ""
			if r.WorktreeRemoved != "" {
				wtMsg = fmt.Sprintf(" (worktree %s would be removed)", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				wtMsg = fmt.Sprintf(" (worktree %s kept: %s)", r.WorktreeKept, r.KeptReason)
			} else if r.WorktreeGone != "" {
				wtMsg = fmt.Sprintf(" (worktree %s already gone)", r.WorktreeGone)
			}
			fmt.Printf("would clear %-10s %s (%d rounds)%s\n", r.Name, r.CWD, r.Rounds, wtMsg)
			for _, line := range relevo.RefLines(r.Refs) {
				fmt.Printf("            %s\n", line)
			}
		case r.Archived:
			fmt.Printf("archived    %-10s\n", r.Name)
			if r.WorktreeRemoved != "" {
				fmt.Printf("            removed worktree %s\n", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				fmt.Printf("            kept worktree %s (%s)\n              remove by hand: git -C %s worktree remove %s\n",
					r.WorktreeKept, r.KeptReason, r.WorktreeKept, r.WorktreeKept)
			} else if r.WorktreeGone != "" {
				fmt.Printf("            worktree %s was already gone\n", r.WorktreeGone)
			}
			for _, line := range relevo.RefLines(r.Refs) {
				fmt.Printf("            %s\n", line)
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
			for _, line := range relevo.RefLines(r.Refs) {
				fmt.Printf("            %s\n", line)
			}
		}
	}

	if dryRun {
		fmt.Printf("\n%d binding(s) would be cleared; re-run without --dry-run\n", len(done))
	}

	return nil
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	file := fs.String("file", "", "path to the plan file to hand the builder")
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	tier := fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then policy tier.<role>, then harness)")
	builder := fs.String("builder", "", "candidate name or harness/provider/model token to run this round and later ones on; refused while a round is open")
	allowYolo := fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	dryRun := fs.Bool("dry-run", false, "check every precondition and print what send would do, without sending")
	regate := fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: config policy gate.regate)")
	verify := fs.Bool("verify", false, "run a read-only reviewer in a throwaway worktree when the round closes")
	noVerify := fs.Bool("no-verify", false, "do not run a reviewer when the round closes (default: config policy verify.default)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// Before newRuntime, like add's flag pair: the refusal must not depend on
	// argv order and must touch neither the state directory nor a harness.
	if *verify && *noVerify {
		fmt.Fprintf(os.Stderr, "relevo: relevo send --verify and --no-verify are exclusive\n")
		return fmt.Errorf("relevo send --verify and --no-verify are exclusive: %w", exitCodeErr{code: 2})
	}
	if *file == "" {
		return fmt.Errorf("relevo send requires --file")
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

	opts := relevo.SendOptions{
		Tier:      *tier,
		AllowYolo: *allowYolo,
		Builder:   *builder,
		Regate:    regateOpt,
		Verify:    verifyOpt,
	}

	if *dryRun {
		d, err := relevo.SendDryRun(context.Background(), rt, target, *file, opts)
		if err != nil {
			return err
		}
		fmt.Print(relevo.RenderDryRun(d))
		return nil
	}

	res, err := relevo.Send(context.Background(), rt, target, *file, opts)
	if err != nil {
		return err
	}

	if res.Pick != "" {
		fmt.Println(res.Pick)
	}
	if res.Drift != "" {
		fmt.Println(res.Drift)
	}
	fmt.Printf("sent round %d to %s's builder\n", res.Round, target)
	warnWaitingOnYou(rt, target)
	return nil
}

// askFlagValues holds the pointers ask's flags parse into. askFlagSet defines
// them on fs; cmdAsk and TestAskFlagsHaveActorNotRole read the same surface
// (A2 round 3 S1).
type askFlagValues struct {
	actor       *string
	cand        *string
	file        *string
	question    *string
	round       *int
	nameFlag    *string
	plannerFlag *string
}

// askFlagSet defines ask's flags on fs and returns the values they parse into.
func askFlagSet(fs *flag.FlagSet) *askFlagValues {
	v := &askFlagValues{}
	v.actor = fs.String("actor", "", "the reader actor to consult: reviewer, researcher, or a reader actor in config actors")
	v.cand = fs.String("candidate", "", "candidate name or harness/provider/model token; omit to take the first ungated in config policy order[<role>]")
	v.file = fs.String("file", "", "file containing the question")
	v.question = fs.String("question", "", "the question itself; with --round, exactly one of --file and -q")
	fs.StringVar(v.question, "q", "", "the question itself (shorthand for --question)")
	v.round = fs.Int("round", 0, "ask the builder that built this closed round: resumes its session, headless and read-only")
	v.nameFlag = fs.String("name", "", "binding name")
	v.plannerFlag = fs.String("planner", "", "act as this planner (id or name; default: $RELEVO_PLANNER, else this session's host)")
	return v
}

func cmdAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	v := askFlagSet(fs)
	role, cand, file, question, round := v.actor, v.cand, v.file, v.question, v.round
	nameFlag, plannerFlag := v.nameFlag, v.plannerFlag
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *round > 0 {
		// The round's recorded session fixes the actor and the candidate, so
		// neither is required; --actor is only worth a note.
		if *role != "" {
			fmt.Fprintln(os.Stderr, "note: relevo ask --round ignores --actor; the resumed session fixes the actor")
		}
		if (*file != "") == (*question != "") {
			return fmt.Errorf("relevo ask --round needs --file or -q")
		}
	} else {
		if *role == "" {
			return fmt.Errorf("relevo ask needs --actor ACTOR")
		}
		if *file == "" {
			return fmt.Errorf("relevo ask needs --file PATH")
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

	res, err := relevo.Ask(context.Background(), rt, relevo.AskOptions{
		Role:      *role,
		Candidate: *cand,
		File:      *file,
		Question:  *question,
		Round:     *round,
		Name:      name,
		PlannerID: *plannerFlag,
	})
	if err != nil {
		return err
	}

	if *round > 0 {
		fmt.Printf("asked round %d's builder (%s session %s) on %s (pid %d)\nfindings: %s\n",
			*round, res.Consult.Endpoint.Kind, res.Consult.Endpoint.SessionID, res.Binding,
			res.Consult.Endpoint.PID, relevo.FindingsCommand(res.Binding, res.Consult.Round, res.Consult.ID))
		return nil
	}

	fmt.Printf("asked %s consult %s on %s (pid %d)\nfindings: %s\n",
		res.Consult.Role, res.Consult.ID, res.Binding, res.Consult.Endpoint.PID,
		relevo.FindingsCommand(res.Binding, res.Consult.Round, res.Consult.ID))
	if n := relevo.GatedNote(rt, res.Candidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(rt, *role, res.Resolution)
	return nil
}

// printDiff is diff's body (the old cmdDiff), shared by `show --diff` and
// `show --drift` (§4.2): the output is byte-identical to the removed diff verb
// for the same arguments, including the #143 .viewed stamp.
// round 0 means diff's default: the newest completed round, or the open round
// with drift.
func printDiff(rt relevo.Runtime, name string, round int, stat, drift, anchors bool) error {
	b, err := rt.Store.Load(name)
	if err != nil {
		return err
	}

	targetRound := round
	if targetRound == 0 {
		if drift {
			targetRound = b.Round
		} else {
			targetRound = b.Round - 1
		}
	}
	if targetRound < 1 {
		return fmt.Errorf("binding %q has no completed round yet", name)
	}

	if stat {
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return err
		}
		targetKind := store.KindDiff
		if drift {
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
			if drift {
				return fmt.Errorf("no drift recorded for round %d of %q (--drift)", targetRound, name)
			}
			return fmt.Errorf("no diff recorded for round %d of %q", targetRound, name)
		}
		fmt.Println(found.Note)
		// #143: a successful print is what "viewed" means; the stamp is
		// best-effort and must never fail a read command.
		_ = rt.Store.MarkViewed(name, time.Now())
		return nil
	}

	var patch []byte
	var ok bool
	if drift {
		patch, ok, err = relevo.ReadDrift(rt, name, targetRound)
	} else {
		patch, ok, err = relevo.ReadDiff(rt, name, targetRound)
	}
	if err != nil {
		return err
	}
	if !ok {
		if drift {
			return fmt.Errorf("no drift recorded for round %d of %q (--drift)", targetRound, name)
		}
		return fmt.Errorf("no diff recorded for round %d of %q", targetRound, name)
	}

	if anchors {
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
	_ = rt.Store.MarkViewed(name, time.Now())
	return nil
}

// filterReport narrows a status report to one binding. An empty name keeps
// every row, because listing them all is what a bare `relevo status` is for.
//
// An unknown name is an error rather than an empty report: a silent blank
// would read exactly like a healthy binding with nothing outstanding.
// scopeReport decides what a status-shaped command shows. A named binding is
// always shown, DONE or not: asking for one by name is already a request for
// that specific thing. Otherwise DONE rows are hidden unless --all, and the
// same rule applies to --json so the two formats never disagree about what
// exists. It is a pure function so the rule can be tested without a harness.
func scopeReport(rep relevo.Report, name string, all bool) relevo.Report {
	if name != "" || all {
		return rep
	}
	return relevo.HideDone(rep)
}

func filterReport(rep relevo.Report, name string) (relevo.Report, error) {
	if name == "" {
		return rep, nil
	}
	for _, b := range rep.Bindings {
		if b.Name == name {
			return relevo.Report{Bindings: []relevo.BindingStatus{b}}, nil
		}
	}
	return relevo.Report{}, fmt.Errorf("no binding named %q", name)
}

// filterReportPlanner narrows a status report to one planner's bindings. It
// is a pure function so the rule is testable without a harness. An empty
// planner id keeps every row, which is what a runtime with no registry gets.
func filterReportPlanner(rep relevo.Report, plannerID string) relevo.Report {
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
	all := fs.Bool("all", false, "include bindings marked DONE (hidden by default; relevo unbind --done clears them)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	name := fs.String("name", "", "show only this binding (default: all)")
	line := fs.Bool("line", false, "this planner's builders, one row each, for Claude Code's statusLine setting; with --json, output as JSON")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// --line is today's statusline: one row per builder of the calling
	// planner, so it takes no binding and no other output mode (§4.5).
	if *line {
		if *all || *name != "" || len(fs.Args()) > 0 {
			fmt.Fprintln(os.Stderr, "relevo: --line cannot be combined with --all/--name")
			return exitCodeErr{code: 2}
		}
		return runStatusline(*asJSON)
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
		if _, serr := relevo.SyncRemote(context.Background(), rt); serr != nil {
			fmt.Fprintf(os.Stderr, "relevo: sync remote bindings: %v\n", serr)
		}
	}
	rep, err := relevo.Status(context.Background(), rt)
	if err != nil {
		return err
	}

	// §3.3: a bare `relevo status` shows the calling planner's bindings. A
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

	// #386: the planner's chat label is computed here, in the command a
	// person ran, and only printed. internal/relevo.Status leaves it empty,
	// so no label is ever computed on, or sent to, a server.
	annotatePlannerChat(rt, &rep, chatResolver())

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	// #293: one line above the rows, only when the daemon's cached check has
	// seen a newer release. Read through the doctor's own Env so `status` and
	// `doctor` can never disagree about the same file. JSON output above stays
	// notice-free.
	env := doctor.NewEnv(rt.Store, releaseInputs())
	running, latest, ok, kind := env.ReleaseState()
	if notice := statusNotice(running, latest, ok, kind); notice != "" {
		fmt.Println(notice)
	}
	// #370: one line above the rows only when a daemon restart right now would
	// kill a process running outside its own scope. The same computation
	// doctor's restart row makes -- cheap, one small file read per running
	// process. JSON output above stays notice-free.
	if notice := restartNotice(restartCheck(rt)); notice != "" {
		fmt.Println(notice)
	}

	// #371: the daemon's own version state, read from its record rather than
	// probed. A read error prints nothing: a status must never fail because a
	// sidecar file could not be read.
	if daemonRunning, derr := rt.Store.DaemonRunning(); derr == nil {
		if info, iok, ierr := rt.Store.ReadDaemonInfo(); ierr == nil {
			if notice := daemonNotice(buildVersion(), info, iok, daemonRunning); notice != "" {
				fmt.Println(notice)
			}
		}
	}

	fmt.Print(relevo.RenderStatus(rep))
	return nil
}

// runStatusline is statusline's body (the old cmdStatusline), now reached
// through `status --line` (§4.5): the same output, COLUMNS,
// RELEVO_STATUSLINE_MARGIN and planner filtering. With asJSON, it prints
// StatusLineDoc as JSON.
func runStatusline(asJSON bool) error {
	if fi, err := os.Stdin.Stat(); err != nil || relevo.ShouldDrainStdin(fi.Mode()) {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	if !asJSON {
		columns, err := strconv.Atoi(os.Getenv("COLUMNS"))
		if err != nil || columns <= 0 {
			columns = 0
		}
		columns = relevo.StatusLineWidth(columns, os.Getenv("RELEVO_STATUSLINE_MARGIN"))
		rt, err := newRuntime()
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
			return nil
		}
		// §3.3: the row set is the calling planner's bindings. A session with no
		// planner renders nothing, the same as no planner did
		// before #303.
		rec, ok := plannerFilter(rt)
		if !ok {
			return nil
		}
		// #386: the first line names the planner, so each terminal shows which
		// planner it is. It is printed before PlannerStatus and survives a
		// PlannerStatus failure: the line is the planner's identity, not a
		// binding row.
		fmt.Print(relevo.RenderPlannerLine(rec.Name, columns))
		rep, err := relevo.PlannerStatus(context.Background(), rt, rec.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
			return nil
		}
		fmt.Print(relevo.RenderStatusLine(rep, rt.Now(), columns))
		return nil
	}

	rt, err := newRuntime()
	if err != nil {
		doc := relevo.StatusLineDoc{Now: time.Now().UTC(), Rows: []relevo.StatusLineRow{}}
		data, _ := json.Marshal(doc)
		fmt.Println(string(data))
		return nil
	}
	rec, ok := plannerFilter(rt)
	now := rt.Now().UTC()
	doc := relevo.StatusLineDoc{Now: now, Rows: []relevo.StatusLineRow{}}
	if ok {
		doc.Planner = &relevo.StatusLinePlanner{ID: rec.ID, Name: rec.Name}
		rep, err := relevo.PlannerStatus(context.Background(), rt, rec.ID)
		if err == nil {
			doc.Rows = relevo.StatusLineRows(rep, rt.Now())
		} else {
			fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
		}
	}
	data, _ := json.Marshal(doc)
	fmt.Println(string(data))
	return nil
}

// printLog is the log body after flag parsing and newRuntime(): it prints
// name's entries, applying the --round filter, JSON-encoded when asJSON is
// set, and follows new entries until the binding is DONE or removed when
// follow is set. markViewed guards the #143 .viewed stamp: `relevo show --log`
// stamps and the read-only `relevo serve log` must not, because the stamp is
// the owner's, not the admin's.
func printLog(rt relevo.Runtime, name string, round, after int, asJSON, follow, markViewed bool) error {
	// A binding that does not exist is named at once, the way every other
	// command reports it; only a binding that disappears mid-follow (below)
	// ends the loop quietly.
	if _, err := rt.Store.Load(name); err != nil {
		return err
	}

	// emit applies the --round filter, so the initial batch and every
	// followed entry render identically.
	emit := func(e store.LogEntry) {
		if round != 0 && e.Round != round {
			return
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(e)
			return
		}
		fmt.Println(relevo.LogLine(e))
	}

	entries, err := rt.Store.ReadLogAfter(name, after)
	if err != nil {
		return err
	}
	last := after
	for _, e := range entries {
		emit(e)
		last = e.Seq
	}

	if !follow {
		// #143: a successful print is what "viewed" means; the stamp is
		// best-effort and must never fail a read command.
		if markViewed {
			_ = rt.Store.MarkViewed(name, time.Now())
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relevo.FollowLog(ctx, rt, name, last, time.Second, emit); err != nil {
		if ctx.Err() != nil {
			// Interrupted: what was already printed is the answer, and the
			// stamp is #143's the same as any other exit.
			if markViewed {
				_ = rt.Store.MarkViewed(name, time.Now())
			}
			return nil
		}
		return err
	}
	if markViewed {
		_ = rt.Store.MarkViewed(name, time.Now())
	}
	return nil
}

// cmdWait blocks until a round closes or needs a human, per spec
// docs/specs/2026-09-14-wait-and-waiting-on-you-design.md §4.8. It reads
// relevo's own state only: it launches nothing.
func cmdWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	anyFlag := fs.Bool("any", false, "wait on every named binding; the first to close or need you wins, its name printed first")
	round := fs.Int("round", 0, "round to wait on (default: the newest round sent; an earlier round answers from the log)")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait before giving up")
	peek := fs.Bool("peek", false, "print the outcome line only: do not deliver the pending report")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("relevo wait --timeout must be positive")
	}
	if *round < 0 {
		return fmt.Errorf("relevo wait --round must be >= 0")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	var names []string
	if *anyFlag {
		if *name != "" || len(fs.Args()) == 0 {
			return fmt.Errorf("usage: relevo wait --any NAME [NAME...]  (--any takes one or more positional names, not --name)")
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

	resName, res, err := relevo.Wait(ctx, rt, relevo.WaitOptions{
		Names: names, Round: *round, Timeout: *timeout, Interval: time.Second, Peek: *peek,
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
	// §4.1: after the outcome line, a blank line and the pending payload --
	// the same text `pull` printed -- for every exit but the timeout and a
	// gone binding. A delivery failure is printed and never changes the exit
	// code (§6).
	if res.DeliverErr != nil {
		fmt.Fprintf(os.Stderr, "relevo wait: round %d not delivered, it stays pending (the next relevo wait prints it): %v\n", res.Round, res.DeliverErr)
	}
	if res.Payload != "" {
		fmt.Println()
		fmt.Println(res.Payload)
	}
	if res.Code == 0 {
		return nil
	}
	return exitCodeErr{res.Code}
}

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	interval := fs.Duration("interval", 0, "refresh interval")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// A positional `:view` is refused with exit 2 before any runtime is
	// built, so nothing touches the state directory (round 3, step 3.2).
	start, err := uiStart(fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitCodeErr{code: 2}
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

	// Preferences live in the machine database rt.DB holds once openDB
	// succeeded; a failed open leaves them disabled, as an empty PrefsPath did
	// (P3b plan §4.4).
	var prefsKV db.KV
	if rt.DB != nil {
		prefsKV = rt.DB
	}

	return ui.Run(ctx, rt, ui.Options{
		Interval: *interval,
		Prefs: ui.PrefsStore{
			KV:         prefsKV,
			Key:        "ui",
			LegacyPath: filepath.Join(root, "ui.json"),
		},
		Notice:  notice,
		Start:   start,
		Version: buildVersion(),
	})
}

// uiStart maps `relevo ui`'s positional args to the shell's start command
// (round 3, step 3.2): an empty list starts at :fleet; otherwise the first
// arg names a view after ':' and every arg is joined into its command line.
// Pure, so a cmd test covers it without running the ui, which needs a
// terminal.
func uiStart(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if !strings.HasPrefix(args[0], ":") {
		return "", fmt.Errorf("relevo ui: want :<view> (e.g. relevo ui :rounds), got %q", args[0])
	}
	parts := append([]string{args[0][1:]}, args[1:]...)
	return strings.Join(parts, " "), nil
}

// runPick opens the interactive picker for one verb (#15). The three
// non-zero outcomes have already been shown on screen, so they exit 1
// silently: a second "relevo: ..." line would go to the plugin log, not to
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
		return fmt.Errorf("usage: relevo done <name> | --pick  (or --name <name>)%s\n"+
			"done stops relaying for a binding; it will not guess which one you meant",
			bindingHint("done"))
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	res, err := relevo.Done(context.Background(), rt, target)
	if err != nil && !errors.Is(err, relevo.ErrStopFailed) {
		return err
	}

	fmt.Println(relevo.DoneText(target, res))
	if err != nil {
		return err
	}
	warnWaitingOnYou(rt, target)
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
		return fmt.Errorf("usage: relevo stop <name> | --name <name>\n" +
			"stop kills the builder process and closes its round; it must not guess")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	res, err := relevo.Stop(context.Background(), rt, target, relevo.StopOptions{})
	if errors.Is(err, relevo.ErrNothingToStop) {
		// Nothing to stop is an answer, not a failure.
		fmt.Printf("nothing to stop: %s has no open round\n", target)
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Println(relevo.StopText(target, res))
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
		return fmt.Errorf("usage: relevo land <name> | --name <name> [--onto <ref>] [--pr] [--no-gate] [--force] [--merge]\n" +
			"land rebases the binding's branch onto its base, runs the gate and pushes it; it must not guess which binding")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	res, err := relevo.Land(context.Background(), rt, target, relevo.LandOptions{
		Onto:   *onto,
		PR:     *pr,
		NoGate: *noGate,
		Force:  *force,
		Merge:  *merge,
	})
	if err != nil {
		return landFailure(target, err)
	}

	fmt.Println(relevo.LandText(res))
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
	case errors.Is(err, relevo.ErrLandConflict):
		fmt.Fprintf(os.Stderr, "relevo: land %s: %v\n", target, err)
		return exitCodeErr{code: 3}
	case errors.Is(err, relevo.ErrLandDirty), errors.Is(err, relevo.ErrLandGate):
		fmt.Fprintf(os.Stderr, "relevo: land %s: %v\n", target, err)
		return exitCodeErr{code: 2}
	default:
		return err
	}
}

// explicitBinding takes the binding from --name or a single positional, and
// never from the current directory. done is destructive and not undoable
// except through `relevo bind --resume`, so a bare invocation must fail rather
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

	return fmt.Sprintf("\nthis directory is bound as %q, so you probably want: relevo %s %s", b.Name, verb, b.Name)
}

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	interval := fs.Duration("interval", 2*time.Second, "poll interval")
	check := fs.Bool("check", false, "exit 0 if a daemon is running, 1 if not; print nothing")
	// --preflight is internal: the daemon runs a candidate binary's own
	// --preflight before re-exec'ing into it (#371 §4.4). It stays out of
	// the usage text and the README, so it is defined but not printed.
	preflight := fs.Bool("preflight", false, "validate the runtime configuration and exit (internal)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relevo daemon [--interval D] [--check]")
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	var (
		rt  relevo.Runtime
		err error
	)
	if *preflight || *check {
		// §4.6: --preflight and --check never write. They read config files
		// and a database read-only, and open nothing that migrates.
		rt, err = newRuntimePeek()
	} else {
		rt, err = newRuntime()
	}
	if err != nil {
		if *preflight {
			// §4.4: the error on stderr, exit 1. Returning rather than
			// os.Exit keeps the path a plain function call a test can make.
			fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
			return exitCodeErr{code: 1}
		}
		return err
	}
	if *preflight {
		// newRuntime read and validated config only: it took no lock,
		// opened no DB (which would migrate), started no process and made no
		// network call, and this returns before the DB open below.
		fmt.Printf("ok %s\n", buildVersion())
		return nil
	}

	// Nowhere else: a CLI one-shot (any other command) must not relaunch a
	// builder it merely happens to observe as "exited, code unknown" (#244).
	rt.StartedAt = time.Now()
	// The daemon's in-memory "seen alive" set (#370, spec §4.2): it tells a
	// process this daemon actually watched from one that merely predates it,
	// so a restart relaunches only what it really took down. Nothing else
	// creates one; every CLI one-shot leaves Watched nil and keeps #244's
	// rule exactly.
	rt.Watched = relevo.NewWatched()
	// The daemon's per-binding auth grace (#373 §3): a transient 401 (clock
	// skew after a reboot) is shown and warned about, and only halts a binding
	// after 15 minutes. Every CLI one-shot leaves AuthGrace nil, so it never
	// halts on one.
	rt.AuthGrace = relevo.NewAuthGrace()
	// The scope template newRuntime filled is logged once here, in the same
	// shape `relevo serve` uses (#295). "off" is scope.enabled: false; the
	// local daemon does not probe at startup, so there is no "unavailable"
	// here -- a later probe failure is the runner's one-line warning (§3.1).
	slog.Info(fmt.Sprintf("scopes=%s", scopeStatusText(rt.Scope)))

	// The reaper is built before anything can spawn a child, so it sees only
	// processes inherited from the previous image across a re-exec. A child
	// started after this point is never in its set (#371 §4.6).
	reaper := proc.NewInheritedReaper(os.Getpid(), "/proc")

	// Capture the executable and its identity once, before any replacement
	// can land. A failure disables re-exec with a warning; the daemon runs on
	// regardless (#371 §4.7, §6).
	exe, exeErr := upgrade.ResolveExe()
	exeID, idErr := upgrade.ExeIdentity(exe)
	reexecOK := exeErr == nil && idErr == nil
	if !reexecOK {
		reason := exeErr
		if reason == nil {
			reason = idErr
		}
		slog.Warn("re-exec disabled", "err", reason)
	}

	// The database is opened only after --check has returned and after
	// AcquireDaemonLock (#372 §4.5): the plugin's --check probe must not
	// create or migrate the db, and only the lock holder writes it.
	// `relevo serve`'s state root is its own and stays out of scope.
	configDir, err := userConfigRoot()
	if err != nil {
		return err
	}
	configDirPath := filepath.Join(configDir, "relevo")
	// rt.Config is nil on the --check / --preflight peek path, which never
	// refreshes. A daemon that opened a real store hands the watcher a
	// labelled copy, so the import it runs is recorded as source "import".
	var configSource relevo.ConfigSource
	if rt.Config != nil {
		configSource = rt.Config.As("import", "imported "+configDirPath)
	}
	watcher := relevo.NewConfigWatcher(configSource, configDirPath, os.Getenv)

	// --check is the plugin startup hook's probe. It prints nothing on either
	// path: the exit status is the whole answer, and a hook that printed would
	// only fill a log with noise on every server start. Returning exitCodeErr
	// rather than calling os.Exit keeps the no-db-yet ordering a test can call
	// (#372 §4.5): main maps the code to the same exit status.
	if *check {
		running, err := rt.Store.DaemonRunning()
		if err != nil {
			return err
		}
		if !running {
			return exitCodeErr{code: 1}
		}
		return nil
	}

	lock, err := rt.Store.AcquireDaemonLock()
	if err != nil {
		return err
	}
	// DaemonLock.Close is already idempotent (it nils its file), so the
	// explicit close on the re-exec path and this defer cannot double-close
	// into an error that matters.
	defer lock.Close()

	// The database is opened only here (and by `relevo db *`): the daemon is
	// the process that writes it every tick. An open failure never blocks the
	// daemon from starting -- every ingest call site treats DB == nil like a
	// machine with no database.
	//
	// Closing is explicit rather than a bare defer: the re-exec path closes
	// the DB itself before syscall.Exec, and the deferred pass must then do
	// nothing. *db.DB.Close is not idempotent.
	var dbClosed bool
	closeDB := func() {
		if dbClosed || rt.DB == nil {
			return
		}
		dbClosed = true
		if cerr := rt.DB.Close(); cerr != nil {
			slog.Warn("relevo daemon: close db", "err", cerr)
		}
	}
	defer closeDB()

	if d, derr := openDB(rt.Store.DBPath()); derr != nil {
		slog.Warn("relevo daemon: db unavailable; ingest disabled", "err", derr)
	} else if d.Newer() {
		// A schema a newer relevo wrote is never migrated or written by this
		// binary: leave rt.DB nil so every ingest call site treats it as a
		// machine with no database, and say why once (#372 §4.5).
		have, know := d.SchemaVersions()
		slog.Warn(fmt.Sprintf("relevo.db schema v%d is newer than this relevo (v%d); ingest paused until relevo is upgraded", have, know))
		_ = d.Close()
	} else {
		rt.DB = d
	}

	// The ingest mirror's proven duplicates are removed once, before daemon.json
	// is written (D3c; v2 #476). The run backs the database up first and records
	// itself in the kv table, so every later start is a no-op, and a failure
	// never stops the daemon: a machine with no database, or one where the
	// backup could not be written, still runs and retries on the next start.
	if rt.DB != nil {
		// The rename substitutions let the stream-lines proof recognise rows
		// whose stored paths predate a cutover. Without them those rows are
		// simply kept, which is the safe direction, so an unavailable roots
		// resolution is a warning, not a failure.
		roots, rerr := renameRoots()
		var renames []legacy.Prefix
		if rerr != nil {
			slog.Warn("relevo daemon: mirror dedupe: rename roots unavailable; renamed rows are kept", "err", rerr)
		} else {
			renames = roots.Prefixes()
		}
		stats, ran, derr := ingest.DedupeMirrorOnce(rt.DB, filepath.Dir(rt.Store.DBPath()), renames, time.Now())
		if derr != nil {
			slog.Warn("relevo daemon: mirror dedupe skipped", "err", derr)
		} else if ran {
			slog.Info("relevo daemon: mirror dedupe",
				"done_at", stats.DoneAt,
				"mirror_bindings", stats.MirrorBindings,
				"unmapped", stats.Unmapped,
				"artifacts_deleted", stats.ArtifactsDeleted,
				"artifacts_kept", stats.ArtifactsKept,
				"transcript_rounds_deleted", stats.TranscriptRoundsDeleted,
				"transcript_rows_deleted", stats.TranscriptRowsDeleted,
				"transcript_rounds_kept", stats.TranscriptRoundsKept,
				"transcript_rounds_by_stream_lines", stats.TranscriptRoundsByStreamLines,
				"transcript_rows_renamed", stats.TranscriptRowsRenamed,
				"backup_path", stats.BackupPath,
				"vacuum_err", stats.VacuumErr)
		}
	}

	// daemon.json: what this image runs. Written under the lock, so its
	// presence with the lock held means a #371 daemon; removed on a clean
	// shutdown, kept across a re-exec (#371 §4.7).
	info := store.DaemonInfo{
		Version:    buildVersion(),
		PID:        os.Getpid(),
		StartedAt:  time.Now(),
		Exe:        exe,
		ExeID:      exeID,
		ReexecFrom: os.Getenv("RELEVO_REEXEC_FROM"),
	}
	if werr := rt.Store.WriteDaemonInfo(info); werr != nil {
		slog.Warn("relevo daemon: daemon.json not written", "err", werr)
	}
	if info.ReexecFrom != "" {
		slog.Info("re-exec'd", "from", info.ReexecFrom, "to", info.Version)
	}

	// Role definitions are refreshed once per image start, after the lock and
	// daemon.json (#371 §4.10): an upgrade leaves the files relevo wrote for
	// each harness on disk, and one nobody edited is stale. Never fatal.
	refreshRoles()

	loaded, err := rt.Config.Load()
	if err != nil {
		return err
	}
	hooksCfg, err := resolveHooksConfig(loaded.Hooks, hooksRunLog(rt.Store))
	if err != nil {
		return err
	}
	rt.Hooks = newHooksDispatcher(hooksCfg, rt.Policy)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The watcher and its hook are the daemon's whole upgrade decision
	// (#371 §4.3): debounce the new identity over two ticks, preflight it,
	// then either re-exec or refuse and record the refusal.
	up := &upgrade.Watcher{
		Path:    exe,
		Started: exeID,
		Stat:    upgrade.ExeIdentity,
		Preflight: func(ctx context.Context, path string) error {
			out, perr := exec.CommandContext(ctx, path, "daemon", "--preflight").CombinedOutput()
			if perr == nil {
				return nil
			}
			if line := firstLine(string(out)); line != "" {
				return errors.New(line)
			}
			return perr
		},
	}

	hook := func(ctx context.Context) bool {
		// Children inherited from the previous image are reaped here, once
		// per tick: nothing else will ever wait for them (#371 §4.6).
		reaper.Reap()
		if !reexecOK {
			return false
		}

		prev := up.Refused()
		decision := up.Check(ctx)
		if !sameFileID(up.Refused(), prev) {
			if up.Refused() == nil {
				info.ReexecFailed = nil
			} else {
				info.ReexecFailed = &store.ReexecFailure{
					ExeID:  *up.Refused(),
					At:     time.Now(),
					Reason: decision.Reason,
				}
			}
			if werr := rt.Store.WriteDaemonInfo(info); werr != nil {
				slog.Warn("relevo daemon: daemon.json not written", "err", werr)
			}
		}

		switch decision.Action {
		case upgrade.Refused:
			// Check reports Refused only when the identity changed, so this
			// is the one warning per refused build.
			slog.Warn("new relevo binary refused", "exe", exe, "reason", decision.Reason)
		case upgrade.Reexec:
			slog.Info("re-exec onto new binary", "exe", exe)
			return true
		}
		return false
	}

	slog.Info("relevo daemon starting", "interval", *interval)
	err = relevo.NewDaemon(rt, *interval).WithRefresh(watcher.Refresh).WithUpgrade(hook).Run(ctx)
	if errors.Is(err, relevo.ErrReexec) {
		// Close explicitly what must not survive the exec -- the DB, the
		// signal context and the lock -- rather than relying on CLOEXEC
		// (#371 §4.7).
		closeDB()
		stop()
		_ = lock.Close()

		env := withEnv(os.Environ(), "RELEVO_REEXEC_FROM", buildVersion())
		execErr := reexec(exe, append([]string{exe}, os.Args[1:]...), env)
		// Only reached when the exec itself failed: a non-zero exit lets
		// systemd's Restart=on-failure start the new binary anyway.
		return fmt.Errorf("re-exec %s: %w", exe, execErr)
	}

	// A clean shutdown removes the record; the re-exec path above must not.
	_ = rt.Store.RemoveDaemonInfo()
	return err
}

// refreshRoles lands relevo's shipped role definitions once per image start
// (#371 §4.10). The kinds are the ones `relevo config agents` picks by default
// -- harness.Install's own "every harness whose binary is on PATH" selection --
// and the env is the same one that verb uses, so both read and write the one
// manifest at <state root>/agents-manifest.json.
//
// It is never fatal: a definition that could not be written is one warning, a
// manifest relevo cannot read or save is one warning, and the daemon's own work
// does not depend on either.
func refreshRoles() {
	env, err := agentInstallEnv()
	if err != nil {
		slog.Warn("role definitions not refreshed", "err", err)
		return
	}

	results, err := harness.Install(env, harness.InstallOptions{})
	if err != nil {
		slog.Warn("role definitions not refreshed", "err", err)
	}

	for _, r := range results {
		switch r.Outcome {
		case harness.OutcomeWrote, harness.OutcomeUpdated:
			slog.Info("role definition refreshed", "kind", r.Kind, "role", r.Role, "path", r.Path)
		case harness.OutcomeKeptDiffers:
			slog.Info(fmt.Sprintf("%s was edited; relevo config agents --force replaces it", r.Path))
		case harness.OutcomeError:
			slog.Warn("role definition not refreshed", "kind", r.Kind, "role", r.Role, "path", r.Path, "err", r.Err)
		}
	}
}

// sameFileID reports whether two identity pointers name the same file. Both
// nil is "no refusal"; one nil is a change.
func sameFileID(a, b *store.FileID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// firstLine is the first non-empty line of s, for a preflight failure's
// reason. It is what daemon.json and the log record, not the whole stderr.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// withEnv sets key=val in env, replacing every existing KEY= entry (collapsing
// duplicates) or appending when none is present. It is how the re-exec passes
// the previous version on without duplicating an inherited value. Pure.
func withEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	set := false
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			if !set {
				out = append(out, prefix+val)
				set = true
			}
			continue
		}
		out = append(out, e)
	}
	if !set {
		out = append(out, prefix+val)
	}
	return out
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
// `relevo send frontend --file p.md` to the CWD's builder instead of frontend's,
// with no error and a round log that recorded it as legitimate.
func resolveBinding(rt relevo.Runtime, nameFlag string, positional []string) (string, error) {
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
		return "", fmt.Errorf("no binding for %s; name one with `relevo <command> NAME` or run relevo bind first", cwd)
	}

	return b.Name, nil
}

// warnWaitingOnYou prints one stderr line per other binding that is waiting
// on a human, per spec §4.9. except is the binding the verb just acted on.
// It never changes the caller's return value or exit code: a WaitingOnYou
// error is itself only a stderr warning.
func warnWaitingOnYou(rt relevo.Runtime, except string) {
	lines, err := relevo.WaitingOnYou(rt, except)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: waiting-on-you check: %v\n", err)
		return
	}
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
}
