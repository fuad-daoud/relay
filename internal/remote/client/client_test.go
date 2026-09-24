package client_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\nOutput: %s", strings.Join(args, " "), dir, err, string(out))
	}

	// A repo these tests create gets auto-maintenance off. Every `git commit`
	// otherwise spawns `git maintenance run --auto --quiet --detach`, which
	// outlives the command and writes under .git/objects while t.TempDir()'s
	// RemoveAll is removing the tree -- and that cleanup failure fails the
	// test, not just the teardown (#304). Repo-local config, so every later
	// git command on it inherits it, including ones the code under test runs.
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return strings.TrimSpace(string(out))
}

type scriptRunner struct {
	mu           sync.Mutex
	specs        []relevo.ProcSpec
	aliveHandles []relevo.ProcHandle
	pidSeq       int
	alive        bool
}

func newScriptRunner() *scriptRunner {
	return &scriptRunner{alive: true}
}

func (r *scriptRunner) Start(ctx context.Context, spec relevo.ProcSpec) (relevo.ProcHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	r.pidSeq++
	r.alive = true
	if spec.LogPath != "" {
		_ = os.WriteFile(spec.LogPath, []byte("builder started\n"), 0o644)
	}
	return relevo.ProcHandle{PID: 1000 + r.pidSeq, StartedAt: time.Now()}, nil
}

func (r *scriptRunner) Alive(ctx context.Context, h relevo.ProcHandle) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliveHandles = append(r.aliveHandles, h)
	return r.alive, nil
}

func (r *scriptRunner) ExitCode(ctx context.Context, h relevo.ProcHandle, logPath string) (code int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alive {
		return 0, false
	}
	return 0, true
}

func (r *scriptRunner) Kill(ctx context.Context, h relevo.ProcHandle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive = false
	return nil
}

func (r *scriptRunner) Rusage(ctx context.Context, h relevo.ProcHandle, streamPath string) (relevo.ProcRusage, bool) {
	return relevo.ProcRusage{}, false
}

func (r *scriptRunner) setAlive(a bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive = a
}

type testServerFixture struct {
	srv         *serve.Server
	ts          *httptest.Server
	kp          remote.Keypair
	fingerprint string
	runner      *scriptRunner
	gitClient   *git.Client
	transport   *remote.BundleTransport
	serverRoot  string
	machineDB   *db.DB
}

func startTestServer(t *testing.T) (*client.Client, *testServerFixture) {
	t.Helper()
	serverRoot := t.TempDir()
	machineDB, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("open machine db: %v", err)
	}
	t.Cleanup(func() { _ = machineDB.Close() })
	secrets := serve.SecretStore{DB: machineDB, Root: serverRoot}
	fp, err := serve.InitTLS(secrets, []string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	cert, err := serve.LoadTLS(secrets)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}

	cJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	candPath := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(candPath, []byte(cJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}

	gitClient := git.NewClient("git", 0, 0)
	runner := newScriptRunner()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}

	clientsPath := filepath.Join(serverRoot, "clients.json")
	cls, err := serve.LoadClients(machineDB, clientsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cls.Add("alice", remote.MarshalPublic(kp.Public, "alice@test"), time.Now()); err != nil {
		t.Fatal(err)
	}

	srv, err := serve.New(serve.Config{
		Root:       serverRoot,
		DB:         machineDB,
		Candidates: cSet,
		Runner:     runner,
		Git:        gitClient,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	ts.StartTLS()
	t.Cleanup(ts.Close)

	transport := remote.NewBundleTransport(gitClient, t.TempDir())

	servers := client.Servers{
		"zen": client.ServerEntry{
			URL:         ts.URL,
			Fingerprint: fp,
		},
	}
	cl := client.New(servers, kp, time.Now)

	fix := &testServerFixture{
		srv:         srv,
		ts:          ts,
		kp:          kp,
		fingerprint: fp,
		runner:      runner,
		gitClient:   gitClient,
		transport:   transport,
		serverRoot:  serverRoot,
		machineDB:   machineDB,
	}
	return cl, fix
}

func TestWhoAmIPinned(t *testing.T) {
	cl, fix := startTestServer(t)
	ctx := context.Background()

	who, err := cl.WhoAmI(ctx, "zen")
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if who.ID != remote.IDOf(fix.kp.Public) {
		t.Fatalf("whoami id = %q, want %q", who.ID, remote.IDOf(fix.kp.Public))
	}
	if who.Label != "alice" {
		t.Fatalf("whoami label = %q, want 'alice'", who.Label)
	}
}

func TestCertChangedRefused(t *testing.T) {
	_, fix := startTestServer(t)
	ctx := context.Background()

	// A client pinned to a different fingerprint
	wrongServers := client.Servers{
		"zen": client.ServerEntry{
			URL:         fix.ts.URL,
			Fingerprint: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	cl := client.New(wrongServers, fix.kp, time.Now)

	_, err := cl.WhoAmI(ctx, "zen")
	if !errors.Is(err, client.ErrCertChanged) {
		t.Fatalf("WhoAmI with bad fingerprint got %v, want ErrCertChanged", err)
	}
}

func TestUnknownServer(t *testing.T) {
	cl, _ := startTestServer(t)
	ctx := context.Background()

	_, err := cl.WhoAmI(ctx, "unknown-server")
	if !errors.Is(err, client.ErrUnknownServer) {
		t.Fatalf("WhoAmI got %v, want ErrUnknownServer", err)
	}
}

func TestUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // closed port

	servers := client.Servers{
		"closed": client.ServerEntry{
			URL:      "http://" + addr,
			Insecure: true,
		},
	}
	kp, _ := remote.Generate()
	cl := client.New(servers, kp, time.Now)
	ctx := context.Background()

	_, err = cl.WhoAmI(ctx, "closed")
	if !errors.Is(err, client.ErrUnreachable) {
		t.Fatalf("WhoAmI on closed port got %v, want ErrUnreachable", err)
	}
}

func TestNotEnrolledIs401Body(t *testing.T) {
	_, fix := startTestServer(t)
	ctx := context.Background()

	unenrolledKey, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}

	servers := client.Servers{
		"zen": client.ServerEntry{
			URL:         fix.ts.URL,
			Fingerprint: fix.fingerprint,
		},
	}
	cl := client.New(servers, unenrolledKey, time.Now)

	_, err = cl.WhoAmI(ctx, "zen")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var httpErr *client.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("got error %T (%v), want *HTTPError", err, err)
	}
	if httpErr.Status != 401 {
		t.Fatalf("http status = %d, want 401", httpErr.Status)
	}
	if httpErr.Body.Code != remote.CodeNotEnrolled {
		t.Fatalf("error code = %q, want %q", httpErr.Body.Code, remote.CodeNotEnrolled)
	}
}

func TestCreateStartFilesBundleAck(t *testing.T) {
	cl, fix := startTestServer(t)
	ctx := context.Background()

	// Temp client repo
	clientDir := t.TempDir()
	runGit(t, clientDir, "init")
	runGit(t, clientDir, "config", "user.name", "Test")
	runGit(t, clientDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(clientDir, "file.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clientDir, "add", "file.txt")
	runGit(t, clientDir, "commit", "-m", "initial commit")

	headSHA, err := fix.gitClient.HeadCommit(ctx, clientDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	rootSHA, err := fix.gitClient.RootCommit(ctx, clientDir)
	if err != nil {
		t.Fatalf("RootCommit: %v", err)
	}
	repoID, err := remote.RepoID(rootSHA)
	if err != nil {
		t.Fatalf("RepoID: %v", err)
	}

	// 1. Create binding
	createReq := remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     repoID,
		BaseCommit: headSHA,
	}
	view, err := cl.CreateBinding(ctx, "zen", createReq)
	if err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}
	if view.Name != "api" {
		t.Fatalf("binding name = %q, want api", view.Name)
	}

	// 2. StartRound with a real bundle from temp client repo
	runGit(t, clientDir, "branch", "relevo/api", headSHA)
	if err := fix.gitClient.UpdateRef(ctx, clientDir, "refs/relevo/api/out", headSHA, ""); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	snap, err := fix.transport.Snapshot(ctx, clientDir, []string{"refs/relevo/api/out"}, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer snap.Body.Close()

	startView, err := cl.StartRound(ctx, "zen", "api", 1, []byte("# Round 1 Plan\nImplement feature"), snap.Body, "", "", nil, false)
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if startView.RoundState != remote.RoundRunning {
		t.Fatalf("startView RoundState = %q, want %q", startView.RoundState, remote.RoundRunning)
	}

	// 3. Verify server's worktree exists
	idDir, ok := remote.IDOf(fix.kp.Public).Dir()
	if !ok {
		t.Fatal("failed to get dir from client ID")
	}
	st := store.NewShared(filepath.Join(fix.serverRoot, "bindings", idDir), string(remote.IDOf(fix.kp.Public)), fix.machineDB)
	b, err := st.Load("api")
	if err != nil {
		t.Fatalf("st.Load(api): %v", err)
	}
	if _, err := os.Stat(b.Worktree); err != nil {
		t.Fatalf("server worktree stat: %v", err)
	}

	workFile := filepath.Join(b.Worktree, "result.txt")
	if err := os.WriteFile(workFile, []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, b.Worktree, "add", "result.txt")
	runGit(t, b.Worktree, "commit", "-m", "round 1 result")

	// 4. Write report+marker on server side and tick it
	reportText := "Finished round\n\n```relevo\nstatus: done\n```\n"
	if err := os.WriteFile(st.ReportPath("api", 1), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.DonePath("api", 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	fix.runner.setAlive(false)

	if err := fix.srv.Tick(ctx); err != nil {
		t.Fatalf("srv.Tick: %v", err)
	}

	// 5. GetBinding closed with ClosedRound == 1
	closedView, err := cl.GetBinding(ctx, "zen", "api")
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if closedView.RoundState != remote.RoundClosed {
		t.Fatalf("closedView RoundState = %q, want %q", closedView.RoundState, remote.RoundClosed)
	}
	if closedView.ClosedRound != 1 {
		t.Fatalf("closedView ClosedRound = %d, want 1", closedView.ClosedRound)
	}

	// 6. RoundFile report
	reportRC, err := cl.RoundFile(ctx, "zen", "api", 1, "report")
	if err != nil {
		t.Fatalf("RoundFile: %v", err)
	}
	reportData, err := io.ReadAll(reportRC)
	_ = reportRC.Close()
	if err != nil {
		t.Fatalf("read reportRC: %v", err)
	}
	if !strings.Contains(string(reportData), "status: done") {
		t.Fatalf("reportData = %q, want status: done", string(reportData))
	}

	// 7. RoundBundle absorbed into the client repo
	bundleRC, err := cl.RoundBundle(ctx, "zen", "api", 1, headSHA)
	if err != nil {
		t.Fatalf("RoundBundle: %v", err)
	}
	if bundleRC == nil {
		t.Fatal("RoundBundle returned nil, want bundle stream")
	}
	absorbed, err := fix.transport.Absorb(ctx, clientDir, remote.ContentTypeGitBundle, bundleRC, []string{"refs/heads/relevo/api"})
	_ = bundleRC.Close()
	if err != nil {
		t.Fatalf("Absorb round bundle: %v", err)
	}
	if len(absorbed) == 0 {
		t.Fatal("no refs absorbed")
	}

	// 8. Ack
	ackView, err := cl.Ack(ctx, "zen", "api", 1)
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if ackView.AckedRound != 1 {
		t.Fatalf("ackView.AckedRound = %d, want 1", ackView.AckedRound)
	}
}

// TestAvailableRoundTrip: there is no Unavailable client test to copy, so the
// Available client is covered by a bare httptest round-trip -- the handler
// records the request and answers with an AvailableResponse, which the client
// must decode.
func TestAvailableRoundTrip(t *testing.T) {
	var gotPath, gotBody, gotContentType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(remote.AvailableResponse{Provider: "anthropic", Removed: 2})
	}))
	defer ts.Close()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	servers := client.Servers{
		"zen": client.ServerEntry{URL: ts.URL, Insecure: true},
	}
	cl := client.New(servers, kp, time.Now)

	resp, err := cl.Available(context.Background(), "zen", "claude/anthropic/haiku")
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if resp.Provider != "anthropic" || resp.Removed != 2 {
		t.Fatalf("Available = %+v, want provider anthropic, removed 2", resp)
	}
	if gotPath != "/v1/available" {
		t.Errorf("path = %q, want /v1/available", gotPath)
	}
	if !strings.Contains(gotBody, `"subject":"claude/anthropic/haiku"`) {
		t.Errorf("body = %q, want it naming the subject", gotBody)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
}

// TestStartRoundSendsTagsField pins the wire shape of the tags data (#242): a
// JSON array in the "tags" form field when tags are given, no field at all
// when they are nil.
func TestStartRoundSendsTagsField(t *testing.T) {
	ctx := context.Background()

	var mu sync.Mutex
	var gotTags string
	var hadTags bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("server ParseMultipartForm: %v", err)
		}
		mu.Lock()
		gotTags = r.FormValue("tags")
		_, hadTags = r.MultipartForm.Value["tags"]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"api","round_state":"running"}`))
	}))
	defer ts.Close()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	cl := client.New(client.Servers{
		"zen": client.ServerEntry{URL: ts.URL, Insecure: true},
	}, kp, time.Now)

	tags := []remote.TagRef{
		{Name: "v0", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Name: "v1", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if _, err := cl.StartRound(ctx, "zen", "api", 1, []byte("# Plan"), nil, "", "", tags, false); err != nil {
		t.Fatalf("StartRound with tags: %v", err)
	}
	want, err := json.Marshal(tags)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got, had := gotTags, hadTags
	mu.Unlock()
	if !had {
		t.Fatal("no tags field on the request when tags were given")
	}
	if got != string(want) {
		t.Fatalf("tags field = %q, want %q", got, string(want))
	}

	if _, err := cl.StartRound(ctx, "zen", "api", 1, []byte("# Plan"), nil, "", "", nil, false); err != nil {
		t.Fatalf("StartRound without tags: %v", err)
	}
	mu.Lock()
	_, had = gotTags, hadTags
	mu.Unlock()
	if had {
		t.Fatal("tags field sent when tags were nil")
	}
}
