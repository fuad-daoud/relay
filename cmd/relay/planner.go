package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/planner"
	"github.com/fuad-daoud/relay/internal/proc"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

// plannerPriorIDTimeout bounds how long `relay planner init` waits for the
// database to open before giving up on reusing a row's id (§4.4 step 3). A
// SessionStart hook must never block a session on sqlite.
const plannerPriorIDTimeout = 2 * time.Second

// cmdPlanner dispatches `relay planner init|list|rename|forget`
// (docs/specs/2026-09-22-drop-herdr-design.md §4.7). It never touches herdr:
// a planner record is relay's own identity, not a pane.
func cmdPlanner(args []string) error {
	const usage = `usage: relay planner init [--name N] [--kind K --session S] [--hook claude]
       relay planner list [--json]
       relay planner rename <id|name> <new-name>
       relay planner forget <id|name>`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "init":
		return cmdPlannerInit(args[1:])
	case "list":
		return cmdPlannerList(args[1:])
	case "rename":
		return cmdPlannerRename(args[1:])
	case "forget":
		return cmdPlannerForget(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relay planner: unknown subcommand %q\n", args[0])
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// plannerRegistry is the CLI's registry: the state root's planners directory
// (store.PlannersDir) on the runtime's clock.
func plannerRegistry(rt relay.Runtime) *planner.FileRegistry {
	return &planner.FileRegistry{Root: rt.Store.PlannersDir(), Now: rt.Now}
}

func cmdPlannerInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	name := fs.String("name", "", "planner name (default: <agent>-<n>, else <kind>-<n>)")
	kind := fs.String("kind", "", "harness kind for an explicit registration (e.g. opencode)")
	session := fs.String("session", "", "harness session id for an explicit registration")
	hook := fs.String("hook", "", "read a Claude Code SessionStart payload from stdin (only \"claude\")")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *hook != "" {
		if *hook != "claude" {
			fmt.Fprintf(os.Stderr, "relay: relay planner init --hook supports only \"claude\", got %q\n", *hook)
			return exitCodeErr{code: 2}
		}
		if *kind != "" || *session != "" {
			fmt.Fprintln(os.Stderr, "relay: relay planner init --hook reads its kind and session from the hook payload, not --kind/--session")
			return exitCodeErr{code: 2}
		}
		return plannerInitHook(*name)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	in := planner.InitInput{Name: *name, CWD: cwd, Now: rt.Now()}

	switch {
	case *kind != "" || *session != "":
		if *kind == "" || *session == "" {
			return fmt.Errorf("relay planner init needs both --kind and --session, or neither")
		}
		in.Kind, in.SessionID = *kind, *session
	default:
		ident, ok := planner.Detect(os.Getenv, os.Getppid())
		if !ok {
			return fmt.Errorf("not in a detectable planner session: pass --kind and --session")
		}
		in.Kind = ident.Kind
		in.SessionID = ident.SessionID
		in.Agent = os.Getenv("CLAUDE_CODE_AGENT")
		in.HostPID = ident.HostPID
		in.HostStartedAt = plannerHostStart(ident.HostPID)
	}

	prior, closePrior := plannerPriorID(rt.Store.DBPath())
	defer closePrior()
	in.PriorID = prior

	rec, res, err := planner.Init(plannerRegistry(rt), in)
	if err != nil {
		return err
	}

	fmt.Printf("planner %s (%s) %s\n", rec.Name, rec.ID, res)
	fmt.Printf("export RELAY_PLANNER=%s\n", rec.ID)
	return nil
}

// plannerInitHook runs `relay planner init --hook claude` (§5.1).
//
// It never returns an error and never exits non-zero: the hook runs at the
// start of a Claude Code session, and blocking that session because relay could
// not read its own state would be a far worse failure than an unregistered
// planner (§4.4, §6.1). Every failure prints HookNote on stdout -- where the
// model reads its context -- and the error on stderr for the human.
func plannerInitHook(nameFlag string) error {
	in, err := planner.ParseHookInput(os.Stdin)
	if err != nil {
		return plannerInitHookFailure(err)
	}

	rt, err := newRuntime()
	if err != nil {
		return plannerInitHookFailure(err)
	}

	// The hook's parent is the Claude Code process, which is the same pid
	// CLAUDE_PID names in a Bash tool and `relay mcp`'s parent (§1.1).
	host := os.Getppid()

	prior, closePrior := plannerPriorID(rt.Store.DBPath())
	defer closePrior()

	rec, _, err := planner.Init(plannerRegistry(rt), planner.InitInput{
		Kind:           "claude",
		SessionID:      in.SessionID,
		TranscriptPath: in.TranscriptPath,
		CWD:            in.CWD,
		Name:           nameFlag,
		Agent:          os.Getenv("CLAUDE_CODE_AGENT"),
		HostPID:        host,
		HostStartedAt:  plannerHostStart(host),
		Now:            rt.Now(),
		PriorID:        prior,
	})
	if err != nil {
		return plannerInitHookFailure(err)
	}

	// The export line is how every later Bash call in the session learns its
	// planner (§3.4). Appending, never truncating: Claude Code reads the whole
	// file, and it may already carry lines from other tools.
	//
	// With no $CLAUDE_ENV_FILE there is nowhere to export to, so the answer
	// says so in additionalContext instead of pretending the export happened
	// (§3.4): relay then resolves the session through the host process.
	out := planner.HookOutput(rec)
	if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" {
		if err := appendEnvLine(envFile, planner.EnvLine(rec.ID)); err != nil {
			return plannerInitHookFailure(err)
		}
	} else {
		out = planner.HookOutputNoEnv(rec)
	}

	_, _ = os.Stdout.Write(out)
	return nil
}

// plannerInitHookFailure reports a hook failure and swallows it: the note goes
// to stdout as the hook's answer, the error to stderr for the human, and the
// command still succeeds.
func plannerInitHookFailure(err error) error {
	fmt.Fprintf(os.Stderr, "relay: %v\n", err)
	_, _ = os.Stdout.Write(planner.HookNote(fmt.Sprintf("relay planner init failed: %v", err)))
	return nil
}

// plannerHostStart reads a host process's start time in Unix seconds, the
// pid-reuse defence §3.1 gives host_started_at. A failure reports 0 rather than
// failing the caller: the record is still useful, and §4.3's session step
// resolves the planner when the host cannot be matched.
func plannerHostStart(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	started, err := proc.StartTime(context.Background(), pid)
	if err != nil {
		return 0
	}
	return started.Unix()
}

// plannerPriorID supplies §3.5's "reuse the db row's id". It opens relay.db on
// a goroutine and gives up after plannerPriorIDTimeout, so a hook never waits on
// sqlite; a nil function means "no prior id" and Init mints one.
//
// The returned close function releases the database, and is a no-op when the
// open never finished.
func plannerPriorID(path string) (func(kind, session string) (string, bool), func()) {
	opened := make(chan *db.DB, 1)
	go func() {
		d, err := openDB(path)
		if err != nil {
			opened <- nil
			return
		}
		opened <- d
	}()

	select {
	case d := <-opened:
		if d == nil {
			return nil, func() {}
		}
		return func(kind, session string) (string, bool) {
			p, ok, err := d.PlannerBySession(kind, session)
			if err != nil || !ok {
				return "", false
			}
			return p.ID, true
		}, func() { _ = d.Close() }
	case <-time.After(plannerPriorIDTimeout):
		// The open is still running; close whatever it eventually produces so
		// a slow database does not leak a connection for the process's life.
		go func() {
			if d := <-opened; d != nil {
				_ = d.Close()
			}
		}()
		return nil, func() {}
	}
}

// appendEnvLine appends one line to the file $CLAUDE_ENV_FILE names.
func appendEnvLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	defer f.Close()

	if _, err := io.WriteString(f, line); err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	return nil
}

func cmdPlannerList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the records as a JSON array")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// FileRegistry.List sorts by name, which is the order `relay planner list`
	// documents (§4.7).
	records, err := plannerRegistry(rt).List()
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(records)
	}

	fmt.Printf("%-20s %-16s %-9s %-24s %-8s %s  %s\n",
		"name", "id", "kind", "session", "host", "cwd", "seen")
	for _, rec := range records {
		host := "-"
		if rec.HostPID > 0 {
			host = strconv.Itoa(rec.HostPID)
		}
		fmt.Printf("%-20s %-16s %-9s %-24s %-8s %s  %s\n",
			rec.Name, rec.ID, rec.HarnessKind, rec.SessionID, host, rec.CWD,
			rec.SeenAt.UTC().Format(time.RFC3339))
	}
	return nil
}

func cmdPlannerRename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 2 {
		return fmt.Errorf("usage: relay planner rename <id|name> <new-name>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	reg := plannerRegistry(rt)
	rec, err := plannerLookup(reg, positional[0])
	if err != nil {
		return err
	}

	updated, err := reg.Rename(rec.ID, positional[1])
	if err != nil {
		return err
	}

	fmt.Printf("renamed planner %s (%s) to %s\n", rec.Name, rec.ID, updated.Name)
	return nil
}

func cmdPlannerForget(args []string) error {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 1 {
		return fmt.Errorf("usage: relay planner forget <id|name>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	reg := plannerRegistry(rt)
	rec, err := plannerLookup(reg, positional[0])
	if err != nil {
		return err
	}

	if err := reg.Forget(rec.ID, plannerInUse(rt)); err != nil {
		return err
	}

	fmt.Printf("forgot planner %s (%s)\n", rec.Name, rec.ID)
	return nil
}

// plannerLookup resolves one <id|name> argument the way Resolve resolves a
// --planner value: by id when it has the id shape, else by name.
func plannerLookup(reg planner.Registry, ref string) (planner.Record, error) {
	if planner.ValidID(ref) == nil {
		rec, err := reg.Get(ref)
		switch {
		case err == nil:
			return rec, nil
		case errors.Is(err, planner.ErrNotFound):
			return planner.Record{}, fmt.Errorf("no planner with id %s", ref)
		default:
			return planner.Record{}, err
		}
	}

	rec, err := reg.ByName(ref)
	switch {
	case err == nil:
		return rec, nil
	case errors.Is(err, planner.ErrNotFound):
		return planner.Record{}, fmt.Errorf("no planner named %s", ref)
	default:
		return planner.Record{}, err
	}
}

// plannerInUse is Forget's guard (§4.7): a record any binding that is not DONE
// still names is in use, and forgetting it would strand that binding's
// history. It walks the same store listing `relay status --all` reads.
//
// An unreadable store refuses the forget: deleting a record a live binding may
// name is worse than making the human retry.
func plannerInUse(rt relay.Runtime) func(id string) bool {
	return func(id string) bool {
		bindings, err := rt.Store.List()
		if err != nil {
			return true
		}
		for _, b := range bindings {
			if b.State != store.StateDone && b.PlannerID == id {
				return true
			}
		}
		return false
	}
}
