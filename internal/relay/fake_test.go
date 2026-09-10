package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
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

type startCall struct {
	Name, Kind, Pane string
	Args             []string
}

type addWorktreeCall struct {
	Dir, Path, Branch, Commit string
}

type removeWorktreeCall struct {
	Dir, Path string
	Force     bool
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

	addWorktreeErr   error
	addWorktreeCalls []addWorktreeCall

	removeWorktreeErr   error
	removeWorktreeCalls []removeWorktreeCall

	dirtyResult  bool
	dirtyErr     error
	dirtyCalls   int
	lastDirtyDir string
}

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

func (f *fakeGit) AddWorktree(ctx context.Context, dir, path, branch, commit string) error {
	f.addWorktreeCalls = append(f.addWorktreeCalls, addWorktreeCall{
		Dir: dir, Path: path, Branch: branch, Commit: commit,
	})
	if f.addWorktreeErr != nil {
		return f.addWorktreeErr
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

func TestFakeSatisfiesGit(t *testing.T) {
	var _ Git = (*fakeGit)(nil)
	var _ Git = (*git.Client)(nil)
}

// consultTable is a table with one consult role, `reviewer`, layered over the
// shipped builder aliases.
func consultTable(t *testing.T) *alias.Table {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aliases.json")
	body := `[{"name":"reviewer","kind":"claude","args":["--agent","reviewer"],"role":"consult","tree":"binding"}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write aliases: %v", err)
	}
	tbl, err := alias.LoadTable(path)
	if err != nil {
		t.Fatalf("LoadTable: %v", err)
	}
	return tbl
}

// fakeHerdr is the in-memory Herdr used by every test in this package.
type fakeHerdr struct {
	agents    []herdr.Agent
	prompts   []promptCall
	keys      []keyCall
	starts    []startCall
	reads     []readCall
	notices   []string
	readOut   string
	newPane   string
	newTab    string
	tabs      []tabCall
	splits    int
	promptErr error
	stalls    int // when >0, Prompt returns ErrPromptStalled and decrements
	listCalls int
	listErr   error // when set, every ListAgents call after the first fails
	// onList runs at the top of every ListAgents call. Tick makes that call
	// after it has listed bindings and before it reconciles them, which is
	// the one window a concurrent `relay unbind` has to land in.
	onList  func()
	readErr error // when set, ReadAgent fails instead of returning readOut
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

func (f *fakeHerdr) SplitPane(context.Context, string, string, string) (string, error) {
	f.splits++
	if f.newPane == "" {
		return "", errors.New("pane split returned no pane id")
	}
	return f.newPane, nil
}

func (f *fakeHerdr) CreateTab(_ context.Context, workspaceID, cwd, label string) (string, error) {
	f.tabs = append(f.tabs, tabCall{WorkspaceID: workspaceID, CWD: cwd, Label: label})
	if f.newTab != "" {
		return f.newTab, nil
	}
	return f.newPane, nil
}

func (f *fakeHerdr) StartAgent(_ context.Context, name, kind, pane string, args []string) error {
	f.starts = append(f.starts, startCall{Name: name, Kind: kind, Pane: pane, Args: args})
	return nil
}

func (f *fakeHerdr) Notify(_ context.Context, msg string) error {
	f.notices = append(f.notices, msg)
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
