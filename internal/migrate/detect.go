package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// daemonLockFileName is the daemon lock's file name, the private constant
// internal/store opens. It is repeated here only so the refusals can tell "no
// daemon ever held this root" from "a daemon may hold it" without asking
// store.DaemonRunning, which creates the file and would make a dry run write.
const daemonLockFileName = ".daemon.lock"

// detection is what the read-only detect phase found.
type detection struct {
	stateOld, stateNew bool
	configPair         bool
	confOld, confNew   bool
}

// anyOld reports whether anything old exists at all. Nothing old means nothing
// to migrate: not even a refusal is due.
func (d detection) anyOld() bool { return d.stateOld || d.confOld }

// detect stats the four roots. A stat error other than "not there" is returned
// naming the path: a root relevo cannot stat is not safe to run beside.
func detect(o Options) (detection, error) {
	var d detection
	var err error

	if d.stateOld, err = pathExists(o.StateFrom); err != nil {
		return d, err
	}
	if d.stateNew, err = pathExists(o.StateTo); err != nil {
		return d, err
	}
	if o.ConfigFrom != "" {
		d.configPair = true
		if d.confOld, err = pathExists(o.ConfigFrom); err != nil {
			return d, err
		}
		if d.confNew, err = pathExists(o.ConfigTo); err != nil {
			return d, err
		}
	}
	return d, nil
}

// refusals collects every reason the migration must not run, not just the
// first. It is read-only: it stats, reads bindings and reads the serve pointer,
// and never creates a directory or a lock file.
func (r *runner) refusals(d detection) []string {
	o := r.opts
	var reasons []string

	if d.stateOld && d.stateNew && !emptyDir(o.StateTo) {
		reasons = append(reasons, fmt.Sprintf("%s already exists and is not empty", o.StateTo))
	}

	if d.configPair && d.confOld && d.confNew {
		reasons = append(reasons, configMergeConflicts(o.ConfigFrom, o.ConfigTo)...)
	}

	if d.stateOld && !sameFS(o.StateFrom, filepath.Dir(o.StateTo)) {
		reasons = append(reasons, fmt.Sprintf("%s and %s are on different filesystems", o.StateFrom, o.StateTo))
	}
	if d.configPair && d.confOld && !sameFS(o.ConfigFrom, filepath.Dir(o.ConfigTo)) {
		reasons = append(reasons, fmt.Sprintf("%s and %s are on different filesystems", o.ConfigFrom, o.ConfigTo))
	}

	if d.stateOld {
		for _, root := range storeRoots(o.StateFrom) {
			bindings, err := store.ListFiles(root)
			if err != nil {
				reasons = append(reasons, fmt.Sprintf("cannot read bindings in %s: %v", root, err))
				continue
			}
			for _, b := range bindings {
				switch b.State {
				case store.StateActive, store.StateNeedsYou, store.StateBroken:
					reasons = append(reasons, fmt.Sprintf(
						"binding %s is %s; finish it (%s done) or pause it first",
						qualifiedName(o.StateFrom, root, b.Name), b.State, legacy.Binary))
				}
				for _, c := range b.Consults {
					if c.State == store.ConsultRunning {
						reasons = append(reasons, fmt.Sprintf("binding %s has a running consult", b.Name))
						break
					}
				}
			}
		}
	}

	for _, dir := range pointerDirs(o) {
		p, ok, err := serve.ReadPointer(dir)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("cannot read the serve pointer in %s: %v", dir, err))
			continue
		}
		if ok && o.Alive(p.PID) {
			reasons = append(reasons, fmt.Sprintf(
				"a %s serve daemon (pid %d) is running on %s; stop it first",
				legacy.Binary, p.PID, p.Root))
		}
	}

	// The daemon lock is only worth checking when the old state root exists: a
	// root that is not there cannot be held, and DaemonRunning would create it.
	if d.stateOld && !o.SkipDaemonCheck && daemonLockFileExists(o.StateFrom) {
		running, err := store.New(o.StateFrom).DaemonRunning()
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("cannot check the daemon lock in %s: %v", o.StateFrom, err))
		} else if running {
			reasons = append(reasons, fmt.Sprintf(
				"a %s daemon holds %s; stop it first (systemctl --user stop %s)",
				legacy.Binary, o.StateFrom, legacy.ClientUnit))
		}
	}

	return reasons
}

// pointerDirs is where a running serve daemon may have written its pointer:
// the old state root's serve directory, and the old default serve root, which
// is where a daemon serving an explicit --state writes.
func pointerDirs(o Options) []string {
	dirs := []string{filepath.Join(o.StateFrom, "serve")}
	if o.DefaultServeRoot != "" && filepath.Clean(o.DefaultServeRoot) != filepath.Clean(dirs[0]) {
		dirs = append(dirs, o.DefaultServeRoot)
	}
	return dirs
}

// storeRoots lists the store roots inside the old state root: the root itself
// when it holds bindings directly, plus every owner directory under
// serve/bindings, where a server keeps its bindings.
func storeRoots(root string) []string {
	var roots []string
	if holdsBindings(root) {
		roots = append(roots, root)
	}

	bindingsDir := filepath.Join(root, "serve", "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		return roots
	}
	for _, e := range entries {
		if e.IsDir() {
			roots = append(roots, filepath.Join(bindingsDir, e.Name()))
		}
	}
	return roots
}

// holdsBindings reports whether root has at least one binding directory: a
// non-dot subdirectory holding a bind.json.
func holdsBindings(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "bind.json")); err == nil {
			return true
		}
	}
	return false
}

// qualifiedName names a binding found under a store root by its path relative
// to the old state root, so a server binding shows its owner directory rather
// than a bare name that could appear in several roots.
func qualifiedName(stateFrom, root, name string) string {
	rel, err := filepath.Rel(stateFrom, filepath.Join(root, name))
	if err != nil || strings.HasPrefix(rel, "..") {
		return name
	}
	return rel
}

// configMergeConflicts lists the top-level entries of from that also exist in
// to and are not identical regular files: those are the ones a merge cannot
// resolve on its own.
func configMergeConflicts(from, to string) []string {
	entries, err := os.ReadDir(from)
	if err != nil {
		return nil
	}

	var reasons []string
	for _, e := range entries {
		dst := filepath.Join(to, e.Name())
		di, err := os.Lstat(dst)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("cannot stat %s: %v", dst, err))
			continue
		}
		src := filepath.Join(from, e.Name())
		if di.Mode().IsRegular() && e.Type().IsRegular() && sameBytes(src, dst) {
			continue
		}
		reasons = append(reasons, fmt.Sprintf(
			"%s and %s both exist and differ; keep one and delete the other", src, dst))
	}
	return reasons
}

// emptyDir reports whether path is a directory with no entries.
func emptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) == 0
}

// pathExists reports whether path exists; a path that is not a directory
// counts as existing. Only a stat error other than "not there" is returned.
func pathExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// pathUnder reports whether path is root itself or inside it.
func pathUnder(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != "" && !strings.HasPrefix(rel, "..")
}

// daemonLockFileExists reports whether the state root carries a daemon lock
// file at all. The file is created only by a daemon taking the lock, so its
// absence is proof no daemon holds the root -- and checking it first keeps a
// dry run from creating the file via store.DaemonRunning.
func daemonLockFileExists(root string) bool {
	_, err := os.Stat(filepath.Join(root, daemonLockFileName))
	return err == nil
}

// legacySliceSeq and relevoSliceSeq are the exact quoted JSON values the slice
// rename rewrites: `"relay.slice"` becomes `"relevo.slice"` (#292 §3 step 6). // name-guard: legacy
// The quotes make the rule exact, so `"relay.slicer"` is left alone. // name-guard: legacy
var (
	legacySliceSeq = []byte(`"` + legacy.Slice + `"`) // "relay.slice" // name-guard: legacy
	relevoSliceSeq = []byte(`"relevo.slice"`)
)

// normalizeLegacy replaces every exact legacy slice value in b with relevo's.
// It is the byte rule RenameSliceValue applies and the config merge compares
// through: a file whose only difference is that rewrite is not a conflict,
// because migrate's own result would be identical (#292).
func normalizeLegacy(b []byte) []byte {
	return bytes.ReplaceAll(b, legacySliceSeq, relevoSliceSeq)
}

// sameBytes reports whether the old file a, once the slice rename is applied to
// it, holds the same bytes as the new file b. The config merge compares an old
// entry with the new one this way: a file that differs only by the rename
// migrate would make anyway is not a conflict (#292).
func sameBytes(a, b string) bool {
	da, errA := os.ReadFile(a)
	db, errB := os.ReadFile(b)
	return errA == nil && errB == nil && bytes.Equal(normalizeLegacy(da), db)
}
