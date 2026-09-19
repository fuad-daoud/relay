package relay

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/transcript"
)

// SessionLocator finds a pane harness's own session record for a builder,
// or reports that there is none: ok is false for a kind that keeps no
// record relay can read (agy, opencode this round), for an empty
// sessionID, and when the file is not (yet) on disk. Pure apart from the
// filesystem; cmd/relay wires HomeSessionLocator, tests wire a map.
type SessionLocator func(kind, sessionID string) (path string, ok bool)

// HomeSessionLocator locates claude's record under home:
// filepath.Glob(home/.claude/projects/*/<sessionID>.jsonl); when several
// match (a session copied between slugs) the newest by mtime wins. A
// sessionID containing a path separator or a glob metacharacter is
// refused (ok false) -- it came from herdr, but it is used in a path.
func HomeSessionLocator(home string) SessionLocator {
	return func(kind, sessionID string) (string, bool) {
		if kind != "claude" || sessionID == "" {
			return "", false
		}
		if strings.ContainsAny(sessionID, `/\*?[]`) {
			return "", false
		}
		matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
		if err != nil || len(matches) == 0 {
			return "", false
		}
		best := matches[0]
		bestTime := mtimeOf(best)
		for _, m := range matches[1:] {
			if t := mtimeOf(m); t.After(bestTime) {
				best, bestTime = m, t
			}
		}
		return best, true
	}
}

// mtimeOf is path's modification time, or the zero time when it cannot be
// stat'd -- HomeSessionLocator's newest-wins tie-break never fails a glob
// match over a stat error.
func mtimeOf(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// armSessionCursor prepares a pane builder's round log (#184): StreamRound
// = b.Round, StreamOffset = the located record's current size (0 when
// rt.Sessions is nil, the id is empty, or the file is not located), and
// LogPath = rt.Store.BuilderLogPath(b.Name, b.Round). Never fails: a stat
// error is offset 0. Not for headless or remote builders (they have
// startRound); the caller guards.
func armSessionCursor(rt Runtime, b store.Binding) store.Binding {
	var off int64
	if rt.Sessions != nil && b.Builder.SessionID != "" {
		if path, ok := rt.Sessions(b.Builder.Kind, b.Builder.SessionID); ok {
			if info, err := os.Stat(path); err == nil {
				off = info.Size()
			}
		}
	}
	b.Builder.StreamRound = b.Round
	b.Builder.StreamOffset = off
	b.Builder.LogPath = rt.Store.BuilderLogPath(b.Name, b.Round)
	return b
}

// drainSession is drainStream for a pane builder (#184): the located
// record past StreamOffset, rendered with transcript.RenderRecord, appended
// to BuilderLogPath(name, StreamRound). Unchanged binding (and no I/O)
// when rt.Sessions is nil, the builder is headless or remote, SessionID is
// "", StreamRound is 0, or the record is not located. Same cursor rules as
// drainStream: partial trailing line waits; an offset past EOF resets to 0
// with a warning; the cursor advances only after the append succeeded.
func drainSession(rt Runtime, b store.Binding) store.Binding {
	if rt.Sessions == nil || b.Builder.Headless() || b.Builder.Remote() || b.Builder.SessionID == "" || b.Builder.StreamRound == 0 {
		return b
	}
	path, ok := rt.Sessions(b.Builder.Kind, b.Builder.SessionID)
	if !ok {
		return b
	}
	round := b.Builder.StreamRound
	b.Builder.StreamOffset = drainFile(
		rt.Store.BuilderLogPath(b.Name, round),
		path,
		b.Builder.StreamOffset,
		func(line []byte) []string { return transcript.RenderRecord(b.Builder.Kind, line) },
		"session", "binding", b.Name, "round", round,
	)
	return b
}
