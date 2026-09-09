package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestRealEnvHomePathAndStat(t *testing.T) {
	env := NewEnv(herdr.NewClient("", 0), store.New(t.TempDir()))

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
	env := NewEnv(herdr.NewClient("", 0), store.New(t.TempDir()))

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
	env := NewEnv(herdr.NewClient("", 0), store.New(t.TempDir()))

	running, err := env.DaemonRunning(context.Background())
	if err != nil {
		t.Fatalf("DaemonRunning: %v", err)
	}
	if running {
		t.Error("DaemonRunning on fresh temp root must be false")
	}
}
