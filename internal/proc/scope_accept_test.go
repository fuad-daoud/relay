//go:build scopeaccept && unix

// #378's local acceptance check, behind a non-default build tag because it
// needs a real systemd user manager and creates a real scope on the machine.
// CI and `make check` never run it; it is not in `go vet ./...` either.
//
//	go test -tags scopeaccept -count=1 -run TestScopeAcceptReapsItsScope -v ./internal/proc
//
// It creates its own relay-accept-* scope and touches nothing else: no
// relay.service, no scope this test did not create.
package proc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// TestScopeAcceptReapsItsScope runs one harness in a scope of its own, the
// shape a round has: the harness starts a process that outlives it
// (`setsid sleep 300` -- detached from the supervisor's process group,
// reparented to the user manager, still a member of the scope's cgroup) and
// exits 0. The supervisor must end the stream with the exit trailer, and the
// scope must be empty: not active, its straggler gone.
func TestScopeAcceptReapsItsScope(t *testing.T) {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("systemd-run is not installed")
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	unit := "relay-accept-" + hex.EncodeToString(suffix)

	dir := t.TempDir()
	stream := filepath.Join(dir, "harness.jsonl")
	// The harness runs with dir as its working directory, so its pid file is
	// relative to it.
	pidFile := filepath.Join(dir, "sleep.pid")

	// The straggler records its own pid before exec'ing sleep, and the
	// harness waits for that record before exiting, so this test knows which
	// pid must be gone and nothing is written to the stream after the
	// trailer. The pid is read with the builtin read of /proc/self/stat,
	// never $$: this harness script is an element of the supervisor's argv,
	// which systemd-run passes through its unit syntax, where $$ collapses
	// to a single $.
	const script = "setsid sh -c 'read -r p _ </proc/self/stat; echo \"$p\" > sleep.pid; exec sleep 300' &\n" +
		"while [ ! -s sleep.pid ]; do sleep 0.05; done\n" +
		"exit 0\n"

	started := time.Now()
	r := New()
	h, err := r.Start(context.Background(), relay.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", script},
		LogPath: filepath.Join(dir, "harness.log"), StreamPath: stream,
		Scope: &relay.ScopeSpec{Unit: unit, CPUWeight: 100},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Keep the machine clean if an assertion fails before the reap did its
	// job: the straggler would otherwise sleep for five minutes.
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	// 1. The stream ends with relay-exit:0 within 5 s.
	code, ok := -1, false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if code, ok = r.ExitCode(context.Background(), h, stream); ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok || code != 0 {
		data, _ := os.ReadFile(stream)
		t.Fatalf("stream after 5s = %q (code %d, ok %v); want it to end with %s0", data, code, ok, ExitTrailer)
	}
	t.Logf("stream ended with %s%d %s after the harness exit", ExitTrailer, code, time.Since(started).Round(time.Millisecond))

	// 2. The scope is no longer active within 5 s: it emptied, so --collect
	// removed it. "not active" covers inactive, failed and gone alike.
	active := ""
	for deadline := time.Now().Add(5 * time.Second); ; {
		active = scopeActive(t, ScopeUnitFileName(unit))
		if active != "active" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if active == "active" {
		t.Errorf("systemctl --user is-active %s = %q; want the scope not active after the harness exited", ScopeUnitFileName(unit), active)
	}

	// 3. The sleep the harness left behind is gone within 5 s.
	pid := readSleepPID(t, pidFile)
	gone := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			gone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gone {
		t.Errorf("sleep pid %d is still there; want the supervisor to have reaped it", pid)
	}

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	t.Logf("acceptance: %s is %q after the round, sleep pid %d is gone; whole check took %s\nstream: %q",
		ScopeUnitFileName(unit), active, pid, time.Since(started).Round(time.Millisecond), data)
}

// scopeActive returns what `systemctl --user is-active` reports for the unit,
// or a description of the failure when systemctl itself could not run.
func scopeActive(t *testing.T, unit string) string {
	t.Helper()
	out, err := exec.Command("systemctl", "--user", "is-active", unit).Output()
	if err != nil && len(out) == 0 {
		return "unknown (" + err.Error() + ")"
	}
	return strings.TrimSpace(string(out))
}

// readSleepPID reads the pid the harness's straggler recorded.
func readSleepPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the harness's pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("pid file %q: %v", data, err)
	}
	return pid
}
