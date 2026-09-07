package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Executor handles the actual OS-level process execution for a single script.
type Executor interface {
	// Execute runs a specific script with the event data injected as environment variables.
	Execute(ctx context.Context, scriptPath string, event Event) error
}

// OSExecutor executes scripts as OS processes and logs output to a file.
type OSExecutor struct {
	LogPath string
}

// NewOSExecutor creates a new OSExecutor with the specified log file path.
func NewOSExecutor(logPath string) *OSExecutor {
	return &OSExecutor{LogPath: logPath}
}

// Execute runs a specific script with the event data injected as environment variables.
func (e *OSExecutor) Execute(ctx context.Context, scriptPath string, event Event) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Env = append(os.Environ(),
		"RELAY_EVENT="+string(event.Type),
		"RELAY_BINDING="+event.BindingID,
		"RELAY_STATE="+event.State,
		"RELAY_OLD_STATE="+event.OldState,
		"RELAY_ROUND="+strconv.Itoa(event.Round),
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
			fmt.Fprintf(f, "[%s] hook execution failed for %s: %v\n", time.Now().UTC().Format(time.RFC3339), scriptPath, err)
		}
		return err
	}

	return nil
}
