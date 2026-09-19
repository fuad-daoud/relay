package relay

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/store"
)

type fakeRemote struct {
	calls []string

	whoAmIResp        remote.WhoAmI
	whoAmIErr         error
	candidatesResp    remote.CandidatesResponse
	candidatesErr     error
	createBindingResp remote.BindingView
	createBindingErr  error
	getBindingResp    remote.BindingView
	getBindingErr     error
	startRoundResp    remote.BindingView
	startRoundErr     error
	roundFileResp     io.ReadCloser
	roundFileErr      error
	roundBundleResp   io.ReadCloser
	roundBundleErr    error
	ackResp           remote.BindingView
	ackErr            error
	unavailableErr    error
	doneErr           error
	unbindErr         error
	resumeResp        remote.BindingView
	resumeErr         error

	onCreateBinding func()
}

func (f *fakeRemote) WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error) {
	f.calls = append(f.calls, "WhoAmI:"+server)
	return f.whoAmIResp, f.whoAmIErr
}

func (f *fakeRemote) Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error) {
	f.calls = append(f.calls, "Candidates:"+server)
	return f.candidatesResp, f.candidatesErr
}

func (f *fakeRemote) CreateBinding(ctx context.Context, server string, req remote.CreateBindingRequest) (remote.BindingView, error) {
	f.calls = append(f.calls, "CreateBinding:"+server+":"+req.Name)
	if f.onCreateBinding != nil {
		f.onCreateBinding()
	}
	return f.createBindingResp, f.createBindingErr
}

func (f *fakeRemote) GetBinding(ctx context.Context, server, name string) (remote.BindingView, error) {
	f.calls = append(f.calls, "GetBinding:"+server+":"+name)
	return f.getBindingResp, f.getBindingErr
}

func (f *fakeRemote) StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader) (remote.BindingView, error) {
	f.calls = append(f.calls, fmt.Sprintf("StartRound:%s:%s:%d", server, name, round))
	return f.startRoundResp, f.startRoundErr
}

func (f *fakeRemote) RoundFile(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
	f.calls = append(f.calls, fmt.Sprintf("RoundFile:%s:%s:%d:%s", server, name, round, kind))
	return f.roundFileResp, f.roundFileErr
}

func (f *fakeRemote) RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
	f.calls = append(f.calls, fmt.Sprintf("RoundBundle:%s:%s:%d:%s", server, name, round, since))
	return f.roundBundleResp, f.roundBundleErr
}

func (f *fakeRemote) Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error) {
	f.calls = append(f.calls, fmt.Sprintf("Ack:%s:%s:%d", server, name, round))
	return f.ackResp, f.ackErr
}

func (f *fakeRemote) Unavailable(ctx context.Context, server, name, token, reason string) error {
	f.calls = append(f.calls, fmt.Sprintf("Unavailable:%s:%s:%s", server, name, token))
	return f.unavailableErr
}

func (f *fakeRemote) Done(ctx context.Context, server, name string) error {
	f.calls = append(f.calls, fmt.Sprintf("Done:%s:%s", server, name))
	return f.doneErr
}

func (f *fakeRemote) Unbind(ctx context.Context, server, name string) error {
	f.calls = append(f.calls, fmt.Sprintf("Unbind:%s:%s", server, name))
	return f.unbindErr
}

func (f *fakeRemote) Resume(ctx context.Context, server, name string) (remote.BindingView, error) {
	f.calls = append(f.calls, fmt.Sprintf("Resume:%s:%s", server, name))
	return f.resumeResp, f.resumeErr
}

type fakeTransport struct {
	snapshotCalls []snapshotCall
	snapshotResp  remote.Snapshot
	snapshotErr   error
	absorbCalls   []absorbCall
	absorbResp    map[string]string
	absorbErr     error
}

type snapshotCall struct {
	Repo  string
	Refs  []string
	Since string
}

type absorbCall struct {
	Repo        string
	ContentType string
	Refs        []string
}

func (f *fakeTransport) Snapshot(ctx context.Context, repo string, refs []string, since string) (remote.Snapshot, error) {
	f.snapshotCalls = append(f.snapshotCalls, snapshotCall{Repo: repo, Refs: refs, Since: since})
	if f.snapshotErr != nil {
		return remote.Snapshot{}, f.snapshotErr
	}
	return f.snapshotResp, nil
}

func (f *fakeTransport) Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error) {
	f.absorbCalls = append(f.absorbCalls, absorbCall{Repo: repo, ContentType: contentType, Refs: refs})
	if f.absorbErr != nil {
		return nil, f.absorbErr
	}
	return f.absorbResp, nil
}

func TestAddRemoteCreatesBranchAfterServerAgrees(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}

	var events []string
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
		onCreateBinding: func() {
			events = append(events, "CreateBinding")
		},
	}

	origCreateBranch := fg.CreateBranch
	_ = origCreateBranch
	// We wrap CreateBranch call via the custom tracker
	rt := Runtime{
		Store:  st,
		Git:    fg,
		Remote: fr,
		Now:    time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}

	res, err := Add(ctx, rt, opts)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if len(events) != 1 || events[0] != "CreateBinding" {
		t.Fatalf("events = %v, want [CreateBinding]", events)
	}
	if len(fg.createBranchCalls) != 1 {
		t.Fatalf("createBranchCalls = %d, want 1", len(fg.createBranchCalls))
	}
	if res.Branch != "relay/api" {
		t.Fatalf("res.Branch = %q, want relay/api", res.Branch)
	}
}

func TestAddRemoteRefusesCWD(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	rt := Runtime{
		Store:  st,
		Git:    &fakeGit{},
		Remote: &fakeRemote{},
		Now:    time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		CWD:    "/some/dir",
		Repo:   "/fake/repo",
	}

	_, err := Add(ctx, rt, opts)
	if err == nil || !strings.Contains(err.Error(), "remote builders are add-only: --cwd and --server cannot be combined") {
		t.Fatalf("got err %v, want '--cwd and --server cannot be combined'", err)
	}
}

func TestAddRemoteServerConflict(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingErr: &client.HTTPError{
			Status: 409,
			Body: remote.ErrorBody{
				Code:    "conflict",
				Message: "already exists",
			},
		},
	}
	rt := Runtime{
		Store:  st,
		Git:    fg,
		Remote: fr,
		Now:    time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}

	_, err := Add(ctx, rt, opts)
	if err == nil || !strings.Contains(err.Error(), "binding api already exists on zen for this client; relay bind --resume --name api") {
		t.Fatalf("got err %v, want 409 conflict message", err)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Fatalf("createBranch called %d times, want 0", len(fg.createBranchCalls))
	}
}

func TestAddRemoteBranchExistsUnbindsServer(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:    "1111111111111111111111111111111111111111",
		rootCommitSHA:   "2222222222222222222222222222222222222222",
		createBranchErr: git.ErrBranchExists,
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name: "api",
		},
	}
	rt := Runtime{
		Store:  st,
		Git:    fg,
		Remote: fr,
		Now:    time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}

	_, err := Add(ctx, rt, opts)
	if err == nil || !strings.Contains(err.Error(), "branch relay/api exists; delete it or pick another name") {
		t.Fatalf("got err %v, want 'branch relay/api exists...'", err)
	}

	// Verify unbind was called on server
	foundUnbind := false
	for _, call := range fr.calls {
		if strings.HasPrefix(call, "Unbind:zen:api") {
			foundUnbind = true
			break
		}
	}
	if !foundUnbind {
		t.Fatalf("Unbind was not called on server, calls = %v", fr.calls)
	}
}

func TestSendRemoteRecordsOnlyOnSuccess(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relay/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relay/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable),
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relay/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile)
	if err == nil || !strings.Contains(err.Error(), "zen unreachable") {
		t.Fatalf("Send got %v, want zen unreachable", err)
	}

	// No plan file written
	if _, err := os.Stat(st.PlanPath("api", 1)); !os.IsNotExist(err) {
		t.Fatalf("PlanPath exists after failure: %v", err)
	}

	// No log entry written
	entries, _ := st.ReadLog("api")
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}

	// Round unchanged
	reloaded, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Round != 1 {
		t.Fatalf("reloaded.Round = %d, want 1", reloaded.Round)
	}
	if !reloaded.RoundStartedAt.IsZero() {
		t.Fatalf("reloaded.RoundStartedAt is not zero: %v", reloaded.RoundStartedAt)
	}
}

func TestSendRemoteRoundStartedIsSuccess(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relay/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relay/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: &client.HTTPError{
			Status: 409,
			Body: remote.ErrorBody{
				Code:    remote.CodeRoundStarted,
				Message: "already started",
			},
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relay/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Send(ctx, rt, "api", planFile)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if res.Round != 1 {
		t.Fatalf("res.Round = %d, want 1", res.Round)
	}

	// Plan file written
	if _, err := os.Stat(st.PlanPath("api", 1)); err != nil {
		t.Fatalf("PlanPath missing: %v", err)
	}

	// Log entry written
	entries, _ := st.ReadLog("api")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

func TestSendRemoteFirstSendFullBundle(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relay/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:        store.ModeRemote,
			Server:      "zen",
			LastShipped: "", // first send
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relay/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relay/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if len(ft.snapshotCalls) != 1 {
		t.Fatalf("snapshotCalls = %d, want 1", len(ft.snapshotCalls))
	}
	if ft.snapshotCalls[0].Since != "" {
		t.Fatalf("snapshot Since = %q, want empty string for first send", ft.snapshotCalls[0].Since)
	}
}

func TestSendRemoteSetsLastShipped(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relay/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	outSHA := "3333333333333333333333333333333333333333"
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relay/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relay/api/out": outSHA},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	reloaded, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Builder.LastShipped != outSHA {
		t.Fatalf("reloaded LastShipped = %q, want %q", reloaded.Builder.LastShipped, outSHA)
	}
}
