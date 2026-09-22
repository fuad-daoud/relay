// Package history keeps the ledger's observations, kept for 30 days by
// provider and hour, so `relay policy` can show when a provider tends to be
// limited (#61 step 7); it decides nothing. The file is availability.json
// (renamed from history.json, #172 q6, so "history" is free for binding
// history); Load migrates an older install's history.json the first time it
// finds no availability.json.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relay/internal/ledger"
)

// RetainWindow is how long an event is kept before Prune drops it.
const RetainWindow = 30 * 24 * time.Hour

// Cleared is the history-only kind a manual clear records (#302). It never
// enters the ledger (ledger validation would reject it there); it exists
// so availability.json can say when a block ended, not only when it began.
const Cleared ledger.Kind = "cleared"

// Event records one ledger observation, mirrored into the 30-day window the
// ledger itself does not keep (spec §3.1).
type Event struct {
	At       time.Time   `json:"at"`
	Kind     ledger.Kind `json:"kind"`
	Provider string      `json:"provider"`
	Token    string      `json:"token,omitempty"`
	Source   string      `json:"source"`
	Binding  string      `json:"binding,omitempty"`
	Note     string      `json:"note,omitempty"`
	// Since is, on a Cleared event, the At of the oldest ledger entry the clear
	// removed: At - Since is how long the provider was blocked. Zero on every
	// other kind, and omitted from the JSON when zero.
	Since time.Time `json:"since,omitzero"`
}

// History holds an ordered collection of availability events.
type History struct {
	Events []Event `json:"events"`
}

// Load reads the availability history from disk. A missing file with no
// legacy history.json beside it returns an empty History without error, as a
// fresh install has recorded nothing yet.
//
// When path itself is missing but <dir>/history.json (the pre-#172 name)
// exists, Load migrates it in place: read the old file, write it back under
// path (the next Save would anyway), remove the old file, then return the
// events -- so the move happens on this first read and every later Load or
// Save only ever sees availability.json.
func Load(path string) (History, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return loadLegacy(path)
	}
	if err != nil {
		return History{}, fmt.Errorf("read history %s: %w", path, err)
	}

	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return History{}, fmt.Errorf("decode history %s: %w", path, err)
	}

	return h, nil
}

// loadLegacy reads the pre-#172 history.json beside path, if any, migrating
// it to path before returning.
func loadLegacy(path string) (History, error) {
	legacy := filepath.Join(filepath.Dir(path), "history.json")

	data, err := os.ReadFile(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return History{}, nil
	}
	if err != nil {
		return History{}, fmt.Errorf("read history %s: %w", legacy, err)
	}

	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return History{}, fmt.Errorf("decode history %s: %w", legacy, err)
	}

	if err := Save(path, h); err != nil {
		return History{}, err
	}
	if err := os.Remove(legacy); err != nil {
		return History{}, fmt.Errorf("remove legacy history %s: %w", legacy, err)
	}

	return h, nil
}

// Save writes the history to disk atomically via a temporary file and
// rename, so concurrent readers never observe a torn write. It creates any
// missing parent directories so callers need not ensure state root existence.
func Save(path string, h History) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}

	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal history: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write history temp file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename history into place: %w", err)
	}

	return nil
}

// Prune returns a new History containing every event no older than
// RetainWindow, in order. It does not mutate the receiver's slice.
func (h History) Prune(now time.Time) History {
	cutoff := now.Add(-RetainWindow)
	var kept []Event
	for _, e := range h.Events {
		if !e.At.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	return History{Events: append([]Event(nil), kept...)}
}

// Append returns a new History with e added to the end. It performs no
// deduplication: two observations are two events. It does not mutate the
// receiver's slice.
func (h History) Append(e Event) History {
	cp := append([]Event(nil), h.Events...)
	return History{Events: append(cp, e)}
}

// FromEntry converts a ledger entry into the history event it mirrors.
// A RateLimited entry's Subject is a provider name; a SpawnFailed entry's
// Subject is a candidate token, whose provider providerOf resolves.
func FromEntry(e ledger.Entry, providerOf func(token string) string) Event {
	ev := Event{
		At:      e.At,
		Kind:    e.Kind,
		Source:  e.Source,
		Binding: e.Binding,
		Note:    e.Note,
	}
	switch e.Kind {
	case ledger.RateLimited:
		ev.Provider = e.Subject
	case ledger.SpawnFailed:
		ev.Token = e.Subject
		ev.Provider = providerOf(e.Subject)
	}
	return ev
}

// HourCounts buckets provider's events of kind by local hour in loc,
// returning a 24-cell count indexed by hour of day. loc is a parameter so
// tests are timezone-independent; production passes time.Local.
func HourCounts(h History, provider string, kind ledger.Kind, loc *time.Location) [24]int {
	var counts [24]int
	for _, e := range h.Events {
		if e.Provider != provider || e.Kind != kind {
			continue
		}
		counts[e.At.In(loc).Hour()]++
	}
	return counts
}

// BlockedDurations returns At - Since for every Cleared event on provider
// whose Since is non-zero and not after At, in event order. Pure.
func BlockedDurations(h History, provider string) []time.Duration {
	var out []time.Duration
	for _, e := range h.Events {
		if e.Kind != Cleared || e.Provider != provider {
			continue
		}
		if e.Since.IsZero() || e.Since.After(e.At) {
			continue
		}
		out = append(out, e.At.Sub(e.Since))
	}
	return out
}
