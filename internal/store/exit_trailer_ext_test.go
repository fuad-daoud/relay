package store_test

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestExitTrailerMatchesProc pins store.ExitTrailer to proc.ExitTrailer:
// internal/store cannot import internal/proc to share the literal -- proc
// imports internal/relevo, which imports store -- so this external test keeps
// the two from drifting apart.
func TestExitTrailerMatchesProc(t *testing.T) {
	if store.ExitTrailer != proc.ExitTrailer {
		t.Errorf("store.ExitTrailer = %q, proc.ExitTrailer = %q; they must name the same line",
			store.ExitTrailer, proc.ExitTrailer)
	}
}
