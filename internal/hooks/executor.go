package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// hookOutputCap bounds the child output one HookRun keeps (P3b round 2 §4.4):
// the first 64 KiB of the child's combined stdout and stderr. The file this
// replaces grew without bound; past the cap nothing more is recorded.
const hookOutputCap = 64 << 10

// Executor handles the actual OS-level process execution for a single hook.
type Executor interface {
	// Execute runs one configured argv with the event data injected as
	// environment variables.
	Execute(ctx context.Context, argv []string, event Event) error
}

// OSExecutor executes argv lists as OS processes and records every run in its
// run log.
type OSExecutor struct {
	Log RunLog
}

// NewOSExecutor creates a new OSExecutor recording its runs in log.
func NewOSExecutor(log RunLog) *OSExecutor {
	return &OSExecutor{Log: log}
}

// Execute runs argv[0] with argv[1:] as arguments and the event data injected
// as environment variables, then appends one HookRun for the run -- the event,
// the argv, the exit code, the error when it failed, and the child's combined
// stdout and stderr, capped. An empty argv is recorded and skipped.
func (e *OSExecutor) Execute(ctx context.Context, argv []string, event Event) error {
	if len(argv) == 0 {
		e.appendRun(HookRun{
			At:     time.Now().UTC(),
			Event:  string(event.Type),
			Output: fmt.Sprintf("hook: empty argv for %s: skipped\n", event.Type),
		})
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(),
		"RELEVO_EVENT="+string(event.Type),
		"RELEVO_BINDING="+event.BindingID,
		"RELEVO_STATE="+event.State,
		"RELEVO_OLD_STATE="+event.OldState,
		"RELEVO_ROUND="+strconv.Itoa(event.Round),
	)

	// The child's combined output, capped, where the log file used to be.
	out := newCappedBuffer(hookOutputCap)
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()

	run := HookRun{
		At:       time.Now().UTC(),
		Event:    string(event.Type),
		Argv:     append([]string(nil), argv...),
		ExitCode: exitCodeOf(cmd),
		Output:   out.String(),
	}
	if err != nil {
		run.Error = err.Error()
	}
	e.appendRun(run)
	return err
}

// appendRun records one run. An Append error is dropped: the run log is
// best-effort by contract and must never change a hook's outcome (§6).
func (e *OSExecutor) appendRun(run HookRun) {
	if e.Log == nil {
		return
	}
	_ = e.Log.Append(run)
}

// exitCodeOf is the child's exit status, -1 when no process ran to completion
// (a start failure, or a timeout the kill left unreaped).
func exitCodeOf(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// cappedBuffer collects a child's combined output up to a limit. Writes past
// the limit are accepted and dropped, so the pipe never blocks the child.
type cappedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

// newCappedBuffer returns a buffer keeping at most limit bytes.
func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

// Write implements io.Writer. It always reports the whole slice as written,
// even when the bytes past the limit are dropped: a short write would make
// os/exec report a copy error the child never caused.
func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return n, nil
}

// String is the output collected so far.
func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
