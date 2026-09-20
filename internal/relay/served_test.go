package relay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// servedTierCandidatesJSON has one builder candidate with a "read" tier
// default, so tests can exercise "candidate over policy" without a bespoke
// fixture per test.
const servedTierCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"],"tier":"read"}
]`

// servedNoTierCandidateJSON has one builder candidate with no tier of its
// own, so ServedBuilderTier's chain falls through to policy/harness.
const servedNoTierCandidateJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
]`

func TestResolveServedTier(t *testing.T) {
	set := candidateSet(t, servedTierCandidatesJSON)
	token := "claude/test/m"

	// explicit beats candidate beats policy beats harness.
	rt := Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "harness"}}}
	if got, err := ResolveServedTier(rt, token, "edit"); err != nil || got != harness.TierEdit {
		t.Fatalf("explicit edit: got %v, err %v, want edit, nil", got, err)
	}

	// candidate beats policy (explicit empty).
	rt = Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "harness"}}}
	if got, err := ResolveServedTier(rt, token, ""); err != nil || got != harness.TierRead {
		t.Fatalf("candidate over policy: got %v, err %v, want read, nil", got, err)
	}

	// policy beats harness (no candidate tier, no explicit).
	rt = Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "edit"}}}
	if got, err := ResolveServedTier(rt, "opencode/test/m", ""); err != nil || got != harness.TierEdit {
		t.Fatalf("policy over harness: got %v, err %v, want edit, nil", got, err)
	}

	// Unresolvable token contributes nothing: falls through to policy.
	rt = Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "edit"}}}
	if got, err := ResolveServedTier(rt, "claude/unknown/model", ""); err != nil || got != harness.TierEdit {
		t.Fatalf("unresolvable token: got %v, err %v, want edit, nil", got, err)
	}

	// Everything empty -> harness.
	rt = Runtime{Candidates: nil, Policy: policy.Policy{}}
	if got, err := ResolveServedTier(rt, "", ""); err != nil || got != harness.TierHarness {
		t.Fatalf("all empty: got %v, err %v, want harness, nil", got, err)
	}

	// Explicit above max_tier -> ErrTierAboveMax.
	rt = Runtime{Candidates: set, Policy: policy.Policy{}}
	if _, err := ResolveServedTier(rt, token, "yolo"); !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("explicit above max_tier: err = %v, want ErrTierAboveMax", err)
	}

	// Policy tier above max_tier with explicit "" -> ErrTierAboveMax.
	rt = Runtime{Candidates: nil, Policy: policy.Policy{Tier: map[string]string{"builder": "yolo"}}}
	if _, err := ResolveServedTier(rt, "", ""); !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("policy tier above max_tier: err = %v, want ErrTierAboveMax", err)
	}

	// explicit=yolo, MaxTier=yolo -> ok (allowYolo is never consulted).
	rt = Runtime{Candidates: nil, Policy: policy.Policy{MaxTier: "yolo"}}
	if got, err := ResolveServedTier(rt, "", "yolo"); err != nil || got != harness.TierYolo {
		t.Fatalf("explicit yolo at max_tier yolo: got %v, err %v, want yolo, nil", got, err)
	}

	// Malformed explicit -> ParseTier's error.
	rt = Runtime{Candidates: nil, Policy: policy.Policy{}}
	if _, err := ResolveServedTier(rt, "", "bogus"); err == nil {
		t.Fatal("malformed explicit: want error, got nil")
	}
}

func TestServedBuilderTier(t *testing.T) {
	set := candidateSet(t, servedNoTierCandidateJSON)

	// Policy tier set -> that tier (candidate token resolved via PickServedCandidate("") has no tier of its own).
	rt := Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "edit"}}, Now: func() time.Time { return baseTime }, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	if got := ServedBuilderTier(rt); got != harness.TierEdit {
		t.Fatalf("policy tier set: got %v, want edit", got)
	}

	// Refused chain (policy tier above max_tier) -> harness, not an error.
	rt = Runtime{Candidates: nil, Policy: policy.Policy{Tier: map[string]string{"builder": "yolo"}}, Now: func() time.Time { return baseTime }}
	if got := ServedBuilderTier(rt); got != harness.TierHarness {
		t.Fatalf("refused chain: got %v, want harness", got)
	}
}

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
	return string(out)
}

func TestRoundStateOf(t *testing.T) {
	// Arm 1: StateNeedsYou
	b1 := store.Binding{
		State: store.StateNeedsYou,
		Round: 1,
	}
	if got := RoundStateOf(b1, nil); got != remote.RoundNeedsYou {
		t.Fatalf("arm 1 (needs_you): got %v, want %v", got, remote.RoundNeedsYou)
	}

	// Arm 2: RoundRunning (plan sent, report not yet sent)
	b2 := store.Binding{
		State: store.StateActive,
		Round: 1,
	}
	entries2 := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan},
	}
	if got := RoundStateOf(b2, entries2); got != remote.RoundRunning {
		t.Fatalf("arm 2 (running): got %v, want %v", got, remote.RoundRunning)
	}

	// Arm 3: RoundClosed (Serve.ClosedRound > Serve.AckedRound)
	b3 := store.Binding{
		State: store.StateActive,
		Round: 2,
		Serve: &store.ServeFacts{
			ClosedRound: 1,
			AckedRound:  0,
		},
	}
	// Entries has both plan and report for round 1
	entries3 := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan},
		{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport},
	}
	if got := RoundStateOf(b3, entries3); got != remote.RoundClosed {
		t.Fatalf("arm 3 (closed): got %v, want %v", got, remote.RoundClosed)
	}

	// Arm 4: RoundIdle
	b4 := store.Binding{
		State: store.StateActive,
		Round: 2,
		Serve: &store.ServeFacts{
			ClosedRound: 1,
			AckedRound:  1,
		},
	}
	if got := RoundStateOf(b4, entries3); got != remote.RoundIdle {
		t.Fatalf("arm 4 (idle): got %v, want %v", got, remote.RoundIdle)
	}
}

func TestServedViewReportOutcome(t *testing.T) {
	b := store.Binding{
		Name:             "api",
		State:            store.StateActive,
		Round:            3,
		BuilderCandidate: "claude-sonnet",
		RoundCap:         10,
		RoundTimeoutMS:   30000,
		Serve: &store.ServeFacts{
			ClosedRound:  2,
			AckedRound:   1,
			ResultCommit: "c222",
			DirtyCommit:  "d222",
		},
	}

	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done"},
		{Round: 2, Kind: store.KindReport, Outcome: "blocked"},
		{Round: 2, Kind: store.KindReport, Outcome: "halted"},
	}

	view := ServedView(b, entries)
	if view.ReportOutcome != "halted" {
		t.Fatalf("ReportOutcome: got %q, want %q", view.ReportOutcome, "halted")
	}
	if view.RoundState != remote.RoundClosed {
		t.Fatalf("RoundState: got %v, want %v", view.RoundState, remote.RoundClosed)
	}
	if view.ResultCommit != "c222" {
		t.Fatalf("ResultCommit: got %q, want %q", view.ResultCommit, "c222")
	}
	if view.DirtyCommit != "d222" {
		t.Fatalf("DirtyCommit: got %q, want %q", view.DirtyCommit, "d222")
	}

	// With no report entries for ClosedRound
	viewNoReports := ServedView(b, nil)
	if viewNoReports.ReportOutcome != "" {
		t.Fatalf("ReportOutcome with no entries: got %q, want %q", viewNoReports.ReportOutcome, "")
	}
}

func TestServedViewDiffFacts(t *testing.T) {
	b := store.Binding{
		Name:             "api",
		State:            store.StateActive,
		Round:            3,
		BuilderCandidate: "claude-sonnet",
		Serve: &store.ServeFacts{
			ClosedRound: 2,
			AckedRound:  1,
		},
	}

	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindDiff, Note: "old round's diff", Commits: 9, Tree: "dirty"},
		{Round: 2, Kind: store.KindDiff, Note: "1 file, +1 -0; 1 commit, clean", Commits: 1, Tree: "clean"},
	}

	view := ServedView(b, entries)
	if view.DiffNote != "1 file, +1 -0; 1 commit, clean" {
		t.Fatalf("DiffNote: got %q, want the round 2 diff entry's note", view.DiffNote)
	}
	if view.DiffCommits != 1 {
		t.Fatalf("DiffCommits: got %d, want 1", view.DiffCommits)
	}
	if view.DiffTree != "clean" {
		t.Fatalf("DiffTree: got %q, want clean", view.DiffTree)
	}

	// With no diff entry for ClosedRound, every fact stays zero.
	viewNoDiff := ServedView(b, entries[:1])
	if viewNoDiff.DiffNote != "" || viewNoDiff.DiffCommits != 0 || viewNoDiff.DiffTree != "" {
		t.Fatalf("diff facts with no matching entry: got %+v, want all zero", viewNoDiff)
	}
}

// TestServedViewCarriesClosedRoundUsage checks that the view ships the
// closed round's usage the way it ships ReportOutcome (#216): from the
// newest KindReport entry for Serve.ClosedRound, and only from it.
func TestServedViewCarriesClosedRoundUsage(t *testing.T) {
	closed := usage.Usage{
		Harness: "opencode",
		Model:   "haiku",
		Tokens:  usage.Tokens{In: 1000, Out: 200},
		Cost:    usage.Cost{USD: 0.12, Basis: usage.Measured},
	}
	old := usage.Usage{
		Harness: "claude",
		Cost:    usage.Cost{Basis: usage.Unknown},
		Note:    "shared cwd",
	}
	b := store.Binding{
		Name:             "api",
		State:            store.StateActive,
		Round:            3,
		BuilderCandidate: "claude-sonnet",
		Serve: &store.ServeFacts{
			ClosedRound: 2,
			AckedRound:  1,
		},
	}

	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done", Usage: &old},
		{Round: 2, Kind: store.KindReport, Outcome: "blocked", Usage: &closed},
	}

	view := ServedView(b, entries)
	if view.Usage == nil || *view.Usage != closed {
		t.Fatalf("Usage = %+v, want the closed round's report entry's usage", view.Usage)
	}

	// The closed round's report carries no usage: nil, not the older
	// round's figure.
	entriesNoUsage := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done", Usage: &old},
		{Round: 2, Kind: store.KindReport, Outcome: "blocked"},
	}
	viewNoUsage := ServedView(b, entriesNoUsage)
	if viewNoUsage.Usage != nil {
		t.Fatalf("Usage = %+v, want nil when the closed round's report has none", viewNoUsage.Usage)
	}

	// No report at all: nil.
	viewNoReports := ServedView(b, nil)
	if viewNoReports.Usage != nil {
		t.Fatalf("Usage with no entries = %+v, want nil", viewNoReports.Usage)
	}
}

func TestCloseServedRoundClean(t *testing.T) {
	ctx := context.Background()
	client := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)

	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relay/api")

	branchHead := strings.TrimSpace(runGit(t, bare, "rev-parse", "refs/heads/relay/api"))

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relay/api")

	rt := Runtime{Git: client}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relay/api",
		Worktree: wt,
		Round:    2, // queueReport already advanced Round from 1 to 2
		Serve: &store.ServeFacts{
			BareRepo: bare,
		},
	}

	res := closeServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 1 {
		t.Fatalf("ClosedRound: got %d, want 1", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != branchHead {
		t.Fatalf("ResultCommit: got %q, want %q", res.Serve.ResultCommit, branchHead)
	}
	if res.Serve.DirtyCommit != "" {
		t.Fatalf("DirtyCommit on clean worktree: got %q, want empty", res.Serve.DirtyCommit)
	}

	// Side ref must not exist
	sideSHA, ok, err := client.RefSHA(ctx, bare, "refs/relay/api/round-1")
	if err != nil || ok {
		t.Fatalf("side ref exists on clean worktree: sha=%q, ok=%v, err=%v", sideSHA, ok, err)
	}

	// Branch must be unchanged
	headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relay/api")
	if err != nil || !ok || headAfter != branchHead {
		t.Fatalf("branch changed: got %q, want %q", headAfter, branchHead)
	}
}

func TestCloseServedRoundDirty(t *testing.T) {
	ctx := context.Background()
	client := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)

	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relay/api")

	branchHead := strings.TrimSpace(runGit(t, bare, "rev-parse", "refs/heads/relay/api"))

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relay/api")

	// Make worktree dirty with an untracked file
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("dirty work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{Git: client}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relay/api",
		Worktree: wt,
		Round:    2,
		Serve: &store.ServeFacts{
			BareRepo: bare,
		},
	}

	res := closeServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 1 {
		t.Fatalf("ClosedRound: got %d, want 1", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != branchHead {
		t.Fatalf("ResultCommit: got %q, want %q", res.Serve.ResultCommit, branchHead)
	}
	if res.Serve.DirtyCommit == "" {
		t.Fatal("DirtyCommit is empty on dirty worktree")
	}

	// Side ref must exist in bare repo and equal DirtyCommit
	sideSHA, ok, err := client.RefSHA(ctx, bare, "refs/relay/api/round-1")
	if err != nil || !ok {
		t.Fatalf("side ref missing: ok=%v, err=%v", ok, err)
	}
	if sideSHA != res.Serve.DirtyCommit {
		t.Fatalf("side ref %q != DirtyCommit %q", sideSHA, res.Serve.DirtyCommit)
	}

	// Side ref parent must be branch head
	parent := strings.TrimSpace(runGit(t, bare, "rev-parse", sideSHA+"^"))
	if parent != branchHead {
		t.Fatalf("side ref parent %q != branchHead %q", parent, branchHead)
	}

	// Branch must be unchanged
	headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relay/api")
	if err != nil || !ok || headAfter != branchHead {
		t.Fatalf("branch changed: got %q, want %q", headAfter, branchHead)
	}
}

func TestCloseServedRoundGitFailureKeepsFacts(t *testing.T) {
	ctx := context.Background()
	fGit := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relay/api": "commit123",
		},
		dirtyErr: errors.New("dirty failure"),
	}

	rt := Runtime{Git: fGit}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relay/api",
		Worktree: "/tmp/fake-wt",
		Round:    2,
		Serve: &store.ServeFacts{
			BareRepo:     "/tmp/fake-bare",
			ClosedRound:  0,
			ResultCommit: "old-head",
			DirtyCommit:  "",
		},
	}

	// Must not panic, and must leave facts unchanged
	res := closeServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 0 {
		t.Fatalf("ClosedRound changed on git error: got %d, want 0", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != "old-head" {
		t.Fatalf("ResultCommit changed on git error: got %q, want old-head", res.Serve.ResultCommit)
	}
	if res.Serve.DirtyCommit != "" {
		t.Fatalf("DirtyCommit changed on git error: got %q, want empty", res.Serve.DirtyCommit)
	}
}

func TestDeliverAndSettleOwnedLeavesQueued(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:    "api",
		Owner:   "client1",
		State:   store.StateActive,
		Round:   1,
		CWD:     t.TempDir(),
		Planner: store.Endpoint{SessionID: "sess1", PaneID: "p1"},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("api", store.LogEntry{
		Round:     1,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Payload:   "the report",
		Confirmed: false,
	}); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{Store: st, Herdr: &fakeHerdr{}, Now: time.Now}
	agents := []herdr.Agent{
		{
			Session: herdr.Session{Value: "sess1"},
			PaneID:  "p1",
			Status:  herdr.StatusIdle,
			Focused: true,
		},
	}

	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = deliverAndSettle(ctx, rt, tx, b, agents)
		return err
	})
	if err != nil {
		t.Fatalf("deliverAndSettle: %v", err)
	}

	if got.State == store.StateHeld {
		t.Fatalf("state was set to Held for owned binding: %v", got.State)
	}
	if got.State != store.StateActive {
		t.Fatalf("state changed: got %v, want %v", got.State, store.StateActive)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[0].Confirmed {
		t.Fatalf("payload was confirmed: %+v", entries)
	}
}

func TestReconcileHeadlessOwnedCloseRecordsFacts(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fr := newFakeRunner()
	expectedSHA := "commit-1234567890"
	fGit := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relay/api": expectedSHA,
		},
	}

	wtDir := t.TempDir()
	bareDir := t.TempDir()

	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		State:    store.StateActive,
		Round:    1,
		RoundCap: 10,
		CWD:      wtDir,
		Builder:  store.Endpoint{Mode: store.ModeHeadless, PID: 1234},
		Branch:   "relay/api",
		Worktree: wtDir,
		Serve: &store.ServeFacts{
			BareRepo: bareDir,
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	// Round 1 plan entry
	if err := st.AppendLog("api", store.LogEntry{
		Round:     1,
		Direction: store.DirToBuilder,
		Kind:      store.KindPlan,
	}); err != nil {
		t.Fatal(err)
	}

	// Write report and marker
	if err := os.WriteFile(st.ReportPath("api", 1), []byte("report\n```relay\nstatus: done\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.DonePath("api", 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{
		Store:  st,
		Git:    fGit,
		Runner: fr,
		Herdr:  &fakeHerdr{},
		Now:    time.Now,
	}

	var reconciled store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		var err error
		reconciled, err = Reconcile(ctx, rt, tx, b, nil)
		return err
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if reconciled.Round != 2 {
		t.Fatalf("Round: got %d, want 2", reconciled.Round)
	}
	if reconciled.Serve == nil {
		t.Fatal("Serve is nil")
	}
	if reconciled.Serve.ClosedRound != 1 {
		t.Fatalf("Serve.ClosedRound: got %d, want 1", reconciled.Serve.ClosedRound)
	}
	if reconciled.Serve.ResultCommit != expectedSHA {
		t.Fatalf("Serve.ResultCommit: got %q, want %q", reconciled.Serve.ResultCommit, expectedSHA)
	}
}
