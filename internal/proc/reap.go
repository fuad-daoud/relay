//go:build unix

package proc

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// InheritedReaper reaps the children a re-exec inherited from the previous
// image (#371 §4.6). A re-exec keeps the PID but replaces the process image,
// so the old image's children are still this process's children; nothing else
// will ever wait for them.
type InheritedReaper struct {
	pids []int
}

// NewInheritedReaper scans procRoot once for processes whose parent is self.
// It must be constructed before this image starts any child, so a child of the
// new image is never mistaken for an inherited one. procRoot is "/proc" on
// Linux; an empty or unreadable root falls back to `ps -A -o pid=,ppid=`.
func NewInheritedReaper(self int, procRoot string) *InheritedReaper {
	return &InheritedReaper{pids: scanChildren(self, procRoot)}
}

func scanChildren(self int, procRoot string) []int {
	if procRoot != "" {
		if pids, ok := scanProcRoot(self, procRoot); ok {
			return pids
		}
	}
	return scanPS(self)
}

// scanProcRoot returns (pids, true) when it could read procRoot; (nil, false)
// asks the caller to fall back to ps.
func scanProcRoot(self int, procRoot string) ([]int, bool) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, false
	}

	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue // the process exited between ReadDir and ReadFile
		}
		ppid, ok := ParseProcStatPPID(string(raw))
		if !ok || ppid != self {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, true
}

func scanPS(self int) []int {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		slog.Debug("reaper: ps", "err", err)
		return nil
	}
	return ParsePSChildren(string(out), self)
}

// Reap waits, without blocking, on every inherited pid and returns the pids it
// reaped. It never waits on a pid it did not inherit, so it cannot steal an
// exec.Cmd's status. A pid is dropped once it is reaped, or once the kernel
// says it is no longer our child.
func (r *InheritedReaper) Reap() []int {
	if r == nil {
		return nil
	}

	var reaped, kept []int
	for _, pid := range r.pids {
		if reap(pid) {
			reaped = append(reaped, pid)
		} else {
			kept = append(kept, pid)
		}
	}
	r.pids = kept
	return reaped
}

// reap reports whether pid should be dropped: true when it was reaped or is
// not ours any more, false while it is still running.
func reap(pid int) bool {
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	if wpid == pid {
		return true
	}
	if err != nil {
		// ECHILD means it was never ours, or has already been reaped.
		// Anything else is worth a debug line, but either way it is gone
		// from our list.
		if !errors.Is(err, syscall.ECHILD) {
			slog.Debug("reaper: wait4", "pid", pid, "err", err)
		}
		return true
	}
	return wpid != 0
}
