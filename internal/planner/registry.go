package planner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// lockFileName is the registry's lock, held for every read-modify-write. It
// sits beside the records, exactly as store's .lock sits beside the bindings.
const lockFileName = ".lock"

// lockAcquireLimit bounds how long a caller waits for the registry lock, the
// way store's lockAcquireLimit bounds the state lock. A holder here does only
// file I/O -- no subprocess, no network -- so the wait is short and
// a long one means a wedged process, not slow work.
const lockAcquireLimit = 90 * time.Second

// lockRetryDelay is the polling interval while the lock is held elsewhere.
const lockRetryDelay = 50 * time.Millisecond

// seenRefreshInterval is how stale seen_at must be before Touch rewrites it
// (§3.1: resolving CLI calls refresh it at most once per minute).
const seenRefreshInterval = time.Minute

// Registry owns the planner records (§4.1).
type Registry interface {
	Get(id string) (Record, error)
	ByName(name string) (Record, error)
	BySession(kind, sessionID string) (Record, error)
	ByHost(pid int, startedAt int64) (Record, error)
	List() ([]Record, error)
	Create(r Record) (Record, error)
	MoveSession(id, sessionID, transcript string, now time.Time) (Record, error)
	SetHost(id string, pid int, startedAt int64) (Record, error)
	Rename(id, name string) (Record, error)
	Touch(id string, now time.Time) error
	// Forget removes a record. inUse is the caller's guard -- the CLI passes
	// one that scans the store's non-DONE bindings for PlannerID == id -- and
	// a true answer refuses the removal with ErrInUse. A nil callback refuses
	// nothing.
	Forget(id string, inUse func(id string) bool) error
}

// FileRegistry is relay's on-disk registry: one JSON record per file at
// <Root>/<id>.json, written as a temp file plus rename, so a reader never sees
// a half-written record (§3.1, §4.1).
//
// Every mutating method holds an exclusive flock on <Root>/.lock across its
// whole read-modify-write, so `relay planner init` and a concurrent `relay mcp`
// serialise and whichever runs second sees the first's record (§4.1). The lock
// is flock-based and build-tag guarded exactly as store's is, so the kernel
// drops it if a holder dies and there is no stale file to reap.
//
// Reads take no lock and create nothing: a resolver that finds no record must
// not leave a file behind, and an atomic rename means a lock-free reader sees
// either the old record or the new one, never a torn one.
type FileRegistry struct {
	// Root is the directory the records live in (store.PlannersDir). It is
	// created, mode 0700, by the first write.
	Root string

	// Now supplies the clock for records this registry writes. Nil means
	// time.Now; tests pin it.
	Now func() time.Time
}

var _ Registry = (*FileRegistry)(nil)

func (r *FileRegistry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *FileRegistry) recordPath(id string) string {
	return filepath.Join(r.Root, id+".json")
}

// withLock runs fn while holding the registry's exclusive lock, creating Root
// (mode 0700) and the lock file if they do not exist yet. Every caller that
// reads-then-writes must run inside it.
func (r *FileRegistry) withLock(fn func() error) error {
	if err := os.MkdirAll(r.Root, 0o700); err != nil {
		return fmt.Errorf("planner: create registry root: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(r.Root, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("planner: open registry lock: %w", err)
	}
	// Closing the descriptor releases the flock, so this defer is both the
	// unlock and the cleanup.
	defer f.Close()

	if err := acquireFlock(f, lockAcquireLimit); err != nil {
		return err
	}

	return fn()
}

// acquireFlock polls for the exclusive lock until limit elapses. The
// platform-specific half is tryLockExclusive, in lock_unix.go.
func acquireFlock(f *os.File, limit time.Duration) error {
	deadline := time.Now().Add(limit)

	for {
		locked, err := tryLockExclusive(f)
		if err != nil {
			return fmt.Errorf("planner: lock registry: %w", err)
		}
		if locked {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("planner: registry lock still held after %s", limit)
		}
		time.Sleep(lockRetryDelay)
	}
}

// --- reads (no lock) -------------------------------------------------------

// Get returns the record with that id, or ErrNotFound. An id that does not
// have §3.1's shape cannot name a record, so it is ErrNotFound too -- which is
// also what keeps the file name below from being a path.
func (r *FileRegistry) Get(id string) (Record, error) { return r.get(id) }

func (r *FileRegistry) get(id string) (Record, error) {
	if ValidID(id) != nil {
		return Record{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	raw, err := os.ReadFile(r.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Record{}, fmt.Errorf("planner: read %s: %w", r.recordPath(id), err)
	}

	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("planner: decode %s: %w", r.recordPath(id), err)
	}
	return rec, nil
}

// ByName returns the record with that name, or ErrNotFound.
func (r *FileRegistry) ByName(name string) (Record, error) { return r.byName(name) }

func (r *FileRegistry) byName(name string) (Record, error) {
	records, err := r.list()
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if rec.Name == name {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("%s: %w", name, ErrNotFound)
}

// BySession returns the record whose CURRENT session is (kind, sessionID), or
// ErrNotFound. Earlier sessions in a record's history never match.
func (r *FileRegistry) BySession(kind, sessionID string) (Record, error) {
	return r.bySession(kind, sessionID)
}

func (r *FileRegistry) bySession(kind, sessionID string) (Record, error) {
	records, err := r.list()
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if rec.HarnessKind == kind && rec.SessionID == sessionID {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("%s/%s: %w", kind, sessionID, ErrNotFound)
}

// ByHost returns the record holding that live (pid, startedAt), or
// ErrNotFound. Both must match, and pid must be positive: a record with no
// host (explicit registration) never matches, and a pid alone is not identity
// -- the machine reuses pids, which is what startedAt is for (§4.1).
func (r *FileRegistry) ByHost(pid int, startedAt int64) (Record, error) {
	return r.byHost(pid, startedAt)
}

func (r *FileRegistry) byHost(pid int, startedAt int64) (Record, error) {
	if pid <= 0 {
		return Record{}, fmt.Errorf("host pid %d: %w", pid, ErrNotFound)
	}
	records, err := r.list()
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if rec.HostPID == pid && rec.HostStartedAt == startedAt {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("host %d@%d: %w", pid, startedAt, ErrNotFound)
}

// List returns every record, sorted by name. A missing root is an empty
// registry, not an error.
func (r *FileRegistry) List() ([]Record, error) { return r.list() }

func (r *FileRegistry) list() ([]Record, error) {
	entries, err := os.ReadDir(r.Root)
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("planner: read registry root: %w", err)
	}

	records := make([]Record, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(r.Root, name))
		if rerr != nil {
			return nil, fmt.Errorf("planner: read %s: %w", filepath.Join(r.Root, name), rerr)
		}
		var rec Record
		if uerr := json.Unmarshal(raw, &rec); uerr != nil {
			// A corrupt record is loud and names its file (§6.1): silently
			// skipping it would turn "your registry is broken" into "unknown
			// planner", which sends the reader chasing the wrong problem.
			return nil, fmt.Errorf("planner: decode %s: %w", filepath.Join(r.Root, name), uerr)
		}
		records = append(records, rec)
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

// --- writes (locked) -------------------------------------------------------

// Create adds a record, refusing a name, session or live host another record
// already holds, and refusing invalid input. CreatedAt and SeenAt default to
// the registry's clock when the caller leaves them zero.
func (r *FileRegistry) Create(rec Record) (Record, error) {
	var out Record
	err := r.withLock(func() error {
		var e error
		out, e = r.create(rec)
		return e
	})
	return out, err
}

func (r *FileRegistry) create(rec Record) (Record, error) {
	now := r.now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.SeenAt.IsZero() {
		rec.SeenAt = now
	}
	if err := rec.Validate(); err != nil {
		return Record{}, err
	}

	records, err := r.list()
	if err != nil {
		return Record{}, err
	}
	for _, other := range records {
		switch {
		case other.ID == rec.ID:
			return Record{}, fmt.Errorf("planner: id %s already exists: %w", rec.ID, ErrInvalid)
		case other.Name == rec.Name:
			return Record{}, fmt.Errorf("planner: name %s: %w", rec.Name, ErrNameTaken)
		case other.HarnessKind == rec.HarnessKind && other.SessionID == rec.SessionID:
			return Record{}, fmt.Errorf("planner: session %s/%s: %w", rec.HarnessKind, rec.SessionID, ErrSessionTaken)
		case rec.HostPID > 0 && other.HostPID == rec.HostPID && other.HostStartedAt == rec.HostStartedAt:
			return Record{}, fmt.Errorf("planner: host %d@%d: %w", rec.HostPID, rec.HostStartedAt, ErrHostTaken)
		}
	}

	if err := r.write(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// MoveSession points the record at a new session and appends the old one to
// its history with To = now (§3.1). A session another record holds is
// ErrSessionTaken.
func (r *FileRegistry) MoveSession(id, sessionID, transcript string, now time.Time) (Record, error) {
	var out Record
	err := r.withLock(func() error {
		var e error
		out, e = r.moveSession(id, sessionID, transcript, now)
		return e
	})
	return out, err
}

func (r *FileRegistry) moveSession(id, sessionID, transcript string, now time.Time) (Record, error) {
	rec, err := r.get(id)
	if err != nil {
		return Record{}, err
	}
	if now.IsZero() {
		now = r.now()
	}

	records, err := r.list()
	if err != nil {
		return Record{}, err
	}
	for _, other := range records {
		if other.ID == rec.ID {
			continue
		}
		if other.HarnessKind == rec.HarnessKind && other.SessionID == sessionID {
			return Record{}, fmt.Errorf("planner: session %s/%s: %w", rec.HarnessKind, sessionID, ErrSessionTaken)
		}
	}

	if rec.SessionID != sessionID {
		rec.Sessions = appendSession(rec.Sessions, SessionRef{
			SessionID: rec.SessionID,
			From:      rec.sessionStartedAt(),
			To:        now,
		})
		rec.SessionID = sessionID
	}
	// The hook's transcript_path for the new session replaces the old one when
	// it carries one; an empty path leaves the stored locator alone rather
	// than blanking a value that is still the best relay has.
	if transcript != "" {
		rec.TranscriptLocator = transcript
	}

	if err := rec.Validate(); err != nil {
		return Record{}, err
	}
	if err := r.write(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// SetHost points the record at a harness process, refusing a live host another
// record already holds. pid 0 means "unknown" and clears the start time with
// it.
func (r *FileRegistry) SetHost(id string, pid int, startedAt int64) (Record, error) {
	var out Record
	err := r.withLock(func() error {
		var e error
		out, e = r.setHost(id, pid, startedAt)
		return e
	})
	return out, err
}

func (r *FileRegistry) setHost(id string, pid int, startedAt int64) (Record, error) {
	rec, err := r.get(id)
	if err != nil {
		return Record{}, err
	}
	if pid < 0 {
		return Record{}, fmt.Errorf("planner: host_pid %d must be >= 0: %w", pid, ErrInvalid)
	}
	if pid == 0 {
		startedAt = 0
	}

	if pid > 0 {
		records, err := r.list()
		if err != nil {
			return Record{}, err
		}
		for _, other := range records {
			if other.ID == rec.ID {
				continue
			}
			if other.HostPID == pid && other.HostStartedAt == startedAt {
				return Record{}, fmt.Errorf("planner: host %d@%d: %w", pid, startedAt, ErrHostTaken)
			}
		}
	}

	rec.HostPID = pid
	rec.HostStartedAt = startedAt
	if err := rec.Validate(); err != nil {
		return Record{}, err
	}
	if err := r.write(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Rename gives a record a new name, refusing one another record holds.
func (r *FileRegistry) Rename(id, name string) (Record, error) {
	var out Record
	err := r.withLock(func() error {
		var e error
		out, e = r.rename(id, name)
		return e
	})
	return out, err
}

func (r *FileRegistry) rename(id, name string) (Record, error) {
	if err := ValidName(name); err != nil {
		return Record{}, err
	}
	rec, err := r.get(id)
	if err != nil {
		return Record{}, err
	}

	records, err := r.list()
	if err != nil {
		return Record{}, err
	}
	for _, other := range records {
		if other.ID != rec.ID && other.Name == name {
			return Record{}, fmt.Errorf("planner: name %s: %w", name, ErrNameTaken)
		}
	}

	rec.Name = name
	if err := r.write(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Touch records that a planner was heard from, at most once per minute (§3.1).
// It is best effort by contract -- callers ignore the error -- and it only
// moves seen_at forward.
func (r *FileRegistry) Touch(id string, now time.Time) error {
	return r.withLock(func() error { return r.touch(id, now) })
}

func (r *FileRegistry) touch(id string, now time.Time) error {
	rec, err := r.get(id)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = r.now()
	}
	if now.Before(rec.SeenAt) || now.Sub(rec.SeenAt) < seenRefreshInterval {
		return nil
	}
	rec.SeenAt = now
	return r.write(rec)
}

// touchForced stamps seen_at with no throttle and returns the updated record.
// `relay planner init` always writes -- its postcondition is "its seen_at is
// now" (§4.4) -- so it cannot ride the once-a-minute rule Touch applies to
// resolving calls.
func (r *FileRegistry) touchForced(id string, now time.Time) (Record, error) {
	rec, err := r.get(id)
	if err != nil {
		return Record{}, err
	}
	if !now.After(rec.SeenAt) {
		return rec, nil
	}
	rec.SeenAt = now
	if err := r.write(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Forget removes a record. inUse is the caller's liveness guard: a true answer
// refuses with ErrInUse rather than deleting a record a live binding names.
func (r *FileRegistry) Forget(id string, inUse func(id string) bool) error {
	return r.withLock(func() error { return r.forget(id, inUse) })
}

func (r *FileRegistry) forget(id string, inUse func(id string) bool) error {
	rec, err := r.get(id)
	if err != nil {
		return err
	}
	if inUse != nil && inUse(rec.ID) {
		return fmt.Errorf("planner %s (%s) is named by a binding that is not done: %w", rec.Name, rec.ID, ErrInUse)
	}
	if err := os.Remove(r.recordPath(rec.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("planner: remove %s: %w", rec.ID, err)
	}
	return nil
}

// write puts one record on disk: a temp file in the registry root, then a
// rename, so a crash never leaves a truncated record.
func (r *FileRegistry) write(rec Record) error {
	if err := os.MkdirAll(r.Root, 0o700); err != nil {
		return fmt.Errorf("planner: create registry root: %w", err)
	}

	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("planner: marshal %s: %w", rec.ID, err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(r.Root, ".tmp-*")
	if err != nil {
		return fmt.Errorf("planner: create temp file in %s: %w", r.Root, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("planner: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("planner: close temp file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("planner: chmod temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), r.recordPath(rec.ID)); err != nil {
		return fmt.Errorf("planner: rename into place: %w", err)
	}
	return nil
}
