# Version skew in shared state (#372)

Status: approved design, 2026-09-23. Implementation: `docs/plans/2026-09-23-state-skew-r1.md` (format guard) and `-r2.md` (lenient readers, DB).

## 1. System overview

With #371, the daemon moves onto a new binary within seconds. Other processes don't:
- `relay mcp` stays on its version for a planner session's whole life;
- `relay ui` runs until it is closed;
- a rollback runs an *older* binary over state a newer one wrote.

So two relay versions share `~/.local/state/relay` routinely. Today that silently loses or breaks data:
- every save of `bind.json` or a planner record is a full rewrite from the Go struct, so an older binary **erases fields it doesn't know**;
- an unknown binding state is reconciled as live;
- one unknown ledger entry fails the whole ledger, and `Gates` then reads it as empty, **turning every rate-limit gate off**;
- `log.jsonl`'s `confirmIndex` rewrite drops unknown keys on every line;
- `policy.json` rejects any key a newer relay documented;
- `candidates.json` rejects the whole file over one unknown harness;
- DB migrations race, and nothing stops an older binary writing into a newer schema.

The design has three principles:

1. **Records whose shape evolves carry a format, and a binary never overwrites a newer format.** It may read one, for display. That covers bindings and planner records.
2. **Append-style files preserve what they don't understand:** the ledger and `log.jsonl`.
3. **Config readers warn on the unknown instead of failing**, and keep validating what they do know.

Out of scope: `ui.json` (per-viewer prefs, loss is harmless), `release-check.json` and `daemon.json` (single-writer caches) and `availability.json`/`latency.json` (entries without a kind switch). `relay serve`'s own root already runs a single server process.

## 2. Components and files

```
R1 format guard
  internal/jsonshape/shape.go (+ test)      NEW  Keys(t reflect.Type) []string -- recursive JSON key paths from struct tags
  internal/store/format.go (+ test)         NEW  BindingFormat, ErrNewerFormat, KnownState
  internal/store/store.go                   save: refuse a newer format; stamp this binary's format (absent == 1)
  internal/store/types.go                   Binding.Format int `json:"format,omitempty"`
  internal/store/testdata/binding-shape.golden   NEW
  internal/planner/format.go (+ test), registry.go, planner.go   PlannerFormat, same guard on write; testdata/record-shape.golden
  internal/relay/daemon.go                  tickOne: skip a newer-format binding (Warn once per name); no Reconcile, no Save
  internal/relay/reconcile.go               an unknown State is left alone (Warn once)
R2 lenient readers, DB
  internal/ledger/ledger.go (+ test)        unknown kind/source entries preserved (Other), ignored by readers, written back verbatim
  internal/store/log.go (+ test)            confirmIndex patches raw lines, so unknown keys survive
  internal/policy/policy.go (+ test)        unknown keys -> warnings (jsonshape), not an error
  internal/candidate/candidate.go (+ test)  an unknown harness or role skips that candidate with a warning
  internal/doctor (+ cmd/relay wiring)      a "config" row lists the warnings
  internal/db/migrate.go, db.go (+ test)    BEGIN IMMEDIATE + re-check per migration; Newer() when schema > embedded
  cmd/relay/main.go                         daemon: open the DB after --check and after the lock; skip ingest on Newer();
                                            relay available: read the serve pointer from the serve root (bug from #371's daemon.json)
```

## 3. Data structures

- **`store.BindingFormat int`**, a const, starting at **1**, which is today's shape.
  - On disk, `format` is **absent for format 1**. Format 1 is written as 0 and read as 1, so every file saved today stays byte-identical (existing tests pin that).
  - From format 2 on, the number is written.
- **`store.ErrNewerFormat`**, a struct error: `{Kind string; Name string; Have int; Know int}`. `Kind` is `binding` or `planner record`. The text:

  `<kind> "<name>" was written by a newer relay (format <Have>; this relay knows <Know>): upgrade relay; a planner session reconnects relay mcp with /mcp`.

  `errors.Is` matches a sentinel `ErrNewerFormatSentinel`, or the type has an `Is` method. Keep it simple.
- **`planner.PlannerFormat`** and the planner record's `Format` field follow the same rules.
- **`jsonshape.Keys`** returns sorted, unique paths, for example: `builder.stream_session_id`, `consults[].endpoint.pid`, `edges[].then.prompt`. The rules:
  - `json:"-"` is skipped;
  - the tag name is used, or the Go field name when there is no tag;
  - embedded structs are flattened as encoding/json does;
  - it recurses through pointers, slices, arrays and structs;
  - a map is `name{}` and recurses into the element type;
  - `time.Time` and any type implementing `json.Marshaler` is a leaf.
- **The golden files:** the first line is `format <N>`, then one key path per line.
- **`ledger.Ledger`** gains `Other []json.RawMessage`: entries with an unknown kind or source, preserved verbatim.
- **`policy.Warnings []string`** and **`candidate.Warnings []string`**, returned alongside the loaded value.

## 4. Contracts

### 4.1 Format guard (R1)

- **`save(b)`:**
  - if `b.Format > BindingFormat` → `ErrNewerFormat`, and nothing is written;
  - otherwise set `b.Format = storedFormat(BindingFormat)`, where `storedFormat(1) == 0` and `storedFormat(n) == n`, and write.
  - `load` leaves `Format` as read. An absent field reads as 0 and means format 1.

  Every Save path goes through `save`, including `Tx.Save`, `Store.Save` and new bindings.
- **Planner registry writes** (`write` in registry.go) apply the same rule to records.
- **Daemon `tickOne`:** after `tx.Load`, if `loaded.Format > BindingFormat`, log Warn once per binding name for this process (`binding <n> is format <f>; this relay knows <k>; leaving it to a newer relay`) and return nil.
- **`Reconcile`:** if `!store.KnownState(b.State)`, log Warn once and return `b` unchanged.
- **The golden test** (`TestBindingShapeMatchesFormat`):
  - `jsonshape.Keys(reflect.TypeOf(Binding{}))` is compared with the golden keys;
  - on a mismatch it fails with: `store.Binding's JSON shape changed: bump store.BindingFormat, then run go test ./internal/store -run TestBindingShapeMatchesFormat -update`;
  - with `-update`, the test rewrites the golden only if `BindingFormat` is greater than the golden's `format` line. Otherwise it fails with: `bump store.BindingFormat first; an older relay would erase the new fields`;
  - the golden's `format` line must equal `BindingFormat` whenever the keys match.

  The planner record has the same test.

### 4.2 Ledger (R2)

- **`Load`:**
  - it decodes `entries` as `[]json.RawMessage`;
  - each entry is decoded into `Entry`. An unknown kind or source → append the raw bytes to `Other`, not an error;
  - malformed JSON is still an error.
- **`Save`** writes `entries` = the known entries (marshalled as today), followed by `Other` verbatim.
- **`Prune`** and every reader (`Gates`, `Available`, `Clear`) operate on the known entries only. `Other` is never pruned or cleared.
- The existing `TestLoadValidation` "unknown kind" case encodes the old rule, and changes to "preserved in Other".

### 4.3 `log.jsonl` (R2)

`confirmIndex` must not re-marshal whole `LogEntry` structs. For each line it rewrites, it decodes into `map[string]json.RawMessage`, sets `"confirmed": true`, and re-marshals that map. Lines it doesn't change are written byte-for-byte. Unknown keys survive, and so do `Seq` semantics (a newline count).

### 4.4 Config (R2)

- **`policy.Load`** returns `(Policy, []string, error)`, or keeps its signature with a new `LoadWithWarnings`. Pick whichever keeps callers simpler, and say which.
  - Unknown keys (every path in the file absent from `jsonshape.Keys(Policy)`; compare leaf and object keys by path) become warnings: `policy.json: unknown key "<path>" (a typo, or a key a newer relay reads)`.
  - The value validation of known keys is unchanged, and still fails.
  - `DisallowUnknownFields` is removed.
- **`candidate.Load`:** a candidate whose `harness` is unknown, or which names an unknown role, is dropped with a warning: `candidates.json: <ref>: unknown harness "<h>" (skipped)`, or the role equivalent. Every other validation still fails the load.
- **Surfacing:**
  - `relay doctor` gains a `config` row: OK when there are no warnings, Warn listing them otherwise;
  - the daemon logs each warning once per distinct text;
  - other CLI commands print nothing.

### 4.5 DB (R2)

- Each migration runs in `BEGIN IMMEDIATE`. Inside that transaction it re-reads `schema_version` for its number, skips if present, and otherwise applies the file and inserts the version.
- **`Open`** computes `max(schema_version)` after migrating. If it exceeds the highest embedded migration, the returned DB reports `Newer() == true`.
- **Daemon:** a `Newer()` DB → Warn `relay.db schema v<have> is newer than this relay (v<know>); ingest paused until relay is upgraded`, and `rt.DB` is left nil. Readers (history, stats, show) keep working.
- `relay db migrate` on a newer schema → an error saying so.
- **Daemon order:** the DB is opened **after** `--check` has returned and **after** `AcquireDaemonLock`. Today it opens before both, so the plugin's `--check` probe migrates the DB.

### 4.6 Serve pointer read (R2)

`cmd/relay/main.go:~901`, in `relay available`, reads `serve.ReadPointer(store.DefaultRoot())`. The serve daemon writes its pointer under `defaultServeRoot()`, and the client root's `daemon.json` is now `DaemonInfo` (#371), which decodes as a pointer with a live pid. Read from `defaultServeRoot()` instead.

## 5. Flow: an old `relay mcp` after an upgrade that bumped `BindingFormat` to 2

```
new CLI send -> save(format 2)
old mcp relay_send -> tx.Load (format 2) -> ... -> save -> ErrNewerFormat -> tool error:
   binding "x" was written by a newer relay (format 2; this relay knows 1): upgrade relay; ... reconnect relay mcp with /mcp
   (plus #371's upgrade notice on the same result)
old daemon (only during its <5s re-exec window) -> tickOne skips x, Warn once
```

## 6. Errors and observability

| Situation | Behaviour |
|---|---|
| `ErrNewerFormat` | Surfaces as the command's error; the daemon logs a Warn and skips. No state changes. |
| Preserved ledger entries | Invisible to readers, never lost. |
| Config warnings | The doctor `config` row; the daemon logs each once. |
| A newer DB schema | Daemon Warn; ingest paused. |

CI: every rule is a pure function or a temp-dir test. No harness, and no network.
