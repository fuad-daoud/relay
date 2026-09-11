// Package relay implements the handoff policy: which text moves between a
// planner and a builder, when, and when to stop. It holds no intelligence --
// every judgement stays with the planner agent.
package relay

import (
	"context"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
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
	ClosePane(ctx context.Context, paneID string) error
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
	Herdr      Herdr
	Git        Git
	Store      *store.Store
	Candidates *candidate.Set
	Now        func() time.Time
	Hooks      hooks.Dispatcher

	// HeldGrace is how long a focused planner's screen must be unchanged before
	// a held payload is injected anyway. Zero means DefaultHeldGrace. Set by
	// `relay daemon --held-grace`; daemon-wide like --interval, not per binding.
	HeldGrace time.Duration

	// NewID mints a consult id. Nil means a crypto/rand id, so no production
	// call site has to set it and tests can make ids deterministic.
	NewID func() string
}

// SameAgent reports whether a live agent is the one an endpoint records.
//
// When ep.AgentName and a.Name are both set, matching by name decides. Relay
// chooses agent names uniquely and they survive workspace moves and session
// flips.
//
// When an endpoint has a recorded SessionID, identity is exact: a live agent
// must carry that exact session. If no live agent carries it, the agent is
// considered gone, and relay does not fall back to matching pane id.
//
// When no session is recorded, a pane match is the best available evidence for
// a harness with no herdr session integration. If Kind is recorded, both pane
// and kind must match. If neither session nor kind is recorded (e.g. from
// older bindings), matching falls back to pane id alone.
//
// Accepted risk: a session-less endpoint whose pane is recycled to an agent of
// the same kind is adopted as the original builder, and relay will relay into
// it. This exposure lasts until a session is recorded. For claude it is brief: the
// window before the next tick backfills the session, since claude reports one
// at spawn. For agy and opencode it lasts until the agent has begun a
// conversation, because both report a session only once one exists -- verified
// live 2026-09-09 against herdr integration v10. Since `builder` is opencode
// and `abuilder` is agy, while claude is the planner, every builder harness
// relay ships is in the late-session population. A nameless endpoint (adopted
// pane) running sub-agents will still read as gone while a sub-agent is in the
// foreground, and `--builder <pane>` adopters should know it.
func SameAgent(a herdr.Agent, ep store.Endpoint) bool {
	if ep.AgentName != "" && a.Name != "" {
		return a.Name == ep.AgentName
	}
	if ep.SessionID != "" {
		return a.Session.Value == ep.SessionID
	}
	if ep.Kind != "" {
		return a.PaneID == ep.PaneID && a.Kind == ep.Kind
	}
	return a.PaneID == ep.PaneID
}

// findAgentIndex is FindAgent's positional form. A caller that needs to record
// something against the agent it just located needs that agent's slot in the
// snapshot, not a copy of it.
func findAgentIndex(agents []herdr.Agent, ep store.Endpoint) (int, bool) {
	for i, a := range agents {
		if SameAgent(a, ep) {
			return i, true
		}
	}
	return -1, false
}

// FindAgent locates a binding endpoint among the live agents using SameAgent.
func FindAgent(agents []herdr.Agent, ep store.Endpoint) (herdr.Agent, bool) {
	i, ok := findAgentIndex(agents, ep)
	if !ok {
		return herdr.Agent{}, false
	}
	return agents[i], true
}
