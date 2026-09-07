package store

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrNotFound reports a binding name with no state on disk.
var ErrNotFound = errors.New("binding not found")

// ErrCWDTaken reports that another active binding already drives that working
// tree. Two builders in one tree is the one failure that destroys work, so it
// is refused rather than warned about.
var ErrCWDTaken = errors.New("working tree already bound")

const (
	maxNameLen      = 32
	bindingFileMode = 0o644
	bindingDirMode  = 0o755
	defaultRoundCap = 20
	// defaultRoundMSecs is the round budget: 24 hours. A builder working a real
	// stage of a plan runs for hours, so a short budget flags healthy work as
	// needing a human and trains the reader to ignore the one state that means
	// act. This is a runaway guard, not a progress estimate; per-binding
	// overrides come from `relay bind --timeout`.
	defaultRoundMSecs = 86400000
	archiveDirName    = ".archive"

	// maxArchiveFileBytes bounds a single file going into an archive. Relay's
	// own state files are small; anything past this means something has gone
	// wrong, and a runaway file should fail the archive rather than balloon it.
	maxArchiveFileBytes = 64 << 20
	lockFileName        = ".lock"
	lockRetryDelay      = 50 * time.Millisecond

	// lockAcquireLimit must exceed the longest possible hold, or a slow herdr
	// turns every other caller's wait into a failure. The longest hold is
	// relay.Send's critical section, which can make two `herdr agent prompt`
	// calls (the stall retry), each bounded by the herdr client timeout of 30s
	// in cmd/relay. 70s is that worst case plus headroom; if the client
	// timeout changes, this must change with it.
	lockAcquireLimit = 70 * time.Second
)

// Store is the state directory. All writes are atomic within it.
type Store struct {
	root string

	mu sync.Mutex // serialises WithLock within this process
}

// Tx is a locked view of the store. Every method on it assumes the state lock
// is already held, which is why the lock can never be taken twice: code inside
// WithLock reaches the store only through Tx, and Tx never re-locks.
type Tx struct {
	s *Store
}

// New returns a Store rooted at root.
func New(root string) *Store {
	return &Store{root: root}
}

// DefaultRoot resolves $XDG_STATE_HOME/relay, falling back to ~/.local/state/relay.
func DefaultRoot() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "relay"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, ".local", "state", "relay"), nil
}

// ValidName enforces herdr's agent-name rule, since binding names become agent
// names: lowercase letter first, then up to 31 of [a-z0-9_-].
func ValidName(name string) error {
	if name == "" {
		return errors.New("binding name is empty")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("binding name %q exceeds %d characters", name, maxNameLen)
	}
	if name[0] < 'a' || name[0] > 'z' {
		return fmt.Errorf("binding name %q must start with a lowercase letter", name)
	}

	for i := 1; i < len(name); i++ {
		c := name[i]
		lower := c >= 'a' && c <= 'z'
		digit := c >= '0' && c <= '9'
		if !lower && !digit && c != '-' && c != '_' {
			return fmt.Errorf("binding name %q has an invalid character %q", name, string(c))
		}
	}

	return nil
}

// Dir is the state directory for one binding.
func (s *Store) Dir(name string) string { return filepath.Join(s.root, name) }

func (s *Store) roundPath(name string, round int, suffix string) string {
	return filepath.Join(s.Dir(name), fmt.Sprintf("%03d-%s.md", round, suffix))
}

// PlanPath is where the planner's plan for a round is stored.
func (s *Store) PlanPath(name string, round int) string {
	return s.roundPath(name, round, "plan")
}

// ReportPath is where the builder is told to write its report for a round.
func (s *Store) ReportPath(name string, round int) string {
	return s.roundPath(name, round, "report")
}

// QuestionPath is where a captured blocking dialog is stored.
func (s *Store) QuestionPath(name string, round int) string {
	return s.roundPath(name, round, "question")
}

func (s *Store) bindingPath(name string) string {
	return filepath.Join(s.Dir(name), "bind.json")
}

// Save writes a binding atomically, refusing a second active binding on the
// same working tree. It acquires the state lock for the operation.
func (s *Store) Save(b Binding) error {
	return s.WithLock(func(tx *Tx) error { return tx.Save(b) })
}

// Load reads one binding, acquiring the state lock for the operation.
func (s *Store) Load(name string) (Binding, error) {
	var b Binding
	err := s.WithLock(func(tx *Tx) error {
		var err error
		b, err = tx.Load(name)
		return err
	})
	return b, err
}

// List reads every binding, acquiring the state lock for the operation.
func (s *Store) List() ([]Binding, error) {
	var bindings []Binding
	err := s.WithLock(func(tx *Tx) error {
		var err error
		bindings, err = tx.List()
		return err
	})
	return bindings, err
}

// Archive moves a binding aside under the state lock, returning its new path.
func (s *Store) Archive(name string) (string, error) {
	var dest string

	err := s.WithLock(func(tx *Tx) error {
		var err error
		dest, err = tx.Archive(name)

		return err
	})
	if err != nil {
		return "", err
	}

	return dest, nil
}

// Delete removes a binding and everything under it, acquiring the state lock.
func (s *Store) Delete(name string) error {
	return s.WithLock(func(tx *Tx) error { return tx.Delete(name) })
}

// FindByCWD returns the binding driving cwd, if any.
func (s *Store) FindByCWD(cwd string) (Binding, bool, error) {
	var found bool
	var b Binding
	err := s.WithLock(func(tx *Tx) error {
		bindings, err := tx.List()
		if err != nil {
			return err
		}
		for _, binding := range bindings {
			// A done binding no longer drives its tree: resolving `relay send`
			// onto one would hand a plan to a finished session. assertCWDFree
			// scans separately, so this does not relax the two-builders-in-one
			// -tree refusal.
			if binding.CWD == cwd && binding.State != StateDone {
				b = binding
				found = true
				return nil
			}
		}
		return nil
	})
	return b, found, err
}

// Tx methods provide locked access to the store. All assume the lock is held.

// Save writes a binding under the held lock, refusing a second active binding
// on the same working tree.
func (t *Tx) Save(b Binding) error {
	return t.s.save(b)
}

// Load reads one binding under the held lock.
func (t *Tx) Load(name string) (Binding, error) {
	return t.s.load(name)
}

// List reads every binding under the held lock.
func (t *Tx) List() ([]Binding, error) {
	return t.s.list()
}

// Delete removes a binding under the held lock.
func (t *Tx) Delete(name string) error {
	return t.s.remove(name)
}

// Archive moves a binding aside under the held lock, returning its new path.
func (t *Tx) Archive(name string) (string, error) {
	return t.s.archive(name)
}

// Unexported methods implement the actual logic, assuming lock is held via Tx.

func (s *Store) save(b Binding) error {
	if err := ValidName(b.Name); err != nil {
		return err
	}
	if b.CWD == "" {
		return errors.New("binding has no working directory")
	}

	if err := s.assertCWDFree(b); err != nil {
		return err
	}

	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	if b.RoundCap == 0 {
		b.RoundCap = defaultRoundCap
	}
	if b.RoundTimeoutMS == 0 {
		b.RoundTimeoutMS = defaultRoundMSecs
	}

	if err := os.MkdirAll(s.Dir(b.Name), bindingDirMode); err != nil {
		return fmt.Errorf("create binding dir: %w", err)
	}

	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal binding %q: %w", b.Name, err)
	}

	return writeFileAtomic(s.bindingPath(b.Name), raw, bindingFileMode)
}

func (s *Store) assertCWDFree(b Binding) error {
	bindings, err := s.list()
	if err != nil {
		return err
	}
	for _, other := range bindings {
		if other.CWD == b.CWD && other.Name != b.Name && other.State != StateDone {
			return fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
				b.CWD, other.Name, other.Builder.PaneID, other.Round, ErrCWDTaken)
		}
	}
	return nil
}

func (s *Store) load(name string) (Binding, error) {
	raw, err := os.ReadFile(s.bindingPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return Binding{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Binding{}, fmt.Errorf("read binding %q: %w", name, err)
	}

	var b Binding
	if err := json.Unmarshal(raw, &b); err != nil {
		return Binding{}, fmt.Errorf("decode binding %q: %w", name, err)
	}

	return b, nil
}

func (s *Store) list() ([]Binding, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state root: %w", err)
	}

	bindings := make([]Binding, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := s.load(e.Name())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}

	return bindings, nil
}

// ArchiveDir is where archived bindings are kept. It lives inside the state
// root but List skips it, since it holds no live bindings.
func (s *Store) ArchiveDir() string { return filepath.Join(s.root, archiveDirName) }

// archive packs a binding's directory into a gzipped tarball and removes the
// directory, so the name frees for a fresh bind while log.jsonl and every
// round file survive. Relay's state is small text, which gzips well enough
// that a long history of archived bindings stays negligible on disk.
//
// It returns the path of the tarball it wrote.
func (s *Store) archive(name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	if _, err := s.load(name); err != nil {
		return "", err
	}

	if err := os.MkdirAll(s.ArchiveDir(), bindingDirMode); err != nil {
		return "", fmt.Errorf("create archive dir: %w", err)
	}

	dest := filepath.Join(s.ArchiveDir(),
		fmt.Sprintf("%s-%s.tar.gz", name, time.Now().UTC().Format("20060102-150405")))

	if err := tarGzDir(s.Dir(name), name, dest); err != nil {
		return "", err
	}

	// Only once the archive is safely on disk: losing the directory to a
	// half-written tarball would destroy the record this exists to keep.
	if err := os.RemoveAll(s.Dir(name)); err != nil {
		return "", fmt.Errorf("remove archived binding %q: %w", name, err)
	}

	return dest, nil
}

// tarGzDir writes the flat contents of dir into a gzipped tar at dest, under
// the given prefix. It streams to a temp file in the destination directory and
// renames only after a clean close, so a crash cannot leave a truncated
// archive that looks complete.
func tarGzDir(dir, prefix, dest string) (err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read binding dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-archive-*")
	if err != nil {
		return fmt.Errorf("create temp archive: %w", err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		if e.IsDir() {
			continue // binding directories are flat
		}
		if err = addArchiveFile(tw, dir, prefix, e); err != nil {
			return err
		}
	}

	if err = tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err = gz.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close archive: %w", err)
	}
	if err = os.Rename(tmp.Name(), dest); err != nil {
		return fmt.Errorf("rename archive into place: %w", err)
	}

	return nil
}

func addArchiveFile(tw *tar.Writer, dir, prefix string, e os.DirEntry) error {
	info, err := e.Info()
	if err != nil {
		return fmt.Errorf("stat %s: %w", e.Name(), err)
	}
	if info.Size() > maxArchiveFileBytes {
		return fmt.Errorf("%s is %d bytes, over the %d byte archive limit",
			e.Name(), info.Size(), maxArchiveFileBytes)
	}

	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("tar header for %s: %w", e.Name(), err)
	}
	hdr.Name = filepath.Join(prefix, e.Name())

	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header for %s: %w", e.Name(), err)
	}

	f, err := os.Open(filepath.Join(dir, e.Name()))
	if err != nil {
		return fmt.Errorf("open %s: %w", e.Name(), err)
	}
	defer f.Close()

	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("copy %s into archive: %w", e.Name(), err)
	}

	return nil
}

func (s *Store) remove(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := os.RemoveAll(s.Dir(name)); err != nil {
		return fmt.Errorf("delete binding %q: %w", name, err)
	}
	return nil
}

// WithLock runs fn while holding an exclusive advisory lock on the state root,
// passing it the only handle that can touch state while the lock is held.
// Every load-modify-save sequence must run inside it.
//
// The lock is an flock, so the kernel drops it if a holder is killed and there
// is no stale lock file to reap. Acquisition is bounded, so a wedged holder
// surfaces as an error instead of hanging the caller forever. The in-process
// mutex is held for the same span too, not because flock would let same-process
// callers corrupt state -- it would still serialise them correctly -- but so
// they block on a cheap mutex instead of busy-polling the flock.
func (s *Store) WithLock(fn func(tx *Tx) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.root, bindingDirMode); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(s.root, lockFileName), os.O_CREATE|os.O_RDWR, bindingFileMode)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}

	// Closing the descriptor releases the flock, so this defer is both the
	// unlock and the cleanup.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close state lock: %w", cerr)
		}
	}()

	if err := acquireFlock(f, lockAcquireLimit); err != nil {
		return err
	}

	return fn(&Tx{s: s})
}

// acquireFlock polls for the exclusive lock until limit elapses. The
// platform-specific half is tryLockExclusive, in lock_unix.go.
func acquireFlock(f *os.File, limit time.Duration) error {
	deadline := time.Now().Add(limit)

	for {
		locked, err := tryLockExclusive(f)
		if err != nil {
			return fmt.Errorf("lock state root: %w", err)
		}
		if locked {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("state lock still held after %s", limit)
		}

		time.Sleep(lockRetryDelay)
	}
}

// writeFileAtomic writes via a temp file in the same directory then renames, so
// a crash mid-write can never leave a truncated binding on disk.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}

	return nil
}
