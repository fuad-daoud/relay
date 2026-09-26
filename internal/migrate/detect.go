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

// daemonLockFileName is checked directly, not via store.DaemonRunning, so a
// dry run can tell "no daemon held this root" without creating the file.
const daemonLockFileName = ".daemon.lock"

// detection is what the read-only detect phase found.
type detection struct {
	stateOld, stateNew bool
	configPair         bool
	confOld, confNew   bool
}

func (d detection) anyOld() bool { return d.stateOld || d.confOld }

// detect stats the four roots; a stat error other than "not there" is
// returned naming the path.
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

// refusals collects every reason to refuse, not just the first, and never
// itself writes anything.
func (r *runner) refusals(d detection) []string {
	var reasons []string
	reasons = append(reasons, r.rootConflicts(d)...)
	reasons = append(reasons, r.activeBindings(d)...)
	reasons = append(reasons, r.runningDaemon(d)...)
	return reasons
}

// rootConflicts refuses a non-empty destination, a config conflict, or a
// cross-filesystem move.
func (r *runner) rootConflicts(d detection) []string {
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
	return reasons
}

// activeBindings refuses a binding that is active, needs attention, broken,
// or mid-consult: none of those are safe to move out from under.
func (r *runner) activeBindings(d detection) []string {
	if !d.stateOld {
		return nil
	}
	o := r.opts
	var reasons []string
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
	return reasons
}

// runningDaemon refuses a live serve pointer and a held daemon lock: both
// mean a process is using the old root right now.
func (r *runner) runningDaemon(d detection) []string {
	o := r.opts
	var reasons []string

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

// pointerDirs is where a running serve daemon may have written its pointer.
func pointerDirs(o Options) []string {
	dirs := []string{filepath.Join(o.StateFrom, "serve")}
	if o.DefaultServeRoot != "" && filepath.Clean(o.DefaultServeRoot) != filepath.Clean(dirs[0]) {
		dirs = append(dirs, o.DefaultServeRoot)
	}
	return dirs
}

// storeRoots lists root itself, if it holds bindings, plus every owner
// directory under serve/bindings.
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

// holdsBindings reports whether root has a non-dot subdirectory holding a
// bind.json.
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

// qualifiedName names a binding by its path relative to stateFrom, so its
// owner directory shows, not just a bare name.
func qualifiedName(stateFrom, root, name string) string {
	rel, err := filepath.Rel(stateFrom, filepath.Join(root, name))
	if err != nil || strings.HasPrefix(rel, "..") {
		return name
	}
	return rel
}

// configMergeConflicts lists the entries of from that also exist in to and
// are not identical regular files: what a merge cannot resolve on its own.
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

func emptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) == 0
}

// pathExists reports whether path exists; only a stat error other than "not
// there" is returned.
func pathExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

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

// daemonLockFileExists checks without creating the file, unlike
// store.DaemonRunning.
func daemonLockFileExists(root string) bool {
	_, err := os.Stat(filepath.Join(root, daemonLockFileName))
	return err == nil
}

// legacySliceSeq and relevoSliceSeq are the exact quoted values `"relay.slice"` // name-guard: legacy
// and `"relevo.slice"`, quoted so that `"relay.slicer"` is left alone. // name-guard: legacy
var (
	legacySliceSeq = []byte(`"` + legacy.Slice + `"`) // "relay.slice" // name-guard: legacy
	relevoSliceSeq = []byte(`"relevo.slice"`)
)

func normalizeLegacy(b []byte) []byte {
	return bytes.ReplaceAll(b, legacySliceSeq, relevoSliceSeq)
}

// sameBytes reports whether a, once the slice rename is applied, holds the
// same bytes as b: a file that differs only by that rewrite is not a conflict.
func sameBytes(a, b string) bool {
	da, errA := os.ReadFile(a)
	db, errB := os.ReadFile(b)
	return errA == nil && errB == nil && bytes.Equal(normalizeLegacy(da), db)
}
