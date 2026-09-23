package relay

import (
	"context"
	"errors"
	"time"
)

// ProcSpec is one process a headless builder round runs (#99, spec §3.4;
// #168 split the output).
type ProcSpec struct {
	Dir        string     // working directory: the binding's CWD
	Argv       []string   // Argv[0] is the binary name, resolved on PATH by the runner
	Env        []string   // additions to the parent environment; nil for none
	LogPath    string     // stderr, appended, created if absent
	StreamPath string     // stdout and the exit trailer, appended, created if absent
	Scope      *ScopeSpec // non-nil launches the process as a transient systemd scope (#244, #216)
}

// ScopeSpec asks the runner to start the process as a transient systemd
// scope (#244, #285). nil on ProcSpec means a plain spawn.
type ScopeSpec struct {
	Unit      string // "relay-round-<owner8>-<name>-<round>"; the runner appends ".scope"
	Slice     string // "" = omit --slice
	CPUWeight int    // >= 1; always emitted
	MemoryMax string // "" = omit
	CPUQuota  string // "" = omit; systemd units, e.g. "200%" = two cores' worth
	TasksMax  int    // 0 = omit

	// GateCPUQuota is the gate's own CPU ceiling (#313), template only. It is
	// set on Runtime.Scope from policy, and read only by scopeFor when it
	// builds a gate's spec: scopeFor copies the template, moves this value
	// into CPUQuota, and always returns a spec with GateCPUQuota zeroed. The
	// local Runner (proc.ScopeArgv) never reads it, so a spec handed to Start
	// must have it empty.
	GateCPUQuota string
}

// RusageTrailerPrefix is the prefix of the rusage line the supervisor appends
// inside a scoped spawn, right before the exit trailer (#216, #313). relay
// cannot import internal/proc (proc imports relay), so relay keeps its own
// copy of proc.RusageTrailer; internal/proc's tests pin the two equal. A
// payload tail (gate.go tailLines) skips lines carrying this prefix.
const RusageTrailerPrefix = "relay-rusage:"

// ProcRusage is what the supervisor measured for the round's cgroup.
type ProcRusage struct {
	CPUMS        int64
	PeakMemBytes int64
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
// supervisor left as the stream's last line (the caller passes the stream path),
// ok false when there is none (the process is still running, or was killed
// before it could write one). Kill stops the process group, escalating after a
// grace; not alive is nil.
type Runner interface {
	Start(ctx context.Context, spec ProcSpec) (ProcHandle, error)
	Alive(ctx context.Context, h ProcHandle) (bool, error)
	ExitCode(ctx context.Context, h ProcHandle, logPath string) (code int, ok bool)
	Kill(ctx context.Context, h ProcHandle) error
	// Rusage reports the relay-rusage: trailer the supervisor left as the
	// stream's second-to-last line; ok false when absent (plain spawn,
	// killed supervisor, still running).
	Rusage(ctx context.Context, h ProcHandle, streamPath string) (ProcRusage, bool)
}

// ErrRunnerUnavailable is returned by a headless path when Runtime.Runner is
// nil: the binary was built or the runtime assembled without one.
var ErrRunnerUnavailable = errors.New("no process runner configured")
