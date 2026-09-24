# P3d: archives are DB rows, not tarballs; tab and stats move under history; the db verb goes

Spec: `docs/specs/2026-09-24-db-as-record-design.md` D9, §6, §12. The base is main with P3c
(`round_file`, `Store.ReadFile`, lazy seal) and P4a round 1 (`unbind --done` replaced `gc`).

**Stop rule:** if a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit. Tests change only as §8 sanctions.

## 1. System overview

Today an archived binding is a tarball, `<root>/.archive/<name>-<stamp>.tar.gz`
(`store.go` `archive`, `tarGzDir`). `tab`/`stats` read the tarballs (`relevo.TabEntries`,
`tab.go:59-92`), and `relevo db backfill` ingests them.

After this round:
- **Archiving** marks the record archived (`RecordArchive`, P3a), seals **every** round's files
  into `round_file`, and removes the binding dir. Nothing is tarred.
- **Existing tarballs are imported once** (a file that is present is imported): each becomes an
  archived record with its events and round files, and the tarball is deleted.
- **tab/stats** read live **and** archived records from the store. The daemon feeds archived
  records into the history mirror once.
- **CLI:** `relevo history --tab` and `relevo history --stats` replace `tab` and `stats`, and the
  `db` verb is removed (doctor reports the DB).

## 2. File structure

```
internal/store/archive.go (+ _test)   NEW: archive over records; SealAll; ListArchived; ArchivedLog; tarball import
internal/store/store.go                archive/tarGzDir/ListArchives/ReadArchivedLog replaced (§8 D1)
internal/db/record.go                  + RecordListArchived, RecordGetByID if missing
internal/relevo/tab.go                 TabEntries over live + archived records
internal/relevo/bind.go, gc.go, text.go, cmd/relevo/main.go   ArchivedTo -> Archived (§4.3)
internal/relevo/daemon.go              one-time mirror ingest of archived records (§4.5)
internal/ingest/source.go              + ArchivedSource(st, recordID); TarSource deleted if unused (D2)
internal/serve/admin.go                AdminTabEntries over records
cmd/relevo/history.go                  --tab, --stats (§4.6)
cmd/relevo/{tab,stats,db}.go           removed (D3); removedVerbs rows
internal/doctor + cmd/relevo/doctor.go a DB row (§4.7)
```

## 3. Data structures

No migration. The existing columns are enough: `binding_record.archived_at`, `binding_event`
and `round_file`.

`type ArchivedBinding struct { Binding Binding; RecordID string; ArchivedAt time.Time }` lives in
`internal/store`.

## 4. Contracts

### 4.1 Archiving (`internal/store/archive.go`)

`func (t *Tx) SealAll(name string) (int, error)`: `SealRound` for every round present among
`Dir(name)`'s NNN files, **ignoring `Sealable`**. Only archive and delete call it, after the caller
has stopped the builder (`Unbind` and `GC` already stop it first).

`archive(name)` keeps its signature `(string, error)` and returns `""` for the path:
1. `load(name)` (ErrNotFound as today);
2. `SealAll(name)`;
3. `RecordArchive(name, now)`;
4. `RemoveAll(Dir(name))`.

Non-NNN files in the dir (`land-gate.log`) are dropped.

`func (s *Store) ListArchived() ([]ArchivedBinding, error)`: archived records, oldest
`archived_at` first. It imports the tarballs first (§4.2).

`func (s *Store) ArchivedLog(recordID string) ([]LogEntry, error)`: the events of that record,
decoded as `readLog` decodes them.

### 4.2 Tarball import (a file that is present is imported)

This runs in `ListArchived`, and once per process in the daemon (`tickOne`'s caller, before the
first tick).

For each `<root>/.archive/*.tar.gz` whose name matches today's `<name>-YYYYMMDD-HHMMSS.tar.gz`
layout:
1. Read `<name>/bind.json`, `<name>/log.jsonl` and every `<name>/NNN-*` member.
2. `RecordPut` a new record with `archived_at` = the tarball's stamp, then `RecordArchive` it
   immediately, so it never collides with a live name.
3. `EventReplaceAll` from the log, keeping raw lines as `entry_json`, as P3a's import does.
4. `RoundFilePut` each NNN member.
5. All in one db.Tx per tarball; then remove the tarball.
6. Remove `.archive/` when it is empty.

A tarball that fails to parse is left in place, with one `slog.Warn` per process. **Reuse today's
tar-reading code** (`ReadArchivedLog`, `tarGzDir`'s inverse), moved into archive.go.

### 4.3 The `ArchivedTo` fields

`relevo.GCResult.ArchivedTo` (`gc.go:26`) and `UnbindResult.ArchivedTo` (`bind.go:699`)
become `Archived bool`, set when archiving happened. The printers:
- `cmd/relevo/main.go` unbind printing (the old `archived %s -> %s` / `deleted` switch)
- `internal/relevo/text.go:144-147` `UnbindText`
- `internal/serve/admin.go:237-242`

They print `archived <name>` and `unbound <name>` / `deleted <name>` exactly as they do today,
minus the ` -> <path>` / ` to <path>` suffix.

### 4.4 TabEntries

`relevo.TabEntries(rt, cut, warn)` (`tab.go:59-92`) collects:
- for live bindings: `ReadLog`, as today;
- for archived ones: `ListArchived`, skipping records whose `ArchivedAt` is before a non-zero
  `cut`, as today's archive-stamp skip does; entries come from `ArchivedLog(recordID)` and
  `TabEntry.Binding` is the record's name.

`internal/serve/admin.go` `AdminTabEntries` gets the same change over each owner store.

### 4.5 Feeding the mirror

- `ingest.ArchivedSource(st *store.Store, recordID string) Source` is like `StoreSource`, but
  `Bind` decodes the archived record, `log.jsonl` comes from `ArchivedLog`, NNN members come from
  `round_file`, and `Origin` returns `("archive", "")`.
- Once per process, after the tarball import, the daemon ingests every archived record whose kv
  key `ingested.archive.<recordID>` is absent: `ingest.Ingest(ctx, ArchivedSource(...), rt.DB, deps)`.
  On success it puts that key.
- Errors are logged and retried next process.
- That replaces `relevo db backfill`.

### 4.6 history absorbs tab and stats

| form | behaviour |
|---|---|
| `relevo history --tab [--since D] [--by binding\|model\|provider] [--json]` | exactly `cmdTab`'s output |
| `relevo history --stats [--since D] [--json]` | exactly `cmdStats`'s output |

- `--tab` and `--stats` are mutually exclusive with each other, and with history's row and filter
  flags other than `--since`/`--json`/`--by`. Any other combination exits 2 naming the flag.
- `--by` under `--tab` accepts only tab's values.
- Move `cmdTab`/`cmdStats` bodies into helpers `history.go` calls. **The output must be
  byte-identical.**

`removedVerbs` gains:
- `"tab": "relevo history --tab"`
- `"stats": "relevo history --stats"`
- `"db": "relevo doctor (the database row)"`

### 4.7 doctor's DB row

One row, `database`: `<path> · <size> · schema v<n>`, plus the `binding_record` live/archived
counts. It is OK unless the open fails. It replaces `db path` and `db stats`. `db migrate` is
implicit, because every open migrates.

## 5. Pseudocode

In §4.1 and §4.2.

## 6. Error handling

| failure | behaviour |
|---|---|
| a bad tarball | kept, and warned about once |
| a SealAll read error during archive | archive fails and the dir stays; today a tar error did the same |
| a mirror ingest failure | logged and retried next process |

## 7. Working efficiently

- **Read in one batch:**
  - `internal/store/{store.go (archive, tarGzDir, addArchiveFile, ListArchives, ReadArchivedLog, remove), seal.go, db.go, log.go (decodeLog, readLog)}`;
  - `internal/db/record.go`;
  - `internal/relevo/{tab.go, gc.go, bind.go:690-780, text.go:130-150, daemon.go}`;
  - `internal/ingest/source.go`;
  - `internal/serve/admin.go:190-250,340-390`;
  - `cmd/relevo/{history.go, tab.go, stats.go, db.go, doctor.go, main.go (removedVerbs, unbind printing)}`.
- **Focused loop:** `go test ./internal/store/ ./internal/db/ ./internal/relevo/ ./internal/ingest/ ./internal/serve/ ./internal/doctor/ ./cmd/relevo/ ./internal/ui/... -count=1`.
- **Full check** once at the end: `make check`, then `make e2e`.
- **CI has no harness and no network.**

## 8. Ordered steps

**Closed deletion list:**
- **D1:** `tarGzDir`, `addArchiveFile`, `ListArchives`, `ReadArchivedLog`, `ArchiveDir`
  (unless a caller remains; name it), and `store.Archive`'s tarball path.
- **D2:** `ingest.TarSource`, if nothing but its tests uses it after §4.5 (with its tests).
- **D3:** `cmd/relevo/{tab,stats,db}.go` verbs (their bodies move into history helpers) and the
  `tab`/`stats`/`db` dispatch cases.
- **D4:** `GCResult.ArchivedTo` / `UnbindResult.ArchivedTo`, which become `Archived`.

**Sanctioned ports:**
- Tests that build an archive by `Archive()` and then read the tarball become assertions on
  `ListArchived`/`ArchivedLog`/`ReadFile`:
  - `internal/relevo/{show,tab,gc}_test.go`, `internal/ui/hist_test.go`;
  - `internal/ingest/{ingest,source}_test.go` `archiveFixtureAs`/`buildArchive` (these become the
    tarball-**import** tests or `ArchivedSource` tests);
  - `internal/store/archive_test.go`.
- Tests asserting `ArchivedTo`/`.tar.gz` paths → `Archived`.
- Tests invoking `tab`/`stats`/`db` → the history forms, or doctor.

**Any other failing test: stop and report.**

1. **archive.go:** SealAll, archive, ListArchived, ArchivedLog, the tarball import. Tests:
   - archive then `ListArchived` shows the binding;
   - its round files are readable via `ReadFile` on the old path;
   - the name can be bound again;
   - a tarball in `.archive/` is imported and removed, and a corrupt one is kept.

   **Mutation:** remove the tarball before the import tx commits, and confirm the failure test
   notices. Then revert.
2. **ArchivedTo → Archived.**
3. **TabEntries, AdminTabEntries, ArchivedSource, the daemon's mirror feed.** Test: `TabEntries`
   totals are the same for a binding before and after archiving it.
4. **history --tab/--stats; removal of tab/stats/db; the doctor row.** Test: the helper output is
   byte-equal to the pre-change `cmdTab`/`cmdStats` output on a fixture. Capture it before moving
   the code.
5. **Full check.** Report:
   - the tails;
   - `git diff --stat`;
   - the mutation result;
   - D-numbered deletions;
   - ported tests.

   Commit as one commit. Do not rebase.
