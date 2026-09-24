package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
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

// DaemonInfo is the daemon's own record, stored in the store database under
// the kv key "daemon" (#371; P3b plan §4.4). It was <state root>/daemon.json
// before the move. A missing record while the daemon lock is held means "a
// daemon older than #371": one that does not follow upgrades.
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

// daemonInfoKey is the kv row the daemon's own record lives in (P3b plan
// §4.4). daemon.json was the file form until this round; ReadDaemonInfo imports
// a present one on first read.
const daemonInfoKey = "daemon"

// daemonInfoPath is the daemon's record as it was written before the move to
// the kv table: the legacy file KVImportFile adopts on first read.
func (s *Store) daemonInfoPath() string { return filepath.Join(s.root, daemonInfoFileName) }

// WriteDaemonInfo stores the daemon's record in the store database under the
// kv key "daemon" (P3b plan §4.3): one short upsert, so a reader never sees a
// half-written record.
func (s *Store) WriteDaemonInfo(info DaemonInfo) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal daemon info: %w", err)
	}
	d, err := s.DB()
	if err != nil {
		return err
	}
	return d.KVPut(daemonInfoKey, raw)
}

// ReadDaemonInfo reads the daemon's record from the kv table. A missing row is
// (zero, false, nil) -- not an error, because a daemon older than #371 writes
// none -- and so is a store root with no database yet. A present legacy
// daemon.json is imported and removed on the first read. Malformed JSON is an
// error.
func (s *Store) ReadDaemonInfo() (DaemonInfo, bool, error) {
	d, err := s.DBIfExists()
	if err != nil {
		return DaemonInfo{}, false, err
	}
	if d == nil {
		return DaemonInfo{}, false, nil
	}

	raw, ok, err := db.KVImportFile(d, daemonInfoKey, s.daemonInfoPath())
	if err != nil {
		return DaemonInfo{}, false, fmt.Errorf("read daemon info: %w", err)
	}
	if !ok {
		return DaemonInfo{}, false, nil
	}

	var info DaemonInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return DaemonInfo{}, false, fmt.Errorf("decode daemon info: %w", err)
	}
	return info, true, nil
}

// RemoveDaemonInfo deletes the daemon's record. An absent key is nil: the
// clean-shutdown path runs this whether or not the write at start succeeded.
// A root with no database has nothing to remove.
func (s *Store) RemoveDaemonInfo() error {
	d, err := s.DBIfExists()
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	return d.KVDelete(daemonInfoKey)
}
