# Headless builders, step 3: `relay.Runner`, `internal/proc`, `fakeRunner` (#99)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #99. **Spec:** `docs/specs/2026-09-12-headless-builders-design.md`
-- read §1, §2, §3.4, §3.7, §4.1, §6 and §7 step 3 before starting; section
numbers below refer to it. Steps 1 and 2 of the spec are being built
concurrently in other worktrees and touch `internal/store` and
`internal/harness` only; this plan touches `internal/relay/runner.go` (new),
`internal/relay/herdr.go`, `internal/relay/fake_test.go`, `internal/proc`
(new) and one line of `cmd/relay/main.go`, so the three merge without
conflict.
**Depends on:** nothing open.

**Goal:** relay can start a process detached from itself, tell later whether
that exact process is still running, read the exit code it left behind, and
stop it -- through one interface a test can fake and a remote runner can
implement later.

**Architecture:** `internal/relay/runner.go` declares `Runner`, `ProcSpec`,
`ProcHandle` and `ErrRunnerUnavailable`; `Runtime` gains a `Runner` field.
`internal/proc` is the local implementation. `Start` runs a **supervisor**,
`/bin/sh -c '"$@" </dev/null; echo "relay-exit:$?"' relay-supervisor <bin>
<args...>`, in its own session (`Setsid`), with stdout and stderr both the
log file opened in append mode, and returns the supervisor's pid without
waiting on it. Liveness is pid + OS start time, read with `ps -o stat= -o
lstart= -p PID` -- one command that exists on Linux and macOS alike (CI runs
both), and the only portable way to get a start time without cgo or a
`/proc` parser. `Kill` is SIGTERM to the process group, a poll up to the
grace, then SIGKILL. `ExitCode` reads the log's last line and nothing else.
`fakeRunner` in `internal/relay/fake_test.go` records specs and kills and
answers `Alive` from a scripted per-pid sequence, which is what steps 5-7
drive their tests with.

**Decisions taken by the planner where the spec left room:**

- Start time comes from `ps`, not `/proc`: `/proc/<pid>/stat` does not
  exist on macOS and CI runs macOS. `ps -o lstart=` has one-second
  resolution, which is the tolerance §4.1 already gives.
- A zombie (`ps` state beginning `Z`) is **not alive**: it has exited and
  is waiting to be reaped, and the trailer is already in the log.
- `ExitCode` takes the handle for signature symmetry with the spec but
  reads only `logPath`.
- `ErrRunnerUnavailable` is what a later step returns when `Runtime.Runner`
  is nil (a test runtime that never set one). `cmd/relay` wires `proc.New()`
  now so step 4 does not have to.
- `exec.Command`, not `exec.CommandContext`: a cancelled context (the CLI
  exiting) must never kill a builder relay meant to leave running.

**Tech stack:** Go 1.22, standard library only (`os/exec`, `syscall`).
Verification is `make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-t3` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-t3`, cut from `main`. |
| `~/.local/state/relay/headless-t3` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself.

## Global constraints

- Files touched: `internal/relay/runner.go` (new), `internal/relay/herdr.go`
  (one field), `internal/relay/fake_test.go` (append), `internal/proc/*`
  (new), `cmd/relay/main.go` (one line + import). Nothing else.
- `internal/proc` imports `internal/relay` for the types; `internal/relay`
  never imports `internal/proc` (the cycle would not compile; `cmd/relay`
  is the wiring point).
- Every file in `internal/proc` that uses `syscall.SysProcAttr.Setsid`,
  `syscall.Kill` or `syscall.Getpgid` carries `//go:build unix`; a
  `//go:build !unix` stub keeps the tree compiling elsewhere, as
  `internal/store/lock_unsupported.go` does.
- The log is never parsed except for the trailing `relay-exit:N` line.
- No test in this plan runs `herdr`. The process tests run `sh` and `sleep`,
  which every CI runner has.
- `go.mod`/`go.sum` do not change.
- One commit per task, on the worktree's branch.

---

### Task 1: the interface

**Files:**
- Create: `internal/relay/runner.go`
- Modify: `internal/relay/herdr.go` (`Runtime.Runner`)

**Interfaces:**
- Produces (every later step and `internal/proc` use these exact names):

```go
type ProcSpec struct { Dir string; Argv []string; Env []string; LogPath string }
type ProcHandle struct { PID int; StartedAt time.Time }
type Runner interface {
	Start(ctx context.Context, spec ProcSpec) (ProcHandle, error)
	Alive(ctx context.Context, h ProcHandle) (bool, error)
	ExitCode(ctx context.Context, h ProcHandle, logPath string) (code int, ok bool)
	Kill(ctx context.Context, h ProcHandle) error
}
var ErrRunnerUnavailable error
// Runtime gains: Runner Runner
```

- [ ] **Step 1: Write `runner.go`**

Create `internal/relay/runner.go`:

```go
package relay

import (
	"context"
	"errors"
	"time"
)

// ProcSpec is one process a headless builder round runs (#99, spec §3.4).
type ProcSpec struct {
	Dir     string   // working directory: the binding's CWD
	Argv    []string // Argv[0] is the binary name, resolved on PATH by the runner
	Env     []string // additions to the parent environment; nil for none
	LogPath string   // stdout and stderr, appended, created if absent
}

// ProcHandle names a running process well enough to tell it from a later
// process that reused its pid: the pid and the start time the OS reports.
type ProcHandle struct {
	PID       int
	StartedAt time.Time
}

// Runner starts, observes and stops one process on behalf of a binding. It
// knows nothing about rounds, reports or harnesses (spec §4.1). The local
// implementation is internal/proc; a remote one would be ssh.
//
// Start returns as soon as the pid exists; the caller never waits on it.
// Alive is true iff a process with the handle's pid exists AND its start
// time matches within one second -- a reused pid is false, and a missing
// pid is (false, nil), not an error. ExitCode reports the code the runner's
// supervisor left as the log's last line, ok false when there is none (the
// process is still running, or was killed before it could write one). Kill
// stops the process group, escalating after a grace; not alive is nil.
type Runner interface {
	Start(ctx context.Context, spec ProcSpec) (ProcHandle, error)
	Alive(ctx context.Context, h ProcHandle) (bool, error)
	ExitCode(ctx context.Context, h ProcHandle, logPath string) (code int, ok bool)
	Kill(ctx context.Context, h ProcHandle) error
}

// ErrRunnerUnavailable is returned by a headless path when Runtime.Runner is
// nil: the binary was built or the runtime assembled without one.
var ErrRunnerUnavailable = errors.New("no process runner configured")
```

- [ ] **Step 2: The `Runtime` field**

In `internal/relay/herdr.go`, add to `Runtime` after the `Git Git` line:

```go
	// Runner starts and stops headless builder processes (#99). cmd/relay
	// wires proc.New(); tests wire fakeRunner. Nil means no headless path
	// can run, and reports ErrRunnerUnavailable.
	Runner Runner
```

- [ ] **Step 3: Build and commit**

Run: `go build ./... && go vet ./internal/relay`
Expected: clean.

```bash
git add internal/relay/runner.go internal/relay/herdr.go
git commit -m "feat(relay): Runner interface, ProcSpec, ProcHandle; Runtime.Runner (#99 step 3)"
```

---

### Task 2: `internal/proc` -- the local runner

**Files:**
- Create: `internal/proc/proc.go` (`//go:build unix`)
- Create: `internal/proc/proc_other.go` (`//go:build !unix`)
- Create: `internal/proc/proc_test.go` (`//go:build unix`)
- Modify: `cmd/relay/main.go` (wire `proc.New()`)

**Interfaces:**
- Consumes: `relay.ProcSpec`, `relay.ProcHandle`, `relay.Runner`, `relay.ErrRunnerUnavailable` from Task 1.
- Produces:

```go
package proc
const ExitTrailer = "relay-exit:"
const DefaultKillGrace = 5 * time.Second
type Runner struct { KillGrace time.Duration }   // zero means DefaultKillGrace
func New() *Runner
// *Runner satisfies relay.Runner
```

- [ ] **Step 1: Write the failing tests**

Create `internal/proc/proc_test.go`:

```go
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
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/proc`
Expected: `no Go files` / build failure -- the package does not exist yet.

- [ ] **Step 3: The unix implementation**

Create `internal/proc/proc.go`:

```go
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
```

Create `internal/proc/proc_other.go`:

```go
//go:build !unix

// Package proc is relay's local process Runner. relay targets Linux and
// macOS; this file exists so the tree compiles elsewhere, where every call
// reports relay.ErrRunnerUnavailable.
package proc

import (
	"context"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

const ExitTrailer = "relay-exit:"

const DefaultKillGrace = 5 * time.Second

type Runner struct {
	KillGrace time.Duration
}

var _ relay.Runner = (*Runner)(nil)

func New() *Runner { return &Runner{} }

func (r *Runner) Start(context.Context, relay.ProcSpec) (relay.ProcHandle, error) {
	return relay.ProcHandle{}, relay.ErrRunnerUnavailable
}

func (r *Runner) Alive(context.Context, relay.ProcHandle) (bool, error) {
	return false, relay.ErrRunnerUnavailable
}

func (r *Runner) ExitCode(context.Context, relay.ProcHandle, string) (int, bool) {
	return 0, false
}

func (r *Runner) Kill(context.Context, relay.ProcHandle) error {
	return relay.ErrRunnerUnavailable
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 -v ./internal/proc`
Expected: PASS, all seven. The kill test takes about the grace (2s); the
whole package should finish in under 10s. If
`TestStartCapturesBothStreamsAndTheExitTrailer` sees `err` before `out`,
stop and report -- both streams share one fd and the shell writes them in
order, so a swap means something else is wrong. If `psInfo` fails to parse,
paste the raw `ps -o stat= -o lstart= -p $$` output from this machine in
the report and stop.

- [ ] **Step 5: Wire `proc.New()` into the CLI**

In `cmd/relay/main.go`, add `"github.com/fuad-daoud/relay/internal/proc"` to
the imports and, in the `relay.Runtime{...}` literal that `newRuntime`
returns, add after the `Git:` line:

```go
		Runner:      proc.New(),
```

Keep the literal `gofmt`-aligned.

- [ ] **Step 6: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/proc cmd/relay/main.go
git commit -m "feat(proc): local Runner -- detached supervisor, ps liveness, exit trailer, kill with grace (#99 step 3)"
```

---

### Task 3: `fakeRunner`

**Files:**
- Modify: `internal/relay/fake_test.go` (append)
- Create: `internal/relay/runner_test.go`

**Interfaces:**
- Produces (steps 5-7 script their tests with these):

```go
type fakeRunner struct {
	specs    []ProcSpec     // every Start, in order
	handles  []ProcHandle   // the handle each Start returned
	kills    []ProcHandle   // every Kill of a live handle, in order
	startErr error          // when set, Start fails with it
	aliveErr error          // when set, Alive fails with it
	killErr  error          // when set, Kill fails with it
	alive    map[int][]bool // per pid: scripted Alive answers, last one repeats
	exits    map[int]int    // per pid: the code ExitCode reports; absent = ok false
	nextPID  int
}
func newFakeRunner() *fakeRunner
func (f *fakeRunner) script(pid int, answers ...bool)   // set the Alive sequence for a pid
func (f *fakeRunner) exit(pid, code int)                // set the exit code for a pid
```

- [ ] **Step 1: Write the failing test**

Create `internal/relay/runner_test.go`:

```go
package relay

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRunnerScriptsAliveAndRecordsKills(t *testing.T) {
	f := newFakeRunner()
	var _ Runner = f

	h, err := f.Start(context.Background(), ProcSpec{Dir: "/tree", Argv: []string{"agy", "-p", "x"}, LogPath: "/state/x/001-builder.log"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(f.specs) != 1 || f.specs[0].Dir != "/tree" || f.specs[0].LogPath != "/state/x/001-builder.log" {
		t.Fatalf("specs = %+v", f.specs)
	}
	if h.PID == 0 || h.StartedAt.IsZero() {
		t.Fatalf("handle = %+v; want a pid and a time", h)
	}
	// Unscripted: alive forever.
	for i := 0; i < 3; i++ {
		if alive, _ := f.Alive(context.Background(), h); !alive {
			t.Fatalf("unscripted Alive #%d = false", i)
		}
	}
	// Scripted: true, true, then false forever.
	f.script(h.PID, true, true, false)
	want := []bool{true, true, false, false}
	for i, w := range want {
		if alive, _ := f.Alive(context.Background(), h); alive != w {
			t.Errorf("scripted Alive #%d = %v, want %v", i, alive, w)
		}
	}
	// ExitCode is absent until set.
	if _, ok := f.ExitCode(context.Background(), h, ""); ok {
		t.Error("ExitCode before exit() must be ok=false")
	}
	f.exit(h.PID, 3)
	if code, ok := f.ExitCode(context.Background(), h, ""); !ok || code != 3 {
		t.Errorf("ExitCode = %d, %v; want 3, true", code, ok)
	}

	// A second Start gets a distinct pid; Kill records it and makes it dead.
	h2, _ := f.Start(context.Background(), ProcSpec{Dir: "/tree", Argv: []string{"agy"}, LogPath: "/state/x/002-builder.log"})
	if h2.PID == h.PID {
		t.Fatal("two Starts returned the same pid")
	}
	if err := f.Kill(context.Background(), h2); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if len(f.kills) != 1 || f.kills[0] != h2 {
		t.Errorf("kills = %+v, want [h2]", f.kills)
	}
	if alive, _ := f.Alive(context.Background(), h2); alive {
		t.Error("a killed handle must read as not alive")
	}

	// Errors pass through.
	f.startErr = errors.New("no binary")
	if _, err := f.Start(context.Background(), ProcSpec{Argv: []string{"x"}}); err == nil {
		t.Error("startErr not returned")
	}
	f.aliveErr = errors.New("ps refused")
	if _, err := f.Alive(context.Background(), h); err == nil {
		t.Error("aliveErr not returned")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/relay -run TestFakeRunner`
Expected: compile error, `undefined: newFakeRunner`.

- [ ] **Step 3: Implement**

Append to `internal/relay/fake_test.go`:

```go
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

	alive   map[int][]bool
	exits   map[int]int
	nextPID int
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

func (f *fakeRunner) ExitCode(_ context.Context, h ProcHandle, _ string) (int, bool) {
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
```

`context` and `time` are already imported by `fake_test.go`.

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS -- the new test and every existing one.

- [ ] **Step 5: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/relay/fake_test.go internal/relay/runner_test.go
git commit -m "test(relay): fakeRunner -- scripted Alive, recorded specs and kills (#99 step 3)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), how long `go test ./internal/proc` took, and
`git diff --stat main..HEAD`. Include the raw output of
`ps -o stat= -o lstart= -p $$` from your shell so the planner can compare it
with the parser. If any step was impossible as written, say which and why --
do not work around it.
