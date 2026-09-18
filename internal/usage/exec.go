package usage

import "context"

// Exec runs a binary and returns its stdout. Only sqlite3 goes through it:
// cmd/relay wires an exec.CommandContext wrapper, tests wire a fake, and
// nil means the binary is not available.
type Exec interface {
	Run(ctx context.Context, bin string, args ...string) ([]byte, error)
}
