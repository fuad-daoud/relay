package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

type promptCall struct {
	Target string
	Text   string
}

type scriptRunner struct {
	mu    sync.Mutex
	specs []relay.ProcSpec
	alive bool
}

func (r *scriptRunner) Start(ctx context.Context, spec relay.ProcSpec) (relay.ProcHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	r.alive = true
	return relay.ProcHandle{PID: 4242, StartedAt: time.Now()}, nil
}

func (r *scriptRunner) Alive(ctx context.Context, h relay.ProcHandle) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.alive, nil
}

func (r *scriptRunner) ExitCode(ctx context.Context, h relay.ProcHandle, logPath string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return 0, !r.alive
}

func (r *scriptRunner) Kill(ctx context.Context, h relay.ProcHandle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive = false
	return nil
}

func (r *scriptRunner) Rusage(ctx context.Context, h relay.ProcHandle, streamPath string) (relay.ProcRusage, bool) {
	return relay.ProcRusage{}, false
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
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
	return string(out)
}

func finishRound(t *testing.T, serverRT relay.Runtime, name string, round int, commitMsg string) {
	t.Helper()
	b, err := serverRT.Store.Load(name)
	if err != nil {
		t.Fatalf("finishRound: read binding %s: %v", name, err)
	}
	worktree := b.Worktree
	if worktree == "" {
		worktree = serverRT.Store.WorktreePath(name)
	}

	reportContent := fmt.Sprintf("# Round %d Report\n\n```relay\nstatus: done\nchanged_paths: [hello.txt]\n```\n", round)
	if err := os.WriteFile(serverRT.Store.ReportPath(name, round), []byte(reportContent), 0o644); err != nil {
		t.Fatalf("finishRound: write report: %v", err)
	}

	helloPath := filepath.Join(worktree, "hello.txt")
	if err := os.WriteFile(helloPath, []byte("hello from round "+strconv.Itoa(round)+"\n"), 0o644); err != nil {
		t.Fatalf("finishRound: write hello.txt: %v", err)
	}
	runGit(t, worktree, "add", "hello.txt")
	runGit(t, worktree, "commit", "-m", commitMsg)

	if sr, ok := serverRT.Runner.(*scriptRunner); ok {
		sr.mu.Lock()
		sr.alive = false
		sr.mu.Unlock()
	}

	if err := os.WriteFile(serverRT.Store.DonePath(name, round), []byte(""), 0o644); err != nil {
		t.Fatalf("finishRound: write done marker: %v", err)
	}
}
