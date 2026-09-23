//go:build unix

// Package proc is relay's local process Runner (#99): it starts a headless
// builder detached from relay, tells later whether that exact process is
// still running, reads the exit code its supervisor left in the log, and
// stops it. It knows nothing about rounds or harnesses.
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// ExitTrailer prefixes the one line the supervisor appends to the stream when
// the builder exits: "relay-exit:<code>". It is the only thing relay ever
// reads out of a builder stream.
const ExitTrailer = "relay-exit:"

// DefaultKillGrace is how long Kill waits after SIGTERM before SIGKILL.
const DefaultKillGrace = 5 * time.Second

// supervisorScript runs the builder with stdin closed and, whatever happens
// to it, appends the trailer. Plain sh: no bash-isms. "$@" is the argv the
// runner passes after the script name.
//
// Before the builder starts, the supervisor raises its own oom_score_adj;
// the builder inherits it. Under memory pressure the kernel then prefers a
// builder over `relay daemon` (#120). The write fails silently where there
// is no /proc (macOS) or it is refused, and the builder runs as before.
// The redirection sits inside a group so the shell's own "No such file"
// for a missing /proc is silenced too, not only echo's stderr: sh applies
// redirections left to right, and the open fails before 2>/dev/null.
// The builder stays a child of this sh (no exec) so an OOM kill of the
// builder still leaves a trailer. A group kill from Kill takes the sh with
// it and leaves none; relay writes its own marker line for every process it
// stops (relay.appendLogMarker).
// The trailer is printed with a leading newline so a builder that died
// mid-line leaves it on a line of its own; the blank line before it is
// rendered as nothing (transcript rule 1). It goes to stdout -- the stream
// file -- so the stream is the complete raw record and ExitCode reads one file.
//
// When Start wrapped this script in a systemd scope (#244, #216), the
// supervisor also reads its own cgroup's cpu.stat and memory.peak after the
// builder exits and prints a relay-rusage: line before the exit trailer.
// want is buildArgv's first argument after "relay-supervisor": the scope
// unit file name Start expects this process to be running in, or "" for a
// plain spawn. The guard is on want, not merely on the inherited cgroup
// matching a relay-round-*.scope shape (#216): a process spawned inside a
// round's own scope -- relay's test suite, run on a scoped server, is
// exactly this case -- inherits that cgroup too, so matching the shape
// alone would make a plain spawn started from inside a round wrongly emit
// a rusage line for the round's cgroup, not its own.
const supervisorScript = `want=$1
shift
{ echo 500 >/proc/self/oom_score_adj; } 2>/dev/null || true
"$@" </dev/null; rc=$?
if [ -n "$want" ]; then
  cg=$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -1)
  case "$cg" in */"$want")
    u=$(awk '/^usage_usec/{print $2}' "/sys/fs/cgroup$cg/cpu.stat" 2>/dev/null)
    m=$(cat "/sys/fs/cgroup$cg/memory.peak" 2>/dev/null)
    printf '\nrelay-rusage:%s%s\n' "${u:+cpu_usec=$u}" "${m:+ mem_peak=$m}"
    ;;
  esac
fi
printf '\nrelay-exit:%s\n' "$rc"`

// Runner is the local relay.Runner.
type Runner struct {
	// KillGrace is the SIGTERM-to-SIGKILL grace; zero means DefaultKillGrace.
	KillGrace time.Duration

	// probeOnce guards the lazy scope probe (#295): the first Start with a
	// scoped spec probes systemd-run, and every later Start reuses that
	// verdict. A Runner that is never asked for a scope never probes.
	probeOnce sync.Once
	// scopesOK is the verdict of that one probe: true means scopes work and
	// scoped specs are launched as scopes, false means every later Start
	// drops the scope and spawns plainly.
	scopesOK bool

	// pinOnce guards the lazy pin probe (#314): the first Start whose scope
	// carries an AllowedCPUs pool probes systemd-run with that property, and
	// every later Start reuses that verdict. A Runner never asked for a pin
	// never probes.
	pinOnce sync.Once
	// pinOK is the verdict of that one probe: true means AllowedCPUs is
	// accepted and specs keep it, false means every later Start drops the pin
	// while keeping the scope and its quota.
	pinOK bool
}

var _ relay.Runner = (*Runner)(nil)

// New returns a Runner with the default grace.
func New() *Runner { return &Runner{} }

func (r *Runner) grace() time.Duration {
	if r.KillGrace > 0 {
		return r.KillGrace
	}
	return DefaultKillGrace
}

// buildArgv builds the argv Start execs: bin run under supervisorScript,
// wrapped in a systemd scope when spec.Scope is set (#244, #216). The
// supervisor's first argument is the scope unit file name Start expects to
// be running in (or "" for a plain spawn), so it can tell its own round's
// scope from one it merely inherited (#216).
func buildArgv(spec relay.ProcSpec, bin string) []string {
	var want string
	if spec.Scope != nil {
		want = ScopeUnitFileName(spec.Scope.Unit)
	}
	inner := append([]string{"/bin/sh", "-c", supervisorScript, "relay-supervisor", want, bin}, spec.Argv[1:]...)
	if spec.Scope != nil {
		return ScopeArgv(*spec.Scope, inner)
	}
	return inner
}

// Start launches spec under a detached supervisor and returns its handle
// without waiting. exec.Command, not CommandContext: the caller's context
// ending (a CLI exiting) must not kill a builder relay meant to leave
// running. Setsid puts the supervisor in its own session and process group,
// so it neither dies with relay's terminal nor shares a group Kill could
// hit by accident. Checks that can fail run before either file is created, so a
// refused Start leaves nothing behind.
func (r *Runner) Start(ctx context.Context, spec relay.ProcSpec) (relay.ProcHandle, error) {
	if len(spec.Argv) == 0 {
		return relay.ProcHandle{}, errors.New("proc: empty argv")
	}
	if spec.StreamPath == "" {
		return relay.ProcHandle{}, errors.New("proc: empty stream path")
	}
	info, err := os.Stat(spec.Dir)
	if err != nil {
		return relay.ProcHandle{}, fmt.Errorf("proc: dir: %w", err)
	}
	if !info.IsDir() {
		return relay.ProcHandle{}, fmt.Errorf("proc: %s is not a directory", spec.Dir)
	}
	bin, err := exec.LookPath(spec.Argv[0])
	if err != nil {
		return relay.ProcHandle{}, fmt.Errorf("proc: %w", err)
	}
	logf, err := os.OpenFile(spec.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return relay.ProcHandle{}, fmt.Errorf("proc: log: %w", err)
	}
	defer logf.Close()
	streamf, err := os.OpenFile(spec.StreamPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return relay.ProcHandle{}, fmt.Errorf("proc: stream: %w", err)
	}
	defer streamf.Close()

	// The scope probe is lazy and runs at most once per Runner (#295): a
	// local CLI verb has no eager startup probe (cmd/relay/serve.go's served
	// path keeps its own), so the verdict is taken here on the first scoped
	// Start. A host where systemd-run is missing or refuses gets one warning
	// and unscoped builders, exactly as before. Start took spec by value, so
	// clearing Scope here never mutates the caller's struct.
	if spec.Scope != nil {
		r.probeOnce.Do(func() {
			if err := ProbeScopes(ctx, spec.Scope.Slice); err != nil {
				slog.Warn("scopes unavailable; builders will run in this process's cgroup", "err", err)
				r.scopesOK = false
				return
			}
			r.scopesOK = true
		})
		if !r.scopesOK {
			spec.Scope = nil // local copy; the caller's spec is not mutated
		}
	}

	// The pin probe is lazy and runs at most once per Runner (#314), like the
	// scope probe above: a user manager that refuses AllowedCPUs gets one
	// warning and scopes without pinning, and a round never fails because of
	// pinning. The fallback clears the field on a dereference copy and
	// re-points the local field: Start took spec by value, but Scope is a
	// pointer, so a write through spec.Scope would mutate the caller's spec.
	if spec.Scope != nil && spec.Scope.AllowedCPUs != "" {
		r.pinOnce.Do(func() {
			if err := ProbeAllowedCPUs(ctx, spec.Scope.Slice, spec.Scope.AllowedCPUs); err != nil {
				slog.Warn("cpu pinning unavailable; scopes will run without AllowedCPUs", "allowed_cpus", spec.Scope.AllowedCPUs, "err", err)
				r.pinOK = false
				return
			}
			r.pinOK = true
		})
		if !r.pinOK {
			sc := *spec.Scope
			sc.AllowedCPUs = ""
			spec.Scope = &sc
		}
	}

	// A scoped spawn gets GOMAXPROCS sized to the CPUs its scope allows
	// (#315). It runs after both fallbacks so it follows the scope actually
	// launched: a refused pin contributes nothing, and a dropped scope adds
	// nothing at all. The full-slice expression forces a copy, so the append
	// never writes the caller's backing array -- spec is a value copy, but
	// spec.Env shares the caller's array.
	spec.Env = append(spec.Env[:len(spec.Env):len(spec.Env)], goMaxProcsEnv(os.Environ(), spec.Env, spec.Scope)...)

	argv := buildArgv(spec, bin)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = ChildEnv(os.Environ(), DeniedEnv, spec.Env)
	cmd.Stdin = nil
	cmd.Stdout = streamf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return relay.ProcHandle{}, fmt.Errorf("proc: start: %w", err)
	}
	pid := cmd.Process.Pid
	// Reap the supervisor when it exits, if this process is still around
	// to do it (the daemon is). A short-lived CLI exits first and init
	// reaps instead. Nobody blocks on this.
	go func() { _ = cmd.Wait() }()

	started, _, err := psInfo(ctx, pid)
	if err != nil {
		// The supervisor may already have finished (a trivial argv) or ps
		// may be unhappy; the handle still needs a time. Now is within the
		// one-second tolerance of a process started a moment ago.
		started = time.Now()
	}
	return relay.ProcHandle{PID: pid, StartedAt: started.Truncate(time.Second)}, nil
}

// Alive reports whether the handle's process exists, is not a zombie, and
// started when the handle says it did (within one second). A missing pid is
// (false, nil); only ps itself failing to run is an error.
func (r *Runner) Alive(ctx context.Context, h relay.ProcHandle) (bool, error) {
	if h.PID <= 0 {
		return false, nil
	}
	started, state, err := psInfo(ctx, h.PID)
	if errors.Is(err, errNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.HasPrefix(state, "Z") {
		return false, nil
	}
	diff := started.Sub(h.StartedAt)
	if diff < 0 {
		diff = -diff
	}
	return diff <= time.Second, nil
}

// ExitCode reads the trailer the supervisor appended, if it is the stream's
// last line. The handle is unused: the stream is the record.
func (r *Runner) ExitCode(_ context.Context, _ relay.ProcHandle, logPath string) (int, bool) {
	line, ok := lastLine(logPath)
	if !ok || !strings.HasPrefix(line, ExitTrailer) {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimPrefix(line, ExitTrailer))
	if err != nil {
		return 0, false
	}
	return code, true
}

// Kill sends SIGTERM to the supervisor's process group -- the supervisor and
// the builder under it -- waits up to the grace for Alive to turn false,
// then SIGKILLs the group. Alive's start-time check runs first, so a reused
// pid is never signalled. Not alive is nil.
func (r *Runner) Kill(ctx context.Context, h relay.ProcHandle) error {
	alive, err := r.Alive(ctx, h)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	if err := syscall.Kill(-h.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("proc: SIGTERM %d: %w", h.PID, err)
	}
	deadline := time.Now().Add(r.grace())
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		alive, err := r.Alive(ctx, h)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
	}
	if err := syscall.Kill(-h.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("proc: SIGKILL %d: %w", h.PID, err)
	}
	return nil
}

// Rusage scans the last few lines of streamPath, from last to first, for the
// relay-rusage: trailer; ok is false when none of those lines match (plain
// spawn, killed supervisor, still running). The scan -- rather than assuming
// a fixed offset -- is needed because supervisorScript's printf leaves a
// blank line between the rusage and exit trailers, so the trailer is not
// reliably the second-to-last line. The handle is unused: the stream is the
// record, as for ExitCode.
func (r *Runner) Rusage(_ context.Context, _ relay.ProcHandle, streamPath string) (relay.ProcRusage, bool) {
	lines, ok := lastLines(streamPath, 6)
	if !ok {
		return relay.ProcRusage{}, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], RusageTrailer) {
			return ParseRusageTrailer(lines[i])
		}
	}
	return relay.ProcRusage{}, false
}

var errNoProcess = errors.New("proc: no such process")

// StartTime reports when a process started, in the OS's own resolution, via the
// same `ps -o lstart=` read psInfo uses (and Endpoint.StartedAt records). It is
// exported for `relay planner init`, which needs the harness process's start
// time to defend a planner record against pid reuse exactly as a binding's
// endpoint does (#303 §3.1). A missing pid is an error, not the zero time: the
// caller decides what a host it cannot measure means.
func StartTime(ctx context.Context, pid int) (time.Time, error) {
	started, _, err := psInfo(ctx, pid)
	if err != nil {
		return time.Time{}, err
	}
	return started, nil
}

// psLayout is what `ps -o lstart=` prints on Linux (procps) and macOS:
// "Sat Sep 12 16:35:34 2026". The day may be space-padded; _2 accepts both.
const psLayout = "Mon Jan _2 15:04:05 2006"

// psInfo asks ps for one process's start time and state. ps is the one
// portable source of a start time: /proc is Linux-only and sysctl needs
// cgo. ps exits non-zero when the pid does not exist, which is
// errNoProcess; any other failure is returned as is.
func psInfo(ctx context.Context, pid int) (started time.Time, state string, err error) {
	out, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return time.Time{}, "", errNoProcess
		}
		return time.Time{}, "", fmt.Errorf("proc: ps: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return time.Time{}, "", errNoProcess
	}
	if len(fields) < 6 {
		return time.Time{}, "", fmt.Errorf("proc: unexpected ps output %q", strings.TrimSpace(string(out)))
	}
	started, err = time.ParseInLocation(psLayout, strings.Join(fields[1:6], " "), time.Local)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("proc: parse ps start time %q: %w", strings.Join(fields[1:6], " "), err)
	}
	return started, fields[0], nil
}

// lastLine returns the final line of the file (ignoring trailing newlines),
// reading only its tail. ok is false for a missing or empty file.
func lastLine(path string) (string, bool) {
	lines, ok := lastLines(path, 1)
	if !ok {
		return "", false
	}
	return lines[len(lines)-1], true
}

// lastLines returns up to the final n non-empty lines of the file, oldest
// first (ignoring trailing newlines), reading only its tail. ok is false
// for a missing or empty file; a file with fewer than n lines in its tail
// returns as many as were read.
func lastLines(path string, n int) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return nil, false
	}
	const tail = 4096
	off := info.Size() - tail
	if off < 0 {
		off = 0
	}
	buf, err := io.ReadAll(io.NewSectionReader(f, off, info.Size()-off))
	if err != nil {
		return nil, false
	}
	s := strings.TrimRight(string(buf), "\n")
	if s == "" {
		return nil, false
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, true
}
