package relay

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrAliasRequired reports an add with no builder to spawn. Unlike a fork,
// which can inherit its source's alias, a peer builder has nothing to inherit
// from.
var ErrAliasRequired = errors.New("relay add needs --builder ALIAS")

// AddOptions describes one peer-builder request.
type AddOptions struct {
	Name        string // name for the new binding; required, must be free
	Alias       string // builder alias to spawn; required
	PlannerPane string // the calling pane, from $HERDR_PANE_ID; required
	Repo        string // the repository the worktree is cut from; the caller's cwd

	NewTab      bool // open the builder in its own tab
	WorkspaceID string

	// CWD binds the peer to a directory the human already prepared instead of
	// creating a worktree. It is the escape hatch for a non-git tree; relay
	// records no Worktree for it and will never remove it.
	CWD string
}

// AddResult is what an add produced, so the CLI can tell the human where the
// new tree and branch are without re-deriving them.
type AddResult struct {
	Binding  store.Binding
	Worktree string // "" when --cwd was used
	Branch   string // "" when --cwd was used
	Base     string // commit the worktree was cut from; "" when --cwd was used
}

// Add attaches an additional builder to the calling planner, on its own tree.
//
// It is deliberately not `fork`. A fork continues a timeline: it copies round
// history through some round, starts at the round after it, and records where
// it came from. A peer was never a continuation of anything -- it starts at
// round 1 with an empty log and no provenance -- so writing ForkedFrom on it
// would record a relationship that does not exist.
//
// Preconditions:  opts.PlannerPane names a live agent pane; opts.Name is valid
//
//	and unused; opts.Alias is non-empty; opts.Repo is a git
//	repository unless opts.CWD is given.
//
// Postconditions: on success a new binding exists at round 1, in StateActive,
//
//	with a running builder and its own working tree. On ANY
//	error, no binding exists and any worktree Add created has
//	been removed.
//
// Errors: ErrAliasRequired, ErrGitRequired, store.ErrCWDTaken,
//
//	git.ErrBranchExists, or a wrapped herdr failure.
func Add(ctx context.Context, rt Runtime, opts AddOptions) (AddResult, error) {
	if opts.PlannerPane == "" {
		return AddResult{}, errors.New("no planner pane; is HERDR_PANE_ID set")
	}
	if err := store.ValidName(opts.Name); err != nil {
		return AddResult{}, err
	}
	// Refuse here, not just inside resolveBuilder: Add cuts its worktree before
	// that runs, and a name herdr would refuse must not leave a worktree
	// behind. The composed name is discarded -- resolveBuilder recomputes it.
	if _, err := builderAgentName(opts.Name); err != nil {
		return AddResult{}, err
	}
	if opts.Alias == "" {
		return AddResult{}, ErrAliasRequired
	}

	agents, err := rt.Herdr.ListAgents(ctx)
	if err != nil {
		return AddResult{}, fmt.Errorf("list agents: %w", err)
	}
	planner, ok := FindAgent(agents, store.Endpoint{PaneID: opts.PlannerPane})
	if !ok {
		return AddResult{}, fmt.Errorf("no planner agent in pane %s", opts.PlannerPane)
	}

	if _, err := rt.Store.Load(opts.Name); err == nil {
		return AddResult{}, fmt.Errorf(
			"binding %q already exists: `relay unbind %s` to start fresh, or `relay bind --resume --name %s` to adopt it",
			opts.Name, opts.Name, opts.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return AddResult{}, err
	}

	var (
		cwd      string
		worktree string
		branch   string
		base     string
	)

	if opts.CWD != "" {
		info, err := os.Stat(opts.CWD)
		if err != nil {
			return AddResult{}, fmt.Errorf("stat %s: %w", opts.CWD, err)
		}
		if !info.IsDir() {
			return AddResult{}, fmt.Errorf("%s is not a directory", opts.CWD)
		}
		cwd = opts.CWD
	} else {
		if rt.Git == nil {
			return AddResult{}, ErrGitRequired
		}
		base, err = rt.Git.HeadCommit(ctx, opts.Repo)
		if err != nil {
			return AddResult{}, err
		}
		branch = "relay/" + opts.Name
		exists, err := rt.Git.BranchExists(ctx, opts.Repo, branch)
		if err != nil {
			return AddResult{}, err
		}
		if exists {
			return AddResult{}, git.ErrBranchExists
		}
		cwd = rt.Store.WorktreePath(opts.Name)
		if err := rt.Git.AddWorktree(ctx, opts.Repo, cwd, branch, base); err != nil {
			return AddResult{}, err
		}
		worktree = cwd
	}

	rollback := func() {
		if worktree != "" && rt.Git != nil {
			_ = rt.Git.RemoveWorktree(ctx, opts.Repo, worktree, true)
		}
	}

	other, found, err := rt.Store.FindByCWD(cwd)
	if err != nil {
		rollback()
		return AddResult{}, err
	}
	if found && other.Name != opts.Name && other.State != store.StateDone {
		rollback()
		return AddResult{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
			cwd, other.Name, other.Builder.PaneID, other.Round, store.ErrCWDTaken)
	}

	bindOpts := BindOptions{
		Name:        opts.Name,
		Alias:       opts.Alias,
		PlannerPane: planner.PaneID,
		CWD:         cwd,
		NewTab:      opts.NewTab,
		WorkspaceID: opts.WorkspaceID,
	}
	builder, err := resolveBuilder(ctx, rt, bindOpts, opts.Name, planner.PaneID)
	if err != nil {
		rollback()
		return AddResult{}, err
	}

	// RoundCap, RoundTimeoutMS, CreatedAt and UpdatedAt are all left zero on
	// purpose: store.Save fills them in (internal/store/store.go:269-279), so a
	// peer builder gets exactly the same defaults a plain `relay bind` does.
	b := store.Binding{
		Name:             opts.Name,
		CWD:              cwd,
		Planner:          endpointOf(planner),
		Builder:          builder,
		BuilderCandidate: opts.Alias,
		Round:            1,
		State:            store.StateActive,
		Worktree:         worktree,
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.Save(b)
	}); err != nil {
		rollback()
		return AddResult{}, fmt.Errorf("add failed after starting builder in pane %s (close it yourself): %w",
			builder.PaneID, err)
	}

	if stored, err := rt.Store.Load(b.Name); err == nil {
		b = stored
	}

	return AddResult{Binding: b, Worktree: worktree, Branch: branch, Base: base}, nil
}
