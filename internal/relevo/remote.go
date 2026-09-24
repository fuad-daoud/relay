package relevo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// ErrServerPreTier is a client-side refusal that happens before any server
// state changes: the server does not advertise the "tier" feature, so a
// requested --tier has nowhere to land.
var ErrServerPreTier = errors.New("server does not carry a permission tier")

// ErrNoGitIdentity is the client-side refusal for an add --server whose repo
// has no effective git identity (#335): a remote builder commits as the
// client, so there is nobody to commit as. It is returned before anything
// is created, on the server or locally.
var ErrNoGitIdentity = errors.New("no git identity")

// orText returns s, or fallback when s is empty.
func orText(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func is40Hex(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func addRemote(ctx context.Context, rt Runtime, opts AddOptions, rec planner.Record, haveRec bool) (result AddResult, err error) {
	// The caller's planner is a hard precondition here exactly as it is on the
	// local path: a remote binding records the client planner's id and
	// session, so without a resolved record there is nothing to record and
	// nothing may be created on the server.
	if !haveRec {
		return AddResult{}, ErrNoPlannerSession
	}
	opts.PlannerID = rec.ID
	plannerEP := recordEndpoint(rec)
	if plannerEP.TranscriptLocator == "" {
		plannerEP.TranscriptLocator = plannerLocator(rt, plannerEP.Kind, plannerEP.SessionID)
	}

	if opts.CWD != "" {
		return AddResult{}, errors.New("remote builders are add-only: --cwd and --server cannot be combined")
	}
	if rt.Remote == nil {
		return AddResult{}, ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return AddResult{}, ErrGitRequired
	}

	// 1. store.ValidName; binding must not exist locally.
	if err := store.ValidName(opts.Name); err != nil {
		return AddResult{}, err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return AddResult{}, err
		}
	}
	if _, err := rt.Store.Load(opts.Name); err == nil {
		return AddResult{}, fmt.Errorf("binding %q already exists locally", opts.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return AddResult{}, err
	}

	// 1.5. --branch adopts an existing branch: the same driven-by-live-binding
	// guard and local/origin resolution as Add, and its tip overrides --base.
	// No worktree is made on the client for a remote binding, so there is no
	// CheckoutWorktree here.
	branch := "relevo/" + opts.Name
	existingBranch := false
	base := opts.Base
	if opts.Branch != "" {
		if opts.Base != "" {
			return AddResult{}, errors.New("--base and --branch are exclusive")
		}
		if err := branchDrivenByLiveBinding(rt, opts.Branch); err != nil {
			return AddResult{}, err
		}
		exists, err := rt.Git.BranchExists(ctx, opts.Repo, opts.Branch)
		if err != nil {
			return AddResult{}, err
		}
		if !exists {
			originRef := "origin/" + opts.Branch
			_, ok, err := rt.Git.RefSHA(ctx, opts.Repo, "refs/remotes/"+originRef)
			if err != nil {
				return AddResult{}, err
			}
			if !ok {
				return AddResult{}, fmt.Errorf("branch %q not found locally or on origin", opts.Branch)
			}
			if err := rt.Git.CreateTrackingBranch(ctx, opts.Repo, opts.Branch, originRef); err != nil {
				return AddResult{}, err
			}
		}
		tip, ok, err := rt.Git.RefSHA(ctx, opts.Repo, "refs/heads/"+opts.Branch)
		if err != nil {
			return AddResult{}, err
		}
		if !ok {
			return AddResult{}, fmt.Errorf("branch %q vanished", opts.Branch)
		}
		branch = opts.Branch
		existingBranch = true
		base = tip
	} else {
		// 2. base := opts.Base if given else rt.Git.HeadCommit(opts.Repo)
		// resolve to a full sha with RefSHA(opts.Repo, base) when it is not 40-hex; missing -> error
		if base == "" {
			var err error
			base, err = rt.Git.HeadCommit(ctx, opts.Repo)
			if err != nil {
				return AddResult{}, fmt.Errorf("head commit: %w", err)
			}
		}
		if !is40Hex(base) {
			sha, ok, err := rt.Git.RefSHA(ctx, opts.Repo, base)
			if err != nil {
				return AddResult{}, fmt.Errorf("resolve base %s: %w", base, err)
			}
			if !ok {
				return AddResult{}, fmt.Errorf("base %q not found", base)
			}
			base = sha
		}
	}

	// 3. root := rt.Git.RootCommit(opts.Repo); repoID := remote.RepoID(root)
	root, err := rt.Git.RootCommit(ctx, opts.Repo)
	if err != nil {
		return AddResult{}, fmt.Errorf("root commit: %w", err)
	}
	repoID, err := remote.RepoID(root)
	if err != nil {
		return AddResult{}, fmt.Errorf("repo id: %w", err)
	}

	// 3.5. name, email := rt.Git.Identity(opts.Repo): the identity the remote
	// builder commits as (#335). Resolved here, before the candidate check
	// and before anything that touches the server, so a missing identity
	// refuses with no binding on the server, no local binding and no branch.
	authorName, authorEmail, err := rt.Git.Identity(ctx, opts.Repo)
	if err != nil {
		return AddResult{}, fmt.Errorf("git identity for %s: %w", opts.Repo, err)
	}
	if authorName == "" || authorEmail == "" {
		return AddResult{}, fmt.Errorf("%w for %s: a remote builder commits as you; set git config user.name and git config user.email (in the repo or --global)", ErrNoGitIdentity, opts.Repo)
	}

	// 4. candidate := opts.Candidate; if non-empty, rt.Remote.Candidates(server) must list it, by canonical
	// token or by name (else error naming the server's candidates); if empty, leave "" and let the server pick.
	// The canonical token is what travels.
	candidateStr := opts.Candidate
	if candidateStr != "" {
		candResp, err := rt.Remote.Candidates(ctx, opts.Server)
		if err != nil {
			return AddResult{}, fmt.Errorf("list remote candidates: %w", err)
		}
		found := false
		if view, ok := matchCandidateView(candResp.Candidates, candidateStr); ok {
			candidateStr, found = view.Token, true
		} else if !strings.Contains(candidateStr, "/") {
			// A name this client knows resolves to its canonical token,
			// which the server may list under that token.
			if c, rerr := rt.Candidates.Resolve(candidateStr); rerr == nil {
				if view, ok := matchCandidateView(candResp.Candidates, c.Ref().String()); ok {
					candidateStr, found = view.Token, true
				}
			}
		}
		if !found {
			available := make([]string, len(candResp.Candidates))
			for i, c := range candResp.Candidates {
				available[i] = c.Token
				if c.Name != "" {
					available[i] = fmt.Sprintf("%s (%s)", c.Name, c.Token)
				}
			}
			return AddResult{}, fmt.Errorf("candidate %q not available on %s (available: %s)", opts.Candidate, opts.Server, strings.Join(available, ", "))
		}
	}

	// 4.5. feature probe: a requested --tier or custom --role requires the
	// server to advertise the matching feature before any binding is created
	// there. One WhoAmI answers both, and it is called at most once.
	wireTier := ""
	if opts.Tier != "" {
		t, err := harness.ParseTier(opts.Tier)
		if err != nil {
			return AddResult{}, err
		}
		if err := checkTierCap(t, rt.Policy, opts.AllowYolo); err != nil {
			return AddResult{}, err
		}
		wireTier = string(t)
	}
	wireRole := normRole(opts.Role)
	if opts.Tier != "" || wireRole != "" {
		who, err := rt.Remote.WhoAmI(ctx, opts.Server)
		if err != nil {
			return AddResult{}, err
		}
		if opts.Tier != "" && !slices.Contains(who.Features, remote.FeatureTier) {
			return AddResult{}, fmt.Errorf("%w: server %s does not carry a permission tier (pre-tier server); upgrade it or drop --tier", ErrServerPreTier, opts.Server)
		}
		// The client's roles.json never travels (§5.3): the server resolves
		// the role against its own. A server too old to do that would ignore
		// the field and run its builder, so it is refused here -- before any
		// branch, worktree or create call.
		if wireRole != "" && !slices.Contains(who.Features, remote.FeatureRoles) {
			return AddResult{}, fmt.Errorf("server %s does not run custom roles (role %q); upgrade it", opts.Server, opts.Role)
		}
	}

	// 5. view := rt.Remote.CreateBinding(ctx, server, {Name, RepoID, BaseCommit: base, Candidate, RoundCap, RoundTimeoutMS, Tier})
	// HTTPError 409 -> the server already has this binding for this client, with
	// no local counterpart here; only `relevo serve unbind` on the server can
	// clear that, since a local `relevo bind --resume` has nothing to resume.
	createReq := remote.CreateBindingRequest{
		Name:       opts.Name,
		RepoID:     repoID,
		BaseCommit: base,
		Candidate:  candidateStr,
		Tier:       wireTier,
		Role:       wireRole,
		Author:     &remote.GitIdentity{Name: authorName, Email: authorEmail},
	}
	view, err := rt.Remote.CreateBinding(ctx, opts.Server, createReq)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 409 {
			return AddResult{}, fmt.Errorf("binding %s already exists on %s for this client but not here; relevo serve unbind --owner <your label> %s on the server, or choose another name", opts.Name, opts.Server, opts.Name)
		}
		if errors.As(err, &httpErr) && httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax {
			return AddResult{}, fmt.Errorf("%w: %s", ErrTierAboveMax, httpErr.Body.Message)
		}
		return AddResult{}, err
	}

	// From here on the server has a binding this client does not yet. Every
	// later failure (branch exists, CreateBranch error, Save error including
	// store.ErrCWDTaken, log append error) unbinds it on the server
	// best-effort before returning the original error, so a client-side
	// refusal never leaves an orphaned server binding behind (#100). Once
	// CreateBranch itself has succeeded, the same failure also removes the
	// local branch relevo just cut -- server first, since that binding is the
	// one another client could see (#100 round 4).
	created := true
	branchCreated := false
	defer func() {
		if !created || err == nil {
			return
		}
		if unbindErr := rt.Remote.Unbind(ctx, opts.Server, opts.Name); unbindErr != nil {
			slog.Warn("unbind after failed add", "server", opts.Server, "name", opts.Name, "err", unbindErr)
			err = fmt.Errorf("%w; server binding %s on %s could not be removed: %v", err, opts.Name, opts.Server, unbindErr)
		}
		if branchCreated {
			if delErr := rt.Git.DeleteBranch(ctx, opts.Repo, branch); delErr != nil {
				slog.Warn("delete branch after failed add", "repo", opts.Repo, "branch", branch, "err", delErr)
				err = fmt.Errorf("%w; local branch %s could not be removed: %v", err, branch, delErr)
			}
		}
	}()

	// 6. rt.Git.CreateBranch(opts.Repo, "relevo/"+name, base) -- after the server agreed, so a refused create leaves no branch
	// ErrBranchExists -> error "branch relevo/api exists; delete it or pick another name"
	// In --branch mode the branch already exists and relevo did not create it:
	// there is nothing to create, and branchCreated stays false so the
	// deferred cleanup above never deletes a branch relevo did not make.
	if !existingBranch {
		if err := rt.Git.CreateBranch(ctx, opts.Repo, branch, base); err != nil {
			if errors.Is(err, git.ErrBranchExists) {
				return AddResult{}, fmt.Errorf("branch relevo/%s exists; delete it or pick another name", opts.Name)
			}
			return AddResult{}, err
		}
		branchCreated = true
	}

	// 7. b := Binding{Name, CWD: opts.Repo, Repo: opts.Repo, Branch, Base: base, Planner: <as Add fills it>,
	//                 Builder: Endpoint{Mode: ModeRemote, Server: server, Kind: <kind from view.Candidate's harness, "" if unknown>,
	//                                   AgentName: name},
	//                 BuilderCandidate: view.Candidate, Round: 1, State: active, RoundCap/Timeout as Add}
	// Save under lock; append the same pick log entry Add writes, with the candidate the server reported.
	builderKind := ""
	var cand candidate.Candidate
	if view.Candidate != "" {
		if ref, err := candidate.ParseRef(view.Candidate); err == nil {
			builderKind = ref.Harness
			cand = candidate.Candidate{Harness: ref.Harness, Provider: ref.Provider, Model: ref.Model}
		}
	}

	b := store.Binding{
		Name:   opts.Name,
		CWD:    opts.Repo,
		Repo:   opts.Repo,
		Branch: branch,
		Base:   base,
		// ExistingBranch records that relevo adopted a branch it did not
		// create, so nothing here will ever delete it.
		ExistingBranch: existingBranch,
		Planner:        plannerEP,
		PlannerID:      opts.PlannerID,
		Builder: store.Endpoint{
			Mode:      store.ModeRemote,
			Server:    opts.Server,
			Kind:      builderKind,
			AgentName: opts.Name,
		},
		BuilderCandidate: view.Candidate,
		Round:            1,
		State:            store.StateActive,
		Tier:             view.Tier,
		// The role travels as the client asked it: the server resolved it
		// against its own roles.json, and the mirror records it so
		// `relevo status` shows it (#382 §5.3).
		Role: wireRole,
		// rt.Git is guaranteed non-nil here (checked at the top of
		// addRemote), and opts.Repo is the client's local checkout the
		// branch and bundle are cut from -- the same "parent repo" concept
		// captureRepo uses for the local Add path.
		RepoRef: captureRepo(ctx, rt, opts.Repo),
		Feature: opts.Feature,
	}
	res := Resolution{
		Candidate: cand,
		How:       HowExplicit,
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		if view.Candidate != "" {
			return tx.AppendLog(b.Name, remotePickEntry(rt.Now(), opts.Server, view.Candidate, opts.Candidate != "", 1))
		}
		return nil
	}); err != nil {
		return AddResult{}, fmt.Errorf("save remote binding: %w", err)
	}

	if stored, err := rt.Store.Load(b.Name); err == nil {
		b = stored
	}

	created = false
	return AddResult{
		Binding:    b,
		Worktree:   "",
		Branch:     branch,
		Base:       base,
		Resolution: res,
	}, nil
}

// remotePickEntry is addRemote's and sendRemote's pick log entry: the same
// shape pickEntry writes (DirToPlanner, KindPick, Confirmed) but naming the
// server and whether the token was named by the planner or picked by the
// server's own policy -- ExplainResolution's "explicit, policy bypassed"
// wording assumes a local resolveCandidate call that never ran here, so it
// would misdescribe a token the server picked on its own. round is the round
// the pick is filed under: 1 for a fresh binding, the sent round for a
// `relevo send --builder` (#318).
func remotePickEntry(now time.Time, server, token string, explicit bool, round int) store.LogEntry {
	how := "server's pick"
	if explicit {
		how = "explicit"
	}
	return store.LogEntry{
		TS: now.UTC(), Round: round, Direction: store.DirToPlanner,
		Kind: store.KindPick, Confirmed: true,
		Note: fmt.Sprintf("picked %s on %s: %s", token, server, how),
	}
}

func sendRemote(ctx context.Context, rt Runtime, b store.Binding, planBody []byte, tier, builder string) (SendResult, error) {
	if rt.Remote == nil {
		return SendResult{}, ErrRemoteUnavailable
	}
	if rt.Git == nil {
		return SendResult{}, ErrGitRequired
	}
	if rt.Transport == nil {
		return SendResult{}, errors.New("no remote transport configured")
	}

	server := b.Builder.Server

	// The cap check already ran in Send; do not repeat it here. One WhoAmI
	// answers everything this function asks the server about itself: whether a
	// requested tier or builder is supported, each of which needs a server that
	// advertises it, and whether a repeated send is safe to retry (#373 §4.4).
	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return SendResult{}, err
	}
	if tier != "" && !slices.Contains(who.Features, remote.FeatureTier) {
		return SendResult{}, fmt.Errorf("%w: server %s does not carry a permission tier (pre-tier server); upgrade it or drop --tier", ErrServerPreTier, server)
	}
	if builder != "" && !slices.Contains(who.Features, remote.FeatureBuilder) {
		return SendResult{}, fmt.Errorf("server %s cannot change a binding's builder (no %q feature); upgrade it, or send without --builder", server, remote.FeatureBuilder)
	}

	name := b.Name

	// 1. rt.Git.UpdateRef(b.Repo, "refs/relevo/"+b.Name+"/out", RefSHA("refs/heads/"+b.Branch), "")
	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	branchSHA, ok, err := rt.Git.RefSHA(ctx, b.Repo, branchRef)
	if err != nil {
		return SendResult{}, fmt.Errorf("resolve branch %s: %w", b.Branch, err)
	}
	if !ok {
		return SendResult{}, fmt.Errorf("branch %s not found", b.Branch)
	}
	outRef := "refs/relevo/" + name + "/out"
	if err := rt.Git.UpdateRef(ctx, b.Repo, outRef, branchSHA, ""); err != nil {
		return SendResult{}, fmt.Errorf("update ref %s: %w", outRef, err)
	}

	// 2. snap := rt.Transport.Snapshot(b.Repo, ["refs/relevo/<name>/out"], b.Builder.LastShipped)
	// ErrSinceUnknown -> treat as first send: Snapshot with since ""
	snap, err := rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, b.Builder.LastShipped)
	if err != nil {
		if errors.Is(err, remote.ErrSinceUnknown) {
			snap, err = rt.Transport.Snapshot(ctx, b.Repo, []string{outRef}, "")
		}
		if err != nil {
			return SendResult{}, fmt.Errorf("snapshot %s: %w", outRef, err)
		}
	}
	defer func() {
		if snap.Body != nil {
			_ = snap.Body.Close()
		}
	}()

	var bundleReader io.Reader
	if !snap.Empty && snap.Body != nil {
		bundleReader = snap.Body
	}

	// 2b. The client's tags travel as data beside the bundle (#242): a tag on
	// an ancestor of the shipped branch already has its commit on the server.
	// A broken repo is a real pre-send failure; no local state is written.
	tagsMap, err := rt.Git.ListTags(ctx, b.Repo)
	if err != nil {
		return SendResult{}, fmt.Errorf("list tags: %w", err)
	}
	tags := make([]remote.TagRef, 0, len(tagsMap))
	for tagName, sha := range tagsMap {
		tags = append(tags, remote.TagRef{Name: tagName, SHA: sha})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })

	// 3. view, err := rt.Remote.StartRound(ctx, server, name, b.Round, planBody, snap.Body or nil when Empty, tier, builder, tags)
	// The retry is safe only on a server that dedupes a repeated send (#373
	// §4.5): without idempotent_send a retry could re-queue a round that did
	// start, so a gateway error is reported as unreachable instead.
	view, err := rt.Remote.StartRound(ctx, server, name, b.Round, planBody, bundleReader, tier, builder, tags,
		slices.Contains(who.Features, remote.FeatureIdempotentSend))
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) {
			if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundStarted {
				// proceed as success
				view.RoundState = remote.RoundRunning
			} else if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundOpen {
				return SendResult{}, fmt.Errorf("round %d is running on %s", b.Round, server)
			} else if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundHalted {
				return SendResult{}, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, httpErr.Body.Message)
			} else if httpErr.Status == 422 && httpErr.Body.Code == remote.CodeTierAboveMax {
				return SendResult{}, fmt.Errorf("%w: %s", ErrTierAboveMax, httpErr.Body.Message)
			} else if httpErr.Status == 422 {
				return SendResult{}, fmt.Errorf("%s: %s", server, httpErr.Body.Message)
			} else {
				return SendResult{}, fmt.Errorf("%s: %s", server, httpErr.Error())
			}
		} else if errors.Is(err, client.ErrUnreachable) {
			cause := strings.TrimPrefix(err.Error(), client.ErrUnreachable.Error()+": ")
			return SendResult{}, fmt.Errorf("%s unreachable: %s", server, cause)
		} else {
			return SendResult{}, err
		}
	}

	// A server built before this plan may still answer 201 with a
	// needs_you view for a round that could not start, rather than the 409
	// round_halted handled above; treat it the same way. Nothing is
	// written: the human's re-send must not look like it succeeded.
	if view.RoundState == remote.RoundNeedsYou {
		return SendResult{}, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, orText(view.Halt, "no reason given"))
	}

	// UNDER the lock:
	// 4. reload b; if b.Round != the round sent -> error "round advanced during send; run relevo status" (nothing recorded)
	// 5. write PlanPath(name, round) = planBody; AppendLog plan to_builder; b.RoundStartedAt = now;
	// b.State = active; b.Halt = ""; b.Builder.LastShipped = snap.Heads["refs/relevo/<name>/out"];
	// b.Builder.RemoteStatus = string(view.RoundState); Save.
	var sendRound int
	var pickLine string
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(name)
		if err != nil {
			return err
		}
		if cur.Round != b.Round {
			return errors.New("round advanced during send; run relevo status")
		}
		sendRound = cur.Round

		now := rt.Now()

		// A --builder change is applied by the server (the served path's own
		// §5.2 write). The client records the candidate the server
		// canonicalised, so the next observeRemote sees view.Candidate ==
		// b.BuilderCandidate and writes no spurious "switched on <server>".
		// The pick entry is filed under the sent round, before its plan entry.
		if builder != "" && view.Candidate != "" && view.Candidate != cur.BuilderCandidate {
			pick := remotePickEntry(now, server, view.Candidate, true, cur.Round)
			if err := tx.AppendLog(name, pick); err != nil {
				return fmt.Errorf("append builder pick log: %w", err)
			}
			pickLine = pick.Note
			cur.BuilderCandidate = view.Candidate
			kind := ""
			if ref, err := candidate.ParseRef(view.Candidate); err == nil {
				kind = ref.Harness
			}
			cur.Builder.Kind = kind
		}

		planPath := rt.Store.PlanPath(name, cur.Round)
		if err := os.WriteFile(planPath, planBody, 0o644); err != nil {
			return fmt.Errorf("write plan %s: %w", planPath, err)
		}

		if err := tx.AppendLog(name, store.LogEntry{
			TS:        now.UTC(),
			Round:     cur.Round,
			Direction: store.DirToBuilder,
			Kind:      store.KindPlan,
			Path:      planPath,
			Confirmed: true,
		}); err != nil {
			return fmt.Errorf("append plan log: %w", err)
		}

		cur.RoundStartedAt = now
		cur.State = store.StateActive
		cur.Halt = ""
		if snap.Heads != nil {
			cur.Builder.LastShipped = snap.Heads[outRef]
		}
		cur.Builder.RemoteStatus = string(view.RoundState)
		return tx.Save(cur)
	})
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{Round: sendRound, Pick: pickLine}, nil
}

const unreachableGrace = 30 * time.Minute

// checkedOutWarned records the bindings whose "checked out" hint catchUp has
// already logged at Info in this process, so the hint does not repeat on every
// SyncRemote (#253). Process-local on purpose: the daemon and `relevo wait` are
// separate processes and each says it once.
var checkedOutWarned sync.Map // binding name -> struct{}

func writeTempAndRename(dest string, r io.Reader) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(dest)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func mirrorLog(ctx context.Context, rt Runtime, server, name string, round int) {
	path := rt.Store.BuilderLogPath(name, round)
	var local int64
	if fi, err := os.Stat(path); err == nil {
		local = fi.Size()
	}
	rc, fr, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", local)
	if err != nil || rc == nil {
		if err != nil {
			slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
		}
		return
	}

	switch {
	case fr.Honored && fr.From == local && fr.Size >= local:
		defer rc.Close()
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Warn("write builder log failed", "path", path, "err", err)
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			slog.Warn("write builder log failed", "path", path, "err", err)
			return
		}
		defer f.Close()
		if _, err := io.Copy(f, rc); err != nil {
			slog.Warn("write builder log failed", "path", path, "err", err)
		}
	case fr.Honored && fr.Size < local:
		_ = rc.Close()
		rc2, _, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", 0)
		if err != nil || rc2 == nil {
			if err != nil {
				slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			}
			return
		}
		defer rc2.Close()
		if err := writeTempAndRename(path, rc2); err != nil {
			slog.Warn("write builder log failed", "path", path, "err", err)
		}
	default:
		defer rc.Close()
		if err := writeTempAndRename(path, rc); err != nil {
			slog.Warn("write builder log failed", "path", path, "err", err)
		}
	}
}

var mirrorDriftAttempts sync.Map // "name/round" -> struct{}

func mirrorDriftOnce(ctx context.Context, rt Runtime, tx *store.Tx, server, name string, round int) {
	driftPath := rt.Store.DriftPath(name, round)
	if _, err := rt.Store.ReadFile(driftPath); err == nil {
		return
	}
	key := fmt.Sprintf("%s/%d", name, round)
	if _, loaded := mirrorDriftAttempts.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	rc, err := rt.Remote.RoundFile(ctx, server, name, round, "drift")
	if err != nil || rc == nil {
		if err != nil {
			slog.Debug("mirror drift not available", "server", server, "name", name, "round", round, "err", err)
		}
		return
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		slog.Warn("mirror drift read failed", "server", server, "name", name, "round", round, "err", err)
		return
	}
	if err := tx.PutRoundFile(name, round, driftPath, data); err != nil {
		slog.Warn("write drift round file failed", "path", driftPath, "err", err)
	}
}

// observeRemote advances b from the server's view: planner refresh, candidate
// refresh, mirrored log, and (on a newly closed round) catchUp -- everything
// reconcileRemote used to do, except the final delivery to the planner pane.
// deliver reports whether the caller should follow up with deliverAndSettle;
// it is false for every path that already returned on its own in the
// original function (a running mirror, a fresh halt, an unreachable/cert/
// other error).
//
// The split exists for SyncRemote (spec §2.2): a read path must observe the
// server's state without ever calling deliverAndSettle, because a CLI one-shot
// has no business claiming a pending payload out from under the daemon.
func liveFactsOf(v *remote.LiveView) *store.LiveFacts {
	if v == nil {
		return nil
	}
	var tail []string
	if v.Tail != nil {
		tail = make([]string, len(v.Tail))
		copy(tail, v.Tail)
	}
	var u *usage.Usage
	if v.Usage != nil {
		copyU := *v.Usage
		u = &copyU
	}
	var diff *store.DiffFacts
	if v.Diff != nil {
		diff = &store.DiffFacts{
			Files:   v.Diff.Files,
			Added:   v.Diff.Added,
			Removed: v.Diff.Removed,
		}
	}
	return &store.LiveFacts{
		At:             v.At,
		PID:            v.PID,
		StartedAt:      v.StartedAt,
		ExitCode:       v.ExitCode,
		Tail:           tail,
		Usage:          u,
		Diff:           diff,
		LastProgressAt: v.LastProgressAt,
		ExploringSince: v.ExploringSince,
		GatingSince:    v.GatingSince,
	}
}

func observeRemote(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, error) {
	if rt.Remote == nil {
		slog.Warn("remote client not configured", "binding", b.Name)
		return b, false, nil
	}

	now := rt.Now().UTC()
	server := b.Builder.Server
	name := b.Name

	view, err := rt.Remote.GetBinding(ctx, server, name)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) {
			if httpErr.Status == 401 {
				// A revoked key is permanent: halt at once, as it always did.
				if httpErr.Body.Code == remote.CodeRevoked {
					b, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s: %s", name, server, httpErr.Body.Message))
					return b, false, err
				}
				// Every other 401 -- stale, bad_signature, not_enrolled -- may
				// clear on its own (#373 §4.6): a reboot's clock skew is the
				// common case. Show it, warn once, and halt only after the
				// grace. A CLI one-shot (nil AuthGrace) never halts.
				first := rt.AuthGrace.Note(name, now)
				if first.Equal(now) {
					slog.Warn("transient auth error", "server", server, "binding", name, "code", string(httpErr.Body.Code))
				}
				b.Builder.RemoteStatus = "auth: " + string(httpErr.Body.Code)
				if rt.AuthGrace.Expired(name, now, authGraceLimit) {
					dur := now.Sub(first).Truncate(time.Second)
					b, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s: %s for %s -- check this machine's clock and relevo config server list",
						name, server, httpErr.Body.Code, dur))
					return b, false, err
				}
				return b, false, nil
			}
			if httpErr.Status == 404 {
				b, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s: binding removed by the server admin", name, server))
				return b, false, err
			}
		}
		if errors.Is(err, client.ErrUnreachable) {
			if b.RemoteUnreachableSince.IsZero() {
				b.RemoteUnreachableSince = now
				slog.Warn(fmt.Sprintf("%s unreachable", server), "server", server, "binding", name)
			}
			b.Builder.RemoteStatus = "unreachable"

			entries, rerr := tx.ReadLog(name)
			roundOpen := rerr == nil &&
				HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
				!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport)

			dur := now.Sub(b.RemoteUnreachableSince).Truncate(time.Second)
			if roundOpen && now.Sub(b.RemoteUnreachableSince) > roundBudget(b)+unreachableGrace {
				b, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s unreachable for %s; round %d may still be running there",
					name, server, dur, b.Round))
				return b, false, err
			}
			return b, false, nil
		}
		if errors.Is(err, client.ErrCertChanged) {
			if b.Builder.RemoteStatus != "cert" {
				slog.Warn("server certificate changed", "server", server, "binding", name)
			}
			b.Builder.RemoteStatus = "cert"
			return b, false, nil
		}
		slog.Warn("remote get binding failed", "server", server, "binding", name, "err", err)
		return b, false, nil
	}

	// A poll that got through means any transient auth error is over, so the
	// next one starts its own grace (#373 §4.6).
	rt.AuthGrace.Clear(name)

	b.RemoteUnreachableSince = time.Time{}
	b.Builder.RemoteStatus = string(view.RoundState)
	// RemoteQueue and RemoteLive are set only in their respective cases below;
	// every other state clears them, including a round-closed catchUp (#285).
	b.Builder.RemoteQueue = nil
	b.Builder.RemoteLive = nil

	// A server-side switch (#100): the candidate that actually ran differs
	// from what this binding last recorded. Refresh the token and the
	// harness kind, and log it the same way a local mid-round switch does,
	// so usage and status name the builder that actually ran.
	if view.Candidate != "" && view.Candidate != b.BuilderCandidate {
		prev := b.BuilderCandidate
		b.BuilderCandidate = view.Candidate
		kind := ""
		if ref, err := candidate.ParseRef(view.Candidate); err == nil {
			kind = ref.Harness
		}
		b.Builder.Kind = kind
		if err := tx.AppendLog(name, store.LogEntry{
			TS: now, Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindSwitch,
			Note:      fmt.Sprintf("switched on %s: %s -> %s", server, prev, view.Candidate),
			Confirmed: true,
		}); err != nil {
			return b, false, err
		}
	}

	switch view.RoundState {
	case remote.RoundQueued:
		// A queued round (#285) has no process and no clocks: it behaves
		// like RoundRunning minus the stall copy -- no halt, no catch-up,
		// and any stall stamp from an earlier running round no longer
		// applies (the process is gone).
		b.StalledSince = time.Time{}
		if view.Queue != nil {
			b.Builder.RemoteQueue = &store.QueueFacts{
				Position: view.Queue.Position,
				Ahead:    view.Queue.Ahead,
				Running:  view.Queue.Running,
				Cap:      view.Queue.Cap,
				Since:    view.Queue.Since,
			}
		}
		return b, false, nil

	case remote.RoundRunning:
		// The server is the only place that can see the builder's stream; a
		// running round carries its stall stamp across so the client shows
		// the same "stalled <age>" (#252).
		b.StalledSince = view.StalledSince
		b.Builder.RemoteLive = liveFactsOf(view.Live)
		mirrorLog(ctx, rt, server, name, b.Round)
		mirrorDriftOnce(ctx, rt, tx, server, name, b.Round)
		return b, false, nil

	case remote.RoundNeedsYou:
		b.StalledSince = view.StalledSince
		b, err := haltBinding(ctx, rt, b, name+": "+view.Halt)
		return b, false, err

	case remote.RoundClosed:
		if view.ClosedRound >= b.Round {
			next, err := catchUp(ctx, rt, tx, b, view)
			return next, true, err
		}
		return b, true, nil

	case remote.RoundIdle:
		if b.State == store.StateBroken {
			b.State = store.StateActive
		}
		// A lost reply to /ack leaves the server Idle with this round closed
		// while the client never recorded the report (#373 §4.5). A repeat ack
		// is idempotent on the server, so catching up again is safe.
		entries, rerr := tx.ReadLog(name)
		if rerr == nil && view.ClosedRound >= b.Round &&
			!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport) {
			next, err := catchUp(ctx, rt, tx, b, view)
			return next, true, err
		}
		return b, true, nil

	default:
		return b, true, nil
	}
}

// reconcileRemote is the daemon tick's entry point for a remote binding: it
// observes the server's state and, unless observeRemote already returned
// (a halt, a running mirror, an error), delivers any pending payload.
func reconcileRemote(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	next, deliver, err := observeRemote(ctx, rt, tx, b)
	if err != nil || !deliver {
		return next, err
	}
	return deliverAndSettle(ctx, rt, tx, next)
}

// SyncRemote runs one read-only observe pass over every remote binding that
// is still relaying (not store.StateDone), so `relevo status`, `relevo wait`
// and each `relevo wait` iteration collect a closed round without the daemon
// running (spec §2.2). It never delivers: it calls observeRemote directly,
// not reconcileRemote, so a payload stays pending for the daemon, the channel
// or `relevo wait` to take.
//
// synced counts bindings whose stored state actually changed under the
// pass; per-binding errors are joined into one returned error rather than
// aborting, so one unreachable server does not stop another binding's sync.
// A halt observeRemote decides on (a 401, a 404, an unreachable round past
// its budget) still fires: only delivery is skipped here, not the same
// observations the daemon would make.
func SyncRemote(ctx context.Context, rt Runtime) (int, error) {
	if rt.Remote == nil {
		return 0, nil
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return 0, err
	}

	synced := 0
	var errs []error
	for _, b := range bindings {
		if !b.Builder.Remote() || b.State == store.StateDone {
			continue
		}
		name := b.Name
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			fresh, err := tx.Load(name)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			next, _, err := observeRemote(ctx, rt, tx, fresh)
			if err != nil {
				return err
			}
			if store.SameBinding(next, fresh) {
				return nil
			}
			synced++
			return tx.Save(next)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	return synced, errors.Join(errs...)
}

// ServerProbe is one configured server's reachability and enrollment, as
// `relevo config server list` and `relevo doctor` both report it (spec §5.5, §4.7).
type ServerProbe struct {
	Name  string
	URL   string
	State string // "enrolled" | "not enrolled" | "unreachable" | "cert changed" | "no key" | "error"
	Label string // enrolled as
	// Detail is the enrollment line for "not enrolled", the failure cause
	// for "unreachable", and the error text otherwise; "" for "enrolled".
	Detail string

	// TierAware, BuilderTier and MaxTier are filled only in the "enrolled"
	// arm, from WhoAmI.Features/BuilderTier/MaxTier (#141 remote half).
	TierAware   bool   // WhoAmI.Features contains FeatureTier
	BuilderTier string // WhoAmI.BuilderTier; "" when !TierAware
	MaxTier     string // WhoAmI.MaxTier;     "" when !TierAware

	// QueueAware and Builders are filled only in the "enrolled" arm, from
	// WhoAmI.Features/Builders (#285).
	QueueAware bool                 // WhoAmI.Features contains FeatureQueue
	Builders   *remote.BuildersView // nil when !QueueAware
}

// ProbeServers checks every configured server's reachability and this
// client's enrollment on it, in name order. It is pure over rt.Remote (a
// RemoteClient), so it is tested with fakeRemote, and shared by `relevo
// servers` and `relevo doctor`'s per-server checks.
//
// rt.Remote == nil means no client key: every server probes "no key",
// naming the fix. Otherwise each server is checked with WhoAmI: success is
// "enrolled"; a 401 is "not enrolled" (Detail is the caller's enrollLine,
// the line to hand the admin); ErrCertChanged is "cert changed";
// ErrUnreachable is "unreachable" (Detail is the failure cause); anything
// else is "error" (Detail is the error text).
func ProbeServers(ctx context.Context, rt Runtime, servers map[string]client.ServerEntry, enrollLine string) []ServerProbe {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	probes := make([]ServerProbe, 0, len(names))
	for _, n := range names {
		p := ServerProbe{Name: n, URL: servers[n].URL}
		if rt.Remote == nil {
			p.State = "no key"
			p.Detail = "run relevo config server key"
			probes = append(probes, p)
			continue
		}

		who, err := rt.Remote.WhoAmI(ctx, n)
		switch {
		case err == nil:
			p.State = "enrolled"
			p.Label = who.Label
			p.TierAware = slices.Contains(who.Features, remote.FeatureTier)
			if p.TierAware {
				p.BuilderTier = who.BuilderTier
				p.MaxTier = who.MaxTier
			}
			p.QueueAware = slices.Contains(who.Features, remote.FeatureQueue)
			if p.QueueAware {
				p.Builders = who.Builders
			}
		case errors.Is(err, client.ErrCertChanged):
			p.State = "cert changed"
		case errors.Is(err, client.ErrUnreachable):
			p.State = "unreachable"
			p.Detail = strings.TrimPrefix(err.Error(), client.ErrUnreachable.Error()+": ")
		default:
			var httpErr *client.HTTPError
			if errors.As(err, &httpErr) && httpErr.Status == 401 {
				p.State = "not enrolled"
				p.Detail = enrollLine
			} else {
				p.State = "error"
				p.Detail = err.Error()
			}
		}
		probes = append(probes, p)
	}
	return probes
}

// probeStatusText is one ServerProbe's status word, exactly what `relevo
// servers` printed before ProbeServers existed (serverStatus in
// cmd/relevo/client.go).
func probeStatusText(p ServerProbe) string {
	switch p.State {
	case "enrolled":
		return "enrolled as " + p.Label
	case "no key":
		return "no client key"
	case "not enrolled", "unreachable", "cert changed":
		return p.State
	default:
		return p.Detail
	}
}

// ServerTierWarning is the one-line warning for a server that would launch
// headless builders at tier harness, "" otherwise (including !TierAware).
func ServerTierWarning(p ServerProbe) string {
	if p.TierAware && p.BuilderTier == string(harness.TierHarness) {
		return "headless builders at tier harness deny every tool unless the server host's harness settings allow them; set tier.builder in the server's config policy or pass --tier"
	}
	return ""
}

// RenderServers formats probes as the table `relevo config server list` prints: one row
// per server, name, url, and enrollment status, aligned on the longest name.
func RenderServers(probes []ServerProbe) string {
	if len(probes) == 0 {
		return "no servers configured; relevo config server add <name> <url>\n"
	}

	width := 0
	for _, p := range probes {
		if len(p.Name) > width {
			width = len(p.Name)
		}
	}

	var sb strings.Builder
	for _, p := range probes {
		row := fmt.Sprintf("%-*s  %-40s  %s", width, p.Name, p.URL, probeStatusText(p))
		if p.State == "enrolled" {
			if p.TierAware {
				row += fmt.Sprintf("  builder tier: %s (max %s)", p.BuilderTier, p.MaxTier)
			} else {
				row += "  builder tier: unknown (pre-tier server)"
			}
			if p.QueueAware && p.Builders != nil {
				scopes := "off"
				switch {
				case p.Builders.Scopes && p.Builders.Slice != "" && p.Builders.Quota != "":
					scopes = fmt.Sprintf("on (%s, %s)", p.Builders.Slice, p.Builders.Quota)
				case p.Builders.Scopes && p.Builders.Quota != "":
					scopes = fmt.Sprintf("on (%s)", p.Builders.Quota)
				case p.Builders.Scopes && p.Builders.Slice != "":
					scopes = fmt.Sprintf("on (%s)", p.Builders.Slice)
				case p.Builders.Scopes:
					scopes = "on"
				}
				row += fmt.Sprintf("  builders %d/%d, %d queued, scopes %s",
					p.Builders.Running, p.Builders.Cap, p.Builders.Queued, scopes)
			}
		}
		sb.WriteString(row)
		sb.WriteString("\n")
		if warning := ServerTierWarning(p); warning != "" {
			fmt.Fprintf(&sb, "  !! %s\n", warning)
		}
	}
	return sb.String()
}

func catchUp(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView) (store.Binding, error) {
	n := view.ClosedRound
	server := b.Builder.Server
	name := b.Name

	// 1. Fetch report, diff, log and write via temp-and-rename.
	//
	// A stopped round may have no report file: the builder was killed before
	// it wrote one, and catchUp writes the stopped payload instead (below).
	// A 404 on a round that was not stopped still halts, as it always has.
	haveReport := false
	rcReport, err := rt.Remote.RoundFile(ctx, server, name, n, "report")
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 404 {
			if view.Stopped == "" {
				return haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s closed round %d without a report file", name, server, n))
			}
		} else {
			slog.Warn("fetch report failed", "server", server, "name", name, "round", n, "err", err)
			return b, nil
		}
	} else {
		defer rcReport.Close()
		if err := writeTempAndRename(rt.Store.ReportPath(name, n), rcReport); err != nil {
			slog.Warn("write report failed", "path", rt.Store.ReportPath(name, n), "err", err)
			return b, nil
		}
		haveReport = true
	}

	diffDownloaded := false
	rcDiff, err := rt.Remote.RoundFile(ctx, server, name, n, "diff")
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 404 {
			// fine (no diff)
		} else {
			slog.Warn("fetch diff failed", "server", server, "name", name, "round", n, "err", err)
			return b, nil
		}
	} else {
		defer rcDiff.Close()
		if err := writeTempAndRename(rt.Store.DiffPath(name, n), rcDiff); err != nil {
			slog.Warn("write diff failed", "path", rt.Store.DiffPath(name, n), "err", err)
			return b, nil
		}
		diffDownloaded = true
	}

	rcLog, err := rt.Remote.RoundFile(ctx, server, name, n, "log")
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 404 {
			// fine
		} else {
			slog.Warn("fetch log failed", "server", server, "name", name, "round", n, "err", err)
			return b, nil
		}
	} else {
		defer rcLog.Close()
		if err := writeTempAndRename(rt.Store.BuilderLogPath(name, n), rcLog); err != nil {
			slog.Warn("write log failed", "path", rt.Store.BuilderLogPath(name, n), "err", err)
			return b, nil
		}
	}

	// The harness's own record, NNN-builder.jsonl (#240). A server that serves
	// no stream file answers 404, which is fine, exactly like the log.
	rcStream, err := rt.Remote.RoundFile(ctx, server, name, n, "stream")
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 404 {
			// fine (no stream file)
		} else {
			slog.Warn("fetch stream failed", "server", server, "name", name, "round", n, "err", err)
			return b, nil
		}
	} else {
		defer rcStream.Close()
		if err := writeTempAndRename(rt.Store.BuilderStreamPath(name, n), rcStream); err != nil {
			slog.Warn("write stream failed", "path", rt.Store.BuilderStreamPath(name, n), "err", err)
			return b, nil
		}
	}

	// 2. RoundBundle
	rcBundle, err := rt.Remote.RoundBundle(ctx, server, name, n, b.Builder.LastKnown)
	if err != nil {
		slog.Warn("fetch round bundle failed", "server", server, "name", name, "round", n, "err", err)
		return b, nil
	}
	if rcBundle != nil {
		defer rcBundle.Close()
		branchRef := b.Branch
		if !strings.HasPrefix(branchRef, "refs/heads/") {
			branchRef = "refs/heads/" + branchRef
		}
		// The server always cuts its own relevo/<name> branch and ships that:
		// handleRoundBundle snapshots refs/heads/<server branch>, and a server
		// binding's branch is relevo/<name>. A binding that adopted a branch
		// (#263) keeps the adopted name locally, so the allow-list names the
		// server's ref -- not b.Branch -- and the adopted branch is fast
		// forwarded to the absorbed result below.
		serverRef := "refs/heads/relevo/" + name
		refs := []string{serverRef}
		if view.DirtyCommit != "" {
			refs = append(refs, fmt.Sprintf("refs/relevo/%s/round-%d", name, n))
		}
		if _, err := rt.Transport.Absorb(ctx, b.Repo, remote.ContentTypeGitBundle, rcBundle, refs); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "checked out") {
				if _, seen := checkedOutWarned.LoadOrStore(name, struct{}{}); !seen {
					slog.Info("checkout another branch, then relevo wait", "binding", name, "branch", b.Branch)
				} else {
					slog.Debug("still checked out", "binding", name, "branch", b.Branch)
				}
				return b, nil
			}
			b.RemoteAbsorbFailures++
			if b.RemoteAbsorbFailures >= 10 {
				return haltBinding(ctx, rt, b, fmt.Sprintf("%s: cannot absorb round %d from %s: %s", name, n, server, err.Error()))
			}
			return b, nil
		}
		checkedOutWarned.Delete(name)

		// An adopted binding's own branch is the one relevo keeps current, so
		// bring it to the absorbed result with a compare-and-swap against the
		// sha it had (an empty old means "create or overwrite", which is what
		// a branch that does not exist locally yet needs). An ordinary
		// binding has serverRef == branchRef, so this is a no-op for it.
		if serverRef != branchRef {
			sha, ok, err := rt.Git.RefSHA(ctx, b.Repo, serverRef)
			if err != nil {
				return b, fmt.Errorf("resolve server branch %s after absorb: %w", serverRef, err)
			}
			if !ok {
				return b, fmt.Errorf("server branch %s missing after absorb", serverRef)
			}
			old, _, _ := rt.Git.RefSHA(ctx, b.Repo, branchRef)
			if err := rt.Git.UpdateRef(ctx, b.Repo, branchRef, sha, old); err != nil {
				// The fast-forward can collide with the same branch being
				// checked out locally; that is the same quiet retry the
				// absorb above gets, not an absorb failure.
				if strings.Contains(strings.ToLower(err.Error()), "checked out") {
					if _, seen := checkedOutWarned.LoadOrStore(name, struct{}{}); !seen {
						slog.Info("checkout another branch, then relevo wait", "binding", name, "branch", b.Branch)
					} else {
						slog.Debug("still checked out", "binding", name, "branch", b.Branch)
					}
					return b, nil
				}
				b.RemoteAbsorbFailures++
				if b.RemoteAbsorbFailures >= 10 {
					return haltBinding(ctx, rt, b, fmt.Sprintf("%s: cannot fast-forward %s to round %d from %s: %s", name, b.Branch, n, server, err.Error()))
				}
				return b, nil
			}
		}
	}

	// 3. b.Builder.LastKnown = view.ResultCommit; b.RemoteAbsorbFailures = 0
	b.Builder.LastKnown = view.ResultCommit
	b.RemoteAbsorbFailures = 0

	// 4. rt.Remote.Ack(server, name, n)
	if _, err := rt.Remote.Ack(ctx, server, name, n); err != nil {
		slog.Warn("ack failed", "server", server, "name", name, "round", n, "err", err)
		return b, nil
	}

	// 5. queueReport. The server already recorded a diff entry at close
	// (DiffSummary note, commits, clean/dirty); the view carried those three
	// facts, so write the client's own diff entry from them here -- with the
	// downloaded patch as Path -- before queueReport, which then sees the
	// entry already exists and skips its own CaptureRoundDiff. Because
	// queueReport only appends its "Diff:" line inside that same "no entry
	// yet" branch, the line is added to the payload here instead, from the
	// stored facts rather than a fresh DiffResult.
	entries, err := tx.ReadLog(name)
	if err != nil {
		return b, err
	}
	if view.DiffNote != "" && !HasEntry(entries, n, store.DirToPlanner, store.KindDiff) {
		diffPath := ""
		if diffDownloaded {
			diffPath = rt.Store.DiffPath(name, n)
		}
		diffEntry := store.LogEntry{
			TS: rt.Now().UTC(), Round: n, Direction: store.DirToPlanner, Kind: store.KindDiff,
			Path: diffPath, Note: view.DiffNote, Commits: view.DiffCommits, Tree: view.DiffTree, Confirmed: true,
		}
		if err := tx.AppendLog(name, diffEntry); err != nil {
			return b, err
		}
		entries = append(entries, diffEntry)
	}
	reportPath := rt.Store.ReportPath(name, n)
	payload := ""
	note := ""
	if view.Stopped != "" {
		payload, note = stopPayload(view.Stopped, name, n, " on "+server, haveReport)
	} else {
		payload = fmt.Sprintf("Builder finished round %d on %s. Report: %s", n, server, showCommand(name, n, "report"))
	}
	if line := DiffLineFromNote(view.DiffNote, view.DiffCommits, view.DiffTree, b.Branch); line != "" {
		payload = payload + "\n" + line
	}
	if line := PathsLineFromNote(view.DiffNote); line != "" {
		payload = payload + "\n" + line
	}
	if view.DirtyCommit != "" {
		note = joinNotes(note, fmt.Sprintf("uncommitted work at refs/relevo/%s/round-%d", name, n))
	}
	var u *usage.Usage = view.Usage
	if u == nil {
		// A pre-usage server ships no figure: record honestly that the
		// server sent none rather than reading a record the client does
		// not have.
		u = remoteNoUsage(rt, b, b.RoundStartedAt, rt.Now().UTC())
	}
	next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, note, nil, u, view.Rusage)
	if err != nil {
		return b, err
	}
	if view.Stopped != "" {
		// After queueReport, the same order closeStopped uses, so the entry is
		// filed under the round that was stopped (#344).
		if err := tx.AppendLog(name, store.LogEntry{
			TS: rt.Now().UTC(), Round: n, Direction: store.DirToPlanner,
			Kind: store.KindStop, Note: "stopped/" + view.Stopped, Confirmed: true,
		}); err != nil {
			return next, err
		}
	}

	// 6. Mark idle; the caller (reconcileRemote or SyncRemote) decides
	// whether to deliver.
	next.Builder.RemoteStatus = "idle"
	next.Builder.RemoteQueue = nil
	next.Builder.RemoteLive = nil
	return next, nil
}

// ForwardUnavailable tells every remote binding with an open round that its
// server-side candidate just hit a usage limit, so that server's own
// reconcile can switch or gate it exactly as a local daemon would (§4.6). It
// never fails the caller: a binding relevo could not reach is named in the
// returned lines instead, and `relevo gate <token>`'s local behaviour (the
// ledger gate) proceeds either way.
func ForwardUnavailable(ctx context.Context, rt Runtime, token, reason string) []string {
	if rt.Remote == nil {
		return nil
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return []string{fmt.Sprintf("list bindings: %v", err)}
	}

	var lines []string
	for _, b := range bindings {
		if !b.Builder.Remote() {
			continue
		}

		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: read log: %v", b.Name, err))
			continue
		}
		open := HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
			!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport)
		if !open {
			continue
		}

		if err := rt.Remote.Unavailable(ctx, b.Builder.Server, b.Name, token, reason); err != nil {
			lines = append(lines, fmt.Sprintf("%s: %s: %v", b.Name, b.Builder.Server, err))
		}
	}

	return lines
}

// ForwardAvailable tells every server this client's bindings name that a
// rate-limit gate can be lifted, so the server-wide ledger stops gating a
// provider the local ledger just cleared. Every remote binding counts, in any
// state and whether or not its round is open: a gate matters most when nothing
// is running. One call and one answer line per distinct server, sorted, and it
// never fails the caller -- a server relevo could not reach is named in the
// returned lines instead, and `relevo gate --clear`'s local behaviour (the ledger
// clear) proceeds either way.
func ForwardAvailable(ctx context.Context, rt Runtime, subject string) []string {
	if rt.Remote == nil {
		return nil
	}

	// A candidate name or token is forwarded as its canonical token, so the
	// server's own ledger clears the same provider; a bare provider is
	// forwarded as itself (A1 §4.2).
	if rt.Candidates != nil {
		if c, err := rt.Candidates.Resolve(subject); err == nil {
			subject = c.Ref().String()
		}
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return []string{fmt.Sprintf("list bindings: %v", err)}
	}

	var servers []string
	seen := make(map[string]bool)
	for _, b := range bindings {
		if !b.Builder.Remote() || seen[b.Builder.Server] {
			continue
		}
		seen[b.Builder.Server] = true
		servers = append(servers, b.Builder.Server)
	}
	sort.Strings(servers)

	var lines []string
	for _, server := range servers {
		resp, err := rt.Remote.Available(ctx, server, subject)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: %v", server, err))
			continue
		}
		if resp.Removed == 0 {
			lines = append(lines, fmt.Sprintf("%s: nothing was gating %s", server, resp.Provider))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: cleared %s (%d entries)", server, resp.Provider, resp.Removed))
	}

	return lines
}

// ServerInUse names every binding that names server -- the pure rule behind
// `relevo config server rm`'s refusal (§4.7). A pure function over the
// binding list rather than a store read, so the CLI (cmd/relevo) can be
// tested without touching the network -- the caller loads the bindings and
// this function decides.
func ServerInUse(bindings []store.Binding, server string) []string {
	var names []string
	for _, b := range bindings {
		if b.Builder.Remote() && b.Builder.Server == server {
			names = append(names, b.Name)
		}
	}
	return names
}

// matchCandidateView returns the server view v names, by its canonical Token
// or by its Name. Pure, so `addRemote`'s name matching is testable without a
// server (A1 §4.2).
func matchCandidateView(views []remote.CandidateView, v string) (remote.CandidateView, bool) {
	for _, view := range views {
		if view.Token == v || (view.Name != "" && view.Name == v) {
			return view, true
		}
	}
	return remote.CandidateView{}, false
}
