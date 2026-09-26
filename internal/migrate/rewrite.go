package migrate

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/legacy"
)

// RewriteJSON rewrites every old-root path prefix inside the *.json and
// *.jsonl records under root, in place, and returns how many files changed.
// It skips whole subtrees that hold user repositories rather than relevo
// records: any .worktrees directory, repos/worktrees directly under a
// directory named serve, and any directory containing a .git entry.
//
// For each pair, `"<Old>/` becomes `"<New>/` and `"<Old>"` becomes `"<New>"`.
// A changed file is replaced atomically; an unchanged file is never touched,
// mtime included, which keeps a re-run after a walk error idempotent.
func RewriteJSON(root string, pairs []Prefix) (files int, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if skipJSONDir(path, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		changed, err := rewriteJSONFile(path, pairs)
		if err != nil {
			return err
		}
		if changed {
			files++
		}
		return nil
	})
	if err != nil {
		return files, err
	}
	return files, nil
}

func skipJSONDir(path, name string) bool {
	if name == ".worktrees" {
		return true
	}
	if name == "repos" || name == "worktrees" {
		if filepath.Base(filepath.Dir(path)) == "serve" {
			return true
		}
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	return false
}

func rewriteJSONFile(path string, pairs []Prefix) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	out := legacy.RewriteJSON(data, pairs)
	if bytes.Equal(out, data) {
		return false, nil
	}

	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), fi.Mode().Perm()); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}
