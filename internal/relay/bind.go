package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrBuilderAlive reports a rebind attempt against a binding whose builder is
// still running. Relay never abandons a live builder: the human ends it, or
// `relay done` the binding first.
var ErrBuilderAlive = errors.New("builder is still alive; rebinding would abandon it")

// BindOptions describes one bind request. The builder is always a headless
// process relay runs per round (#99).
type BindOptions struct {
	Name string
	// Candidate is a harness/provider/model token; empty means resolve by role
	// through resolveCandidate, except in resume, where empty means "not rebinding".
	Candidate string
	// PlannerPane identifies the planner.
	// SPIKE(planner-id): was the multiplexer pane id; now --planner / $RELAY_PLANNER.
	PlannerPane string
	CWD         string
	Resume      bool

	// Rebind, with Resume, replaces a builder that is gone by resolving a
	// candidate through policy.json order and the ledger, exactly as a
	// fresh bind with Candidate empty does (#92). Without it, an empty
	// Candidate on resume means "planner-only: touch no builder". Ignored
	// when Candidate is set.
	Rebind bool

	// RoundTimeout overrides the binding's round budget. Zero keeps the
	// store's default.
	RoundTimeout time.Duration

	// Tier overrides the candidate/policy permission tier (#141).
	Tier string

	// AllowYolo permits Tier == "yolo" above policy max_tier for this command (#141).
	AllowYolo bool

	// Gate is the acceptance command relay runs on the binding's completion
	// marker (#132). Empty means fall back to policy.json's gate.default,
	// unless NoGate opts out of that default.
	Gate string

	// NoGate opts this binding out of policy.json's gate.default even when
	// Gate is empty (#132). Ignored when Gate is set.
	NoGate bool

	// Regate is the binding's automatic repair-round budget (#132 part 2):
	// how many repair rounds relay may open after a failing gate. nil falls
	// back to policy.json's gate.regate; an explicit 0 disables repair even
	// when the policy sets one.
	Regate *int

	// Feature is the human-given label grouping this binding with others
	// (#172); "" means ungrouped. Validated by store.ValidFeature when set.
	// On resume, an empty Feature means "leave the binding's existing
	// Feature untouched" rather than clearing it.
	Feature string
}

// ErrNoPlanner reports a verb that records a planner run without one.
// SPIKE(planner-id): was "no planner pane; is <pane env> set".
var ErrNoPlanner = errors.New("no planner; pass --planner or set RELAY_PLANNER")

// plannerEndpoint is the planner side of a binding. It used to be the
// live multiplexer agent in the planner's pane, carrying its kind and harness session.
//
// SPIKE(planner-id): the planner is now just an opaque id string, with no
// kind and no session, so Planner.TranscriptLocator (#172) cannot be
// resolved at bind time any more.
func plannerEndpoint(id string) store.Endpoint {
	return store.Endpoint{PaneID: id}
}

// BindResolved ties the calling planner to a headless builder over one
// working tree. The second return is how the builder was chosen.
func BindResolved(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, Resolution, error) {
	if opts.PlannerPane == "" {
		return store.Binding{}, Resolution{}, ErrNoPlanner
	}
	if opts.CWD == "" {
		return store.Binding{}, Resolution{}, errors.New("no working directory")
	}

	planner := plannerEndpoint(opts.PlannerPane)

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

// resume re-points an existing binding at the calling planner, and -- when
// the caller supplied a builder, or asked for one with Rebind -- at a new builder as well.
//
// Preconditions:  the binding exists. When a builder is supplied, the binding's
//
//	current builder must NOT be alive: rebinding over a working
//	builder would abandon a round mid-flight. A headless binding's
//	builder is a process; it is alive when the Runner says so.
//
// Postconditions: Planner points at the caller. A rebind of a DONE binding is
//
//	refused. A planner-only resume of one is allowed, and
//	reactivates it, exactly as before this feature existed. When
//	a builder was supplied: Builder is the new headless endpoint,
//	State is Active, and HaltNotifiedRound is 0. RoundClosedTree is cleared when a builder was supplied:
//	a tree that changed hands says nothing about a builder that no
//	longer exists. Round, CWD, Name, RoundBaselineTree and the round
//	log are untouched.
//	A legacy pane binding (see ErrPaneBuilder) rebinds to a
//	headless endpoint.
//
// Errors: store.ErrNotFound; ErrBuilderAlive; ErrRunnerUnavailable.
func resume(ctx context.Context, rt Runtime, opts BindOptions, planner store.Endpoint) (store.Binding, Resolution, error) {
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return store.Binding{}, Resolution{}, err
		}
	}

	b, err := rt.Store.Load(opts.Name)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	// A remote binding has no local worktree: its mode is fixed at
	// creation, so none of the worktree logic below applies to it. §4.6.
	if b.Builder.Remote() {
		return resumeRemote(ctx, rt, opts, planner, b)
	}

	var res Resolution
	restore := false
	if b.Worktree != "" {
		if _, err := os.Stat(b.Worktree); errors.Is(err, os.ErrNotExist) {
			restore = true
		}
	}

	if restore {
		if b.Branch == "" {
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q: worktree %s is gone and no branch is recorded; relay add to start fresh", opts.Name, b.Worktree)
		}
		if rt.Git == nil {
			return store.Binding{}, Resolution{}, errors.New("git unavailable; cannot restore worktree")
		}
		// An add binding's CWD is the worktree itself (add.go), which is the
		// directory that is gone; the repository it was cut from is not
		// recorded. The caller's cwd is where add ran, so restore from there
		// once the branch is confirmed to live in it.
		exists, err := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
		if err != nil {
			return store.Binding{}, Resolution{}, fmt.Errorf("restore worktree: %w", err)
		}
		if !exists {
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q: branch %s is not in %s; run resume from the repository the worktree was cut from", opts.Name, b.Branch, opts.CWD)
		}
		if err := rt.Git.CheckoutWorktree(ctx, opts.CWD, b.Worktree, b.Branch); err != nil {
			if errors.Is(err, git.ErrBranchCheckedOut) {
				return store.Binding{}, Resolution{}, fmt.Errorf("binding %q: branch %s is checked out in another worktree (git worktree list); free it, then resume", opts.Name, b.Branch)
			}
			return store.Binding{}, Resolution{}, fmt.Errorf("restore worktree: %w", err)
		}
		res.RestoredWorktree = b.Worktree
		res.RestoredBranch = b.Branch
	}

	rebinding := opts.Rebind || opts.Candidate != ""
	// A paused binding has no builder identity left -- pause cleared it -- so
	// a resume from PAUSED always rebinds; --rebind is implied (#137).
	if b.State == store.StatePaused {
		res.WasPaused = true
		rebinding = true
	}

	var (
		builder store.Endpoint
	)
	if rebinding {
		if b.State == store.StateDone && !restore {
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q is done: `relay bind` to start fresh", opts.Name)
		}
		// A legacy pane builder cannot be observed any more; it is rebound
		// to a headless endpoint without a liveness check.
		if b.Builder.Headless() {
			// The old builder is a process: ask the Runner.
			if rt.Runner == nil {
				return store.Binding{}, Resolution{}, ErrRunnerUnavailable
			}
			if b.Builder.PID != 0 {
				alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
				if err != nil {
					return store.Binding{}, Resolution{}, fmt.Errorf("check builder process: %w", err)
				}
				if alive {
					return store.Binding{}, Resolution{}, ErrBuilderAlive
				}
			}
		}
		{
			resCandidate, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), opts.Candidate, "builder")
			if err != nil {
				return store.Binding{}, Resolution{}, err
			}
			if opts.Tier != "" {
				tier := resolveTier(opts.Tier, resCandidate.Candidate, rt.Policy, "builder")
				if err := checkTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
					return store.Binding{}, Resolution{}, err
				}
				opts.Tier = string(tier)
				b.Tier = string(tier)
			} else if b.Tier != "" {
				opts.Tier = b.Tier
			}
		}
		var res2 Resolution
		builder, res2, err = resolveBuilder(ctx, rt, nil, opts, opts.Name)
		if err != nil {
			return store.Binding{}, Resolution{}, err
		}
		res2.RestoredWorktree = res.RestoredWorktree
		res2.RestoredBranch = res.RestoredBranch
		res2.WasPaused = res.WasPaused
		res = res2
	}

	var out store.Binding
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		if rebinding && b.State == store.StateDone && !restore {
			return fmt.Errorf("binding %q is done: `relay bind` to start fresh", opts.Name)
		}

		// RepoRef and Feature are deliberately left untouched here (beyond
		// the explicit Feature override below): a resume re-points endpoints,
		// it does not rediscover facts a fresh bind already captured. The
		// planner transcript locator is kept from the old endpoint.
		oldTranscriptLocator := b.Planner.TranscriptLocator
		b.Planner = planner
		b.Planner.TranscriptLocator = oldTranscriptLocator
		if opts.Feature != "" {
			b.Feature = opts.Feature
		}
		wasPaused := b.State == store.StatePaused
		b.State = store.StateActive
		// A resume or a rebind is a fresh attempt, so the previous round's
		// progress clock, stall, exploring and stale stamps say nothing about
		// it (#135).
		b.StalledSince = time.Time{}
		b.Progress = nil
		b.ExploringSince = time.Time{}
		b.StaleSince = time.Time{}
		b.StaleNotifiedAt = time.Time{}
		if rebinding {
			b.Builder = builder
			b.BuilderCandidate = res.Token()
			b.HaltNotifiedRound = 0
			b.Halt = ""
			b.HaltAt = time.Time{}
			b.RoundClosedTree = ""
			if opts.Tier != "" {
				b.Tier = opts.Tier
			}
		}

		if err := tx.Save(b); err != nil {
			return err
		}

		if rebinding && res.How != "" {
			if err := tx.AppendLog(opts.Name, pickEntry(rt.Now(), b.Round, "builder", res)); err != nil {
				return err
			}
		}

		// The resume entry is appended after the pick so it is the log's
		// last word on the binding: it is the event, the pick is a detail.
		if wasPaused {
			if err := tx.AppendLog(opts.Name, store.LogEntry{
				Round:     b.Round,
				Direction: store.DirToPlanner,
				Kind:      store.KindResume,
				Confirmed: true,
				Note:      "resumed",
			}); err != nil {
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

// resumeRemote is resume's remote-binding path (§4.6). A remote binding's
// mode is fixed at creation, so this never spawns anything: it only forwards to the server and, once the server agrees, repoints the
// planner and reactivates the binding locally.
//
// Errors: "cannot change a remote builder; unbind and add" when the caller
// asked to change the builder (--rebind or --candidate); ErrRemoteUnavailable; a wrapped server error; a wrapped git
// error; or a message naming the binding when its branch is gone.
func resumeRemote(ctx context.Context, rt Runtime, opts BindOptions, planner store.Endpoint, b store.Binding) (store.Binding, Resolution, error) {
	if opts.Rebind || opts.Candidate != "" {
		return store.Binding{}, Resolution{}, errors.New("cannot change a remote builder; unbind and add")
	}
	if rt.Remote == nil {
		return store.Binding{}, Resolution{}, ErrRemoteUnavailable
	}

	if _, rerr := rt.Remote.Resume(ctx, b.Builder.Server, b.Name); rerr != nil {
		var httpErr *client.HTTPError
		if errors.As(rerr, &httpErr) {
			return store.Binding{}, Resolution{}, fmt.Errorf("%s: %s", b.Builder.Server, httpErr.Body.Message)
		}
		if errors.Is(rerr, client.ErrUnreachable) {
			return store.Binding{}, Resolution{}, fmt.Errorf("%s unreachable: %w", b.Builder.Server, rerr)
		}
		return store.Binding{}, Resolution{}, fmt.Errorf("%s: %w", b.Builder.Server, rerr)
	}

	if rt.Git == nil {
		return store.Binding{}, Resolution{}, errors.New("git unavailable; cannot verify the branch")
	}
	exists, berr := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
	if berr != nil {
		return store.Binding{}, Resolution{}, fmt.Errorf("check branch %s: %w", b.Branch, berr)
	}
	if !exists {
		return store.Binding{}, Resolution{}, fmt.Errorf("binding %q: branch %s is gone; relay unbind, then relay add --server to start fresh", opts.Name, b.Branch)
	}

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		cur.Planner = planner
		cur.State = store.StateActive
		if err := tx.Save(cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	return out, Resolution{}, nil
}

// resolveGate applies the gate resolution rule (#132): --no-gate wins over
// everything, an explicit --gate is used as given, and an unset flag falls
// back to policy.json's gate.default.
func resolveGate(gate string, noGate bool, pol policy.Policy) string {
	if noGate {
		return ""
	}
	if gate != "" {
		return gate
	}
	return pol.GateDefault()
}

// resolveRegate applies the repair-round rule (#132 part 2): an explicit
// --regate is used as given (0 disables repair), and an unset flag falls back
// to policy.json's gate.regate.
func resolveRegate(regate *int, pol policy.Policy) int {
	if regate != nil {
		return *regate
	}
	return pol.GateRegate()
}

func create(ctx context.Context, rt Runtime, opts BindOptions, planner store.Endpoint) (store.Binding, Resolution, error) {
	name := opts.Name
	if name == "" {
		name = SanitizeName(baseName(opts.CWD))
	}
	if err := store.ValidName(name); err != nil {
		return store.Binding{}, Resolution{}, err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return store.Binding{}, Resolution{}, err
		}
	}

	// Refuse a name that is already taken, before anything is spawned. Save
	// would overwrite only bind.json: the round log and the NNN-*.md files
	// survive, so a fresh round 1 would collide with the previous session's
	// round 1 and Reconcile would read that old report entry as "already
	// handled" -- silently, with no error.
	if _, err := rt.Store.Load(name); err == nil {
		return store.Binding{}, Resolution{}, fmt.Errorf(
			"binding %q already exists: `relay unbind %s` to start fresh, or `relay bind --resume --name %s` to adopt it",
			name, name, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Binding{}, Resolution{}, err
	}

	// Check the working tree before recording anything. Save re-checks under
	// the lock and stays authoritative.
	other, found, err := rt.Store.FindByCWD(opts.CWD)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}
	if found && other.Name != name && other.State != store.StateDone {
		return store.Binding{}, Resolution{}, fmt.Errorf("%s is driven by binding %q (round %d): %w",
			opts.CWD, other.Name, other.Round, store.ErrCWDTaken)
	}

	var tier harness.Tier
	{
		resCandidate, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), opts.Candidate, "builder")
		if err != nil {
			return store.Binding{}, Resolution{}, err
		}
		tier = resolveTier(opts.Tier, resCandidate.Candidate, rt.Policy, "builder")
		if err := checkTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
			return store.Binding{}, Resolution{}, err
		}
		opts.Tier = string(tier)
	}

	builder, res, err := resolveBuilder(ctx, rt, nil, opts, name)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	b := store.Binding{
		Name:             name,
		CWD:              opts.CWD,
		Planner:          planner,
		Builder:          builder,
		BuilderCandidate: res.Token(),
		Round:            1,
		State:            store.StateActive,
		Tier:             string(tier),
		Gate:             resolveGate(opts.Gate, opts.NoGate, rt.Policy),
		Regate:           resolveRegate(opts.Regate, rt.Policy),
		RepoRef:          captureRepo(ctx, rt, opts.CWD),
		Feature:          opts.Feature,
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
		return store.Binding{}, Resolution{}, fmt.Errorf("bind: %w", err)
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

// builderAgentName composes the agent name recorded for a binding's builder.
func builderAgentName(name string) string {
	return name + "-builder"
}

// resolveBuilder resolves the builder candidate and records a headless
// endpoint; it spawns nothing (#99). Send fills in the process fields per
// round (spec §5.3). tx is unused and kept for its callers' shape.
func resolveBuilder(_ context.Context, rt Runtime, _ *store.Tx, opts BindOptions, name string) (store.Endpoint, Resolution, error) {
	res, err := resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), opts.Candidate, "builder")
	if err != nil {
		return store.Endpoint{}, Resolution{}, err
	}
	return store.Endpoint{AgentName: builderAgentName(name), Kind: res.Candidate.Harness, Mode: store.ModeHeadless}, res, nil
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
	// zero between rounds.
	ProcessStopped int
	ProcessErr     string
}

// Unbind clears away one binding's state.
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

	// A remote binding's server is told first (§4.6), same shape as Done: it
	// is asked to release the binding before anything local changes. A 404
	// means the server already considers it gone, which is not a reason to
	// refuse the local unbind -- it proceeds exactly as if the server had
	// agreed.
	if b.Builder.Remote() {
		if rt.Remote == nil {
			return UnbindResult{}, ErrRemoteUnavailable
		}
		if uerr := rt.Remote.Unbind(ctx, b.Builder.Server, b.Name); uerr != nil {
			var httpErr *client.HTTPError
			if !(errors.As(uerr, &httpErr) && httpErr.Status == 404) {
				if errors.Is(uerr, client.ErrUnreachable) {
					return UnbindResult{}, fmt.Errorf("%s unreachable: %w", b.Builder.Server, uerr)
				}
				return UnbindResult{}, fmt.Errorf("%s: %w", b.Builder.Server, uerr)
			}
		}
	}

	var res UnbindResult
	// Stop a live headless round before touching its tree (#99, spec §4.6):
	// a builder still writing would dirty the worktree relay is about to
	// judge clean or not. A failed stop is reported, never fatal -- the
	// unbind is the human's decision and it proceeds.
	if pid, err := stopProcess(ctx, rt, b.Builder, "unbind"); err != nil {
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

// SanitizeName coerces a directory name into a valid binding name.
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
