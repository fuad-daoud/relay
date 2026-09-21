package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

type fakeRemote struct {
	calls []string

	whoAmIResp        remote.WhoAmI
	whoAmIErr         error
	candidatesResp    remote.CandidatesResponse
	candidatesErr     error
	createBindingResp remote.BindingView
	createBindingErr  error
	createBindingReq  remote.CreateBindingRequest
	getBindingResp    remote.BindingView
	getBindingErr     error
	startRoundResp    remote.BindingView
	startRoundErr     error
	startRoundTier    string
	startRoundTags    []remote.TagRef
	roundFileResp     io.ReadCloser
	roundFileErr      error
	roundBundleResp   io.ReadCloser
	roundBundleErr    error
	roundFileFunc     func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error)
	roundBundleFunc   func(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error)
	ackResp           remote.BindingView
	ackErr            error
	unavailableErr    error
	availableResp     remote.AvailableResponse
	availableErr      error
	doneErr           error
	unbindErr         error
	resumeResp        remote.BindingView
	resumeErr         error

	onCreateBinding func()
	onUnbind        func()
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
	f.createBindingReq = req
	if f.onCreateBinding != nil {
		f.onCreateBinding()
	}
	return f.createBindingResp, f.createBindingErr
}

func (f *fakeRemote) GetBinding(ctx context.Context, server, name string) (remote.BindingView, error) {
	f.calls = append(f.calls, "GetBinding:"+server+":"+name)
	return f.getBindingResp, f.getBindingErr
}

func (f *fakeRemote) StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier string, tags []remote.TagRef) (remote.BindingView, error) {
	f.calls = append(f.calls, fmt.Sprintf("StartRound:%s:%s:%d", server, name, round))
	f.startRoundTier = tier
	f.startRoundTags = tags
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

func (f *fakeRemote) Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error) {
	f.calls = append(f.calls, fmt.Sprintf("Available:%s:%s", server, subject))
	return f.availableResp, f.availableErr
}

func (f *fakeRemote) Done(ctx context.Context, server, name string) error {
	f.calls = append(f.calls, fmt.Sprintf("Done:%s:%s", server, name))
	return f.doneErr
}

func (f *fakeRemote) Unbind(ctx context.Context, server, name string) error {
	f.calls = append(f.calls, fmt.Sprintf("Unbind:%s:%s", server, name))
	if f.onUnbind != nil {
		f.onUnbind()
	}
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

// recordingTransport drives a real transport -- bundles and all -- and keeps
// what the last Absorb did, so a test can show why an absorb was rejected
// (#274) instead of inferring it from the refs that did not move.
type recordingTransport struct {
	inner remote.TreeTransport
	moved map[string]string
	err   error
}

func (r *recordingTransport) Snapshot(ctx context.Context, repo string, refs []string, since string) (remote.Snapshot, error) {
	return r.inner.Snapshot(ctx, repo, refs, since)
}

func (r *recordingTransport) Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error) {
	moved, err := r.inner.Absorb(ctx, repo, contentType, body, refs)
	r.moved, r.err = moved, err
	return moved, err
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

// TestProbeServersFillsTierFields pins that ProbeServers copies
// WhoAmI.Features/BuilderTier/MaxTier into the enrolled probe, and leaves
// them zero for a pre-tier (non-aware) server (#141 remote half).
func TestProbeServersFillsTierFields(t *testing.T) {
	servers := map[string]client.ServerEntry{
		"zen":   {URL: "https://zen:7777"},
		"other": {URL: "https://other:7777"},
	}
	rt := Runtime{Remote: &fakeRemote{
		whoAmIResp: remote.WhoAmI{
			Label:       "laptop",
			Features:    []string{remote.FeatureTier},
			BuilderTier: "harness",
			MaxTier:     "edit",
		},
	}}

	probes := ProbeServers(context.Background(), rt, servers, "")
	if len(probes) != 2 {
		t.Fatalf("got %d probes, want 2", len(probes))
	}
	for _, p := range probes {
		if !p.TierAware {
			t.Fatalf("probe %s: TierAware = false, want true", p.Name)
		}
		if p.BuilderTier != "harness" {
			t.Fatalf("probe %s: BuilderTier = %q, want harness", p.Name, p.BuilderTier)
		}
		if p.MaxTier != "edit" {
			t.Fatalf("probe %s: MaxTier = %q, want edit", p.Name, p.MaxTier)
		}
	}
}

// TestProbeServersPreTierLeavesTierFieldsZero pins the pre-tier (v1) server
// case: WhoAmI carries no Features, so the probe is not TierAware and its
// tier fields stay empty (#141 remote half).
func TestProbeServersPreTierLeavesTierFieldsZero(t *testing.T) {
	servers := map[string]client.ServerEntry{"zen": {URL: "https://zen:7777"}}
	rt := Runtime{Remote: &fakeRemote{
		whoAmIResp: remote.WhoAmI{Label: "laptop"}, // no Features: pre-tier server
	}}

	probes := ProbeServers(context.Background(), rt, servers, "")
	if len(probes) != 1 {
		t.Fatalf("got %d probes, want 1", len(probes))
	}
	p := probes[0]
	if p.TierAware {
		t.Fatalf("TierAware = true, want false")
	}
	if p.BuilderTier != "" || p.MaxTier != "" {
		t.Fatalf("BuilderTier/MaxTier = %q/%q, want empty", p.BuilderTier, p.MaxTier)
	}
}

// TestServerTierWarning pins the one condition that warns: an enrolled,
// tier-aware server whose builder tier is harness. Every other combination
// -- not aware, or aware at a tier above harness -- is silent (#141 remote
// half).
func TestServerTierWarning(t *testing.T) {
	cases := []struct {
		name string
		p    ServerProbe
		want bool
	}{
		{"tier aware at harness", ServerProbe{TierAware: true, BuilderTier: "harness"}, true},
		{"tier aware at edit", ServerProbe{TierAware: true, BuilderTier: "edit"}, false},
		{"not tier aware", ServerProbe{TierAware: false, BuilderTier: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ServerTierWarning(tc.p) != ""
			if got != tc.want {
				t.Fatalf("ServerTierWarning(%+v) non-empty = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

// TestRenderServersShowsTierAndWarning pins the two lines RenderServers adds
// per probe: the builder-tier annotation on the row, and a "!!" warning line
// underneath when ServerTierWarning fires (#141 remote half).
func TestRenderServersShowsTierAndWarning(t *testing.T) {
	probes := []ServerProbe{
		{Name: "zen", URL: "https://zen:7777", State: "enrolled", Label: "laptop", TierAware: true, BuilderTier: "harness", MaxTier: "edit"},
		{Name: "old", URL: "https://old:7777", State: "enrolled", Label: "laptop", TierAware: false},
	}
	out := RenderServers(probes)
	if !strings.Contains(out, "builder tier: harness (max edit)") {
		t.Fatalf("output missing builder tier line; got:\n%s", out)
	}
	if !strings.Contains(out, "!! headless builders at tier harness") {
		t.Fatalf("output missing !! warning line; got:\n%s", out)
	}
	if !strings.Contains(out, "builder tier: unknown (pre-tier server)") {
		t.Fatalf("output missing unknown pre-tier line; got:\n%s", out)
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

// TestAddRemoteTierPreTierServerRefused pins ErrServerPreTier: a server that
// does not advertise FeatureTier refuses --tier before any binding is
// created there (#141 remote half).
func TestAddRemoteTierPreTierServerRefused(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{}, // Features nil: pre-tier server
		createBindingResp: remote.BindingView{
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now}

	_, err := Add(ctx, rt, AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
		Tier:   "edit",
	})
	if !errors.Is(err, ErrServerPreTier) {
		t.Fatalf("Add err = %v, want ErrServerPreTier", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateBinding") {
			t.Fatalf("calls = %v, want no CreateBinding", fr.calls)
		}
	}
}

// TestAddRemoteTierWiresRequestAndEchoesBinding pins the wire contract: a
// tier-aware server gets Tier on the CreateBindingRequest, and the local
// binding records the tier the server echoed back, not the request's own
// (#141 remote half).
func TestAddRemoteTierWiresRequestAndEchoesBinding(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureTier}},
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
			Tier:      "edit",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now}

	res, err := Add(ctx, rt, AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
		Tier:   "edit",
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if fr.createBindingReq.Tier != "edit" {
		t.Fatalf("CreateBindingRequest.Tier = %q, want edit", fr.createBindingReq.Tier)
	}
	if res.Binding.Tier != "edit" {
		t.Fatalf("res.Binding.Tier = %q, want edit", res.Binding.Tier)
	}
	stored, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Tier != "edit" {
		t.Fatalf("stored.Tier = %q, want edit", stored.Tier)
	}
}

// TestAddRemoteNoTierSkipsProbe pins the no-op path: omitting --tier never
// probes WhoAmI and sends no Tier on the wire, so a pre-tier server is
// unaffected by a plain `relay add --server` (#141 remote half).
func TestAddRemoteNoTierSkipsProbe(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now}

	if _, err := Add(ctx, rt, AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "WhoAmI") {
			t.Fatalf("calls = %v, want no WhoAmI", fr.calls)
		}
	}
	if fr.createBindingReq.Tier != "" {
		t.Fatalf("CreateBindingRequest.Tier = %q, want empty", fr.createBindingReq.Tier)
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

// TestAddRemoteExistingBranch pins `add --server --branch`: the client adopts
// a branch it did not create, sends the branch tip as BaseCommit, creates no
// branch, and a failure after the server agreed never deletes the branch.
func TestAddRemoteExistingBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("adopts the branch and sends its tip as base", func(t *testing.T) {
		st := store.New(t.TempDir())
		fg := &fakeGit{
			branchExists:  true,
			refSHA:        map[string]string{"refs/heads/feature/x": "tip"},
			rootCommitSHA: "2222222222222222222222222222222222222222",
		}
		fr := &fakeRemote{createBindingResp: remote.BindingView{Name: "x"}}
		rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now}

		got, err := Add(ctx, rt, AddOptions{
			Name: "x", Branch: "feature/x", Server: "zen", Repo: "/fake/repo",
		})
		if err != nil {
			t.Fatalf("Add --server --branch: %v", err)
		}
		if fr.createBindingReq.BaseCommit != "tip" {
			t.Errorf("BaseCommit = %q, want the branch tip tip", fr.createBindingReq.BaseCommit)
		}
		if len(fg.createBranchCalls) != 0 {
			t.Errorf("an adopted branch must not be created: %+v", fg.createBranchCalls)
		}
		if len(fg.createTrackingBranchCalls) != 0 {
			t.Errorf("a local branch needs no tracking branch: %+v", fg.createTrackingBranchCalls)
		}
		if !got.Binding.ExistingBranch {
			t.Error("ExistingBranch must record that relay did not create the branch")
		}
		if got.Binding.Branch != "feature/x" {
			t.Errorf("Branch = %q, want feature/x", got.Binding.Branch)
		}
	})

	t.Run("a failure after CreateBinding unbinds the server and deletes no branch", func(t *testing.T) {
		root := t.TempDir()
		st := store.New(root)
		fg := &fakeGit{
			branchExists:  true,
			refSHA:        map[string]string{"refs/heads/feature/x": "tip"},
			rootCommitSHA: "2222222222222222222222222222222222222222",
		}
		fr := &fakeRemote{createBindingResp: remote.BindingView{Name: "x"}}
		rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now}

		// Same provocation as TestAddRemoteCleansUpLocalBranchOnSaveFailure:
		// the lock file exists, then the state root goes read-only, so the
		// save after CreateBinding fails.
		if err := st.WithLock(func(tx *store.Tx) error { return nil }); err != nil {
			t.Fatalf("seed lock file: %v", err)
		}
		if err := os.Chmod(root, 0o500); err != nil {
			t.Fatalf("chmod root: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

		_, err := Add(ctx, rt, AddOptions{
			Name: "x", Branch: "feature/x", Server: "zen", Repo: "/fake/repo",
		})
		if err == nil {
			t.Fatal("Add expected error, got nil")
		}
		if !slices.Contains(fr.calls, "Unbind:zen:x") {
			t.Fatalf("Unbind was not called on server, calls = %v", fr.calls)
		}
		if len(fg.deleteBranchCalls) != 0 {
			t.Errorf("relay must not delete a branch it did not create: %+v", fg.deleteBranchCalls)
		}
	})
}

// TestAddRemoteCleansUpLocalBranchOnSaveFailure pins #100 round 4: a failure
// that happens after CreateBranch has already succeeded also removes the
// local branch relay just cut, not only the server binding -- otherwise the
// branch is left behind and the next `add` with the same name is refused
// with "branch relay/<name> exists; delete it or pick another name", even
// though relay itself created it. The provocation makes Save fail (a real
// store, not fakeGit, since fakeGit.CreateBranch must succeed here): the
// binding's own state directory is pre-occupied by a plain file, so the
// store's os.MkdirAll refuses it -- exercising the same "some failure after
// CreateBinding succeeds" path TestAddRemoteCleansUpServerOnSaveFailure pins
// for CreateBranch, but one step later, after the branch already exists.
// Mutation target: drop the DeleteBranch call in addRemote's deferred
// cleanup and this fails, since DeleteBranch is then never called.
func TestAddRemoteCleansUpLocalBranchOnSaveFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := store.New(root)

	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name: "api",
		},
	}

	var order []string
	fr.onUnbind = func() { order = append(order, "unbind") }
	fg.deleteBranchFunc = func(ctx context.Context, dir, branch string) error {
		order = append(order, "delete-branch")
		return nil
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

	// Make the store's own root read-only, but only after its lock file
	// already exists: addRemote's first step (rt.Store.Load) must still see
	// a plain "no such file" for a binding that has never been saved, not a
	// permission error, or this would fail before CreateBinding and
	// CreateBranch ever ran. tx.Save's later os.MkdirAll(root/api, ...) does
	// need to create a new directory entry, though, so it fails.
	if err := st.WithLock(func(tx *store.Tx) error { return nil }); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	_, err := Add(ctx, rt, opts)
	if err == nil {
		t.Fatal("Add expected error, got nil")
	}

	if !slices.Contains(fr.calls, "Unbind:zen:api") {
		t.Fatalf("Unbind was not called on server, calls = %v", fr.calls)
	}
	if len(fg.deleteBranchCalls) != 1 {
		t.Fatalf("DeleteBranch calls = %v, want exactly one", fg.deleteBranchCalls)
	}
	want := deleteBranchCall{Dir: "/fake/repo", Branch: "relay/api"}
	if fg.deleteBranchCalls[0] != want {
		t.Fatalf("DeleteBranch call = %+v, want %+v", fg.deleteBranchCalls[0], want)
	}
	if len(order) != 2 || order[0] != "unbind" || order[1] != "delete-branch" {
		t.Fatalf("expected Unbind before DeleteBranch, got order %v", order)
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

	_, err := Send(ctx, rt, "api", planFile, SendOptions{})
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

	res, err := Send(ctx, rt, "api", planFile, SendOptions{})
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

// TestSendRemoteHaltedIsAnError pins #250 items 1 and 3 client-side: a
// StartRound response that says the round could not start -- whether an old
// server's 201 with a needs_you view, or a new server's 409 round_halted --
// is reported as an error, and nothing is written locally: a resend must
// not look like it succeeded.
func TestSendRemoteHaltedIsAnError(t *testing.T) {
	newRT := func(t *testing.T, fr *fakeRemote) (Runtime, *store.Store) {
		t.Helper()
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
				LastShipped: "0000000000000000000000000000000000000000",
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
		ft := &fakeTransport{
			snapshotResp: remote.Snapshot{
				Heads: map[string]string{"refs/relay/api/out": "1111111111111111111111111111111111111111"},
			},
		}
		return Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now}, st
	}

	assertNothingWritten := func(t *testing.T, st *store.Store, err error, wantSubstrs ...string) {
		t.Helper()
		if err == nil {
			t.Fatal("Send succeeded, want an error")
		}
		for _, want := range wantSubstrs {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Send error = %q, want it to contain %q", err.Error(), want)
			}
		}
		if _, statErr := os.Stat(st.PlanPath("api", 1)); !os.IsNotExist(statErr) {
			t.Errorf("PlanPath exists after failure: stat err = %v", statErr)
		}
		entries, _ := st.ReadLog("api")
		if len(entries) != 0 {
			t.Errorf("entries = %d, want 0", len(entries))
		}
		reloaded, loadErr := st.Load("api")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if reloaded.State != store.StateActive {
			t.Errorf("State = %s, want unchanged active", reloaded.State)
		}
		if reloaded.Builder.LastShipped != "0000000000000000000000000000000000000000" {
			t.Errorf("LastShipped = %q, want unchanged", reloaded.Builder.LastShipped)
		}
	}

	t.Run("201 needs_you view (old server)", func(t *testing.T) {
		fr := &fakeRemote{
			startRoundResp: remote.BindingView{
				RoundState: remote.RoundNeedsYou,
				Halt:       "builder spawn failed: boom",
			},
		}
		rt, st := newRT(t, fr)

		planFile := filepath.Join(t.TempDir(), "plan.md")
		if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
			t.Fatal(err)
		}

		_, err := Send(context.Background(), rt, "api", planFile, SendOptions{})
		assertNothingWritten(t, st, err, "could not start", "boom")
	})

	t.Run("409 round_halted (new server)", func(t *testing.T) {
		fr := &fakeRemote{
			startRoundErr: &client.HTTPError{
				Status: 409,
				Body: remote.ErrorBody{
					Code:    remote.CodeRoundHalted,
					Message: "already switched 2 time(s) this round (max_switches 2)",
				},
			},
		}
		rt, st := newRT(t, fr)

		planFile := filepath.Join(t.TempDir(), "plan.md")
		if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
			t.Fatal(err)
		}

		_, err := Send(context.Background(), rt, "api", planFile, SendOptions{})
		assertNothingWritten(t, st, err, "could not start", "already switched 2 time(s)")
	})
}

// TestSendRemoteTierPassedToStartRound pins that a Send with a Tier option
// reaches StartRound over the wire, after the pre-tier probe succeeds
// (#141 remote half).
func TestSendRemoteTierPassedToStartRound(t *testing.T) {
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
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureTier}},
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

	if _, err := Send(ctx, rt, "api", planFile, SendOptions{Tier: "edit"}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if fr.startRoundTier != "edit" {
		t.Fatalf("startRoundTier = %q, want edit", fr.startRoundTier)
	}
}

// TestSendRemoteTierAboveMaxWraps pins that a 422 tier_above_max from
// StartRound maps back to ErrTierAboveMax client-side, matching the local
// refusal's wording (#141 remote half).
func TestSendRemoteTierAboveMaxWraps(t *testing.T) {
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
			Status: 422,
			Body: remote.ErrorBody{
				Code:    remote.CodeTierAboveMax,
				Message: "tier yolo exceeds this server's max_tier edit",
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

	_, err := Send(ctx, rt, "api", planFile, SendOptions{})
	if !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("Send err = %v, want ErrTierAboveMax", err)
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

	_, err := Send(ctx, rt, "api", planFile, SendOptions{})
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

func TestSendRemoteShipsTags(t *testing.T) {
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
			LastShipped: "",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relay/api": "1111111111111111111111111111111111111111",
		},
		tags: map[string]string{
			"v1": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"v0": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relay/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Send(ctx, rt, "api", planFile, SendOptions{}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	want := []remote.TagRef{
		{Name: "v0", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Name: "v1", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if !slices.Equal(fr.startRoundTags, want) {
		t.Fatalf("StartRound tags = %+v, want %+v (sorted by name)", fr.startRoundTags, want)
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

	_, err := Send(ctx, rt, "api", planFile, SendOptions{})
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

// TestObserveRemoteCopiesStalledSince pins #252's remote half: a running
// round's stall stamp rides from the server view onto the client binding,
// status shows it, and a later view without one clears it.
func TestObserveRemoteCopiesStalledSince(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	stalled := baseTime.Add(-20 * time.Minute)
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundRunning, StalledSince: stalled},
		roundFileResp:  io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !got.StalledSince.Equal(stalled) {
		t.Fatalf("StalledSince = %s, want the view's %s", got.StalledSince, stalled)
	}

	// Status reads the stamp and labels the remote row stalled.
	if err := st.Save(got); err != nil {
		t.Fatal(err)
	}
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(rep.Bindings))
	}
	if status := rep.Bindings[0].BuilderStatus; !strings.HasPrefix(status, "stalled ") {
		t.Fatalf("BuilderStatus = %q, want it to start with %q", status, "stalled ")
	} else if !strings.Contains(status, AgeText(baseTime.Sub(stalled))) {
		t.Errorf("BuilderStatus = %q, want it to contain the age %q", status, AgeText(baseTime.Sub(stalled)))
	}

	// A later view with a zero stamp clears it.
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundRunning}
	got2, err := reconcile(t, rt, got, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile (cleared): %v", err)
	}
	if !got2.StalledSince.IsZero() {
		t.Fatalf("StalledSince = %s, want zero once the view has none", got2.StalledSince)
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

// newRoundResultBundle clones base (sitting at baseSHA on ref), adds one
// commit there, and returns a real bundle -- built the same way
// BundleTransport.Snapshot always does -- carrying just that new commit,
// plus its sha. ref is the full ref the server ships, which for an
// adopted-branch binding is refs/heads/relay/<name> even though the client's
// own branch keeps the adopted name (#274).
func newRoundResultBundle(t *testing.T, ctx context.Context, g *git.Client, base, ref, baseSHA string) (io.ReadCloser, string) {
	t.Helper()
	resultDir := t.TempDir()
	runGit(t, resultDir, "clone", base, ".")
	runGit(t, resultDir, "checkout", strings.TrimPrefix(ref, "refs/heads/"))
	if err := os.WriteFile(filepath.Join(resultDir, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, resultDir, "add", "result.txt")
	runGit(t, resultDir, "commit", "-m", "round result")
	headSHA := strings.TrimSpace(runGit(t, resultDir, "rev-parse", "HEAD"))

	transport := remote.NewBundleTransport(g, t.TempDir())
	snap, err := transport.Snapshot(ctx, resultDir, []string{ref}, baseSHA)
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
	bundle, headSHA := newRoundResultBundle(t, ctx, g, clientRepo, "refs/heads/relay/api", c1)
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

// TestCatchUpAdoptedBranchAbsorbsServerRef pins the adopted-branch fix
// (#274): `add --server --branch feature/x` adopts a branch relay did not
// create, but the server still cuts its own branch and the round bundle it
// ships always names refs/heads/relay/<name> (handleRoundBundle snapshots
// refs/heads/<server branch>, and the server's branch is relay/<name>).
// Catch-up must allow the server's ref through Absorb, then fast-forward the
// adopted branch to it, so the planner's own branch is the one carrying the
// round's result.
func TestCatchUpAdoptedBranchAbsorbsServerRef(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)

	// The client repo: an adopted branch feature/x at base. relay creates no
	// local relay/<name> branch when it adopts feature/x, so the round bundle
	// is cut here from refs/heads/relay/api -- the server's own branch name,
	// this repo standing in for the server repo the bundle really comes from.
	// Catch-up absorbs that ref (moving it here) and then fast-forwards the
	// adopted feature/x to it.
	clientRepo := t.TempDir()
	runGit(t, clientRepo, "init")
	if err := os.WriteFile(filepath.Join(clientRepo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clientRepo, "add", "seed.txt")
	runGit(t, clientRepo, "commit", "-m", "seed")
	c1 := strings.TrimSpace(runGit(t, clientRepo, "rev-parse", "HEAD"))
	runGit(t, clientRepo, "update-ref", "refs/heads/feature/x", c1)
	runGit(t, clientRepo, "update-ref", "refs/heads/relay/api", c1)

	bundle, c2 := newRoundResultBundle(t, ctx, g, clientRepo, "refs/heads/relay/api", c1)

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Branch = "feature/x"
	b.ExistingBranch = true
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1, ResultCommit: c2,
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
		roundBundleResp: bundle,
	}
	trans := &recordingTransport{inner: remote.NewBundleTransport(g, t.TempDir())}
	rt := Runtime{
		Store: st, Herdr: &fakeHerdr{}, Remote: fr, Transport: trans, Git: g,
		Now: func() time.Time { return baseTime },
	}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Mutation target: allow refs/heads/<b.Branch> instead of the server's
	// ref and this reads "unexpected ref: refs/heads/relay/api" -- the
	// bundle is rejected, nothing is absorbed, and no ref moves below.
	if trans.err != nil {
		t.Fatalf("Absorb rejected the server's bundle: %v", trans.err)
	}
	if moved := trans.moved["refs/heads/relay/api"]; moved != c2 {
		t.Fatalf("Absorb moved refs/heads/relay/api to %q, want the bundle tip %q", moved, c2)
	}
	if got.State == store.StateNeedsYou {
		t.Fatal("the binding needs you after catch-up, want the round absorbed")
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}
	if got.Builder.LastKnown != c2 {
		t.Fatalf("LastKnown = %q, want the bundle tip %q", got.Builder.LastKnown, c2)
	}
	if got.RemoteAbsorbFailures != 0 {
		t.Fatalf("RemoteAbsorbFailures = %d, want 0: the server's ref must absorb, not fail", got.RemoteAbsorbFailures)
	}

	adoptedSHA, ok, err := g.RefSHA(ctx, clientRepo, "refs/heads/feature/x")
	if err != nil || !ok || adoptedSHA != c2 {
		t.Fatalf("refs/heads/feature/x: got (%q, %v, %v), want (%q, true, nil): the adopted branch must be fast-forwarded to the round's result", adoptedSHA, ok, err, c2)
	}
	serverSHA, ok, err := g.RefSHA(ctx, clientRepo, "refs/heads/relay/api")
	if err != nil || !ok || serverSHA != c2 {
		t.Fatalf("refs/heads/relay/api: got (%q, %v, %v), want (%q, true, nil): the server's own ref stays beside the adopted branch", serverSHA, ok, err, c2)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	foundReport := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			foundReport = true
		}
	}
	if !foundReport {
		t.Fatal("no report entry queued after catch-up")
	}
}

func TestCatchUpFetchesStream(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	streamBody := "{\"type\":\"assistant\"}\n{\"type\":\"error\",\"message\":\"boom\"}\n"
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "stream":
				return io.NopCloser(strings.NewReader(streamBody)), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := os.ReadFile(st.BuilderStreamPath("api", 1))
	if err != nil {
		t.Fatalf("read stream file: %v", err)
	}
	if string(got) != streamBody {
		t.Fatalf("stream file = %q, want %q", string(got), streamBody)
	}
	found := false
	for _, c := range fr.calls {
		if c == "RoundFile:zen:api:1:stream" {
			found = true
		}
	}
	if !found {
		t.Fatalf("catch-up never fetched the stream file: %v", fr.calls)
	}
}

func TestCatchUpStreamMissingIsFine(t *testing.T) {
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
	fg := &fakeGit{}
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("a missing stream file must not halt: %+v", got)
	}
	if _, err := os.Stat(st.BuilderStreamPath("api", 1)); !os.IsNotExist(err) {
		t.Fatalf("stream file exists (stat err = %v), want none", err)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	hasReport := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			hasReport = true
		}
	}
	if !hasReport {
		t.Fatal("no report entry: catch-up must still complete")
	}
}

// TestCatchUpKeepsServerUsage checks that catch-up keeps the usage the
// server measured and shipped with the round (#216): the client's report
// entry stores it verbatim instead of re-measuring a round whose record
// lives on the server.
func TestCatchUpKeepsServerUsage(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	sent := usage.Usage{
		Harness: "opencode",
		Model:   "haiku",
		Tokens:  usage.Tokens{In: 1000, Out: 200},
		Cost:    usage.Cost{USD: 0.12, Basis: usage.Measured},
	}
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1,
			DiffNote: "1 file, +1 -0; 1 commit, clean", DiffCommits: 1, DiffTree: "clean",
			Usage: &sent,
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

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var reportEntry store.LogEntry
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportEntry, found = e, true
		}
	}
	if !found {
		t.Fatal("no report entry written")
	}
	if reportEntry.Usage == nil {
		t.Fatal("report entry Usage = nil, want the server's figure")
	}
	if *reportEntry.Usage != sent {
		t.Fatalf("report entry Usage = %+v, want the server's figure %+v", *reportEntry.Usage, sent)
	}
	// The server's round is not re-measured here: the client's own record
	// of a shared-cwd read would be wrong, and its note must not say
	// otherwise.
	if strings.Contains(reportEntry.Usage.Note, "shared cwd") {
		t.Fatalf("Usage.Note = %q, must not mention a shared cwd", reportEntry.Usage.Note)
	}
}

// TestCatchUpPreUsageServerNotes checks the honest answer for a server
// built before this (#216): with no usage in the view, the client's report
// entry says the server sent none -- basis unknown -- rather than reading
// a record the client does not have.
func TestCatchUpPreUsageServerNotes(t *testing.T) {
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
	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Remote: fr, Git: &fakeGit{}, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var reportEntry store.LogEntry
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportEntry, found = e, true
		}
	}
	if !found {
		t.Fatal("no report entry written")
	}
	if reportEntry.Usage == nil {
		t.Fatal("report entry Usage = nil, want an honest unknown")
	}
	if reportEntry.Usage.Cost.Basis != usage.Unknown {
		t.Fatalf("Usage.Cost.Basis = %q, want unknown", reportEntry.Usage.Cost.Basis)
	}
	if reportEntry.Usage.Note != "remote: server sent no usage" {
		t.Fatalf("Usage.Note = %q, want %q", reportEntry.Usage.Note, "remote: server sent no usage")
	}
}

func TestCatchUpBranchCheckedOutRetries(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")
	// Unlike the ordinary case, check the binding's own branch out as this
	// repo's current branch, which is what makes the fetch below collide.
	runGit(t, clientRepo, "checkout", "relay/api")

	bundle, _ := newRoundResultBundle(t, ctx, g, clientRepo, "refs/heads/relay/api", c1)

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

// captureHandler is a slog.Handler that records every record it sees, so a
// test can count how often a message was logged (#253).
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool { return true }

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(name string) slog.Handler { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.records)
}

func TestCatchUpBranchCheckedOutLogsOnce(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")
	// Check the binding's own branch out, which makes the absorb below collide.
	runGit(t, clientRepo, "checkout", "relay/api")

	bundle, _ := newRoundResultBundle(t, ctx, g, clientRepo, "refs/heads/relay/api", c1)
	bundleBytes, err := io.ReadAll(bundle)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	_ = bundle.Close()

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
		roundBundleFunc: func(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bundleBytes)), nil
		},
	}
	rt := Runtime{
		Store: st, Herdr: &fakeHerdr{}, Remote: fr,
		Transport: remote.NewBundleTransport(g, t.TempDir()),
		Now:       func() time.Time { return baseTime },
	}

	// Ordering between tests cannot leak: this binding starts unwarned.
	checkedOutWarned.Delete("api")

	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cur := b
	for i := 0; i < 3; i++ {
		next, err := reconcile(t, rt, cur, []herdr.Agent{plannerAgent()})
		if err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
		cur = next
	}

	count := 0
	for _, r := range h.snapshot() {
		if r.Level == slog.LevelInfo && r.Message == "checkout another branch, then relay pull" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Info 'checkout another branch, then relay pull' logged %d times, want exactly 1", count)
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

// TestForwardAvailablePostsToEveryServerOnce: a gate can matter most when
// nothing runs, so ForwardAvailable names every server this client's remote
// bindings point at -- open round or not, active or DONE -- once each, sorted,
// with one answer line per server. Filtering to open rounds (as
// ForwardUnavailable does) must fail this: alpha would still be called, beta
// would not.
func TestForwardAvailablePostsToEveryServerOnce(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())

	// Two remote bindings on alpha: one with an open round, one idle.
	openA := remoteBinding("alpha")
	openA.Name = "open-alpha"
	openA.CWD = "/fake/open-alpha"
	if err := st.Save(openA); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-alpha", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan}); err != nil {
		t.Fatal(err)
	}

	idleA := remoteBinding("alpha")
	idleA.Name = "idle-alpha"
	idleA.CWD = "/fake/idle-alpha"
	if err := st.Save(idleA); err != nil {
		t.Fatal(err)
	}

	// One remote binding on beta, whose work is done.
	doneB := remoteBinding("beta")
	doneB.Name = "done-beta"
	doneB.CWD = "/fake/done-beta"
	doneB.State = store.StateDone
	if err := st.Save(doneB); err != nil {
		t.Fatal(err)
	}

	// One local binding: not a server, so it must never be called.
	local := remoteBinding("")
	local.Name = "local"
	local.CWD = "/fake/local"
	local.Builder = store.Endpoint{PaneID: "w2:p4", Mode: store.ModePane}
	if err := st.Save(local); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{availableResp: remote.AvailableResponse{Provider: "anthropic", Removed: 1}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardAvailable(ctx, rt, "claude/anthropic/haiku")
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want one line per server (alpha, beta)", lines)
	}

	var calls []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Available:") {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("Available calls = %d, want exactly 2 (one per server): %v", len(calls), fr.calls)
	}
	if calls[0] != "Available:alpha:claude/anthropic/haiku" {
		t.Errorf("call 0 = %q, want alpha first", calls[0])
	}
	if calls[1] != "Available:beta:claude/anthropic/haiku" {
		t.Errorf("call 1 = %q, want beta second", calls[1])
	}
	if !strings.Contains(lines[0], "alpha") {
		t.Errorf("line 0 = %q, want it naming alpha", lines[0])
	}
	if !strings.Contains(lines[1], "beta") {
		t.Errorf("line 1 = %q, want it naming beta", lines[1])
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
