package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/policy"
)

// stubBinary writes an executable stub named name into dir.
func stubBinary(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

// initRoot gives a test its own HOME and XDG_CONFIG_HOME and returns them.
func initRoot(t *testing.T) (home, configHome string) {
	t.Helper()
	home = t.TempDir()
	configHome = filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	return home, configHome
}

func TestInitWritesConfigAndRoles(t *testing.T) {
	home, configHome := initRoot(t)

	bin := t.TempDir()
	stubBinary(t, bin, "claude")
	stubBinary(t, bin, "opencode")
	t.Setenv("PATH", bin)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"init"})
	})
	if err != nil {
		t.Fatalf("run init: %v (stderr: %s)", err, stderr)
	}

	candPath := filepath.Join(configHome, "relay", "candidates.json")
	if _, err := candidate.Load(candPath); err != nil {
		t.Fatalf("candidate.Load(%s): %v", candPath, err)
	}
	polPath := filepath.Join(configHome, "relay", "policy.json")
	if _, err := policy.Load(polPath); err != nil {
		t.Fatalf("policy.Load(%s): %v", polPath, err)
	}

	for _, path := range []string{
		filepath.Join(home, ".claude", "agents", "plan-executor.md"),
		filepath.Join(home, ".config", "opencode", "agents", "plan-executor.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("role file %s: %v", path, err)
		}
	}

	out := string(stdout) + string(stderr)
	if !strings.Contains(out, "wrote") {
		t.Errorf("output does not contain %q:\n%s", "wrote", out)
	}
	if !strings.Contains(out, "next:") {
		t.Errorf("output does not contain %q:\n%s", "next:", out)
	}
}

func TestInitRefusesOverwriteWithoutForce(t *testing.T) {
	_, configHome := initRoot(t)

	bin := t.TempDir()
	stubBinary(t, bin, "claude")
	t.Setenv("PATH", bin)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"init"})
	}); err != nil {
		t.Fatalf("first init: %v (stderr: %s)", err, stderr)
	}

	_, stderr, err := captureOutput(t, func() error {
		return run([]string{"init"})
	})
	if err == nil {
		t.Fatal("second init without --force: want error, got nil")
	}
	if !strings.Contains(err.Error(), "--force") && !strings.Contains(string(stderr), "--force") {
		t.Fatalf("second init error %v does not mention --force (stderr: %s)", err, stderr)
	}

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"init", "--force"})
	}); err != nil {
		t.Fatalf("init --force: %v (stderr: %s)", err, stderr)
	}

	if _, err := os.Stat(filepath.Join(configHome, "relay", "candidates.json")); err != nil {
		t.Errorf("candidates.json after --force: %v", err)
	}
}

func TestInitNoBinariesExits1(t *testing.T) {
	initRoot(t)

	t.Setenv("PATH", t.TempDir())

	_, stderr, err := captureOutput(t, func() error {
		return run([]string{"init"})
	})
	if err == nil {
		t.Fatal("init with no binaries on PATH: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no harness binaries") {
		t.Fatalf("init error %v does not mention %q (stderr: %s)", err, "no harness binaries", stderr)
	}
}

func TestInitNoRolesSkipsInstall(t *testing.T) {
	home, _ := initRoot(t)

	bin := t.TempDir()
	stubBinary(t, bin, "claude")
	t.Setenv("PATH", bin)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"init", "--no-roles"})
	}); err != nil {
		t.Fatalf("init --no-roles: %v (stderr: %s)", err, stderr)
	}

	if _, err := os.Stat(filepath.Join(home, ".claude", "agents", "plan-executor.md")); err == nil {
		t.Fatal("plan-executor.md written despite --no-roles")
	}
}
