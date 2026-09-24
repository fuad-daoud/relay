package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrRoundOutOfRange reports a --round outside the source binding's history.
var ErrRoundOutOfRange = errors.New("round is outside the source binding's history")

// ErrNoBuilderCandidate reports a fork of an adopted-builder binding with no
// --builder to spawn from.
var ErrNoBuilderCandidate = errors.New("source binding has no builder candidate; pass --builder")

// ErrGitRequired reports a fork that needs a worktree with no git available.
var ErrGitRequired = errors.New("relevo fork needs git; pass --cwd to bind a tree yourself")

// ForkOptions describes one fork request.
type ForkOptions struct {
	Source  string // binding to fork from; required
	Round   int    // source round to copy through; required, 1..source.Round
	NewName string // name for the fork; required, must be free

	// Candidate is a harness/provider/model token; empty inherits the source's
	// BuilderCandidate; a source with none (an adopted builder) falls through
	// to resolveCandidate, so a one-candidate machine still forks without a flag.
	Candidate string

	// PlannerID is the caller's --planner value when it has one, and the
	// resolved record's id afterwards. Empty means "resolve this session's
	// planner" (§4.3).
	PlannerID string

	// CWD binds the fork to a directory the human already prepared instead of
	// creating a worktree. It is the escape hatch for a non-git tree; relevo
	// records no Worktree for it and will never remove it.
	CWD string

	// Headless makes the fork's builder a process relevo runs per round
	// instead of a pane (#99). Not inherited from the source: the mode is a
	// property of this binding, chosen at its creation.
	Headless bool

	// Tier overrides the candidate/policy permission tier (#141).
	Tier string

	// AllowYolo permits Tier == "yolo" above policy max_tier for this command (#141).
	AllowYolo bool

	// Gate is the acceptance command relevo runs on the fork's completion
	// marker (#132). Empty inherits the source binding's Gate; NoGate opts
	// out of that inheritance too.
	Gate string

	// NoGate opts this fork out of a gate even when the source binding has
	// one (#132). Ignored when Gate is set.
	NoGate bool

	// Regate is the fork's automatic repair-round budget (#132 part 2). nil
	// inherits the source binding's budget; an explicit 0 disables repair for
	// the fork even when the source has one.
	Regate *int

	// Feature is the human-given label grouping this binding with others
	// (#172); "" inherits the source binding's Feature. Validated by
	// store.ValidFeature when set.
	Feature string
}

// ForkResult is what a fork produced, so the CLI can tell the human where the
// new tree and branch are without re-deriving them.
type ForkResult struct {
	Binding  store.Binding
	Worktree string // "" when --cwd was used
	Branch   string // "" when --cwd was used
	Base     string // commit the worktree was cut from; "" when --cwd was used

	// Resolution is how the builder was chosen, for the pick line.
	Resolution Resolution
}

// Fork branches a new binding from the source's state as of a given round.
//
// Preconditions:  a relevo planner resolves for the caller (--planner,
//
//	$RELEVO_PLANNER, the host process, or the session); opts.Source exists;
//	1 <= opts.Round <= source.Round; opts.NewName is valid and
//	unused; the source round has a plan in the log.
//
// Postconditions: on success a new binding exists at round opts.Round+1, in
//
//	StateActive, with a running builder and its own working tree.
//	The source binding is byte-for-byte unchanged.
//
// Errors: store.ErrNotFound, ErrRoundOutOfRange, ErrNoBuilderCandidate,
//
//	ErrGitRequired, store.ErrCWDTaken, git.ErrBranchExists, or a wrapped
//	git failure. Rollback is described in §5.
func Fork(ctx context.Context, rt Runtime, opts ForkOptions) (ForkResult, error) {
	rec, haveRec, err := resolveVerbPlanner(rt, opts.PlannerID)
	if err != nil {
		return ForkResult{}, err
	}
	if err := store.ValidName(opts.NewName); err != nil {
		return ForkResult{}, err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return ForkResult{}, err
		}
	}
	// Refuse here, not just inside resolveBuilder: Fork cuts its worktree
	// before that runs, and a name relevo would refuse must not leave a
	// worktree behind. The composed name is discarded -- resolveBuilder
	// recomputes it.
	if _, err := builderAgentName(opts.NewName); err != nil {
		return ForkResult{}, err
	}

	if !haveRec {
		return ForkResult{}, ErrNoPlannerSession
	}
	opts.PlannerID = rec.ID
	plannerEP := recordEndpoint(rec)
	if plannerEP.TranscriptLocator == "" {
		plannerEP.TranscriptLocator = plannerLocator(rt, plannerEP.Kind, plannerEP.SessionID)
	}

	src, err := rt.Store.Load(opts.Source)
	if err != nil {
		return ForkResult{}, err
	}
	if src.Builder.Remote() {
		return ForkResult{}, errors.New("fork across servers is not supported")
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
			"binding %q already exists: `relevo unbind %s` to start fresh, or `relevo bind --resume --name %s` to adopt it",
			opts.NewName, opts.NewName, opts.NewName)
	} else if !errors.Is(err, store.ErrNotFound) {
		return ForkResult{}, err
	}

	token := opts.Candidate
	if token == "" {
		token = src.BuilderCandidate
	}
	// A fork inherits the source binding's writer role (#382 §2). No
	// checkWriterRole here: the source's role was checked when it was created.
	roleName := bindingRole(src)
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, Gates(rt), token, roleName)
	if err != nil && token == "" {
		return ForkResult{}, fmt.Errorf("%w (%v)", ErrNoBuilderCandidate, err)
	}
	if err != nil {
		return ForkResult{}, err
	}
	c := res.Candidate

	explicitTier := opts.Tier
	if explicitTier == "" && src.Tier != "" {
		explicitTier = src.Tier
	}
	tier := resolveRoleTier(explicitTier, c, rt.RoleRegistry(), roleName)
	if err := checkTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
		return ForkResult{}, err
	}

	explicitGate := opts.Gate
	if explicitGate == "" && src.Gate != "" {
		explicitGate = src.Gate
	}
	gate := resolveGateFor(explicitGate, opts.NoGate, rt.Policy, roleGates(rt.RoleRegistry(), roleName))

	// A fork inherits the source's repair budget unless the human named one
	// (#132 part 2): the source's own value already carries whatever policy
	// default applied when it was created.
	regate := src.Regate
	if opts.Regate != nil {
		regate = *opts.Regate
	}

	var (
		cwd      string
		worktree string
		branch   string
		base     string
		baseRef  string
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
		branch = "relevo/" + opts.NewName
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
		// The branch the cut came from, for `relevo land` (#136). The source
		// checkout is src.Repo -- src.CWD is the source binding's own tree --
		// and "" for a --cwd fork, where nothing was cut.
		if ref, err := rt.Git.CurrentBranch(ctx, src.Repo); err == nil {
			baseRef = ref
		}
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
			cwd, other.Name, other.BuilderCandidate, other.Round, store.ErrCWDTaken)
	}

	bindOpts := BindOptions{
		Name:      opts.NewName,
		Candidate: c.Ref().String(),
		PlannerID: opts.PlannerID,
		CWD:       cwd,
		Headless:  opts.Headless,
		Tier:      string(tier),
		AllowYolo: opts.AllowYolo,
		Role:      src.Role,
	}
	// Discard resolveBuilder's own resolution: bindOpts.Candidate is already
	// pinned to c (explicit), so resolveBuilder's internal resolveCandidate
	// call would report HowExplicit and lose the real How/Position/Skipped
	// resolved above -- res, from before the worktree was cut, is what the
	// pick entry and ForkResult.Resolution must carry.
	builder, _, err := resolveBuilder(ctx, rt, nil, bindOpts, opts.NewName)
	if err != nil {
		rollback()
		return ForkResult{}, err
	}
	if opts.Candidate == "" {
		res.InheritedFrom = src.Name
	}

	// RepoRef is copied from the source when it has one (they share a
	// working tree lineage); only a source bound before RepoRef existed
	// falls back to a fresh capture. Feature inherits the source's unless
	// the caller named its own.
	repoRef := src.RepoRef
	if repoRef == nil {
		repoRef = captureRepo(ctx, rt, src.CWD)
	}
	feature := opts.Feature
	if feature == "" {
		feature = src.Feature
	}

	b := store.Binding{
		Name:             opts.NewName,
		CWD:              cwd,
		Planner:          plannerEP,
		PlannerID:        opts.PlannerID,
		Builder:          builder,
		BuilderCandidate: c.Ref().String(),
		Round:            opts.Round + 1,
		State:            store.StateActive,
		RoundCap:         src.RoundCap,
		RoundTimeoutMS:   src.RoundTimeoutMS,
		Worktree:         worktree,
		Branch:           branch,
		Base:             base,
		BaseRef:          baseRef,
		ForkedFrom:       src.Name,
		ForkedAtRound:    opts.Round,
		Repo:             src.Repo,
		RepoRef:          repoRef,
		Feature:          feature,
		Tier:             string(tier),
		Role:             src.Role,
		Gate:             gate,
		Regate:           regate,
	}

	now := time.Now().UTC()
	if rt.Now != nil {
		now = rt.Now().UTC()
	}

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		return writeFork(tx, rt.Store, src.Name, b, opts.Round, now, pickEntry(now, b.Round, "builder", res))
	})
	if err != nil {
		rollback()
		return ForkResult{}, fmt.Errorf("fork failed after choosing builder %s: %w",
			builder.Kind, err)
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
		Binding:    b,
		Worktree:   worktree,
		Branch:     branch,
		Base:       base,
		Resolution: res,
	}, nil
}

// writeFork populates the new binding's state and saves it, under the caller's
// held lock. It is all-or-nothing: if any step fails, both the destination
// directory and (when Save created it) the binding's record are removed, so a
// failed fork leaves neither a record store.list could see nor a directory a
// later bind would collide with.
//
// Save comes before the appended entries because it is what creates the record
// they belong to; it is also what adopts the dst/log.jsonl ForkState wrote.
// The log order is fixed: the entries copied from src, then the fork note, then
// the pick.
//
// Preconditions:  the lock is held; b.Name has no record and no directory yet.
// Postconditions: on success, <root>/<b.Name>/ holds the copied round files and
//
//	the binding's record holds the forked entries, the fork note and the pick.
//	On ANY error, neither the record nor the directory exists.
func writeFork(tx *store.Tx, s *store.Store, src string, b store.Binding, throughRound int, now time.Time, pick store.LogEntry) error {
	success := false
	defer func() {
		if !success {
			_ = tx.Delete(b.Name)
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
	if err := tx.Save(b); err != nil {
		return err
	}
	if err := tx.AppendLog(b.Name, forkEntry); err != nil {
		return err
	}
	if err := tx.AppendLog(b.Name, pick); err != nil {
		return err
	}
	success = true
	return nil
}
