//go:build unix

package migrate

import (
	"os"
	"syscall"
)

// sameFS reports whether a and b live on the same filesystem, by comparing
// their device numbers. A path that cannot be stat'ed answers true: the
// refusal exists to catch a move that would copy across devices, and refusing
// on a path that is not there yet would be a false alarm.
func sameFS(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return true
	}
	ib, err := os.Stat(b)
	if err != nil {
		return true
	}

	sa, ok := ia.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	sb, ok := ib.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return uint64(sa.Dev) == uint64(sb.Dev)
}
