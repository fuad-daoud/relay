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
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
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
