package relay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

type promptCall struct{ Target, Text string }
type keyCall struct{ Target, Keys string }
type readCall struct {
	Target, Source string
	Lines          int
}
type tabCall struct {
	WorkspaceID, CWD, Label string
}

// metadataCall is one recorded ReportMetadata call.
type metadataCall struct {
	Pane string
	Meta herdr.PaneMetadata
}

type startCall struct {
	Name, Kind, Pane string
	Args             []string
}

type addWorktreeCall struct {
	Dir, Path, Branch, Commit string
}

type addDetachedWorktreeCall struct {
	Dir, Path, Commit string
}

type checkoutWorktreeCall struct {
	Dir, Path, Branch string
}

type removeWorktreeCall struct {
	Dir, Path string
	Force     bool
}

type refSHACall struct {
	Dir, Ref string
}

type updateRefCall struct {
	Dir, Ref, NewSHA, OldSHA string
}

type commitTreeCall struct {
	Dir, Tree, Parent, Message string
}

type commitAllCall struct {
	Dir, Message string
}

type mergeFFCall struct {
	Dir, Ref string
}

type rootCommitCall struct {
	Dir string
}

type createBranchCall struct {
	Dir, Branch, Commit string
}

type createTrackingBranchCall struct {
	Dir, Branch, Upstream string
}

type deleteBranchCall struct {
	Dir, Branch string
}

// fakeGit is the in-memory Git used by tests in this package.
type fakeGit struct {
	snapshotTreeID  string
	snapshotTreeErr error
	snapshotCalls   int
	lastSnapshotDir string

	diffResult   git.Diff
	diffErr      error
	diffCalls    int
	lastDiffDir  string
	lastDiffFrom string
	lastDiffTo   string

	headCommitID  string
	headCommitErr error
	headCalls     int
	lastHeadDir   string

	branchExists    bool
	branchExistsErr error
	branchCalls     int
	lastBranchDir   string
	lastBranchName  string

	createBranchErr   error
	createBranchCalls []createBranchCall

	createTrackingBranchErr   error
	createTrackingBranchCalls []createTrackingBranchCall

	deleteBranchErr   error
	deleteBranchCalls []deleteBranchCall
	deleteBranchFunc  func(ctx context.Context, dir, branch string) error

	addWorktreeErr   error
	addWorktreeCalls []addWorktreeCall

	addDetachedWorktreeErr   error
	addDetachedWorktreeCalls []addDetachedWorktreeCall

	checkoutWorktreeErr   error
	checkoutWorktreeCalls []checkoutWorktreeCall

	removeWorktreeErr   error
	removeWorktreeCalls []removeWorktreeCall

	dirtyResult  bool
	dirtyErr     error
	dirtyCalls   int
	lastDirtyDir string

	revListCount    int
	revListErr      error
	revListCalls    int
	lastRevListDir  string
	lastRevListFrom string
	lastRevListTo   string

	refSHACalls []refSHACall
	refSHA      map[string]string
	refSHAErr   error

	updateRefCalls []updateRefCall
	updateRefErr   error

	commitTreeCalls []commitTreeCall
	commitTreeSHA   string
	commitTreeErr   error

	commitAllCalls []commitAllCall
	commitAllSHA   string
	commitAllErr   error

	mergeFFCalls []mergeFFCall
	mergeFFErr   error

	rootCommitCalls []rootCommitCall
	rootCommitSHA   string
	rootCommitErr   error

	// repoFactsOrigin/repoFactsCommonDir configure RepoFacts's success
	// return; repoFactsCommonDir defaults to "<dir>/.git" when unset, since
	// that is what a real repo with no worktrees reports.
	repoFactsOrigin    string
	repoFactsCommonDir string
	repoFactsErr       error
	repoFactsCalls     []repoFactsCall

	// tags is what ListTags returns; ListTagsErr makes it fail.
	tags        map[string]string
	listTagsErr error

	// treeFingerprints is the sequence TreeFingerprint returns, one entry
	// per call; the last repeats once the sequence is exhausted, so a test
	// that wants a constant signal sets one entry. treeFingerprintErr makes
	// every call fail.
	treeFingerprints     []string
	treeFingerprintErr   error
	treeFingerprintCalls int
}

type repoFactsCall struct{ Dir string }

func (f *fakeGit) SnapshotTree(ctx context.Context, dir string) (string, error) {
	f.snapshotCalls++
	f.lastSnapshotDir = dir
	if f.snapshotTreeErr != nil {
		return "", f.snapshotTreeErr
	}
	return f.snapshotTreeID, nil
}

func (f *fakeGit) DiffTrees(ctx context.Context, dir, from, to string) (git.Diff, error) {
	f.diffCalls++
	f.lastDiffDir = dir
	f.lastDiffFrom = from
	f.lastDiffTo = to
	if f.diffErr != nil {
		return git.Diff{}, f.diffErr
	}
	return f.diffResult, nil
}

func (f *fakeGit) HeadCommit(ctx context.Context, dir string) (string, error) {
	f.headCalls++
	f.lastHeadDir = dir
	if f.headCommitErr != nil {
		return "", f.headCommitErr
	}
	return f.headCommitID, nil
}

func (f *fakeGit) BranchExists(ctx context.Context, dir, branch string) (bool, error) {
	f.branchCalls++
	f.lastBranchDir = dir
	f.lastBranchName = branch
	if f.branchExistsErr != nil {
		return false, f.branchExistsErr
	}
	return f.branchExists, nil
}

func (f *fakeGit) CreateBranch(ctx context.Context, dir, branch, commit string) error {
	f.createBranchCalls = append(f.createBranchCalls, createBranchCall{
		Dir: dir, Branch: branch, Commit: commit,
	})
	if f.createBranchErr != nil {
		return f.createBranchErr
	}
	return nil
}

func (f *fakeGit) CreateTrackingBranch(ctx context.Context, dir, branch, upstream string) error {
	f.createTrackingBranchCalls = append(f.createTrackingBranchCalls, createTrackingBranchCall{
		Dir: dir, Branch: branch, Upstream: upstream,
	})
	return f.createTrackingBranchErr
}

func (f *fakeGit) DeleteBranch(ctx context.Context, dir, branch string) error {
	f.deleteBranchCalls = append(f.deleteBranchCalls, deleteBranchCall{
		Dir: dir, Branch: branch,
	})
	if f.deleteBranchFunc != nil {
		return f.deleteBranchFunc(ctx, dir, branch)
	}
	return f.deleteBranchErr
}

func (f *fakeGit) AddWorktree(ctx context.Context, dir, path, branch, commit string) error {
	f.addWorktreeCalls = append(f.addWorktreeCalls, addWorktreeCall{
		Dir: dir, Path: path, Branch: branch, Commit: commit,
	})
	if f.addWorktreeErr != nil {
		return f.addWorktreeErr
	}
	return nil
}

func (f *fakeGit) AddDetachedWorktree(ctx context.Context, dir, path, commit string) error {
	f.addDetachedWorktreeCalls = append(f.addDetachedWorktreeCalls, addDetachedWorktreeCall{
		Dir: dir, Path: path, Commit: commit,
	})
	if f.addDetachedWorktreeErr != nil {
		return f.addDetachedWorktreeErr
	}
	return nil
}

func (f *fakeGit) CheckoutWorktree(ctx context.Context, dir, path, branch string) error {
	f.checkoutWorktreeCalls = append(f.checkoutWorktreeCalls, checkoutWorktreeCall{
		Dir: dir, Path: path, Branch: branch,
	})
	if f.checkoutWorktreeErr != nil {
		return f.checkoutWorktreeErr
	}
	return nil
}

func (f *fakeGit) RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	f.removeWorktreeCalls = append(f.removeWorktreeCalls, removeWorktreeCall{
		Dir: dir, Path: path, Force: force,
	})
	if f.removeWorktreeErr != nil {
		return f.removeWorktreeErr
	}
	return nil
}

func (f *fakeGit) Dirty(ctx context.Context, dir string) (bool, error) {
	f.dirtyCalls++
	f.lastDirtyDir = dir
	if f.dirtyErr != nil {
		return false, f.dirtyErr
	}
	return f.dirtyResult, nil
}

func (f *fakeGit) RevListCount(ctx context.Context, dir, from, to string) (int, error) {
	f.revListCalls++
	f.lastRevListDir = dir
	f.lastRevListFrom = from
	f.lastRevListTo = to
	if f.revListErr != nil {
		return 0, f.revListErr
	}
	return f.revListCount, nil
}

func (f *fakeGit) RefSHA(ctx context.Context, dir, ref string) (string, bool, error) {
	f.refSHACalls = append(f.refSHACalls, refSHACall{Dir: dir, Ref: ref})
	if f.refSHAErr != nil {
		return "", false, f.refSHAErr
	}
	if f.refSHA != nil {
		sha, ok := f.refSHA[ref]
		return sha, ok, nil
	}
	return "fakerefsha", true, nil
}

func (f *fakeGit) UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA string) error {
	f.updateRefCalls = append(f.updateRefCalls, updateRefCall{
		Dir: dir, Ref: ref, NewSHA: newSHA, OldSHA: oldSHA,
	})
	if f.refSHA != nil {
		f.refSHA[ref] = newSHA
	}
	return f.updateRefErr
}

func (f *fakeGit) CommitTree(ctx context.Context, dir, tree, parent, message string) (string, error) {
	f.commitTreeCalls = append(f.commitTreeCalls, commitTreeCall{
		Dir: dir, Tree: tree, Parent: parent, Message: message,
	})
	if f.commitTreeErr != nil {
		return "", f.commitTreeErr
	}
	if f.commitTreeSHA != "" {
		return f.commitTreeSHA, nil
	}
	return "fakecommit", nil
}

func (f *fakeGit) CommitAll(ctx context.Context, dir, message string) (string, error) {
	f.commitAllCalls = append(f.commitAllCalls, commitAllCall{Dir: dir, Message: message})
	if f.commitAllErr != nil {
		return "", f.commitAllErr
	}
	return f.commitAllSHA, nil
}

func (f *fakeGit) MergeFF(ctx context.Context, dir, ref string) error {
	f.mergeFFCalls = append(f.mergeFFCalls, mergeFFCall{Dir: dir, Ref: ref})
	return f.mergeFFErr
}

func (f *fakeGit) RootCommit(ctx context.Context, dir string) (string, error) {
	f.rootCommitCalls = append(f.rootCommitCalls, rootCommitCall{Dir: dir})
	if f.rootCommitErr != nil {
		return "", f.rootCommitErr
	}
	if f.rootCommitSHA != "" {
		return f.rootCommitSHA, nil
	}
	return "fakerootcommit", nil
}

func (f *fakeGit) ListTags(ctx context.Context, dir string) (map[string]string, error) {
	if f.listTagsErr != nil {
		return nil, f.listTagsErr
	}
	return f.tags, nil
}

func (f *fakeGit) RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error) {
	f.repoFactsCalls = append(f.repoFactsCalls, repoFactsCall{Dir: dir})
	if f.repoFactsErr != nil {
		return "", "", f.repoFactsErr
	}
	commonDir = f.repoFactsCommonDir
	if commonDir == "" {
		commonDir = dir + "/.git"
	}
	return f.repoFactsOrigin, commonDir, nil
}

// TreeFingerprint returns the next configured fingerprint; the last repeats
// once the sequence is exhausted. No entries means "".
func (f *fakeGit) TreeFingerprint(ctx context.Context, dir string) (string, error) {
	if f.treeFingerprintErr != nil {
		return "", f.treeFingerprintErr
	}
	if len(f.treeFingerprints) == 0 {
		return "", nil
	}
	i := f.treeFingerprintCalls
	f.treeFingerprintCalls++
	if i >= len(f.treeFingerprints) {
		i = len(f.treeFingerprints) - 1
	}
	return f.treeFingerprints[i], nil
}

func TestFakeSatisfiesGit(t *testing.T) {
	var _ Git = (*fakeGit)(nil)
	var _ Git = (*git.Client)(nil)
}

// TestFakeStartAgentRefusesAnInvalidName pins the fake against drift from the
// real client: it must return herdr's own error for a name herdr would refuse,
// or tests could pass against a name `herdr agent start` then rejects.
func TestFakeStartAgentRefusesAnInvalidName(t *testing.T) {
	f := &fakeHerdr{}

	err := f.StartAgent(context.Background(), strings.Repeat("a", 33), "claude", "w2:p9", nil)

	if !errors.Is(err, herdr.ErrInvalidAgentName) {
		t.Fatalf("StartAgent err = %v, want one wrapping herdr.ErrInvalidAgentName", err)
	}
}

// fakeHerdr is the in-memory Herdr used by every test in this package.
type fakeHerdr struct {
	agents    []herdr.Agent
	prompts   []promptCall
	keys      []keyCall
	starts    []startCall
	reads     []readCall
	notices   []string      // titles, so every existing `f.notices[0]` assertion still reads the message text
	bodies    []string      // parallel to notices
	sounds    []herdr.Sound // parallel to notices
	metadata  []metadataCall
	metaErr   error // when set, ReportMetadata returns this error instead of recording the call
	readOut   string
	newPane   string
	newTab    string
	tabs      []tabCall
	promptErr error
	stalls    int // when >0, Prompt returns ErrPromptStalled and decrements
	listCalls int
	listErr   error // when set, every ListAgents call after the first fails
	// onList runs at the top of every ListAgents call. Tick makes that call
	// after it has listed bindings and before it reconciles them, which is
	// the one window a concurrent `relay unbind` has to land in.
	onList  func()
	readErr error // when set, ReadAgent fails instead of returning readOut

	closed   []string // pane ids handed to ClosePane
	closeErr error    // when set, ClosePane fails
	onClose  func()

	// onSpawn runs at the top of CreateTab, before any other logic. Ask calls CreateTab as its first herdr call after releasing the lock, which is where a test proves the lock is free and where it can rewrite the reservation to simulate a slow spawn.
	onSpawn func()

	// startErr is returned by StartAgent when set, after recording the call. The strand-on-start path had no test because the fake could not fail a start.
	startErr error
}

func (f *fakeHerdr) ListAgents(context.Context) ([]herdr.Agent, error) {
	f.listCalls++
	if f.onList != nil {
		f.onList()
	}
	// The first call always succeeds: it is Bind's own planner lookup, which
	// is what gets a test flow to the spawn path at all. listErr targets a
	// later, incidental lookup (the post-spawn session-id read), not that one.
	if f.listErr != nil && f.listCalls > 1 {
		return nil, f.listErr
	}
	return f.agents, nil
}

func (f *fakeHerdr) Prompt(_ context.Context, target, text string) error {
	if f.stalls > 0 {
		f.stalls--
		return herdr.ErrPromptStalled
	}
	if f.promptErr != nil {
		return f.promptErr
	}
	f.prompts = append(f.prompts, promptCall{Target: target, Text: text})
	return nil
}

func (f *fakeHerdr) SendKeys(_ context.Context, target, keys string) error {
	f.keys = append(f.keys, keyCall{Target: target, Keys: keys})
	return nil
}

func (f *fakeHerdr) ReadAgent(ctx context.Context, target string, lines int) (string, error) {
	return f.ReadAgentSource(ctx, target, "recent-unwrapped", lines)
}

func (f *fakeHerdr) ReadAgentSource(_ context.Context, target, source string, lines int) (string, error) {
	f.reads = append(f.reads, readCall{Target: target, Source: source, Lines: lines})
	if f.readErr != nil {
		return "", f.readErr
	}
	return f.readOut, nil
}

func (f *fakeHerdr) CreateTab(_ context.Context, workspaceID, cwd, label string) (string, error) {
	if f.onSpawn != nil {
		f.onSpawn()
	}
	f.tabs = append(f.tabs, tabCall{WorkspaceID: workspaceID, CWD: cwd, Label: label})
	if f.newTab != "" {
		return f.newTab, nil
	}
	if f.newPane == "" {
		return "", errors.New("tab creation returned no pane id")
	}
	return f.newPane, nil
}

func (f *fakeHerdr) StartAgent(_ context.Context, name, kind, pane string, args []string) error {
	f.starts = append(f.starts, startCall{Name: name, Kind: kind, Pane: pane, Args: args})
	// The fixture must refuse what the real client would refuse, or a test
	// could pass against a name herdr then rejects: #64 was found because
	// this fake had drifted from the real client.
	if err := herdr.ValidateAgentName(name); err != nil {
		return err
	}
	if f.startErr != nil {
		return f.startErr
	}
	return nil
}

func (f *fakeHerdr) Notify(_ context.Context, title, body string, sound herdr.Sound) error {
	f.notices = append(f.notices, title)
	f.bodies = append(f.bodies, body)
	f.sounds = append(f.sounds, sound)
	return nil
}

func (f *fakeHerdr) ReportMetadata(_ context.Context, paneID string, m herdr.PaneMetadata) error {
	if f.metaErr != nil {
		return f.metaErr
	}
	f.metadata = append(f.metadata, metadataCall{Pane: paneID, Meta: m})
	return nil
}

func (f *fakeHerdr) ClosePane(_ context.Context, paneID string) error {
	if f.onClose != nil {
		f.onClose()
	}
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, paneID)
	return nil
}

func TestFakeSatisfiesHerdr(t *testing.T) {
	var _ Herdr = (*fakeHerdr)(nil)
}

// fakeClock is a movable Now for the tests that need time to pass. newRuntime's
// clock is fixed, which is what most tests want; withClock swaps it out on an
// already-built runtime rather than duplicating every seed helper.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// withClock returns rt with its clock replaced. Runtime is a value with a
// *store.Store inside, so the copy shares the same state directory.
func withClock(rt Runtime, c *fakeClock) Runtime {
	rt.Now = c.Now
	return rt
}

func TestFindAgentPrefersSessionOverPane(t *testing.T) {
	agents := []herdr.Agent{
		{PaneID: "w2:p3", Session: herdr.Session{Value: "stale"}, Status: herdr.StatusIdle},
		{PaneID: "w9:pZ", Session: herdr.Session{Value: "sess-1"}, Status: herdr.StatusWorking},
	}
	ep := store.Endpoint{PaneID: "w2:p3", SessionID: "sess-1"}

	got, ok := FindAgent(agents, ep)
	if !ok {
		t.Fatal("FindAgent found nothing")
	}
	if got.PaneID != "w9:pZ" {
		t.Errorf("matched %q; session id must win, since a moved pane gets a new id", got.PaneID)
	}
}

func TestFindAgentFallsBackToPane(t *testing.T) {
	agents := []herdr.Agent{{PaneID: "w2:p4", Status: herdr.StatusIdle}}

	if _, ok := FindAgent(agents, store.Endpoint{PaneID: "w2:p4"}); !ok {
		t.Fatal("pane fallback failed")
	}
	if _, ok := FindAgent(agents, store.Endpoint{PaneID: "w2:p9"}); ok {
		t.Fatal("matched a pane that is not present")
	}
}

// fakeRunner is the in-memory Runner (#99). It records every Start and
// Kill, answers Alive from a per-pid script (the last answer repeats; an
// unscripted pid is alive until killed), and reports the exit code a test
// set with exit(). Nothing here runs a process.
type fakeRunner struct {
	specs   []ProcSpec
	handles []ProcHandle
	kills   []ProcHandle

	startErr error
	aliveErr error
	killErr  error

	alive     map[int][]bool
	exits     map[int]int
	nextPID   int
	exitPaths []string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{alive: map[int][]bool{}, exits: map[int]int{}, nextPID: 4000}
}

// script sets the sequence Alive returns for pid; the last answer repeats.
func (f *fakeRunner) script(pid int, answers ...bool) {
	f.alive[pid] = append([]bool(nil), answers...)
}

// exit sets the code ExitCode reports for pid.
func (f *fakeRunner) exit(pid, code int) { f.exits[pid] = code }

func (f *fakeRunner) Start(_ context.Context, spec ProcSpec) (ProcHandle, error) {
	if f.startErr != nil {
		return ProcHandle{}, f.startErr
	}
	f.nextPID++
	h := ProcHandle{PID: f.nextPID, StartedAt: time.Unix(1_700_000_000+int64(f.nextPID), 0)}
	f.specs = append(f.specs, spec)
	f.handles = append(f.handles, h)
	if _, scripted := f.alive[h.PID]; !scripted {
		f.alive[h.PID] = []bool{true}
	}
	return h, nil
}

func (f *fakeRunner) Alive(_ context.Context, h ProcHandle) (bool, error) {
	if f.aliveErr != nil {
		return false, f.aliveErr
	}
	seq := f.alive[h.PID]
	if len(seq) == 0 {
		return false, nil
	}
	if len(seq) > 1 {
		f.alive[h.PID] = seq[1:]
	}
	return seq[0], nil
}

func (f *fakeRunner) ExitCode(_ context.Context, h ProcHandle, path string) (int, bool) {
	f.exitPaths = append(f.exitPaths, path)
	code, ok := f.exits[h.PID]
	return code, ok
}

func (f *fakeRunner) Kill(_ context.Context, h ProcHandle) error {
	if f.killErr != nil {
		return f.killErr
	}
	f.kills = append(f.kills, h)
	f.alive[h.PID] = []bool{false}
	return nil
}

// fakeUsage scripts what the usage reader returns and records the Source
// it was asked for.
type fakeUsage struct {
	samples     []usage.Sample
	note        string
	sources     []usage.Source
	block       bool // when true, Read waits for ctx and returns nothing
	peekSamples []usage.Sample
	peekNote    string
	peeks       []usage.Source // recorded by Peek; Peek never blocks
}

func (f *fakeUsage) Read(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	f.sources = append(f.sources, src)
	if f.block {
		<-ctx.Done()
		return nil, "blocked"
	}
	return f.samples, f.note
}

func (f *fakeUsage) Peek(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	f.peeks = append(f.peeks, src)
	return f.peekSamples, f.peekNote
}
