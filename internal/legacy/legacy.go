// Package legacy is the only home of relay-era names. Every name was renamed
// to relevo; this package keeps the old spellings that something still has to
// read, so no other package has to spell them again. It is a leaf package: it
// imports only the standard library, so whatever imports it -- proc, usage,
// relevo, ledger, doctor, cmd/relevo -- stays acyclic.
package legacy

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The old names, each paired with who still reads it now relevo spells it differently.
const (
	// Name is the old tool name; `relevo migrate` reads it for reporting and the root paths below.
	Name = "relay"

	// Binary is the old executable; `relevo migrate` removes it unless --keep-old-binary is given.
	Binary = "relay"

	// ExitTrailer prefixes the old supervisor's exit line; proc.Runner and usage read pre-cutover streams.
	ExitTrailer = "relay-exit:"

	// RusageTrailer prefixes the old supervisor's rusage line; proc.Runner and usage still read it.
	RusageTrailer = "relay-rusage:"

	// LedgerSource is the old ledger entry source; ledger.LoadKV rewrites it to "relevo" on read.
	LedgerSource = "relay"

	// DBFile is the old database file; `relevo migrate` renames it with its -wal/-shm siblings.
	DBFile = "relay.db"

	// ClientUnit is the old systemd user client unit; `relevo migrate` stops and retires it.
	ClientUnit = "relay.service"

	// ServeUnit is the old systemd user server unit; `relevo migrate` stops and retires it, no replacement.
	ServeUnit = "relay-serve.service"

	// LaunchdLabel is the old macOS LaunchAgent label; `relevo migrate` unloads and deletes its plist.
	LaunchdLabel = "com.github.fuad-daoud.relay"

	// Slice is the old systemd slice; the server's deploy owns the new one.
	Slice = "relay.slice"

	// KeyPEMType is a pre-rename client key's PEM block type; remote.ParsePrivate still accepts it.
	KeyPEMType = "RELAY ED25519 PRIVATE KEY"
)

// Roots is the four absolute paths a cutover touches: the relay-era state and
// config directories, and the relevo-era directories they become.
type Roots struct {
	OldState, NewState   string
	OldConfig, NewConfig string
}

// Prefix is one absolute-directory substitution: every stored path that is
// Old exactly, or starts with Old followed by "/", becomes New plus the tail.
type Prefix struct{ Old, New string }

// Prefixes returns the state pair, then the config pair, skipping any pair
// with an empty side or where Old already equals New.
func (r Roots) Prefixes() []Prefix {
	var pairs []Prefix
	add := func(old, new string) {
		if old == "" || new == "" || old == new {
			return
		}
		pairs = append(pairs, Prefix{Old: old, New: new})
	}
	add(r.OldState, r.NewState)
	add(r.OldConfig, r.NewConfig)
	return pairs
}

// RewriteJSON applies every pair to data in order and returns the result; it
// never modifies data, and with no pairs returns its bytes unchanged.
func RewriteJSON(data []byte, pairs []Prefix) []byte {
	out := data
	for _, p := range pairs {
		out = bytes.ReplaceAll(out, []byte(`"`+p.Old+`/`), []byte(`"`+p.New+`/`))
		out = bytes.ReplaceAll(out, []byte(`"`+p.Old+`"`), []byte(`"`+p.New+`"`))
	}
	return out
}

// Status says which of a Roots' four directories exist.
type Status struct {
	OldState, NewState, OldConfig, NewConfig bool
}

// StateRoot resolves the relay-era state root the way store.DefaultRoot
// resolves relevo's: $XDG_STATE_HOME/relay, else ~/.local/state/relay.
func StateRoot(getenv func(string) string, home string) string {
	if xdg := getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, Name)
	}
	return filepath.Join(home, ".local", "state", Name)
}

// ConfigRoot is the relay-era config root inside configHome (what
// userConfigRoot() returns); relevo's own root is composed the same way.
func ConfigRoot(configHome string) string {
	return filepath.Join(configHome, Name)
}

// Probe stats a Roots' four paths. A missing path is false with no error; any
// other stat error is returned naming the path. A path occupied by a
// non-directory still counts as present.
func Probe(r Roots) (Status, error) {
	var s Status
	for _, p := range []struct {
		path string
		flag *bool
	}{
		{r.OldState, &s.OldState},
		{r.NewState, &s.NewState},
		{r.OldConfig, &s.OldConfig},
		{r.NewConfig, &s.NewConfig},
	} {
		exists, err := pathExists(p.path)
		if err != nil {
			return Status{}, err
		}
		*p.flag = exists
	}
	return s, nil
}

func pathExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

// Unmigrated reports an install that has run relevo before `relevo migrate`:
// an old root exists and its new one does not. Config lives in the relevo
// state root's database, so once NewState exists a missing NewConfig alone is
// not unmigrated.
func (s Status) Unmigrated() bool {
	return (s.OldState && !s.NewState) || (s.OldConfig && !s.NewConfig && !s.NewState)
}

// Stale reports an old root beside its new one: the cutover happened and
// something relay-era recreated the old directory afterwards.
func (s Status) Stale() bool {
	return (s.OldState && s.NewState) || (s.OldConfig && s.NewConfig)
}
