package legacy

import (
	"os"
	"path/filepath"
	"testing"
)

func rootsUnder(dir string) Roots {
	return Roots{
		OldState:  filepath.Join(dir, "relay-state"),
		NewState:  filepath.Join(dir, "relevo-state"),
		OldConfig: filepath.Join(dir, "relay-config"),
		NewConfig: filepath.Join(dir, "relevo-config"),
	}
}

func mkdirAll(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}
