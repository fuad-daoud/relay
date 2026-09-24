package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
)

// manifestKey is the kv row the role manifest lives in (P3b plan §1, §4.4). It
// was the file <state root>/agents-manifest.json until this round; ReadManifest
// imports a present one on first read.
const manifestKey = "agents-manifest"

// docSHA is the manifest's value for one definition: the lowercase hex sha256
// of the raw bytes. It is always taken over the raw bytes, never over the
// DocEqual-normalised form, so the manifest records exactly what is on disk.
func docSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadManifest loads the role manifest from the kv row "agents-manifest",
// importing a present legacyPath file (agents-manifest.json) on first read
// (P3b plan §4.3, §4.4). An absent row with no file behind it is an empty map
// and no error: a machine relevo has never installed roles on has nothing
// recorded. Malformed JSON is an error, and the caller decides what to do with
// it -- Install reports it once and then treats the manifest as empty
// (#371 §3).
func ReadManifest(kv db.KV, legacyPath string) (map[string]string, error) {
	raw, ok, err := db.KVImportFile(kv, manifestKey, legacyPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]string{}, nil
	}

	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode role manifest: %w", err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

// WriteManifest stores the whole manifest in the kv row "agents-manifest"
// (#371 §3; P3b plan §4.4).
func WriteManifest(kv db.KV, m map[string]string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal role manifest: %w", err)
	}
	return kv.KVPut(manifestKey, append(raw, '\n'))
}

// writeFileAtomic writes via a temp file in the same directory then renames, so
// no reader -- and no crash -- can leave a truncated file behind. The temp file
// never survives: it is renamed into place on success and removed on any
// failure.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".relevo-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
