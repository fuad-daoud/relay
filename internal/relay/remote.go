package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/store"
)

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

func addRemote(ctx context.Context, rt Runtime, opts AddOptions) (AddResult, error) {
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
	if _, err := rt.Store.Load(opts.Name); err == nil {
		return AddResult{}, fmt.Errorf("binding %q already exists locally", opts.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return AddResult{}, err
	}

	// 2. base := opts.Base if given else rt.Git.HeadCommit(opts.Repo)
	// resolve to a full sha with RefSHA(opts.Repo, base) when it is not 40-hex; missing -> error
	base := opts.Base
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

	// 3. root := rt.Git.RootCommit(opts.Repo); repoID := remote.RepoID(root)
	root, err := rt.Git.RootCommit(ctx, opts.Repo)
	if err != nil {
		return AddResult{}, fmt.Errorf("root commit: %w", err)
	}
	repoID, err := remote.RepoID(root)
	if err != nil {
		return AddResult{}, fmt.Errorf("repo id: %w", err)
	}

	// 4. candidate := opts.Candidate; if non-empty, rt.Remote.Candidates(server) must list it (else error naming the
	// server's tokens); if empty, leave "" and let the server pick.
	candidateStr := opts.Candidate
	if candidateStr != "" {
		candResp, err := rt.Remote.Candidates(ctx, opts.Server)
		if err != nil {
			return AddResult{}, fmt.Errorf("list remote candidates: %w", err)
		}
		found := false
		tokens := make([]string, len(candResp.Candidates))
		for i, c := range candResp.Candidates {
			tokens[i] = c.Token
			if c.Token == candidateStr {
				found = true
			}
		}
		if !found {
			return AddResult{}, fmt.Errorf("candidate %q not available on %s (available: %s)", candidateStr, opts.Server, strings.Join(tokens, ", "))
		}
	}

	// 5. view := rt.Remote.CreateBinding(ctx, server, {Name, RepoID, BaseCommit: base, Candidate, RoundCap, RoundTimeoutMS})
	// HTTPError 409 -> "binding api already exists on zen for this client; relay bind --resume --name api"
	createReq := remote.CreateBindingRequest{
		Name:       opts.Name,
		RepoID:     repoID,
		BaseCommit: base,
		Candidate:  candidateStr,
	}
	view, err := rt.Remote.CreateBinding(ctx, opts.Server, createReq)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 409 {
			return AddResult{}, fmt.Errorf("binding %s already exists on %s for this client; relay bind --resume --name %s", opts.Name, opts.Server, opts.Name)
		}
		return AddResult{}, err
	}

	// 6. rt.Git.CreateBranch(opts.Repo, "relay/"+name, base) -- after the server agreed, so a refused create leaves no branch
	// ErrBranchExists -> error "branch relay/api exists; delete it or pick another name" AND rt.Remote.Unbind the
	// server binding just created (best effort, logged).
	branch := "relay/" + opts.Name
	if err := rt.Git.CreateBranch(ctx, opts.Repo, branch, base); err != nil {
		if errors.Is(err, git.ErrBranchExists) {
			if unbindErr := rt.Remote.Unbind(ctx, opts.Server, opts.Name); unbindErr != nil {
				slog.Warn("unbind after failed branch create", "server", opts.Server, "name", opts.Name, "err", unbindErr)
			}
			return AddResult{}, fmt.Errorf("branch relay/%s exists; delete it or pick another name", opts.Name)
		}
		return AddResult{}, err
	}

	// 7. b := Binding{Name, CWD: opts.Repo, Repo: opts.Repo, Branch, Base: base, Planner: <as Add fills it>,
	//                 Builder: Endpoint{Mode: ModeRemote, Server: server, Kind: <kind from view.Candidate's harness, "" if unknown>,
	//                                   AgentName: name},
	//                 BuilderCandidate: view.Candidate, Round: 1, State: active, RoundCap/Timeout as Add}
	// Save under lock; append the same pick log entry Add writes, with the candidate the server reported.
	var planner store.Endpoint
	if opts.PlannerPane != "" && rt.Herdr != nil {
		if agents, err := rt.Herdr.ListAgents(ctx); err == nil {
			if p, ok := FindAgent(agents, store.Endpoint{PaneID: opts.PlannerPane}); ok {
				planner = endpointOf(p)
			}
		}
	}

	builderKind := ""
	var cand candidate.Candidate
	if view.Candidate != "" {
		if ref, err := candidate.ParseRef(view.Candidate); err == nil {
			builderKind = ref.Harness
			cand = candidate.Candidate{Harness: ref.Harness, Provider: ref.Provider, Model: ref.Model}
		}
	}

	b := store.Binding{
		Name:    opts.Name,
		CWD:     opts.Repo,
		Repo:    opts.Repo,
		Branch:  branch,
		Base:    base,
		Planner: planner,
		Builder: store.Endpoint{
			Mode:      store.ModeRemote,
			Server:    opts.Server,
			Kind:      builderKind,
			AgentName: opts.Name,
		},
		BuilderCandidate: view.Candidate,
		Round:            1,
		State:            store.StateActive,
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
			return tx.AppendLog(b.Name, pickEntry(rt.Now(), 1, "builder", res))
		}
		return nil
	}); err != nil {
		return AddResult{}, fmt.Errorf("save remote binding: %w", err)
	}

	if stored, err := rt.Store.Load(b.Name); err == nil {
		b = stored
	}

	return AddResult{
		Binding:    b,
		Worktree:   "",
		Branch:     branch,
		Base:       base,
		Resolution: res,
	}, nil
}

func sendRemote(ctx context.Context, rt Runtime, b store.Binding, planBody []byte) (SendResult, error) {
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
	name := b.Name

	// 1. rt.Git.UpdateRef(b.Repo, "refs/relay/"+b.Name+"/out", RefSHA("refs/heads/"+b.Branch), "")
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
	outRef := "refs/relay/" + name + "/out"
	if err := rt.Git.UpdateRef(ctx, b.Repo, outRef, branchSHA, ""); err != nil {
		return SendResult{}, fmt.Errorf("update ref %s: %w", outRef, err)
	}

	// 2. snap := rt.Transport.Snapshot(b.Repo, ["refs/relay/<name>/out"], b.Builder.LastShipped)
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

	// 3. view, err := rt.Remote.StartRound(ctx, server, name, b.Round, planBody, snap.Body or nil when Empty)
	view, err := rt.Remote.StartRound(ctx, server, name, b.Round, planBody, bundleReader)
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) {
			if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundStarted {
				// proceed as success
				view.RoundState = remote.RoundRunning
			} else if httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundOpen {
				return SendResult{}, fmt.Errorf("round %d is running on %s", b.Round, server)
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

	// UNDER the lock:
	// 4. reload b; if b.Round != the round sent -> error "round advanced during send; run relay status" (nothing recorded)
	// 5. write PlanPath(name, round) = planBody; AppendLog plan to_builder; b.RoundStartedAt = now;
	// b.State = active; b.Halt = ""; b.Builder.LastShipped = snap.Heads["refs/relay/<name>/out"];
	// b.Builder.RemoteStatus = string(view.RoundState); Save.
	var sendRound int
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(name)
		if err != nil {
			return err
		}
		if cur.Round != b.Round {
			return errors.New("round advanced during send; run relay status")
		}
		sendRound = cur.Round

		planPath := rt.Store.PlanPath(name, cur.Round)
		if err := os.WriteFile(planPath, planBody, 0o644); err != nil {
			return fmt.Errorf("write plan %s: %w", planPath, err)
		}

		now := rt.Now()
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
	return SendResult{Round: sendRound}, nil
}
