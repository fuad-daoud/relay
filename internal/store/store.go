package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrNotFound reports a binding name with no state on disk.
var ErrNotFound = errors.New("binding not found")

// ErrCWDTaken reports that another active binding already drives that working
// tree. Two builders in one tree is the one failure that destroys work, so it
// is refused rather than warned about.
var ErrCWDTaken = errors.New("working tree already bound")

const (
	// MaxAgentNameLen is the cap ValidName enforces. 32 is the
	// agent-name limit the pre-#303 integration imposed (#303 §2). It is
	// named for what it counts, not for that integration: no name relevo
	// already wrote becomes invalid.
	MaxAgentNameLen = 32
	bindingFileMode = 0o644
	bindingDirMode  = 0o755
	defaultRoundCap = 20
	// defaultRoundMSecs is the round budget: 24 hours. A builder working a real
	// stage of a plan runs for hours, so a short budget flags healthy work as
	// needing a human and trains the reader to ignore the one state that means
	// act. This is a runaway guard, not a progress estimate; per-binding
	// overrides come from `relevo bind --timeout`.
	defaultRoundMSecs = 86400000
	archiveDirName    = ".archive"

	// maxArchiveFileBytes bounds a single file going into an archive. Relevo's
	// own state files are small; anything past this means something has gone
	// wrong, and a runaway file should fail the archive rather than balloon it.
	maxArchiveFileBytes = 64 << 20
	lockFileName        = ".lock"
	// daemonLockFileName is held for the daemon's entire lifetime, so it is a
	// separate file from lockFileName: sharing one would mean the daemon held
	// the state lock forever and no other command could read anything.
	daemonLockFileName = ".daemon.lock"
	// daemonInfoFileName is the daemon's own record of what it runs (#371):
	// the version, the executable and its identity, written at image start
	// and removed on a clean shutdown. A missing file with the lock held
	// means "a daemon older than #371".
	daemonInfoFileName = "daemon.json"
	lockRetryDelay     = 50 * time.Millisecond

	// lockAcquireLimit must exceed the longest possible hold, or a slow external
	// call turns every other caller's wait into a failure. The longest hold is
	// Reconcile's critical section on the report-close path, which can make two
	// git calls for round diff capture (10s snapshot + 10s diff) and one
	// deliverer call (30s). 90s is that worst case plus headroom; if client
	// timeouts change, this must change with them. Ask spawns its consult
	// between two short critical sections on purpose, so its external calls do
	// not count here; keep it that way.
	lockAcquireLimit = 90 * time.Second
)

// Store is the state directory. All writes are atomic within it.
type Store struct {
	root string

	// owner is the scope every record call is filtered to (P5 §4.2): "" for
	// the local store (New), and the enrolled client id for a shared,
	// server-owned store (NewShared). A Store never writes a row outside it.
	owner string

	// shared is the machine-database handle a NewShared store borrows: the
	// caller opened it and owns its lifetime, and the store never opens or
	// closes a handle. It is nil for a New store, which opens
	// <root>/relevo.db lazily.
	shared *db.DB

	mu sync.Mutex // serialises WithLock within this process

	// The database at <root>/relevo.db holds the bindings, their logs and the
	// small records (config, the ledger/availability/latency kv rows, daemon
	// info). It opens lazily on the first data-method call: path helpers and
	// WithLock alone never open it (P3a plan §3.2, §4.2). A shared store
	// never uses this path -- it borrows shared above.
	dbOnce sync.Once
	dbh    *db.DB
	dbErr  error
}

// Tx is a locked view of the store. Every method on it assumes the state lock
// is already held, which is why the lock can never be taken twice: code inside
// WithLock reaches the store only through Tx, and Tx never re-locks.
type Tx struct {
	s *Store
}

// New returns a Store rooted at root, scoped to the local owner "".
func New(root string) *Store {
	return &Store{root: root}
}

// NewShared returns a Store over root's bindings that reads and writes through
// d, the machine database the caller opened and owns (P5 §4.2). owner scopes
// every record call, so two shared stores over one database see only their own
// owner's rows. The store never opens or closes a handle: dbForRead and
// dbForWrite return d for its whole lifetime.
func NewShared(root, owner string, d *db.DB) *Store {
	return &Store{root: root, owner: owner, shared: d}
}

// DefaultRoot resolves $XDG_STATE_HOME/relevo, falling back to ~/.local/state/relevo.
func DefaultRoot() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "relevo"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, ".local", "state", "relevo"), nil
}

// ValidName enforces relevo's binding-name rule: lowercase letter first, then
// up to 31 more of [a-z0-9_-].
func ValidName(name string) error {
	if name == "" {
		return errors.New("binding name is empty")
	}
	if len(name) > MaxAgentNameLen {
		return fmt.Errorf("binding name %q exceeds %d characters", name, MaxAgentNameLen)
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

func (s *Store) roundFile(name string, round int, suffix, ext string) string {
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.Join(s.Dir(name), fmt.Sprintf("%03d-%s%s", round, suffix, ext))
}

// PlanPath is where the planner's plan for a round is stored.
func (s *Store) PlanPath(name string, round int) string {
	return s.roundFile(name, round, "plan", ".md")
}

// ReportPath is where the builder is told to write its report for a round.
func (s *Store) ReportPath(name string, round int) string {
	return s.roundFile(name, round, "report", ".md")
}

// DonePath is the builder's completion marker for a round: an empty file it
// creates as its last action (spec 2026-09-12-completion-marker §1). relevo
// only ever stats it.
func (s *Store) DonePath(name string, round int) string {
	return s.roundFile(name, round, "done", "")
}

// BuilderLogPath is where a headless builder's stderr and the rendered stream (#168) for a round
// are appended (#99). A round file like the plan and the report, so fork
// copies it with the history and gc archives it with the directory.
// Layout: <binding dir>/NNN-builder.log
func (s *Store) BuilderLogPath(name string, round int) string {
	return s.roundFile(name, round, "builder", ".log")
}

// BuilderStreamPath is where a headless builder's raw stdout for a round --
// the harness's streamed JSON, one event per line -- and the supervisor's
// relevo-exit trailer are appended (#168). A round file like the log, so
// fork copies it and gc archives it. relevo renders it into the log for
// humans (relevo.drainStream) and never reads it for meaning.
// Layout: <binding dir>/NNN-builder.jsonl
func (s *Store) BuilderStreamPath(name string, round int) string {
	return s.roundFile(name, round, "builder", ".jsonl")
}

// GateLogPath is where the gate command's output for a round is appended
// (#132). A round file like the plan and the report, so fork copies it and
// gc archives it.
// Layout: <binding dir>/NNN-gate.log
func (s *Store) GateLogPath(name string, round int) string {
	return s.roundFile(name, round, "gate", ".log")
}

// QuestionPath is where a captured blocking dialog is stored.
func (s *Store) QuestionPath(name string, round int) string {
	return s.roundFile(name, round, "question", ".md")
}

// DiffPath is where a round's captured patch is stored.
// Layout: <binding dir>/NNN-diff.patch
func (s *Store) DiffPath(name string, round int) string {
	return s.roundFile(name, round, "diff", ".patch")
}

// DriftPath is where the patch for the window between the previous round's
// report and this round's send is stored.
func (s *Store) DriftPath(name string, round int) string {
	return s.roundFile(name, round, "drift", ".patch")
}

// ViewedPath is where the "viewed" sidecar lived before viewed_at became a
// binding_record column (#143). Nothing writes it any more; it stays as a
// helper for callers that only need the path.
// Layout: <binding dir>/.viewed
func (s *Store) ViewedPath(name string) string {
	return filepath.Join(s.Dir(name), ".viewed")
}

// MarkViewed records that a human looked at name's live diff, log or show
// output (#143) at at. It is a no-op when the binding has no record: a read
// verb must not fail because a stamp could not be written.
func (s *Store) MarkViewed(name string, at time.Time) error {
	if err := s.importPresent(name); err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	_, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return d.RecordSetViewed(s.owner, name, at)
}

// ViewedAt returns when name was last viewed and true, or the zero time and
// false when it has never been viewed or has no record.
func (s *Store) ViewedAt(name string) (time.Time, bool) {
	if err := s.importPresent(name); err != nil {
		return time.Time{}, false
	}
	d, err := s.dbForRead()
	if err != nil || d == nil {
		return time.Time{}, false
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil || !ok || rec.ViewedAt == nil {
		return time.Time{}, false
	}
	return *rec.ViewedAt, true
}

// AskPath is where a consult's question is staged.
// Layout: <binding dir>/NNN-<id>-ask.md
//
// Deliberately not NNN-question.md: that name belongs to QuestionPath, the
// blocked-dialog capture, and a consult being asked a question is a different
// event from a builder being blocked on one.
func (s *Store) AskPath(name string, round int, id string) string {
	return s.consultFile(name, round, id, "ask", ".md")
}

// FindingsPath is where a consult is told to write. Its existence is the entire
// completion gate.
// Layout: <binding dir>/NNN-<id>-findings.md
func (s *Store) FindingsPath(name string, round int, id string) string {
	return s.consultFile(name, round, id, "findings", ".md")
}

// ConsultStreamPath is where a headless consult's raw stdout -- the
// harness's streamed JSON, one event per line, and the supervisor's
// relevo-exit trailer -- is appended. A headless consult carries its own
// stream, exactly as a headless builder round does (#99, #168).
// Layout: <binding dir>/NNN-<id>-consult.jsonl
func (s *Store) ConsultStreamPath(name string, round int, id string) string {
	return s.consultFile(name, round, id, "consult", ".jsonl")
}

// consultFile is roundFile with a consult id folded in. roundFile takes no id,
// and widening it would touch five call sites that will never have one.
func (s *Store) consultFile(name string, round int, id, suffix, ext string) string {
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.Join(s.Dir(name), fmt.Sprintf("%03d-%s-%s%s", round, id, suffix, ext))
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
//
// A remote binding is never returned: its CWD is the repo the branch is cut
// from and results are fetched into, not a working tree it drives, so the
// cwd-addressed verbs (`relevo send` with no --name, `relevo status` for "this
// tree") resolve to the tree's local binding or to nothing. Remote bindings
// are always addressed by --name.
func (s *Store) FindByCWD(cwd string) (Binding, bool, error) {
	var found bool
	var b Binding
	err := s.WithLock(func(tx *Tx) error {
		bindings, err := tx.List()
		if err != nil {
			return err
		}
		for _, binding := range bindings {
			if binding.Builder.Remote() {
				continue
			}
			// A done binding no longer drives its tree: resolving `relevo send`
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
	return t.remove(name)
}

// Archive archives a binding under the held lock, returning "" for the path
// it used to write a tarball at (P3d §4.1).
func (t *Tx) Archive(name string) (string, error) {
	return t.archive(name)
}

// Unexported methods implement the actual logic, assuming lock is held via Tx.

func (s *Store) save(b Binding) error {
	// A binding written by a newer relevo is read-only for this binary: its
	// rewrite would erase every field this relevo does not know (#372). The
	// check comes first, so a refusal leaves not even a directory or a temp
	// file behind.
	if b.Format > BindingFormat {
		return &ErrNewerFormat{Kind: "binding", Name: b.Name, Have: b.Format, Know: BindingFormat}
	}
	b.Format = storedFormat(recordFormat(b))

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

	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal binding %q: %w", b.Name, err)
	}
	if _, err := d.RecordPut(db.Record{
		Owner:     s.owner,
		Name:      b.Name,
		State:     string(b.State),
		Round:     b.Round,
		CWD:       b.CWD,
		JSON:      string(raw),
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
	}); err != nil {
		return fmt.Errorf("save binding %q: %w", b.Name, err)
	}

	// A dst/log.jsonl ForkState left waiting is adopted now that the record
	// exists (§4.3, §4.6).
	return s.importPresent(b.Name)
}

// assertCWDFree refuses a second active binding on the same working tree.
//
// A remote binding is exempt on both sides: its CWD is only the repo its
// branch is cut from and results are fetched into, never a working tree a
// builder writes in, so it may share a CWD with any number of remote
// bindings and with one local binding, and a local binding is never refused
// because a remote one names its repo.
func (s *Store) assertCWDFree(b Binding) error {
	if b.Builder.Remote() {
		return nil
	}
	bindings, err := s.list()
	if err != nil {
		return err
	}
	for _, other := range bindings {
		if other.Builder.Remote() {
			continue
		}
		if other.CWD == b.CWD && other.Name != b.Name && other.State != StateDone {
			return fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
				b.CWD, other.Name, other.BuilderCandidate, other.Round, ErrCWDTaken)
		}
	}
	return nil
}

func (s *Store) load(name string) (Binding, error) {
	if err := s.importPresent(name); err != nil {
		return Binding{}, err
	}
	d, err := s.dbForRead()
	if err != nil {
		return Binding{}, err
	}
	if d == nil {
		return Binding{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return Binding{}, fmt.Errorf("read binding %q: %w", name, err)
	}
	if !ok {
		return Binding{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return decodeBinding([]byte(rec.JSON), name)
}

// decodeBinding turns a binding's JSON into a Binding, mapping the legacy
// states this version no longer has and refusing one written by a newer
// relevo. It is shared by load (a record_json) and ListFiles (a bind.json).
func decodeBinding(raw []byte, name string) (Binding, error) {
	var b Binding
	if err := json.Unmarshal(raw, &b); err != nil {
		return Binding{}, fmt.Errorf("decode binding %q: %w", name, err)
	}

	// A binding written by a newer relevo is read-only for this binary: its
	// rewrite would erase every field this relevo does not know (#372).
	if b.Format > BindingFormat {
		return Binding{}, &ErrNewerFormat{Kind: "binding", Name: name, Have: b.Format, Know: BindingFormat}
	}

	// States this version no longer has still load (#303 §1): a held payload
	// was a pane delivery in flight, and orphaned meant the planner's session
	// had gone. Both are simply active again -- the entry is pending and a
	// route will take it -- so an old bind.json keeps working instead of
	// being refused.
	switch b.State {
	case "held", "orphaned":
		slog.Debug("legacy binding state mapped to active", "binding", name, "state", string(b.State))
		b.State = StateActive
	}

	return b, nil
}

func (s *Store) list() ([]Binding, error) {
	if err := s.importAll(); err != nil {
		return nil, err
	}
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	recs, err := d.RecordList(s.owner)
	if err != nil {
		return nil, fmt.Errorf("read state root: %w", err)
	}

	bindings := make([]Binding, 0, len(recs))
	for _, rec := range recs {
		b, err := decodeBinding([]byte(rec.JSON), rec.Name)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}

	return bindings, nil
}

// ListFiles reads the bindings under root the way this store read them before
// the database: one <name>/bind.json per directory. It is read-only, opens no
// database and writes nothing, so internal/migrate can list a pre-DB root
// (P3a plan §4.4, §4.8).
func ListFiles(root string) ([]Binding, error) {
	entries, err := os.ReadDir(root)
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
		b, err := loadBindingFile(root, e.Name())
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

// loadBindingFile reads one binding's bind.json, the way load did before the
// database.
func loadBindingFile(root, name string) (Binding, error) {
	raw, err := os.ReadFile(filepath.Join(root, name, "bind.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Binding{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Binding{}, fmt.Errorf("read binding %q: %w", name, err)
	}
	return decodeBinding(raw, name)
}

// ArchiveDir is where a pre-P3d relevo kept archived bindings as
// <name>-<stamp>.tar.gz. Nothing writes it any more: it is the one-time
// import's input, and its only caller is importTarballs (P3d §4.2).
func (s *Store) ArchiveDir() string { return filepath.Join(s.root, archiveDirName) }

// archiveStampLayout is the stamp a tarball's file name carries, and the
// archived_at the import gives the record it makes from it (P3d §4.2).
const archiveStampLayout = "20060102-150405"

// DBPath is relevo's sqlite database file (docs/specs/2026-09-20-persistence-design.md
// §4), the one record for bindings, config and the small stores (P3b plan D2:
// the ledger, availability and latency path helpers are gone -- each record is
// a kv row now).
func (s *Store) DBPath() string { return filepath.Join(s.root, "relevo.db") }

// ChannelsDir is where relevo mcp's claim files used to live, one per planner
// (docs/specs/2026-09-21-planner-channel-design.md §3.2). The claims are
// `claim/<planner-id>` kv rows now (P3b round 2 §4.2), and this is the
// directory the claim import reads them from and then removes.
func (s *Store) ChannelsDir() string { return filepath.Join(s.root, "channels") }

// PlannersDir is where relevo's planner records used to live: one JSON file
// per record, named <id>.json (#303 §3.1), beside the registry's .lock. The
// records are `planner/<id>` kv rows now (P3b round 2 §4.1), and this is the
// directory the registry's import reads them from and then removes.
func (s *Store) PlannersDir() string { return filepath.Join(s.root, "planners") }

// AgyCredsDir is where the captured agy agentapi credentials used to live: one
// 0600 JSON file per conversation id (#349), under planners/.agy. The
// credentials are `agy/<conversation>` secrets now (P3b round 2 §4.3), and
// this is the directory the credential import reads them from and then
// removes. It still sits inside planners/ because the credentials belong to a
// planner session, and it is dot-prefixed so a directory scan never reads it
// as a record.
func (s *Store) AgyCredsDir() string { return filepath.Join(s.PlannersDir(), ".agy") }

// WorktreeDir is where relevo keeps the worktrees it creates. Like ArchiveDir it
// is dot-prefixed, which is exactly what keeps list() from walking into it and
// trying to read a working tree as a binding.
func (s *Store) WorktreeDir() string {
	return filepath.Join(s.root, ".worktrees")
}

// WorktreePath is the worktree directory for one binding name.
func (s *Store) WorktreePath(name string) string {
	return filepath.Join(s.WorktreeDir(), name)
}

// VerifyWorktreePath is the throwaway worktree a verify consult runs in for
// one closed round (#144): <WorktreeDir()>/.verify/<name>-<NNN>.
//
// It lives under its own dot-prefixed subdirectory so a human can see at a
// glance which trees are leftovers from a crashed verify -- `git worktree
// remove` takes them away -- and so nothing mistakes one for a binding's
// worktree (Store.WorktreePath is the only path relevo ever removes).
func (s *Store) VerifyWorktreePath(name string, round int) string {
	return filepath.Join(s.WorktreeDir(), ".verify", fmt.Sprintf("%s-%03d", name, round))
}

// PruneWorktreeDirs removes the parents of relevo's worktrees once they are
// empty: .worktrees/.verify first, then .worktrees. os.Remove never removes a
// non-empty directory, so a sibling worktree keeps its parent; every error --
// not-exist, not-empty -- is ignored. It never logs: the parents of relevo's
// worktrees go once they are empty; a racing `git worktree add` recreates its
// parent itself.
func (s *Store) PruneWorktreeDirs() {
	_ = os.Remove(filepath.Join(s.WorktreeDir(), ".verify"))
	_ = os.Remove(s.WorktreeDir())
}

// archive marks a binding's record archived and removes its directory, so the
// name frees for a fresh bind while its log and every round file survive as
// rows. Every round's NNN-* files are sealed into round_file first, ignoring
// Sealable: the caller has already stopped the builder, and nothing that still
// reads an open round is in flight.
//
// It returns "" for the path: nothing is tarred any more (P3d §4.1).
func (t *Tx) archive(name string) (string, error) {
	s := t.s
	if err := ValidName(name); err != nil {
		return "", err
	}
	// load adopts a present bind.json/log.jsonl first and is the ErrNotFound
	// check, exactly as it was before the tarball went (§4.1 step 1).
	if _, err := s.load(name); err != nil {
		return "", err
	}

	// A SealAll read error fails the archive and leaves the directory alone;
	// today a tar error did the same (§6).
	if _, err := t.SealAll(name); err != nil {
		return "", err
	}

	d, err := s.dbForWrite()
	if err != nil {
		return "", err
	}
	if err := d.RecordArchive(s.owner, name, time.Now().UTC()); err != nil {
		return "", err
	}

	if err := os.RemoveAll(s.Dir(name)); err != nil {
		return "", fmt.Errorf("remove archived binding %q: %w", name, err)
	}

	return "", nil
}

// remove deletes a binding under the held lock: its record, its directory and
// -- through the record's own cascade -- its events and sealed round files.
//
// Like archive it seals every round's NNN-* files first (§4.1), so the same
// rule holds on both paths: nothing leaves a binding unsealed.
func (t *Tx) remove(name string) error {
	s := t.s
	if err := ValidName(name); err != nil {
		return err
	}
	if err := s.importPresent(name); err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	_, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return err
	}
	if ok {
		if _, err := t.SealAll(name); err != nil {
			return err
		}
		// The events and sealed files go with the row, through ON DELETE
		// CASCADE (§4.4).
		if err := d.RecordDelete(s.owner, name); err != nil {
			return err
		}
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
