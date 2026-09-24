# P3b round 2: the planner registry, channel claims, agy creds and the hooks log move into relevo.db

Continue on `relevo/p3b`. Round 1 (the kv API, `db.KVImportFile`, `store.DB()/DBIfExists()`) is
merged to main, and the planner rebased this branch onto main. The same design as round 1
applies: each record is a **kv row holding the same JSON** the file held, and a file that is
present is imported and removed. **No migration.**

**Stop rule:** if a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit. Tests change only as §8 sanctions.

## 1. System overview

| today | becomes | where |
|---|---|---|
| `<root>/planners/<id>.json` + `planners/.lock` | kv `planner/<id>` | the store root DB (`rt.Store.DB()`) |
| `<root>/channels/<plannerID>.json` | kv `claim/<plannerID>` | the store root DB |
| `<root>/planners/.agy/<conv>.json` | secret `agy/<conv>` (the `secret` table, 0600 DB) | the machine DB (`store.New(store.DefaultRoot()).DB()`) |
| `<root>/hooks.log` | kv `hooks.log`: a JSON array of the last 200 runs | the machine DB |

## 2. File structure

```
internal/planner/registry.go (+ _test)  NEW DBRegistry implementing Registry over kv; FileRegistry deleted (§8 D1)
internal/planner/hook.go                 Init's locked path uses DBRegistry's tx instead of fr.withLock
internal/relevo/channel.go (+ _test)     KVClaims replaces FileClaims (D2)
internal/relevo/agy_creds.go (+ _test)   capture/read/prune over secrets (D3)
internal/relevo/deliver_agy.go           reads creds via the secret store
internal/hooks/executor.go, events.go    run output + failures to a sink, not a file (D4)
cmd/relevo/{main,planner,mcp,doctor}.go  wiring
```

## 3. Data structures

- **`planner/<id>`:** exactly `json.Marshal(planner.Record)`, the same shape the file held. The
  golden `internal/planner/testdata/record-shape.golden` still pins it.
- **`claim/<id>`:** exactly `json.Marshal(relevo.Claim)`.
- **`agy/<conv>` secret:** exactly `json.Marshal(AgyCreds)`. The token stays redacted by
  `String`/`GoString` as today.
- **`hooks.log`:** `[{"at","event","argv","exit_code","error","output"}]`, the newest last, capped
  at 200 entries. `output` holds the last 4 KiB of the child's combined stdout/stderr.

## 4. Contracts

### 4.1 `planner.DBRegistry{KV DBTxKV; Now func() time.Time}`

It implements every method of `planner.Registry` (`registry.go:34`) with today's semantics:
- lookups scan all `planner/` keys (`KVKeys`), as today's dir scan does, and a corrupt record
  fails loudly;
- name uniqueness and the `ErrNewerFormat` refusal on write are unchanged.

`DBTxKV` is the minimal interface `planner` needs:
- `KVGet`, `KVPut`, `KVDelete`, `KVKeys`;
- `Tx(func(KVTx) error) error`, where `KVTx` exposes the same four inside one `BEGIN IMMEDIATE`.

Add the adapter over `*db.DB` in `internal/db/kv.go`, if round 1 did not already give `db.Tx` the
kv methods.

**Every mutation runs in one `Tx`,** replacing `FileRegistry.withLock`: Create, MoveSession,
SetHost, Rename, Touch/touchForced and Forget. `planner.Init` (`hook.go:177-211`) runs
`initLocked` inside one `Tx` via a `regOps` over `KVTx`. The `PriorID` callback may open a
**separate** read-only handle during that tx; WAL allows it.

**Import:** on the first registry call in a process, every `planners/*.json` file present is put to
`planner/<id>` and removed. Then `planners/.lock` is removed, and `planners/` too if it is left
empty, but **not** if `.agy/` is still in it; §4.3 empties that. A malformed record file fails
the import loudly, leaving it in place, as a corrupt file failed `list` today.

**Wiring:** `cmd/relevo/planner.go:67` `plannerRegistry(rt)` returns
`&planner.DBRegistry{KV: <rt.Store.DB()>, Now: rt.Now}`. A `DB()` error is fatal for the verb.
`newRuntimePeek` must not open the DB for this: give Peek a nil `Planners`, and check that
preflight and check never call it.

### 4.2 `relevo.KVClaims{KV DBTxKV; Alive func(int) bool}`

It implements `ClaimStore` (`channel.go:37`) with today's semantics:
- `Live` removes a stale claim on read;
- `Write` returns `ErrClaimHeld` when another live pid holds the claim;
- `Remove` acts only when the pid matches;
- `SweepPaneKeyed` sweeps as today.

`Write`'s check-then-write now runs in one `Tx` and becomes atomic. **Say so in its doc comment.**

**Import:** `channels/*.json` present → `claim/<id>`, removed; `channels/` removed when empty.

**Wiring:** `main.go:695` sets `Channels: &relevo.KVClaims{KV: st.DB(), ...}`.

### 4.3 agy creds

- `CaptureAgyCreds(env, secrets SecretStore, now)` and `ReadAgyCreds(secrets, conv)`, where
  `SecretStore` is `{SecretGet, SecretPut, SecretDelete, SecretNames}` over the machine DB.
- The prune walks `SecretNames` with the prefix `agy/` and drops entries older than 7 days by
  `CapturedAt`.
- `captureAgyEnv` (`main.go:510-515`) runs on **every verb**. It must stay silent and never change
  an exit code: open the machine DB **only when the ANTIGRAVITY_* env is present**, as the capture
  itself already short-circuits.
- **Import:** `planners/.agy/*.json` present → the secret, then removed, then `.agy/` removed if
  empty.
- `AgyDeliverer.CredsDir string` becomes `Creds SecretStore`. A nil store gives today's
  `OutcomeNotMine`.

### 4.4 The hooks log

- `hooks.Config.LogPath` becomes `Log RunLog`, where
  `type RunLog interface { Append(HookRun) error }` and `HookRun` is §3's element.
- `OSExecutor.Execute` captures the child's combined stdout and stderr into a buffer capped at
  64 KiB instead of the file, and appends one `HookRun` for **every** run. Today it logs only
  failures to the file. So that nothing is lost, the child's output was the file's content, so
  keep the whole capped tail. The `error` field is set on failure.
- `WebhookSink`'s failure log (`openHooksLogAppend`, `main.go:455-467`) appends a `HookRun` with
  `event` = the webhook event, `argv` = `["webhook", url]` and the error.
- **The kv implementation of RunLog:** read the array, append, cap it at 200, write, in one Tx.
- **Import:** `<root>/hooks.log` present → one `HookRun{event:"imported", output: <last 4 KiB of
  the file>}`, then the file is removed.
- **Doctor** gains one row: "hooks: N runs, M failed in the last 24h", plus the last failure's
  event and error. The row is OK when M == 0 and a warning otherwise. Keep the text on one line.

## 5. Pseudocode

In §4. Every import follows round 1's `KVImportFile` pattern: put, then remove, never the reverse.

## 6. Error handling

- Each package returns errors exactly where the file version did.
- The agy capture and the hook log are best-effort (errors are dropped silently, as today).
- Registry and claim errors propagate as today.

## 7. Working efficiently

- **Read in one batch:**
  - `internal/planner/{registry,hook,resolve,prune,planner}.go`;
  - `internal/relevo/{channel,agy_creds,deliver_agy}.go`;
  - `internal/hooks/{executor,events,dispatcher,webhook}.go`;
  - `cmd/relevo/main.go` (captureAgyEnv ~502-535, resolveHooksConfig/newHooksDispatcher/openHooksLogAppend ~430-470, buildRuntime ~646-705, the daemon's hooks rebuild);
  - `cmd/relevo/{planner.go:60-70,220-265; mcp.go:180-245; doctor.go:600-665}`;
  - `internal/db/kv.go`.
- **Focused loop:** `go test ./internal/planner/ ./internal/relevo/ ./internal/hooks/ ./internal/doctor/ ./internal/db/ ./cmd/relevo/ ./internal/e2e/ -count=1`.
- **Full check** once at the end: `make check`, then `make e2e`.
- **CI has no harness and no network.**

## 8. Ordered steps

**Closed deletion list:**
- **D1:** `planner.FileRegistry`, its flock (`lock_unix.go` in planner if only it uses it) and
  its file I/O.
- **D2:** `relevo.FileClaims` and `ClaimFileName`, if nothing else uses them.
- **D3:** the agy file read/write/prune.
- **D4:** `hooks.Config.LogPath` and the file writes in the executor and in `openHooksLogAppend`.

**Sanctioned ports:**
- The 15 `FileRegistry{` literals in the 9 test files the map found:
  - `cmd/relevo/planner_test.go`;
  - `internal/relevo/{daemon,bind,remote}_test.go`;
  - `internal/planner/{init,planner,resolve}_test.go` and the `testRegistry` helper;
  - `internal/e2e/{headless,remote}_test.go`.

  Change each to a `DBRegistry` over a temp DB. A helper in each package is fine.
- `internal/planner/planner_test.go:357` (it reads a record file): read the kv row.
- `format_test.go` hand-written record files: put the bytes as a kv row, or keep the file and
  exercise the import.
- `channel_test.go` (23), `agy_creds_test.go` (13), `deliver_agy_test.go` (5),
  `internal/e2e/headless_test.go` claims (2).
- `internal/hooks/executor_test.go` (4), and `cmd/relevo/main_test.go`'s one hooks.log line.

**Any other failing test: stop and report.**

1. **DBRegistry + the Init tx + the import + wiring.** **Mutation:** move the planner file removal
   before the kv put, and confirm an import-failure test notices. Then revert.
2. **KVClaims.**
3. **agy creds.**
4. **The hooks run log + the doctor row.**
5. **Full check.** Report:
   - the tails;
   - `git diff --stat`;
   - D-numbered deletions;
   - ported tests;
   - the mutation result;
   - `find <a fresh temp state root> -type f` after `make e2e`, if easy.

   Commit as one commit. Do not rebase.
