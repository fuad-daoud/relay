// The daemon pointer lives in the default serve root because an admin's shell
// has no way to know the unit's --state directory.
package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const PointerFileName = "daemon.json"

var initialisedMarkers = []string{"clients.json", "server.key", "bindings"}

// DaemonPointer identifies the state root of a running serve daemon.
type DaemonPointer struct {
	Root      string    `json:"root"`
	PID       int       `json:"pid"`
	Listen    string    `json:"listen"`
	StartedAt time.Time `json:"started_at"`
}

// WritePointer atomically records the running daemon in the default serve root.
func WritePointer(defaultRoot string, p DaemonPointer) error {
	if err := os.MkdirAll(defaultRoot, 0o755); err != nil {
		return fmt.Errorf("write daemon pointer: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("write daemon pointer: %w", err)
	}
	tmpPath := filepath.Join(defaultRoot, PointerFileName+".tmp")
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write daemon pointer: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(defaultRoot, PointerFileName)); err != nil {
		return fmt.Errorf("write daemon pointer: %w", err)
	}
	return nil
}

// ReadPointer reads the daemon pointer, if one exists.
func ReadPointer(defaultRoot string) (p DaemonPointer, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(defaultRoot, PointerFileName))
	if errors.Is(err, os.ErrNotExist) {
		return DaemonPointer{}, false, nil
	}
	if err != nil {
		return DaemonPointer{}, false, fmt.Errorf("read daemon pointer: %w", err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return DaemonPointer{}, false, fmt.Errorf("read daemon pointer: %w", err)
	}
	return p, true, nil
}

// RemovePointer removes the daemon pointer, if it exists.
func RemovePointer(defaultRoot string) error {
	err := os.Remove(filepath.Join(defaultRoot, PointerFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Initialised reports whether root has any serve state marker.
func Initialised(root string) (bool, error) {
	for _, marker := range initialisedMarkers {
		info, err := os.Stat(filepath.Join(root, marker))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if marker == "bindings" {
			if info.IsDir() {
				return true, nil
			}
			continue
		}
		if !info.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// ResolveAdminRoot resolves the root an administrative serve command should use.
func ResolveAdminRoot(explicitState, defaultRoot string, alive func(pid int) bool) (root string, note string, err error) {
	if explicitState != "" {
		return filepath.Join(explicitState, "serve"), "", nil
	}
	p, ok, err := ReadPointer(defaultRoot)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return defaultRoot, "", nil
	}
	if alive(p.PID) {
		if p.Root != defaultRoot {
			return p.Root, fmt.Sprintf("using the running daemon's state: %s (pid %d)", p.Root, p.PID), nil
		}
		return defaultRoot, "", nil
	}
	return defaultRoot, fmt.Sprintf("stale daemon pointer: %s (pid %d is not running)", p.Root, p.PID), nil
}
