# Persistence round 1: `internal/db`, schema, migrations, `relay db`, availability rename

Spec: `docs/specs/2026-09-20-persistence-design.md` (§3 decisions 1, 2, 9;
§4 schema; §5.1 `internal/db`; §5.6 `relay db`). Issue #172.

This plan stands alone: everything you need is in this file and in the
tree. If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

## 1. System overview

relay keeps every binding's record as files under `~/.local/state/relay`
and loses sight of it once `relay gc` tars it. This round adds the database
that will hold everything relay has ever done: a pure-Go SQLite file at
`<state root>/relay.db` behind one package, `internal/db`, that is the only
place a driver is imported. Nothing writes rows yet except the tests and
`relay db migrate`; round 3 adds the ingester that fills it. This round
also renames the availability file `history.json` -> `availability.json`
(#172 q6) so the word "history" is free for binding history.

## 2. File structure

```
go.mod                                  go directive 1.22 -> 1.25; + modernc.org/sqlite
go.sum                                  updated by go mod tidy
.github/workflows/ci.yml                matrix go: ['1.22','stable'] -> ['1.25','stable']
internal/db/
  db.go                                 DB, Open, Close, Version, Tx, driver import (the ONLY one)
  migrate.go                            embedded migrations, schema_version, apply in order
  migrations/001_initial.sql            the schema in §4 of the spec
  types.go                              Repo, Planner, Binding, Round, Event, Artifact, TranscriptRecord, Cursor, Filter, RoundRow, BindingRow, Stats
  write.go                              Upsert*/Append*/SaveCursor on *Tx and *DB
  read.go                               Query, Bindings, Binding, Rounds, Artifact, Transcript, Events, Cursor, Stats
  ulid.go                               NewID() text ULID (crypto/rand, no dependency)
  errors.go                             ErrOpen, ErrBusy, ErrNotFound
  db_test.go, migrate_test.go, write_test.go, read_test.go, ulid_test.go
internal/store/store.go                 HistoryPath -> AvailabilityPath; DBPath()
internal/history/history.go             package doc + Load: read availability.json, fall back to history.json once
internal/relay/herdr.go                 Runtime.HistoryPath -> AvailabilityPath; + DB *db.DB
cmd/relay/main.go                       wire AvailabilityPath, DB; verb "db"
cmd/relay/db.go                         cmdDB: path | migrate | stats
cmd/relay/db_test.go                    pure tests of the stats formatter (no subcommand that reaches herdr)
README.md                               the `history.json` sentence (#### Availability history) names availability.json; a `relay db` paragraph
```

## 3. Data structures (`internal/db/types.go`)

Every id is a text ULID (26 chars, Crockford base32). Every time is
`time.Time` in Go and RFC3339 UTC with milliseconds (`2006-01-02T15:04:05.000Z`)
in the db. `sql.NullString`/pointers are used for nullable columns; the
Go types below use pointers for nullables.

```
Repo            { ID string; OriginURL, CommonDir *string; FirstSeen time.Time }
Planner         { ID string; HarnessKind, SessionID string; TranscriptLocator *string; FirstSeen, LastSeen time.Time }
Binding         { ID, Name string; RepoID, PlannerID, Feature, ForkedFromBindingID *string; ForkedFromRound *int;
                  CWD string; Worktree, Branch, BaseCommit, Tier, Gate *string; BuilderMode string; Server *string;
                  CreatedAt time.Time; FinalState *string; ArchivedAt *time.Time; ArchivePath *string; IngestSource string }
Round           { ID, BindingID string; Number int; StartedAt time.Time; ClosedAt *time.Time; Outcome string;
                  BuilderCandidate, BuilderHarness, BuilderProvider, BuilderModel, BuilderMode, Tier *string;
                  Commits *int; Tree *string; GateResult *string; GateExit *int; GateDurationMS *int64;
                  InTokens, CacheTokens, WriteTokens, OutTokens *int64; CostUSD *float64; CostBasis *string;
                  ReportOutcome *string; Switches int }
Event           { ID, BindingID string; RoundID *string; Seq int; TS time.Time; Kind, Direction string;
                  Note, Path *string; DeliveredAt *time.Time; Confirmed, Late bool; Flagged *int; FlaggedBy *string; EntryJSON string }
Artifact        { ID, RoundID, Kind string; ConsultID *string; Text string; Bytes int64; SHA256 string; CapturedAt time.Time }
TranscriptRecord{ ID, OwnerKind, OwnerID string; Seq int; TS *time.Time; RecordJSON, Rendered string }
Cursor          { Source string; ByteOffset int64; HeadSHA string; WholeSHA *string; UpdatedAt time.Time }
Filter          { Repo, Here, Feature, Binding, Planner, Harness, Provider, Model, Candidate,
                  Outcome, ReportOutcome, State, GateResult, CostBasis string; Round int;
                  Since, Until time.Time; Archived *bool; Limit int; Newest bool }
RoundRow        { BindingID, BindingName string; Repo, Feature *string; Number int; StartedAt time.Time; ClosedAt *time.Time;
                  Outcome string; BuilderCandidate, BuilderHarness, BuilderProvider, BuilderModel *string;
                  Commits *int; Tree, GateResult *string; CostUSD *float64; CostBasis *string; Archived bool; ArchivedAt *time.Time }
BindingRow      { Binding; RepoOrigin, RepoCommonDir *string; Rounds int; LastActivity time.Time }
Stats           { Version int; SizeBytes int64; Rows map[string]int; NewestRound *time.Time }
```

Constraints: `Outcome` is one of `reported|halted|exited|switched|done_no_report|open`
(a `const` block + `ValidOutcome(string) bool`). `Artifact.Kind` is one of
`plan|report|diff|drift|gate_log|question|answer|ask|findings`
(`ValidArtifactKind`). `TranscriptRecord.OwnerKind` is `round|planner`.
`Binding.IngestSource` is `live|archive`. Writers reject invalid enum
values with `ErrInvalid` wrapping the field name.

## 4. Interfaces (`internal/db`)

```
func Open(path string) (*DB, error)
    pre:  path's directory exists
    post: file exists, journal_mode=WAL, busy_timeout=5000, foreign_keys=ON, all migrations applied
    err:  ErrOpen wrapping the driver error
func (d *DB) Close() error
func (d *DB) Version() (int, error)                 // max(schema_version.version), 0 when empty

func (d *DB) Tx(fn func(*Tx) error) error           // BEGIN IMMEDIATE; commit on nil, rollback on error; SQLITE_BUSY -> ErrBusy

// Writers exist on *Tx; the *DB forms wrap one Tx each.
func (t *Tx) UpsertRepo(r Repo) (string, error)            // key: origin_url when set, else common_dir; returns existing id on hit
func (t *Tx) UpsertPlanner(p Planner) (string, error)      // key: (harness_kind, session_id); updates last_seen, locator when now set
func (t *Tx) UpsertBinding(b Binding) (string, error)      // key: (name, created_at); updates every mutable column
func (t *Tx) UpsertRound(r Round) (string, error)          // key: (binding_id, number); updates every column
func (t *Tx) AppendEvents(bindingID string, evs []Event) (added int, err error)   // INSERT OR IGNORE on (binding_id, seq)
func (t *Tx) UpsertArtifact(a Artifact) error              // key: (round_id, kind, consult_id) with consult_id '' for null
func (t *Tx) AppendTranscript(ownerKind, ownerID string, recs []TranscriptRecord) (added int, err error) // INSERT OR IGNORE on (owner_kind, owner_id, seq)
func (t *Tx) SaveCursor(c Cursor) error                    // INSERT OR REPLACE on source

// Readers on *DB (and *Tx).
func (d *DB) Query(f Filter) ([]RoundRow, error)           // §Query below
func (d *DB) Bindings(f Filter) ([]BindingRow, error)      // same filters that apply to binding; newest LastActivity first
func (d *DB) Binding(name string) (BindingRow, bool, error)// newest created_at for that name
func (d *DB) Rounds(bindingID string) ([]Round, error)     // by number asc
func (d *DB) Artifact(roundID, kind string) (Artifact, bool, error)      // consult_id null only
func (d *DB) Transcript(ownerKind, ownerID string, fromSeq, limit int) ([]TranscriptRecord, error)
func (d *DB) Events(bindingID string, round int) ([]Event, error)         // round 0 = all; by seq
func (d *DB) Cursor(source string) (Cursor, bool, error)
func (d *DB) Stats() (Stats, error)
```

**Query** builds one SELECT over `round JOIN binding LEFT JOIN repo` with a
WHERE clause assembled from the non-zero `Filter` fields, all
parameterised (never string-formatted values):

| field | clause |
|---|---|
| Repo | `(repo.origin_url = ? OR repo.common_dir = ?)` |
| Here | resolved by the caller into Repo before calling; `Query` treats a non-empty `Here` as an error `ErrInvalid("Here must be resolved")` |
| Feature | `binding.feature = ?` |
| Binding | `binding.name = ?` |
| Planner | `binding.planner_id IN (SELECT id FROM planner WHERE session_id = ?)` |
| Harness / Provider / Model | `round.builder_harness = ?` etc. |
| Candidate | `round.builder_candidate = ?` |
| Outcome / ReportOutcome / GateResult / CostBasis | equality on the round column |
| State | `binding.final_state = ?` |
| Round | `round.number = ?` |
| Since / Until | `round.started_at >= ?` / `round.started_at < ?` (RFC3339 text compares correctly) |
| Archived | `binding.archived_at IS NOT NULL` / `IS NULL` |
| Limit | `LIMIT ?` (0 = no limit) |
| Newest | `ORDER BY round.started_at DESC` else ASC |

**Migrations**: `//go:embed migrations/*.sql`; files sorted by name; each
applied in one transaction with `INSERT INTO schema_version`; a file whose
number is already recorded is skipped. `001_initial.sql` is the schema in
spec §4 verbatim (tables, uniques, indexes), written in the conservative
dialect: no `AUTOINCREMENT`, no `WITHOUT ROWID`, no triggers/views/FTS, no
`RETURNING`. Partial unique indexes (`WHERE origin_url IS NOT NULL`) are
used for repo's two keys.

**`relay db`** (`cmd/relay/db.go`):

```
relay db path       print rt.Store.DBPath()
relay db migrate    db.Open(path); print "relay.db at <path>: schema version N"
relay db stats      Open; Stats(); print one line per table "<table>  <rows>", then "size <human>  version N  newest round <ts|->"
```
Unknown subcommand: usage, exit 2. `relay db` with no args: usage, exit 2.

**Availability rename**:
- `store.AvailabilityPath()` returns `<root>/availability.json`; `HistoryPath()` is deleted.
- `history.Load(path)`: if `path` is missing and `<dir>/history.json` exists, read that; the next `Save` writes `availability.json`, and `Load` removes `history.json` after a successful read+save round trip (do the move in `Load`: read old, write new, remove old, then return). Package doc updated to say the file is `availability.json`.
- `Runtime.AvailabilityPath` replaces `Runtime.HistoryPath`; every reference updated (`cmd/relay/main.go:355,480`, any test).
- README: in the `#### Availability history` section, `history.json` -> `availability.json`, and add: "Older installs are migrated on first read."

## 5. Pseudocode

```
Open(path):
    dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
    sql.Open("sqlite", dsn)         -- modernc registers driver name "sqlite"
    ping; on error -> ErrOpen
    migrate(db)
    return &DB{sql}

migrate(db):
    CREATE TABLE IF NOT EXISTS schema_version(version INTEGER PRIMARY KEY, applied_at TEXT)
    applied := SELECT version FROM schema_version
    for each embedded file in name order:
        n := leading integer of the file name
        if n in applied: continue
        tx: exec file contents (split on ";\n" is NOT needed -- modernc executes multi-statement strings with Exec); INSERT version; commit

Tx(fn):
    tx := BEGIN IMMEDIATE
    err := fn(&Tx{tx}); if err: rollback, return err (map SQLITE_BUSY to ErrBusy)
    commit

UpsertRepo(r):
    if r.OriginURL != nil: row := SELECT id WHERE origin_url = ?
    else if r.CommonDir != nil: row := SELECT id WHERE common_dir = ?
    else: return ErrInvalid("repo needs origin_url or common_dir")
    if found: UPDATE the other column when it is null and r has it; return id
    id := NewID(); INSERT; return id
(the other upserts follow the same select-then-insert-or-update shape on their natural key)

NewID():
    48-bit ms timestamp + 80 bits crypto/rand, Crockford base32, 26 chars, monotonic not required
```

## 6. Error handling

- `ErrOpen`, `ErrBusy`, `ErrNotFound`, `ErrInvalid` in `errors.go`; every
  exported function wraps with `fmt.Errorf("db: ...: %w", ...)`.
- `Open` failing is non-recoverable for the caller: `relay db *` exits 1
  with the path and the message. Nothing in this round touches the daemon
  path yet, so no tick can fail because of the db.
- `Tx` maps a driver busy error (modernc: `sqlite.Error` with code 5 /
  `SQLITE_BUSY`, or an error string containing "database is locked") to
  `ErrBusy`.

## 7. Ordered implementation steps

### Task 1 -- toolchain

**Files:** `go.mod`, `.github/workflows/ci.yml`.

- `go.mod`: `go 1.22` -> `go 1.25`. Then `go get modernc.org/sqlite@latest`
  and `go mod tidy`. If `go mod tidy` reports the module needs a newer Go
  than the machine has (`go version` must be >= the module's directive),
  halt and report the two versions.
- `ci.yml` line 30: `go: ['1.22', 'stable']` -> `go: ['1.25', 'stable']`.
  Change nothing else in the workflow.

**Verify:** `go build ./...` passes; `grep -n "^go " go.mod` prints `go 1.25`.

### Task 2 -- `internal/db` core: types, ids, open, migrate

**Files:** `internal/db/{types.go,ulid.go,errors.go,db.go,migrate.go,migrations/001_initial.sql}` + tests.

Implement §3, §4 `Open/Close/Version/Tx`, migrations, `NewID`.

**Tests**
- `TestOpenCreatesAndMigrates`: `Open` in a temp dir -> file exists, `Version()==1`, `PRAGMA journal_mode` returns `wal`.
- `TestOpenIsIdempotent`: `Open` twice on the same path -> version still 1, `schema_version` has one row.
- `TestMigrationsApplyInOrder`: a test-only second migration injected through an unexported `applyMigrations(db, fs)` with two files -> both recorded, in order.
- `TestNewIDShapeAndUniqueness`: 1000 ids, all 26 chars of Crockford base32, all distinct, sorted order is time order across a 2 ms sleep.
- `TestTxRollsBackOnError`: insert a repo inside `Tx` returning an error -> no row.

**Verify:** `go test ./internal/db/`.

### Task 3 -- writers

**Files:** `internal/db/write.go`, `write_test.go`.

**Tests** (each is call-twice-count-once):
- `TestUpsertRepoByOrigin`, `TestUpsertRepoByCommonDirFallback`, `TestUpsertRepoFillsMissingKey` (origin-only row, upsert with both -> common_dir filled, same id).
- `TestUpsertPlannerUpdatesLastSeen`.
- `TestUpsertBindingNaturalKey` (same name, different created_at -> two rows).
- `TestUpsertRoundReplacesColumns` (outcome open -> reported on second call).
- `TestAppendEventsIgnoresKnownSeq` (append 3, append same 3 + 1 new -> added 1, total 4).
- `TestUpsertArtifactByKind`, `TestAppendTranscriptIgnoresKnownSeq`, `TestSaveCursorReplaces`.
- `TestWriterRejectsInvalidEnum` (`Round.Outcome = "won"` -> `ErrInvalid`).

**Verify:** `go test ./internal/db/`.

### Task 4 -- readers and Query

**Files:** `internal/db/read.go`, `read_test.go`.

Seed helper in the test: two repos, three bindings (two on repo A, one archived; one on repo B), planner rows, six rounds spanning harnesses `agy`/`opencode`, outcomes `reported`/`halted`/`open`, one gated round, two cost bases, dates one day apart.

**Tests**
- `TestQueryNoFilterReturnsAllNewestFirst` (`Newest: true`).
- One test per `Filter` field: `TestQueryByRepoOrigin`, `...ByRepoCommonDir`, `...ByFeature`, `...ByBinding`, `...ByPlanner`, `...ByHarness`, `...ByProvider`, `...ByModel`, `...ByCandidate`, `...ByOutcome`, `...ByReportOutcome`, `...ByState`, `...ByGateResult`, `...ByCostBasis`, `...ByRound`, `...Since`, `...Until`, `...ArchivedTrue`, `...ArchivedFalse`, `...Limit`. Each asserts the exact set of `(binding, number)` pairs.
- `TestQueryHereUnresolvedIsInvalid`.
- `TestBindingsNewestActivityFirst`, `TestBindingNewestByName`, `TestRoundsAscending`, `TestArtifactMissingIsFalse`, `TestTranscriptPaging` (fromSeq/limit), `TestEventsRoundZeroIsAll`, `TestStatsCounts`.

**Verify:** `go test ./internal/db/`.

### Task 5 -- availability rename

**Files:** `internal/store/store.go`, `internal/history/history.go` (+test), `internal/relay/herdr.go`, `cmd/relay/main.go`, `README.md`, every test referencing `HistoryPath`.

**Tests**
- `TestLoadMigratesLegacyHistoryFile` in `internal/history`: write `history.json` in a temp dir, `Load(<dir>/availability.json)` returns its events, `availability.json` now exists, `history.json` is gone.
- `TestLoadPrefersNewFileWhenBothExist`: both present -> new file wins, old file left alone.

**Verify:** `grep -rn "HistoryPath" --include=*.go .` prints nothing; `grep -n "history.json" README.md` prints only the "Older installs" sentence if you keep the word there, else nothing.

### Task 6 -- `relay db`, `Runtime.DB`, `Store.DBPath`

**Files:** `internal/store/store.go` (`DBPath() = <root>/relay.db`), `internal/relay/herdr.go` (`DB *db.DB` on `Runtime`, doc: nil means no database; nothing in this round reads it), `cmd/relay/main.go` (verb `db`; do NOT open the db in the shared runtime constructor this round -- only `cmdDB` opens it), `cmd/relay/db.go`, `cmd/relay/db_test.go`, `README.md`.

README: add a short `### relay db` subsection under the state-directory documentation: the path, `migrate`, `stats`, and one sentence that the database is being filled in a later change.

**Tests** (`cmd/relay/db_test.go`, pure -- CI has no herdr and this must not execute a subcommand that reaches it): `TestFormatStats` over a literal `db.Stats` -> the exact lines.

**Verify:** `relay db path` prints `~/.local/state/relay/relay.db` (expanded); `relay db migrate` prints `schema version 1`; `relay db stats` prints zero rows per table.

### Task 7 -- full check and commit

Run, in this order: `gofmt -l .` (must print nothing), `go vet ./...`,
`go test -race -count=1 ./...`, `go mod tidy && git diff --exit-code go.mod go.sum`,
then `make check`. Known: `scripts/plugin-build_test.sh` case 3 fails on a
clone without tags (#242) -- if that is the **only** failure and `git tag`
prints nothing, say so in the report and treat the check as passed.

Tests that create git repos must pass `-c commit.gpgsign=false -c tag.gpgsign=false`
(this machine signs by default).

**Commit** (one commit for the round):
`feat(db): internal/db on modernc sqlite, schema v1, relay db path|migrate|stats; history.json -> availability.json (#172)`

## Report

Per task: what was done, the test names added, the verify result. Then
the commit sha, the `make check` result (with the #242 caveat if it
applied), and `go.mod`'s final `go` directive and `modernc.org/sqlite`
version. If any step was impossible as written, say which and stop there.
