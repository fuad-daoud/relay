package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrRoundOutOfRange reports a --round outside the source binding's history.
var ErrRoundOutOfRange = errors.New("round is outside the source binding's history")

// ErrNoBuilderAlias reports a fork of an adopted-builder binding with no
// --builder to spawn from.
var ErrNoBuilderAlias = errors.New("source binding has no builder alias; pass --builder")

// ErrGitRequired reports a fork that needs a worktree with no git available.
var ErrGitRequired = errors.New("relay fork needs git; pass --cwd to bind a tree yourself")

// ForkOptions describes one fork request.
type ForkOptions struct {
	Source  string // binding to fork from; required
	Round   int    // source round to copy through; required, 1..source.Round
	NewName string // name for the fork; required, must be free

	// Alias is the builder to spawn for the fork. Empty inherits the source's
	// BuilderAlias; a source with no alias (an adopted builder) has none to
	// inherit, so the fork must be given one explicitly.
	Alias string

	PlannerPane string // the calling pane, from $HERDR_PANE_ID; required
	NewTab      bool   // open the builder in its own tab
	WorkspaceID string

	// CWD binds the fork to a directory the human already prepared instead of
	// creating a worktree. It is the escape hatch for a non-git tree; relay
	// records no Worktree for it and will never remove it.
	CWD string
}

// ForkResult is what a fork produced, so the CLI can tell the human where the
// new tree and branch are without re-deriving them.
type ForkResult struct {
	Binding  store.Binding
	Worktree string // "" when --cwd was used
	Branch   string // "" when --cwd was used
	Base     string // commit the worktree was cut from; "" when --cwd was used
}

// Fork branches a new binding from the source's state as of a given round.
//
// Preconditions:  opts.PlannerPane names a live agent pane; opts.Source exists;
//
//	1 <= opts.Round <= source.Round; opts.NewName is valid and
//	unused; the source round has a plan in the log.
//
// Postconditions: on success a new binding exists at round opts.Round+1, in
//
//	StateActive, with a running builder and its own working tree.
//	The source binding is byte-for-byte unchanged.
//
// Errors: store.ErrNotFound, ErrRoundOutOfRange, ErrNoBuilderAlias,
//
//	ErrGitRequired, store.ErrCWDTaken, git.ErrBranchExists, or a wrapped
//	herdr failure. Rollback is described in §5.
func Fork(ctx context.Context, rt Runtime, opts ForkOptions) (ForkResult, error) {
	if opts.PlannerPane == "" {
		return ForkResult{}, errors.New("no planner pane; is HERDR_PANE_ID set")
	}
	if err := store.ValidName(opts.NewName); err != nil {
		return ForkResult{}, err
	}
	// Refuse here, not just inside resolveBuilder: Fork cuts its worktree
	// before that runs, and a name herdr would refuse must not leave a
	// worktree behind. The composed name is discarded -- resolveBuilder
	// recomputes it.
	if _, err := builderAgentName(opts.NewName); err != nil {
		return ForkResult{}, err
	}

	agents, err := rt.Herdr.ListAgents(ctx)
	if err != nil {
		return ForkResult{}, fmt.Errorf("list agents: %w", err)
	}
	planner, ok := FindAgent(agents, store.Endpoint{PaneID: opts.PlannerPane})
	if !ok {
		return ForkResult{}, fmt.Errorf("no planner agent in pane %s", opts.PlannerPane)
	}

	src, err := rt.Store.Load(opts.Source)
	if err != nil {
		return ForkResult{}, err
	}
	if opts.Round < 1 || opts.Round > src.Round {
		return ForkResult{}, ErrRoundOutOfRange
	}

	entries, err := rt.Store.ReadLog(src.Name)
	if err != nil {
		return ForkResult{}, err
	}
	hasPlan := false
	for _, e := range entries {
		if e.Round == opts.Round && e.Direction == store.DirToBuilder && e.Kind == store.KindPlan {
			hasPlan = true
			break
		}
	}
	if !hasPlan {
		return ForkResult{}, fmt.Errorf("round %d of %q was never sent; nothing to fork from", opts.Round, opts.Source)
	}

	if _, err := rt.Store.Load(opts.NewName); err == nil {
		return ForkResult{}, fmt.Errorf(
			"binding %q already exists: `relay unbind %s` to start fresh, or `relay bind --resume --name %s` to adopt it",
			opts.NewName, opts.NewName, opts.NewName)
	} else if !errors.Is(err, store.ErrNotFound) {
		return ForkResult{}, err
	}

	alias := opts.Alias
	if alias == "" {
		alias = src.BuilderCandidate
	}
	if alias == "" {
		return ForkResult{}, ErrNoBuilderAlias
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
			return ForkResult{}, fmt.Errorf("stat %s: %w", opts.CWD, err)
		}
		if !info.IsDir() {
			return ForkResult{}, fmt.Errorf("%s is not a directory", opts.CWD)
		}
		cwd = opts.CWD
	} else {
		if rt.Git == nil {
			return ForkResult{}, ErrGitRequired
		}
		base, err = rt.Git.HeadCommit(ctx, src.CWD)
		if err != nil {
			return ForkResult{}, err
		}
		branch = "relay/" + opts.NewName
		exists, err := rt.Git.BranchExists(ctx, src.CWD, branch)
		if err != nil {
			return ForkResult{}, err
		}
		if exists {
			return ForkResult{}, git.ErrBranchExists
		}
		cwd = rt.Store.WorktreePath(opts.NewName)
		if err := rt.Git.AddWorktree(ctx, src.CWD, cwd, branch, base); err != nil {
			return ForkResult{}, err
		}
		worktree = cwd
	}

	rollback := func() {
		if worktree != "" && rt.Git != nil {
			_ = rt.Git.RemoveWorktree(ctx, src.CWD, worktree, true)
		}
	}

	other, found, err := rt.Store.FindByCWD(cwd)
	if err != nil {
		rollback()
		return ForkResult{}, err
	}
	if found && other.Name != opts.NewName && other.State != store.StateDone {
		rollback()
		return ForkResult{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
			cwd, other.Name, other.Builder.PaneID, other.Round, store.ErrCWDTaken)
	}

	bindOpts := BindOptions{
		Name:        opts.NewName,
		Alias:       alias,
		PlannerPane: planner.PaneID,
		CWD:         cwd,
		NewTab:      opts.NewTab,
		WorkspaceID: opts.WorkspaceID,
	}
	builder, err := resolveBuilder(ctx, rt, bindOpts, opts.NewName, planner.PaneID)
	if err != nil {
		rollback()
		return ForkResult{}, err
	}

	b := store.Binding{
		Name:             opts.NewName,
		CWD:              cwd,
		Planner:          endpointOf(planner),
		Builder:          builder,
		BuilderCandidate: alias,
		Round:            opts.Round + 1,
		State:            store.StateActive,
		RoundCap:         src.RoundCap,
		RoundTimeoutMS:   src.RoundTimeoutMS,
		Worktree:         worktree,
		ForkedFrom:       src.Name,
		ForkedAtRound:    opts.Round,
	}

	now := time.Now().UTC()
	if rt.Now != nil {
		now = rt.Now().UTC()
	}

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		return writeFork(tx, rt.Store, src.Name, b, opts.Round, now)
	})
	if err != nil {
		rollback()
		return ForkResult{}, fmt.Errorf("fork failed after starting builder in pane %s (close it yourself): %w",
			builder.PaneID, err)
	}

	if stored, err := rt.Store.Load(b.Name); err == nil {
		b = stored
	}

	if rt.Hooks != nil {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventForkCreated,
			BindingID: b.Name,
			State:     string(store.StateActive),
			OldState:  src.Name,
			Round:     b.Round,
			Timestamp: now,
		})
	}

	return ForkResult{
		Binding:  b,
		Worktree: worktree,
		Branch:   branch,
		Base:     base,
	}, nil
}

// writeFork populates the new binding's directory and saves it, under the
// caller's held lock. It is all-or-nothing: if any step fails, the destination
// directory is removed, so a failed fork never leaves a directory that
// store.list cannot see and a later bind would collide with.
//
// Preconditions:  the lock is held; b.Name has no directory yet.
// Postconditions: on success, <root>/<b.Name>/ holds log.jsonl, the copied
//
//	round files, and bind.json. On ANY error, that directory
//	does not exist.
func writeFork(tx *store.Tx, s *store.Store, src string, b store.Binding, throughRound int, now time.Time) error {
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(s.Dir(b.Name))
		}
	}()

	if err := tx.ForkState(src, b.Name, throughRound); err != nil {
		return err
	}

	if now.IsZero() {
		now = time.Now()
	}
	forkEntry := store.LogEntry{
		TS:        now,
		Round:     b.Round,
		Direction: store.DirToPlanner,
		Kind:      store.KindFork,
		Confirmed: true,
		Note:      fmt.Sprintf("forked from %s at round %d", src, throughRound),
	}
	if err := tx.AppendLog(b.Name, forkEntry); err != nil {
		return err
	}
	if err := tx.Save(b); err != nil {
		return err
	}
	success = true
	return nil
}
