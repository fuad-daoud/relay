package planner

// State is a planner record's host state: the `state` column `relay planner
// list` renders, and what `relay planner prune` reads (§4.7). It is derived,
// never stored -- a record carries host_pid and host_started_at, and whether
// that exact process is still there is a question for the machine, not for
// the file.
type State string

const (
	// StateLive means a process with the record's host_pid is running and
	// started at the record's host_started_at.
	StateLive State = "live"
	// StateGone means the record has a host_pid, but that exact process is no
	// longer there. A reused pid is gone, not live: the start time is what
	// makes a pid an identity (§4.1).
	StateGone State = "gone"
	// StateExplicit means the record is an explicit registration with no host
	// process to check (host_pid == 0).
	StateExplicit State = "-"
)

// RecordState reports rec's host state: live when the record's host process
// (pid and start time together) exists, gone otherwise, and "-" for an
// explicit registration.
//
// procStart is the same process-start read planner.Resolve's host step uses
// (ResolveInput.ProcStart), injected so the caller decides how processes are
// read and tests never touch a real one. A nil procStart, or an error from
// it, means the host cannot be matched, so a host-bearing record is StateGone
// -- the same "no host match" an error means to Resolve, not a failure here.
func RecordState(rec Record, procStart func(pid int) (int64, error)) State {
	if rec.HostPID <= 0 {
		return StateExplicit
	}
	if procStart == nil {
		return StateGone
	}
	startedAt, err := procStart(rec.HostPID)
	if err != nil {
		return StateGone
	}
	if startedAt != rec.HostStartedAt {
		return StateGone
	}
	return StateLive
}
