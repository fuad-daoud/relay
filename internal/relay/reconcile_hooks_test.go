package relay

import (
	"context"
	"sync"

	"github.com/fuad-daoud/relay/internal/hooks"
)

type recordDispatcher struct {
	mu     sync.Mutex
	events []hooks.Event
}

func (r *recordDispatcher) Dispatch(ctx context.Context, event hooks.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordDispatcher) getEvents() []hooks.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]hooks.Event, len(r.events))
	copy(cp, r.events)
	return cp
}

// TestBuilderStalledHookFiresOncePerEpisode pins #252's hook contract: the
// builder_stalled event fires exactly once when a stall is first stamped,
// never again while it persists, and never when it clears.
