//go:build unix

package proc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestInheritedReaperReapsAnInheritedChild covers §4.6: a child started before
// the reaper is constructed is inherited, and Reap must collect it.
func TestInheritedReaperReapsAnInheritedChild(t *testing.T) {
	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid

	r := NewInheritedReaper(os.Getpid(), "/proc")
	time.Sleep(400 * time.Millisecond)

	reaped := r.Reap()
	if !containsPID(reaped, pid) {
		t.Fatalf("Reap() = %v, want it to contain the inherited pid %d", reaped, pid)
	}
	// The pid was reaped, so a second Reap must not report it again.
	if again := r.Reap(); containsPID(again, pid) {
		t.Errorf("Reap() returned %d twice: %v", pid, again)
	}
}

// TestInheritedReaperLeavesNewChildrenAlone is the safety half: a child started
// after construction is not in the inherited set, so Reap must not wait on it
// and steal its status from exec.Cmd.
func TestInheritedReaperLeavesNewChildrenAlone(t *testing.T) {
	r := NewInheritedReaper(os.Getpid(), "/proc")

	cmd := exec.Command("sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if reaped := r.Reap(); len(reaped) != 0 {
		t.Errorf("Reap() = %v, want nothing for a child started after construction", reaped)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait() after Reap: %v (the reaper must not steal a new child's status)", err)
	}
}

func containsPID(pids []int, pid int) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}
