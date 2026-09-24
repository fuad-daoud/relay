package planner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// plannerKeyPrefix is the kv prefix every record's row shares: one record is
// one `planner/<id>` row holding exactly the JSON the file held (P3b round 2
// §3).
const plannerKeyPrefix = "planner/"

// registryKey is the row a record's JSON lives under.
func registryKey(id string) string { return plannerKeyPrefix + id }

// lockFileName is the pre-database registry's lock file, removed by the
// import. It has no meaning over kv rows: the flock is gone with FileRegistry.
const lockFileName = ".lock"

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

// DBRegistry is relevo's registry over the store root database's kv rows: one
// JSON record per `planner/<id>` row, holding the same document the file under
// <Root>/<id>.json held (P3b round 2 §4.1).
//
// Every mutating method runs its whole read-modify-write inside one
// DBTxKV.Tx (BEGIN IMMEDIATE), replacing FileRegistry's flock: `relevo planner
// init` and a concurrent `relevo mcp`, or two hook firings, serialise, and
// whichever runs second sees the first's record (§4.1). A read takes no
// transaction and creates nothing.
type DBRegistry struct {
	// KV is the kv handle the records live in: the store root's database.
	KV db.DBTxKV

	// Now supplies the clock for records this registry writes. Nil means
	// time.Now; tests pin it.
	Now func() time.Time

	// Root is the pre-database planners directory (store.PlannersDir) the
	// import reads once per registry. "" imports nothing.
	Root string

	importOnce sync.Once
	importErr  error
}

var _ Registry = (*DBRegistry)(nil)

func (r *DBRegistry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// ensureImported adopts the pre-database record files once per registry: every
// <Root>/*.json present is put to planner/<id> and removed, and then the lock
// file and -- when nothing is left in it -- the directory itself go (§4.1). A
// malformed record file fails the import loudly and stays where it is, exactly
// as a corrupt file failed list before. It is a no-op with no Root.
func (r *DBRegistry) ensureImported() error {
	if r.Root == "" {
		return nil
	}
	r.importOnce.Do(func() { r.importErr = r.importFiles() })
	return r.importErr
}

func (r *DBRegistry) importFiles() error {
	entries, err := os.ReadDir(r.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("planner: read registry root: %w", err)
	}

	for _, e := range entries {
		name := e.Name()
		// The same filter the directory scan applied: a directory, a
		// dot-prefixed name (.lock, .agy) and anything without the .json
		// extension is not a record.
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if _, _, err := db.KVImportFile(r.KV, registryKey(id), filepath.Join(r.Root, name)); err != nil {
			return err
		}
	}

	// The lock has no meaning over kv rows, and an empty directory must not
	// linger. A directory still holding .agy/ -- which the agy credential
	// import empties (§4.3) -- is not empty, so it stays.
	_ = os.Remove(filepath.Join(r.Root, lockFileName))
	_ = os.Remove(r.Root)
	return nil
}

// ops is initLocked's lock-free view of this registry over one transaction, so
// the whole `planner init` sequence is one BEGIN IMMEDIATE.
func (r *DBRegistry) ops(kv db.KVTx) regOps { return kvOps{reg: r, kv: kv} }

// kvOps runs the registry's lock-free operations over one KVTx.
type kvOps struct {
	reg *DBRegistry
	kv  db.KVTx
}

func (o kvOps) get(id string) (Record, error) { return o.reg.getFrom(o.kv, id) }

func (o kvOps) byName(name string) (Record, error) { return o.reg.byNameFrom(o.kv, name) }

func (o kvOps) byHost(pid int, startedAt int64) (Record, error) {
	return o.reg.byHostFrom(o.kv, pid, startedAt)
}

func (o kvOps) bySession(kind, sessionID string) (Record, error) {
	return o.reg.bySessionFrom(o.kv, kind, sessionID)
}

func (o kvOps) create(r Record) (Record, error) { return o.reg.createIn(o.kv, r) }

func (o kvOps) moveSession(id, sessionID, transcript string, now time.Time) (Record, error) {
	return o.reg.moveSessionIn(o.kv, id, sessionID, transcript, now)
}

func (o kvOps) setHost(id string, pid int, startedAt int64) (Record, error) {
	return o.reg.setHostIn(o.kv, id, pid, startedAt)
}

func (o kvOps) rename(id, name string) (Record, error) { return o.reg.renameIn(o.kv, id, name) }

func (o kvOps) touchForced(id string, now time.Time) (Record, error) {
	return o.reg.touchForcedIn(o.kv, id, now)
}

// decodeRecord decodes one row's document. A corrupt record is loud and names
// its key (§6.1): silently skipping it would turn "your registry is broken"
// into "unknown planner", which sends the reader chasing the wrong problem.
func decodeRecord(key string, raw []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("planner: decode %s: %w", key, err)
	}
	return rec, nil
}

// --- reads (no transaction) -------------------------------------------------

// Get returns the record with that id, or ErrNotFound. An id that does not
// have §3.1's shape cannot name a record, so it is ErrNotFound too.
func (r *DBRegistry) Get(id string) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	return r.getFrom(r.KV, id)
}

func (r *DBRegistry) getFrom(kv db.KVTx, id string) (Record, error) {
	if ValidID(id) != nil {
		return Record{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	raw, ok, err := kv.KVGet(registryKey(id))
	if err != nil {
		return Record{}, fmt.Errorf("planner: read %s: %w", registryKey(id), err)
	}
	if !ok {
		return Record{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return decodeRecord(registryKey(id), raw)
}

// ByName returns the record with that name, or ErrNotFound.
func (r *DBRegistry) ByName(name string) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	return r.byNameFrom(r.KV, name)
}

func (r *DBRegistry) byNameFrom(kv db.KVTx, name string) (Record, error) {
	records, err := r.listFrom(kv)
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
func (r *DBRegistry) BySession(kind, sessionID string) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	return r.bySessionFrom(r.KV, kind, sessionID)
}

func (r *DBRegistry) bySessionFrom(kv db.KVTx, kind, sessionID string) (Record, error) {
	records, err := r.listFrom(kv)
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

// ByHost returns the record holding that live (pid, startedAt), or ErrNotFound.
// Both must match, and pid must be positive: a record with no host (explicit
// registration) never matches, and a pid alone is not identity -- the machine
// reuses pids, which is what startedAt is for (§4.1).
func (r *DBRegistry) ByHost(pid int, startedAt int64) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	return r.byHostFrom(r.KV, pid, startedAt)
}

func (r *DBRegistry) byHostFrom(kv db.KVTx, pid int, startedAt int64) (Record, error) {
	if pid <= 0 {
		return Record{}, fmt.Errorf("host pid %d: %w", pid, ErrNotFound)
	}
	records, err := r.listFrom(kv)
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

// List returns every record, sorted by name. No rows is an empty registry, not
// an error.
func (r *DBRegistry) List() ([]Record, error) {
	if err := r.ensureImported(); err != nil {
		return nil, err
	}
	return r.listFrom(r.KV)
}

func (r *DBRegistry) listFrom(kv db.KVTx) ([]Record, error) {
	keys, err := kv.KVKeys(plannerKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("planner: list records: %w", err)
	}

	records := make([]Record, 0, len(keys))
	for _, key := range keys {
		raw, ok, gerr := kv.KVGet(key)
		if gerr != nil {
			return nil, fmt.Errorf("planner: read %s: %w", key, gerr)
		}
		if !ok {
			continue
		}
		rec, derr := decodeRecord(key, raw)
		if derr != nil {
			return nil, derr
		}
		records = append(records, rec)
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

// --- writes (one transaction each) ------------------------------------------

// Create adds a record, refusing a name, session or live host another record
// already holds, and refusing invalid input. CreatedAt and SeenAt default to
// the registry's clock when the caller leaves them zero.
func (r *DBRegistry) Create(rec Record) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.KV.Tx(func(tx db.KVTx) error {
		var e error
		out, e = r.createIn(tx, rec)
		return e
	})
	return out, err
}

func (r *DBRegistry) createIn(kv db.KVTx, rec Record) (Record, error) {
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

	records, err := r.listFrom(kv)
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

	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// MoveSession points the record at a new session and appends the old one to
// its history with To = now (§3.1). A session another record holds is
// ErrSessionTaken.
func (r *DBRegistry) MoveSession(id, sessionID, transcript string, now time.Time) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.KV.Tx(func(tx db.KVTx) error {
		var e error
		out, e = r.moveSessionIn(tx, id, sessionID, transcript, now)
		return e
	})
	return out, err
}

func (r *DBRegistry) moveSessionIn(kv db.KVTx, id, sessionID, transcript string, now time.Time) (Record, error) {
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return Record{}, err
	}
	if now.IsZero() {
		now = r.now()
	}

	records, err := r.listFrom(kv)
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
	// than blanking a value that is still the best relevo has.
	if transcript != "" {
		rec.TranscriptLocator = transcript
	}

	if err := rec.Validate(); err != nil {
		return Record{}, err
	}
	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// SetHost points the record at a harness process, refusing a live host another
// record already holds. pid 0 means "unknown" and clears the start time with
// it.
func (r *DBRegistry) SetHost(id string, pid int, startedAt int64) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.KV.Tx(func(tx db.KVTx) error {
		var e error
		out, e = r.setHostIn(tx, id, pid, startedAt)
		return e
	})
	return out, err
}

func (r *DBRegistry) setHostIn(kv db.KVTx, id string, pid int, startedAt int64) (Record, error) {
	rec, err := r.getFrom(kv, id)
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
		records, err := r.listFrom(kv)
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
	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Rename gives a record a new name, refusing one another record holds.
func (r *DBRegistry) Rename(id, name string) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.KV.Tx(func(tx db.KVTx) error {
		var e error
		out, e = r.renameIn(tx, id, name)
		return e
	})
	return out, err
}

func (r *DBRegistry) renameIn(kv db.KVTx, id, name string) (Record, error) {
	if err := ValidName(name); err != nil {
		return Record{}, err
	}
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return Record{}, err
	}

	records, err := r.listFrom(kv)
	if err != nil {
		return Record{}, err
	}
	for _, other := range records {
		if other.ID != rec.ID && other.Name == name {
			return Record{}, fmt.Errorf("planner: name %s: %w", name, ErrNameTaken)
		}
	}

	rec.Name = name
	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Touch records that a planner was heard from, at most once per minute (§3.1).
// It is best effort by contract -- callers ignore the error -- and it only
// moves seen_at forward.
func (r *DBRegistry) Touch(id string, now time.Time) error {
	if err := r.ensureImported(); err != nil {
		return err
	}
	return r.KV.Tx(func(tx db.KVTx) error { return r.touchIn(tx, id, now) })
}

func (r *DBRegistry) touchIn(kv db.KVTx, id string, now time.Time) error {
	rec, err := r.getFrom(kv, id)
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
	return r.writeIn(kv, rec)
}

// touchForcedIn stamps seen_at with no throttle and returns the updated
// record. `relevo planner init` always writes -- its postcondition is "its
// seen_at is now" (§4.4) -- so it cannot ride the once-a-minute rule Touch
// applies to resolving calls.
func (r *DBRegistry) touchForcedIn(kv db.KVTx, id string, now time.Time) (Record, error) {
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return Record{}, err
	}
	if !now.After(rec.SeenAt) {
		return rec, nil
	}
	rec.SeenAt = now
	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Forget removes a record. inUse is the caller's liveness guard: a true answer
// refuses with ErrInUse rather than deleting a record a live binding names.
func (r *DBRegistry) Forget(id string, inUse func(id string) bool) error {
	if err := r.ensureImported(); err != nil {
		return err
	}
	return r.KV.Tx(func(tx db.KVTx) error { return r.forgetIn(tx, id, inUse) })
}

func (r *DBRegistry) forgetIn(kv db.KVTx, id string, inUse func(id string) bool) error {
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return err
	}
	if inUse != nil && inUse(rec.ID) {
		return fmt.Errorf("planner %s (%s) is named by a binding that is not done: %w", rec.Name, rec.ID, ErrInUse)
	}
	if err := kv.KVDelete(registryKey(rec.ID)); err != nil {
		return fmt.Errorf("planner: remove %s: %w", rec.ID, err)
	}
	return nil
}

// writeIn puts one record's whole document in its row: exactly the JSON the
// file held, marshalled without an indent.
func (r *DBRegistry) writeIn(kv db.KVTx, rec Record) error {
	// A record written by a newer relevo is read-only for this binary: its
	// rewrite would erase every field this relevo does not know (#372). The
	// check comes first, so a refusal writes nothing at all.
	if rec.Format > PlannerFormat {
		return &store.ErrNewerFormat{Kind: "planner record", Name: rec.Name, Have: rec.Format, Know: PlannerFormat}
	}
	rec.Format = storedFormat(PlannerFormat)

	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("planner: marshal %s: %w", rec.ID, err)
	}
	if err := kv.KVPut(registryKey(rec.ID), raw); err != nil {
		return fmt.Errorf("planner: write %s: %w", rec.ID, err)
	}
	return nil
}
