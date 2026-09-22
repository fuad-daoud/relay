package relay

import (
	"sort"
	"sync"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// agentCache holds the daemon's current view of herdr's agent list while a
// socket subscription is live (#146): a status event updates the matching
// agent in place, an exit/close event removes it, and a detected event marks
// the cache stale so the next tick refreshes it with a fresh ListAgents call.
// Safe for concurrent use: the socket reader (Apply) and reconnect (Replace)
// touch it from their own goroutines, while Run and Tick read it (Snapshot)
// from theirs.
type agentCache struct {
	mu     sync.Mutex
	agents []herdr.Agent
}

// Snapshot returns a copy of the cached agent list, safe for the caller to
// mutate or hand to Reconcile without racing a concurrent Apply/Replace.
func (c *agentCache) Snapshot() []herdr.Agent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]herdr.Agent, len(c.agents))
	copy(out, c.agents)
	return out
}

// Replace overwrites the cache wholesale, e.g. after a bootstrap or refresh
// ListAgents call.
func (c *agentCache) Replace(agents []herdr.Agent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agents = append([]herdr.Agent(nil), agents...)
}

// Apply folds one socket event into the cache. touchedPane is the pane id a
// tick should reconcile (its binding, if any) -- "" when the event names no
// single pane. refresh reports that the cache is stale and the caller must
// take a fresh ListAgents snapshot and Replace it in.
func (c *agentCache) Apply(ev herdr.Event) (touchedPane string, refresh bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch ev.Kind {
	case "pane_agent_status_changed":
		for i := range c.agents {
			if c.agents[i].PaneID == ev.PaneID {
				c.agents[i].Status = ev.AgentStatus
				return ev.PaneID, false
			}
		}
		// An unknown pane: the cache does not carry this agent yet, so a
		// snapshot refresh is the only way to pick it up.
		return ev.PaneID, true
	case "pane_exited", "pane_closed":
		kept := make([]herdr.Agent, 0, len(c.agents))
		for _, a := range c.agents {
			if a.PaneID != ev.PaneID {
				kept = append(kept, a)
			}
		}
		c.agents = kept
		return ev.PaneID, false
	case "pane_agent_detected":
		// A new pane appeared; the snapshot is stale everywhere, not just at
		// one pane.
		return "", true
	default:
		return "", false
	}
}

// boundPanes returns the sorted, unique planner pane ids a socket
// subscription should watch: a DONE or PAUSED binding is settled (nothing
// left to watch for), and a remote planner has no local pane.
func boundPanes(bs []store.Binding) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, b := range bs {
		if b.State == store.StateDone || b.State == store.StatePaused {
			continue
		}
		if !b.Planner.Remote() {
			add(b.Planner.PaneID)
		}
	}
	sort.Strings(out)
	return out
}

// backoffAfter returns the delay before reconnect attempt number attempt
// (1-based): 1s, 2s, 4s, 8s, 16s, doubling, capped at 30s.
func backoffAfter(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 5 { // 2^5 = 32s already exceeds the cap
		return 30 * time.Second
	}
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

// equalPanes reports whether a and b hold the same pane ids in the same
// order -- both boundPanes' own output and Daemon.subscribedPanes are always
// sorted, so a positional comparison is enough.
func equalPanes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
