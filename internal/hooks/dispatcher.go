package hooks

import (
	"context"
	"os"
	"path/filepath"
)

// Dispatcher is responsible for routing events to registered executables.
type Dispatcher interface {
	// Dispatch fires an event asynchronously. It must return immediately and never block.
	Dispatch(ctx context.Context, event Event)
}

// LocalDispatcher routes events to local executable hook scripts.
type LocalDispatcher struct {
	config   Config
	executor Executor
}

// NewLocalDispatcher creates a new LocalDispatcher with the given configuration and executor.
func NewLocalDispatcher(config Config, executor Executor) *LocalDispatcher {
	return &LocalDispatcher{
		config:   config,
		executor: executor,
	}
}

// Dispatch fires an event asynchronously by finding executable scripts in the hook directory
// corresponding to the event type and executing each one in a detached goroutine.
func (d *LocalDispatcher) Dispatch(ctx context.Context, event Event) {
	if d == nil || d.executor == nil || d.config.HooksDir == "" {
		return
	}

	dir := filepath.Join(d.config.HooksDir, string(event.Type)+".d")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&0111 == 0 {
			continue
		}
		scriptPath := filepath.Join(dir, entry.Name())
		go d.executor.Execute(context.Background(), scriptPath, event)
	}
}
