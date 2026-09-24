package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestSettled pins the settled predicate (spec §7): DONE with Serve facts and
// no live process, consult or gate, and either the last closed round was acked
// or the binding has been quiet for settledGrace.
func TestSettled(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	aged := now.Add(-settledGrace - time.Hour)

	cases := []struct {
		name string
		b    store.Binding
		want bool
	}{
		{
			name: "acked",
			b: store.Binding{
				State:     store.StateDone,
				Serve:     &store.ServeFacts{ClosedRound: 2, AckedRound: 2},
				UpdatedAt: fresh,
			},
			want: true,
		},
		{
			name: "not acked but older than the grace",
			b: store.Binding{
				State:     store.StateDone,
				Serve:     &store.ServeFacts{ClosedRound: 2, AckedRound: 1},
				UpdatedAt: aged,
			},
			want: true,
		},
		{
			name: "not acked and fresh",
			b: store.Binding{
				State:     store.StateDone,
				Serve:     &store.ServeFacts{ClosedRound: 2, AckedRound: 1},
				UpdatedAt: fresh,
			},
			want: false,
		},
		{
			name: "builder running",
			b: store.Binding{
				State:     store.StateDone,
				Builder:   store.Endpoint{PID: 4242},
				Serve:     &store.ServeFacts{ClosedRound: 1, AckedRound: 1},
				UpdatedAt: fresh,
			},
			want: false,
		},
		{
			name: "consult running",
			b: store.Binding{
				State:     store.StateDone,
				Consults:  []store.Consult{{State: store.ConsultRunning}},
				Serve:     &store.ServeFacts{ClosedRound: 1, AckedRound: 1},
				UpdatedAt: fresh,
			},
			want: false,
		},
		{
			name: "gate running",
			b: store.Binding{
				State:     store.StateDone,
				GateRun:   &store.GateRun{PID: 7},
				Serve:     &store.ServeFacts{ClosedRound: 1, AckedRound: 1},
				UpdatedAt: fresh,
			},
			want: false,
		},
		{
			name: "not done",
			b: store.Binding{
				State:     store.StateActive,
				Serve:     &store.ServeFacts{ClosedRound: 1, AckedRound: 1},
				UpdatedAt: aged,
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := settled(tc.b, now); got != tc.want {
				t.Fatalf("settled = %v, want %v", got, tc.want)
			}
		})
	}
}

// seedServedBinding creates a bare repo with a branch, a checked-out worktree
// and two refs/relevo/<name>/* refs, then saves a DONE served binding over it.
// It returns the bare repo path and the worktree path.
func seedServedBinding(t *testing.T, env *testEnv, name string, facts store.ServeFacts) (bare, worktree string) {
	t.Helper()
	ctx := context.Background()

	ownerDir, ok := env.id.Dir()
	if !ok {
		t.Fatal("client id has no owner dir")
	}
	bare = filepath.Join(env.srv.cfg.Root, "repos", ownerDir, env.repoID+".git")
	if err := env.gitClient.InitBare(ctx, bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	branch := "relevo/" + name
	// Push the client's HEAD into the bare repo on the binding's branch, so
	// the branch and the refs below have a commit to point at.
	runGit(t, env.clientDir, "push", bare, env.headSHA+":refs/heads/"+branch)

	worktree = filepath.Join(t.TempDir(), name+"-wt")
	if err := env.gitClient.CheckoutWorktree(ctx, bare, worktree, branch); err != nil {
		t.Fatalf("checkout worktree: %v", err)
	}
	for _, ref := range []string{
		"refs/relevo/" + name + "/out",
		"refs/relevo/" + name + "/round-1",
	} {
		if err := env.gitClient.UpdateRef(ctx, bare, ref, env.headSHA, ""); err != nil {
			t.Fatalf("update-ref %s: %v", ref, err)
		}
	}

	facts.BareRepo = bare
	b := store.Binding{
		Name:     name,
		Owner:    string(env.id),
		CWD:      worktree,
		Worktree: worktree,
		Branch:   branch,
		State:    store.StateDone,
		Round:    2,
		Serve:    &facts,
	}
	rt := env.runtime(t)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save binding %s: %v", name, err)
	}
	return bare, worktree
}

// TestCollectSettledOnTick drives one server Tick with a DONE, acked binding
// and a DONE, un-acked, fresh one: the acked binding is collected (gone from
// List, worktree removed, branch and refs deleted, record archived) while the
// fresh one is untouched.
func TestCollectSettledOnTick(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	ackedBare, ackedWT := seedServedBinding(t, env, "api", store.ServeFacts{
		RepoID:      env.repoID,
		ClosedRound: 1,
		AckedRound:  1,
	})
	freshBare, freshWT := seedServedBinding(t, env, "beta", store.ServeFacts{
		RepoID:      env.repoID,
		ClosedRound: 1,
		AckedRound:  0,
	})

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	rt := env.runtime(t)
	bindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := map[string]bool{}
	for _, b := range bindings {
		names[b.Name] = true
	}
	if names["api"] {
		t.Error("acked binding api still in List after the tick")
	}
	if !names["beta"] {
		t.Error("fresh un-acked binding beta was collected; want it untouched")
	}

	if _, err := os.Stat(ackedWT); !os.IsNotExist(err) {
		t.Errorf("acked worktree still present (stat err = %v); want it removed", err)
	}
	if _, ok, err := env.gitClient.RefSHA(ctx, ackedBare, "refs/heads/relevo/api"); err != nil || ok {
		t.Errorf("acked branch refs/heads/relevo/api = (ok %v, err %v); want it gone", ok, err)
	}
	refs, err := env.gitClient.ListRefs(ctx, ackedBare, "refs/relevo/api/")
	if err != nil {
		t.Fatalf("list acked refs: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("acked refs/relevo/api/* = %v; want none", refs)
	}

	archived, err := rt.Store.ListArchived()
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	foundArchived := false
	for _, rec := range archived {
		if rec.Binding.Name == "api" {
			foundArchived = true
		}
	}
	if !foundArchived {
		t.Error("acked binding api has no archived record")
	}

	// The fresh binding keeps everything it had.
	if _, err := os.Stat(freshWT); err != nil {
		t.Errorf("fresh worktree stat = %v; want it present", err)
	}
	if _, ok, err := env.gitClient.RefSHA(ctx, freshBare, "refs/heads/relevo/beta"); err != nil || !ok {
		t.Errorf("fresh branch refs/heads/relevo/beta = (ok %v, err %v); want it present", ok, err)
	}
	freshRefs, err := env.gitClient.ListRefs(ctx, freshBare, "refs/relevo/beta/")
	if err != nil {
		t.Fatalf("list fresh refs: %v", err)
	}
	if len(freshRefs) != 2 {
		t.Errorf("fresh refs/relevo/beta/* = %v; want the two seeded refs", freshRefs)
	}
}
