# P2a: config and secrets live in the DB; files in ~/.config/relevo are imported and removed

Spec: `docs/specs/2026-09-24-db-as-record-design.md` (§2 D5/D6, §3.2, §4.4, §8 steps 1-2).
This round moves config and secrets into the DB and keeps every existing verb working. The next
round (P2b) adds the `relevo config` verb and removes the old config verbs. **Add no new verb in
this round.**

**Stop rule:** if a step is impossible as written or contradicts the code, stop and report.
Do not improvise, and do not bend a test to fit. The only tests you may change are those named in
§8's sanctioned-ports list.

## 1. System overview

Today every verb's `newRuntime` (`cmd/relevo/main.go:541-637`) reads these files from
`~/.config/relevo/` (via `userConfigRoot()`, `main.go:405-416`):
- `candidates.json`, `policy.json`, `roles.json`, `prices.json`, `servers.json`;
- `client.key`, `client.pub`, `typesafe.key`;
- `hooks/<event>.d/*`.

`relevo serve` (`cmd/relevo/serve.go:261-409`) reads the same files, and so do the doctor and
the client verbs. After this round:

- **The DB is the only place config is read from.** That is the machine DB,
  `store.DefaultRoot()/relevo.db`, for local verbs and serve alike. Config lives in `config_doc`
  (one JSON body per section) and secrets live in `secret`.
- **Import rule: a file that is present is imported.** Every runtime construction first imports
  any config file present in `<userConfigRoot>/relevo/`:
  - validate it with the existing parser;
  - store it (the section body, plus the raw bytes in `config_import`);
  - delete the file.

  This is the one-time migration, and it stays the way to drop config in (provisioning, tests).
- **Invalid files block the import.** If any file present is invalid, nothing is imported or
  deleted, and the verb fails with that file's error, just as a bad policy.json fails today.
- **`daemon --preflight` and `daemon --check` never write:** they read with no import and no
  migration (§4.6).
- **Writers write the DB:**
  - `init` and `roles init` put sections;
  - `client init` puts a secret;
  - `client add-server` and `rm-server` put the servers section.

## 2. File structure

```
internal/db/migrations/002_config.sql   NEW: config_doc, config_meta, secret, kv, config_import
internal/db/config.go                   NEW: DB/Tx methods for those tables; OpenReadOnly
internal/db/db.go                       Open: chmod the db file 0600 after open
internal/config/config.go               NEW package: Section, Store, Loaded, Load, Validate
internal/config/import.go               NEW: ImportFiles (the present-file import), LoadFiles (peek)
internal/config/*_test.go               NEW
internal/candidate/candidate.go         + Parse(name, data); Load/LoadWithWarnings become ReadFile+Parse
internal/policy/policy.go               + Parse(name, data); same
internal/roles/file.go                  + Parse(name, data); same
internal/usage/prices.go                + ParsePrices(data); LoadPrices becomes ReadFile+ParsePrices
internal/remote/client/config.go        + ParseServers, EncodeServers; file functions deleted (§8 D1)
internal/classify/resolve.go, classify.go, keyfile.go   the key comes from the DB (§4.5)
internal/hooks/events.go, dispatcher.go, executor.go    argv lists from config, no dir scan (§4.4)
internal/relevo/reload.go               ConfigWatcher over config.Store version (§4.7)
internal/legacy/legacy.go               Unmigrated: the config clause is false when NewState exists
cmd/relevo/main.go                      newRuntime / newRuntimePeek / newRemoteClient / hooks / daemon wiring
cmd/relevo/serve.go                     config from config.Store
cmd/relevo/usage_wire.go                prices from Loaded
cmd/relevo/init.go, roles.go, client.go, doctor.go   writers/readers on config.Store
cmd/relevo/db.go                        openDB: dir 0700
internal/doctor/doctor.go               usageChecks read the prices body, not a path
internal/setup/setup.go                 Write deleted (§8 D2); Plan stays
tests                                   ported per §8's sanctioned list
```

## 3. Data structures

### 3.1 Migration `002_config.sql`

Use the 001 dialect rules: `CREATE TABLE IF NOT EXISTS`, TEXT timestamps (RFC3339Nano UTC), and
no version insert in the SQL (the migrator records it).

| table | columns |
|---|---|
| config_doc | `name TEXT PRIMARY KEY` (one of the six sections), `body TEXT NOT NULL` (JSON), `updated_at TEXT NOT NULL` |
| config_meta | `id INTEGER PRIMARY KEY CHECK (id = 1)`, `version INTEGER NOT NULL`; the migration inserts `(1, 0)` with `INSERT OR IGNORE` |
| secret | `name TEXT PRIMARY KEY`, `value BLOB NOT NULL`, `updated_at TEXT NOT NULL` |
| kv | `key TEXT PRIMARY KEY`, `value_json TEXT NOT NULL`, `updated_at TEXT NOT NULL` (unused this round; phase 3 needs it) |
| config_import | `id INTEGER PRIMARY KEY AUTOINCREMENT`, `name TEXT NOT NULL` (the file name), `source_path TEXT NOT NULL`, `body BLOB NOT NULL` (raw file bytes), `imported_at TEXT NOT NULL` |

If the Turso-safe rules in 001's header forbid `AUTOINCREMENT` or `CHECK`, use the closest
allowed form and say so in the report.

### 3.2 `internal/config`

```
type Section string
const Candidates, Policy, Roles, Prices, Servers, Hooks Section = "candidates", "policy", "roles", "prices", "servers", "hooks"
var Sections = []Section{Candidates, Policy, Roles, Prices, Servers, Hooks}
// FileName: candidates.json, policy.json, roles.json, prices.json, servers.json; Hooks has none.

const SecretClientKey = "client.key"   // PEM bytes, as remote.MarshalPrivate writes
const SecretTypesafe  = "typesafe"      // trimmed API key bytes

type Hooks map[string][][]string        // event type -> argv lists, run in order

type Loaded struct {
    Candidates *candidate.Set           // never nil; an absent section = empty set (today's missing file)
    Policy     policy.Policy            // an absent section = zero Policy (today's missing file)
    RolesFile  *roles.File              // nil when absent (the legacy derivation, as today)
    Registry   *roles.Registry          // roles.Build(RolesFile, Candidates, Policy)
    Prices     usage.Prices             // DefaultPrices() overlaid by the section, as LoadPrices does
    Servers    client.Servers           // never nil
    Hooks      Hooks                    // never nil
    ClientKey  []byte                   // nil when absent
    Typesafe   string                   // "" when absent
    Warnings   []string                 // parser warnings, in the order candidates, policy, roles
    Version    int64                    // config_meta.version at load
}

type Store struct { /* holds *db.DB */ }
```

"Absent section = missing file" is the invariant everywhere. **If a parser's missing-file result
cannot be reproduced from an absent section, stop.**

## 4. Contracts

### 4.1 `internal/db/config.go`

Reads come on both `*DB` and `*Tx` via the existing `queryer` pattern (`read.go`). Writes are `*Tx`,
with `*DB` wrappers that open their own Tx, as in `write.go:505-571`.

```
ConfigGet(name string) (body []byte, ok bool, err error)
(t *Tx) ConfigPut(name string, body []byte, now time.Time) error   // upsert + config_meta.version += 1
(t *Tx) ConfigDelete(name string) error                            // + version += 1
ConfigVersion() (int64, error)
SecretGet(name string) ([]byte, bool, error)
(t *Tx) SecretPut(name string, value []byte, now time.Time) error
(t *Tx) SecretDelete(name string) error
SecretNames() ([]string, error)
(t *Tx) ConfigImportRecord(name, sourcePath string, body []byte, now time.Time) error
func OpenReadOnly(path string) (*DB, error)
```

`OpenReadOnly`:
- uses a `mode=ro` URI and never migrates;
- returns an error wrapping `os.ErrNotExist` when the file is missing;
- when the schema has no `config_doc` table (schema < 2), the config reads return
  `ok=false` / `version 0` rather than an error.

### 4.2 `db.Open`

After a successful open, `db.Open` chmods `path` to 0600, and the `-wal`/`-shm` files too if they
exist; a chmod error is returned. `openDB` (`cmd/relevo/db.go:20-25`) creates the dir 0700
instead of 0755. Secrets now live in this file.

### 4.3 Parsers (from bytes)

| new function | notes |
|---|---|
| `candidate.Parse(name string, data []byte) (*Set, []string, error)` | |
| `policy.Parse(name string, data []byte) (Policy, []string, error)` | wraps `ErrBadPolicy` as today |
| `roles.Parse(name string, data []byte) (*File, []string, error)` | a top-level null is still an error |
| `usage.ParsePrices(data []byte) (Prices, error)` | the default overlaid by the rows |
| `client.ParseServers(data []byte) (Servers, error)` | validates every entry with `ValidateEntry` |
| `client.EncodeServers(Servers) ([]byte, error)` | indented JSON |

- `name` replaces `path`/`filepath.Base(path)` in the message texts. `Load*` pass the path, so
  every existing message is unchanged.
- The existing `Load`/`LoadWithWarnings`/`LoadPrices` become `os.ReadFile` (missing file → today's
  missing result) + Parse.
- **A test that asserts an exact message must pass unchanged. If one cannot, stop.**

### 4.4 Hooks

- `hooks.Config` loses `HooksDir` and gains `Hooks map[string][][]string`; `LogPath` stays.
- `LocalDispatcher.Dispatch` (`internal/hooks/dispatcher.go:29-56`) iterates
  `config.Hooks[string(event.Type)]` in order and runs each argv:
  `go d.executor.Execute(ctx.Background(), argv, event)`.
- `OSExecutor.Execute(ctx, argv []string, event)` (`executor.go:30`) runs
  `exec.CommandContext(ctx, argv[0], argv[1:]...)`. The env vars, the 10 s timeout and the logging
  are unchanged. An empty argv is logged and skipped.
- `resolveHooksConfig` (`main.go:419-438`) takes the `config.Hooks` and keeps `LogPath`.
- `TestHooksConfigHome` (`cmd/relevo/main_test.go:639-653`) is ported per §8.

### 4.5 Classify key

- `classify.Resolve(cfg *policy.Classify, key string, getenv func(string) string) (Classifier, Status)`
  (`internal/classify/resolve.go:20-70`). The precedence is the env var `TYPESAFE_API_KEY`, then
  `key` (from the DB).
- `Status.KeySource` becomes `"env" | "db" | ""`.
- Delete `Status.KeyPath`, `KeyFileMode` and `KeyFileLoose`, and `keyfile.go`'s `KeyFileUsable`
  (§8 D3). `doctor.ClassifyCheck` (`internal/doctor/doctor.go:851-880`) drops the file-mode texts;
  a missing key says "no classifier key".

### 4.6 `config.Store` and runtime construction

```
func Open(d *db.DB) *Store
func (s *Store) Load() (Loaded, error)          // every section + both secrets; a parse error of a stored
                                                 // body is returned (it cannot happen after a validated Put)
func (s *Store) Put(sec Section, body []byte) ([]string, error)   // Validate first; refused on error; one tx
func (s *Store) PutSecret(name string, value []byte) error        // client.key validated with remote.ParsePrivate
func (s *Store) Version() (int64, error)
func Validate(sec Section, body []byte) ([]string, error)         // the §4.3 parser for sec; Hooks: JSON shape only
func (s *Store) ImportFiles(configDir string, now time.Time) (ImportResult, error)
func LoadFiles(configDir string) (Loaded, error)                   // read-only: today's file reads, for peek
type ImportResult struct { Imported []string; Removed []string }
```

**`ImportFiles(configDir)`**, where configDir is `<userConfigRoot>/relevo`:

```
present = the section files that exist, plus client.key and typesafe.key
validate each (Validate / ParsePrivate / non-empty trimmed key); on the first error:
    return fmt.Errorf("%s: %w", path, err)   // nothing written, nothing removed
if nothing is present and no hooks dir needs importing: return empty
one tx:
  for each section file: ConfigPut(sec, bytes); ConfigImportRecord(file, path, bytes)
  client.key -> SecretPut(SecretClientKey); typesafe.key -> SecretPut(SecretTypesafe, trimmed)
  if the hooks section is absent and <configDir>/hooks/ exists:
      Hooks[event] = the absolute paths of the executable files in <configDir>/hooks/<event>.d,
      sorted by name (today's ReadDir order); ConfigPut(Hooks)
commit
after commit, remove: every imported file; also client.pub when client.key is imported or already
  in the DB; aliases.json (dead since #80)
the hooks/ scripts are NEVER removed; leave every other file (*.bak-*) alone
remove <configDir> itself only when it is left empty (os.Remove, ignoring ErrExist/ENOTEMPTY)
```

**`newRuntime()`** (`main.go:541-637`) becomes:

```
root = store.DefaultRoot(); d = openDB(filepath.Join(root, "relevo.db"))   // error -> return it (fatal)
cs = config.Open(d)
if !d.Newer(): ImportFiles(userConfigRoot()/relevo, now)                  // error -> return it
          else: log once "relevo.db schema is newer; config import skipped"
L = cs.Load()
build the Runtime exactly as today from L:
  - Candidates, Policy, Registry, ConfigWarnings = L.Warnings
  - Classify = classify.Resolve(L.Policy.Classify, L.Typesafe, os.Getenv)
  - Prices + usage reader: newUsageReader(L.Prices)
  - Hooks: newHooksDispatcher(hooks.Config{Hooks: L.Hooks, LogPath: ...}, L.Policy)
  - Remote: newRemoteClient(L.Servers, L.ClientKey, gitClient)
  - rt.Config = cs
```

- `rt.DB` is **not** set here; the existing sites keep setting it. Holding a second handle is fine.
- Add a field `Config *config.Store` to `relevo.Runtime` (`internal/relevo/runtime.go`).
- **Import cycle check:** `internal/config` imports candidate, policy, roles, usage, remote/client
  and db. **If any of those imports `internal/relevo` or `internal/config`, stop.**

**`newRuntimePeek()`** is for `daemon --preflight` and `daemon --check` only. It never creates,
migrates or writes anything:

```
if any config file is present in <userConfigRoot>/relevo -> L = config.LoadFiles(dir)   // what the import would store
else if relevo.db exists -> d = db.OpenReadOnly; L = config.Open(d).Load(); close
else -> L = config.LoadFiles(dir)   // all absent -> empty config, as today
```

`cmdDaemon` (`main.go:2282-2313`) calls `newRuntimePeek()` when `*preflight || *check`, else
`newRuntime()`. `TestDaemonPreflightOpensNothing` (`main_test.go:1183-1205`) and
`TestDaemonCheckLeavesNoDB` (`:604-620`) must pass **unchanged**.

**`newRemoteClient(servers client.Servers, key []byte, git)`** (`main.go:640-664`):
- with no servers: behaviour as today;
- with servers but no key: today's stderr line, reworded "servers configured but no client key;
  run relevo client init";
- the key is parsed with `remote.ParsePrivate`.

### 4.7 ConfigWatcher (`internal/relevo/reload.go`)

- Replace `ConfigPaths` with `ConfigSource`:
  `interface { ImportFiles(dir string, now time.Time) (config.ImportResult, error); Version() (int64, error); Load() (config.Loaded, error) }`,
  plus `ConfigDir string` and `Getenv`.
- **Refresh:**
  1. `ImportFiles` (an error is logged through today's `fail` path and the old copy is kept);
  2. `Version()`; if it is unchanged since the last load, return rt unchanged;
  3. otherwise `Load()` and set `rt.Candidates`, `Policy`, `Registry`, `ConfigWarnings`, and
     `Classify = resolve(L.Policy.Classify, L.Typesafe, getenv)`, as today.

  Prices, servers and hooks keep today's "loaded once at start" behaviour.
- Keep a test seam that replaces the source.
- Wire it at `main.go:2358-2368` with `rt.Config`.
- `reload_test.go` is ported per §8.

### 4.8 Serve (`cmd/relevo/serve.go`)

- `loadCandidatesAndPolicy(configDir)` (261-284) becomes
  `loadServeConfig() (config.Loaded, error)`: it opens the **machine** DB exactly as newRuntime
  does (`store.DefaultRoot()`, not `--state`), runs `ImportFiles` and returns `Load()`.
- `cmdServeRun` (316-472) takes candidates, policy, registry, prices and hooks from it.
- `serveAdminConfigWithCandidates` (295-314): the stat of candidates.json becomes "the loaded
  `Candidates` set is empty". It keeps the same error text, with `no candidates configured`
  replacing the path.

### 4.9 Writers and other readers

| site | change |
|---|---|
| `cmdInit` (`cmd/relevo/init.go:17-78`) | keep `setup.Plan`; replace `setup.Write` with `rt.Config.Put(Candidates, f.Candidates)` and `Put(Policy, f.Policy)` in that order. The refusal becomes "candidates or policy already configured; pass --force to overwrite" when either section exists and not `--force`. The config comes from a runtime or a `config.Open(openDB(...))`, whichever `init` can build without needing candidates. |
| `cmdRolesInit` (`cmd/relevo/roles.go:46-109`) | reads candidates and policy from `Load()`, runs `roles.FromLegacy` as today, and `Put(Roles, encoded)`. Keeps its refusal (roles section exists and not `--force`) and `--dry-run` (print, no Put). |
| `cmdClientInit` (`client.go:50-79`) | refuse `client.ErrKeyExists` when the secret exists; otherwise `remote.Generate()`, `PutSecret(SecretClientKey, remote.MarshalPrivate(kp))`, print the id and the enrolment line as today, where the enrolment line is `remote.MarshalPublic(kp.Public, user@host)` using the same comment rule `InitKey` used (`internal/remote/client/config.go:146-236`; lift that rule into a small helper) |
| `cmdClientAddServer` / `cmdClientRmServer` (`client.go:85-219`) | read `L.Servers`, mutate, `Put(Servers, EncodeServers(...))`; everything else unchanged |
| `cmdServers` (`client.go:224-259`), doctor (`cmd/relevo/doctor.go:283-294`), add-server's enrolment print (`client.go:158`) | derive the enrolment line from the key secret with that helper, instead of reading client.pub |
| doctor prices (`cmd/relevo/doctor.go:274-278,316`; `internal/doctor/doctor.go:184-189,789-835`) | `WithUsage` takes the prices section body (`[]byte`, nil when absent) instead of a path; `usageChecks` parses `as_of` from it; absent → the built-in-prices row as today when the file was missing |
| doctor classify (`doctor.go:392-393`) | `classify.Resolve(rt.Policy.Classify, L.Typesafe, os.Getenv)` via the runtime's loaded config |
| legacy (`internal/legacy/legacy.go:69-71`) | `Unmigrated()` returns `(s.OldState && !s.NewState) \|\| (s.OldConfig && !s.NewConfig && !s.NewState)`. Once a relevo state root exists, config lives in its DB, so a missing `~/.config/relevo` is not unmigrated. Its test gets one row for this case. |

User-facing fix texts that name files (doctor Fix strings, README, flag help) are **left unchanged**
this round; P2b rewrites them around `relevo config`.

## 5. Pseudocode: the import at runtime construction

In §4.6. Everything runs in one transaction. File deletion happens only after commit, so a crash
leaves the files and the next verb re-imports them idempotently: the Put overwrites with the same
bytes, and one more `config_import` row is harmless.

## 6. Error handling

| failure | result |
|---|---|
| DB open / migrate fails in newRuntime | the verb exits 1 with the path and error |
| an invalid config file present | the verb fails with `<path>: <parser error>` (today's text for that file); nothing imported |
| the DB schema is newer | reads work, the import is skipped with a log line, and writers (init, roles init, client) fail with `db.ErrNewerSchema` |
| a Put that fails validation | refused with the parser error; nothing is written |
| file removal after commit fails | slog.Warn, continue (next run re-imports the same bytes) |

## 7. Working efficiently

- **Read in one parallel batch:**
  - `cmd/relevo/main.go` 405-455, 541-664, 2282-2430;
  - `cmd/relevo/serve.go` 250-410;
  - `cmd/relevo/{init,roles,client,usage_wire}.go`;
  - `cmd/relevo/doctor.go` 254-420;
  - `internal/relevo/reload.go`;
  - `internal/hooks/{events,dispatcher,executor}.go`;
  - `internal/classify/{resolve,classify,keyfile}.go`;
  - `internal/remote/client/config.go`;
  - `internal/db/{db,migrate,write,read}.go` and `migrations/001_initial.sql`;
  - `internal/legacy/legacy.go` 60-80.

  All locations are given; do not search for them again.
- One edit call per file. The Parse extractions (§4.3) are mechanical: do each loader file in one edit.
- **Focused loop:**
  `go test ./internal/db/ ./internal/config/ ./internal/candidate/ ./internal/policy/ ./internal/roles/ ./internal/usage/ ./internal/classify/ ./internal/hooks/ ./internal/remote/... ./internal/relevo/ ./internal/doctor/ ./internal/legacy/ ./cmd/relevo/ -count=1`.
  Fix every failure before re-running.
- **Full check** once at the end: `make check`, then `make e2e`.
- **CI has no harness and no network.** New cmd/relevo tests call functions (`newRuntime`,
  `config.Store` methods, `cmdClientInit`, `cmdInit` with a fake InstallEnv if one exists) against
  a temp state and config root. They never spawn a harness or dial a server.
- **Per-test roots:** a cmd/relevo test that writes config must set **both** `XDG_CONFIG_HOME`
  and `XDG_STATE_HOME` to its own `t.TempDir()` (the #235 rule). TestMain's shared state root
  would otherwise carry one test's imported config into the next.

## 8. Ordered steps

**Closed deletion list:**
- **D1:** `client.LoadServers`, `SaveServers`, `KeyPaths`, `ServersPath`, `LoadKey`, `InitKey`
  (`internal/remote/client/config.go:30-236`, keeping `ServerEntry`, `Servers`, `ValidateEntry`,
  the errors, and the user@host comment rule lifted into the helper), plus their tests in
  `config_test.go`.
- **D2:** `setup.Write`, `WriteResult`, `fileExists` (`internal/setup/setup.go:38-41,96-126`) and
  their tests.
- **D3:** the classify key-file mode handling (§4.5) and its tests.
- **D4:** the hooks `.d` directory scan (§4.4) and the tests that write `.d` scripts; port them to
  argv lists.
- **D5:** `ConfigPaths` and file stamps in reload.go and their tests; port them to the source seam.

Everything not listed survives.

**Sanctioned test ports** (change assertions, never delete a behaviour test):
- tests of the D-items;
- `cmd/relevo/main_test.go` `TestCandidatesConfigHome`, `TestDiffHonoursConfigHome`,
  `TestHooksConfigHome`, `TestDaemonPreflightFailsOnBadConfig` (each gets its own
  XDG_STATE_HOME; file writes stay, and assertions move to the DB where they read files back);
- `cmd/relevo/init_test.go` (read the sections back from the DB);
- `cmd/relevo/doctor_test.go` (prices and client.pub inputs);
- `internal/classify/resolve_test.go`, `internal/relevo/reload_test.go`,
  `internal/hooks/dispatcher_test.go`, `internal/doctor/doctor_test.go` (prices/typesafe rows),
  `internal/legacy` (one new row).

**Any other test that fails: stop and report**, with its name and why.

1. **Migration 002 + `internal/db/config.go` + the chmod in `db.Open` + `OpenReadOnly`.** Test in
   `internal/db/config_test.go`:
   - Put/Get/Delete round-trips;
   - each Put or Delete increments the version;
   - `OpenReadOnly` on a schema-1 DB reports absent and version 0, and on a missing file wraps
     `os.ErrNotExist`;
   - the file mode is 0600 after `Open`.

   Verify: `go test ./internal/db/`.
2. **The §4.3 Parse functions.** Verify: every loader package's tests pass unchanged.
3. **`internal/config`: Store, Load, Validate, ImportFiles, LoadFiles.** Tests:
   - an import of all five files plus both keys plus a hooks dir → sections, secrets and hooks are
     stored, the files are removed (client.pub too), the hooks scripts remain, the config dir
     remains because hooks/ is inside it;
   - an invalid policy.json → an error naming its path, and nothing written or removed;
   - an empty config dir → no-op;
   - `.bak` files left alone;
   - `LoadFiles` equals `Load` after an import of the same files.

   **Mutation check:** move the file removal before the commit, and confirm that a test injecting a
   commit failure (or a Put error) sees the files deleted, i.e. the test fails. Then revert. If no
   seam allows a commit failure, say so.
4. **Classify and hooks** (§4.4, §4.5).
5. **Runtime:** `newRuntime`, `newRuntimePeek`, `newRemoteClient`, the daemon wiring, the watcher
   (§4.6, §4.7). The two "no DB" daemon tests must pass unchanged.
6. **Serve** (§4.8) and **writers/readers** (§4.9), including legacy.
7. **Full check:** `make check` and `make e2e`. Report:
   - both tails;
   - `git diff --stat`;
   - the mutation result;
   - every deleted test with its D-number;
   - every ported test, with one line on what changed.
