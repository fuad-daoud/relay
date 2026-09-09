package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

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
}

type realEnv struct {
	herdr HerdrClient
	store *store.Store
}

// NewEnv returns a real Env backed by the given herdr client and store.
func NewEnv(client HerdrClient, st *store.Store) Env {
	return &realEnv{herdr: client, store: st}
}

// defaultEnv returns an Env using default clients.
func defaultEnv() (Env, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return &realEnv{
		herdr: herdr.NewClient("", 0),
		store: store.New(root),
	}, nil
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
