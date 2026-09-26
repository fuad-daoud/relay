package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ownerEnv is a second (or third...) enrolled client with its own tiny git
// repo, for tests that need more than setupTestEnv's single owner (#285's
// admit tests need several bindings competing for the same builder cap).
type ownerEnv struct {
	kp        remote.Keypair
	id        remote.ClientID
	clientDir string
	headSHA   string
	repoID    string
}

// addOwner enrolls a fresh client against env's server and gives it its own
// one-commit git repo to send rounds from, mirroring setupTestEnv's own
// single-owner setup.
func addOwner(t *testing.T, env *testEnv, label string) ownerEnv {
	t.Helper()
	ctx := context.Background()

	clientDir := t.TempDir()
	runGit(t, clientDir, "init")
	runGit(t, clientDir, "config", "user.name", label)
	runGit(t, clientDir, "config", "user.email", label+"@example.com")
	if err := os.WriteFile(filepath.Join(clientDir, "file.txt"), []byte(label+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clientDir, "add", "file.txt")
	runGit(t, clientDir, "commit", "-m", "initial commit")

	headSHA, ok, err := env.gitClient.RefSHA(ctx, clientDir, "HEAD")
	if err != nil || !ok {
		t.Fatalf("headSHA: %v, ok=%v", err, ok)
	}
	rootSHA, err := env.gitClient.RootCommit(ctx, clientDir)
	if err != nil {
		t.Fatalf("rootCommit: %v", err)
	}
	repoID, err := remote.RepoID(rootSHA)
	if err != nil {
		t.Fatalf("repoID: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := env.srv.clients.Add(label, remote.MarshalPublic(kp.Public, label), time.Now()); err != nil {
		t.Fatal(err)
	}

	return ownerEnv{kp: kp, id: id, clientDir: clientDir, headSHA: headSHA, repoID: repoID}
}

// sendRound creates binding name for the given owner and starts round 1 on
// it with plan, returning the round-start response exactly as the client
// would see it. No git identity rides on the create request (sendRoundAs
// with nil does the same thing); the tests that care about #335 call
// sendRoundAs directly.
func sendRound(t *testing.T, env *testEnv, kp remote.Keypair, clientDir, repoID, headSHA, name, plan string) (*http.Response, []byte) {
	t.Helper()
	return sendRoundAs(t, env, kp, clientDir, repoID, headSHA, name, plan, nil)
}

// sendRoundAs is sendRound with the client's git identity carried on the
// create request (#335), so a test can drive what the server stores and what
// the builder it starts runs with.
func sendRoundAs(t *testing.T, env *testEnv, kp remote.Keypair, clientDir, repoID, headSHA, name, plan string, author *remote.GitIdentity) (*http.Response, []byte) {
	t.Helper()
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       name,
		RepoID:     repoID,
		BaseCommit: headSHA,
		Author:     author,
	})
	resp, body := doSigned(t, env.ts, kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create binding %s status = %d, want 201; body: %s", name, resp.StatusCode, string(body))
	}

	outRef := "refs/relevo/" + name + "/out"
	if err := env.gitClient.UpdateRef(ctx, clientDir, outRef, headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	snap, err := env.transport.Snapshot(ctx, clientDir, []string{outRef}, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	bundleBytes, err := io.ReadAll(snap.Body)
	_ = snap.Body.Close()
	if err != nil {
		t.Fatalf("read snap body: %v", err)
	}

	formBytes, ct := makeRoundForm(t, 1, plan, bundleBytes)
	return doSigned(t, env.ts, kp, "POST", "/v1/bindings/"+name+"/rounds", formBytes, ct)
}

// startedSpecs copies the runner's specs under its mutex, so a test can read
// what a round started without racing the handler that started it.
func startedSpecs(env *testEnv) []relevo.ProcSpec {
	env.runner.mu.Lock()
	defer env.runner.mu.Unlock()
	return append([]relevo.ProcSpec(nil), env.runner.specs...)
}

// TestRoundSpawnCarriesAuthorEnv pins #335's last hop: the round a binding's
// stored author starts runs the builder with that identity in its
// environment, so every commit it makes is the client's. A binding with no
// author gets no GIT_* additions at all.
func TestRoundSpawnCarriesAuthorEnv(t *testing.T) {
	authorEnv := []string{
		"GIT_AUTHOR_NAME=Ada Lovelace",
		"GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada Lovelace",
		"GIT_COMMITTER_EMAIL=ada@example.com",
	}

	t.Run("author set", func(t *testing.T) {
		env := setupTestEnv(t)
		author := &remote.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
		resp, body := sendRoundAs(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan", author)
		requireCreated(t, resp, body, "api")

		specs := startedSpecs(env)
		if len(specs) == 0 {
			t.Fatal("round started no process")
		}
		for _, want := range authorEnv {
			found := false
			for _, got := range specs[0].Env {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("spec.Env = %v, want it to contain %q", specs[0].Env, want)
			}
		}
	})

	t.Run("no author", func(t *testing.T) {
		env := setupTestEnv(t)
		resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan")
		requireCreated(t, resp, body, "api")

		specs := startedSpecs(env)
		if len(specs) == 0 {
			t.Fatal("round started no process")
		}
		if len(specs[0].Env) != 0 {
			t.Errorf("spec.Env = %v, want nil or empty for a binding with no author", specs[0].Env)
		}
	})
}

// TestCreateBindingStoresAuthor pins #335's wire-to-store half: the git
// identity a client sends on the create request lands on the owner's
// ServeFacts, which is what the builder environment reads later.
func TestCreateBindingStoresAuthor(t *testing.T) {
	env := setupTestEnv(t)

	author := &remote.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
	resp, body := sendRoundAs(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan", author)
	requireCreated(t, resp, body, "api")

	b, err := env.runtime(t).Store.Load("api")
	if err != nil {
		t.Fatalf("Load api: %v", err)
	}
	if b.Serve == nil {
		t.Fatal("binding has no ServeFacts")
	}
	if b.Serve.AuthorName != "Ada Lovelace" || b.Serve.AuthorEmail != "ada@example.com" {
		t.Errorf("ServeFacts author = %q <%q>, want Ada Lovelace <ada@example.com>",
			b.Serve.AuthorName, b.Serve.AuthorEmail)
	}
}

// TestCreateBindingRejectsBadAuthor pins #335's validation: a malformed
// author is a 400 invalid and stores nothing, so a value that could forge a
// line in the builder's environment never reaches the store.
func TestCreateBindingRejectsBadAuthor(t *testing.T) {
	env := setupTestEnv(t)

	cases := []struct {
		name  string
		email string
	}{
		{"", "a@b"},
		{"Ada", ""},
		{"Ada\nX", "a@b"},
		{"Ada", "<a@b>"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q/%q", tc.name, tc.email), func(t *testing.T) {
			createBody, err := json.Marshal(remote.CreateBindingRequest{
				Name:       "api",
				RepoID:     env.repoID,
				BaseCommit: env.headSHA,
				Author:     &remote.GitIdentity{Name: tc.name, Email: tc.email},
			})
			if err != nil {
				t.Fatal(err)
			}
			resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(body))
			}
			var werr remote.ErrorBody
			if err := json.Unmarshal(body, &werr); err != nil {
				t.Fatalf("unmarshal error body: %v; body: %s", err, string(body))
			}
			if werr.Code != remote.CodeInvalid {
				t.Errorf("error code = %q, want %q", werr.Code, remote.CodeInvalid)
			}
			if _, lerr := env.runtime(t).Store.Load("api"); !errors.Is(lerr, store.ErrNotFound) {
				t.Errorf("Load api err = %v, want store.ErrNotFound", lerr)
			}
		})
	}
}

// closeRound writes round 1's report and completion marker for name, and
// marks pid exited on runner without disturbing any other pid it is
// tracking -- what a multi-owner test needs that the single-pid
// runner.setAlive(false) used elsewhere cannot give it.
func closeRound(t *testing.T, rt relevo.Runtime, name string, pid int, runner *scriptRunner) {
	t.Helper()
	reportText := "# Report 1\nDone.\n\n```relevo\nstatus: done\n```\n"
	if err := os.WriteFile(rt.Store.ReportPath(name, 1), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath(name, 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.finish(pid)
}

func requireCreated(t *testing.T, resp *http.Response, body []byte, label string) {
	t.Helper()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("%s start status = %d, want 201; body: %s", label, resp.StatusCode, string(body))
	}
}

func decodeView(t *testing.T, body []byte) remote.BindingView {
	t.Helper()
	var view remote.BindingView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal binding view: %v; body: %s", err, string(body))
	}
	return view
}

// TestAdmitCapQueuesSecondOwner pins #285's cap: with MaxBuilders 1, a
// second owner's round accepts (201) but is queued, not started, and the
// wire carries its exact position.
//
// Mutation check: return true unconditionally from cap()'s "MaxBuilders >
// 0" branch check (i.e. hardcode a large cap) and this fails on B's
// round_state staying "running" instead of "queued".
func TestAdmitCapQueuesSecondOwner(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	viewA := decodeView(t, bodyA)
	if viewA.RoundState != remote.RoundRunning {
		t.Fatalf("A round_state = %q, want running", viewA.RoundState)
	}

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	viewB := decodeView(t, bodyB)
	if viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}
	if viewB.Queue == nil {
		t.Fatal("B Queue = nil, want a QueueView")
	}
	if viewB.Queue.Position != 1 || viewB.Queue.Ahead != 0 || viewB.Queue.Running != 1 || viewB.Queue.Cap != 1 {
		t.Errorf("B Queue = %+v, want {Position:1 Ahead:0 Running:1 Cap:1 ...}", viewB.Queue)
	}
	if viewB.Queue.Since.IsZero() {
		t.Error("B Queue.Since is zero, want the accept time")
	}

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/whoami", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	var who remote.WhoAmI
	if err := json.Unmarshal(body, &who); err != nil {
		t.Fatalf("unmarshal whoami: %v", err)
	}
	if who.Builders == nil || who.Builders.Running != 1 || who.Builders.Queued != 1 || who.Builders.Cap != 1 {
		t.Errorf("whoami.Builders = %+v, want {Running:1 Queued:1 Cap:1 ...}", who.Builders)
	}

	rtB := testRuntime(t, env.srv, ownerB.id)
	bB, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if bB.QueuedAt.IsZero() {
		t.Error("B QueuedAt is zero, want set")
	}
	if bB.Builder.PID != 0 {
		t.Errorf("B PID = %d, want 0", bB.Builder.PID)
	}

	if len(env.runner.specs) != 1 {
		t.Errorf("runner specs = %d, want 1 (only A started)", len(env.runner.specs))
	}
}

// TestAdmitAfterSlotFrees continues TestAdmitCapQueuesSecondOwner's setup:
// once A's round closes, the freed slot admits B on the next Tick, and the
// log carries both halves of the wait as separate entries.
func TestAdmitAfterSlotFrees(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	viewB := decodeView(t, bodyB)
	if viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	closeRound(t, rtA, "api", bA.Builder.PID, env.runner)

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	rtA2 := testRuntime(t, env.srv, env.id)
	bA2, err := rtA2.Store.Load("api")
	if err != nil {
		t.Fatalf("reload A: %v", err)
	}
	entriesA, _ := rtA2.Store.ReadLog("api")
	if relevo.RoundStateOf(bA2, entriesA) != remote.RoundClosed {
		t.Errorf("A round_state = %v, want closed", relevo.RoundStateOf(bA2, entriesA))
	}

	rtB := testRuntime(t, env.srv, ownerB.id)
	bB, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatalf("reload B: %v", err)
	}
	entriesB, err := rtB.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog B: %v", err)
	}
	if relevo.RoundStateOf(bB, entriesB) != remote.RoundRunning {
		t.Errorf("B round_state = %v, want running", relevo.RoundStateOf(bB, entriesB))
	}

	if len(env.runner.specs) != 2 {
		t.Errorf("runner specs = %d, want 2 (A then B)", len(env.runner.specs))
	}

	var queueNotes []string
	for _, e := range entriesB {
		if e.Kind == store.KindQueue {
			queueNotes = append(queueNotes, e.Note)
		}
	}
	if len(queueNotes) != 2 {
		t.Fatalf("B queue notes = %v, want 2 entries", queueNotes)
	}
	if !strings.HasPrefix(queueNotes[0], "queued (1/1 builders busy)") {
		t.Errorf("B first queue note = %q, want it to start with %q", queueNotes[0], "queued (1/1 builders busy)")
	}
	if !strings.HasPrefix(queueNotes[1], "started after ") || !strings.HasSuffix(queueNotes[1], "queued") {
		t.Errorf("B second queue note = %q, want %q...%q", queueNotes[1], "started after ", "queued")
	}
}

// TestAdmitStrictFIFO pins #285's ordering: with MaxBuilders 1 and two
// queued rounds, the older one is admitted first every time a slot frees,
// never the newer one, and a round still queued reports its correct
// (shrinking) position.
//
// Mutation check: sort census.Queued by Name instead of QueuedAt and this
// fails on C running before B.
func TestAdmitStrictFIFO(t *testing.T) {
	clock := time.Now()
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return clock }
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	if viewA := decodeView(t, bodyA); viewA.RoundState != remote.RoundRunning {
		t.Fatalf("A round_state = %q, want running", viewA.RoundState)
	}

	clock = clock.Add(2 * time.Second)
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	clock = clock.Add(2 * time.Second)
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	viewC := decodeView(t, bodyC)
	if viewC.RoundState != remote.RoundQueued || viewC.Queue == nil || viewC.Queue.Position != 2 {
		t.Fatalf("C view = %+v, want queued at position 2", viewC)
	}

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	closeRound(t, rtA, "api", bA.Builder.PID, env.runner)

	clock = clock.Add(2 * time.Second)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}

	rtB := testRuntime(t, env.srv, ownerB.id)
	bB, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	entriesB, _ := rtB.Store.ReadLog("api")
	if relevo.RoundStateOf(bB, entriesB) != remote.RoundRunning {
		t.Errorf("B round_state after tick 1 = %v, want running", relevo.RoundStateOf(bB, entriesB))
	}

	rtC := testRuntime(t, env.srv, ownerC.id)
	bC, err := rtC.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	entriesC, _ := rtC.Store.ReadLog("api")
	if relevo.RoundStateOf(bC, entriesC) != remote.RoundQueued {
		t.Errorf("C round_state after tick 1 = %v, want still queued", relevo.RoundStateOf(bC, entriesC))
	}
	if pos := queuePosition(t, env, ownerC.id); pos != 1 {
		t.Errorf("C position after tick 1 = %d, want 1", pos)
	}

	closeRound(t, rtB, "api", bB.Builder.PID, env.runner)
	clock = clock.Add(time.Minute)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	rtC2 := testRuntime(t, env.srv, ownerC.id)
	bC2, err := rtC2.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	entriesC2, _ := rtC2.Store.ReadLog("api")
	if relevo.RoundStateOf(bC2, entriesC2) != remote.RoundRunning {
		t.Errorf("C round_state after tick 2 = %v, want running", relevo.RoundStateOf(bC2, entriesC2))
	}
}

// queuePosition is q's 1-based position in a fresh census, or 0 if it is
// not currently queued.
func queuePosition(t *testing.T, env *testEnv, owner remote.ClientID) int {
	t.Helper()
	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	for i, q := range c.Queued {
		if q.Owner == owner {
			return i + 1
		}
	}
	return 0
}

// TestAdmitFreeSlotButQueueNonEmpty pins #285's FIFO guarantee against a
// stale-but-not-yet-reaped running slot: A's process is dead but no Tick
// has observed it yet, so census still counts A as running (PID != 0,
// State active) and a brand new send must never jump the existing queue.
//
// Mutation check: have handleStartRound's inline admit ignore an existing
// non-empty queue and try to admit the newest send directly, and this
// fails on C landing at position 1 instead of 2.
func TestAdmitFreeSlotButQueueNonEmpty(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	env.runner.finish(bA.Builder.PID) // dead, but no Tick has reaped it yet

	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	viewC := decodeView(t, bodyC)
	if viewC.RoundState != remote.RoundQueued || viewC.Queue == nil || viewC.Queue.Position != 2 {
		t.Fatalf("C view = %+v, want queued at position 2 (must not jump B)", viewC)
	}

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	rtB := testRuntime(t, env.srv, ownerB.id)
	bB, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	entriesB, _ := rtB.Store.ReadLog("api")
	if relevo.RoundStateOf(bB, entriesB) != remote.RoundRunning {
		t.Errorf("B round_state after tick = %v, want running", relevo.RoundStateOf(bB, entriesB))
	}
	if pos := queuePosition(t, env, ownerC.id); pos != 1 {
		t.Errorf("C position after tick = %d, want 1", pos)
	}
}

// TestRestartRequeuesDeadBuilder pins #285's server-side half of #244: a
// new Server standing in for a daemon restart, with its own runner that
// never started A's pid (so A's exit code is unknown, not "0" -- the
// signature of a supervisor that died with its child), re-queues A instead
// of relaunching it, then admits it again within the same Tick because the
// cap allows it.
//
// Mutation check: drop the `b.Owner != ""` branch in headless.go's lost
// handling (added in the relevo-package step of this plan) and this fails
// on newRunner.specs staying empty.
func TestRestartRequeuesDeadBuilder(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	oldStartedAt := bA.Builder.StartedAt

	newRunner := newScriptRunner()
	restarted, err := New(Config{
		DB:          env.srv.cfg.DB,
		Root:        env.srv.cfg.Root,
		Candidates:  env.srv.cfg.Candidates,
		Runner:      newRunner,
		Git:         env.gitClient,
		Now:         time.Now,
		MaxBuilders: 1,
		StartedAt:   time.Unix(oldStartedAt+60, 0), // after A's own builder start
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := restarted.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	entries, err := rtA.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var queueNotes []string
	for _, e := range entries {
		if e.Kind == store.KindQueue {
			queueNotes = append(queueNotes, e.Note)
		}
	}
	// The original send/admit already logged its own "queued (...)" /
	// "started after ..." pair; the restart adds a second pair on top of
	// it, so only the last two entries are this test's concern.
	if len(queueNotes) != 4 {
		t.Fatalf("queue notes = %v, want 4 (send+admit, then re-queue+re-admit)", queueNotes)
	}
	if queueNotes[2] != "re-queued (builder lost to a restart)" {
		t.Errorf("third queue note = %q, want %q", queueNotes[2], "re-queued (builder lost to a restart)")
	}
	if !strings.HasPrefix(queueNotes[3], "started after ") {
		t.Errorf("fourth queue note = %q, want it to start with %q", queueNotes[3], "started after ")
	}

	got, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.PID == 0 {
		t.Error("PID = 0, want a fresh pid from the restarted server's runner")
	}
	if got.RoundStartedAt.IsZero() {
		t.Error("RoundStartedAt is zero, want stamped by the re-admit")
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero (re-admitted within the same tick)", got.QueuedAt)
	}
	if len(newRunner.specs) != 1 {
		t.Errorf("newRunner specs = %d, want 1 (the re-admit spawn)", len(newRunner.specs))
	}
}

// TestUnbindDropsQueued pins #285: unbinding a queued binding removes it
// from the queue outright -- the census no longer counts it, and a Tick
// afterwards has nothing new to admit.
func TestUnbindDropsQueued(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	resp, body := doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings/api/unbind", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unbind status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(c.Queued) != 0 {
		t.Errorf("census.Queued = %+v, want empty", c.Queued)
	}

	specsBefore := len(env.runner.specs)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(env.runner.specs) != specsBefore {
		t.Errorf("specs after tick = %d, want unchanged at %d (nothing left to admit)", len(env.runner.specs), specsBefore)
	}
}

// TestWireUnbindStopsRunningRound pins #331's premise for the wire verb the
// hints now name: POST /v1/bindings/{name}/unbind has no running-round guard
// and no --force, yet it does stop a running remote round -- the server's
// builder process is killed and the binding is archived out of the owner's
// live store.
func TestWireUnbindStopsRunningRound(t *testing.T) {
	env := setupTestEnv(t)

	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, resp, body, "A")
	if view := decodeView(t, body); view.RoundState != remote.RoundRunning {
		t.Fatalf("round_state = %q, want running", view.RoundState)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	pid := b.Builder.PID
	if pid == 0 {
		t.Fatal("builder PID = 0, want a running process")
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/unbind", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unbind status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	env.runner.mu.Lock()
	alive := env.runner.alive[pid]
	env.runner.mu.Unlock()
	if alive {
		t.Errorf("builder pid %d still alive after unbind, want killed", pid)
	}

	if _, err := rt.Store.Load("api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Load after unbind: err = %v, want ErrNotFound", err)
	}
}

// TestDoneRefusesQueuedRound pins #285's wire contract (§4.8): a queued
// round has no process to stop and nothing to hand back, so done is
// refused exactly like an open round.
func TestDoneRefusesQueuedRound(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	resp, body := doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings/api/done", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("done status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v; body: %s", err, string(body))
	}
	wantMsg := fmt.Sprintf("round %d is queued; relevo stop to drop it from the queue, or unbind", 1)
	if errBody.Message != wantMsg {
		t.Errorf("error message = %q, want %q", errBody.Message, wantMsg)
	}
}

// TestStopDropsQueued pins #344's queued wire stop: with the cap taken, a
// second owner's queued round stops with 200 as "dequeued" rather than being
// refused, and the server's queue census no longer lists it.
func TestStopDropsQueued(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	resp, body := doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings/api/stop", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	view := decodeView(t, body)
	if view.Stopped != "dequeued" {
		t.Errorf("stopped = %q, want dequeued", view.Stopped)
	}
	if view.RoundState != remote.RoundClosed {
		t.Errorf("round_state = %q, want closed", view.RoundState)
	}

	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(c.Queued) != 0 {
		t.Errorf("census.Queued = %+v, want empty", c.Queued)
	}
}

// TestGetBindingQueuePosition pins #285's position math: three queued
// rounds report positions 1, 2 and 3 in FIFO (QueuedAt) order.
func TestGetBindingQueuePosition(t *testing.T) {
	clock := time.Now()
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return clock }
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")
	ownerD := addOwner(t, env, "dave")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	for _, o := range []ownerEnv{ownerB, ownerC, ownerD} {
		clock = clock.Add(2 * time.Second)
		resp, body := sendRound(t, env, o.kp, o.clientDir, o.repoID, o.headSHA, "api", "# Plan")
		requireCreated(t, resp, body, string(o.id))
	}

	for i, o := range []ownerEnv{ownerB, ownerC, ownerD} {
		resp, body := doSigned(t, env.ts, o.kp, "GET", "/v1/bindings/api", nil, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get binding status = %d, want 200; body: %s", resp.StatusCode, string(body))
		}
		view := decodeView(t, body)
		if view.Queue == nil || view.Queue.Position != i+1 {
			t.Errorf("owner %d Queue = %+v, want Position %d", i, view.Queue, i+1)
		}
	}
}

// TestHeldCPUsCrossOwnerCensus pins #314's server census: owner A's live round
// pins core 0 in its own store, and owner B's round -- started through the
// served runtime's HeldCPUs -- takes core 1.
//
// Mutation check: make heldCPUs read only the caller's tx (drop the other
// owners) and B gets core 0: the distinct-core assertion fails.
func TestHeldCPUsCrossOwnerCensus(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Scope = &relevo.ScopeSpec{CPUWeight: 100, AllowedCPUs: "0-1"}
	})
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	if bA.RoundCPU == nil {
		t.Fatal("A RoundCPU = nil, want 0")
	}
	if *bA.RoundCPU != 0 {
		t.Errorf("A RoundCPU = %d, want 0", *bA.RoundCPU)
	}

	rtB := testRuntime(t, env.srv, ownerB.id)
	bB, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if bB.RoundCPU == nil {
		t.Fatal("B RoundCPU = nil, want 1: A's core 0 is held across the owner boundary")
	}
	if *bB.RoundCPU != 1 {
		t.Errorf("B RoundCPU = %d, want 1: A's core 0 is held across the owner boundary", *bB.RoundCPU)
	}
}

// TestHeldCPUsSkipsAFailingOwner pins #314's error rule: an owner whose store
// cannot be listed is skipped and its error returned first, but the cores of
// the owners that did list are still returned.
func TestHeldCPUsSkipsAFailingOwner(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Scope = &relevo.ScopeSpec{CPUWeight: 100, AllowedCPUs: "0-1"}
	})
	ownerB := addOwner(t, env, "bob")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")

	// A third owner with a legacy binding file that cannot be imported:
	// bind.json holds invalid JSON, so the import fails and its List errors
	// where B's does not.
	ownerC := addOwner(t, env, "carol")
	cDir, ok := ownerC.id.Dir()
	if !ok {
		t.Fatal("carol's client id has no dir")
	}
	brokenDir := filepath.Join(env.srv.cfg.Root, "bindings", cDir, "broken")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "bind.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	aDir, ok := env.id.Dir()
	if !ok {
		t.Fatal("alice's client id has no dir")
	}
	rootA := filepath.Join(env.srv.cfg.Root, "bindings", aDir)
	rtA := testRuntime(t, env.srv, env.id)
	err := rtA.Store.WithLock(func(tx *store.Tx) error {
		held, herr := env.srv.heldCPUs(rootA, tx, "nobody")
		if herr == nil {
			t.Error("heldCPUs: want the unreadable owner's error, got nil")
		}
		found := false
		for _, c := range held {
			if c == 0 {
				found = true
			}
		}
		if !found {
			t.Errorf("held = %v, want B's core 0 despite the failing owner", held)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}
