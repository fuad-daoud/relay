package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// plannerPriorIDTimeout bounds how long `relevo planner init` waits for the
// database to open before giving up on reusing a row's id (§4.4 step 3). A
// SessionStart hook must never block a session on sqlite.
const plannerPriorIDTimeout = 2 * time.Second

// cmdPlanner dispatches `relevo planner init|list|rename|forget`
// (#303 §4.7). It touches no harness:
// a planner record is relevo's own identity, not a pane.
func cmdPlanner(args []string) error {
	const usage = `usage: relevo planner init [--name N] [--kind K --session S] [--hook claude]
       relevo planner list [--json]
       relevo planner rename <id|name> <new-name>
       relevo planner forget <id|name>`

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
	case "prune":
		// §4.4: pruning is automatic now; the verb names that and exits 2
		// like every other removed spelling.
		fmt.Fprintf(os.Stderr, "relevo: %q was removed; the daemon prunes dead planners hourly\n", "prune")
		return exitCodeErr{code: 2}
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relevo planner: unknown subcommand %q\n", args[0])
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// plannerRegistry is the CLI's registry: the state root's database kv rows
// `<root>/relevo.db` (P3b round 2 §4.1) on the runtime's clock, importing the
// pre-database planners directory the first time a record is touched. A store
// whose database cannot be opened is fatal for the verb.
func plannerRegistry(rt relevo.Runtime) (*planner.DBRegistry, error) {
	d, err := rt.Store.DB()
	if err != nil {
		return nil, err
	}
	return &planner.DBRegistry{KV: db.TxKV{DB: d}, Root: rt.Store.PlannersDir(), Now: rt.Now}, nil
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
			fmt.Fprintf(os.Stderr, "relevo: relevo planner init --hook supports only \"claude\", got %q\n", *hook)
			return exitCodeErr{code: 2}
		}
		if *kind != "" || *session != "" {
			fmt.Fprintln(os.Stderr, "relevo: relevo planner init --hook reads its kind and session from the hook payload, not --kind/--session")
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
			return fmt.Errorf("relevo planner init needs both --kind and --session, or neither")
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

	reg, err := plannerRegistry(rt)
	if err != nil {
		return err
	}
	rec, res, err := planner.Init(reg, in)
	if err != nil {
		return err
	}

	fmt.Printf("planner %s (%s) %s\n", rec.Name, rec.ID, res)
	fmt.Printf("export RELEVO_PLANNER=%s\n", rec.ID)
	return nil
}

// plannerInitHook runs `relevo planner init --hook claude` (§5.1).
//
// It never returns an error and never exits non-zero: the hook runs at the
// start of a Claude Code session, and blocking that session because relevo could
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
	// CLAUDE_PID names in a Bash tool and `relevo mcp`'s parent (§1.1).
	host := os.Getppid()

	prior, closePrior := plannerPriorID(rt.Store.DBPath())
	defer closePrior()

	reg, err := plannerRegistry(rt)
	if err != nil {
		return plannerInitHookFailure(err)
	}
	rec, _, err := planner.Init(reg, planner.InitInput{
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
	// (§3.4): relevo then resolves the session through the host process.
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
	fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
	_, _ = os.Stdout.Write(planner.HookNote(fmt.Sprintf("relevo planner init failed: %v", err)))
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

// plannerPriorID supplies §3.5's "reuse the db row's id". It opens relevo.db on
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

	// DBRegistry.List sorts by name, which is the order `relevo planner list`
	// documents (§4.7).
	reg, err := plannerRegistry(rt)
	if err != nil {
		return err
	}
	records, err := reg.List()
	if err != nil {
		return err
	}

	// The count of non-DONE bindings naming each planner, from the same store
	// walk forget's guard uses. Both it and the state column are derived here,
	// not stored on the record: the file is identity, and this is the
	// machine's present answer about it.
	counts, err := plannerBindingCounts(rt)
	if err != nil {
		return err
	}

	// The chat column (#386): what the harness itself calls each session, read
	// from the harness's own files now and never stored on the record.
	res := chatResolver()
	labels := make([]chatlabel.Label, len(records))
	for i, rec := range records {
		labels[i] = res.Resolve(context.Background(), rec.HarnessKind, rec.SessionID, rec.TranscriptLocator)
	}

	if *asJSON {
		views := make([]plannerListView, 0, len(records))
		for i, rec := range records {
			views = append(views, plannerListView{
				Record:    rec,
				State:     string(planner.RecordState(rec, rt.ProcStart)),
				Bindings:  counts[rec.ID],
				ChatLabel: labels[i].Text,
				ChatLink:  labels[i].Link,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(views)
	}

	// tabwriter, not fixed widths: a 36-character session id or a long cwd
	// used to overflow its cell and shift every column after it.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "name\tchat\tid\tkind\tsession\thost pid\tstate\tbindings\tcwd\tseen")
	for i, rec := range records {
		host := "-"
		if rec.HostPID > 0 {
			host = strconv.Itoa(rec.HostPID)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			rec.Name, labels[i].String(), rec.ID, rec.HarnessKind, rec.SessionID, host,
			planner.RecordState(rec, rt.ProcStart), counts[rec.ID], rec.CWD,
			rec.SeenAt.UTC().Format(time.RFC3339))
	}
	return w.Flush()
}

// plannerListView is one record as `relevo planner list --json` renders it: the
// record's own fields, flattened by the embedded struct, plus the two derived
// columns the table shows.
type plannerListView struct {
	planner.Record
	State    string `json:"state"`
	Bindings int    `json:"bindings"`
	// ChatLabel and ChatLink are the harness's own name for the session
	// (#386), read when the command runs and never stored.
	ChatLabel string `json:"chat_label,omitempty"`
	ChatLink  string `json:"chat_link,omitempty"`
}

// chatResolver is the label reader `relevo planner list` uses (#386): a claude
// transcript is read directly, and an opencode session title is read through
// the sqlite3 shell-out, which is wired only when sqlite3 is on PATH.
func chatResolver() chatlabel.Resolver {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return chatlabel.Resolver{}
	}
	return chatlabel.Resolver{Exec: binExec{}, OpencodeDB: opencodeDBPath()}
}

// annotatePlannerChat fills each binding row's PlannerChatLabel and
// PlannerChatLink (#386) from the planner record the row names. It runs only
// from cmd/relevo, inside the command a person ran, and fills only fields that
// are printed: internal/relevo.Status itself never computes a label, because it
// also serves relevo serve. The label is resolved at most once per planner id,
// every lookup failure ends in the empty label, and the function never returns
// an error and never prints.
func annotatePlannerChat(rt relevo.Runtime, rep *view.Report, res chatlabel.Resolver) {
	if rt.Planners == nil {
		return
	}
	labels := make(map[string]chatlabel.Label)
	for i := range rep.Bindings {
		b := &rep.Bindings[i]
		if b.PlannerID == "" {
			continue
		}
		lbl, ok := labels[b.PlannerID]
		if !ok {
			if rec, err := rt.Planners.Get(b.PlannerID); err != nil {
				lbl = chatlabel.Label{}
			} else {
				lbl = res.Resolve(context.Background(), rec.HarnessKind, rec.SessionID, rec.TranscriptLocator)
			}
			labels[b.PlannerID] = lbl
		}
		b.PlannerChatLabel = lbl.Text
		b.PlannerChatLink = lbl.Link
	}
}

func cmdPlannerRename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 2 {
		return fmt.Errorf("usage: relevo planner rename <id|name> <new-name>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	reg, err := plannerRegistry(rt)
	if err != nil {
		return err
	}
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
		return fmt.Errorf("usage: relevo planner forget <id|name>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	reg, err := plannerRegistry(rt)
	if err != nil {
		return err
	}
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

// errGCUsage is gcScope's usage-error shape: a message cmdUnbind/runGC print
// to stderr verbatim before exiting 2 (#482).
type errGCUsage string

func (e errGCUsage) Error() string { return string(e) }

// gcScope turns unbind --done's --planner/--all-planners flags into a GC
// scope, with no fallback to "everything" (#482). resolve is injected so the
// function stays pure and needs no registry to test; in production it closes
// over a Runtime and calls planner.Resolve.
func gcScope(plannerFlag string, all bool, resolve func(ref string) (planner.Record, error)) (relevo.GCOptions, error) {
	switch {
	case all && plannerFlag != "":
		return relevo.GCOptions{}, errGCUsage("relevo: --all-planners and --planner are exclusive")
	case all:
		return relevo.GCOptions{AllPlanners: true}, nil
	}

	rec, err := resolve(plannerFlag)
	if err != nil {
		return relevo.GCOptions{}, errGCUsage(fmt.Sprintf(
			"relevo: unbind --done clears this planner's DONE bindings, and no planner resolved (%v); pass --planner <name|id>, or --all-planners to clear every planner's", err))
	}
	return relevo.GCOptions{PlannerID: rec.ID}, nil
}

// plannerFilter resolves this session's planner for the commands that filter
// by it without requiring one: `relevo status` with no name (§3.3) and
// `relevo status --line`. A miss is not an error there -- the caller keeps its
// old behaviour -- and neither is a Runtime with no registry (tests).
func plannerFilter(rt relevo.Runtime) (planner.Record, bool) {
	if rt.Planners == nil {
		return planner.Record{}, false
	}
	var now time.Time
	if rt.Now != nil {
		now = rt.Now()
	}
	cwd, _ := os.Getwd()
	rec, _, err := planner.Resolve(rt.Planners, planner.ResolveInput{
		Env:             os.Getenv,
		PPID:            os.Getppid(),
		ProcStart:       rt.ProcStart,
		Now:             now,
		CWD:             cwd,
		OpencodeSession: rt.OpencodeSession,
	})
	if err != nil {
		return planner.Record{}, false
	}
	return rec, true
}

// plannerInUse is Forget's guard (§4.7): a record any binding that is not DONE
// still names is in use, and forgetting it would strand that binding's
// history. It walks the same store listing `relevo status --all` reads.
//
// An unreadable store refuses the forget: deleting a record a live binding may
// name is worse than making the human retry.
func plannerInUse(rt relevo.Runtime) func(id string) bool {
	return func(id string) bool {
		counts, err := plannerBindingCounts(rt)
		if err != nil {
			return true
		}
		return counts[id] > 0
	}
}

// plannerBindingCounts counts, per planner id, the bindings that are not DONE
// and name that planner -- the store listing Forget's in-use guard walks. An
// unreadable store is an error, never an empty map: "no bindings" would let
// the daemon's hourly prune delete a record a binding still names.
func plannerBindingCounts(rt relevo.Runtime) (map[string]int, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, b := range bindings {
		if b.State != store.StateDone && b.PlannerID != "" {
			counts[b.PlannerID]++
		}
	}
	return counts, nil
}
