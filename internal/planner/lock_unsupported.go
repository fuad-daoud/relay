//go:build !unix

package planner

import (
	"errors"
	"os"
	"runtime"
)

// tryLockExclusive has no implementation here, for the reason store's
// lock_unsupported.go has none: relevo targets Linux and macOS, and two writers
// in one registry is exactly the corruption the lock exists to prevent, so an
// unsupported platform must fail loudly rather than silently race.
func tryLockExclusive(*os.File) (bool, error) {
	return false, errors.New("relevo planner registry locking is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}
