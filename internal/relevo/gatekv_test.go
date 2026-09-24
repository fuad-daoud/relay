package relevo

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testGateKV returns a real t.TempDir() database for a Runtime's Gates or
// Latency field (P3b plan §7: new tests use t.TempDir() DBs only).
func testGateKV(t *testing.T) db.KV {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// testGates returns a Gates handle and the directory the legacy ledger.json,
// availability.json and history.json are imported from: the kv row and its
// legacy path can then be exercised together.
func testGates(t *testing.T) (db.KV, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, dir
}

// badJSONKV returns invalid bytes for every key, so a reader hits the decode
// failure a hand-edited file used to cause.
type badJSONKV struct{}

func (badJSONKV) KVGet(string) ([]byte, bool, error) { return []byte("not json"), true, nil }
func (badJSONKV) KVPut(string, []byte) error         { return nil }
func (badJSONKV) KVDelete(string) error              { return nil }

// failPutKV delegates to an inner KV but fails KVPut for one key, so one
// record's write can be made to fail while another's succeeds.
type failPutKV struct {
	inner db.KV
	key   string
}

func (k failPutKV) KVGet(key string) ([]byte, bool, error) { return k.inner.KVGet(key) }
func (k failPutKV) KVPut(key string, v []byte) error {
	if key == k.key {
		return errors.New("put failed")
	}
	return k.inner.KVPut(key, v)
}
func (k failPutKV) KVDelete(key string) error { return k.inner.KVDelete(key) }
