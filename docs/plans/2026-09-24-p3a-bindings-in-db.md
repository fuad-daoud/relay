# P3a: bind.json and log.jsonl become DB rows behind the unchanged store.Store / store.Tx API

Spec: `docs/specs/2026-09-24-db-as-record-design.md` §2 (D2-D4), §4.2. This plan **refines** the
spec in three places:
- the flock `.lock` stays as the cross-process mutex;
- the record lives in new tables (`binding_record`, `binding_event`), not the ingest-mirror
  `binding`/`event` tables;
- each store root has its own `relevo.db` until phase 5.

Round files (`NNN-*`) stay in the binding dir this round.

**Stop rule:** if a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit. The only tests you may change are those named in
§8's sanctioned list.

## 1. System overview

`internal/store` persists each binding as `<root>/<name>/bind.json` (`store.go:399-524`) and
its round log as `<root>/<name>/log.jsonl` (`log.go:296-526`). After this round, both live in
`<root>/relevo.db`:
- a Binding is one row, the full struct as JSON plus promoted columns;
- a LogEntry is one row, keyed by its 1-based Seq.

The public API does not change: every method signature in `store.go`, `log.go` and `fork.go`,
`store.New(root) *Store` (still infallible), `WithLock` and every `Tx` method. The 90 non-test
importers and roughly 200 test call sites must not need edits, apart from §8's list.

**Import rule: a file that is present is imported.** Whenever the store touches binding `name`,
or lists the root:
- a `<root>/<name>/bind.json` present is imported (upsert the record) and deleted;
- a `<root>/<name>/log.jsonl` present replaces that binding's events and is deleted.

This rule migrates existing state on first use, and keeps hand-written fixtures working.

**Unchanged:**
- `WithLock` keeps the flock (`store.go:791-817`) and the `s.mu` mutex. Each DB write inside it is
  a short autocommit statement or transaction. Callers hold the flock for up to ~90 s
  (`store.go:60-68`), and sqlite must never be held that long.
- The ingest mirror (`internal/ingest`, the `binding`/`event`/`round` tables) keeps working. The
  daemon feeds it from a new `StoreSource`, which synthesizes bind.json and log.jsonl from the
  store instead of reading files.

## 2. File structure

```
internal/db/migrations/003_binding_record.sql  NEW: binding_record, binding_event
internal/db/record.go                          NEW: DB/Tx methods for those tables (§4.1)
internal/store/db.go                           NEW: lazy per-root DB handle, import-on-presence (§4.2, §4.3)
internal/store/store.go                        save/load/list/archive/remove/MarkViewed/ViewedAt over the DB; + ListFiles (§4.4)
internal/store/log.go                          appendLog/readLog/readLogAfter/pendingForPlanner/confirmIndex over the DB
internal/store/fork.go                         ForkState writes events to the DB (files for NNN-* unchanged)
internal/ingest/source.go                      + StoreSource (§4.5)
internal/relevo/daemon.go                      ingestLiveBindings uses StoreSource (line ~339)
cmd/relevo/db.go                               backfill's live half uses StoreSource (line ~205)
internal/migrate/detect.go, migrate.go         old-root listing uses store.ListFiles (lines 82, 520)
tests                                          per §8
```

**Migration number.** P2a (running in parallel) adds `002_config.sql`. This round adds **`003`**.
If `internal/db/migrate.go` rejects or mis-orders a gap (a DB at 1 applying 003 before 002 exists),
**say so in the report and stop.** Do not renumber: the planner lands P2a first and rebases this
branch onto it.

## 3. Data structures

### 3.1 `003_binding_record.sql` (001's dialect rules; no version insert)

**`binding_record`**

| column | type | notes |
|---|---|---|
| id | TEXT PRIMARY KEY | ULID, like the other tables |
| owner | TEXT NOT NULL DEFAULT '' | `Binding.Owner` |
| name | TEXT NOT NULL | |
| state | TEXT NOT NULL | promoted from `Binding.State` on every write |
| round | INTEGER NOT NULL | promoted from `Binding.Round` |
| cwd | TEXT NOT NULL | promoted; FindByCWD/assertCWDFree may still scan in Go |
| record_json | TEXT NOT NULL | `json.Marshal(b)` of the fully stamped Binding; authoritative |
| created_at | TEXT NOT NULL | |
| updated_at | TEXT NOT NULL | |
| viewed_at | TEXT | replaces `.viewed` |
| archived_at | TEXT | set by Archive; archived rows are invisible to Load/List |

Unique index: `UNIQUE (owner, name) WHERE archived_at IS NULL`. If the dialect rules forbid
partial indexes, use `UNIQUE(owner, name, archived_at)` with archived_at = '' for live rows, and
say so.

**`binding_event`**

| column | type | notes |
|---|---|---|
| record_id | TEXT NOT NULL REFERENCES binding_record(id) ON DELETE CASCADE | |
| seq | INTEGER NOT NULL | LogEntry.Seq, 1-based |
| ts | TEXT NOT NULL | |
| round | INTEGER NOT NULL | |
| direction | TEXT NOT NULL | |
| kind | TEXT NOT NULL | |
| confirmed | INTEGER NOT NULL | |
| delivered_at | TEXT | |
| route | TEXT | |
| entry_json | TEXT NOT NULL | the entry's JSON object: every key, including unknown keys a newer relevo wrote |

Key: `PRIMARY KEY (record_id, seq)`.

### 3.2 Store internals

- `Store` gains `dbOnce sync.Once`, `dbh *db.DB` and `dbErr error`.
- `store.New` stays `func New(root string) *Store` with no I/O.
- The DB opens lazily on the first data-method call. Path helpers, `WithLock` alone,
  `DaemonRunning` and the daemon-info methods never open it. So `serve/admin.go:440`'s lock-only
  store and `ingest.go:70`'s `store.New("/")` create no DB.

## 4. Contracts

### 4.1 `internal/db/record.go`

Writes run in the existing `(*DB).Tx` (BEGIN IMMEDIATE, short). Reads use the pool.

```
type Record struct{ ID, Owner, Name, State string; Round int; CWD, JSON string; CreatedAt, UpdatedAt time.Time; ViewedAt, ArchivedAt *time.Time }
type RecordEvent struct{ Seq int; TS time.Time; Round int; Direction, Kind string; Confirmed bool; DeliveredAt *time.Time; Route string; JSON string }

(d *DB) RecordGet(owner, name string) (Record, bool, error)         // live row only
(d *DB) RecordList() ([]Record, error)                              // live rows, ordered by name
(t *Tx) RecordPut(r Record) (id string, err error)                  // upsert on (owner,name) live row; keeps id and viewed_at
(t *Tx) RecordArchive(owner, name string, at time.Time) error
(t *Tx) RecordDelete(owner, name string) error                      // cascades events
(t *Tx) RecordSetViewed(owner, name string, at time.Time) error     // no row -> no-op
(d *DB) EventsOf(recordID string, afterSeq int) ([]RecordEvent, error)   // seq ascending
(t *Tx) EventAppend(recordID string, e RecordEvent) error
(t *Tx) EventReplaceAll(recordID string, evs []RecordEvent) error
(t *Tx) EventConfirm(recordID string, seq int, at time.Time, route, newJSON string) error
(d *DB) EventMaxSeq(recordID string) (int, error)
```

Every `*DB` write wrapper opens its own Tx, following `write.go:505-571`.

### 4.2 The lazy handle (`internal/store/db.go`)

`func (s *Store) db() (*db.DB, error)`:
- once: `os.MkdirAll(s.root, bindingDirMode)`, then `db.Open(filepath.Join(s.root, "relevo.db"))`;
- a newer schema (`d.Newer()`) returns `db.ErrNewerSchema` from every data method;
- errors are cached and returned by every data method.

### 4.3 Import on presence (`internal/store/db.go`)

`func (s *Store) importPresent(name string) error`. It is called first in `load`, `save`, `appendLog`,
`readLog`, `readLogAfter`, `pendingForPlanner`, `confirmIndex`, `archive`, `remove`, and `ForkState`
(for src). It always runs under the flock, because every one of those callers already holds it.

```
bp := Dir(name)/bind.json; lp := Dir(name)/log.jsonl
if bp exists:
    b := decode (the same decode + legacy-state mapping + Format > BindingFormat check that load does
                 today, store.go:471-497); a newer format -> return ErrNewerFormat (nothing imported,
                 the file stays)
    RecordPut(record of b, record_json = the file's bytes re-marshalled compactly)
if lp exists:
    entries := decodeLog(file)                 // log.go:382-414 (assigns positions for Seq==0)
    rec := RecordGet(owner of b, or of the existing record)
    if no record exists (a log without bind.json):
        create a placeholder record ONLY IF bind.json existed this call; otherwise leave lp alone
        and return nil (a fork dir before its Save, see §4.6)
    EventReplaceAll(rec.ID, entries)           // entry_json = each line's exact bytes
remove bp and lp only after their DB writes committed
```

`func (s *Store) importAll() error` is called first in `list()`. It reads the root dir the way
`list()` does today (`store.go:499-524`: non-dirs and dot-dirs skipped) and calls `importPresent`
for each dir that holds a bind.json.

### 4.4 Store methods over the DB (signatures unchanged)

- **`save(b)`** (`store.go:399-442`): keep every check and stamp in its current order: format,
  ValidName, CWD, `assertCWDFree` (now over `list()`), CreatedAt, UpdatedAt, RoundCap, RoundTimeoutMS.
  Keep `os.MkdirAll(s.Dir(b.Name))`, because harnesses and round-file writers rely on the dir.
  Replace the file write with `RecordPut`.
- **`load(name)`**: `RecordGet(owner "", name)`. Store roots are single-owner today, so match on
  name among live rows regardless of owner. A missing row returns
  `fmt.Errorf("%s: %w", name, ErrNotFound)` exactly as today. Decode `record_json`, then apply the
  legacy state mapping and the format check.
- **`list()`**: `RecordList`, decoded in name order. Today's order is ReadDir order, which is also
  by name.
- **`archive(name)`** (`store.go:661-687`), in this order:
  1. load (ErrNotFound as today);
  2. write `Dir(name)/bind.json` (indented, as today's save) and `Dir(name)/log.jsonl` (each event's
     `entry_json`, one per line) **into the dir**;
  3. `tarGzDir` as today;
  4. `RecordArchive`;
  5. `RemoveAll(Dir)`;
  6. return the tarball path.

  Tarballs stay byte-compatible with today's, so `TarSource`, `ReadArchivedLog`, `TabEntries` and
  `db backfill` are unchanged.

  Guard: **step 2's files must not be imported by the `importPresent` that step 4's `RecordArchive`
  path could trigger.** `archive` calls `importPresent` only at its start, before writing them.
- **`remove(name)`**: `RecordDelete` (cascades), then `RemoveAll(Dir)`.
- **`MarkViewed(name, at)`**: `RecordSetViewed`.
- **`ViewedAt(name)`**: read `viewed_at`; (zero, false) when unset or there is no row. Delete the
  `.viewed` sidecar logic; `ViewedPath` stays as a helper if anything calls it.
- **`ListFiles(root string) ([]Binding, error)`** (NEW, package-level, no DB): today's file-based
  `list()` + `load()`, read-only. Used only by `internal/migrate` for pre-DB roots.

### 4.5 Log over the DB (`log.go`)

- **`appendLog(name, e)`**:
  - ValidName; TS stamp as today;
  - the record must exist, else `fmt.Errorf("append log for %q: %w", name, ErrNotFound)`. **If any
    existing caller appends before the first Save** (grep `AppendLog` in `internal/relevo`, notably
    fork and remote), **stop and report** rather than inventing a placeholder;
  - `e.Seq = EventMaxSeq+1`, overwriting any caller value exactly as today (`log.go:308-312`);
  - refuse past `maxLogEntries` exactly as `decodeLog` does today;
  - `entry_json = json.Marshal(e)`;
  - keep the `MkdirAll(Dir)` at `log.go:319`.
- **`readLog` / `readLogAfter`**: `EventsOf`, decoding each `entry_json` into LogEntry and then
  overriding `Seq`, `Confirmed`, `DeliveredAt` and `Route` from the columns. No record → `nil, nil`
  (today's absent-file result).
- **`pendingForPlanner`**: unchanged logic over `readLog`. It returns the 0-based index in that
  slice, as today.
- **`confirmIndex(name, idx, route)`**:
  - the idx'th event in seq order; out of range → today's error text (`log.go:~470`);
  - already confirmed → return nil;
  - otherwise patch `entry_json` as today's map[string]json.RawMessage patch does (keep unknown
    keys; set confirmed, delivered_at=now, route when non-empty, seq), then `EventConfirm`.

### 4.6 ForkState (`fork.go:60-141`)

Keep the order: ValidName, `throughRound >= 1`, load(src), the dst-dir-exists refusal, Mkdir dst,
copy `NNN-*` files.

Fork writes dst's log **before** dst's bind.json is saved: today it writes `dst/log.jsonl` and
never writes bind.json. So **keep writing the filtered entries to `dst/log.jsonl` as a file**,
byte-for-byte as today. The next `save` of dst (which fork's caller does,
`internal/relevo/fork.go` `writeFork`) triggers `importPresent(dst)`: bind.json is absent but a
record now exists after the RecordPut inside save.

So change `save`'s order to: stamp, MkdirAll, RecordPut, **then** `importPresent(name)` to adopt
a waiting log.jsonl. §4.3's rule then reads: "lp present and a record exists → EventReplaceAll".
Update §4.3's placeholder branch accordingly, so a log.jsonl with no record yet is left in place.

**If `writeFork` appends log entries between ForkState and its first Save, stop and report.**

### 4.7 `ingest.StoreSource` (`internal/ingest/source.go`)

`func StoreSource(st *store.Store, name string) Source`:

| method | behaviour |
|---|---|
| Name | name |
| Bind | `st.Load(name)` (ErrNotFound → `ErrSource` wrap, as dirSource does) |
| Open("bind.json") | `json.MarshalIndent` of Load |
| Open("log.jsonl") | the lines from `st.ReadLog(name)`, each `json.Marshal`ed, newline-terminated |
| Open(other) | dirSource's Open over `st.Dir(name)` |
| List | dirSource's List plus "bind.json" and "log.jsonl", sorted |
| Origin | ("live", st.Dir(name)) |

Replace `ingest.DirSource(rt.Store.Dir(b.Name))` with `ingest.StoreSource(rt.Store, b.Name)` at
`internal/relevo/daemon.go:~339` and `cmd/relevo/db.go:~205`. `DirSource` stays; its tests and
`show_test` use it.

Ingest's cursor over log.jsonl sees synthetic bytes. A confirm changes them, and today's
head-sha/whole-sha rewrite detection (`internal/ingest/cursor.go:55-100`) resets and re-reads, as
it does today after confirmIndex. **Do not change ingest otherwise.**

### 4.8 migrate

`internal/migrate/detect.go:82` and `internal/migrate/migrate.go:520` replace
`store.New(root).List()` with `store.ListFiles(root)`. These roots are pre-DB relay-era roots, and
a DB-backed List would find nothing.

## 5. Pseudocode: covered in §4 per method.

## 6. Error handling

| failure | behaviour |
|---|---|
| DB open or migrate failure | returned from the data method, wrapped `open store db %s: %w` |
| a newer schema | `db.ErrNewerSchema` from data methods; path helpers still work |
| a newer binding format in an imported bind.json | `ErrNewerFormat`; the file stays |
| a DB write that fails inside importPresent | the file is not removed; the error is returned |
| `db.ErrBusy` | returned as is. The flock serializes relevo writers, so busy means a non-store writer (the ingest tx) held the DB past 5 s. **Report any test that hits it.** |

## 7. Working efficiently

- **Read in one parallel batch:** `internal/store/{store,log,fork,format,types}.go` (the ranges
  cited), `internal/ingest/source.go:20-95`, `internal/db/{db,migrate,write,read}.go`,
  `internal/db/migrations/001_initial.sql`, `internal/relevo/daemon.go:300-350`,
  `internal/relevo/fork.go` (writeFork), `cmd/relevo/db.go:160-215`, and
  `internal/migrate/detect.go:70-90` and `migrate.go:510-530`.
- **Focused loop:**
  `go test ./internal/db/ ./internal/store/ ./internal/ingest/ ./internal/relevo/ ./internal/serve/ ./internal/migrate/ ./internal/ui/... ./internal/mcp/ ./cmd/relevo/ -count=1`.
- **Full check** once at the end: `make check` and `make e2e`.
- **CI has no harness and no network.** New tests exercise the store against `t.TempDir()` roots
  only.

## 8. Ordered steps

**Closed deletion list:**
- **D1:** the file write in `save`, and the file read in `load`/`list` (they move to `ListFiles`).
- **D2:** `log.jsonl` append/read/rewrite in `appendLog`/`readLog`/`confirmIndex`. `decodeLog`
  stays: import and `ReadArchivedLog` use it.
- **D3:** the `.viewed` sidecar.

**Sanctioned test ports.** Change the assertion and keep the behaviour. A test may be deleted
only if it pins a file-format internal listed here, and the report must name it:
- Every test in `internal/store/*_test.go`.
  - Tests of bind.json or log.jsonl **file bytes** are deleted (cite D1/D2) when their behaviour
    has no DB equivalent:
    - raw writes and reads at `store_test.go:336,648,673,686,710`;
    - `log_test.go`'s byte-preservation tests at :80,190,400,419,442,453;
    - `format_test.go:215,239,278`.
  - Tests of **behaviour** are ported (for example, "unknown keys survive a confirm" becomes an
    assertion on the decoded entry_json).
  - `legacy_state_test.go` writes a bind.json with state held/orphaned **before** first use, which
    now exercises the import. Keep it and adjust only if needed.
- `internal/relevo/daemon_test.go:759-780`: it writes a format-3 bind.json and asserts the file
  is untouched after a Tick. Port it to: the import refuses (ErrNewerFormat), and the file is still
  there, untouched.
- `internal/serve/serve_test.go:719`: it asserts `<owner>/api/bind.json` exists. Port it to assert
  the owner store's `Load("api")` succeeds.
- `internal/relevo/gc_test.go:238,258` (ReadDir of the root): port it to ignore `relevo.db*`.

**Any other failing test: stop and report**, with its name and the failure.

1. **Migration 003 + `internal/db/record.go`.** Test: put/get/list/archive (name reusable after
   archive)/delete cascade/events append/replace/confirm/after-seq.
2. **Store lazy DB + import-on-presence** (§4.2, §4.3). Test:
   - a root holding two legacy binding dirs (bind.json + log.jsonl, copied from
     `internal/ingest/testdata/`) → `List` returns both; the files are gone; `ReadLog` returns the
     same entries (Seq, Confirmed, DeliveredAt, Route, and an unknown key in entry_json);
   - a format-3 bind.json → ErrNewerFormat, and the file is kept.
3. **Store methods and log over the DB** (§4.4, §4.5, §4.6). **Mutation check:** make `confirmIndex`
   skip the `route` update, and confirm a named test fails; then revert and report it.
4. **StoreSource and its two call sites; migrate's ListFiles** (§4.7, §4.8). Test:
   `ingest.Ingest(StoreSource(...))` of a DB-backed binding fills the mirror's binding/event rows
   just as DirSource did for the same fixture.
5. **Full check:** `make check` and `make e2e`. Report:
   - the tails;
   - `git diff --stat`;
   - the mutation result;
   - each deleted test with its D-number;
   - each ported test with a one-line reason;
   - the migration-gap answer from §2.
