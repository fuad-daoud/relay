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
