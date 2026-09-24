package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Executor handles the actual OS-level process execution for a single hook.
type Executor interface {
	// Execute runs one configured argv with the event data injected as
	// environment variables.
	Execute(ctx context.Context, argv []string, event Event) error
}

// OSExecutor executes argv lists as OS processes and logs output to a file.
type OSExecutor struct {
	LogPath string
}

// NewOSExecutor creates a new OSExecutor with the specified log file path.
func NewOSExecutor(logPath string) *OSExecutor {
	return &OSExecutor{LogPath: logPath}
}

// Execute runs argv[0] with argv[1:] as arguments and the event data injected
// as environment variables. An empty argv is logged and skipped.
func (e *OSExecutor) Execute(ctx context.Context, argv []string, event Event) error {
	if len(argv) == 0 {
		e.logf("hook: empty argv for %s: skipped\n", event.Type)
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

	var f *os.File
	if e.LogPath != "" {
		_ = os.MkdirAll(filepath.Dir(e.LogPath), 0755)
		var err error
		f, err = os.OpenFile(e.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			defer f.Close()
			cmd.Stdout = f
			cmd.Stderr = f
		}
	}

	err := cmd.Run()
	if err != nil {
		if f != nil {
			fmt.Fprintf(f, "[%s] hook execution failed for %s: %v\n", time.Now().UTC().Format(time.RFC3339), strings.Join(argv, " "), err)
		}
		return err
	}

	return nil
}

// logf appends one line to the executor's log file, if it has one.
func (e *OSExecutor) logf(format string, args ...any) {
	if e.LogPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(e.LogPath), 0755); err != nil {
		return
	}
	f, err := os.OpenFile(e.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] "+format, append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
}
