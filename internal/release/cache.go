package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TTL is how long a cached answer stands before the daemon refreshes it.
const TTL = 24 * time.Hour

// cacheFile is the name Load and Save use under the state root.
const cacheFile = "release-check.json"

// Cache is the last answer relay got from the release endpoint. It lives at
// <state>/release-check.json, where <state> is the root store.DefaultRoot()
// returns: Load and Save take that root, so nothing here builds an XDG path
// of its own (#42).
type Cache struct {
	Latest    string    `json:"latest"` // "v0.7.0"
	CheckedAt time.Time `json:"checked_at"`
	Source    string    `json:"source"` // the URL it came from
}

// Load reads the cache. A missing file is (Cache{}, false, nil) -- not an
// error. A malformed file is the same: a corrupt cache must never fail a
// caller, it must only fail to inform one.
func Load(root string) (Cache, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, cacheFile))
	if errors.Is(err, os.ErrNotExist) {
		return Cache{}, false, nil
	}
	if err != nil {
		return Cache{}, false, err
	}

	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, false, nil
	}
	return c, true, nil
}

// Save writes it via temp-and-rename.
func Save(root string, c Cache) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release cache: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(root, ".release-check-*.json")
	if err != nil {
		return fmt.Errorf("write release cache: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write release cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write release cache: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write release cache: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(root, cacheFile)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename release cache into place: %w", err)
	}
	return nil
}

// Stale reports whether a refresh is due: no cache, or CheckedAt older
// than TTL.
func Stale(c Cache, ok bool, now time.Time, ttl time.Duration) bool {
	if !ok {
		return true
	}
	return now.Sub(c.CheckedAt) > ttl
}
