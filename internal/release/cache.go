package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TTL is how long a cached answer stands before the daemon refreshes it.
const TTL = 24 * time.Hour

// cacheKey is the kv row the release cache lives in (P3b plan §1).
const cacheKey = "release-check"

// Cache is the last answer relevo got from the release endpoint. It lives in
// the machine database's kv row "release-check" (P3b plan §1), which was the
// file <state>/release-check.json: Load and Save take the db.KV and the
// legacy path, so nothing here builds an XDG path of its own (#42).
type Cache struct {
	Latest    string    `json:"latest"` // "v0.7.0"
	CheckedAt time.Time `json:"checked_at"`
	Source    string    `json:"source"` // the URL it came from
}

// Load reads the cache from the kv row "release-check", importing a present
// legacyPath file (release-check.json) on first read (P3b plan §4.3, §4.4). An
// absent row and no file is (Cache{}, false, nil) -- not an error. A malformed
// document is the same: a corrupt cache must never fail a caller, it must only
// fail to inform one. That includes a legacy file whose bytes are not JSON: the
// import refuses to store it, and Load reads the refusal as "no cache".
func Load(kv db.KV, legacyPath string) (Cache, bool, error) {
	data, ok, err := db.KVImportFile(kv, cacheKey, legacyPath)
	if err != nil {
		if errors.Is(err, db.ErrInvalid) {
			return Cache{}, false, nil
		}
		return Cache{}, false, err
	}
	if !ok {
		return Cache{}, false, nil
	}

	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, false, nil
	}
	return c, true, nil
}

// Save writes the whole cache document to the kv row "release-check".
func Save(kv db.KV, c Cache) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release cache: %w", err)
	}
	data = append(data, '\n')
	return kv.KVPut(cacheKey, data)
}

// Stale reports whether a refresh is due: no cache, or CheckedAt older
// than TTL.
func Stale(c Cache, ok bool, now time.Time, ttl time.Duration) bool {
	if !ok {
		return true
	}
	return now.Sub(c.CheckedAt) > ttl
}
