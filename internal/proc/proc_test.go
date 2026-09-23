//go:build unix

package proc

import (
	"github.com/fuad-daoud/relevo/internal/usage"

	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// waitGone polls Alive until it is false or the deadline passes.
func waitGone(t *testing.T, r *Runner, h relevo.ProcHandle, within time.Duration) {
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

func start(t *testing.T, r *Runner, argv ...string) (relevo.ProcHandle, string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{Dir: dir, Argv: argv, LogPath: log, StreamPath: stream})
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
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "pwd; echo $RELEVO_T3"}, Env: []string{"RELEVO_T3=yes"}, LogPath: log, StreamPath: stream,
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
// builder before `relevo daemon` (spec 2026-09-13-headless-recovery §4.2).
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
	h := relevo.ProcHandle{PID: os.Getpid(), StartedAt: time.Unix(1_000_000, 0)}
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
	alive, err = r.Alive(context.Background(), relevo.ProcHandle{})
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
	_, err := r.Start(context.Background(), relevo.ProcSpec{Dir: dir, Argv: []string{"relevo-no-such-binary-t3"}, LogPath: log, StreamPath: stream})
	if err == nil {
		t.Fatal("Start with a missing binary must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), relevo.ProcSpec{Dir: dir, LogPath: log, StreamPath: stream}); err == nil {
		t.Error("Start with empty Argv must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), relevo.ProcSpec{Dir: filepath.Join(dir, "missing"), Argv: []string{"sh", "-c", "true"}, LogPath: log, StreamPath: stream}); err == nil {
		t.Error("Start with a missing Dir must fail")
	}
	assertNeither()
	if _, err := r.Start(context.Background(), relevo.ProcSpec{Dir: dir, Argv: []string{"sh", "-c", "true"}, LogPath: log}); err == nil {
		t.Error("Start with empty StreamPath must fail")
	}
	assertNeither()
}

func TestExitCodeReadsOnlyATrailingRelevoExitLine(t *testing.T) {
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
			code, ok := r.ExitCode(context.Background(), relevo.ProcHandle{}, p)
			if code != c.code || ok != c.ok {
				t.Errorf("ExitCode = %d, %v; want %d, %v", code, ok, c.code, c.ok)
			}
		})
	}
	if _, ok := r.ExitCode(context.Background(), relevo.ProcHandle{}, filepath.Join(dir, "absent.log")); ok {
		t.Error("ExitCode on a missing file must be ok=false")
	}
}

// The usage reader waits for the exit trailer before reading a headless
// stream (#142) and carries its own copy of the prefix so internal/usage
// stays free of the process model. This is the only place that pins the
// two equal: proc imports relevo, so the pin cannot live in relevo's tests.
func TestExitTrailerMatchesUsage(t *testing.T) {
	if usage.ExitTrailerForTest() != ExitTrailer {
		t.Fatalf("usage.exitTrailer %q != proc.ExitTrailer %q: the reader would wait out its deadline on every headless round",
			usage.ExitTrailerForTest(), ExitTrailer)
	}
}

// TestSupervisorEmitsRusageOnlyInScope pins the guard's negative half:
// /proc/self/cgroup cannot be faked in a test, so a plain spawn (this
// test's own process tree, never under a relevo-round-*.scope) must print
// no relevo-rusage: line. The positive half is verified on the box (§8 of
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
		t.Errorf("stream = %q; a plain spawn outside a relevo-round-*.scope must print no rusage line", data)
	}
	if code, ok := r.ExitCode(context.Background(), h, stream); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}
}

// TestRusageTrailerPrefixMatchesProc pins #313's duplication: relevo cannot
// import internal/proc (proc imports relevo), so relevo keeps its own copy of
// the rusage prefix and the two must stay equal.
func TestRusageTrailerPrefixMatchesProc(t *testing.T) {
	if relevo.RusageTrailerPrefix != RusageTrailer {
		t.Errorf("relevo.RusageTrailerPrefix = %q, want proc.RusageTrailer %q", relevo.RusageTrailerPrefix, RusageTrailer)
	}
}

// TestSupervisorEmitsRusageWhenUnitMatches pins the guard's positive half:
// when supervisorScript is told to want the unit its own cgroup is actually
// running in, it does emit the rusage trailer (#216). It reads its own
// cgroup rather than one it fabricates, because /proc/self/cgroup cannot be
// faked in a test; it skips where cpu.stat is not readable (CI runners and
// macOS).
func TestSupervisorEmitsRusageWhenUnitMatches(t *testing.T) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip("no /proc/self/cgroup on this platform")
	}
	line := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
	fields := strings.SplitN(line, ":", 3)
	if len(fields) != 3 {
		t.Skipf("unexpected /proc/self/cgroup line %q", line)
	}
	cg := fields[2]
	unit := path.Base(cg)
	if _, err := os.Stat("/sys/fs/cgroup" + cg + "/cpu.stat"); err != nil {
		t.Skip("cpu.stat not readable for this cgroup (CI runner or macOS)")
	}

	// #378: the matching branch now also reaps the scope, reading
	// /sys/fs/cgroup$cg/cgroup.procs and TERMing every pid but the
	// supervisor's own. Executing that here would point the fragment at this
	// test's own cgroup -- the same one the test binary runs in -- and kill
	// it. The whole two-step reap is replaced by a no-op instead: the same
	// fragment, the same branch, the same order, no live process list. The
	// fragment's own tests below run it against fake procs files.
	script := strings.Replace(supervisorScript, reapBlock, ":", 1)
	if script == supervisorScript {
		t.Fatalf("supervisorScript no longer contains %q; this test would run the real reap against the test's own cgroup", reapBlock)
	}
	out, err := exec.Command("/bin/sh", "-c", script, "relevo-supervisor", unit, "/bin/echo", "hi").Output()
	if err != nil {
		t.Fatalf("run supervisorScript: %v", err)
	}
	stream := string(out)
	if !strings.Contains(stream, "\n"+RusageTrailer+"cpu_usec=") {
		t.Errorf("stream = %q; want a %scpu_usec= line", stream, RusageTrailer)
	}
	if !strings.HasSuffix(stream, ExitTrailer+"0\n") {
		t.Errorf("stream = %q; want it to end with %s0", stream, ExitTrailer)
	}
}

// TestStartWrapsArgvWithScope exercises buildArgv, the argv builder Start
// uses, rather than executing systemd-run.
func TestStartWrapsArgvWithScope(t *testing.T) {
	spec := relevo.ProcSpec{
		Argv:  []string{"echo", "hi"},
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-abc12345-foo-3", Slice: "relevo.slice", CPUWeight: 100},
	}
	got := buildArgv(spec, "/usr/bin/echo")
	inner := []string{"/bin/sh", "-c", supervisorScript, "relevo-supervisor", ScopeUnitFileName(spec.Scope.Unit), "/usr/bin/echo", "hi"}
	want := ScopeArgv(*spec.Scope, inner)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildArgv = %v, want %v", got, want)
	}
}

// TestBuildArgvPassesTheWantedUnit pins buildArgv's contract with the
// supervisor (#216): the supervisor's first argument names the scope unit
// Start expects this process to be running in, or "" for a plain spawn, so
// supervisorScript can gate the rusage trailer on its own scope rather than
// on whatever cgroup it happens to have inherited.
func TestBuildArgvPassesTheWantedUnit(t *testing.T) {
	t.Run("scoped", func(t *testing.T) {
		spec := relevo.ProcSpec{
			Argv:  []string{"echo", "hi"},
			Scope: &relevo.ScopeSpec{Unit: "relevo-round-abc-x-1", CPUWeight: 100},
		}
		got := buildArgv(spec, "/usr/bin/echo")
		idx := indexOf(got, "relevo-supervisor")
		if idx < 0 || idx+1 >= len(got) {
			t.Fatalf("buildArgv = %v; want a \"relevo-supervisor\" element followed by the wanted unit", got)
		}
		if want := "relevo-round-abc-x-1.scope"; got[idx+1] != want {
			t.Errorf("buildArgv[after relevo-supervisor] = %q, want %q", got[idx+1], want)
		}
		if unitFlag := "--unit=relevo-round-abc-x-1.scope"; indexOf(got, unitFlag) < 0 {
			t.Errorf("buildArgv = %v; want it to contain %q", got, unitFlag)
		}
	})
	t.Run("unscoped", func(t *testing.T) {
		spec := relevo.ProcSpec{Argv: []string{"echo", "hi"}}
		got := buildArgv(spec, "/usr/bin/echo")
		idx := indexOf(got, "relevo-supervisor")
		if idx < 0 || idx+1 >= len(got) {
			t.Fatalf("buildArgv = %v; want a \"relevo-supervisor\" element followed by the wanted unit slot", got)
		}
		if got[idx+1] != "" {
			t.Errorf("buildArgv[after relevo-supervisor] = %q, want \"\" for an unscoped spec", got[idx+1])
		}
		for _, a := range got {
			if strings.Contains(a, "systemd-run") {
				t.Errorf("buildArgv = %v; an unscoped spec must have no systemd-run element", got)
			}
		}
	})
}

func indexOf(argv []string, s string) int {
	for i, a := range argv {
		if a == s {
			return i
		}
	}
	return -1
}

func TestStartStripsDeniedEnv(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "leak")
	t.Setenv("RELEVO_T4", "keep")
	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", `echo "k=${TYPESAFE_API_KEY-unset}"; echo "r=$RELEVO_T4"`}, LogPath: log, StreamPath: stream,
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

// TestStartFallsBackWhenScopeProbeFails pins §3.1's fallback (#295): the
// first Start carrying a Scope probes systemd-run (a stub here -- the real
// one does not exist in CI), the probe fails, and the spawn still happens,
// unwrapped, with the normal exit trailer and a readable exit code. The
// command really runs: it writes a line only the builder could write.
func TestStartFallsBackWhenScopeProbeFails(t *testing.T) {
	stubDir := t.TempDir()
	writeStub(t, stubDir, "#!/bin/sh\necho 'Failed to start transient scope unit: Permission denied' >&2\nexit 1\n")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "sleep 1; echo ran-unscoped"},
		LogPath: log, StreamPath: stream,
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	alive, err := r.Alive(context.Background(), h)
	if err != nil || !alive {
		t.Fatalf("Alive right after Start = %v, %v; want true, nil", alive, err)
	}
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "ran-unscoped") {
		t.Errorf("stream = %q; want the builder's own output, so the fallback really ran it", got)
	}
	if !strings.Contains(got, "\n"+ExitTrailer+"0\n") {
		t.Errorf("stream = %q; want the normal %s0 trailer", got, ExitTrailer)
	}
	code, ok := r.ExitCode(context.Background(), h, stream)
	if !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}
	if alive, err := r.Alive(context.Background(), h); err != nil || alive {
		t.Errorf("Alive after exit = %v, %v; want false, nil", alive, err)
	}
}

// TestStartProbesOnlyOnce pins §3.1's sync.Once (#295): a Runner asked for a
// scope probes exactly once, however many scoped Starts follow. The stub
// appends a line to a file for every invocation, so the count is the
// verdict.
func TestStartProbesOnlyOnce(t *testing.T) {
	stubDir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	writeStub(t, stubDir, "#!/bin/sh\necho called >> \""+calls+"\"\necho 'Failed to start transient scope unit: Permission denied' >&2\nexit 1\n")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	scope := &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100}
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		h, err := r.Start(context.Background(), relevo.ProcSpec{
			Dir: dir, Argv: []string{"sh", "-c", "true"},
			LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
			Scope: scope,
		})
		if err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		waitGone(t, r, h, 5*time.Second)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read the stub's call log: %v", err)
	}
	if got := len(strings.Fields(string(data))); got != 1 {
		t.Errorf("systemd-run stub called %d times; want exactly 1 probe for the Runner's lifetime", got)
	}
}

// TestStartWithoutScopeNeverProbes pins §3.1's laziness (#295): a Runner
// whose specs carry no Scope must never shell out to systemd-run at all.
func TestStartWithoutScopeNeverProbes(t *testing.T) {
	stubDir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	writeStub(t, stubDir, "#!/bin/sh\necho called >> \""+calls+"\"\nexit 1\n")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		h, err := r.Start(context.Background(), relevo.ProcSpec{
			Dir: dir, Argv: []string{"sh", "-c", "true"},
			LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
		})
		if err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		waitGone(t, r, h, 5*time.Second)
	}
	if data, err := os.ReadFile(calls); err == nil && strings.TrimSpace(string(data)) != "" {
		t.Errorf("systemd-run stub was called (%q); a Runner never asked for a scope must never probe", data)
	}
}

// refusePinningStub is a systemd-run that accepts a plain scope but refuses any
// invocation carrying AllowedCPUs, the shape a user manager without a
// delegated cpuset prints. It still execs the inner command for the accepted
// (unpinned) scope.
const refusePinningStub = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    AllowedCPUs=*) echo 'Failed to set AllowedCPUs: Permission denied' >&2; exit 1;;
  esac
done
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`

// TestStartDropsPinningWhenRefused pins #314's fallback: the scope probe
// accepts scopes, the pin probe is refused, and the spawn still runs -- with
// the scope and its quota but no AllowedCPUs. The command really runs: it
// writes a line only the builder could write.
func TestStartDropsPinningWhenRefused(t *testing.T) {
	stubDir := t.TempDir()
	writeStub(t, stubDir, refusePinningStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	dir := t.TempDir()
	log := filepath.Join(dir, "001-builder.log")
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "echo ran-unpinned-fallback"},
		LogPath: log, StreamPath: stream,
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "2"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "ran-unpinned-fallback") {
		t.Errorf("stream = %q; want the builder's own output, so the command still ran", got)
	}
	if !strings.Contains(got, "\n"+ExitTrailer+"0\n") {
		t.Errorf("stream = %q; want the normal %s0 trailer", got, ExitTrailer)
	}
}

// probeUnitStub is a systemd-run that records every *probe* invocation: one
// carrying an AllowedCPUs= argument under a relevo-probe-cpus- unit. It never
// records a real round spawn (unit relevo-round-*), so the count is exactly the
// number of pin probes. Everything is exec'd, so the spawns run for real.
func probeUnitStub(calls string) string {
	return `#!/bin/sh
pin=0
for a in "$@"; do
  case "$a" in
    --unit=relevo-probe-cpus-*) pin=1;;
  esac
done
[ "$pin" = 1 ] && echo pin >> "` + calls + `"
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`
}

// TestStartPinsOnlyOnce pins #314's sync.Once: a Runner asked for a pin probes
// AllowedCPUs exactly once, however many pinned Starts follow.
func TestStartPinsOnlyOnce(t *testing.T) {
	stubDir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	writeStub(t, stubDir, probeUnitStub(calls))
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	scope := &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "2"}
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		h, err := r.Start(context.Background(), relevo.ProcSpec{
			Dir: dir, Argv: []string{"sh", "-c", "true"},
			LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
			Scope: scope,
		})
		if err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		waitGone(t, r, h, 5*time.Second)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read the stub's call log: %v", err)
	}
	if got := len(strings.Fields(string(data))); got != 1 {
		t.Errorf("pinning probe ran %d times; want exactly 1 for the Runner's lifetime", got)
	}
}

// TestStartWithoutAllowedCPUsNeverProbes pins #314's laziness: a scope with no
// AllowedCPUs must never probe the pin.
func TestStartWithoutAllowedCPUsNeverProbes(t *testing.T) {
	stubDir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	writeStub(t, stubDir, probeUnitStub(calls))
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	dir := t.TempDir()
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "true"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)
	if data, err := os.ReadFile(calls); err == nil && strings.TrimSpace(string(data)) != "" {
		t.Errorf("pinning probe ran (%q); a scope with no AllowedCPUs must never probe the pin", data)
	}
}

// TestStartDoesNotMutateCallerScope pins #314's copy discipline: when the pin
// probe is refused, the fallback clears AllowedCPUs on a local copy, so the
// caller's ScopeSpec object is byte-for-byte unchanged.
func TestStartDoesNotMutateCallerScope(t *testing.T) {
	stubDir := t.TempDir()
	writeStub(t, stubDir, refusePinningStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	scope := &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "2"}
	before := *scope

	dir := t.TempDir()
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "true"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
		Scope: scope,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	if *scope != before {
		t.Errorf("caller's ScopeSpec mutated: %+v, want %+v", *scope, before)
	}
	if scope.AllowedCPUs != "2" {
		t.Errorf("caller's AllowedCPUs = %q, want it unchanged at 2", scope.AllowedCPUs)
	}
}

// acceptScopeStub is a systemd-run that accepts every invocation: it execs
// everything after "--", the way systemd-run --scope would, so both the scope
// probe and the pin probe succeed and a scoped spawn really runs.
const acceptScopeStub = `#!/bin/sh
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`

// unsetGoMaxProcs removes any GOMAXPROCS this test process inherited, so the
// value a scoped spawn adds is what the child sees. t.Setenv registers the
// restore; the Unsetenv then removes the entry for the test's duration.
func unsetGoMaxProcs(t *testing.T) {
	t.Helper()
	t.Setenv("GOMAXPROCS", "")
	os.Unsetenv("GOMAXPROCS")
}

// childEnvValue returns the value of name in a spawn's stream, whose argv ran
// `env`, and whether it was present at all; the last occurrence wins, as
// os/exec does for a duplicated name.
func childEnvValue(t *testing.T, stream, name string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	value, found := "", false
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, name+"="); ok {
			value, found = v, true
		}
	}
	return value, found
}

// childEnvValues returns every value of name in a spawn's stream, whose argv
// ran `env`, in order; a name can appear more than once when a duplicate
// leaked through, which is what the override test counts.
func childEnvValues(t *testing.T, stream, name string) []string {
	t.Helper()
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	var values []string
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, name+"="); ok {
			values = append(values, v)
		}
	}
	return values
}

// TestStartSetsGoMaxProcsFromThePin pins #315's pinned case: a scope with
// AllowedCPUs=1, its pin probe accepted, gives the child GOMAXPROCS=1.
func TestStartSetsGoMaxProcsFromThePin(t *testing.T) {
	unsetGoMaxProcs(t)
	stubDir := t.TempDir()
	writeStub(t, stubDir, acceptScopeStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "env"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "1"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	got, ok := childEnvValue(t, stream, "GOMAXPROCS")
	if !ok || got != "1" {
		t.Errorf("child GOMAXPROCS = %q, %v; want 1, true", got, ok)
	}
}

// TestStartGoMaxProcsFollowsThePinFallback pins #315's ordering: the value is
// computed after the pin fallback, so a refused pin contributes nothing and
// the quota alone gives the child GOMAXPROCS=2, never a stale 1.
func TestStartGoMaxProcsFollowsThePinFallback(t *testing.T) {
	unsetGoMaxProcs(t)
	stubDir := t.TempDir()
	writeStub(t, stubDir, refusePinningStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "env"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
		Scope: &relevo.ScopeSpec{
			Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100,
			AllowedCPUs: "1", CPUQuota: "200%",
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	got, ok := childEnvValue(t, stream, "GOMAXPROCS")
	if !ok || got != "2" {
		t.Errorf("child GOMAXPROCS = %q, %v; want 2 (the quota, after the refused pin), true", got, ok)
	}
}

// TestStartNoGoMaxProcsWithoutScopes pins #315's dropped-scope case: the scope
// probe fails, the scope falls away, and the spawn carries no GOMAXPROCS even
// though the dropped scope's quota would have asked for one.
func TestStartNoGoMaxProcsWithoutScopes(t *testing.T) {
	unsetGoMaxProcs(t)
	stubDir := t.TempDir()
	writeStub(t, stubDir, "#!/bin/sh\necho 'Failed to start transient scope unit: Permission denied' >&2\nexit 1\n")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "env"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, CPUQuota: "200%"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	if got, ok := childEnvValue(t, stream, "GOMAXPROCS"); ok {
		t.Errorf("child GOMAXPROCS = %q, true; want it unset when the scope was dropped", got)
	}
}

// TestStartScopeGoMaxProcsOverridesParent pins #315 round 2: a scope that
// limits CPUs sets the child's GOMAXPROCS even when the daemon inherited one,
// and the inherited entry is removed rather than shadowed, so the child sees
// exactly one value. Start must not append to the DeniedEnv package var.
func TestStartScopeGoMaxProcsOverridesParent(t *testing.T) {
	t.Setenv("GOMAXPROCS", "7")
	stubDir := t.TempDir()
	writeStub(t, stubDir, acceptScopeStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	deniedBefore := slices.Clone(DeniedEnv)

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "env"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "1"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	got := childEnvValues(t, stream, "GOMAXPROCS")
	if len(got) != 1 || got[0] != "1" {
		t.Errorf("child GOMAXPROCS entries = %v, want exactly [1]", got)
	}
	if !reflect.DeepEqual(DeniedEnv, deniedBefore) {
		t.Errorf("DeniedEnv = %v, want %v (Start must not append to the package var)", DeniedEnv, deniedBefore)
	}
}

// TestStartParentGoMaxProcsPassesThroughWithoutLimits pins #315 round 2's
// other half: a scope that limits nothing leaves an inherited GOMAXPROCS
// untouched, and only once.
func TestStartParentGoMaxProcsPassesThroughWithoutLimits(t *testing.T) {
	t.Setenv("GOMAXPROCS", "7")
	stubDir := t.TempDir()
	writeStub(t, stubDir, acceptScopeStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "env"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	got := childEnvValues(t, stream, "GOMAXPROCS")
	if len(got) != 1 || got[0] != "7" {
		t.Errorf("child GOMAXPROCS entries = %v, want exactly [7]", got)
	}
}

// TestStartDoesNotMutateCallerEnv pins #315's copy discipline: appending the
// GOMAXPROCS entry must not write the caller's Env backing array, even into
// its spare capacity.
func TestStartDoesNotMutateCallerEnv(t *testing.T) {
	unsetGoMaxProcs(t)
	stubDir := t.TempDir()
	writeStub(t, stubDir, acceptScopeStub)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	caller := make([]string, 1, 4)
	caller[0] = "RELEVO_T5=keep"
	before := slices.Clone(caller[:cap(caller)])

	r := New()
	dir := t.TempDir()
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"true"},
		Env:     caller,
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: filepath.Join(dir, "001-builder.jsonl"),
		Scope: &relevo.ScopeSpec{Unit: "relevo-round-local-foo-1", Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "1"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	if want := []string{"RELEVO_T5=keep"}; !reflect.DeepEqual(caller, want) {
		t.Errorf("caller's Env = %v, want %v", caller, want)
	}
	if full := caller[:cap(caller)]; !reflect.DeepEqual(full, before) {
		t.Errorf("caller's Env backing array = %v, want %v (Start must copy, not append in place)", full, before)
	}
}

// fakeProbeRunner returns a Runner whose scope probe is a counter and whose
// clock starts at a fixed instant the test can advance. It exercises
// scopesUsable, the extracted method holding the retry rule, so no
// systemd-run is needed (#370 §4.7).
func fakeProbeRunner(fail *bool) (*Runner, *int, *time.Time) {
	calls := 0
	now := time.Unix(1_700_000_000, 0)
	r := New()
	r.probe = func(context.Context, string) error {
		calls++
		if *fail {
			return errors.New("systemd-run: failed to start transient scope unit: Permission denied")
		}
		return nil
	}
	r.now = func() time.Time { return now }
	return r, &calls, &now
}

// TestScopesUsableRetriesAfterAFailure pins #370 §4.7's retry rule: a failed
// probe is not retried on an immediate Start, is retried once
// ScopeReprobeAfter has passed, and a success is sticky -- later Starts never
// probe again. Dropping the time condition makes this test's "not re-probed
// immediately" assertion fail (calls would be 2).
func TestScopesUsableRetriesAfterAFailure(t *testing.T) {
	fail := true
	r, calls, now := fakeProbeRunner(&fail)
	ctx := context.Background()
	const slice = "relevo.slice"

	if r.scopesUsable(ctx, slice) {
		t.Fatal("a failed probe must leave scopes unusable")
	}
	if *calls != 1 {
		t.Fatalf("probe calls after the first Start = %d, want 1", *calls)
	}

	// An immediate Start is inside the retry window: no probe, still unscoped.
	if r.scopesUsable(ctx, slice) {
		t.Fatal("still inside the retry window: scopes must stay unusable")
	}
	if *calls != 1 {
		t.Fatalf("probe calls after a Start inside the window = %d, want 1 (must not re-probe)", *calls)
	}

	// Advance past the window with the probe now succeeding: the next Start
	// re-probes, succeeds, and runs scoped.
	fail = false
	*now = now.Add(ScopeReprobeAfter)
	if !r.scopesUsable(ctx, slice) {
		t.Fatal("the re-probe after ScopeReprobeAfter succeeded; scopes must be usable")
	}
	if *calls != 2 {
		t.Fatalf("probe calls after the re-probe = %d, want 2", *calls)
	}

	// Success is sticky: even far in the future, later Starts never probe.
	*now = now.Add(24 * time.Hour)
	if !r.scopesUsable(ctx, slice) {
		t.Fatal("a successful probe must stay sticky")
	}
	if *calls != 2 {
		t.Fatalf("probe calls after success = %d, want 2 (success is sticky)", *calls)
	}
}

// TestScopesUsableProbesOncePerFailure pins #370 §4.7's warn-once rule through
// the probe count: a failure is taken, and warned about, once, not once per
// Start. internal/proc installs no slog handler in its tests (checked: no
// test in this package captures slog), so the count of probes -- exactly the
// number of warnings -- is what is asserted here.
func TestScopesUsableProbesOncePerFailure(t *testing.T) {
	fail := true
	r, calls, now := fakeProbeRunner(&fail)
	ctx := context.Background()
	const slice = "relevo.slice"

	// One failure, then ten Starts inside the window: one probe, one warning.
	r.scopesUsable(ctx, slice)
	for i := 0; i < 10; i++ {
		r.scopesUsable(ctx, slice)
	}
	if *calls != 1 {
		t.Fatalf("probe calls after a failure and 10 Starts in the window = %d, want 1 (one warning, not one per Start)", *calls)
	}

	// Past the window: a second failure is taken -- a second warning -- and
	// the Starts that follow it inside the new window are silent again.
	*now = now.Add(ScopeReprobeAfter)
	r.scopesUsable(ctx, slice)
	r.scopesUsable(ctx, slice)
	if *calls != 2 {
		t.Fatalf("probe calls after a second failure and one Start = %d, want 2 (one warning per failure)", *calls)
	}
}

// reapCall is the exact text supervisorScript's matching branch uses to reap
// its scope (#378): the scope's own cgroup.procs, excluding the supervisor's
// own pid. The pid is the supervisor's own $self, never $$: see reapBlock.
const reapCall = `relevo_reap_scope "/sys/fs/cgroup$cg/cgroup.procs" "$self"`

// reapBlock is the whole two-step reap that branch runs: it reads the
// supervisor's own pid with the builtin read of /proc/self/stat, then calls
// the fragment with it. The read is not decoration: a scoped spawn reaches
// the script through systemd-run, whose unit syntax rewrites a literal $$ to
// a single $, so $$ would arrive as the one-character string "$".
const reapBlock = `read -r self _ </proc/self/stat
    [ -n "$self" ] && ` + reapCall

// selfPID is the "self" pid these tests hand the fragment: a value no kernel
// will ever assign, so the pid the fragment skips is never a live one.
const selfPID = 42424242

// runReap runs ReapFragment exactly as production runs it -- sourced from the
// same const text by the same sh -- against procs, a fake cgroup.procs file.
// It returns the command's combined output and error. It never reads a real
// cgroup file: the pids such a file lists are this test's own.
func runReap(t *testing.T, procs string, self int) (string, error) {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", ReapFragment+"relevo_reap_scope '"+procs+"' "+strconv.Itoa(self)+"\n").CombinedOutput()
	return string(out), err
}

// TestReapScopeTerminatesTheListedProcesses pins #378's fragment against a
// fake procs file: every pid in the file but self gets SIGTERM, and a pid
// absent from the file is never touched. Dropping the TERM loop makes this
// test fail -- A then dies of SIGKILL at the end instead.
func TestReapScopeTerminatesTheListedProcesses(t *testing.T) {
	a := exec.Command("sleep", "60")
	if err := a.Start(); err != nil {
		t.Fatalf("start A: %v", err)
	}
	t.Cleanup(func() { _ = a.Process.Kill(); _ = a.Wait() })

	b := exec.Command("sleep", "60")
	if err := b.Start(); err != nil {
		t.Fatalf("start B: %v", err)
	}
	t.Cleanup(func() { _ = b.Process.Kill(); _ = b.Wait() })

	procs := filepath.Join(t.TempDir(), "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(a.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write procs file: %v", err)
	}

	out, err := runReap(t, procs, selfPID)
	if err != nil {
		t.Fatalf("reap: %v (output %q)", err, out)
	}
	if out != "" {
		t.Errorf("reap output = %q, want empty: the fragment prints nothing", out)
	}

	// A was listed, so it got SIGTERM; sleep has no handler and dies of it.
	err = a.Wait()
	ws, ok := a.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("A's wait status = %v (%v); want it killed by SIGTERM", a.ProcessState, err)
	}

	// B was not listed: the fragment signals only what its file lists.
	if err := b.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("B is gone (%v); want it untouched: it was not in the procs file", err)
	}
}

// TestReapScopeKillsWhatIgnoresTERM pins the KILL half and its bound: a
// process that ignores SIGTERM is gone after the call, and the whole call
// stays well inside the grace the supervisor owes the round. exec keeps the
// ignored disposition -- sh sets SIG_IGN, and an ignored signal survives
// exec -- so one process, not a sh plus its child.
func TestReapScopeKillsWhatIgnoresTERM(t *testing.T) {
	stubborn := exec.Command("sh", "-c", "trap '' TERM; exec sleep 60")
	if err := stubborn.Start(); err != nil {
		t.Fatalf("start the TERM-ignoring process: %v", err)
	}
	t.Cleanup(func() { _ = stubborn.Process.Kill(); _ = stubborn.Wait() })

	procs := filepath.Join(t.TempDir(), "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(stubborn.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write procs file: %v", err)
	}

	start := time.Now()
	out, err := runReap(t, procs, selfPID)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("reap: %v (output %q)", err, out)
	}
	if out != "" {
		t.Errorf("reap output = %q, want empty: the fragment prints nothing", out)
	}

	err = stubborn.Wait()
	ws, ok := stubborn.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Errorf("wait status = %v (%v); want SIGKILL, since TERM was ignored", stubborn.ProcessState, err)
	}
	// 15s is still far below the 60s sleep, so only the KILL fallback can explain it ending; the old 3s bound was too tight for macOS CI under -race, where forking a sleep per poll stretches 20 polls to about 3s.
	if elapsed > 15*time.Second {
		t.Errorf("reap took %s; want under about 15s (20 polls 0.1s apart)", elapsed)
	}
}

// TestReapScopeIsSilentAndZeroOnAMissingFile pins #378's error rule: the reap
// can never fail the supervisor, so a missing procs file is exit 0 with
// nothing on either stream -- sh's own "No such file" included, which is why
// the stderr redirect precedes the file open.
func TestReapScopeIsSilentAndZeroOnAMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-cgroup.procs")
	out, err := runReap(t, missing, selfPID)
	if err != nil {
		t.Errorf("reap of a missing file = %v; want exit 0", err)
	}
	if out != "" {
		t.Errorf("reap output = %q, want empty", out)
	}
}

// TestSupervisorScriptReapsInsideTheWantBranch pins #378's placement with
// string checks on the const: the fragment comes first, the reap sits after
// the rusage line and inside the */"$want" case branch, and the exit trailer
// is still the last thing the script prints. Moving the reap outside the
// case makes the position check fail.
//
// It also pins the pid source. A scoped spawn reaches this script through
// systemd-run, which parses argv with systemd's unit syntax: a literal $$
// there means one literal $, so the supervisor would receive the
// one-character string "$" as its own pid, TERM itself along with the scope
// and never print the exit trailer. The script must therefore contain no $$
// at all and must read its pid from /proc/self/stat.
func TestSupervisorScriptReapsInsideTheWantBranch(t *testing.T) {
	if !strings.HasPrefix(supervisorScript, ReapFragment) {
		t.Error("supervisorScript does not start with ReapFragment: the fragment must be defined before the script calls it")
	}
	const trailer = `printf '\nrelevo-exit:%s\n' "$rc"`
	if !strings.HasSuffix(supervisorScript, trailer) {
		t.Errorf("supervisorScript does not end with %q: the trailer must stay the stream's last line", trailer)
	}
	if strings.Contains(supervisorScript, "$$") {
		t.Error(`supervisorScript contains $$: systemd-run's unit syntax rewrites it to a single $, so a scoped supervisor would reap itself and lose the exit trailer`)
	}

	caseAt := strings.Index(supervisorScript, `case "$cg" in */"$want")`)
	rusageAt := strings.Index(supervisorScript, `printf '\nrelevo-rusage:`)
	readAt := strings.Index(supervisorScript, "read -r self _ </proc/self/stat")
	reapAt := strings.Index(supervisorScript, reapCall)
	esacAt := strings.Index(supervisorScript, "esac")
	if caseAt < 0 || rusageAt < 0 || readAt < 0 || reapAt < 0 || esacAt < 0 {
		t.Fatalf("supervisorScript lacks one of the branch, the rusage line, the self-pid read, the reap call or its esac: %q", supervisorScript)
	}
	if !(caseAt < rusageAt && rusageAt < readAt && readAt < reapAt && reapAt < esacAt) {
		t.Errorf("the read at %d and the reap at %d must sit inside the branch [%d,%d), after the rusage line at %d", readAt, reapAt, caseAt, esacAt, rusageAt)
	}
}

// TestStartDisablesFsmonitor pins #378's belt and braces through Start: a
// spawned env shows exactly one GIT_CONFIG_COUNT -- the one Start added, the
// parent's denied -- and the fsmonitor entry that count makes git read. The
// parent's count of 2 puts the new entry at index 2.
func TestStartDisablesFsmonitor(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", "env"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	if counts := childEnvValues(t, stream, "GIT_CONFIG_COUNT"); !reflect.DeepEqual(counts, []string{"3"}) {
		t.Errorf("child GIT_CONFIG_COUNT entries = %v, want exactly [3]", counts)
	}
	if got, ok := childEnvValue(t, stream, "GIT_CONFIG_KEY_2"); !ok || got != "core.fsmonitor" {
		t.Errorf("child GIT_CONFIG_KEY_2 = %q, %v; want core.fsmonitor, true", got, ok)
	}
	if got, ok := childEnvValue(t, stream, "GIT_CONFIG_VALUE_2"); !ok || got != "false" {
		t.Errorf("child GIT_CONFIG_VALUE_2 = %q, %v; want false, true", got, ok)
	}
}

// TestStartDisablesFsmonitorForGit runs the real git under the environment
// Start builds: git reads the GIT_CONFIG_* entry as config and reports
// core.fsmonitor as false, the value relevo set -- the daemon a builder's git
// commands would otherwise start is what kept a scope alive (#378).
func TestStartDisablesFsmonitorForGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	r := New()
	dir := t.TempDir()
	stream := filepath.Join(dir, "001-builder.jsonl")
	h, err := r.Start(context.Background(), relevo.ProcSpec{
		Dir: dir, Argv: []string{"git", "-C", repo, "config", "--get", "core.fsmonitor"},
		LogPath: filepath.Join(dir, "001-builder.log"), StreamPath: stream,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	var printed []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, ExitTrailer) {
			printed = append(printed, line)
		}
	}
	if !reflect.DeepEqual(printed, []string{"false"}) {
		t.Errorf("git config --get core.fsmonitor printed %v; want [false]: relevo disables the fsmonitor through GIT_CONFIG_*", printed)
	}
}
