package migrate

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

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

func readFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// saveBinding writes the pre-store layout directly, <root>/<name>/bind.json,
// rather than going through the DB-backed store.
func saveBinding(t *testing.T, root, name, cwd, repo string, state store.State) {
	t.Helper()
	b := store.Binding{Name: name, CWD: cwd, Worktree: cwd, Repo: repo, State: state}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create binding dir %s: %v", name, err)
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatalf("marshal binding %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), raw, 0o644); err != nil {
		t.Fatalf("write binding %s: %v", name, err)
	}
}

// treeSnapshot maps every regular file under root to its mode and bytes.
func treeSnapshot(t *testing.T, root string) map[string]string {
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

func findStep(t *testing.T, res Result, name string) Step {
	t.Helper()
	for _, s := range res.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step named %q in %v", name, res.Steps)
	return Step{}
}

// gitFileTree makes dir a linked worktree: a directory holding a regular .git file.
func gitFileTree(t *testing.T, dir string) string {
	t.Helper()
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, ".git"), "gitdir: /nowhere/worktrees/"+filepath.Base(dir)+"\n")
	return dir
}
