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
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/ingest"
	"github.com/fuad-daoud/relay/internal/planner"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/release"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/roles"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// Git is the slice of the git CLI relay needs. *git.Client satisfies it.
type Git interface {
	SnapshotTree(ctx context.Context, dir string) (string, error)
	DiffTrees(ctx context.Context, dir, from, to string) (git.Diff, error)
	// DiffWorktreeStat compares tree against dir's current working tree and
	// returns just the stat, no patch (#143): the live "+N/-M in F" a status
	// row shows while a round is open, cheaper than DiffTrees because it
	// never reads the patch body.
	DiffWorktreeStat(ctx context.Context, dir, tree string) (git.Stat, error)
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
	// Identity reports dir's effective git identity -- the user.name and
	// user.email `git config --get` resolves, global and system config
	// included -- so a remote builder can commit as the client (#335). An
	// unset key is ("", nil), not an error.
	Identity(ctx context.Context, dir string) (name, email string, err error)
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
	Git Git
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

	// LatencyPath is the per-candidate latency history file (#324 part 1):
	// time to first output per candidate, recorded by `relay candidates
	// --probe` and read back for the p50 on a plain listing. "" means no
	// store is configured, so nothing is recorded (tests, and any caller
	// that never set one).
	LatencyPath string

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

	// Registry is the roles registry: roles.json merged over the built-ins,
	// or the legacy derivation. nil means "derive the legacy registry from
	// Candidates and Policy on demand" (#374), which is what every existing
	// test gets, since tests don't set it.
	Registry *roles.Registry

	// ConfigWarnings collects the unknown-key and skipped-candidate warnings
	// the last config load produced (candidates.json then policy.json). They
	// never fail a command: `relay doctor` renders them in a `config` row and
	// the daemon logs each once (#372 §4.4). newRuntime fills it without
	// printing; the daemon's ConfigWatcher refills it on reload.
	ConfigWarnings []string

	// Usage reads what a round consumed from the harness's own record
	// (#142). Nil means every round records Basis unknown, note
	// "no reader"; tests that do not set it behave as a machine with no
	// reader, and rounds close exactly as before.
	Usage usage.Reader

	// Sessions locates a session record so a round's transcript can be
	// recorded (#184). plannerLocator (bind.go) also calls it at bind time
	// to fill Planner.TranscriptLocator (#172), the same file path, for the
	// coming history database.
	Sessions SessionLocator

	// Classify judges report and dialog paragraphs for instruction-shaped
	// content beside the regex scan (#211). Nil means no classifier is
	// configured and the regex result stands alone; cmd/relay wires
	// classify.Resolve, tests wire *classify.Fake.
	Classify classify.Classifier

	// Prices is ~/.config/relay/prices.json over the embedded default. The
	// zero value prices nothing, so every estimate is unknown.
	Prices usage.Prices

	// Fetcher reads the newest published release tag for the day-cached
	// staleness check (#293). Nil means no check runs at all: a served
	// daemon, an air-gapped build and every test that does not set it tick
	// exactly as before. cmd/relay wires release.NewHTTPFetcher.
	Fetcher release.Fetcher

	Now       func() time.Time
	Hooks     hooks.Dispatcher
	Remote    RemoteClient
	Transport remote.TreeTransport

	// NewID mints a consult id. Nil means a crypto/rand id, so no production
	// call site has to set it and tests can make ids deterministic.
	NewID func() string

	// StartedAt is when this daemon process started; zero means unknown
	// (CLI one-shots, tests), which disables the daemon-restart-relaunch
	// check (#244): a builder can never be "lost to a daemon restart" if
	// the daemon does not know when it itself started.
	StartedAt time.Time

	// Watched is the daemon's in-memory record of the processes it has seen
	// alive (#370). Only `relay daemon` sets one (relay.NewWatched); a nil
	// *Watched has seen nothing, which is exactly the #244 rule, so every
	// CLI one-shot keeps its old behaviour.
	Watched *Watched

	// AuthGrace is the daemon's in-memory record of when each binding's server
	// first answered a transient auth error (#373 §3). Only `relay daemon`
	// sets one (relay.NewAuthGrace); a nil *AuthGrace never expires, so every
	// CLI one-shot keeps its old behaviour and never halts on a transient 401.
	AuthGrace *AuthGrace

	// Roles checks whether a harness kind's shipped role files are present
	// on disk, so a candidate whose harness has none installed is gated
	// before it is picked (#238). Nil means no check, so tests that do not
	// set it behave as before; cmd/relay wires harness.OSRoleChecker().
	Roles harness.RoleChecker

	// Scope is the template a served headless round's ProcSpec.Scope is
	// filled from (#244, #216); its Unit is always empty here, since
	// startRound fills in the per-round unit name. Nil means no scopes
	// (the local daemon, CI, or a server whose scope probe failed).
	Scope *ScopeSpec

	// HeldCPUs returns the cores held by live rounds other than the binding
	// named self, in every store that shares this host's pool (#314). Nil
	// means localHeldCPUs, which reads only the caller's tx. The server sets
	// it to a closure over its owner root, so internal/relay stays unaware of
	// owners.
	HeldCPUs func(tx *store.Tx, self string) ([]int, error)

	// Channels arbitrates a planner's mailbox between the daemon and a live
	// `relay mcp` channel (docs/specs/2026-09-21-planner-channel-design.md).
	// Nil means no claims exist, so DeliverPending leaves the entry pending
	// for `relay pull`; cmd/relay wires
	// relay.FileClaims{Root: st.ChannelsDir()}.
	Channels ClaimStore

	// Planners is the planner registry (#303 step 1a). bind, add, fork and
	// ask resolve their planner through it, and the daemon back-fills a
	// binding written before PlannerID existed. Nil means no registry is
	// configured -- tests, and any embedded caller that predates it -- and
	// resolution then fails with ErrNoPlanner.
	Planners planner.Registry

	// ProcStart reads a process's start time in Unix seconds, the pid-reuse
	// defence planner.Resolve's host step needs. Nil means the host step
	// cannot run, and resolution falls through to the session.
	ProcStart func(pid int) (int64, error)

	// Deliverers routes a planner-bound payload to that planner kind's own
	// push path (docs/specs/2026-09-22-opencode-delivery-design.md). A kind
	// with no entry, and a nil map, leave the entry pending for `relay pull`.
	Deliverers map[string]PlannerDeliverer
}

// legacyRegistry is the registry derived from candidates.json and
// policy.json, the behaviour every runtime path had before roles.json (#374).
// It is roles.Build with no file, and that never errors in legacy mode, so
// the error is discarded.
func legacyRegistry(set *candidate.Set, pol policy.Policy) *roles.Registry {
	reg, _ := roles.Build(nil, set, pol)
	return reg
}

// RoleRegistry returns the runtime's roles registry: the one loaded from
// roles.json when it was set, else the legacy derivation of Candidates and
// Policy, built on demand (#374). It never returns nil.
func (rt Runtime) RoleRegistry() *roles.Registry {
	if rt.Registry != nil {
		return rt.Registry
	}
	return legacyRegistry(rt.Candidates, rt.Policy)
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

// ErrRemoteUnavailable is returned when a remote operation is attempted without a configured remote client.
var ErrRemoteUnavailable = errors.New("no remote client configured; run relay client init and relay client add-server")

// RemoteClient is the client for communicating with remote relay servers.
type RemoteClient interface {
	WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error)
	Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error)
	CreateBinding(ctx context.Context, server string, req remote.CreateBindingRequest) (remote.BindingView, error)
	GetBinding(ctx context.Context, server, name string) (remote.BindingView, error)
	// StartRound's retryOnUnreachable is the caller's answer to whether the
	// server advertised remote.FeatureIdempotentSend: only a server that
	// dedupes a repeated send may be sent the same round twice (#373 §4.4).
	StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier, candidate string, tags []remote.TagRef, retryOnUnreachable bool) (remote.BindingView, error)
	RoundFile(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error)
	RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error)
	Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error)
	Unavailable(ctx context.Context, server, name, token, reason string) error
	Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error)
	Done(ctx context.Context, server, name string) error
	Unbind(ctx context.Context, server, name string) error
	Resume(ctx context.Context, server, name string) (remote.BindingView, error)
	Stop(ctx context.Context, server, name string) (remote.BindingView, error)
}
