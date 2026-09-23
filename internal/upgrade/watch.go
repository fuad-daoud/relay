package upgrade

import (
	"context"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// PreflightTimeout bounds the `--preflight` run Check makes before it refuses a
// new binary. A preflight that hangs must not wedge the daemon.
const PreflightTimeout = 30 * time.Second

// Action is what a Watcher.Check decided about the file it watches.
type Action int

const (
	// None means "nothing to do": the file is ours, or a build we refused.
	None Action = iota
	// Wait means the debounce has not yet seen one identity twice.
	Wait
	// Reexec means the new binary passed preflight and should be exec'd into.
	Reexec
	// Refused means the new binary failed preflight; it is not tried again.
	Refused
)

func (a Action) String() string {
	switch a {
	case None:
		return "none"
	case Wait:
		return "wait"
	case Reexec:
		return "reexec"
	case Refused:
		return "refused"
	}
	return "unknown"
}

// Decision is Check's answer: an action, and a reason when the action is
// Refused.
type Decision struct {
	Action Action
	Reason string
}

// Watcher decides when the file at Path has become a new binary worth
// re-exec'ing into (#371 §4.3). It is pure: its two inputs, Stat and Preflight,
// are injected, so the whole decision is testable without a file, a process or
// a clock.
type Watcher struct {
	// Path is the executable to watch.
	Path string
	// Started is the identity of the binary this image is running.
	Started store.FileID
	// Stat reads Path's current identity.
	Stat func(string) (store.FileID, error)
	// Preflight runs the candidate binary's own check; a nil error means it
	// can run.
	Preflight func(ctx context.Context, path string) error

	// pending is the identity seen once and awaiting confirmation on the next
	// check: the debounce that keeps a half-written install from being tried.
	pending *store.FileID
	// refused is the identity that failed preflight; it is never tried again.
	refused *store.FileID
}

// Check runs one decision step. It is called once per completed daemon tick,
// so the debounce counts ticks.
func (w *Watcher) Check(ctx context.Context) Decision {
	cur, err := w.Stat(w.Path)
	if err != nil {
		// The file is missing mid-install. Wait, and forget any pending
		// identity: the next check must see one twice again.
		w.pending = nil
		return Decision{Action: Wait}
	}

	if cur == w.Started {
		// The binary is back to ours, as after a rollback.
		w.pending = nil
		return Decision{Action: None}
	}

	if w.refused != nil && cur == *w.refused {
		// The same build is not tried again.
		return Decision{Action: None}
	}

	if w.pending == nil || *w.pending != cur {
		c := cur
		w.pending = &c
		return Decision{Action: Wait}
	}

	pctx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()

	if err := w.Preflight(pctx, w.Path); err != nil {
		c := cur
		w.refused = &c
		return Decision{Action: Refused, Reason: reasonText(err)}
	}

	return Decision{Action: Reexec}
}

// Refused returns the identity this watcher refused, or nil when it refused
// none. The daemon records it in daemon.json.
func (w *Watcher) Refused() *store.FileID { return w.refused }

// reasonText is the first line of a preflight failure, capped at 300 bytes:
// daemon.json is read by humans, and a full stderr dump belongs in the log.
func reasonText(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
