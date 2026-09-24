# Cockpit A3a: every config write is a revision; `config log` and `config rollback`

Spec: `docs/specs/2026-09-24-cockpit-design.md` §6.4 (revisions, rollback, CLI) and §7.
This is the first half of A3. It covers revisions at the whole-document level with a
generic path diff. The typed draft engine and the TUI come later (A3b, C1). Line
numbers are from `ff04f5d`.

**One round. If a step is impossible as written or contradicts what you find, stop and
report. Do not improvise.**

CI has no harness and no network. Everything here is DB and pure code; `cmd/relevo`
tests may run `config` subcommands in-process (they spawn nothing), using the existing
`initRoot(t)` isolation (`cmd/relevo/init_test.go:23-31`).

Do not touch `internal/ui/**`, `internal/candidate/**`, `internal/roles/**` or
`internal/policy/**`: parallel rounds own them.

## 1. System overview

Config lives in `config_doc` rows, one JSON body per section. `config_meta.version`
goes up on every `ConfigPut` or `ConfigDelete`. Nothing records who changed what, and
nothing can undo a change.

After this round:

- Every write through `config.Store` (`Put`, `Delete`, `PutDoc`, `PutSecret`,
  `SecretDelete`, `ImportFiles`, and the new `Rollback`) also inserts one
  `config_revision` row **in the same transaction**. The row records when, the source,
  a message, the list of changes (a generic JSON-path diff), and a snapshot of the
  whole config document after the write.
- A write that changes nothing records no revision. The version still goes up, as
  today.
- The first revision ever written on a machine that already has config is preceded by
  a `baseline` revision, so the pre-revision state can be rolled back to.
- `relevo config log` lists revisions and `relevo config log --rev N` shows one.
- `relevo config rollback N` makes the config equal to revision N's snapshot, as a new
  revision with source `rollback`.

## 2. File structure

```
internal/db/migrations/006_config_revision.sql   NEW  the table
internal/db/revision.go          NEW  RevisionRow; Tx.RevisionInsert, Tx.RevisionsExist; DB.Revisions, DB.Revision
internal/db/revision_test.go     NEW
internal/config/diff.go          NEW  Change, DiffDocs, Describe (pure)
internal/config/diff_test.go     NEW
internal/config/revision.go      NEW  Store labelling (As), the in-Tx recorder, Rollback, Log, Revision
internal/config/revision_test.go NEW
internal/config/config.go        Store gains source/message/now; Put, Delete, PutDoc, PutSecret, SecretDelete record
internal/config/import.go        ImportFiles records (source "import")
cmd/relevo/config.go             `log`, `rollback` subcommands; labels on set/unset/edit/import/secret; usage text
cmd/relevo/init.go, roles.go, client.go   labels on their writes
cmd/relevo/main.go               labels on the ImportFiles call in newRuntime
cmd/relevo/serve.go              label on the ImportFiles call in loadConfig
internal/relevo/reload.go        label on the watcher's ImportFiles (via the ConfigSource it is given)
cmd/relevo/config_test.go        new CLI tests
```

## 3. Data structures

### 3.1 `006_config_revision.sql`

Follow the Turso-safe rules in the `002_config.sql` header (lines 1-7):

```
CREATE TABLE IF NOT EXISTS config_revision (
  rev      INTEGER PRIMARY KEY,   -- rowid alias: 1, 2, 3 ... (never reused; no AUTOINCREMENT)
  at       TEXT NOT NULL,         -- RFC3339 UTC milli (db.formatTime)
  source   TEXT NOT NULL,         -- see §3.3
  message  TEXT NOT NULL,         -- "" allowed
  version  INTEGER NOT NULL,      -- config_meta.version after this write
  changes  TEXT NOT NULL,         -- JSON array of config.Change; "[]" only for a baseline
  snapshot TEXT NOT NULL          -- JSON object: the whole config document after the write (§3.2)
);
```

### 3.2 The snapshot document

Its shape is the one `config export` prints: a JSON object keyed by section name, one
key per section present, and each value the stored body. It is built inside the Tx by
reading every section in `config.Sections` order with `t.ConfigGet`. Secrets are
**never** part of it.

### 3.3 Sources

| source | written by |
|---|---|
| `cli` | `config set`, `unset`, `edit`, `import <file>`, `server add` / `rm`, `server key`, `secret set` / `rm` |
| `init` | `config init`, `config roles-init` |
| `import` | `ImportFiles` (the one-time `~/.config/relevo` import, from `newRuntime`, `loadConfig` and the daemon watcher) |
| `rollback` | `Rollback` |
| `baseline` | the automatic first row (§4.2) |
| `unknown` | a `Store` write with no label; nothing in this round should produce it |

Later plans add `tui` and `migration`.

### 3.4 `internal/db/revision.go`

```
type RevisionRow struct {
    Rev      int64
    At       time.Time
    Source   string
    Message  string
    Version  int64
    Changes  []byte   // raw JSON array
    Snapshot []byte   // raw JSON object
}
func (t *Tx) RevisionsExist() (bool, error)            // SELECT EXISTS(SELECT 1 FROM config_revision)
func (t *Tx) RevisionInsert(r RevisionRow) (int64, error) // INSERT ...; returns LastInsertId (no RETURNING)
func (d *DB) Revisions(limit int) ([]RevisionRow, error) // newest first; limit <= 0 = all; Snapshot left nil
func (d *DB) Revision(rev int64) (RevisionRow, bool, error)
```

The `*DB` readers are gated like `hasConfig` (`internal/db/config.go:26`): add
`hasRevisions()` returning `d.have >= 6`. A missing table (`isMissingTable`) reads as
empty or absent.

### 3.5 `internal/config/diff.go` (pure)

```
type Change struct {
    Path   string          `json:"path"`             // "policy.max_switches", "roles.builder.candidates[0]", "candidates", "secret.typesafe"
    Op     string          `json:"op"`               // "add" | "remove" | "change" | "set" (secrets only)
    Before json.RawMessage `json:"before,omitempty"` // compact JSON; absent for add/set
    After  json.RawMessage `json:"after,omitempty"`  // compact JSON; absent for remove/set
}
type Doc = map[Section]json.RawMessage

func DiffDocs(a, b Doc) []Change
func Describe(c Change) string
```

**`DiffDocs`**
- Sections are walked in `Sections` order. A section present on one side only gives
  one change at path `<section>` (`add` or `remove`).
- Inside a section both bodies are decoded with `json.Decoder.UseNumber`, then walked
  recursively:
  - **Objects:** the union of keys, sorted. A key on one side only is `add` or
    `remove` at `<path>.<key>`.
  - **Arrays:** compared index by index to the shorter length (differences go to
    `<path>[i]`), then the extra elements are `add` or `remove` at `<path>[i]`.
  - **Scalars, or values of different kinds:** `change` when their compact encodings
    differ.
- Values are stored compact (`json.Compact`). A key that is not a valid identifier is
  quoted: `<path>["a.b"]`.
- The output is deterministic: equal inputs give an empty, non-nil slice.

**`Describe`**
- `~ <path>  <before> → <after>` for change.
- `+ <path>  <after>` for add.
- `- <path>  <before>` for remove.
- `* <path>` for set.
- Each value is cut to 60 runes with `…`.

## 4. Contracts

### 4.1 `Store` labelling (`internal/config/config.go:107-113`)

- `Store` gains unexported fields `source`, `message string` and `now func() time.Time`.
- `Open` sets `now = time.Now`.
- `func (s *Store) As(source, message string) *Store` returns a **copy** carrying the
  label. The receiver is not modified.
- `func (s *Store) WithClock(now func() time.Time) *Store` returns a copy with the
  clock. It is used by tests and by `ImportFiles`, which already takes `now`.
- `Put`, `PutDoc` and `PutSecret` use `s.now().UTC()` instead of the inline
  `time.Now().UTC()` (282, 328, 366).

### 4.2 Recording, inside every write Tx (`internal/config/revision.go`)

```
func (s *Store) record(t *db.Tx, before Doc, extra []Change) error
```

It is called as the last statement inside each write's existing `s.db.Tx` func.
`before` is the document read **at the start of the same Tx**, via a new helper
`readDoc(t)` that runs `t.ConfigGet` for each section in `Sections` order.

1. `after := readDoc(t)`.
2. `changes := append(DiffDocs(before, after), extra...)`. `extra` carries secret
   changes, which never appear in a doc.
3. If `len(changes) == 0`, return nil and record nothing.
4. If `!t.RevisionsExist()` and `len(before) > 0`, first insert a baseline row: source
   `baseline`, message `config before revisions`, changes `[]`, snapshot `before`, and
   the version read at the start of the Tx.
5. Insert the row: source is `s.source`, or `unknown` when empty; message is
   `s.message`; version is `t.ConfigVersion()`; changes is the JSON of `changes`;
   snapshot is the JSON of `after`, encoded exactly as `cmd/relevo`'s `encodeDoc` does.
   Move `encodeDoc`'s logic into `internal/config` as `EncodeDoc(Doc) ([]byte, error)`,
   and have `cmd/relevo/config.go` call it, so there is one encoder.
6. Any error aborts the whole Tx, so the config write is rolled back too.

Every write gets the same treatment:

| write | lines | `before` | `extra` |
|---|---|---|---|
| `Put` | 276-287 | read in the Tx | none |
| `Delete` | 292-296 | read in the Tx | none |
| `PutDoc` | 303-345 | read in the Tx | none |
| `PutSecret` | 359-368 | read in the Tx | `{Path: "secret."+name, Op: "set"}` |
| `SecretDelete` | 371-375 | read in the Tx | `{Path: "secret."+name, Op: "remove"}`, only when the secret existed (check with `t.SecretGet` first) |
| `ImportFiles` | `import.go`, the Tx at 94-123 | read at the start of that Tx | `set` changes for each imported secret; source forced to `import` unless the caller labelled it |

### 4.3 `Rollback`, `Log` and `Revision`

```
var ErrNoRevision = errors.New("no such revision")
var ErrNoChange  = errors.New("config already equals that revision")

// Plan returns the changes rolling back to rev would make (current -> rev's snapshot).
func (s *Store) RollbackPlan(rev int64) ([]Change, error)
// Rollback makes the stored config equal to rev's snapshot, as one new revision:
// source "rollback", message "rollback to #<rev>" plus "; "+s.message when a message was given.
func (s *Store) Rollback(rev int64) (db.RevisionRow, error)
func (s *Store) Log(limit int) ([]db.RevisionRow, error)
func (s *Store) Revision(rev int64) (db.RevisionRow, error)   // ErrNoRevision when absent
```

`Rollback` runs these steps:

1. Load the target row (`ErrNoRevision` if it is absent) and decode its snapshot into a
   `Doc`.
2. Validate every section with `Validate`. On failure, return the error and write
   nothing. A snapshot can fail validation if a later relevo tightened a rule.
3. In one Tx:
   1. Read `before`.
   2. `ConfigPut` each snapshot section whose body differs from `before`.
   3. `ConfigDelete` each section in `before` that is absent from the snapshot. This
      is the one place a section is deleted by document.
   4. `record` with source `rollback`.
   5. If nothing differed, return `ErrNoChange` and write nothing.
4. Return the new row. Secrets are not rolled back.

### 4.4 CLI (`cmd/relevo/config.go`)

The `configUsage` constant (22-38) gains two lines:

```
       relevo config log [-n N] [--rev N] [--json]
       relevo config rollback <rev> [--yes] [-m <message>]
```

`cmdConfig` (44-80) dispatches `log` and `rollback`.

**`config log`**
- Default `-n 20`. One line per revision, newest first:
  `#<rev>  <at local, 2006-01-02 15:04>  <source padded 9>  <n> change(s)  <message>`.
- With no revisions, prints `no revisions yet`.
- **`--rev N`** prints a header line
  `#N  <at>  <source>  config version <version>`, then `message: <m>` when the
  message is non-empty, then one `Describe` line per change indented by two spaces. A
  baseline prints `  (baseline: the config as it was before revisions were recorded)`.
  An unknown N exits 1 with `relevo: no such revision #N`.
- **`--json`** prints `[{rev, at, source, message, version, changes}]`. With `--rev`
  it prints one object that also carries `snapshot` (the raw object).

**`config rollback N`**
1. `RollbackPlan(N)`. `ErrNoChange` prints `config already equals revision #N` and
   exits 0.
2. Print `rollback to #N would change:`, then the `Describe` lines.
3. Without `--yes`:
   - When stdin is a terminal, ask `Roll back to #N? [y/N] ` and read one line.
     Anything but `y` or `yes` prints `nothing changed` and exits 1.
   - When stdin is not a terminal, print
     `relevo: config rollback needs a terminal to confirm; pass --yes` and exit 2.
     Detect the terminal with the pattern `internal/ui/source.go:115-118` and
     `internal/pick/pick.go:27` use: a package var `stdinStat = os.Stdin.Stat` in
     `cmd/relevo/config.go`, and "terminal" means
     `err == nil && info.Mode()&os.ModeCharDevice != 0`. **Do not add a
     dependency.**
4. Call `Rollback` with `rt.Config.As("rollback", *msg)`.
5. Print `rolled back to #N as #<new> (config version <v>)`.

**Labels at every caller** (each wraps `rt.Config.As(source, message)` right before its
write):

| caller | source | message |
|---|---|---|
| `configSet` (260, 274) | `cli` | `config set <path>` |
| `configUnset` (306, 323) | `cli` | `config unset <path>` |
| `configEdit` (403) | `cli` | `config edit` |
| `configImport` (178) | `cli` | `config import <arg>` |
| `configSecretSet` (571) | `cli` | `config secret set <name>` |
| `configSecretRm` (602) | `cli` | `config secret rm <name>` |
| `ensureClientKey` (895) | `cli` | `config server key` |
| `cmdInit` (57, 60) | `init` | `config init` |
| `cmdRolesInit` (58) | `init` | `config roles-init` |
| `cmdClientAddServer` (88) | `cli` | `config server add <name>` |
| `cmdClientRmServer` (160) | `cli` | `config server rm <name>` |
| `newRuntime` ImportFiles (main.go:562) | `import` | `imported <dir>` |
| serve `loadConfig` ImportFiles (serve.go:311) | `import` | `imported <dir>` |
| the daemon watcher (`reload.go:79`) | `import` | `imported <dir>` |

For the daemon watcher, label the `*config.Store` handed to `ConfigWatcher` at
`main.go:2574` with `.As("import", ...)`. The `ConfigSource` interface
(`reload.go:16-24`) is unchanged. `*Store` still satisfies it.

`config edit` prints `saved (config version %d, revision #%d)` (410-414). Keep the
`saved (config version` prefix, which `TestConfigEditSavesChanges` checks.

## 5. Pseudocode: `config set policy.max_switches 3`

```
rt.Config.As("cli", "config set policy.max_switches").Put(policy, body')
  Validate(policy, body')                          // refuse -> nothing written
  Tx:
    before = readDoc(t)
    t.ConfigPut(policy, body', now)                // version +1 (unchanged behaviour)
    after  = readDoc(t)
    changes = DiffDocs(before, after)              // [~ policy.max_switches 2 -> 3]
    if empty -> commit, no revision
    if no revisions yet and before non-empty -> insert baseline(before)
    insert {source cli, message ..., version, changes, snapshot after}
  commit
```

## 6. Error handling

| case | result |
|---|---|
| The revision insert fails | The whole Tx rolls back, the config write included. The error is returned to the caller unchanged. |
| An older schema (`have < 6`) read by `OpenReadOnly` | `Revisions`/`Revision` read as empty or absent, never an error. |
| A newer schema | `Tx` already refuses (`ErrNewerSchema`). Nothing changes. |
| Rollback to a revision whose snapshot no longer validates | The `Validate` error, prefixed `rollback to #N: `. Nothing written. |
| Rollback with no difference | `ErrNoChange` → `config already equals revision #N`, exit 0. |
| An unknown revision | `ErrNoRevision` → `relevo: no such revision #N`, exit 1. |
| A non-terminal rollback without `--yes` | Exit 2 with the §4.4 text. |

## 7. Tests

### `internal/config/diff_test.go`

- `TestDiffDocsSections`: a section added, removed, unchanged.
- `TestDiffDocsNested`: an object key added, removed, changed; array elements changed,
  appended, truncated; a type change (number to string); a key with a dot is quoted.
- `TestDiffDocsEqualIsEmpty`: the result is non-nil and has length 0.
- `TestDescribe`: all four ops, and truncation at 60 runes.

### `internal/db/revision_test.go`

- `TestRevisionInsertAndRead`: consecutive revs; `Revisions(0)` is newest first with
  `Snapshot` nil; `Revision(n)` carries the snapshot; `(0, false)` when absent.
- `TestRevisionsOnSchemaFive`: a DB built through migration 005 only (use the
  `fstest.MapFS` pattern from `TestOpenReadOnlySchemaOne`, `internal/db/config_test.go:146`)
  opened read-only. `Revisions` is empty with no error.

### `internal/config/revision_test.go`

Use `openStore` (`config_test.go:17-25`) and `WithClock` for fixed times.

- `TestPutRecordsRevision`: source, message, version, one change, and a snapshot equal
  to the export.
- `TestIdenticalPutRecordsNothing`: the version goes up, no revision is written.
- `TestFirstRevisionAddsBaseline`: seed config with a raw `db.Tx` `ConfigPut` (no
  `Store`), then `Put` through the Store. Expect two rows: the baseline (changes `[]`,
  snapshot = the seeded doc), then the change.
- `TestNoBaselineOnEmptyConfig`.
- `TestPutDocOneRevision`: three sections in one `PutDoc` give one revision with every
  change.
- `TestSecretRevisionHasNoValue`: after `PutSecret`, the change is `secret.typesafe`
  `set`, and the row's JSON does not contain the secret bytes (assert with
  `bytes.Contains` on changes and snapshot).
- `TestRevisionInsertFailureRollsBackWrite`: make the insert fail and assert the section
  is unchanged. For example, drop the table through a raw connection (the test is
  in-package, so `s.db` is reachable, and a db test helper can expose `ExecForTest` if
  none exists). If the only way needs a production seam, stop and report.
- `TestRollbackRestoresAndDeletes`: a sequence of Puts, including adding a section
  after revision 2. Roll back to 2: bodies equal revision 2's snapshot, the added
  section is deleted, the new row's source is `rollback` with message `rollback to #2`.
- `TestRollbackNoChange` gives `ErrNoChange`. `TestRollbackUnknown` gives
  `ErrNoRevision`.
- `TestImportFilesRecordsImport`: `seedConfigDir` then `ImportFiles` gives one
  revision, source `import`, with `secret.client.key` and `secret.typesafe` `set`
  changes.

### `cmd/relevo/config_test.go` (in-process `run`, `initRoot(t)`)

- `TestConfigLogListsRevisions`: `set` twice, then `log` shows two lines with sources
  `cli` and messages `config set …`.
- `TestConfigLogRevShowsChanges`.
- `TestConfigLogJSON`.
- `TestConfigRollbackYes`: `set` A, `set` B, `rollback 1 --yes`. The config equals A,
  and `log` shows the rollback on top.
- `TestConfigRollbackRefusesWithoutTerminal`: exit 2 with the text. Test stdin is not
  a terminal.

### Existing tests that must pass unchanged

`internal/config/config_test.go`, `internal/db/config_test.go` (including
`TestConfigVersionBumps`), `internal/db/migrate_test.go`, and every
`cmd/relevo/config_test.go` and `init_test.go` test. If one needs an edit other than
the `saved (config version` line gaining `, revision #N`, stop and report why.

### Mutation check

In `record`, skip the baseline insert. `TestFirstRevisionAddsBaseline` must fail.
Report it, then revert.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch independent reads and edits as parallel tool calls in one step.
- Read each file once at the lines named here.
- Make each file's changes in one edit call.
- Iterate on `go test ./internal/db/... ./internal/config/... ./cmd/relevo/...`.
- Run `make check` once at the end.

## 9. Ordered steps

**1. Migration and db API.**
- Deliverable: `006_config_revision.sql`, `internal/db/revision.go` (§3.4) including
  `hasRevisions`, and `revision_test.go`.
- Depends on nothing.
- Verify: `go test ./internal/db/...`. `TestMigrationsApplyInOrder` still passes
  (`embeddedVersion` adapts on its own, `db_test.go:17-24`).

**2. Diff.**
- Deliverable: `internal/config/diff.go`, `diff_test.go` (§3.5), and `EncodeDoc`
  moved from `cmd/relevo/config.go:652-685` into `internal/config`, with the cmd
  calling it.
- Depends on nothing.
- Verify: `go test ./internal/config/ -run 'Diff|Describe'`, and `go build ./...`.

**3. Recording and rollback.**
- Deliverable: the `Store` fields, `As`, `WithClock`, `readDoc`, `record`, the five
  write paths and `ImportFiles` (§4.2), `RollbackPlan`, `Rollback`, `Log`, `Revision`
  (§4.3), and `revision_test.go`.
- Depends on 1 and 2.
- Verify: `go test ./internal/config/...` and `go test ./internal/db/...`.

**4. CLI and labels.**
- Deliverable: `log` and `rollback` (§4.4), every label in the §4.4 table, the usage
  text, `config edit`'s saved line, and the cmd tests.
- Depends on 3.
- Verify: `go test ./cmd/relevo/ -run 'Config|Init|Client'`.

**5. Check and report.**
- Run the mutation check, then `make check`.
- Report:
  - each new function with its line range;
  - the exact output of `relevo config log` and `relevo config log --rev 2` from
    `TestConfigLogRevShowsChanges` (print it with `t.Log`);
  - `git diff --stat`, which must not include `internal/ui`, `internal/candidate`,
    `internal/roles` or `internal/policy`.
- Depends on 4.
