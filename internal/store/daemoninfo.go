package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileID is a file's identity at one instant: the device and inode that name
// the bytes, plus the size and mtime that say whether those bytes changed in
// place. It is comparable with ==, and the zero value means "unknown" -- it
// never equals a real identity, so a zero Started never matches a stat.
type FileID struct {
	Dev     uint64    `json:"dev"`
	Ino     uint64    `json:"ino"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// ReexecFailure records the binary this image refused: the identity that
// failed preflight, when, and why (#371).
type ReexecFailure struct {
	ExeID  FileID    `json:"exe_id"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// DaemonInfo is the daemon's own record, written at <state root>/daemon.json
// (#371). A missing file while the daemon lock is held means "a daemon older
// than #371": one that does not follow upgrades.
type DaemonInfo struct {
	// Version is buildVersion() of the running image.
	Version string `json:"version"`
	// PID is that image's process id; it survives a re-exec.
	PID int `json:"pid"`
	// StartedAt is the start of this image, so a re-exec resets it.
	StartedAt time.Time `json:"started_at"`
	// Exe is the resolved executable path, with no " (deleted)" suffix.
	Exe string `json:"exe"`
	// ExeID is the identity of Exe when this image started.
	ExeID FileID `json:"exe_id"`
	// ReexecFrom is the previous image's version, when this image came from
	// a re-exec.
	ReexecFrom string `json:"reexec_from,omitempty"`
	// ReexecFailed is set while the file at Exe is a build this daemon
	// refused to load.
	ReexecFailed *ReexecFailure `json:"reexec_failed,omitempty"`
}

// daemonInfoPath is the daemon's record inside the state root.
func (s *Store) daemonInfoPath() string { return filepath.Join(s.root, daemonInfoFileName) }

// WriteDaemonInfo writes daemon.json atomically: a temp file in the root, then
// a rename, mode 0644. A reader never sees a half-written record.
func (s *Store) WriteDaemonInfo(info DaemonInfo) error {
	if err := os.MkdirAll(s.root, bindingDirMode); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}

	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal daemon info: %w", err)
	}

	return writeFileAtomic(s.daemonInfoPath(), raw, bindingFileMode)
}

// ReadDaemonInfo reads daemon.json. A missing file is (zero, false, nil) --
// not an error, because a daemon older than #371 writes none. Malformed JSON
// is an error.
func (s *Store) ReadDaemonInfo() (DaemonInfo, bool, error) {
	raw, err := os.ReadFile(s.daemonInfoPath())
	if errors.Is(err, os.ErrNotExist) {
		return DaemonInfo{}, false, nil
	}
	if err != nil {
		return DaemonInfo{}, false, fmt.Errorf("read daemon info: %w", err)
	}

	var info DaemonInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return DaemonInfo{}, false, fmt.Errorf("decode daemon info: %w", err)
	}
	return info, true, nil
}

// RemoveDaemonInfo removes daemon.json. A file that is not there is nil: the
// clean-shutdown path runs this whether or not the write at start succeeded.
func (s *Store) RemoveDaemonInfo() error {
	if err := os.Remove(s.daemonInfoPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove daemon info: %w", err)
	}
	return nil
}
