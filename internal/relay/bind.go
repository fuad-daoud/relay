package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// splitDirection matches herdr's own guidance for a sibling agent pane.
const splitDirection = "right"

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

// ErrConsultAlias reports a bind or add naming a consult role. A consult is
// ephemeral, read-only and one-shot; installing one as a binding's builder
// would put a reader where the work happens.
var ErrConsultAlias = errors.New("that alias is a consult role, not a builder")

// BindOptions describes one bind request. BuilderPane adopts an existing pane;
// leaving it empty spawns a new one from Alias.
type BindOptions struct {
	Name        string
	Alias       string
	BuilderPane string
	PlannerPane string
	CWD         string
	Resume      bool

	// AssumeDead releases the ErrBuilderUnverified guard: the caller asserts a
	// builder relay cannot verify is gone really is gone. It never overrides
	// ErrBuilderAlive -- a builder relay can positively see is refused either
	// way.
	AssumeDead bool

	// NewTab opens the builder in its own herdr tab instead of splitting the
	// planner's pane. WorkspaceID scopes that tab to the planner's workspace.
	NewTab      bool
	WorkspaceID string

	// RoundTimeout overrides the binding's round budget. Zero keeps the
	// store's default.
	RoundTimeout time.Duration
}

// Bind ties the calling planner pane to a builder over one working tree.
func Bind(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, error) {
	if opts.PlannerPane == "" {
		return store.Binding{}, errors.New("no planner pane; is HERDR_PANE_ID set")
	}
	if opts.CWD == "" {
		return store.Binding{}, errors.New("no working directory")
	}

	agents, err := rt.Herdr.ListAgents(ctx)
	if err != nil {
		return store.Binding{}, fmt.Errorf("list agents: %w", err)
	}

	planner, ok := FindAgent(agents, store.Endpoint{PaneID: opts.PlannerPane})
	if !ok {
		return store.Binding{}, fmt.Errorf("no agent in planner pane %s", opts.PlannerPane)
	}

	if opts.Resume {
		return resume(ctx, rt, opts, planner)
	}

	return create(ctx, rt, opts, planner)
}

// resume re-points an existing binding at the calling planner pane, and -- when
// the caller supplied a builder -- at a new builder as well.
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
//	session id recorded, PreamblePending is true, State is Active,
//	HaltNotifiedRound is 0, and the builder-screen fields are
//	cleared. RoundClosedTree is cleared when a builder was supplied:
//	a tree that changed hands says nothing about a builder that no
//	longer exists. Round, CWD, Name, RoundBaselineTree and the round
//	log are untouched.
//
// Errors: store.ErrNotFound; ErrBuilderAlive; ErrBuilderUnverified; a wrapped
// herdr failure.
func resume(ctx context.Context, rt Runtime, opts BindOptions, planner herdr.Agent) (store.Binding, error) {
	rebinding := opts.Alias != "" || opts.BuilderPane != ""

	var builder store.Endpoint
	if rebinding {
		// Refuse before anything is spawned.
		b, err := rt.Store.Load(opts.Name)
		if err != nil {
			return store.Binding{}, err
		}
		if b.State == store.StateDone {
			return store.Binding{}, fmt.Errorf("binding %q is done: `relay bind` to start fresh", opts.Name)
		}
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return store.Binding{}, fmt.Errorf("list agents: %w", err)
		}
		if _, alive := FindAgent(agents, b.Builder); alive {
			return store.Binding{}, ErrBuilderAlive
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
		if !d.SessionIdentified && d.RoundOpen && !opts.AssumeDead {
			return store.Binding{}, fmt.Errorf(
				"%w: relay cannot tell a dead builder for %q from a moved pane. "+
					"Check %s is really gone, then re-run with --assume-dead",
				ErrBuilderUnverified, opts.Name, b.Builder.PaneID)
		}
		builder, err = resolveBuilder(ctx, rt, opts, opts.Name, planner.PaneID)
		if err != nil {
			return store.Binding{}, err
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
			b.BuilderAlias = opts.Alias // "" when adopting a pane
			b.PreamblePending = true
			b.HaltNotifiedRound = 0
			b.BuilderScreen = ""
			b.BuilderScreenAt = time.Time{}
			b.RoundClosedTree = ""
		}

		if err := tx.Save(b); err != nil {
			return err
		}

		out = b
		return nil
	})
	if err != nil {
		return store.Binding{}, err
	}

	return out, nil
}

func create(ctx context.Context, rt Runtime, opts BindOptions, planner herdr.Agent) (store.Binding, error) {
	name := opts.Name
	if name == "" {
		name = SanitizeName(baseName(opts.CWD))
	}
	if err := store.ValidName(name); err != nil {
		return store.Binding{}, err
	}

	// Refuse a name that is already taken, before anything is spawned. Save
	// would overwrite only bind.json: the round log and the NNN-*.md files
	// survive, so a fresh round 1 would collide with the previous session's
	// round 1 and Reconcile would read that old report entry as "already
	// handled" -- silently, with no error and no notification.
	if _, err := rt.Store.Load(name); err == nil {
		return store.Binding{}, fmt.Errorf(
			"binding %q already exists: `relay unbind %s` to start fresh, or `relay bind --resume --name %s` to adopt it",
			name, name, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Binding{}, err
	}

	// Check the working tree before spawning anything. Save re-checks under the
	// lock and stays authoritative, but without this a refused bind would leave
	// a started builder pane stranded with nothing pointing at it.
	other, found, err := rt.Store.FindByCWD(opts.CWD)
	if err != nil {
		return store.Binding{}, err
	}
	if found && other.Name != name && other.State != store.StateDone {
		return store.Binding{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
			opts.CWD, other.Name, other.Builder.PaneID, other.Round, store.ErrCWDTaken)
	}

	builder, err := resolveBuilder(ctx, rt, opts, name, planner.PaneID)
	if err != nil {
		return store.Binding{}, err
	}

	b := store.Binding{
		Name:         name,
		CWD:          opts.CWD,
		Planner:      endpointOf(planner),
		Builder:      builder,
		BuilderAlias: opts.Alias,
		Round:        1,
		State:        store.StateActive,
	}
	if opts.RoundTimeout > 0 {
		b.RoundTimeoutMS = int(opts.RoundTimeout / time.Millisecond)
	}

	if err := rt.Store.Save(b); err != nil {
		// The pre-check passed but the lock disagreed, so a builder pane is now
		// running with no binding. Name it: relay never closes a pane itself.
		return store.Binding{}, fmt.Errorf("bind failed after starting builder in pane %s (close it yourself): %w",
			builder.PaneID, err)
	}

	// Save fills in the defaults it owns -- round cap, round budget -- on its
	// own copy, so read back what was actually stored rather than returning
	// the pre-Save value and letting the two drift.
	stored, err := rt.Store.Load(b.Name)
	if err != nil {
		return store.Binding{}, err
	}

	return stored, nil
}

// resolveBuilder adopts an existing builder pane, or splits a sibling pane and
// starts the aliased agent in it. Focus stays with the planner either way.
//
// The alias is looked up only on the spawn path. Adopting a pane needs no
// alias: the human launched that agent themselves, so relay has no kind or
// args to supply -- and for agy, their fish function already activated the
// plan-executor role in that session.
func resolveBuilder(ctx context.Context, rt Runtime, opts BindOptions, name, plannerPane string) (store.Endpoint, error) {
	if opts.BuilderPane != "" {
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return store.Endpoint{}, fmt.Errorf("list agents: %w", err)
		}
		found, ok := FindAgent(agents, store.Endpoint{PaneID: opts.BuilderPane})
		if !ok {
			return store.Endpoint{}, fmt.Errorf("no agent in builder pane %s", opts.BuilderPane)
		}
		return endpointOf(found), nil
	}

	// There is no default builder: which agent, model and role to spawn is a
	// choice only the human can make, and guessing one would silently start
	// the wrong (and possibly expensive) agent.
	if opts.Alias == "" {
		return store.Endpoint{}, fmt.Errorf(
			"relay bind needs --builder: an alias to spawn (known: %v), or a herdr pane id to adopt",
			rt.Aliases.Names())
	}

	spec, err := rt.Aliases.Lookup(opts.Alias)
	if err != nil {
		return store.Endpoint{}, err
	}

	if spec.IsConsult() {
		return store.Endpoint{}, fmt.Errorf(
			"%q is a consult role; ask it with `relay ask --role %s`: %w",
			opts.Alias, opts.Alias, ErrConsultAlias)
	}

	agentName := name + "-builder"

	paneID, err := builderPane(ctx, rt, opts, agentName, plannerPane)
	if err != nil {
		return store.Endpoint{}, err
	}

	if err := rt.Herdr.StartAgent(ctx, agentName, spec.Kind, paneID, spec.Args); err != nil {
		return store.Endpoint{}, fmt.Errorf("start builder %q: %w", agentName, err)
	}

	ep := store.Endpoint{AgentName: agentName, PaneID: paneID, Kind: spec.Kind}

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

	return ep, nil
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

// builderPane makes somewhere for the builder to live: its own tab when asked,
// otherwise a sibling pane beside the planner. Focus stays with the planner
// either way.
func builderPane(ctx context.Context, rt Runtime, opts BindOptions, agentName, plannerPane string) (string, error) {
	if opts.NewTab {
		paneID, err := rt.Herdr.CreateTab(ctx, opts.WorkspaceID, opts.CWD, agentName)
		if err != nil {
			return "", fmt.Errorf("create tab for builder: %w", err)
		}

		return paneID, nil
	}

	paneID, err := rt.Herdr.SplitPane(ctx, plannerPane, splitDirection, opts.CWD)
	if err != nil {
		return "", fmt.Errorf("split pane for builder: %w", err)
	}

	return paneID, nil
}

// worktreeOutcome is what a teardown attempt decided about one binding's
// relay-created worktree. All three fields empty means there was nothing to do.
type worktreeOutcome struct {
	Removed string // worktree relay removed, or ""
	Kept    string // worktree relay left in place, or ""
	Reason  string // why it was kept; "" when nothing was kept
}

// worktreeTeardown decides what to do with a binding's relay-created worktree
// and reports what it did. It never returns an error: failing to remove a
// directory must not fail the unbind or the sweep that asked for it.
//
// Rules, in order:
//  1. b.Worktree == ""        -> nothing to do (zero outcome)
//  2. rt.Git == nil           -> keep, reason "git unavailable"
//  3. the dirty check errors    -> keep, reason naming the FAILED CHECK: "dirty check failed: " + brief(err)
//  4. the tree is dirty         -> keep, reason "uncommitted changes"
//  5. otherwise                 -> remove; on failure keep with the git error brief(err)
//
// The branch is never removed: a branch holds commits, and commits are work.
func worktreeTeardown(ctx context.Context, rt Runtime, b store.Binding, dryRun bool) worktreeOutcome {
	if b.Worktree == "" {
		return worktreeOutcome{}
	}
	if rt.Git == nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: "git unavailable"}
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

// UnbindResult is what unbinding actually did. A kept worktree is the important
// case: it means the fork's tree still holds uncommitted work, so relay left it
// alone and the human decides.
type UnbindResult struct {
	ArchivedTo      string // archive path, or "" when deleted
	WorktreeRemoved string // worktree relay removed, or ""
	WorktreeKept    string // worktree relay refused to remove, or ""
	KeptReason      string // why it was kept; "" when nothing was kept
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
	outcome := worktreeTeardown(ctx, rt, b, false)
	res.WorktreeRemoved = outcome.Removed
	res.WorktreeKept = outcome.Kept
	res.KeptReason = outcome.Reason

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
