package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/legacy"
)

func TestGuardExempt(t *testing.T) {
	exempt := []string{"help", "-h", "--help", "version", "-v", "--version", "doctor", "migrate"}
	for _, verb := range exempt {
		if !guardExempt(verb) {
			t.Errorf("guardExempt(%q) = false, want true", verb)
		}
	}

	blocked := []string{"status", "send", "bind", "add", "fork", "pull", "wait", "daemon", "serve", "ui", "unbind", "", "Help", "DOCTOR", "migrate --dry-run"}
	for _, verb := range blocked {
		if guardExempt(verb) {
			t.Errorf("guardExempt(%q) = true, want false", verb)
		}
	}
}

func TestRefuseUnmigrated(t *testing.T) {
	t.Run("old state root with no new one refuses with the exact text", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, legacy.Name), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_STATE_HOME", dir)

		err := refuseUnmigrated()
		if err == nil {
			t.Fatal("refuseUnmigrated = nil, want the refusal while relay/ has no relevo/") // name-guard: legacy
		}
		want := "relevo: relay-era state at " + filepath.Join(dir, "relay") + // name-guard: legacy
			" is not migrated; run relevo migrate --dry-run, then relevo migrate"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("both roots present is not unmigrated", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{legacy.Name, "relevo"} {
			if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("XDG_STATE_HOME", dir)

		if err := refuseUnmigrated(); err != nil {
			t.Errorf("refuseUnmigrated = %v, want nil: an old root beside its new one is stale, not unmigrated", err)
		}
	})

	t.Run("neither root present is nil", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())

		if err := refuseUnmigrated(); err != nil {
			t.Errorf("refuseUnmigrated = %v, want nil on a fresh install", err)
		}
	})
}
