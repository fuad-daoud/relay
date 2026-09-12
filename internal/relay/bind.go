package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrBuilderAlive reports a rebind attempt against a binding whose builder is
// still running. Relay never abandons a live builder: the human ends it, or
// `relay done` the binding first.
var ErrBuilderAlive = errors.New("builder is still alive; rebinding would abandon it")

// ErrBuilderUnverified reports a rebind attempt against a binding whose builder
// could not be located but was never session-identified.
//
// SameAgent falls back to pane plus kind when no session is recorded, and a
// workspace move changes the pane id -- so a failed match means either "dead"
// or "moved, still running". relay cannot tell which, and rebinding on the
// guess spawns a replacement and orphans a builder that is still working.
// It refuses instead, until a human says the builder really is gone.
var ErrBuilderUnverified = errors.New("builder was never session-identified")

// ErrHeadlessAdopt: --headless describes a process relay will run; a pane
// the human already started is the opposite of that (headless spec §3.6).
var ErrHeadlessAdopt = errors.New("--headless spawns a process; it cannot adopt a pane (drop --builder <pane>)")

// ErrHeadlessResume: a binding's mode is fixed at creation. Changing it
// under a live round would leave the old shape's state (pane id, or pid and
// log) meaning nothing (headless spec §1, scope boundary).
var ErrHeadlessResume = errors.New("--headless cannot change an existing binding's mode; unbind and bind again")

// BindOptions describes one bind request. BuilderPane adopts an existing pane;
// leaving it empty spawns a new one from Candidate.
type BindOptions struct {
	Name string
	// Candidate is a harness/provider/model token; empty means resolve by role
	// through resolveCandidate, except in resume, where empty means "not rebinding".
	Candidate   string
	BuilderPane string
	PlannerPane string
	CWD         string
	Resume      bool

	// Rebind, with Resume, replaces a builder that is gone by resolving a
	// candidate through policy.json order and the ledger, exactly as a
	// fresh bind with Candidate empty does (#92). Without it, an empty
	// Candidate and BuilderPane on resume mean "planner-only: touch no
	// builder". Ignored when Candidate or BuilderPane is set.
	Rebind bool

	// AssumeDead releases the ErrBuilderUnverified guard: the caller asserts a
	// builder relay cannot verify is gone really is gone. It never overrides
	// ErrBuilderAlive -- a builder relay can positively see is refused either
	// way.
	AssumeDead bool

	// WorkspaceID scopes the builder's tab to the planner's workspace.
	WorkspaceID string

	// RoundTimeout overrides the binding's round budget. Zero keeps the
	// store's default.
	RoundTimeout time.Duration

	// Headless makes the builder a process relay runs per round instead of
	// a pane it watches (#99). Refused with BuilderPane and with Resume.
	Headless bool
}

// BindResolved ties the calling planner pane to a builder over one working
// tree. The second return is how the builder was chosen, zero when a pane
// was adopted.
func BindResolved(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, Resolution, error) {
	if opts.PlannerPane == "" {
		return store.Binding{}, Resolution{}, errors.New("no planner pane; is HERDR_PANE_ID set")
	}
	if opts.CWD == "" {
		return store.Binding{}, Resolution{}, errors.New("no working directory")
	}

	if opts.Headless && opts.BuilderPane != "" {
		return store.Binding{}, Resolution{}, ErrHeadlessAdopt
	}
	if opts.Headless && opts.Resume {
		return store.Binding{}, Resolution{}, ErrHeadlessResume
	}

	agents, err := rt.Herdr.ListAgents(ctx)
	if err != nil {
		return store.Binding{}, Resolution{}, fmt.Errorf("list agents: %w", err)
	}

	planner, ok := FindAgent(agents, store.Endpoint{PaneID: opts.PlannerPane})
	if !ok {
		return store.Binding{}, Resolution{}, fmt.Errorf("no agent in planner pane %s", opts.PlannerPane)
	}

	if opts.Resume {
		return resume(ctx, rt, opts, planner)
	}

	return create(ctx, rt, opts, planner)
}

// Bind is BindResolved without the resolution, for the callers that only
// need the binding. cmdBind uses BindResolved to print why relay picked
// what it did.
func Bind(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, error) {
	b, _, err := BindResolved(ctx, rt, opts)
	return b, err
}

// resume re-points an existing binding at the calling planner pane, and -- when
// the caller supplied a builder, or asked for one with Rebind -- at a new builder as well.
//
// Preconditions:  the binding exists. When a builder is supplied, the binding's
//
//	current builder must NOT be alive: rebinding over a working
//	builder would abandon a round mid-flight and strand its pane.
//	A binding whose builder cannot be found by session identity is
//	treated as gone, even if another agent now occupies its former pane.
//
// Postconditions: Planner points at the caller. A rebind of a DONE binding is
//
//	refused. A planner-only resume of one is allowed, and
//	reactivates it, exactly as before this feature existed. When
//	a builder was supplied: Builder is the new endpoint with its
//	session id recorded, State is Active,
//	HaltNotifiedRound is 0, and the builder-screen fields are
//	cleared. RoundClosedTree is cleared when a builder was supplied:
//	a tree that changed hands says nothing about a builder that no
//	longer exists. Round, CWD, Name, RoundBaselineTree and the round
//	log are untouched.
//
// Errors: store.ErrNotFound; ErrBuilderAlive; ErrBuilderUnverified; a wrapped
// herdr failure.
func resume(ctx context.Context, rt Runtime, opts BindOptions, planner herdr.Agent) (store.Binding, Resolution, error) {
	rebinding := opts.Rebind || opts.Candidate != "" || opts.BuilderPane != ""

	var (
		builder store.Endpoint
		res     Resolution
	)
	if rebinding {
		// Refuse before anything is spawned.
		b, err := rt.Store.Load(opts.Name)
		if err != nil {
			return store.Binding{}, Resolution{}, err
		}
		if b.State == store.StateDone {
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q is done: `relay bind` to start fresh", opts.Name)
		}
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return store.Binding{}, Resolution{}, fmt.Errorf("list agents: %w", err)
		}
		if _, alive := FindAgent(agents, b.Builder); alive {
			return store.Binding{}, Resolution{}, ErrBuilderAlive
		}
		// Reached only when the builder was NOT located. Without a recorded
		// session that miss is ambiguous: the pane id it would match on is the
		// one a workspace move invalidates.
		//
		// Only refuse when a round is open. That is where #21's harm lives --
		// orphaning a builder "still running the round" -- and it is what
		// keeps #20's recovery (PR #22) working: a session-less builder with
		// nothing in flight still rebinds without ceremony.
		//
		// Keyed on live evidence rather than b.State so the guard does not
		// depend on whether the daemon has ticked since the pane went away.
		d := DiagnoseBuilder(b)
		if !d.Identified && d.RoundOpen && !opts.AssumeDead {
			return store.Binding{}, Resolution{}, fmt.Errorf(
				"%w: relay cannot tell a dead builder for %q from a moved pane. "+
					"Check %s is really gone, then re-run with --assume-dead",
				ErrBuilderUnverified, opts.Name, b.Builder.PaneID)
		}
		builder, res, err = resolveBuilder(ctx, rt, nil, opts, opts.Name, planner.PaneID)
		if err != nil {
			return store.Binding{}, Resolution{}, err
		}
		// IRREVERSIBLE: a pane may now exist. Never closed by relay.
	}

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		if rebinding && b.State == store.StateDone {
			return fmt.Errorf("binding %q is done: `relay bind` to start fresh", opts.Name)
		}

		b.Planner = endpointOf(planner)
		b.State = store.StateActive
		if rebinding {
			b.Builder = builder
			b.BuilderCandidate = res.Token() // "" when adopting a pane
			b.HaltNotifiedRound = 0
			b.BuilderScreen = ""
			b.BuilderScreenAt = time.Time{}
			b.RoundClosedTree = ""
		}

		if err := tx.Save(b); err != nil {
			return err
		}

		if rebinding && res.How != "" {
			if err := tx.AppendLog(opts.Name, pickEntry(rt.Now(), b.Round, "builder", res)); err != nil {
				return err
			}
		}

		out = b
		return nil
	})
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	return out, res, nil
}

func create(ctx context.Context, rt Runtime, opts BindOptions, planner herdr.Agent) (store.Binding, Resolution, error) {
	name := opts.Name
	if name == "" {
		name = SanitizeName(baseName(opts.CWD))
	}
	if err := store.ValidName(name); err != nil {
		return store.Binding{}, Resolution{}, err
	}

	// Refuse a name that is already taken, before anything is spawned. Save
	// would overwrite only bind.json: the round log and the NNN-*.md files
	// survive, so a fresh round 1 would collide with the previous session's
	// round 1 and Reconcile would read that old report entry as "already
	// handled" -- silently, with no error and no notification.
	if _, err := rt.Store.Load(name); err == nil {
		return store.Binding{}, Resolution{}, fmt.Errorf(
			"binding %q already exists: `relay unbind %s` to start fresh, or `relay bind --resume --name %s` to adopt it",
			name, name, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Binding{}, Resolution{}, err
	}

	// Check the working tree before spawning anything. Save re-checks under the
	// lock and stays authoritative, but without this a refused bind would leave
	// a started builder pane stranded with nothing pointing at it.
	other, found, err := rt.Store.FindByCWD(opts.CWD)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}
	if found && other.Name != name && other.State != store.StateDone {
		return store.Binding{}, Resolution{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
			opts.CWD, other.Name, other.Builder.PaneID, other.Round, store.ErrCWDTaken)
	}

	builder, res, err := resolveBuilder(ctx, rt, nil, opts, name, planner.PaneID)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	b := store.Binding{
		Name:             name,
		CWD:              opts.CWD,
		Planner:          endpointOf(planner),
		Builder:          builder,
		BuilderCandidate: res.Token(),
		Round:            1,
		State:            store.StateActive,
	}
	if opts.RoundTimeout > 0 {
		b.RoundTimeoutMS = int(opts.RoundTimeout / time.Millisecond)
	}

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		if res.How == "" {
			return nil
		}
		return tx.AppendLog(name, pickEntry(rt.Now(), 1, "builder", res))
	})
	if err != nil {
		// The pre-check passed but the lock disagreed, so a builder pane is now
		// running with no binding. Name it: relay never closes a pane itself.
		return store.Binding{}, Resolution{}, fmt.Errorf("bind failed after starting builder in pane %s (close it yourself): %w",
			builder.PaneID, err)
	}

	// Save fills in the defaults it owns -- round cap, round budget -- on its
	// own copy, so read back what was actually stored rather than returning
	// the pre-Save value and letting the two drift.
	stored, err := rt.Store.Load(b.Name)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	return stored, res, nil
}

// builderAgentName composes and validates the herdr agent name for a
// binding's builder. It runs before any pane or worktree exists, so a name
// herdr would refuse fails as a validation error with nothing to clean up.
func builderAgentName(name string) (string, error) {
	agentName := name + "-builder"
	if err := herdr.ValidateAgentName(agentName); err != nil {
		return "", fmt.Errorf("builder agent name %q: %w -- use a binding name of at most %d characters",
			agentName, err, herdr.MaxAgentNameLen-len("-builder"))
	}
	return agentName, nil
}

// resolveBuilder adopts an existing builder pane, opens a tab and starts the
// candidate agent in its root pane, or -- with opts.Headless -- records a
// headless endpoint and spawns nothing (#99). Focus stays with the planner
// either way.
//
// The candidate is resolved only on the spawn path. Adopting a pane needs no
// candidate: the human launched that agent themselves, so relay has no kind or
// args to supply -- and for agy, their fish function already activated the
// plan-executor role in that session. The second return is the resolution,
// zero when adopting.
//
// tx witnesses whether the caller already holds the state lock: nil means it
// does not (create, resume, Add and Fork all call resolveBuilder before their
// own WithLock, and pass nil), non-nil means it does (switchBuilder runs
// inside Reconcile's WithLock and passes its tx). It decides nothing about
// what resolveBuilder does -- the ledger file stays outside the store's
// transaction -- it only selects which spawn-failure recorder is safe to
// call: recordSpawnFailure takes Store.WithLock itself, which would deadlock
// a caller that is already inside it, so a non-nil tx routes to
// recordSpawnFailureLocked instead, which performs the same append without
// re-taking the lock.
func resolveBuilder(ctx context.Context, rt Runtime, tx *store.Tx, opts BindOptions, name, plannerPane string) (store.Endpoint, Resolution, error) {
	if opts.BuilderPane != "" {
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return store.Endpoint{}, Resolution{}, fmt.Errorf("list agents: %w", err)
		}
		found, ok := FindAgent(agents, store.Endpoint{PaneID: opts.BuilderPane})
		if !ok {
			return store.Endpoint{}, Resolution{}, fmt.Errorf("no agent in builder pane %s", opts.BuilderPane)
		}
		return endpointOf(found), Resolution{}, nil
	}

	res, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), opts.Candidate, "builder")
	if err != nil {
		return store.Endpoint{}, Resolution{}, err
	}
	c := res.Candidate
	agentName, err := builderAgentName(name)
	if err != nil {
		return store.Endpoint{}, Resolution{}, err
	}

	// Headless (#99): the builder is a process relay starts on each send,
	// not a pane. Nothing to open, nothing to start, nothing to strand; the
	// endpoint records the mode, the name and the kind, and Send fills in
	// the process fields per round (spec §5.3).
	if opts.Headless {
		return store.Endpoint{AgentName: agentName, Kind: c.Harness, Mode: store.ModeHeadless}, res, nil
	}

	role, _ := harness.RoleByName("builder")
	h, _ := harness.Lookup(c.Harness) // cannot miss: Load validated it
	l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)

	paneID, err := openTab(ctx, rt, opts.WorkspaceID, opts.CWD, agentName)
	if err != nil {
		return store.Endpoint{}, Resolution{}, err
	}

	if err := rt.Herdr.StartAgent(ctx, agentName, l.Kind, paneID, l.Args); err != nil {
		if tx != nil {
			recordSpawnFailureLocked(rt, c.Ref().String(), name, err)
		} else {
			recordSpawnFailure(rt, c.Ref().String(), name, err)
		}
		return store.Endpoint{}, Resolution{}, fmt.Errorf("start builder %q: %w", agentName, err)
	}

	ep := store.Endpoint{AgentName: agentName, PaneID: paneID, Kind: l.Kind}

	// Record the new agent's session id. It is what lets a later
	// disappearance be told apart from a different agent taking over the same
	// pane, so a binding can recover from a detection flicker without ever
	// resuming into a stranger.
	//
	// Best effort only. This lookup races the agent's registration for every
	// harness: claude usually wins it, while agy usually loses it because its
	// session does not exist until the agent starts work. Either way, failing
	// the bind here would strand a live pane over a lookup relay can recover
	// without, and Reconcile backfills the session on a later tick.
	if agents, err := rt.Herdr.ListAgents(ctx); err == nil {
		if started, ok := FindAgent(agents, store.Endpoint{PaneID: paneID}); ok {
			ep.SessionID = started.Session.Value
		}
	}

	return ep, res, nil
}

// endpointOf projects a live herdr agent onto the store's durable endpoint
// shape, used for both a binding's planner and an adopted builder.
func endpointOf(a herdr.Agent) store.Endpoint {
	return store.Endpoint{
		PaneID:    a.PaneID,
		SessionID: a.Session.Value,
		Kind:      a.Kind,
	}
}

// openTab makes somewhere for a spawned agent to live: its own herdr tab in
// the planner's workspace, rooted at cwd and labelled so the tab strip says
// which builder or consult lives there. Focus stays with the planner.
// Every spawn site (bind, add, fork, ask) comes through here; placement is
// not a per-command decision (#79).
func openTab(ctx context.Context, rt Runtime, workspaceID, cwd, label string) (string, error) {
	paneID, err := rt.Herdr.CreateTab(ctx, workspaceID, cwd, label)
	if err != nil {
		return "", fmt.Errorf("create tab %q: %w", label, err)
	}
	return paneID, nil
}

// worktreeOutcome is what a teardown attempt decided about one binding's
// relay-created worktree. Exactly one of Removed, Kept, Gone is set, or none
// when the binding never had a worktree.
type worktreeOutcome struct {
	Removed string // worktree relay removed, or ""
	Kept    string // worktree relay left in place, or ""
	Reason  string // why it was kept; "" when nothing was kept
	Gone    string // recorded worktree whose directory no longer exists, or ""
}

// worktreeTeardown decides what to do with a binding's relay-created worktree
// and reports what it did. It never returns an error: failing to remove a
// directory must not fail the unbind or the sweep that asked for it.
//
// Rules, in order:
//  1. b.Worktree == ""        -> nothing to do (zero outcome)
//  2. rt.Git == nil           -> keep, reason "git unavailable"
//  3. stat(worktree) is ErrNotExist -> gone (the directory was already removed)
//  4. the dirty check errors    -> keep, reason naming the FAILED CHECK: "dirty check failed: " + brief(err)
//  5. the tree is dirty         -> keep, reason "uncommitted changes"
//  6. otherwise                 -> remove; on failure keep with the git error brief(err)
//
// The branch is never removed: a branch holds commits, and commits are work.
func worktreeTeardown(ctx context.Context, rt Runtime, b store.Binding, dryRun bool) worktreeOutcome {
	if b.Worktree == "" {
		return worktreeOutcome{}
	}
	if rt.Git == nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: "git unavailable"}
	}

	// git classifies a chdir into a missing directory as a missing binary,
	// which is the wrong diagnosis; the caller knows the path and checks it first.
	if _, err := os.Stat(b.Worktree); errors.Is(err, os.ErrNotExist) {
		return worktreeOutcome{Gone: b.Worktree}
	}

	dirty, err := rt.Git.Dirty(ctx, b.Worktree)
	if err != nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: "dirty check failed: " + brief(err)}
	}
	if dirty {
		return worktreeOutcome{Kept: b.Worktree, Reason: "uncommitted changes"}
	}

	if dryRun {
		return worktreeOutcome{Removed: b.Worktree}
	}

	if err := rt.Git.RemoveWorktree(ctx, b.CWD, b.Worktree, false); err != nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: brief(err)}
	}
	return worktreeOutcome{Removed: b.Worktree}
}

// UnbindResult is what unbinding actually did. Exactly one of WorktreeRemoved,
// WorktreeKept, WorktreeGone is set, or none when the binding never had a
// worktree. A kept worktree is the important case: it means the fork's tree
// still holds uncommitted work, so relay left it alone and the human decides.
type UnbindResult struct {
	ArchivedTo      string // archive path, or "" when deleted
	WorktreeRemoved string // worktree relay removed, or ""
	WorktreeKept    string // worktree relay refused to remove, or ""
	KeptReason      string // why it was kept; "" when nothing was kept
	WorktreeGone    string // recorded worktree whose directory no longer exists, or ""
	// ProcessStopped is the headless builder process relay stopped, or 0
	// (#99). ProcessErr is why a stop failed; "" when it did not. Both
	// zero for a pane binding and for a headless one between rounds.
	ProcessStopped int
	ProcessErr     string
}

// Unbind clears away one binding's state, leaving its herdr panes untouched.
//
// When archive is set the binding's directory is moved aside rather than
// deleted, which frees the name for a fresh bind while keeping log.jsonl and
// every round file — the record of what the planner actually told the builder.
// If relay created a git worktree for this binding, Unbind removes it provided
// it is clean, never removing the branch.
func Unbind(ctx context.Context, rt Runtime, name string, archive bool) (UnbindResult, error) {
	b, err := rt.Store.Load(name)
	if err != nil {
		return UnbindResult{}, err
	}

	var res UnbindResult
	// Stop a live headless round before touching its tree (#99, spec §4.6):
	// a builder still writing would dirty the worktree relay is about to
	// judge clean or not. A failed stop is reported, never fatal -- the
	// unbind is the human's decision and it proceeds.
	if pid, err := stopProcess(ctx, rt, b.Builder); err != nil {
		res.ProcessErr = fmt.Sprintf("pid %d: %v", pid, err)
	} else if pid != 0 {
		res.ProcessStopped = pid
	}

	outcome := worktreeTeardown(ctx, rt, b, false)
	res.WorktreeRemoved = outcome.Removed
	res.WorktreeKept = outcome.Kept
	res.KeptReason = outcome.Reason
	res.WorktreeGone = outcome.Gone

	if archive {
		dest, err := rt.Store.Archive(name)
		if err != nil {
			return res, err
		}
		res.ArchivedTo = dest
	} else {
		if err := rt.Store.Delete(name); err != nil {
			return res, err
		}
	}

	return res, nil
}

// SanitizeName coerces a directory name into herdr's agent-name rule.
func SanitizeName(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}

	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "relay"
	}
	if out[0] < 'a' || out[0] > 'z' {
		out = "b" + out
	}
	if len(out) > 32 {
		out = out[:32]
	}

	return out
}

func baseName(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}
