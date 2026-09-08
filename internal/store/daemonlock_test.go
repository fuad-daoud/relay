package store

import (
	"errors"
	"testing"
)

func TestAcquireDaemonLockExcludesSecondHolder(t *testing.T) {
	s := New(t.TempDir())

	first, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Close()

	if _, err := s.AcquireDaemonLock(); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("second acquire: got %v, want ErrDaemonRunning", err)
	}
}

func TestDaemonRunningReflectsTheLock(t *testing.T) {
	s := New(t.TempDir())

	running, err := s.DaemonRunning()
	if err != nil {
		t.Fatalf("check before acquire: %v", err)
	}
	if running {
		t.Fatal("reported a daemon running before one acquired the lock")
	}

	held, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	running, err = s.DaemonRunning()
	if err != nil {
		t.Fatalf("check while held: %v", err)
	}
	if !running {
		t.Fatal("reported no daemon while the lock was held")
	}

	if err := held.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	running, err = s.DaemonRunning()
	if err != nil {
		t.Fatalf("check after release: %v", err)
	}
	if running {
		t.Fatal("reported a daemon running after the lock was released")
	}
}

// The daemon holds its lock for its whole life. If AcquireDaemonLock took the
// in-process mutex that WithLock uses, every state operation the daemon made
// would deadlock. This test is the regression guard for that.
func TestDaemonLockDoesNotBlockStateOperations(t *testing.T) {
	s := New(t.TempDir())

	held, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.Close()

	if err := s.WithLock(func(tx *Tx) error { return nil }); err != nil {
		t.Fatalf("WithLock while the daemon lock was held: %v", err)
	}
}
