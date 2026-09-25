package relevo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// OpencodeSessionFinder finds the active OpenCode session id matching a working directory (§4).
type OpencodeSessionFinder struct {
	Exec    usage.Exec
	DBPath  string
	Timeout time.Duration
}

// Find queries OpenCode's SQLite database for session records and returns the matching session id.
func (f OpencodeSessionFinder) Find(cwd string, now time.Time) (string, error) {
	if f.Exec == nil {
		return "", fmt.Errorf("%w: nil exec", planner.ErrNoOpencodeSession)
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := f.Exec.Run(ctx, "sqlite3", "-readonly", "-json", f.DBPath, "select id, directory, parent_id, title, time_updated, time_archived from session")
	if err != nil {
		return "", fmt.Errorf("%w: %v", planner.ErrNoOpencodeSession, err)
	}

	trimmed := strings.TrimSpace(string(out))
	if len(trimmed) == 0 {
		return "", planner.ErrNoOpencodeSession
	}

	var rows []struct {
		ID           string  `json:"id"`
		Directory    string  `json:"directory"`
		ParentID     *string `json:"parent_id"`
		Title        string  `json:"title"`
		TimeUpdated  int64   `json:"time_updated"`
		TimeArchived any     `json:"time_archived"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return "", fmt.Errorf("%w: %v", planner.ErrNoOpencodeSession, err)
	}

	sessions := make([]planner.OpencodeSession, len(rows))
	for i, r := range rows {
		var parentID string
		if r.ParentID != nil {
			parentID = *r.ParentID
		}
		sessions[i] = planner.OpencodeSession{
			ID:        r.ID,
			Directory: r.Directory,
			ParentID:  parentID,
			Title:     r.Title,
			Updated:   time.UnixMilli(r.TimeUpdated),
			Archived:  r.TimeArchived != nil,
		}
	}

	return planner.MatchOpencodeSession(cwd, sessions, now)
}
