package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
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
	roundFileFunc     func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error)
	roundBundleFunc   func(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error)
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
	if f.roundFileFunc != nil {
		return f.roundFileFunc(ctx, server, name, round, kind)
	}
	return f.roundFileResp, f.roundFileErr
}

func (f *fakeRemote) RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
	f.calls = append(f.calls, fmt.Sprintf("RoundBundle:%s:%s:%d:%s", server, name, round, since))
	if f.roundBundleFunc != nil {
		return f.roundBundleFunc(ctx, server, name, round, since)
	}
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

// TestProbeServersStates checks ProbeServers' one-state-per-outcome mapping
// over fakeRemote (#100 step 5): no client key, enrolled, not enrolled, a
// changed certificate, unreachable, and an unrecognised error.
func TestProbeServersStates(t *testing.T) {
	servers := map[string]client.ServerEntry{"zen": {URL: "https://zen:7777"}}

	cases := []struct {
		name       string
		rt         Runtime
		enrollLine string
		wantState  string
		wantLabel  string
		wantDetail string
	}{
		{
			name:      "no key",
			rt:        Runtime{},
			wantState: "no key", wantDetail: "run relay client init",
		},
		{
			name:      "enrolled",
			rt:        Runtime{Remote: &fakeRemote{whoAmIResp: remote.WhoAmI{Label: "laptop"}}},
			wantState: "enrolled", wantLabel: "laptop",
		},
		{
			name:       "not enrolled",
			rt:         Runtime{Remote: &fakeRemote{whoAmIErr: &client.HTTPError{Status: 401, Body: remote.ErrorBody{Code: "not_enrolled"}}}},
			enrollLine: "ed25519 AAAA... me@laptop",
			wantState:  "not enrolled", wantDetail: "ed25519 AAAA... me@laptop",
		},
		{
			name:      "cert changed",
			rt:        Runtime{Remote: &fakeRemote{whoAmIErr: client.ErrCertChanged}},
			wantState: "cert changed",
		},
		{
			name:      "unreachable",
			rt:        Runtime{Remote: &fakeRemote{whoAmIErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}},
			wantState: "unreachable", wantDetail: "dial tcp: connection refused",
		},
		{
			name:      "error",
			rt:        Runtime{Remote: &fakeRemote{whoAmIErr: errors.New("boom")}},
			wantState: "error", wantDetail: "boom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probes := ProbeServers(context.Background(), tc.rt, servers, tc.enrollLine)
			if len(probes) != 1 {
				t.Fatalf("got %d probes, want 1", len(probes))
			}
			p := probes[0]
			if p.Name != "zen" || p.URL != "https://zen:7777" {
				t.Fatalf("Name/URL = %q/%q, want zen/https://zen:7777", p.Name, p.URL)
			}
			if p.State != tc.wantState {
				t.Fatalf("State = %q, want %q", p.State, tc.wantState)
			}
			if p.Label != tc.wantLabel {
				t.Fatalf("Label = %q, want %q", p.Label, tc.wantLabel)
			}
			if p.Detail != tc.wantDetail {
				t.Fatalf("Detail = %q, want %q", p.Detail, tc.wantDetail)
			}
		})
	}
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

	// #100 step 6: the server picked the candidate (opts.Candidate == ""),
	// so the pick entry must say so rather than ExplainResolution's
	// "explicit, policy bypassed", which would misdescribe a token nobody
	// on this side named.
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var pickNote string
	for _, e := range entries {
		if e.Kind == store.KindPick {
			pickNote = e.Note
		}
	}
	if pickNote != "picked claude/anthropic/haiku on zen: server's pick" {
		t.Fatalf("pick note = %q, want it to name the server's own pick", pickNote)
	}
}

// TestAddRemoteTwicePerRepo pins #100: a remote binding's CWD is the repo its
// branch is cut from, not a working tree it drives, so two remote bindings
// may share one repo the same way two `relay add` worktrees do. (Mutation
// target: revert the assertCWDFree remote exemption in
// internal/store/store.go and this fails with ErrCWDTaken on the second
// addRemote call.)
func TestAddRemoteTwicePerRepo(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{
		Store:  st,
		Git:    fg,
		Remote: fr,
		Now:    time.Now,
	}

	if _, err := Add(ctx, rt, AddOptions{Name: "e2e2", Server: "zen", Repo: "/fake/repo"}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if _, err := Add(ctx, rt, AddOptions{Name: "e2e3", Server: "zen", Repo: "/fake/repo"}); err != nil {
		t.Fatalf("second Add with the same repo: %v", err)
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
	if err == nil || !strings.Contains(err.Error(), "binding api already exists on zen for this client but not here; relay serve unbind --owner <your label> api on the server, or choose another name") {
		t.Fatalf("got err %v, want 409 conflict message", err)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Fatalf("createBranch called %d times, want 0", len(fg.createBranchCalls))
	}
}

// TestAddRemoteCleansUpServerOnSaveFailure pins #100: any failure after
// CreateBinding succeeds -- not just ErrBranchExists -- unbinds the server
// binding it just created, best-effort, before addRemote returns. A
// fakeGit.CreateBranch error that is not ErrBranchExists exercises the
// general defer path (the ErrBranchExists path already has its own test,
// TestAddRemoteBranchExistsUnbindsServer). Mutation target: remove the
// deferred cleanup in addRemote and this fails, since Unbind is never
// called for this generic error.
func TestAddRemoteCleansUpServerOnSaveFailure(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:    "1111111111111111111111111111111111111111",
		rootCommitSHA:   "2222222222222222222222222222222222222222",
		createBranchErr: errors.New("disk full"),
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
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got err %v, want the CreateBranch error", err)
	}

	if !slices.Contains(fr.calls, "Unbind:zen:api") {
		t.Fatalf("Unbind was not called on server, calls = %v", fr.calls)
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

// remoteBinding is a minimal active remote binding, round 1, planner
// pointed at plannerAgent()'s pane so deliverAndSettle's FindAgent succeeds
// and does not turn the binding orphaned.
func remoteBinding(server string) store.Binding {
	return store.Binding{
		Name:    "api",
		CWD:     "/fake/repo",
		Repo:    "/fake/repo",
		Branch:  "relay/api",
		Round:   1,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: server,
		},
	}
}

func TestReconcileRemoteRunningMirrorsLog(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundRunning},
		roundFileResp:  io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Fatalf("RemoteStatus = %q, want %q", got.Builder.RemoteStatus, remote.RoundRunning)
	}
	data, err := os.ReadFile(st.BuilderLogPath("api", 1))
	if err != nil {
		t.Fatalf("read mirrored log: %v", err)
	}
	if string(data) != "builder log line 1\n" {
		t.Fatalf("log = %q, want mirrored content", string(data))
	}
	foundGet, foundLog := false, false
	for _, c := range fr.calls {
		if c == "GetBinding:zen:api" {
			foundGet = true
		}
		if c == "RoundFile:zen:api:1:log" {
			foundLog = true
		}
	}
	if !foundGet || !foundLog {
		t.Fatalf("calls = %v, want GetBinding and RoundFile(log)", fr.calls)
	}
	if got.State != store.StateActive {
		t.Fatalf("state = %s, want active while running", got.State)
	}
}

// TestReconcileRemoteRefreshesCandidate checks that a server-side switch
// (the round is now running a different candidate than this binding last
// recorded) updates the token and harness kind and logs a switch entry
// naming the server, and that a later tick whose view still names the same
// candidate adds nothing more (#100 step 2).
func TestReconcileRemoteRefreshesCandidate(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.BuilderCandidate = "claude/anthropic/haiku"
	b.Builder.Kind = "claude"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundRunning, Candidate: "opencode/anthropic/sonnet"},
		roundFileResp:  io.NopCloser(strings.NewReader("")),
	}
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Now: func() time.Time { return baseTime }}
	agents := []herdr.Agent{plannerAgent()}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderCandidate != "opencode/anthropic/sonnet" {
		t.Fatalf("BuilderCandidate = %q, want the server's candidate", got.BuilderCandidate)
	}
	if got.Builder.Kind != "opencode" {
		t.Fatalf("Builder.Kind = %q, want opencode", got.Builder.Kind)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	switches := 0
	var note string
	for _, e := range entries {
		if e.Kind == store.KindSwitch {
			switches++
			note = e.Note
		}
	}
	if switches != 1 {
		t.Fatalf("switch entries = %d, want 1", switches)
	}
	if !strings.Contains(note, "zen") || !strings.Contains(note, "claude/anthropic/haiku") || !strings.Contains(note, "opencode/anthropic/sonnet") {
		t.Fatalf("switch note = %q, want it to name the server and both tokens", note)
	}

	// Mutation target: compare view.Candidate against something other than
	// b.BuilderCandidate (or drop the comparison entirely) and a second,
	// identical view adds a second switch entry instead of nothing.
	got2, err := reconcile(t, rt, got, agents)
	if err != nil {
		t.Fatalf("Reconcile (second tick): %v", err)
	}
	if got2.BuilderCandidate != "opencode/anthropic/sonnet" {
		t.Fatalf("BuilderCandidate after second tick = %q, want unchanged", got2.BuilderCandidate)
	}
	entries2, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	switches2 := 0
	for _, e := range entries2 {
		if e.Kind == store.KindSwitch {
			switches2++
		}
	}
	if switches2 != 1 {
		t.Fatalf("switch entries after an identical second view = %d, want still 1", switches2)
	}
}

func TestReconcileRemoteNeedsYouHalts(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundNeedsYou, Halt: "stuck at a dialog"},
	}
	f := &fakeHerdr{}
	rt := Runtime{Store: st, Herdr: f, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if got.Halt != "stuck at a dialog" {
		t.Fatalf("Halt = %q, want the server's halt text", got.Halt)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "stuck at a dialog") {
		t.Fatalf("notices = %v, want one naming the server's halt", f.notices)
	}
}

func TestReconcileRemoteUnreachableIsNotHalt(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000 // 1s budget; unreachableGrace (30m) is what keeps this from halting below
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	f := &fakeHerdr{}
	rt := Runtime{Store: st, Herdr: f, Remote: fr, Now: func() time.Time { return baseTime }}
	agents := []herdr.Agent{plannerAgent()}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.RemoteUnreachableSince.IsZero() {
		t.Fatal("RemoteUnreachableSince must be set once unreachable")
	}
	if got.Builder.RemoteStatus != "unreachable" {
		t.Fatalf("RemoteStatus = %q, want unreachable", got.Builder.RemoteStatus)
	}

	// Five minutes later: well past the 1s budget alone, but inside the 30m
	// grace, so this must not halt.
	got, err = reconcile(t, at(rt, 5*time.Minute), got, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("halted after 5m unreachable; the grace should have covered it")
	}
	if len(f.notices) != 0 {
		t.Fatalf("notices = %v, want none before the grace elapses", f.notices)
	}
}

func TestReconcileRemoteUnreachablePastBudgetHalts(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000 // 1s budget
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	f := &fakeHerdr{}
	rt := Runtime{Store: st, Herdr: f, Remote: fr, Now: func() time.Time { return baseTime }}
	agents := []herdr.Agent{plannerAgent()}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Past the 1s budget plus the 30m unreachableGrace: this must halt.
	// Mutation target: drop "+ unreachableGrace" from the comparison in
	// reconcileRemote and this halt fires one tick early (or the
	// not-halt test above starts halting instead).
	got, err = reconcile(t, at(rt, 31*time.Minute), got, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you past budget+grace", got.State)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "unreachable for") || !strings.Contains(f.notices[0], "may still be running there") {
		t.Fatalf("notices = %v, want the unreachable-past-budget halt", f.notices)
	}
}

func TestReconcileRemote401Halts(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: &client.HTTPError{Status: 401, Body: remote.ErrorBody{Code: "revoked", Message: "key revoked"}}}
	f := &fakeHerdr{}
	rt := Runtime{Store: st, Herdr: f, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you on 401", got.State)
	}
	if !strings.Contains(got.Halt, "key revoked") {
		t.Fatalf("Halt = %q, want the server's message", got.Halt)
	}
}

func TestReconcileRemote404Halts(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "gone"}}}
	f := &fakeHerdr{}
	rt := Runtime{Store: st, Herdr: f, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you on 404", got.State)
	}
	if !strings.Contains(got.Halt, "binding removed by the server admin") {
		t.Fatalf("Halt = %q, want the removed-by-admin message", got.Halt)
	}
}

// TestSyncRemoteCollectsClosedRoundWithoutDelivery checks that SyncRemote
// (the read path `relay status`/`pull`/`wait` share, spec §2.2) collects a
// closed round -- the report entry lands, pending -- without ever
// delivering it to a planner pane.
func TestSyncRemoteCollectsClosedRoundWithoutDelivery(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
	}
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Now: func() time.Time { return baseTime }}

	synced, err := SyncRemote(context.Background(), rt)
	if err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	if synced != 1 {
		t.Fatalf("synced = %d, want 1", synced)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			found = true
			if e.Confirmed {
				t.Fatal("report entry must still be pending: SyncRemote must never deliver it")
			}
		}
	}
	if !found {
		t.Fatal("no report entry after SyncRemote collected the closed round")
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	// Mutation target: have SyncRemote call reconcileRemote instead of
	// observeRemote, and this assertion fails: reconcileRemote's
	// deliverAndSettle reads a nil agents list as "the planner is gone"
	// and orphans the binding.
	if got.State == store.StateOrphaned {
		t.Fatal("SyncRemote must not deliver, so it must never orphan a binding")
	}
}

// TestSyncRemoteSkipsDoneAndLocal checks that SyncRemote never touches the
// network for a binding it should not sync: one already DONE, and one that
// is not a remote binding at all.
func TestSyncRemoteSkipsDoneAndLocal(t *testing.T) {
	st := store.New(t.TempDir())

	doneRemote := remoteBinding("zen")
	doneRemote.Name = "done-remote"
	doneRemote.State = store.StateDone
	if err := st.Save(doneRemote); err != nil {
		t.Fatal(err)
	}

	local := store.Binding{
		Name: "local", CWD: "/fake/repo", State: store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
	}
	if err := st.Save(local); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Now: func() time.Time { return baseTime }}

	synced, err := SyncRemote(context.Background(), rt)
	if err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	if synced != 0 {
		t.Fatalf("synced = %d, want 0: a DONE remote binding and a local binding must both be skipped", synced)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("fakeRemote calls = %v, want none: SyncRemote must not touch the network for a skipped binding", fr.calls)
	}
}

// newRemoteClientRepo creates a client-side repo (what a remote binding's
// Repo points at day to day) with one commit and a "relay/<name>" branch ref
// at that commit -- not checked out, matching the ordinary case where the
// planner's own repo sits on its own branch.
func newRemoteClientRepo(t *testing.T, name string) (dir, headSHA string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "seed.txt")
	runGit(t, dir, "commit", "-m", "seed")
	headSHA = strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "update-ref", "refs/heads/relay/"+name, headSHA)
	return dir, headSHA
}

// newRoundResultBundle clones base (at baseSHA on refs/heads/relay/<name>),
// adds one commit there, and returns a real bundle -- built the same way
// BundleTransport.Snapshot always does -- carrying just that new commit,
// plus its sha.
func newRoundResultBundle(t *testing.T, ctx context.Context, g *git.Client, base, name, baseSHA string) (io.ReadCloser, string) {
	t.Helper()
	resultDir := t.TempDir()
	runGit(t, resultDir, "clone", base, ".")
	runGit(t, resultDir, "checkout", "relay/"+name)
	if err := os.WriteFile(filepath.Join(resultDir, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, resultDir, "add", "result.txt")
	runGit(t, resultDir, "commit", "-m", "round result")
	headSHA := strings.TrimSpace(runGit(t, resultDir, "rev-parse", "HEAD"))

	transport := remote.NewBundleTransport(g, t.TempDir())
	snap, err := transport.Snapshot(ctx, resultDir, []string{"refs/heads/relay/" + name}, baseSHA)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Empty || snap.Body == nil {
		t.Fatal("expected a non-empty bundle")
	}
	return snap.Body, headSHA
}

func TestCatchUpOrderAndIdempotence(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
	}
	rt := Runtime{
		Store:     st,
		Herdr:     &fakeHerdr{},
		Remote:    fr,
		Transport: remote.NewBundleTransport(g, t.TempDir()),
		Now:       func() time.Time { return baseTime },
	}
	agents := []herdr.Agent{plannerAgent()}

	// First tick: the server hands back a bundle that carries an extra ref
	// the client never allowed (view.DirtyCommit == "", so only the branch
	// itself is permitted). A real Absorb genuinely fails on this --
	// ErrUnexpectedRef -- which is what lets this test tell "Ack moved
	// before Absorb" apart from "Ack never runs at all": the RPC round-trip
	// (RoundFile, RoundBundle) succeeds, catchUp actually reaches Absorb, and
	// only Absorb itself rejects the bundle.
	badResultDir := t.TempDir()
	runGit(t, badResultDir, "clone", clientRepo, ".")
	runGit(t, badResultDir, "checkout", "relay/api")
	if err := os.WriteFile(filepath.Join(badResultDir, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, badResultDir, "add", "result.txt")
	runGit(t, badResultDir, "commit", "-m", "round result")
	runGit(t, badResultDir, "branch", "extra", "HEAD")
	badBundleTransport := remote.NewBundleTransport(g, t.TempDir())
	badSnap, err := badBundleTransport.Snapshot(ctx, badResultDir, []string{"refs/heads/relay/api", "refs/heads/extra"}, c1)
	if err != nil {
		t.Fatalf("Snapshot (bad): %v", err)
	}
	if badSnap.Empty || badSnap.Body == nil {
		t.Fatal("expected a non-empty bad bundle")
	}
	fr.roundBundleResp = badSnap.Body

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile (absorb fails): %v", err)
	}
	if got.Builder.LastKnown != "" {
		t.Fatalf("LastKnown = %q after a failed absorb, want empty", got.Builder.LastKnown)
	}
	if got.RemoteAbsorbFailures != 1 {
		t.Fatalf("RemoteAbsorbFailures = %d, want 1", got.RemoteAbsorbFailures)
	}
	if got.Round != 1 {
		t.Fatalf("Round = %d after a failed absorb, want unchanged 1", got.Round)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Ack:") {
			t.Fatalf("Ack called before a successful absorb: %v", fr.calls)
		}
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindReport {
			t.Fatalf("a report entry exists after a failed absorb: %+v", e)
		}
	}

	// Next tick: a clean bundle carrying only the allowed ref, so the absorb
	// succeeds.
	bundle, headSHA := newRoundResultBundle(t, ctx, g, clientRepo, "api", c1)
	fr.roundBundleResp = bundle
	fr.getBindingResp.ResultCommit = headSHA

	got, err = reconcile(t, rt, got, agents)
	if err != nil {
		t.Fatalf("Reconcile (bundle succeeds): %v", err)
	}
	if got.Builder.LastKnown != headSHA {
		t.Fatalf("LastKnown = %q, want %q", got.Builder.LastKnown, headSHA)
	}
	if got.RemoteAbsorbFailures != 0 {
		t.Fatalf("RemoteAbsorbFailures = %d, want 0 after success", got.RemoteAbsorbFailures)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}
	foundAck := false
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Ack:") {
			foundAck = true
		}
	}
	if !foundAck {
		t.Fatalf("Ack never called after a successful absorb: %v", fr.calls)
	}

	entries, err = st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	reportCount := 0
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportCount++
		}
	}
	if reportCount != 1 {
		t.Fatalf("report entries = %d, want exactly 1", reportCount)
	}

	branchHead, ok, err := g.RefSHA(ctx, clientRepo, "refs/heads/relay/api")
	if err != nil || !ok || branchHead != headSHA {
		t.Fatalf("client branch after absorb: got (%q, %v, %v), want (%q, true, nil)", branchHead, ok, err, headSHA)
	}
}

func TestCatchUpDirtyNote(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")

	// Build the round's result: relay/api advances to c2, and a dirty side
	// ref (round-1) sits on top of it, exactly as a real dirty close does.
	resultDir := t.TempDir()
	runGit(t, resultDir, "clone", clientRepo, ".")
	runGit(t, resultDir, "checkout", "relay/api")
	if err := os.WriteFile(filepath.Join(resultDir, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, resultDir, "add", "result.txt")
	runGit(t, resultDir, "commit", "-m", "round result")
	c2 := strings.TrimSpace(runGit(t, resultDir, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(runGit(t, resultDir, "rev-parse", "HEAD^{tree}"))
	c3, err := g.CommitTree(ctx, resultDir, tree, c2, "[relay] api: round 1, uncommitted work")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	sideRef := "refs/relay/api/round-1"
	if err := g.UpdateRef(ctx, resultDir, sideRef, c3, ""); err != nil {
		t.Fatalf("UpdateRef side: %v", err)
	}

	transport := remote.NewBundleTransport(g, t.TempDir())
	snap, err := transport.Snapshot(ctx, resultDir, []string{"refs/heads/relay/api", sideRef}, c1)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Empty || snap.Body == nil {
		t.Fatal("expected a non-empty bundle")
	}

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1, DirtyCommit: c3, ResultCommit: c2},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
		roundBundleResp: snap.Body,
	}
	rt := Runtime{
		Store: st, Herdr: &fakeHerdr{}, Remote: fr, Transport: transport,
		Now: func() time.Time { return baseTime },
	}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2", got.Round)
	}
	if got.Builder.LastKnown != c2 {
		t.Fatalf("LastKnown = %q, want %q", got.Builder.LastKnown, c2)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var reportNote string
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportNote = e.Note
			found = true
		}
	}
	if !found || !strings.Contains(reportNote, "uncommitted work at "+sideRef) {
		t.Fatalf("report note = %q (found=%v), want it to name %s", reportNote, found, sideRef)
	}

	sideSHA, ok, err := g.RefSHA(ctx, clientRepo, sideRef)
	if err != nil || !ok || sideSHA != c3 {
		t.Fatalf("client side ref: got (%q, %v, %v), want (%q, true, nil)", sideSHA, ok, err, c3)
	}
}

// TestCatchUpWritesDiffEntryFromView checks that catchUp writes the
// client's own diff log entry from the server's view facts (Note, Commits,
// Tree, plus the downloaded patch as Path) rather than letting queueReport
// capture its own diff from a local baseline that a remote binding never
// has.
func TestCatchUpWritesDiffEntryFromView(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1,
			DiffNote: "1 file, +1 -0; 1 commit, clean", DiffCommits: 1, DiffTree: "clean",
		},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "diff":
				return io.NopCloser(strings.NewReader("--- a/file\n+++ b/file\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}

	// Mutation target: skip writing the diff entry in catchUp, and
	// queueReport falls back to its own CaptureRoundDiff, which either
	// calls fakeGit.SnapshotTree (raising this count above 0) or, since a
	// remote binding never carries a local RoundBaselineTree, writes a
	// diff entry whose note reads "unavailable: no baseline" instead of
	// the server's own note.
	if fg.snapshotCalls != 0 {
		t.Fatalf("fakeGit.SnapshotTree called %d times, want 0: queueReport must not capture its own diff", fg.snapshotCalls)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var diffEntry store.LogEntry
	found := false
	for _, e := range entries {
		if e.Kind == store.KindDiff {
			diffEntry, found = e, true
		}
	}
	if !found {
		t.Fatal("no diff entry written")
	}
	if diffEntry.Note != "1 file, +1 -0; 1 commit, clean" {
		t.Fatalf("diff entry Note = %q, want the server's own note", diffEntry.Note)
	}
	if diffEntry.Commits != 1 {
		t.Fatalf("diff entry Commits = %d, want 1", diffEntry.Commits)
	}
	if diffEntry.Tree != "clean" {
		t.Fatalf("diff entry Tree = %q, want clean", diffEntry.Tree)
	}
	if diffEntry.Path != st.DiffPath("api", 1) {
		t.Fatalf("diff entry Path = %q, want the downloaded patch's path %q", diffEntry.Path, st.DiffPath("api", 1))
	}
	if !diffEntry.Confirmed {
		t.Fatal("diff entry must be Confirmed, like every other relay-bookkeeping entry")
	}

	var reportEntry store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportEntry = e
		}
	}
	if !strings.Contains(reportEntry.Payload, "Diff:") {
		t.Fatalf("report payload = %q, want a Diff: line from DiffLineFromNote", reportEntry.Payload)
	}
}

func TestCatchUpBranchCheckedOutRetries(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")
	// Unlike the ordinary case, check the binding's own branch out as this
	// repo's current branch, which is what makes the fetch below collide.
	runGit(t, clientRepo, "checkout", "relay/api")

	bundle, _ := newRoundResultBundle(t, ctx, g, clientRepo, "api", c1)

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
		roundBundleResp: bundle,
	}
	rt := Runtime{
		Store: st, Herdr: &fakeHerdr{}, Remote: fr,
		Transport: remote.NewBundleTransport(g, t.TempDir()),
		Now:       func() time.Time { return baseTime },
	}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("Round = %d, want unchanged 1 (retried, not halted)", got.Round)
	}
	if got.Builder.LastKnown != "" {
		t.Fatalf("LastKnown = %q, want empty: the absorb never happened", got.Builder.LastKnown)
	}
	if got.RemoteAbsorbFailures != 0 {
		t.Fatalf("RemoteAbsorbFailures = %d, want 0: a checked-out branch is retried, not counted as a failure", got.RemoteAbsorbFailures)
	}
	if got.State == store.StateNeedsYou {
		t.Fatal("a checked-out branch must retry quietly, not halt")
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Ack:") {
			t.Fatalf("Ack called despite the absorb never completing: %v", fr.calls)
		}
	}
}

func TestCatchUpAbsorbFailuresHaltAtTen(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
		roundBundleFunc: func(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("not a real bundle")), nil
		},
	}
	ft := &fakeTransport{absorbErr: errors.New("disk full")}
	f := &fakeHerdr{}
	rt := Runtime{Store: st, Herdr: f, Remote: fr, Transport: ft, Now: func() time.Time { return baseTime }}
	agents := []herdr.Agent{plannerAgent()}

	got := b
	var err error
	for i := 1; i <= 10; i++ {
		got, err = reconcile(t, rt, got, agents)
		if err != nil {
			t.Fatalf("Reconcile iteration %d: %v", i, err)
		}
		if i < 10 {
			if got.State == store.StateNeedsYou {
				t.Fatalf("halted after only %d absorb failures", i)
			}
			if got.RemoteAbsorbFailures != i {
				t.Fatalf("iteration %d: RemoteAbsorbFailures = %d, want %d", i, got.RemoteAbsorbFailures, i)
			}
		}
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you at 10 absorb failures", got.State)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "cannot absorb round") {
		t.Fatalf("notices = %v, want one naming the absorb failure", f.notices)
	}
}

func TestDoneRemoteForwardsFirst(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{doneErr: &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeRoundOpen, Message: "round is open"}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	_, err := Done(ctx, rt, "api")
	if err == nil || !strings.Contains(err.Error(), "round 1 is running on zen; wait or relay unbind --force") {
		t.Fatalf("Done err = %v, want the round-open refusal", err)
	}

	reloaded, lerr := st.Load("api")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if reloaded.State != store.StateActive {
		t.Fatalf("state = %s, want unchanged active: a refused Done must not mark the binding done", reloaded.State)
	}
}

func TestUnbindRemote404Proceeds(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{unbindErr: &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "gone"}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := Unbind(ctx, rt, "api", false); err != nil {
		t.Fatalf("Unbind: %v, want a 404 to proceed locally", err)
	}

	if _, err := st.Load("api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Load after unbind: err = %v, want ErrNotFound", err)
	}
	foundUnbind := false
	for _, c := range fr.calls {
		if c == "Unbind:zen:api" {
			foundUnbind = true
		}
	}
	if !foundUnbind {
		t.Fatalf("server Unbind never called: %v", fr.calls)
	}
}

func TestResumeRemoteRefusesRebind(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := Runtime{Store: st, Herdr: f, Now: func() time.Time { return baseTime }}

	_, _, err := BindResolved(ctx, rt, BindOptions{
		Name: "api", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/fake/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot change a remote builder; unbind and add") {
		t.Fatalf("BindResolved err = %v, want the remote-rebind refusal", err)
	}
}

func TestGCRemoteOnlyWhenDone(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())

	doneB := remoteBinding("zen")
	doneB.Name = "done-remote"
	doneB.CWD = "/fake/done-remote"
	doneB.State = store.StateDone
	if err := st.Save(doneB); err != nil {
		t.Fatal(err)
	}

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	results, err := GC(ctx, rt, GCOptions{})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(results) != 1 || results[0].Name != "done-remote" {
		t.Fatalf("results = %+v, want only done-remote", results)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("GC made a server call: %v, want none", fr.calls)
	}
	if _, lerr := st.Load("open-remote"); lerr != nil {
		t.Fatalf("open-remote must survive GC: %v", lerr)
	}
}

func TestForwardUnavailable(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-remote", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan}); err != nil {
		t.Fatal(err)
	}

	idleB := remoteBinding("zen")
	idleB.Name = "idle-remote"
	idleB.CWD = "/fake/idle-remote"
	if err := st.Save(idleB); err != nil {
		t.Fatal(err)
	}
	// No plan entry for idle-remote's round: its round is not open.

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(ctx, rt, "some-token", "hit a limit")
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none: the fake never fails", lines)
	}

	unavailableCalls := 0
	var target string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailableCalls++
			target = c
		}
	}
	if unavailableCalls != 1 {
		t.Fatalf("Unavailable calls = %d, want exactly 1: %v", unavailableCalls, fr.calls)
	}
	if !strings.Contains(target, "open-remote") {
		t.Fatalf("the one call = %q, want it naming open-remote", target)
	}
}

func TestAnswerAskForkRefuseRemote(t *testing.T) {
	t.Run("Answer", func(t *testing.T) {
		st := store.New(t.TempDir())
		b := remoteBinding("zen")
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
		rt := Runtime{Store: st, Now: func() time.Time { return baseTime }}

		err := Answer(context.Background(), rt, "api", AnswerInput{Keys: "y"})
		if err == nil || !strings.Contains(err.Error(), "remote builders take no dialogs") {
			t.Fatalf("Answer err = %v, want the remote-dialog refusal", err)
		}
	})

	t.Run("Ask", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, _ := seedForAsk(t, f)
		b := remoteBinding("zen")
		b.Name = "remote-ask"
		b.CWD = "/other-tree-remote"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		q := writeQuestion(t, "Review something.")

		_, err := Ask(context.Background(), rt, AskOptions{
			Role: "reviewer", File: q, Name: "remote-ask", PlannerPane: "w2:p3",
		})
		if err == nil || !strings.Contains(err.Error(), "consults are local-only") {
			t.Fatalf("Ask err = %v, want the consults-are-local-only refusal", err)
		}
	})

	t.Run("Fork", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
		rt := newForkRuntime(t, f, nil, nil)
		b := remoteBinding("zen")
		b.CWD = "/fake/fork-src"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		_, err := Fork(context.Background(), rt, ForkOptions{
			Source: "api", NewName: "api2", Round: 1, PlannerPane: "w2:p3", CWD: "/fake/fork-dst",
		})
		if err == nil || !strings.Contains(err.Error(), "fork across servers is not supported") {
			t.Fatalf("Fork err = %v, want the cross-server refusal", err)
		}
	})
}

func TestServerInUse(t *testing.T) {
	bindings := []store.Binding{
		remoteBinding("zen"),
		func() store.Binding { b := remoteBinding("mars"); b.Name = "other"; return b }(),
		{Name: "local", Builder: store.Endpoint{Mode: store.ModePane}},
	}

	if got := ServerInUse(bindings, "zen"); len(got) != 1 || got[0] != "api" {
		t.Fatalf("ServerInUse(zen) = %v, want [api]", got)
	}
	if got := ServerInUse(bindings, "mars"); len(got) != 1 || got[0] != "other" {
		t.Fatalf("ServerInUse(mars) = %v, want [other]", got)
	}
	if got := ServerInUse(bindings, "pluto"); len(got) != 0 {
		t.Fatalf("ServerInUse(pluto) = %v, want none", got)
	}
}
