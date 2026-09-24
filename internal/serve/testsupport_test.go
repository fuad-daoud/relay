package serve

import (
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testServeDB opens a fresh machine database for a server under test (P5
// §4.3): serve.New takes the machine database from its caller now, so every
// test that builds a Server supplies one.
func testServeDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("open test machine db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
