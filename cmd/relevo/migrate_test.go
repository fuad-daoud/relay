package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCmdMigrateFlagPairRule checks that naming only one of --state-from and
// --state-to is a usage error, before anything is resolved or touched.
func TestCmdMigrateFlagPairRule(t *testing.T) {
	defer silenceStderr(t)()

	for _, args := range [][]string{
		{"--state-from", "/tmp/one"},
		{"--state-to", "/tmp/two"},
	} {
		err := cmdMigrate(args)
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("cmdMigrate(%v) = %v, want exit 2", args, err)
		}
	}
}

// TestCmdMigrateDryRun builds a relay-era tree and runs the dry run against
// it: every step is named, and nothing on disk changes.
func TestCmdMigrateDryRun(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	configHome := filepath.Join(root, "config")
	home := filepath.Join(root, "home")

	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", home)

	oldState := filepath.Join(stateHome, "relay")
	oldConfig := filepath.Join(configHome, "relay")
	mustMkdir(t, oldState)
	mustMkdir(t, oldConfig)
	mustWrite(t, filepath.Join(oldState, "relay.db"), "db")
	mustWrite(t, filepath.Join(oldConfig, "candidates.json"), "{}")
	// The old client unit is installed, so stop/install/retire all report.
	mustWrite(t, filepath.Join(configHome, "systemd", "user", "relay.service"), "[Unit]\n")

	before := snapshotTree(t, root)

	out := captureStdout(t, func() error {
		return cmdMigrate([]string{"--dry-run"})
	})

	for _, name := range []string{
		"pre-check", "stop", "move-config", "move-state", "rename-db",
		"rewrite-json", "rewrite-db", "repair-worktrees", "install", "retire", "old-binary",
	} {
		if !strings.Contains(out, name+":") {
			t.Errorf("dry-run output does not name %q:\n%s", name, out)
		}
	}

	if after := snapshotTree(t, root); !equalTrees(before, after) {
		t.Errorf("dry run changed the tree:\nbefore: %v\nafter:  %v", before, after)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// snapshotTree maps every regular file under root to its mode and bytes.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap[path] = fmt.Sprintf("%v|%s", info.Mode(), data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// it printed.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	runErr := fn()
	w.Close()
	os.Stdout = prev
	out := <-done
	if runErr != nil {
		t.Fatalf("cmdMigrate: %v", runErr)
	}
	return out
}

// silenceStderr redirects os.Stderr for the duration of a test and returns the
// restore function.
func silenceStderr(t *testing.T) func() {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	go func() { _, _ = io.Copy(io.Discard, r) }()
	return func() {
		w.Close()
		os.Stderr = prev
	}
}
