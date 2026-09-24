// Package latency keeps, for 30 days, how long each candidate took to
// produce its first model output, so a listing can show what a candidate
// costs to start before relevo moves a provider or a region (#324 part 1).
// It is a record, not a policy: nothing here decides which candidate relevo
// runs, and nothing loads it on a spawn path.
package latency

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// RetainWindow is how long a sample is kept before Prune drops it, matching
// history.RetainWindow so both records age out together.
const RetainWindow = 30 * 24 * time.Hour

// Sample records one probe: how long a candidate took to produce its first
// model output, and how long the whole run took. Err is "" on success; a
// sample counts as successful only then.
type Sample struct {
	At      time.Time `json:"at"`
	Token   string    `json:"token"`
	Host    string    `json:"host"`
	TTFTMS  int64     `json:"ttft_ms"`
	TotalMS int64     `json:"total_ms"`
	Err     string    `json:"err,omitempty"`
}

// History holds every sample in the 30-day window, oldest first.
type History struct {
	Samples []Sample `json:"samples"`
}

// Summary is one token's window: how many probes succeeded, how many failed,
// and the p50 time to first output over the successful ones.
type Summary struct {
	N         int
	Errors    int
	TTFTP50MS int64
}

// latencyKey is the kv row the latency document lives in (P3b plan §1).
const latencyKey = "latency"

// LoadKV reads the latency history from the kv row "latency", importing a
// present legacyPath file (latency.json) on first read (P3b plan §4.3, §4.4).
// An absent row with no file behind it is an empty History and no error: a
// fresh install has probed nothing yet. Invalid JSON is an error, so a torn or
// hand-edited document is reported rather than read as empty.
func LoadKV(kv db.KV, legacyPath string) (History, error) {
	data, ok, err := db.KVImportFile(kv, latencyKey, legacyPath)
	if err != nil {
		return History{}, err
	}
	if !ok {
		return History{}, nil
	}

	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return History{}, fmt.Errorf("decode latency: %w", err)
	}

	return h, nil
}

// SaveKV writes the whole history document to the kv row "latency".
func SaveKV(kv db.KV, h History) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal latency: %w", err)
	}
	return kv.KVPut(latencyKey, data)
}

// Prune returns a new History containing every sample no older than
// RetainWindow, in order. It does not mutate the receiver's slice.
func (h History) Prune(now time.Time) History {
	cutoff := now.Add(-RetainWindow)
	var kept []Sample
	for _, s := range h.Samples {
		if !s.At.Before(cutoff) {
			kept = append(kept, s)
		}
	}
	return History{Samples: append([]Sample(nil), kept...)}
}

// Append returns a new History with s added to the end. It performs no
// deduplication and does not mutate the receiver's slice.
func (h History) Append(s Sample) History {
	cp := append([]Sample(nil), h.Samples...)
	return History{Samples: append(cp, s)}
}

// Summary summarises token over every sample in h. The caller prunes first,
// so the window is the caller's choice. Errored samples count in Errors and
// are excluded from TTFTP50MS; with no successful sample the p50 is 0.
func (h History) Summary(token string) Summary {
	ttfts := make([]int64, 0, len(h.Samples))
	var sum Summary
	for _, s := range h.Samples {
		if s.Token != token {
			continue
		}
		if s.Err != "" {
			sum.Errors++
			continue
		}
		sum.N++
		ttfts = append(ttfts, s.TTFTMS)
	}

	if len(ttfts) == 0 {
		return sum
	}
	sort.Slice(ttfts, func(i, j int) bool { return ttfts[i] < ttfts[j] })
	sum.TTFTP50MS = ttfts[(len(ttfts)-1)/2] // lower median

	return sum
}
