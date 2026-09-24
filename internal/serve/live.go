package serve

import (
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

const liveViewTTL = 2 * time.Second

type liveCacheEntry struct {
	round int
	at    time.Time
	view  *remote.LiveView
}

type liveCache struct {
	mu      sync.Mutex
	entries map[string]liveCacheEntry
}

func newLiveCache() *liveCache {
	return &liveCache{
		entries: make(map[string]liveCacheEntry),
	}
}

func (c *liveCache) get(key string, round int, now time.Time) (*remote.LiveView, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if entry.round != round {
		return nil, false
	}
	if now.Sub(entry.at) >= liveViewTTL {
		return nil, false
	}
	return entry.view, true
}

func (c *liveCache) put(key string, round int, now time.Time, v *remote.LiveView) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = liveCacheEntry{
		round: round,
		at:    now,
		view:  v,
	}
}
