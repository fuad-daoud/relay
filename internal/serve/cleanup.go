package serve

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// settledGrace is how long a DONE binding the owner never acked is left alone
// before the server collects it anyway (spec §7). A crash between the result
// reaching the client and the ack leaves no proof the client absorbed it; seven
// days is long enough that no live planner is still reading the result.
const settledGrace = 7 * 24 * time.Hour

// ackedGrace is how long a DONE binding whose rounds are all acked stays
// before it is collected: a client that has just run `relevo done` reads the
// binding's view right after (TestRemoteRoundEndToEnd does), and must not get
// a 404 because the server's next tick collected it in between.
const ackedGrace = time.Hour

// settled reports whether a served binding is finished with the server and its
// per-binding resources may be collected (spec §7). It is pure: now is a
// parameter, so tests can pin the seven-day fallback boundary.
//
// It is true iff the binding is DONE, carries its Serve facts, has no builder
// process, no running consult and no running gate, and either the owner acked
// the last closed round and the binding has been quiet for ackedGrace, or it
// has been quiet for settledGrace.
func settled(b store.Binding, now time.Time) bool {
	if b.State != store.StateDone || b.Serve == nil {
		return false
	}
	if b.Builder.PID != 0 || b.GateRun != nil {
		return false
	}
	for _, c := range b.Consults {
		if c.State == store.ConsultSpawning || c.State == store.ConsultRunning {
			return false
		}
	}
	if b.Serve.AckedRound >= b.Serve.ClosedRound {
		return now.Sub(b.UpdatedAt) >= ackedGrace
	}
	return now.Sub(b.UpdatedAt) >= settledGrace
}

// collectSettled archives and releases every served binding across all owners
// that is settled (spec §7): the retention fix for a server that had collected
// nothing. For each settled binding, in order:
//
//  1. force-remove the server worktree -- this copy is disposable once the
//     client holds the result (teardownServed);
//  2. delete the binding's branch from the owner's bare repo;
//  3. list the binding's refs/relevo/<name>/* refs and delete each;
//  4. archive the record (relevo.Unbind with archive=true).
//
// If teardownServed (steps 1-3) fails, step 4 does not run, so a failed cleanup
// is retried next tick with the record still live. Each binding is handled
// independently: an error is logged, that binding is skipped, and the walk
// carries on -- it never fails the tick. The returned error is only an
// unreadable bindings dir; a missing dir returns nil, as Tick does. The count
// is how many bindings were collected.
func (s *Server) collectSettled(ctx context.Context) (int, error) {
	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	now := s.cfg.Now()
	collected := 0

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)

		bindings, err := rt.Store.List()
		if err != nil {
			slog.Warn("collect settled: list owner bindings failed", "owner", id, "err", err)
			continue
		}

		for _, b := range bindings {
			if !settled(b, now) {
				continue
			}

			if err := teardownServed(ctx, rt, b); err != nil {
				slog.Warn("collect settled binding failed", "owner", id, "binding", b.Name, "err", err)
				continue
			}

			if _, err := relevo.Unbind(ctx, rt, b.Name, true); err != nil {
				slog.Warn("collect settled binding failed", "owner", id, "binding", b.Name, "err", err)
				continue
			}

			slog.Info("collected settled binding", "owner", id, "binding", b.Name)
			collected++
		}
	}

	return collected, nil
}

// releaseServedRefs deletes the binding's branch and its refs/relevo/<name>/*
// refs from the owner's bare repo (steps 2-3 of teardownServed).
func releaseServedRefs(ctx context.Context, rt relevo.Runtime, b store.Binding) error {
	if b.Serve == nil || b.Serve.BareRepo == "" {
		return nil
	}
	bare := b.Serve.BareRepo
	if err := rt.Git.DeleteBranch(ctx, bare, b.Branch); err != nil {
		return err
	}
	refs, err := rt.Git.ListRefs(ctx, bare, "refs/relevo/"+b.Name+"/")
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := rt.Git.DeleteRef(ctx, bare, ref); err != nil {
			return err
		}
	}
	return nil
}

// teardownServed force-removes the server worktree, then deletes the binding's
// branch and refs from its bare repo.
func teardownServed(ctx context.Context, rt relevo.Runtime, b store.Binding) error {
	if b.Serve == nil || b.Serve.BareRepo == "" {
		return nil
	}
	bare := b.Serve.BareRepo
	if err := rt.Git.RemoveWorktree(ctx, bare, b.Worktree, true); err != nil {
		return err
	}
	return releaseServedRefs(ctx, rt, b)
}

// unusedRepos returns the entries of repos that no binding in live references.
// A binding references path p when b.Serve != nil && filepath.Clean(b.Serve.BareRepo) == filepath.Clean(p),
// or when b.Serve == nil && filepath.Clean(b.Repo) == filepath.Clean(p).
// It keeps the input order.
func unusedRepos(repos []string, live []store.Binding) []string {
	var unused []string
	for _, p := range repos {
		cleanP := filepath.Clean(p)
		referenced := false
		for _, b := range live {
			var ref string
			if b.Serve != nil {
				ref = b.Serve.BareRepo
			} else {
				ref = b.Repo
			}
			if ref != "" && filepath.Clean(ref) == cleanP {
				referenced = true
				break
			}
		}
		if !referenced {
			unused = append(unused, p)
		}
	}
	return unused
}

// pruneUnusedRepos deletes every bare repo under repos/<owner>/<id>.git that no
// live binding of that owner references. The caller holds s.mu.
func (s *Server) pruneUnusedRepos(ctx context.Context) int {
	reposDir := filepath.Join(s.cfg.Root, "repos")
	entries, err := os.ReadDir(reposDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		slog.Warn("prune unused repos: read repos dir failed", "err", err)
		return 0
	}

	pruned := 0
	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in repos dir", "entry", entry.Name())
			continue
		}

		ownerRepoDir := filepath.Join(reposDir, entry.Name())
		ownerBindingsDir := filepath.Join(s.cfg.Root, "bindings", entry.Name())
		rt := s.runtimeAt(ownerBindingsDir)

		live, err := rt.Store.List()
		if err != nil {
			slog.Warn("prune unused repos: list owner bindings failed", "owner", id, "err", err)
			continue
		}

		repoEntries, err := os.ReadDir(ownerRepoDir)
		if err != nil {
			slog.Warn("prune unused repos: read owner repo dir failed", "owner", id, "err", err)
			continue
		}

		var ownerRepos []string
		for _, re := range repoEntries {
			if re.IsDir() && strings.HasSuffix(re.Name(), ".git") {
				ownerRepos = append(ownerRepos, filepath.Join(ownerRepoDir, re.Name()))
			}
		}

		for _, path := range unusedRepos(ownerRepos, live) {
			if err := os.RemoveAll(path); err != nil {
				slog.Warn("prune unused bare repo failed", "owner", id, "repo", filepath.Base(path), "err", err)
				continue
			}
			slog.Info("pruned unused bare repo", "owner", id, "repo", filepath.Base(path))
			pruned++
		}

		_ = os.Remove(ownerRepoDir)
	}

	return pruned
}
