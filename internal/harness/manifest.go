package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// manifestFileName is the role manifest's name inside relay's state root.
const manifestFileName = "agents-manifest.json"

// ManifestPath returns the role manifest's location under stateRoot (#371 §3).
// The manifest maps a home-relative definition path -- exactly Role.Path, the
// form InstallResult.Path carries -- to the lowercase hex sha256 of what relay
// last wrote there, so a later relay can tell "unchanged since relay wrote it"
// (safe to refresh) from "edited by the user" (kept).
func ManifestPath(stateRoot string) string {
	return filepath.Join(stateRoot, manifestFileName)
}

// docSHA is the manifest's value for one definition: the lowercase hex sha256
// of the raw bytes. It is always taken over the raw bytes, never over the
// DocEqual-normalised form, so the manifest records exactly what is on disk.
func docSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadManifest loads the manifest at path. A missing file is an empty map and
// no error: a machine relay has never installed roles on has nothing recorded.
// Malformed JSON is an error, and the caller decides what to do with it --
// Install reports it once and then treats the manifest as empty (#371 §3).
func ReadManifest(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read role manifest: %w", err)
	}

	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode role manifest %s: %w", path, err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

// WriteManifest writes the manifest atomically -- a temp file in the same
// directory, then a rename, mode 0644 -- so a reader never sees half of it
// (#371 §3).
func WriteManifest(path string, m map[string]string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal role manifest: %w", err)
	}
	return writeFileAtomic(path, append(raw, '\n'), 0o644)
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

	tmp, err := os.CreateTemp(dir, ".relay-*")
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
