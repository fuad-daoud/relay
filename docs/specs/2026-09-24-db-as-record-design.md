# The database as relevo's record: no config files, fewer files, fewer commands

Date: 2026-09-24. Status: **draft for review**. Input: `2026-09-24-housekeeping-spike.md`
(the inventory, §6 decisions, §7 contabo). This spec supersedes phases 2 and 3 of
`2026-09-20-persistence-design.md`.

## 1. Purpose and end state

relevo keeps its state in about 12 mechanisms. The SQLite DB is only a mirror of them, and the
mirror is drifting. Configuration lives in hand-edited JSON files. The CLI has about 76 entry
points. This spec makes the DB the only record, moves configuration and secrets into it, and cuts
the command set to about 24 top-level verbs.

**End state on a machine.** `~/.config/relevo/` no longer exists. `$XDG_STATE_HOME/relevo/`
holds:

```
relevo.db (+ -wal, -shm)   0600, in a 0700 dir: every record, config and secret
.daemon.lock               process-lifetime flock (must not need sqlite)
spool/<name>/NNN-*         transport files of OPEN rounds only; deleted when the round seals
.worktrees/<name>/         git worktrees (local builders)
serve/repos/<owner>/*.git  server only: bare repos (git)
serve/tmp/                 server only: in-flight request bodies
```

Outside the root, these stay files by contract: the harness agent definitions relevo installs,
the systemd/launchd units, and git refs.

## 2. Decisions

| # | decision | source |
|---|---|---|
| D1 | The DB is the record. Files exist only as transport between relevo and a child process during an open round. | user |
| D2 | **The seam is the existing `store.Store`/`store.Tx` API.** Its about 20 data methods (Save, Load, List, Delete, Archive, FindByCWD, AppendLog, ReadLog, ReadLogAfter, PendingForPlanner, ConfirmIndex, ForkState, MarkViewed, ViewedAt, WithLock, daemon info) are reimplemented over sqlite. The 98 files that call them keep compiling. The path helpers keep their signatures and point into the spool. | planner |
| D3 | `bind.json` becomes one `binding` row: the full `store.Binding` as `record_json`, plus indexed columns promoted from it on every Save. The ~100 nested fields are **not** normalised. | planner |
| D4 | `log.jsonl` becomes `event` rows. `ConfirmIndex` becomes an UPDATE, which ends the append-only-cursor drift. | planner |
| D5 | Config is stored as **one JSON document per section** (`candidates`, `policy`, `roles`, `prices`, `servers`, `hooks`), parsed by the **existing** loaders from bytes. `relevo config` edits them. Hot reload keys on a DB version counter instead of file mtimes. | planner |
| D6 | Secrets live in the DB (`secret` table): client key, server TLS key and cert, typesafe key, agy creds. The DB file is 0600 in a 0700 dir. Every output redacts secrets. | user |
| D7 | Closed rounds are DB-only. When a round closes, its spool files become `artifact`/`transcript` rows in the same transaction as the close event, and are deleted after commit. The planner payload carries the report **text**, not a path. | user |
| D8 | One DB per machine. `relevo serve` uses it, and bindings carry an `owner` (`''` = local). The serve pointer file and the second root go. | planner, from spike §7 |
| D9 | Ingest, the ingest cursors, archive tarballs, `db backfill` and the `db` verb are deleted after a one-time import (§8). | planner |
| D10 | **No retention: everything is kept.** Round, event, artifact and transcript rows, including planner transcripts, are never pruned by relevo. The only deletion is an explicit `unbind --purge`. | user (Q3) |
| D11 | Commands are a clean break: an old name exits 2 and names its replacement (§6). | user |
| D12 | The legacy relay→relevo code survives one release. It is deleted in the first round after the next tag (phase L). | user |

## 3. Schema: migration `002_record.sql`

Unchanged conventions from 001: TEXT RFC3339 timestamps, TEXT ids, a Turso-safe dialect.

### 3.1 Changed tables

**binding.** New columns:

| column | type | notes |
|---|---|---|
| owner | TEXT NOT NULL DEFAULT '' | serve owner id; '' = this machine's own |
| state | TEXT NOT NULL | promoted from record_json on Save |
| round | INTEGER NOT NULL | promoted |
| record_json | TEXT NOT NULL | the full `store.Binding`, authoritative |
| viewed_at | TEXT | replaces `.viewed` |

- Uniqueness: `UNIQUE(owner, name) WHERE archived_at IS NULL`, so a name is reusable after archive.
- `ingest_source` and `archive_path` stay as nullable columns; they are not written after the import.

**event.**
- `seq` becomes the LogEntry's own 1-based Seq.
- `entry_json` is authoritative.
- `confirmed`, `delivered_at` and `route` are updated in place.
- New column `route TEXT`.

**artifact.** Kinds gain `builder_log`, `consult_log`, `review_plan`, `land_gate`. `text` holds
the full body. A body over 8 MiB is truncated from the front, with a marker line, and
`bytes`/`sha256` record the original.

**planner.** Absorbs `planners/<id>.json`. New columns: `name TEXT UNIQUE`, `sessions_json`,
`host_pid`, `host_started_at`, `cwd`, `created_at`, `seen_at`.

**transcript.** `owner_kind` gains `'consult'`. Planner transcripts are still copied (D10).

### 3.2 New tables

| table | columns | replaces |
|---|---|---|
| gate_event | id, owner, kind (`rate_limited`/`spawn_failed`/`cleared`), subject, at, until, note, source, binding | ledger.json + availability.json (a gate is active if its latest event for the subject is not `cleared` and `until` > now) |
| latency_sample | id, candidate, at, first_output_ms | latency.json |
| channel_claim | planner_id PK, pid, host_pid, host_started_at, started_at, seen_at, cwd, version | channels/*.json |
| kv | key PK, value_json, updated_at | daemon.json, release-check.json, ui.json (keys `ui`, `serve.ui`), agents-manifest.json, `import.done` |
| config_doc | name PK (`candidates`/`policy`/`roles`/`prices`/`servers`/`hooks`), body TEXT (JSON), updated_at | the ~/.config/relevo files |
| config_meta | id = 1, version INTEGER | the mtime watcher |
| secret | name PK, value BLOB, updated_at | client.key/.pub, typesafe.key, serve TLS key/cert, planners/.agy/* (names: `client.key`, `typesafe`, `serve.tls.key`, `serve.tls.cert`, `agy/<conv>`) |
| client | id PK, label UNIQUE, pubkey, enrolled_at, revoked_at | serve clients.json |

Dropped once the import is done: `ingest_cursor`.

## 4. Components and contracts

### 4.1 `internal/db` is still the only driver importer

New exported surface, all running inside a caller-supplied `*db.Tx` (`BEGIN IMMEDIATE`) or a read snapshot:

- `BindingPut(tx, owner, Binding-as-json, promoted cols)`, `BindingGet(owner, name)`,
  `BindingList(owner, includeArchived)`, `BindingArchive(tx, owner, name, at)`,
  `BindingDelete(tx, owner, name)`
- `EventAppend(tx, bindingID, entry) (seq)`, `EventsAfter(bindingID, after)`,
  `EventUpdate(tx, bindingID, seq, confirmed, deliveredAt, route)`
- `ArtifactPut(tx, roundID, kind, consultID, body)`, `ArtifactGet(roundID, kind, consultID)`
- `TranscriptAppend(tx, ownerKind, ownerID, records)`
- `GateEventAppend`, `GatesActive(owner, now)`, `GateEventsSince(owner, t)`, `GatePrune(before)`
- `LatencyAppend`, `LatencySince`
- `PlannerPut`, `PlannerGet(idOrName)`, `PlannerList`, `PlannerDelete`
- `ClaimPut`, `ClaimGet`, `ClaimDelete`
- `KVGet(key)`, `KVPut(tx, key, json)`
- `ConfigGet(name) (body, version)`, `ConfigPut(tx, name, body)` (bumps `config_meta.version`), `ConfigVersion()`
- `SecretGet(name)`, `SecretPut(tx, name, value)`, `SecretDelete(tx, name)`
- `ClientPut`, `ClientList`, `ClientRevoke`

Errors: `db.ErrNotFound`, `db.ErrConflict` (a unique violation), `db.ErrNewerSchema` (read-only
refusal), `db.ErrBusy` (busy after retries).

### 4.2 `internal/store` keeps its API and changes backend

- `store.Open(root) (*Store, error)` opens `root/relevo.db` and creates the root 0700 and the DB
  0600. **A DB open error is fatal for every verb.** Today's silent "no DB, skip ingest" goes.
- `(*Store).WithLock(fn func(*Tx) error)` runs fn in one `BEGIN IMMEDIATE` transaction, retried
  on `ErrBusy` up to 3 times with a 5 s busy timeout. `.lock` is deleted.
- Every Tx method listed in D2 maps 1:1 onto §4.1. `Archive` returns `""` for the path it used to
  return (callers that print it change in phase 4).
- The path helpers (`PlanPath`, `ReportPath`, `DonePath`, `BuilderLogPath`, `BuilderStreamPath`,
  `GateLogPath`, `AskPath`, `FindingsPath`, `ConsultStreamPath`, `ConsultLogPath`, `DiffPath`,
  `DriftPath`, `QuestionPath`) return paths under `root/spool/<owner-prefix><name>/`.
  They are valid only while a round or consult is open.
- New: `(*Tx).SealRound(name, round) error` and `(*Tx).SealConsult(name, round, id) error` (§5.2).
- New: `(*Store).SweepSpool() error` deletes spool files of rounds that are already sealed. The
  daemon runs it at start and every tick.
- `DaemonRunning`/`AcquireDaemonLock` are unchanged (`.daemon.lock` stays a file).

### 4.3 The small stores

`internal/ledger`, `internal/history`, `internal/latency`, the `internal/planner` registry,
`relevo.FileClaims`, `internal/release` cache, `internal/ui` prefs and the `internal/harness`
manifest each **keep their exported types and functions**. They swap `Load(path)`/`Save(path)`
for DB-backed equivalents that take a `*store.Store` (or an interface exposing the §4.1 calls).
The 17 copies of temp-file-plus-rename go with them. `ledger` and `history` merge into one
package over `gate_event`, because the ledger→availability mirror (`relevo/ledger.go:58-60`)
becomes one write.

### 4.4 Config: `internal/config` (new, small)

```
type Section string   // candidates | policy | roles | prices | servers | hooks
Get(s Section) ([]byte, version int64, error)
Put(tx, s Section, body []byte) error   // validates with the section's existing parser first
Doc() (map[Section]json.RawMessage, error)
PutDoc(doc map[Section]json.RawMessage) error   // all sections, one tx, one version bump
Version() (int64, error)
```

- **Validation** reuses the existing loaders: `candidate.LoadWithWarnings`, `policy.LoadWithWarnings`,
  `roles.LoadWithWarnings` and `usage.LoadPrices` gain `…FromBytes` twins. The file-path forms are
  deleted in phase 2.
- **Hard errors** refuse the Put. **Warnings** are printed and the Put proceeds.
- **Hot reload:** `relevo.ConfigWatcher` compares `Version()` each tick instead of file stamps
  (`internal/relevo/reload.go`).
- **Hooks section:** `{"<event>": [["argv0", "arg"...], ...]}`. Scripts stay wherever the user keeps
  them, and relevo runs the argv lists. The `hooks/<event>.d/` scan goes (Q1).

### 4.5 `relevo config` (new verb)

| form | behaviour |
|---|---|
| `relevo config` | print the whole doc as JSON, secrets excluded |
| `relevo config edit` | the doc goes to `$EDITOR`. It is validated on save, and an invalid doc reopens the editor with the error on top (abort by saving the file empty). Written in one tx. |
| `relevo config get <section>[.<key>...]` | print one value |
| `relevo config set <section>.<key>... <json>` | set one value (e.g. `policy.max_switches 3`, `roles.builder.candidates '["a","b"]'`) |
| `relevo config import <file\|->` / `export` | whole doc in or out. This is for provisioning (fuad-daoud/servers seeds workers with it, #391) and backups |
| `relevo config init [--force]` | today's `init`: detect harnesses on PATH, write starter candidates/roles/policy, install agents |
| `relevo config server add <name> <url> [--fingerprint F] [--ca FILE] [--insecure]` / `server rm <name>` / `server list` | today's `client add-server/rm-server/servers`. The first `add` generates the client key if absent and prints the enrolment line |
| `relevo config secret set <name>` (value on stdin) / `secret rm <name>` / `secret list` (names only) | the only way a secret enters |

## 5. Rounds, spool and seal

### 5.1 Spool

- **Layout:** `spool/<name>/NNN-plan.md`, `NNN-report.md`, `NNN-done`, `NNN-builder.{log,jsonl}`,
  `NNN-gate.log`, `NNN-<id>-{ask,findings}.md`, `NNN-<id>-consult.{log,jsonl}`. Served bindings use
  `spool/<owner8>-<name>/`.
- **Writers** are unchanged (send, the builder, proc, gate, consult). Relevo-only artifacts no longer
  touch the spool; they go straight to `artifact` rows: diff, drift, review plan, land-gate log.

### 5.2 Seal

```
on round close (reconcile close, stop, a switch that ends the round, remote close via catchUp):
  in the same WithLock tx that appends the close event:
    for each spool file of (name, round) that exists:
      plan/report/gate/builder.log/question/ask/findings  -> ArtifactPut(kind, text)
      builder.jsonl                                       -> TranscriptAppend(round) and usage facts into the round row
      done marker                                         -> round.outcome (as ingest's outcome.go derives it today)
    promote the round's facts into its round row (the outcome/facts logic moves from internal/ingest into store)
  after commit: remove those spool files; remove spool/<name>/ when empty
  a read error on one file: record an event note "seal: <file>: <err>", still close; SweepSpool retries the copy while the file exists
consult close: SealConsult does the same for NNN-<id>-*
```

Crash safety: the files are deleted only after the commit. `SweepSpool` deletes files whose
round is already sealed, and re-seals files whose round is closed but unsealed.

### 5.3 Readers that used to read files

| reader | now |
|---|---|
| `pull`/`wait` report | `ArtifactGet(report)`, or the spool while open |
| `diff`/`show --diff`, `review`, capture's own diff | `artifact` diff |
| `log`/`status`/`tab`/`stats` | `event` + `round` rows |
| `show --log`/`--transcript` | artifact `builder_log` / `transcript` |
| drift | `artifact` drift |
| ui | the same, via `relevo.Show`/`Status` |
| planner payload (channel/opencode/agy/pull) | the report text inline, plus `relevo show --name N --round R` |

## 6. Command map (phase 4, clean break)

| today | becomes |
|---|---|
| bind, add, fork | `bind [--worktree \| --cwd DIR \| --branch B \| --server S] [--from SRC@ROUND] …` (add = `bind --worktree`, fork = `bind --from`) |
| send | send |
| wait + pull | `wait`: on exit 0/2/5 it prints the pending report and marks it delivered; `--peek` leaves it undelivered |
| status, statusline, planner list | `status [--line] [--planners]` |
| show, diff, log | `show [--round N] [--plan\|--report\|--diff [--stat\|--anchors\|--drift]\|--log [--follow --after N]\|--transcript]` |
| history, tab, stats | `history [--by binding\|model\|provider\|day\|outcome] [--stats]` |
| done | done |
| stop | stop |
| pause | removed (Q2): `done` releases the worktree and `bind --resume` restores it |
| unbind, gc | `unbind <name> [--purge]`, `unbind --done` (every DONE binding) |
| unavailable, available, serve unavailable/available/gates | `gate <token> [--for D] [--reason S]`, `gate --clear <provider\|token>`, `gate` (list); `--local-server` targets this machine's serve owner scope |
| candidates, policy, roles, roles init, init, agent print/install, client *, servers | `config …` (§4.5); `candidates --probe` becomes `doctor --probe`; agent install stays automatic (daemon), `doctor --fix` forces it |
| db path/migrate/stats/backfill | removed; `doctor` prints the DB path, size and schema |
| planner init/rename/forget/prune | `planner init` (hook), `planner rename`, `planner forget`; prune is automatic in the daemon |
| edge add/list/rm, ask, review, land, ui, doctor, daemon, mcp, migrate, help, version | unchanged |
| serve (run), init, enroll, clients, revoke, fingerprint, status, ui | `serve` (bare = run), `serve init`, `serve enroll`, `serve clients [--revoke ID]`, `serve status`, `serve ui`; fingerprint is printed by `serve init` and `serve status` |
| serve log/show/tab/unbind/gc | `show`/`history`/`unbind --owner <label>` run on the server host |

The result is 24 top-level verbs. Old names get a one-line refusal naming the new form, which
lives in one table in `cmd/relevo`.
In the same PR as the removals, these change:
- `internal/planner/handoff.md`
- the architect definitions (`scripts/agents-shipped.sh --write`)
- `internal/mcp/instructions.go`
- the MCP send result's wait command
- the plugin commands: `/relevo:diff` and `/relevo:log` become `/relevo:show`
- README
- `~/.claude/settings.json`: its `statusLine` becomes `relevo status --line` (a user step, printed by `doctor`)

## 7. Server (phase 5)

- **One DB per machine.** `relevo serve` opens the machine DB. `--state` stays only for tests and
  for a second instance on one host. `serve/daemon.json` (the pointer) goes, because an admin verb
  opens the same DB.
- **Owners:** each owner store under `serve/bindings/<owner>/` becomes `binding.owner` rows.
  Worktrees move to `.worktrees/<owner8>-<name>`. Bare repos stay at `serve/repos/<owner>/`.
- **Clients, TLS and server gates** become the `client`, `secret` and `gate_event` tables, with `owner`
  = '' for server-wide gates.
- **The ack gap is a prerequisite.** 61 of 63 DONE server bindings show an un-acked report
  (spike §7). Phase 5's first round finds out why, as a read-only investigation, before anything is
  built on the ack.
- **Cleanup after ack:** once a DONE binding's last round is acked, the daemon archives its row,
  removes its worktree and deletes `refs/relevo/<name>/*` and the branch from the bare repo.
- **contabo:** once phase L lands, the unit's `ExecStartPre` migrate lines go. Its
  `~/.config/relevo` is imported like any other.

## 8. The one-time import

This runs on the first open of a schema-2 DB when `kv['import.done']` is absent. It holds
`.daemon.lock` (the daemon runs it at start; a CLI verb finding it undone refuses with "start the
daemon, or run `relevo doctor --fix`").

1. **Config:** candidates.json, policy.json, roles.json, prices.json, servers.json → `config_doc`.
   The `hooks/<event>.d/*` files become argv entries (their absolute paths).
   aliases.json and `*.bak-*` are ignored.
2. **Secrets:** client.key, typesafe.key, serve TLS files, `planners/.agy/*`.
3. **Small state:** ledger.json and availability.json → `gate_event`; latency.json; planners/*.json;
   channels/*.json; daemon.json, release-check.json, ui.json and agents-manifest.json → `kv`;
   serve clients.json → `client`.
4. **Bindings:** every live `<name>/`. Its bind.json goes to `binding`, log.jsonl to `event` (a full
   re-read, not the cursor), and closed-round files are sealed. Files of the open round move into
   `spool/`.
5. **Archives:** every `.archive/*.tar.gz` goes through the existing ingest path, which is deleted
   after this phase.
6. **Serve:** every `serve/bindings/<owner>/` as in steps 4–5, with `owner` set.
7. Every imported source is packed into one `pre-db-<stamp>.tar.gz` in the root, and the
   originals are removed. `doctor` shows that file with its size until the user deletes it.
8. `kv['import.done'] = {stamp, counts}`.

The import is idempotent: each step upserts, and step 7 runs only after steps 1–6 committed.
Downgrading to a pre-import binary is not supported. A pre-import binary finds no bind.json
files and shows no bindings; the release notes say so.

## 9. Error handling

| failure | behaviour |
|---|---|
| DB open or migrate fails | every verb exits 1 with the path and error. The daemon exits (systemd restarts it). There is no silent degraded mode. |
| DB schema newer than the binary | reads allowed; any write fails with `db.ErrNewerSchema` naming both versions |
| busy after retries | the verb exits 1 with "relevo.db is busy (held by pid …)" when the holder is known |
| an invalid config Put | refused and nothing is written; the loader's error is shown with the section and the key |
| secret missing | an error naming `relevo config secret set <name>` |
| seal cannot read a spool file | an event note, the round still closes, SweepSpool retries |
| import step fails | the import stops, `kv['import.done']` stays unset, every verb refuses with the failing step, and a daemon restart retries |

Observability: `slog` lines `seal` and `import` with counts. `doctor` rows show the DB
path, size, schema, the import state, the spool size, and the `pre-db` backup.

## 10. Phases and rounds

Each round is a standalone plan in `docs/plans/`. Each lists its deletions as a closed list, and
ends with `make check` and `make e2e`.

| phase | rounds | depends on |
|---|---|---|
| **1: cleanup** | HK1 (in flight): dead hints, `--headless`, `gc --archive`, flag parsing, opencode path | — |
| **2: config into the DB** | 2a: migration 002 (config_doc, config_meta, secret, kv), `internal/config`, `…FromBytes` loaders, config import (§8 steps 1–2); 2b: the `relevo config` verb with `server` and `secret` subverbs, the version-based watcher; remove init, candidates, policy, roles, roles init, agent print, client *, servers; update docs | 1 |
| **3: state into the DB** | 3a: binding and event behind the Store API, WithLock as a tx; 3b: the small stores (§4.3); 3c: spool, seal, sweep, artifact readers, report text in payloads; 3d: import §8 steps 3–8, and the deletion of ingest, cursors, archives and `db` | 2 |
| **4: commands** | 4a: bind/wait/show/history/unbind/gate merges and the refusal table; 4b: docs, agents, MCP and plugin sweep | 3 |
| **5: serve** | 5a: ack-gap investigation (read-only); 5b: owner rows, one DB, clients and TLS in the DB, the pointer gone; 5c: cleanup after ack; contabo and zen redeploy | 3 (and 4 for the `--owner` forms) |
| **L: legacy** | delete migrate, the rename guard and internal/legacy, plus contabo's ExecStartPre lines | the next tag after 2 |

`fuad-daoud/servers` depends on phase 2: provisioning switches from copying candidates.json and
policy.json to `relevo config import`.

## 11. Answered questions (user, 2026-09-24)

- **Q1: hooks.** Config stores argv lists per event (§4.4), and the `hooks/<event>.d/` scan goes.
  The import turns each existing script into an argv entry.
- **Q2: pause.** Removed.
- **Q3: transcripts.** Everything is kept, with no retention (D10). Planner transcripts are still copied.
- **Q4: statusline.** Merged into `relevo status --line`. `doctor` prints the settings change.
