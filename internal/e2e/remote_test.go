package e2e

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/serve"
	"github.com/fuad-daoud/relay/internal/store"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test Repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func tickUntil(t *testing.T, deadline time.Duration, step func() bool) {
	t.Helper()
	start := time.Now()
	for {
		if step() {
			return
		}
		if time.Since(start) > deadline {
			t.Fatalf("tickUntil: timed out after %v", deadline)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func newServer(t *testing.T) (*serve.Server, string, string, func(pub string) remote.ClientID, func(owner remote.ClientID) *store.Store, *scriptRunner) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "serve")
	now := time.Now()

	fp, err := serve.InitTLS(root, []string{"localhost", "127.0.0.1"}, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	cert, err := serve.LoadTLS(root)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}

	candDir := t.TempDir()
	candPath := filepath.Join(candDir, "candidates.json")
	candJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	if err := os.WriteFile(candPath, []byte(candJSON), 0o644); err != nil {
		t.Fatalf("write candidates.json: %v", err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("load candidates: %v", err)
	}

	runner := &scriptRunner{alive: true}
	gitClient := git.NewClient("git", 10*time.Second, 0)

	cfg := serve.Config{
		Root:           root,
		Candidates:     cSet,
		Policy:         policy.Policy{},
		Runner:         runner,
		Git:            gitClient,
		Now:            time.Now,
		Interval:       time.Second,
		MaxBundleBytes: 64 << 20,
	}

	clientsPath := filepath.Join(root, "clients.json")
	enroll := func(pub string) remote.ClientID {
		cls, err := serve.LoadClients(clientsPath)
		if err != nil {
			t.Fatalf("LoadClients: %v", err)
		}
		cl, err := cls.Add("testclient", pub, time.Now())
		if err != nil {
			t.Fatalf("Add client: %v", err)
		}
		return cl.ID
	}

	srv, err := serve.New(cfg)
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, serve.ListenConfig{
			Addr: "127.0.0.1:0",
			TLS:  &cert,
		})
	}()

	var addr net.Addr
	start := time.Now()
	for time.Since(start) < 5*time.Second {
		if a := srv.Addr(); a != nil {
			addr = a
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == nil {
		t.Fatal("timed out waiting for server address")
	}

	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	url := "https://" + addr.String()
	srvStore := func(owner remote.ClientID) *store.Store {
		dir, _ := owner.Dir()
		return store.New(filepath.Join(root, "bindings", dir))
	}

	return srv, url, fp, enroll, srvStore, runner
}

func newClient(t *testing.T, url, fingerprint string) (relay.Runtime, *fakeHerdr, remote.Keypair) {
	t.Helper()
	cfgDir := t.TempDir()
	privPath, pubPath := client.KeyPaths(cfgDir)
	kp, err := client.InitKey(privPath, pubPath)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}

	servers := client.Servers{
		"zen": client.ServerEntry{
			URL:         url,
			Fingerprint: fingerprint,
		},
	}

	hd := &fakeHerdr{
		agents: []herdr.Agent{
			{PaneID: "p1", Name: "planner", Status: herdr.StatusIdle, Kind: "claude"},
		},
	}

	gitClient := git.NewClient("git", 10*time.Second, 0)
	st := store.New(t.TempDir())

	candDir := t.TempDir()
	candPath := filepath.Join(candDir, "candidates.json")
	candJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	if err := os.WriteFile(candPath, []byte(candJSON), 0o644); err != nil {
		t.Fatalf("write candidates.json: %v", err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("load candidates: %v", err)
	}

	rt := relay.Runtime{
		Herdr:       hd,
		Git:         gitClient,
		Store:       st,
		Candidates:  cSet,
		LedgerPath:  filepath.Join(t.TempDir(), "ledger.json"),
		HistoryPath: filepath.Join(t.TempDir(), "history.json"),
		Now:         time.Now,
		Remote:      client.New(servers, kp, time.Now),
		Transport:   remote.NewBundleTransport(gitClient, t.TempDir()),
	}

	return rt, hd, kp
}

func TestRemoteRoundEndToEnd(t *testing.T) {
	ctx := context.Background()

	// 1. server, client (enrolled), repo.
	srv, url, fp, enroll, srvStore, runner := newServer(t)
	rt, hd, kp := newClient(t, url, fp)
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	_, err := relay.Add(ctx, rt, relay.AddOptions{
		Name:        "api",
		Server:      "zen",
		Repo:        repo,
		PlannerPane: "p1",
	})
	if err != nil {
		t.Fatalf("step 1: relay.Add: %v", err)
	}

	// Assert: local branch relay/api exists at the repo's HEAD; server store for the owner has api.
	repoGit := git.NewClient("git", 10*time.Second, 0)
	exists, err := repoGit.BranchExists(ctx, repo, "relay/api")
	if err != nil || !exists {
		t.Fatalf("step 1: branch relay/api exists = %v, err = %v", exists, err)
	}
	headSHA, _, err := repoGit.RefSHA(ctx, repo, "HEAD")
	if err != nil {
		t.Fatalf("step 1: ref HEAD: %v", err)
	}
	branchSHA, _, err := repoGit.RefSHA(ctx, repo, "refs/heads/relay/api")
	if err != nil {
		t.Fatalf("step 1: ref relay/api: %v", err)
	}
	if branchSHA != headSHA {
		t.Fatalf("step 1: branch SHA = %s, want HEAD %s", branchSHA, headSHA)
	}

	serverStore := srvStore(owner)
	sb, err := serverStore.Load("api")
	if err != nil {
		t.Fatalf("step 1: server store Load(api): %v", err)
	}
	if sb.Name != "api" {
		t.Fatalf("step 1: server store binding name = %q, want api", sb.Name)
	}

	// 2. relay.Send(ctx, rt, "api", planFile) with a one-line plan.
	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("step 2: write plan: %v", err)
	}
	if _, err := relay.Send(ctx, rt, "api", planFile); err != nil {
		t.Fatalf("step 2: relay.Send: %v", err)
	}

	// Assert: runner.specs has one entry whose Dir is the server worktree; the worktree has README.md;
	// client binding Builder.LastShipped == repo HEAD.
	runner.mu.Lock()
	specsLen := len(runner.specs)
	var spec relay.ProcSpec
	if specsLen > 0 {
		spec = runner.specs[0]
	}
	runner.mu.Unlock()

	if specsLen != 1 {
		t.Fatalf("step 2: runner.specs count = %d, want 1", specsLen)
	}

	serverWorktree := sb.Worktree
	if serverWorktree == "" {
		serverWorktree = serverStore.WorktreePath("api")
	}
	if spec.Dir != serverWorktree {
		t.Fatalf("step 2: spec.Dir = %q, want server worktree %q", spec.Dir, serverWorktree)
	}
	if _, err := os.Stat(filepath.Join(serverWorktree, "README.md")); err != nil {
		t.Fatalf("step 2: server worktree missing README.md: %v", err)
	}

	cb, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("step 2: read client binding: %v", err)
	}
	if cb.Builder.LastShipped != headSHA {
		t.Fatalf("step 2: Builder.LastShipped = %q, want repo HEAD %q", cb.Builder.LastShipped, headSHA)
	}

	// 3. finishRound(...); srv.Tick; then tickUntil the client daemon's Tick leaves a KindReport entry for round 1.
	gitClient := git.NewClient("git", 10*time.Second, 0)
	serverRT := relay.Runtime{
		Store:  serverStore,
		Git:    gitClient,
		Runner: runner,
	}
	finishRound(t, serverRT, "api", 1, "round 1 done")

	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("step 3: srv.Tick: %v", err)
	}

	clientDaemon := relay.NewDaemon(rt, time.Second)
	tickUntil(t, 10*time.Second, func() bool {
		_ = clientDaemon.Tick(ctx)
		entries, err := rt.Store.ReadLog("api")
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.Kind == store.KindReport && e.Round == 1 {
				return true
			}
		}
		return false
	})

	// Assert: hd.prompts has exactly one entry for p1 whose text contains Report: and Diff: 1 file and
	// 1 commit on relay/api; the client repo's relay/api is one commit ahead of init with hello.txt;
	// the server view (via rt.Remote.GetBinding) has acked_round == 1 and round_state == idle;
	// client Builder.RemoteStatus == "idle"; BuilderCandidate equals the server's candidate token.
	hd.mu.Lock()
	prompts := make([]struct{ Target, Text string }, len(hd.prompts))
	copy(prompts, hd.prompts)
	hd.mu.Unlock()

	if len(prompts) != 1 {
		t.Fatalf("step 3: prompts = %d, want 1", len(prompts))
	}
	if prompts[0].Target != "p1" {
		t.Fatalf("step 3: prompt target = %q, want p1", prompts[0].Target)
	}
	pText := prompts[0].Text
	for _, substr := range []string{"Report:", "Diff: 1 file", "1 commit on relay/api"} {
		if !strings.Contains(pText, substr) {
			t.Fatalf("step 3: prompt text missing %q; got:\n%s", substr, pText)
		}
	}

	diffOut := runGit(t, repo, "diff", "HEAD..refs/heads/relay/api", "--name-only")
	if !strings.Contains(diffOut, "hello.txt") {
		t.Fatalf("step 3: branch relay/api does not have hello.txt; diff:\n%s", diffOut)
	}
	revCount := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "HEAD..refs/heads/relay/api"))
	if revCount != "1" {
		t.Fatalf("step 3: commits ahead = %s, want 1", revCount)
	}

	sv, err := rt.Remote.GetBinding(ctx, "zen", "api")
	if err != nil {
		t.Fatalf("step 3: GetBinding: %v", err)
	}
	if sv.AckedRound != 1 {
		t.Fatalf("step 3: server AckedRound = %d, want 1", sv.AckedRound)
	}
	if sv.RoundState != "idle" {
		t.Fatalf("step 3: server RoundState = %q, want idle", sv.RoundState)
	}

	cb, err = rt.Store.Load("api")
	if err != nil {
		t.Fatalf("step 3: read client binding: %v", err)
	}
	if cb.Builder.RemoteStatus != "idle" {
		t.Fatalf("step 3: client Builder.RemoteStatus = %q, want idle", cb.Builder.RemoteStatus)
	}
	if cb.BuilderCandidate != "claude/anthropic/haiku" {
		t.Fatalf("step 3: client BuilderCandidate = %q, want claude/anthropic/haiku", cb.BuilderCandidate)
	}

	// 4. relay.Done(ctx, rt, "api"). Assert: server view state done; client state done.
	if _, err := relay.Done(ctx, rt, "api"); err != nil {
		t.Fatalf("step 4: relay.Done: %v", err)
	}
	sv, err = rt.Remote.GetBinding(ctx, "zen", "api")
	if err != nil {
		t.Fatalf("step 4: server view after Done: %v", err)
	}
	if sv.State != "done" {
		t.Fatalf("step 4: server view state = %q, want done", sv.State)
	}
	cb, err = rt.Store.Load("api")
	if err != nil {
		t.Fatalf("step 4: read client binding after Done: %v", err)
	}
	if cb.State != store.StateDone {
		t.Fatalf("step 4: client binding state = %q, want done", cb.State)
	}
}
