package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// HerdrClient represents the methods on herdr needed for doctor preflight checks.
type HerdrClient interface {
	Version(ctx context.Context) (string, error)
	IntegrationStatus(ctx context.Context) (map[string]herdr.IntegrationState, error)
}

// Env abstracts external system facts for testability.
type Env interface {
	// HerdrVersion returns the parsed semver of the herdr CLI.
	HerdrVersion(ctx context.Context) (string, error)
	// IntegrationStatus returns every target herdr knows, keyed by target name.
	IntegrationStatus(ctx context.Context) (map[string]herdr.IntegrationState, error)
	// DaemonRunning reports whether a relay daemon holds the lock.
	DaemonRunning(ctx context.Context) (bool, error)
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
	// its trimmed stdout, so a harness with a version floor can be held to
	// it the way herdr is (spec §4.4). path came from LookPath.
	BinaryVersion(ctx context.Context, path string) (string, error)
}

type realEnv struct {
	herdr HerdrClient
	store *store.Store
}

// NewEnv returns a real Env backed by the given herdr client and store.
func NewEnv(client HerdrClient, st *store.Store) Env {
	return &realEnv{herdr: client, store: st}
}

func (e *realEnv) HerdrVersion(ctx context.Context) (string, error) {
	if e.herdr == nil {
		return "", os.ErrNotExist
	}
	return e.herdr.Version(ctx)
}

func (e *realEnv) IntegrationStatus(ctx context.Context) (map[string]herdr.IntegrationState, error) {
	if e.herdr == nil {
		return nil, os.ErrNotExist
	}
	return e.herdr.IntegrationStatus(ctx)
}

func (e *realEnv) DaemonRunning(ctx context.Context) (bool, error) {
	if e.store == nil {
		return false, nil
	}
	return e.store.DaemonRunning()
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
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return "", errors.New("empty version output")
	}
	return fields[0], nil
}
