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

// The old names. Each constant is the relay-era spelling of a name relevo
// spells differently now, so the comment names relevo's counterpart and the
// reader (or, where only round 3 will touch it, the migrate step) that still
// needs it.
const (
	// Name is the old name of the tool, where relevo's is "relevo". `relevo
	// migrate` reads it when it reports what it moved and when it renders the
	// root paths; no log reader needs it.
	Name = "relay"

	// Binary is the old executable name, where relevo's is "relevo". `relevo
	// migrate` looks for it beside the running executable and removes it
	// unless --keep-old-binary is given.
	Binary = "relay"

	// ExitTrailer prefixes the last line the old supervisor wrote to a
	// builder stream, where relevo writes "relevo-exit:". proc.(*Runner).
	// ExitCode and usage.streamClosed still read streams written before the
	// cutover; internal/relevo's tailLines reads the line as gate output.
	ExitTrailer = "relay-exit:"

	// RusageTrailer prefixes the old supervisor's rusage line, where relevo
	// writes "relevo-rusage:". proc.(*Runner).Rusage, proc.ParseRusageTrailer
	// and internal/relevo's tailLines still read it.
	RusageTrailer = "relay-rusage:"

	// LedgerSource is the old ledger entry source, where relevo writes
	// "relevo". ledger.LoadKV rewrites it to "relevo" on read, so a
	// pre-cutover rate-limit gate does not lapse into Other.
	LedgerSource = "relay"

	// DBFile is the old database file name, where relevo uses "relevo.db".
	// `relevo migrate` renames it, with its -wal and -shm siblings, inside
	// the moved state root; nothing this round reads it.
	DBFile = "relay.db"

	// ClientUnit is the old user-level systemd client unit, where relevo
	// installs "relevo.service". `relevo migrate` stops it and retires its
	// unit file.
	ClientUnit = "relay.service"

	// ServeUnit is the old user-level systemd server unit, where relevo
	// installs "relevo-serve.service". `relevo migrate` stops it and retires
	// its unit file; it never installs a new one.
	ServeUnit = "relay-serve.service"

	// LaunchdLabel is the old macOS LaunchAgent label, where relevo uses
	// "com.github.fuad-daoud.relevo". `relevo migrate` unloads the old plist
	// and deletes it.
	LaunchdLabel = "com.github.fuad-daoud.relay"

	// Slice is the old systemd slice, where relevo uses "relevo.slice".
	// `relevo migrate` reports it and the server's deploy owns the new one;
	// nothing this round reads it.
	Slice = "relay.slice"

	// KeyPEMType is the PEM block type of a client key written before the
	// rename, where relevo writes "RELEVO ED25519 PRIVATE KEY". The key's
	// bytes are unchanged and enrolment is by public key, so remote.
	// ParsePrivate still accepts it; new keys are written as "RELEVO ED25519
	// PRIVATE KEY".
	KeyPEMType = "RELAY ED25519 PRIVATE KEY"
)

// Roots is the four roots a cutover touches: the relay-era state and config
// directories, and the relevo-era directories they become. All four are
// absolute.
type Roots struct {
	OldState, NewState   string // <XDG_STATE_HOME or ~/.local/state>/{relay,relevo}
	OldConfig, NewConfig string // <XDG_CONFIG_HOME or ~/.config>/{relay,relevo}
}

// Prefix is one absolute-directory substitution: every stored path that is Old
// exactly, or starts with Old followed by "/", becomes New plus the same tail.
type Prefix struct{ Old, New string }

// Prefixes is the substitution list a rewrite of this Roots applies, in the
// order migrate builds it: the state root always, the config root when there
// is one. A pair is left out when either side is empty or Old already equals
// New. The zero Roots returns an empty slice.
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

// RewriteJSON applies every pair to data, in order, and returns the result:
// each `"<Old>/` becomes `"<New>/`, then each `"<Old>"` becomes `"<New>"`.
// It is pure -- data is never modified -- and with no pairs it returns data's
// bytes unchanged.
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

// StateRoot resolves the relay-era state root exactly the way
// store.DefaultRoot resolves relevo's: $XDG_STATE_HOME/relay when the
// variable is set, else ~/.local/state/relay. getenv is os.Getenv in
// production; home is the home directory the caller already resolved.
func StateRoot(getenv func(string) string, home string) string {
	if xdg := getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, Name)
	}
	return filepath.Join(home, ".local", "state", Name)
}

// ConfigRoot is the relay-era config root inside configHome, which is what
// userConfigRoot() returns. relevo's own config root is composed through
// userConfigRoot() too, spelled with "relevo" literally by cmd/relevo, which
// owns that name.
func ConfigRoot(configHome string) string {
	return filepath.Join(configHome, Name)
}

// Probe stats a Roots' four paths and reports which of them exist. A missing
// path is false and no error; any other stat error is returned naming the
// path, because a root relevo cannot stat is not safe to run beside. A path
// that exists but is not a directory is true: the root is taken, and migrate
// decides what to do with it.
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

// pathExists reports whether path exists; a path that is not a directory
// counts as existing. Only a stat error other than "not there" is returned.
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
// an old root exists and its new one does not. The old root's real state still
// sits there, and starting relevo beside it would create an empty new root
// that then blocks migrate.
//
// Config lives in the relevo state root's database, so a new state root means
// config is already recorded there: a missing ~/.config/relevo is not by
// itself unmigrated.
func (s Status) Unmigrated() bool {
	return (s.OldState && !s.NewState) || (s.OldConfig && !s.NewConfig && !s.NewState)
}

// Stale reports an old root beside its new one: the cutover happened, and
// something relay-era -- a lingering binary or an old plugin -- recreated the
// old directory afterwards.
func (s Status) Stale() bool {
	return (s.OldState && s.NewState) || (s.OldConfig && s.NewConfig)
}
