//go:build unix

package proc

import (
	"github.com/fuad-daoud/relay/internal/usage"

	"context"
	"os"
	"path/filepath"
	"reflect"
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

func start(t *testing.T, r *Runner, argv ...string) (relay.ProcHandle, string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relay.ProcSpec{Dir: dir, Argv: argv, LogPath: log, StreamPath: stream})
	if err != nil {
		t.Fatalf("Start(%v): %v", argv, err)
	}
	return h, log, stream
}

func TestStartCapturesBothStreamsAndTheExitTrailer(t *testing.T) {
	r := New()
	h, log, stream := start(t, r, "sh", "-c", "echo out; echo err >&2; exit 3")
	if h.PID <= 0 || h.StartedAt.IsZero() {
		t.Fatalf("handle = %+v; want a pid and a start time", h)
	}
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got, want := string(data), "out\n\n"+ExitTrailer+"3\n"; got != want {
		t.Errorf("stream = %q, want stdout, a blank line, then the trailer %q", got, want)
	}
	data, err = os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got, want := string(data), "err\n"; got != want {
		t.Errorf("log = %q, want stderr only %q", got, want)
	}
	code, ok := r.ExitCode(context.Background(), h, stream)
	if !ok || code != 3 {
		t.Errorf("ExitCode(stream) = %d, %v; want 3, true", code, ok)
	}
	if _, ok := r.ExitCode(context.Background(), h, log); ok {
		t.Error("the log carries no trailer any more; ExitCode(log) must be ok=false")
	}
}

func TestTrailerIsOnItsOwnLineAfterAPartialWrite(t *testing.T) {
	r := New()
	h, _, stream := start(t, r, "sh", "-c", "printf 'no newline'; exit 0")
	waitGone(t, r, h, 5*time.Second)
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "no newline\n"+ExitTrailer+"0\n"; got != want {
		t.Errorf("stream = %q, want %q", got, want)
	}
	if code, ok := r.ExitCode(context.Background(), h, stream); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true after a partial last line", code, ok)
	}
}

func TestStartRunsInDirWithExtraEnv(t *testing.T) {
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relay.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "pwd; echo $RELAY_T3"}, Env: []string{"RELAY_T3=yes"}, LogPath: log, StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)
	data, _ := os.ReadFile(stream)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("stream = %q; want pwd, env, blank, trailer", data)
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
	if lines[3] != ExitTrailer+"0" {
		t.Errorf("trailer = %q", lines[3])
	}
}

// The supervisor raises its own oom_score_adj before running the builder,
// and the builder inherits it, so under memory pressure the kernel takes a
// builder before `relay daemon` (spec 2026-09-13-headless-recovery §4.2).
func TestStartedProcessInheritsRaisedOOMScore(t *testing.T) {
	if _, err := os.Stat("/proc/self/oom_score_adj"); err != nil {
		t.Skip("no /proc/self/oom_score_adj on this platform")
	}
	r := New()
	h, _, stream := start(t, r, "cat", "/proc/self/oom_score_adj")
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got, want := string(data), "500\n\n"+ExitTrailer+"0\n"; got != want {
		t.Errorf("stream = %q, want %q (builder must see oom_score_adj 500)", got, want)
	}
}

func TestStartedProcessIsInItsOwnGroupAndKillReturnsWithinGrace(t *testing.T) {
	r := New()
	r.KillGrace = 2 * time.Second
	h, _, stream := start(t, r, "sleep", "60")
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
	if _, ok := r.ExitCode(context.Background(), h, stream); ok {
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
	h, _, _ := start(t, r, "sh", "-c", "exit 0")
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
	stream := filepath.Join(dir, "001-builder.jsonl")
	assertNeither := func() {
		t.Helper()
		if _, statErr := os.Stat(log); statErr == nil {
			t.Error("a refused Start must not create the log")
		}
		if _, statErr := os.Stat(stream); statErr == nil {
			t.Error("a refused Start must not create the stream")
		}
	}
	_, err := r.Start(context.Background(), relay.ProcSpec{Dir: dir, Argv: []string{"relay-no-such-binary-t3"}, LogPath: log, StreamPath: stream})
	if err == nil {
		t.Fatal("Start with a missing binary must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), relay.ProcSpec{Dir: dir, LogPath: log, StreamPath: stream}); err == nil {
		t.Error("Start with empty Argv must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), relay.ProcSpec{Dir: filepath.Join(dir, "missing"), Argv: []string{"sh", "-c", "true"}, LogPath: log, StreamPath: stream}); err == nil {
		t.Error("Start with a missing Dir must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), relay.ProcSpec{Dir: dir, Argv: []string{"sh", "-c", "true"}, LogPath: log}); err == nil {
		t.Error("Start with empty StreamPath must fail")
	}
	assertNeither()
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

// The usage reader waits for the exit trailer before reading a headless
// stream (#142) and carries its own copy of the prefix so internal/usage
// stays free of the process model. This is the only place that pins the
// two equal: proc imports relay, so the pin cannot live in relay's tests.
func TestExitTrailerMatchesUsage(t *testing.T) {
	if usage.ExitTrailerForTest() != ExitTrailer {
		t.Fatalf("usage.exitTrailer %q != proc.ExitTrailer %q: the reader would wait out its deadline on every headless round",
			usage.ExitTrailerForTest(), ExitTrailer)
	}
}

// TestSupervisorEmitsRusageOnlyInScope pins the guard's negative half:
// /proc/self/cgroup cannot be faked in a test, so a plain spawn (this
// test's own process tree, never under a relay-round-*.scope) must print
// no relay-rusage: line. The positive half is verified on the box (§8 of
// the plan).
func TestSupervisorEmitsRusageOnlyInScope(t *testing.T) {
	r := New()
	h, _, stream := start(t, r, "true")
	waitGone(t, r, h, 5*time.Second)
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if strings.Contains(string(data), RusageTrailer) {
		t.Errorf("stream = %q; a plain spawn outside a relay-round-*.scope must print no rusage line", data)
	}
	if code, ok := r.ExitCode(context.Background(), h, stream); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}
}

// TestStartWrapsArgvWithScope exercises buildArgv, the argv builder Start
// uses, rather than executing systemd-run.
func TestStartWrapsArgvWithScope(t *testing.T) {
	spec := relay.ProcSpec{
		Argv:  []string{"echo", "hi"},
		Scope: &relay.ScopeSpec{Unit: "relay-round-abc12345-foo-3", Slice: "relay.slice", CPUWeight: 100},
	}
	got := buildArgv(spec, "/usr/bin/echo")
	inner := []string{"/bin/sh", "-c", supervisorScript, "relay-supervisor", "/usr/bin/echo", "hi"}
	want := ScopeArgv(*spec.Scope, inner)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildArgv = %v, want %v", got, want)
	}
}

func TestStartStripsDeniedEnv(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "leak")
	t.Setenv("RELAY_T4", "keep")
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relay.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", `echo "k=${TYPESAFE_API_KEY-unset}"; echo "r=$RELAY_T4"`}, LogPath: log, StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "k=unset") {
		t.Errorf("stream does not contain k=unset: %q", out)
	}
	if !strings.Contains(out, "r=keep") {
		t.Errorf("stream does not contain r=keep: %q", out)
	}
}
