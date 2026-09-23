// Package upgrade moves the running daemon onto a replaced relevo binary
// (#371). It resolves the executable the daemon runs, identifies it by the
// bytes on disk, and decides -- behind a two-check debounce and a preflight --
// when a new binary is safe to exec into.
package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveExe returns the path of the running executable, symlinks resolved and
// with any trailing " (deleted)" stripped: a replaced binary's /proc link says
// "path (deleted)", and the path -- not the deleted inode -- is what a re-exec
// must run. It is called once at image start, before any possible replacement.
func ResolveExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("upgrade: resolve executable: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("upgrade: resolve %s: %w", exe, err)
	}

	return strings.TrimSuffix(resolved, " (deleted)"), nil
}
