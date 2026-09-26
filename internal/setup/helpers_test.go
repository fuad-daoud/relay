package setup

import (
	"fmt"
	"io/fs"
)

// pathEnv is the InstallEnv seam Plan consults. Only LookPath answers; every
// other method is unreachable from Plan and returns a zero value.
type pathEnv struct{ onPath map[string]bool }

func (e pathEnv) LookPath(binary string) (string, error) {
	if e.onPath[binary] {
		return "/bin/" + binary, nil
	}
	return "", fmt.Errorf("binary not found: %s", binary)
}

func (e pathEnv) HomePath(rel string) (string, error)      { return rel, nil }
func (e pathEnv) ReadFile(string) ([]byte, error)          { return nil, fs.ErrNotExist }
func (e pathEnv) MkdirAll(string) error                    { return nil }
func (e pathEnv) WriteFile(string, []byte) error           { return nil }
func (e pathEnv) LoadManifest() (map[string]string, error) { return map[string]string{}, nil }
func (e pathEnv) SaveManifest(map[string]string) error     { return nil }
