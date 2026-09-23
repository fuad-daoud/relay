package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/store"
)

// renameRoots resolves the four roots a cutover touches (#292 §3 step 1): the
// relay-era state and config roots, and the relevo-era ones they become. Every // name-guard: legacy
// path is absolute, and every relevo-era one is composed the way the rest of
// the CLI composes it -- store.DefaultRoot for state, userConfigRoot for
// config (CLAUDE.md, #42) -- so the row, the guard and (in round 3) migrate all
// name the same directories.
func renameRoots() (legacy.Roots, error) {
	newState, err := store.DefaultRoot()
	if err != nil {
		return legacy.Roots{}, err
	}

	// The old root mirrors store.DefaultRoot: the same XDG_STATE_HOME check,
	// with the old name. legacy.StateRoot takes getenv and home so it stays a
	// leaf, so resolve home here.
	home, err := os.UserHomeDir()
	if err != nil {
		return legacy.Roots{}, fmt.Errorf("resolve home directory: %w", err)
	}

	configHome, err := userConfigRoot()
	if err != nil {
		return legacy.Roots{}, err
	}

	return legacy.Roots{
		OldState:  legacy.StateRoot(os.Getenv, home),
		NewState:  newState,
		OldConfig: legacy.ConfigRoot(configHome),
		NewConfig: filepath.Join(configHome, "relevo"),
	}, nil
}

// guardExempt reports whether verb runs even while relay-era state sits // name-guard: legacy
// unmigrated. help and version touch no state; doctor is how the human learns
// what is wrong; migrate is the command that fixes it. migrate does not exist
// until round 3 -- exempting it now is deliberate, so the refusal never stands
// between an install and the command the refusal itself names.
func guardExempt(verb string) bool {
	switch verb {
	case "help", "-h", "--help", "version", "-v", "--version", "doctor", "migrate":
		return true
	}
	return false
}

// refuseUnmigrated is the guard run() applies before any state-touching verb
// (#292 §4). It returns nil when there is no relay-era root, or when every one // name-guard: legacy
// has its relevo-era counterpart; otherwise it names each old root that lacks
// its new one and the command that migrates them.
//
// A probe relevo cannot complete is returned wrapped: a root relevo cannot stat
// is not safe to run beside, so the verb fails.
func refuseUnmigrated() error {
	roots, err := renameRoots()
	if err != nil {
		return fmt.Errorf("relevo: %w", err)
	}

	st, err := legacy.Probe(roots)
	if err != nil {
		return fmt.Errorf("relevo: %w", err)
	}
	if !st.Unmigrated() {
		return nil
	}

	var unmigrated []string
	if st.OldState && !st.NewState {
		unmigrated = append(unmigrated, roots.OldState)
	}
	if st.OldConfig && !st.NewConfig {
		unmigrated = append(unmigrated, roots.OldConfig)
	}

	return fmt.Errorf("relevo: relay-era state at %s is not migrated; run relevo migrate --dry-run, then relevo migrate", // name-guard: legacy
		strings.Join(unmigrated, ", "))
}
