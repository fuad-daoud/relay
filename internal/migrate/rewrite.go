package migrate

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RewriteJSON rewrites every old-root path prefix inside the JSON records
// under root, in place, and returns how many files changed.
//
// Only regular files named *.json or *.jsonl are candidates. Whole subtrees
// that hold user repositories rather than relevo records are skipped:
//
//   - any directory named .worktrees;
//   - the repos and worktrees directories directly under a directory named
//     serve;
//   - any directory containing a .git entry.
//
// In each candidate file, for each pair, the byte sequence `"<Old>/` becomes
// `"<New>/` and `"<Old>"` becomes `"<New>"`. Only a JSON string value that
// starts with the path changes, and nothing else moves.
//
// A changed file is replaced atomically -- a temp file in the same directory,
// the original's mode, then a rename -- so a crash cannot truncate a record.
// An unchanged file is never touched, mtime included. The walk stops at the
// first I/O error; files already rewritten stay rewritten, and a re-run is
// idempotent.
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

// skipJSONDir reports whether a directory is one of the whole subtrees the
// rewrite must not enter.
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

// rewriteJSONFile applies every pair to one file and replaces it atomically if
// anything changed. It reports whether the file changed.
func rewriteJSONFile(path string, pairs []Prefix) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	out := data
	for _, p := range pairs {
		out = bytes.ReplaceAll(out, []byte(`"`+p.Old+`/`), []byte(`"`+p.New+`/`))
		out = bytes.ReplaceAll(out, []byte(`"`+p.Old+`"`), []byte(`"`+p.New+`"`))
	}
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
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
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
