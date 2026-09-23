package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relay/internal/release"
	"github.com/fuad-daoud/relay/internal/store"
)

// Env abstracts external system facts for testability.
type Env interface {
	// DaemonRunning reports whether a relay daemon holds the lock.
	DaemonRunning(ctx context.Context) (bool, error)
	// DaemonInfo reads the daemon's own record (daemon.json): the version,
	// executable and identity of the running image, and any binary it refused
	// (#371). A missing record is (zero, false, nil) and means a daemon older
	// than #371.
	DaemonInfo() (store.DaemonInfo, bool, error)
	// LookPath resolves an executable on PATH.
	LookPath(binary string) (string, error)
	// HomePath joins a home-relative path, and Stat reports whether it exists.
	HomePath(rel string) (string, error)
	Stat(path string) error
	// ReadFile reads a file whose existence Stat has already established.
	// Used to report facts about an installed role definition; a read error
	// is never itself a check failure.
	ReadFile(path string) ([]byte, error)
	// BinaryVersion runs `<path> --version` and returns the first field of
	// its trimmed stdout, so a harness with a version floor can be held to it
	// (spec §4.4). path came from LookPath.
	BinaryVersion(ctx context.Context, path string) (string, error)
	// Probe tests whether dir is writable by creating and removing a temporary file.
	Probe(dir string) error
	// Command runs bin with args and returns its stdout, for a doctor check
	// that reads a local fact no other Env method exposes (#256: opencode's
	// session count, read from opencode.db via sqlite3 -- no network call,
	// no credential). bin not being on PATH is a plain error, like any other
	// exec.CommandContext failure.
	Command(ctx context.Context, bin string, args ...string) ([]byte, error)
	// ReleaseState returns the running version, the cached latest (ok false
	// when there is no usable cache) and the install kind (#293). Cache read
	// only: it never touches the network, which is why the release check can
	// be unconditional while every claim it makes is one relay can prove.
	ReleaseState() (running string, latest string, ok bool, kind release.Kind)
}

type realEnv struct {
	store *store.Store

	// self is relay's own build fact. Only package main can see
	// buildVersion(), so the caller gathers it and passes it to NewEnv; the
	// zero value means this install cannot be classified, which
	// ReleaseState reports as release.KindUnknown.
	self release.Inputs
}

// NewEnv returns a real Env backed by the given store.
//
// self is release.Detect's Inputs for the running relay, optional because a
// caller that never reads ReleaseState (the bind preflight) need not gather
// it: the check then reads as "not checked" rather than guessing.
func NewEnv(st *store.Store, self ...release.Inputs) Env {
	env := &realEnv{store: st}
	if len(self) > 0 {
		env.self = self[0]
	}
	return env
}

func (e *realEnv) DaemonRunning(ctx context.Context) (bool, error) {
	if e.store == nil {
		return false, nil
	}
	return e.store.DaemonRunning()
}

// DaemonInfo reads daemon.json from the state root. A nil store is "no record",
// exactly as a missing file is.
func (e *realEnv) DaemonInfo() (store.DaemonInfo, bool, error) {
	if e.store == nil {
		return store.DaemonInfo{}, false, nil
	}
	return e.store.ReadDaemonInfo()
}

func (e *realEnv) LookPath(binary string) (string, error) {
	return exec.LookPath(binary)
}

func (e *realEnv) HomePath(rel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rel), nil
}

func (e *realEnv) Stat(path string) error {
	_, err := os.Stat(path)
	return err
}

func (e *realEnv) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (e *realEnv) BinaryVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return versionField(string(out))
}

// versionField picks the version out of a --version line: the first
// whitespace field that, after an optional leading "v", starts with a
// digit; the first field when none does; an error on no fields.
func versionField(out string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return "", errors.New("empty version output")
	}
	for _, f := range fields {
		v := strings.TrimPrefix(f, "v")
		if v != "" && v[0] >= '0' && v[0] <= '9' {
			return f, nil
		}
	}
	return fields[0], nil
}

func (e *realEnv) Command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).Output()
}

// ReleaseState reads the daemon's cached answer and classifies this install.
// It composes the cache path from store.DefaultRoot() rather than building an
// XDG path by hand (#42), and a root or cache it cannot read is not an error:
// it is "not checked", which is what every unrefreshed, offline or
// unclassifiable install reads as.
func (e *realEnv) ReleaseState() (string, string, bool, release.Kind) {
	kind := release.Detect(e.self)

	root, err := store.DefaultRoot()
	if err != nil {
		return e.self.Version, "", false, kind
	}
	cached, ok, err := release.Load(root)
	if err != nil {
		return e.self.Version, "", false, kind
	}
	return e.self.Version, cached.Latest, ok, kind
}

func (e *realEnv) Probe(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "doctor-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
