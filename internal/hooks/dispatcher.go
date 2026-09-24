package hooks

import (
	"context"
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

// Dispatch fires an event asynchronously by running each argv list configured
// for the event type, in order, each in a detached goroutine.
func (d *LocalDispatcher) Dispatch(ctx context.Context, event Event) {
	if d == nil || d.executor == nil {
		return
	}

	for _, argv := range d.config.Hooks[string(event.Type)] {
		go d.executor.Execute(context.Background(), argv, event)
	}
}

// MultiDispatcher fans one event out to every member, e.g. a LocalDispatcher
// alongside a WebhookSink. Each member's Dispatch must itself return
// immediately (per the Dispatcher contract), so fanning out in a simple loop
// keeps that guarantee; a nil member is skipped.
type MultiDispatcher []Dispatcher

var _ Dispatcher = (MultiDispatcher)(nil)

// Dispatch calls Dispatch on every non-nil member.
func (m MultiDispatcher) Dispatch(ctx context.Context, event Event) {
	for _, d := range m {
		if d == nil {
			continue
		}
		d.Dispatch(ctx, event)
	}
}
