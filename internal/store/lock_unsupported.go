//go:build !unix

package store

import (
	"errors"
	"os"
	"runtime"
)

// tryLockExclusive has no implementation here. relevo targets Linux and macOS
// (see the platform notes in the README), and this file exists so the tree
// still compiles elsewhere rather than failing at an undefined syscall.
//
// It refuses instead of running unlocked on purpose: two writers in the state
// root is exactly the corruption the lock exists to prevent, so an unsupported
// platform must fail loudly, not silently race.
func tryLockExclusive(*os.File) (bool, error) {
	return false, errors.New("relevo state locking is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}
