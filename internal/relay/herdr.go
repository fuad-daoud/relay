// Package relay implements the handoff policy: which text moves between a
// planner and a builder, when, and when to stop. It holds no intelligence --
// every judgement stays with the planner agent.
package relay

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/classify"
	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/ingest"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// Herdr is the slice of the herdr CLI this package needs. It is declared here,
// by the consumer; *herdr.Client satisfies it.
type Herdr interface {
	ListAgents(ctx context.Context) ([]herdr.Agent, error)
	Prompt(ctx context.Context, target, text string) error
	SendKeys(ctx context.Context, target, keys string) error
	ReadAgent(ctx context.Context, target string, lines int) (string, error)
	ReadAgentSource(ctx context.Context, target, source string, lines int) (string, error)
	CreateTab(ctx context.Context, workspaceID, cwd, label string) (string, error)
	StartAgent(ctx context.Context, name, kind, paneID string, args []string) error
	Notify(ctx context.Context, title, body string, sound herdr.Sound) error
	ReportMetadata(ctx context.Context, paneID string, m herdr.PaneMetadata) error
	ClosePane(ctx context.Context, paneID string) error
}

// Git is the slice of the git CLI relay needs. *git.Client satisfies it.
type Git interface {
	SnapshotTree(ctx context.Context, dir string) (string, error)
	DiffTrees(ctx context.Context, dir, from, to string) (git.Diff, error)
	HeadCommit(ctx context.Context, dir string) (string, error)
	// TreeFingerprint hashes dir's HEAD and porcelain status into one short
	// string that changes when the tree does (#135). It never reads a diff or
	// writes a snapshot, so a binding's progress sample is cheap.
	TreeFingerprint(ctx context.Context, dir string) (string, error)
	RevListCount(ctx context.Context, dir, from, to string) (int, error)
	BranchExists(ctx context.Context, dir, branch string) (bool, error)
	CreateBranch(ctx context.Context, dir, branch, commit string) error
	// CreateTrackingBranch creates branch in dir tracking upstream, the
	// existing-branch form of creation: add --branch uses it when only
	// origin/<name> exists, before CheckoutWorktree.
	CreateTrackingBranch(ctx context.Context, dir, branch, upstream string) error
	DeleteBranch(ctx context.Context, dir, branch string) error
	AddWorktree(ctx context.Context, dir, path, branch, commit string) error
	// AddDetachedWorktree is AddWorktree without a branch: a throwaway tree
	// at commit with a detached HEAD (#144).
	AddDetachedWorktree(ctx context.Context, dir, path, commit string) error
	// CheckoutWorktree is the existing-branch form of git worktree add; AddWorktree creates the branch, this one checks it out.
	CheckoutWorktree(ctx context.Context, dir, path, branch string) error
	RemoveWorktree(ctx context.Context, dir, path string, force bool) error
	Dirty(ctx context.Context, dir string) (bool, error)
	RefSHA(ctx context.Context, dir, ref string) (string, bool, error)
	UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA string) error
	CommitTree(ctx context.Context, dir, tree, parent, message string) (string, error)
	// CommitAll stages the whole working tree (git add -A) and commits it
	// with relay's fixed identity, returning the new HEAD sha, or ("", nil)
	// when there was nothing to commit (#137).
	CommitAll(ctx context.Context, dir, message string) (string, error)
	MergeFF(ctx context.Context, dir, ref string) error
	RootCommit(ctx context.Context, dir string) (string, error)
	// ListTags maps each of dir's tags (short name) to the commit it points
	// at, annotated tags peeled (#242).
	ListTags(ctx context.Context, dir string) (map[string]string, error)
	// RepoFacts reports dir's repository identity -- origin remote URL
	// (raw, unnormalised) and the main worktree's absolute .git directory --
	// for the coming history database (#172; captureRepo is the caller).
	RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error)
	// CurrentBranch names the branch dir has checked out, or "" when HEAD
	// is detached. add/fork record it as a binding's BaseRef, the branch
	// `relay land` rebases onto (#136).
	CurrentBranch(ctx context.Context, dir string) (string, error)
	// Fetch fetches ref from remote, so origin/<ref> resolves afterwards.
	Fetch(ctx context.Context, dir, remote, ref string) error
	// Rebase rewrites dir's branch onto onto. On a conflict it returns the
	// unmerged paths and aborts the rebase, leaving the worktree as it was.
	Rebase(ctx context.Context, dir, onto string) ([]string, error)
	// Merge integrates ref into dir's branch without rewriting it (--merge's
	// escape hatch). On a conflict it returns the unmerged paths and aborts
	// the merge, leaving the worktree as it was.
	Merge(ctx context.Context, dir, ref string) ([]string, error)
	// Push pushes branch to remote, force-with-lease when the rebase rewrote
	// a branch that already exists there.
	Push(ctx context.Context, dir, remote, branch string, forceWithLease bool) error
	// RemoteBranchExists reports whether remote already has branch, which
	// decides whether a push needs the lease.
	RemoteBranchExists(ctx context.Context, dir, remote, branch string) (bool, error)
}

// Runtime carries relay's dependencies explicitly, so every command and the
// daemon can be driven by a fake in tests.
type Runtime struct {
	Herdr Herdr
	Git   Git
	// Runner starts and stops headless builder processes (#99). cmd/relay
	// wires proc.New(); tests wire fakeRunner. Nil means no headless path
	// can run, and reports ErrRunnerUnavailable.
	Runner     Runner
	Store      *store.Store
	Candidates *candidate.Set
	LedgerPath string // the availability ledger file (#61 step 1)

	// AvailabilityPath is the availability history file (#61 step 7, renamed
	// availability.json by #172 q6).
	AvailabilityPath string

	// DB is relay's sqlite database (docs/specs/2026-09-20-persistence-design.md).
	// Nil means no database: this round opens it only in `relay db *`, never
	// in the shared runtime constructor, so nothing else reads it yet and
	// every call site that will (later rounds) must treat nil the same as a
	// machine with no db.
	DB *db.DB

	// Policy is ~/.config/relay/policy.json: the planner's candidate order
	// per role (#61 step 2). The zero value means nothing is ordered, so
	// tests that do not set it behave as a machine with no policy file.
	Policy policy.Policy

	// Usage reads what a round consumed from the harness's own record
	// (#142). Nil means every round records Basis unknown, note
	// "no reader"; tests that do not set it behave as a machine with no
	// reader, and rounds close exactly as before.
	Usage usage.Reader

	// Sessions locates a pane builder's own session record so the daemon can
	// render it into the round log the way it renders a headless stream
	// (#184). Nil means pane builders keep the screen capture; tests that do
	// not set it behave exactly as before. plannerLocator (bind.go) also
	// calls it at bind time to fill Planner.TranscriptLocator (#172), the
	// same file path, for the coming history database.
	Sessions SessionLocator

	// Classify judges report and dialog paragraphs for instruction-shaped
	// content beside the regex scan (#211). Nil means no classifier is
	// configured and the regex result stands alone; cmd/relay wires
	// classify.Resolve, tests wire *classify.Fake.
	Classify classify.Classifier

	// Prices is ~/.config/relay/prices.json over the embedded default. The
	// zero value prices nothing, so every estimate is unknown.
	Prices usage.Prices

	Now       func() time.Time
	Hooks     hooks.Dispatcher
	Remote    RemoteClient
	Transport remote.TreeTransport

	// HeldGrace is how long a focused planner's screen must be unchanged before
	// a held payload is injected anyway. Zero means DefaultHeldGrace. Set by
	// `relay daemon --held-grace`; daemon-wide like --interval, not per binding.
	HeldGrace time.Duration

	// NewID mints a consult id. Nil means a crypto/rand id, so no production
	// call site has to set it and tests can make ids deterministic.
	NewID func() string

	// StartedAt is when this daemon process started; zero means unknown
	// (CLI one-shots, tests), which disables the daemon-restart-relaunch
	// check (#244): a builder can never be "lost to a daemon restart" if
	// the daemon does not know when it itself started.
	StartedAt time.Time

	// Roles checks whether a harness kind's shipped role files are present
	// on disk, so a candidate whose harness has none installed is gated
	// before it is picked (#238). Nil means no check, so tests that do not
	// set it behave as before; cmd/relay wires harness.OSRoleChecker().
	Roles harness.RoleChecker
}

// IngestDeps builds internal/ingest's Deps from rt: Git carries through
// nil-safe (a nil rt.Git converts to a nil ingest.GitFacts, since both are
// true nil interfaces), Sessions is converted to ingest's own
// SessionLocator type at this boundary (internal/ingest cannot import this
// package -- it is ingest's caller -- so it declares an identical function
// type rather than reusing SessionLocator directly), and Now is time.Now.
func IngestDeps(rt Runtime) ingest.Deps {
	return ingest.Deps{
		Git:      rt.Git,
		Sessions: ingest.SessionLocator(rt.Sessions),
		Now:      time.Now,
	}
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

// ErrRemoteUnavailable is returned when a remote operation is attempted without a configured remote client.
var ErrRemoteUnavailable = errors.New("no remote client configured; run relay client init and relay client add-server")

// RemoteClient is the client for communicating with remote relay servers.
type RemoteClient interface {
	WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error)
	Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error)
	CreateBinding(ctx context.Context, server string, req remote.CreateBindingRequest) (remote.BindingView, error)
	GetBinding(ctx context.Context, server, name string) (remote.BindingView, error)
	StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier string, tags []remote.TagRef) (remote.BindingView, error)
	RoundFile(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error)
	RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error)
	Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error)
	Unavailable(ctx context.Context, server, name, token, reason string) error
	Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error)
	Done(ctx context.Context, server, name string) error
	Unbind(ctx context.Context, server, name string) error
	Resume(ctx context.Context, server, name string) (remote.BindingView, error)
}
