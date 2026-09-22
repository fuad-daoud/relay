package relay

import (
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

// TestRenderServersBuilders pins #285's queue line and #295's quota:
// an enrolled, queue-aware server's row gains "builders %d/%d, %d queued,
// scopes %s", with the scopes word covering off/on/on (<slice>)/on (<quota>)/
// on (<slice>, <quota>); an enrolled server that is not queue-aware (a
// pre-queue server) gets no builders text.
func TestRenderServersBuilders(t *testing.T) {
	probes := []ServerProbe{
		{
			Name: "zen", URL: "https://zen:7777", State: "enrolled", Label: "laptop",
			QueueAware: true, Builders: &remote.BuildersView{Running: 2, Queued: 1, Cap: 3, Scopes: false},
		},
		{
			Name: "contabo", URL: "https://contabo:7777", State: "enrolled", Label: "vps",
			QueueAware: true, Builders: &remote.BuildersView{Running: 2, Queued: 1, Cap: 3, Scopes: true, Slice: "relay.slice", Quota: "200%"},
		},
		{
			Name: "quotaonly", URL: "https://quota:7777", State: "enrolled", Label: "vps",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true, Quota: "150%"},
		},
		{
			Name: "sliceonly", URL: "https://slice:7777", State: "enrolled", Label: "vps",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true, Slice: "relay.slice"},
		},
		{
			Name: "plain", URL: "https://plain:7777", State: "enrolled", Label: "vps",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true},
		},
		{
			Name: "old", URL: "https://old:7777", State: "enrolled", Label: "laptop",
		},
	}
	out := RenderServers(probes)
	for _, want := range []string{
		"builders 2/3, 1 queued, scopes off",
		"builders 2/3, 1 queued, scopes on (relay.slice, 200%)",
		"builders 1/3, 0 queued, scopes on (150%)",
		"builders 1/3, 0 queued, scopes on (relay.slice)",
		"builders 1/3, 0 queued, scopes on\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q; got:\n%s", want, out)
		}
	}
	oldLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "https://old:7777") {
			oldLine = line
			break
		}
	}
	if oldLine == "" {
		t.Fatalf("output missing the old server's row; got:\n%s", out)
	}
	if strings.Contains(oldLine, "builders ") {
		t.Errorf("non-queue-aware server row must not carry builders text; got:\n%s", oldLine)
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

// TestObserveRemoteCopiesStalledSince pins #252's remote half: a running
// round's stall stamp rides from the server view onto the client binding,
// status shows it, and a later view without one clears it.
// TestObserveRemoteQueuedKeepsFacts pins #285's client tolerance: a queued
// view copies the server's queue facts onto Builder.RemoteQueue, sets
// RemoteStatus to "queued", and neither halts nor catches up.
// TestObserveRemoteRunningClearsQueue pins #285's clear rule: a binding that
// was queued and now observes a running view drops its stale RemoteQueue.
// TestReconcileRemoteRefreshesCandidate checks that a server-side switch
// (the round is now running a different candidate than this binding last
// recorded) updates the token and harness kind and logs a switch entry
// naming the server, and that a later tick whose view still names the same
// candidate adds nothing more (#100 step 2).
// TestSyncRemoteCollectsClosedRoundWithoutDelivery checks that SyncRemote
// (the read path `relay status`/`pull`/`wait` share, spec §2.2) collects a
// closed round -- the report entry lands, pending -- without ever
// delivering it to a planner pane.
// TestSyncRemoteSkipsDoneAndLocal checks that SyncRemote never touches the
// network for a binding it should not sync: one already DONE, and one that
// is not a remote binding at all.
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

// TestCatchUpWritesDiffEntryFromView checks that catchUp writes the
// client's own diff log entry from the server's view facts (Note, Commits,
// Tree, plus the downloaded patch as Path) rather than letting queueReport
// capture its own diff from a local baseline that a remote binding never
// has.
// TestCatchUpAdoptedBranchAbsorbsServerRef pins the adopted-branch fix
// (#274): `add --server --branch feature/x` adopts a branch relay did not
// create, but the server still cuts its own branch and the round bundle it
// ships always names refs/heads/relay/<name> (handleRoundBundle snapshots
// refs/heads/<server branch>, and the server's branch is relay/<name>).
// Catch-up must allow the server's ref through Absorb, then fast-forward the
// adopted branch to it, so the planner's own branch is the one carrying the
// round's result.
// TestCatchUpKeepsServerUsage checks that catch-up keeps the usage the
// server measured and shipped with the round (#216): the client's report
// entry stores it verbatim instead of re-measuring a round whose record
// lives on the server.
// TestCatchUpRecordsRusage extends TestCatchUpKeepsServerUsage's pattern:
// the server's cgroup measurement rides the same view as Usage, and lands
// on the report entry catchUp writes (#244, #216).
// TestCatchUpPreUsageServerNotes checks the honest answer for a server
// built before this (#216): with no usage in the view, the client's report
// entry says the server sent none -- basis unknown -- rather than reading
// a record the client does not have.
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
