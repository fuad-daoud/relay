//go:build unix

package planner

import (
	"errors"
	"os"
	"syscall"
)

// tryLockExclusive takes a non-blocking exclusive flock on f, reporting whether
// the lock was granted. A lock held by someone else is (false, nil), not an
// error: the caller polls, and only a real failure should abort that.
//
// This is store's scheme, mirrored deliberately (internal/store/lock_unix.go):
// flock is what makes the lock self-healing, because the kernel drops it when a
// holder dies and there is never a stale lock file to reap by hand.
func tryLockExclusive(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}
