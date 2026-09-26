package chatlabel

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/usage"
)

// Resolver builds a Label for a planner record from what the record already
// stores. A zero Resolver answers with empty labels: no transcript
// path, no sqlite3.
type Resolver struct {
	Exec       usage.Exec // the sqlite3 shell-out; nil -> opencode labels are empty
	OpencodeDB string     // path to opencode.db; "" -> opencode labels are empty
	TailBytes  int64      // 0 -> DefaultTailBytes
}

// Resolve returns the harness's own name for one planner's session. Every
// failure -- a missing or unreadable file, a garbled transcript, a missing
// sqlite3, an absent database or row, or a timeout -- ends in the empty Label.
// It never returns an error.
func (r Resolver) Resolve(ctx context.Context, kind, sessionID, locator string) Label {
	switch kind {
	case "claude":
		if locator == "" {
			return Label{}
		}
		tail, err := ReadTail(locator, r.TailBytes)
		if err != nil {
			return Label{}
		}
		return Claude(tail)
	case "opencode":
		if r.Exec == nil || r.OpencodeDB == "" || !opencodeSessionID.MatchString(sessionID) {
			return Label{}
		}
		ctx, cancel := context.WithTimeout(ctx, OpencodeTimeout)
		defer cancel()
		out, err := r.Exec.Run(ctx, "sqlite3", "-readonly", r.OpencodeDB, OpencodeQuery(sessionID))
		if err != nil {
			// A pre-2.0 database has no session_v2, so the first query
			// fails; the legacy session table holds the title there.
			out, err = r.Exec.Run(ctx, "sqlite3", "-readonly", r.OpencodeDB, OpencodeLegacyQuery(sessionID))
			if err != nil {
				return Label{}
			}
		}
		return Opencode(out)
	default:
		// Every other kind -- agy included -- gets the empty label: agy's
		// conversation metadata has no verified readable title today.
		return Label{}
	}
}
