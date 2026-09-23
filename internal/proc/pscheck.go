//go:build unix

package proc

import (
	"os/exec"
	"strings"
	"syscall"
)

// psVerdict is what a failed ps says (#370): the process is really gone, or
// the failure was relay's own (the caller's context ended, or something
// killed ps) and must not be read as a dead process.
type psVerdict int

const (
	// psOK is no failure at all: exitErr was nil, so there is nothing to
	// classify.
	psOK psVerdict = iota
	// psNoProcess is ps saying "no such pid": it exited 1 with nothing on
	// stdout, which is how procps and BSD ps report a pid that is gone.
	psNoProcess
	// psTransient is anything else: the caller's context ended, ps was
	// signalled, or it failed another way. Every Alive caller already
	// treats an error as "alive this tick" (spec §4.1, §6).
	psTransient
)

// classifyPS is the decision behind psInfo, pure so it can be tested without
// a process (#370, spec §4.1). ctxErr is the context's error (nil while the
// context is live), exitErr is ps's *exec.ExitError (nil when ps ran, or
// failed to start some other way), and stdout is whatever ps printed.
//
// The rules, in order:
//   - no exec.ExitError at all -> psOK;
//   - the caller's context is done -> psTransient, because the kill was ours;
//   - the process was signalled -> psTransient, because systemd SIGTERMs the
//     ps child in the unit's cgroup during a stop;
//   - it exited 1 with trimmed stdout empty -> psNoProcess, the one shape
//     that really means "no such pid";
//   - anything else -> psTransient.
func classifyPS(ctxErr error, exitErr *exec.ExitError, stdout []byte) psVerdict {
	if exitErr == nil {
		return psOK
	}
	if ctxErr != nil {
		return psTransient
	}
	ws, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		// A platform whose wait status is not a syscall.WaitStatus cannot be
		// read as a signal: report the failure rather than guess it means a
		// dead process (#370).
		return psTransient
	}
	if ws.Signaled() {
		return psTransient
	}
	if exitErr.ExitCode() == 1 && strings.TrimSpace(string(stdout)) == "" {
		return psNoProcess
	}
	return psTransient
}
