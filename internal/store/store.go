package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ErrNotFound reports a binding name with no state on disk.
var ErrNotFound = errors.New("binding not found")

// ErrCWDTaken reports that another active binding already drives that working
// tree. Two builders in one tree is the one failure that destroys work, so it
// is refused rather than warned about.
var ErrCWDTaken = errors.New("working tree already bound")

const (
	maxNameLen        = 32
	bindingFileMode   = 0o644
	bindingDirMode    = 0o755
	defaultRoundCap   = 20
	defaultRoundMSecs = 1800000
	lockFileName      = ".lock"
	lockRetryDelay    = 50 * time.Millisecond
	lockAcquireLimit  = 5 * time.Second
)

// Store is the state directory. All writes are atomic within it.
type Store struct {
	root string

	mu   sync.Mutex // guards held and serializes WithLock calls
	held bool       // true while this Store holds the state lock
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
// same working tree. It takes the state lock unless the caller already holds
// it, so a bare Save is safe and a Save inside WithLock does not deadlock.
func (s *Store) Save(b Binding) error {
	return s.WithLock(func() error { return s.save(b) })
}

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
	other, found, err := s.FindByCWD(b.CWD)
	if err != nil {
		return err
	}
	if found && other.Name != b.Name && other.State != StateDone {
		return fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
			b.CWD, other.Name, other.Builder.PaneID, other.Round, ErrCWDTaken)
	}
	return nil
}

// Load reads one binding.
func (s *Store) Load(name string) (Binding, error) {
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

// List reads every binding, skipping directories without a bind.json.
func (s *Store) List() ([]Binding, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state root: %w", err)
	}

	bindings := make([]Binding, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := s.Load(e.Name())
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

// FindByCWD returns the binding driving cwd, if any.
func (s *Store) FindByCWD(cwd string) (Binding, bool, error) {
	bindings, err := s.List()
	if err != nil {
		return Binding{}, false, err
	}

	for _, b := range bindings {
		if b.CWD == cwd {
			return b, true, nil
		}
	}

	return Binding{}, false, nil
}

// Delete removes a binding and everything under it.
func (s *Store) Delete(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := os.RemoveAll(s.Dir(name)); err != nil {
		return fmt.Errorf("delete binding %q: %w", name, err)
	}
	return nil
}

// WithLock runs fn while holding an exclusive advisory lock on the state root.
// Every load-modify-save sequence must run inside it.
//
// The lock is an flock, so the kernel drops it if a holder is killed and there
// is no stale lock file to reap. Acquisition is bounded, so a wedged holder
// surfaces as an error instead of hanging the caller forever.
func (s *Store) WithLock(fn func() error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.held {
		// Already holding lock in this goroutine, call fn directly to avoid deadlock
		return fn()
	}
	s.held = true
	defer func() {
		s.held = false
	}()

	if err := os.MkdirAll(s.root, bindingDirMode); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(s.root, lockFileName), os.O_CREATE|os.O_RDWR, bindingFileMode)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	defer f.Close()

	if err := acquireFlock(f, lockAcquireLimit); err != nil {
		return err
	}

	return fn()
}

// acquireFlock polls for the exclusive lock until limit elapses.
func acquireFlock(f *os.File, limit time.Duration) error {
	deadline := time.Now().Add(limit)

	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("lock state root: %w", err)
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
