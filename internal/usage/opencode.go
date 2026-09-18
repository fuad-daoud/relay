package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type opencodeTokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

func (t opencodeTokens) tokens() Tokens {
	return Tokens{In: t.Input, CacheRead: t.Cache.Read, CacheWrite: t.Cache.Write, Out: t.Output + t.Reasoning}
}

type opencodeEvent struct {
	Type string `json:"type"`
	Part *struct {
		Type   string          `json:"type"`
		Cost   *float64        `json:"cost"`
		Tokens *opencodeTokens `json:"tokens"`
	} `json:"part"`
}

// opencodeStream reads a headless round's `run --format json` stream: one
// sample per step_finish part, dollars as opencode computed them. The
// stream names no model, so provider and model are the candidate's.
func opencodeStream(r io.Reader, provider, model string) []Sample {
	var out []Sample
	scanLines(r, func(line []byte) {
		var ev opencodeEvent
		if json.Unmarshal(line, &ev) != nil || ev.Type != "step_finish" || ev.Part == nil || ev.Part.Tokens == nil {
			return
		}
		s := Sample{Provider: provider, Model: model, Tokens: ev.Part.Tokens.tokens()}
		if ev.Part.Cost != nil {
			s.USD, s.HasCost = *ev.Part.Cost, true
		}
		out = append(out, s)
	})
	return out
}

// OpencodeQuery is the one statement relay runs against opencode.db. The
// worktree path is the only interpolated value and is quoted with '
// doubled; the window is epoch milliseconds, as message.time_created is.
func OpencodeQuery(worktree string, start, end time.Time) string {
	dir := strings.ReplaceAll(worktree, "'", "''")
	return fmt.Sprintf(
		"select m.data from message m join session s on s.id = m.session_id where s.directory = '%s' and m.time_created between %d and %d order by m.time_created",
		dir, start.UnixMilli(), end.UnixMilli())
}

// opencodeDB runs the query through sqlite3 and parses the rows. note is
// non-empty when nothing could be read and says why; a query that returns
// no rows is zero samples and an empty note.
func opencodeDB(ctx context.Context, exec Exec, dbPath, worktree string, start, end time.Time) ([]Sample, string) {
	if exec == nil {
		return nil, "sqlite3 not on PATH"
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, "no opencode store"
	}
	out, err := exec.Run(ctx, "sqlite3", "-readonly", "-json", dbPath, OpencodeQuery(worktree, start, end))
	if err != nil {
		first, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
		return nil, "sqlite3: " + first
	}
	return opencodeRows(out), ""
}

type opencodeRow struct {
	Data string `json:"data"`
}

type opencodeMessage struct {
	Role       string          `json:"role"`
	Cost       *float64        `json:"cost"`
	Tokens     *opencodeTokens `json:"tokens"`
	ModelID    string          `json:"modelID"`
	ProviderID string          `json:"providerID"`
}

// opencodeRows parses `sqlite3 -json` output: a JSON array of {"data": "<json>"}.
// Empty output (no rows) is nil, not an error.
func opencodeRows(out []byte) []Sample {
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil
	}
	var rows []opencodeRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil
	}
	var samples []Sample
	for _, r := range rows {
		var m opencodeMessage
		if json.Unmarshal([]byte(r.Data), &m) != nil || m.Role != "assistant" || m.Tokens == nil {
			continue
		}
		s := Sample{Provider: m.ProviderID, Model: m.ModelID, Tokens: m.Tokens.tokens()}
		if m.Cost != nil {
			s.USD, s.HasCost = *m.Cost, true
		}
		samples = append(samples, s)
	}
	return samples
}
