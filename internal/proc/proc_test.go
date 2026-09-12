//go:build unix

package proc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// waitGone polls Alive until it is false or the deadline passes.
func waitGone(t *testing.T, r *Runner, h relay.ProcHandle, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		alive, err := r.Alive(context.Background(), h)
		if err != nil {
			t.Fatalf("Alive: %v", err)
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %s", h.PID, within)
}

func start(t *testing.T, r *Runner, argv ...string) (relay.ProcHandle, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	h, err := r.Start(context.Background(), relay.ProcSpec{Dir: dir, Argv: argv, LogPath: log})
	if err != nil {
		t.Fatalf("Start(%v): %v", argv, err)
	}
	return h, log
}

func TestStartCapturesBothStreamsAndTheExitTrailer(t *testing.T) {
	r := New()
	h, log := start(t, r, "sh", "-c", "echo out; echo err >&2; exit 3")
	if h.PID <= 0 || h.StartedAt.IsZero() {
		t.Fatalf("handle = %+v; want a pid and a start time", h)
	}
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got, want := string(data), "out\nerr\n"+ExitTrailer+"3\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
	code, ok := r.ExitCode(context.Background(), h, log)
	if !ok || code != 3 {
		t.Errorf("ExitCode = %d, %v; want 3, true", code, ok)
	}
}

func TestStartRunsInDirWithExtraEnv(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	h, err := r.Start(context.Background(), relay.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "pwd; echo $RELAY_T3"}, Env: []string{"RELAY_T3=yes"}, LogPath: log,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)
	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("log = %q; want pwd, env, trailer", data)
	}
	// t.TempDir may sit behind a symlink on macOS; compare resolved paths.
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(lines[0])
	if gotDir != wantDir {
		t.Errorf("cwd = %q, want %q", lines[0], dir)
	}
	if lines[1] != "yes" {
		t.Errorf("env line = %q, want yes", lines[1])
	}
	if lines[2] != ExitTrailer+"0" {
		t.Errorf("trailer = %q", lines[2])
	}
}

func TestStartedProcessIsInItsOwnGroupAndKillReturnsWithinGrace(t *testing.T) {
	r := New()
	r.KillGrace = 2 * time.Second
	h, log := start(t, r, "sleep", "60")
	t.Cleanup(func() { _ = syscall.Kill(-h.PID, syscall.SIGKILL) })

	alive, err := r.Alive(context.Background(), h)
	if err != nil || !alive {
		t.Fatalf("Alive right after Start = %v, %v; want true", alive, err)
	}
	pgid, err := syscall.Getpgid(h.PID)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	if pgid != h.PID {
		t.Errorf("supervisor pgid = %d, want its own pid %d (Setsid)", pgid, h.PID)
	}
	if pgid == syscall.Getpgrp() {
		t.Error("supervisor shares the test's process group; it would die with the test")
	}

	began := time.Now()
	if err := r.Kill(context.Background(), h); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if took := time.Since(began); took > r.KillGrace+2*time.Second {
		t.Errorf("Kill took %s; want within the grace plus slack", took)
	}
	alive, err = r.Alive(context.Background(), h)
	if err != nil || alive {
		t.Errorf("Alive after Kill = %v, %v; want false", alive, err)
	}
	if _, ok := r.ExitCode(context.Background(), h, log); ok {
		t.Error("a killed supervisor writes no trailer; ExitCode must be ok=false")
	}
	// Kill on a dead handle is a no-op.
	if err := r.Kill(context.Background(), h); err != nil {
		t.Errorf("second Kill: %v", err)
	}
}

func TestAliveIsFalseForAReusedPid(t *testing.T) {
	r := New()
	// Our own pid certainly exists; a start time that is not ours must not match.
	h := relay.ProcHandle{PID: os.Getpid(), StartedAt: time.Unix(1_000_000, 0)}
	alive, err := r.Alive(context.Background(), h)
	if err != nil || alive {
		t.Errorf("Alive(reused pid) = %v, %v; want false, nil", alive, err)
	}
}

func TestAliveIsFalseNotAnErrorForAMissingPid(t *testing.T) {
	r := New()
	h, _ := start(t, r, "sh", "-c", "exit 0")
	waitGone(t, r, h, 5*time.Second)
	// Give the reaper a moment so the pid is gone, not merely a zombie.
	time.Sleep(100 * time.Millisecond)
	alive, err := r.Alive(context.Background(), h)
	if err != nil || alive {
		t.Errorf("Alive(exited) = %v, %v; want false, nil", alive, err)
	}
	alive, err = r.Alive(context.Background(), relay.ProcHandle{})
	if err != nil || alive {
		t.Errorf("Alive(zero handle) = %v, %v; want false, nil", alive, err)
	}
}

func TestStartRefusesAMissingBinaryBeforeTouchingTheLog(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	_, err := r.Start(context.Background(), relay.ProcSpec{Dir: dir, Argv: []string{"relay-no-such-binary-t3"}, LogPath: log})
	if err == nil {
		t.Fatal("Start with a missing binary must fail")
	}
	if _, statErr := os.Stat(log); statErr == nil {
		t.Error("a refused Start must not create the log")
	}
	if _, err := r.Start(context.Background(), relay.ProcSpec{Dir: dir, LogPath: log}); err == nil {
		t.Error("Start with empty Argv must fail")
	}
	if _, err := r.Start(context.Background(), relay.ProcSpec{Dir: filepath.Join(dir, "missing"), Argv: []string{"sh", "-c", "true"}, LogPath: log}); err == nil {
		t.Error("Start with a missing Dir must fail")
	}
}

func TestExitCodeReadsOnlyATrailingRelayExitLine(t *testing.T) {
	r := New()
	dir := t.TempDir()
	cases := map[string]struct {
		body string
		code int
		ok   bool
	}{
		"trailer":            {"noise\n" + ExitTrailer + "7\n", 7, true},
		"trailer no newline": {ExitTrailer + "0", 0, true},
		"no trailer":         {"hello\nworld\n", 0, false},
		"trailer not last":   {ExitTrailer + "1\nmore output\n", 0, false},
		"garbage code":       {ExitTrailer + "x\n", 0, false},
		"empty":              {"", 0, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".log")
			if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			code, ok := r.ExitCode(context.Background(), relay.ProcHandle{}, p)
			if code != c.code || ok != c.ok {
				t.Errorf("ExitCode = %d, %v; want %d, %v", code, ok, c.code, c.ok)
			}
		})
	}
	if _, ok := r.ExitCode(context.Background(), relay.ProcHandle{}, filepath.Join(dir, "absent.log")); ok {
		t.Error("ExitCode on a missing file must be ok=false")
	}
}
