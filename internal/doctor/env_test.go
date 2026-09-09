package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRealEnvHomePathAndStat(t *testing.T) {
	env, err := DefaultEnv()
	if err != nil {
		t.Fatalf("DefaultEnv: %v", err)
	}

	tmp := t.TempDir()
	file := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := env.Stat(file); err != nil {
		t.Errorf("Stat existing file: %v", err)
	}

	if err := env.Stat(filepath.Join(tmp, "nonexistent")); err == nil {
		t.Error("Stat nonexistent file should error")
	}

	homePath, err := env.HomePath("some/rel/path")
	if err != nil {
		t.Fatalf("HomePath: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, "some/rel/path")
	if homePath != expected {
		t.Errorf("HomePath = %q, want %q", homePath, expected)
	}
}

func TestRealEnvLookPath(t *testing.T) {
	env, err := DefaultEnv()
	if err != nil {
		t.Fatalf("DefaultEnv: %v", err)
	}

	// sh is standard across unix systems
	path, err := env.LookPath("sh")
	if err != nil {
		t.Fatalf("LookPath(sh): %v", err)
	}
	if path == "" {
		t.Error("LookPath(sh) returned empty path")
	}
}

func TestRealEnvDaemonRunningWithoutDaemon(t *testing.T) {
	env, err := DefaultEnv()
	if err != nil {
		t.Fatalf("DefaultEnv: %v", err)
	}

	running, err := env.DaemonRunning(context.Background())
	if err != nil {
		t.Fatalf("DaemonRunning: %v", err)
	}
	_ = running // Can be true or false depending on whether daemon is running locally
}
