package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrDaemonRunning reports that another process already holds the daemon lock.
var ErrDaemonRunning = errors.New("relay daemon already running")

// DaemonLock is a held daemon lock. Closing it releases the lock; so does the
// process exiting, because flock is owned by the kernel and dropped when the
// last descriptor closes. There is no stale lock file to reap.
type DaemonLock struct {
	f *os.File
}

// AcquireDaemonLock takes the exclusive daemon lock, returning ErrDaemonRunning
// if another process holds it.
//
// It deliberately does not take s.mu. The daemon holds this lock for its entire
// lifetime, and s.mu serialises WithLock: holding both would deadlock every
// state operation the daemon subsequently makes.
func (s *Store) AcquireDaemonLock() (*DaemonLock, error) {
	f, err := s.openDaemonLockFile()
	if err != nil {
		return nil, err
	}

	locked, err := tryLockExclusive(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	if !locked {
		f.Close()
		return nil, ErrDaemonRunning
	}

	return &DaemonLock{f: f}, nil
}

// DaemonRunning reports whether a daemon currently holds the lock. It answers
// by trying to take the lock and releasing it again immediately, so a true
// result means "was held a moment ago", which is all any caller can know.
func (s *Store) DaemonRunning() (bool, error) {
	f, err := s.openDaemonLockFile()
	if err != nil {
		return false, err
	}
	defer f.Close()

	locked, err := tryLockExclusive(f)
	if err != nil {
		return false, fmt.Errorf("lock %s: %w", f.Name(), err)
	}

	// Granted means nobody held it, so no daemon is running. Closing f in the
	// defer releases what we just took.
	return !locked, nil
}

func (s *Store) openDaemonLockFile() (*os.File, error) {
	if err := os.MkdirAll(s.root, bindingDirMode); err != nil {
		return nil, fmt.Errorf("create state root: %w", err)
	}

	path := filepath.Join(s.root, daemonLockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, bindingFileMode)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	return f, nil
}

// Close releases the lock.
func (l *DaemonLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("close daemon lock: %w", err)
	}
	l.f = nil
	return nil
}
