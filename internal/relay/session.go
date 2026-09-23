package relay

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
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
// refused (ok false): a session id is used in a path.
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

// builderSessionOf names the harness session that built the binding's closed
// round (#147), for the report entry: a headless builder's stream id when the
// round's stream announced one. Remote builders and any binding with no
// headless session answer nil -- never guessed.
func builderSessionOf(b store.Binding) *store.BuilderSession {
	if !b.Builder.Headless() || b.Builder.StreamSessionID == "" {
		return nil
	}
	return &store.BuilderSession{Kind: b.Builder.Kind, ID: b.Builder.StreamSessionID}
}

// roundSession names the harness session that built a closed round (#147
// part 2), read off the round's own report entry: the newest KindReport entry
// for that round carrying a BuilderSession. ok is false when the round has no
// report entry at all, or when its report names no session -- relay never
// guesses, and a resumed session must be the one that built the round.
func roundSession(entries []store.LogEntry, round int) (*store.BuilderSession, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Round == round && e.Kind == store.KindReport && e.BuilderSession != nil {
			return e.BuilderSession, true
		}
	}
	return nil, false
}
