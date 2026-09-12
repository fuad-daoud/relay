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
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// ExitTrailer prefixes the one line the supervisor appends to the log when
// the builder exits: "relay-exit:<code>". It is the only thing relay ever
// reads out of a builder log.
const ExitTrailer = "relay-exit:"

// DefaultKillGrace is how long Kill waits after SIGTERM before SIGKILL.
const DefaultKillGrace = 5 * time.Second

// supervisorScript runs the builder with stdin closed and, whatever happens
// to it, appends the trailer. Plain sh: no bash-isms. "$@" is the argv the
// runner passes after the script name.
const supervisorScript = `"$@" </dev/null; echo "` + ExitTrailer + `$?"`

// Runner is the local relay.Runner.
type Runner struct {
	// KillGrace is the SIGTERM-to-SIGKILL grace; zero means DefaultKillGrace.
	KillGrace time.Duration
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

// Start launches spec under a detached supervisor and returns its handle
// without waiting. exec.Command, not CommandContext: the caller's context
// ending (a CLI exiting) must not kill a builder relay meant to leave
// running. Setsid puts the supervisor in its own session and process group,
// so it neither dies with relay's terminal nor shares a group Kill could
// hit by accident. Checks that can fail run before the log is created, so a
// refused Start leaves nothing behind.
func (r *Runner) Start(ctx context.Context, spec relay.ProcSpec) (relay.ProcHandle, error) {
	if len(spec.Argv) == 0 {
		return relay.ProcHandle{}, errors.New("proc: empty argv")
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

	argv := append([]string{"/bin/sh", "-c", supervisorScript, "relay-supervisor", bin}, spec.Argv[1:]...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stdin = nil
	cmd.Stdout = logf
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

// ExitCode reads the trailer the supervisor appended, if it is the log's
// last line. The handle is unused: the log is the record.
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

var errNoProcess = errors.New("proc: no such process")

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
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return "", false
	}
	const tail = 4096
	off := info.Size() - tail
	if off < 0 {
		off = 0
	}
	buf, err := io.ReadAll(io.NewSectionReader(f, off, info.Size()-off))
	if err != nil {
		return "", false
	}
	s := strings.TrimRight(string(buf), "\n")
	if s == "" {
		return "", false
	}
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s, true
}
