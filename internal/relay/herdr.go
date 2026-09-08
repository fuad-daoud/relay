// Package relay implements the handoff policy: which text moves between a
// planner and a builder, when, and when to stop. It holds no intelligence --
// every judgement stays with the planner agent.
package relay

import (
	"context"
	"time"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/store"
)

// Herdr is the slice of the herdr CLI this package needs. It is declared here,
// by the consumer; *herdr.Client satisfies it.
type Herdr interface {
	ListAgents(ctx context.Context) ([]herdr.Agent, error)
	Prompt(ctx context.Context, target, text string) error
	SendKeys(ctx context.Context, target, keys string) error
	ReadAgent(ctx context.Context, target string, lines int) (string, error)
	ReadAgentSource(ctx context.Context, target, source string, lines int) (string, error)
	SplitPane(ctx context.Context, paneID, direction, cwd string) (string, error)
	CreateTab(ctx context.Context, workspaceID, cwd, label string) (string, error)
	StartAgent(ctx context.Context, name, kind, paneID string, args []string) error
	Notify(ctx context.Context, message string) error
}

// Git is the slice of the git CLI relay needs. *git.Client satisfies it.
type Git interface {
	SnapshotTree(ctx context.Context, dir string) (string, error)
	DiffTrees(ctx context.Context, dir, from, to string) (git.Diff, error)
	HeadCommit(ctx context.Context, dir string) (string, error)
	BranchExists(ctx context.Context, dir, branch string) (bool, error)
	AddWorktree(ctx context.Context, dir, path, branch, commit string) error
	RemoveWorktree(ctx context.Context, dir, path string, force bool) error
	Dirty(ctx context.Context, dir string) (bool, error)
}

// Runtime carries relay's dependencies explicitly, so every command and the
// daemon can be driven by a fake in tests.
type Runtime struct {
	Herdr   Herdr
	Git     Git
	Store   *store.Store
	Aliases *alias.Table
	Now     func() time.Time
	Hooks   hooks.Dispatcher
}

// FindAgent locates a binding endpoint among the live agents. Session id wins
// over pane id, because a pane moved between workspaces is issued a new pane
// id while its session survives.
func FindAgent(agents []herdr.Agent, ep store.Endpoint) (herdr.Agent, bool) {
	if ep.SessionID != "" {
		for _, a := range agents {
			if a.Session.Value == ep.SessionID {
				return a, true
			}
		}
	}

	for _, a := range agents {
		if a.PaneID == ep.PaneID {
			return a, true
		}
	}

	return herdr.Agent{}, false
}
