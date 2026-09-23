# Plan: version skew in shared state, round 2 of 2: lenient readers and the DB (#372)

**Read first:** `docs/specs/2026-09-23-state-skew-design.md` in your tree (committed in round 1), §3 (R2 types) and §4.2–4.6. Round 1 added `internal/jsonshape`. Where this plan and the spec disagree, the spec wins. Stop and report rather than pick.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit, except the tests this plan names as encoding the old rule.

**Parallel branch:** #373 edits `internal/serve`, `internal/remote`, `internal/relay/remote.go` and `internal/relay/runtime.go`, plus `cmd/relay/main.go`: a `client.Version` line early in `main`, and `rt.AuthGrace` in the daemon. In `cmd/relay/main.go`, keep your edits to the daemon's DB-open block and the `relay available` pointer read.

## 1. System overview

Round 1 stopped older binaries overwriting newer bindings and planner records. This round fixes the files that are shared differently:

- **The ledger:** one unknown entry currently disables every rate-limit gate. After this round, unknown entries are preserved and ignored.
- **`log.jsonl`:** the confirm rewrite drops unknown keys. After this round it patches lines instead.
- **`policy.json` and `candidates.json`:** they reject what a newer relay documents. After this round they warn instead, surfaced in `relay doctor`.
- **The DB:** migrations race, and an older binary writes into a newer schema. After this round migrations are guarded, and ingest pauses on a newer schema.

It also fixes two small bugs: the daemon opens (and migrates) the DB even for `--check`, and `relay available` reads the serve pointer from the wrong root.

## 2. File structure

```
internal/ledger/ledger.go, ledger_test.go        §4.2
internal/store/log.go, log_test.go               §4.3 (confirmIndex, log.go:446-476)
internal/policy/policy.go, policy_test.go        §4.4 (policy.go:494-510)
internal/candidate/candidate.go, candidate_test.go  §4.4 (unknown harness ~201, unknown role ~208)
internal/relay/reload.go                         log each config warning once (the ConfigWatcher already dedupes errors; reuse that)
internal/doctor/doctor.go (+ env.go, test)       "config" row
cmd/relay/main.go                                newRuntime carries warnings to where doctor and the daemon need them; daemon DB-open order + Newer(); relay available pointer root
cmd/relay/db.go                                  `relay db migrate` refuses a newer schema
internal/db/migrate.go, db.go (+ tests)          §4.5
```

## 3. Data structures

- `ledger.Ledger.Other []json.RawMessage`.
- `policy` warnings and `candidate` warnings as `[]string`.
- `db.DB.Newer() bool`, plus the version pair for the message. Add whatever minimal accessor the daemon's Warn text needs, e.g. `SchemaVersions() (have, know int)`.

## 4. Interfaces and contracts

Spec §4.2–§4.6, verbatim. Clarifications:

- **Ledger.** `mutateLedgerLocked` and `Available` do Load → mutate → Save. They must carry `Other` through untouched. `Entry` gains no field.
- **confirmIndex.** Only the lines whose `confirmed` value changes are re-marshalled from a map. Every other line keeps its exact bytes. `Seq` stays a newline count.
- **Policy.**
  - Unknown-key detection walks the decoded `map[string]any` against `jsonshape.Keys(reflect.TypeOf(Policy{}))`.
  - A key inside a map-typed field (a field whose shape path ends in `{}`) is **not** unknown: any key is allowed there.
  - Known keys with bad values still fail exactly as today (every `ErrBadPolicy` path).
  - `TestRefreshKeepsLastGoodOnBadPolicy` must keep passing. If its "bad policy" is only an unknown key, change its fixture to a bad *value* (e.g. `cpu_weight: 0` if that is invalid, or another validated field). This plan authorises that fixture change; report it.
- **Candidates.** Only an unknown harness and an unknown role become skip-with-warning. Duplicates, bad tree/tier/patterns and everything else still fail.
- **Warnings plumbing.**
  - `newRuntime` must not print warnings: every CLI command would be noisy.
  - Carry them on `relay.Runtime` as `ConfigWarnings []string`, or return them another way you judge simpler, and say which.
  - Doctor renders the `config` row from them. The daemon logs each once at start, and on reload when the set changes.
- **DB.**
  - A newer schema **must not** be migrated or written by the daemon.
  - `openDB` today always migrates. Make `Open` check the schema version *before* applying anything: if `max(schema_version)` is greater than the embedded maximum, skip migrating and return the DB with `Newer() == true`.
  - Inside `applyOneMigration`, use a dedicated `*sql.Conn` and `BEGIN IMMEDIATE`. Re-select the version, and skip if it is present.
  - If the driver can't do `BEGIN IMMEDIATE` on a conn, halt and report the driver and what you tried.
- **The daemon's DB open** moves after the `--check` early return and after `AcquireDaemonLock`. Keep `closeDB`'s existing guard from #371.
- **The pointer read** in `relay available` uses `defaultServeRoot()` (defined in cmd/relay for `relay serve`).

## 5. High-level pseudocode

```
ledger.Load:
  raw := decode {"entries": []json.RawMessage}
  for r in raw: var e Entry; if json.Unmarshal(r,&e) fails -> error (malformed)
               if !knownKind(e.Kind) || !knownSource(e.Source) -> l.Other = append(l.Other, r); continue
               l.Entries = append(l.Entries, e)
ledger.Save: entries := marshal each known entry to RawMessage; append l.Other...; write {"entries": entries}

db.Open(path):
  open; have := maxVersion(); know := maxEmbedded()
  if have > know -> return DB{newer:true}
  applyMigrations (each: conn; BEGIN IMMEDIATE; if versionApplied(n) {COMMIT; continue}; exec file; insert; COMMIT)
```

## 6. Error handling strategy

- A malformed ledger JSON is still an error, and `Gates` still reads it as empty with its stderr note. Only an *unknown kind or source* is preserved.
- Config warnings never fail a command. Value errors still do.
- A newer DB schema → the daemon runs without ingest, with a Warn. `relay db migrate` fails with the message. Readers are unaffected.

## 7. Ordered implementation steps

1. **Ledger** (§4.2).
   - Change `TestLoadValidation`'s "unknown kind" case to expect preservation. That case encodes the old rule, and this plan authorises the change.
   - New tests:
     - an unknown-kind entry and an unknown-source entry survive Load, `mutateLedgerLocked` (append a `rate_limited`) and Save, byte-equivalent as JSON values;
     - `Gates` ignores them and still returns the known gates.
   - Mutation: drop `Other` from Save, and the survival test fails.
2. **`confirmIndex`** (§4.3).
   - Test: a log whose lines carry an extra `"future_key": 1`, after `ConfirmIndex`, still has `future_key` on every line. Unchanged lines are byte-identical.
   - Existing log tests pass unedited.
3. **Policy** (§4.4), with tests:
   - an unknown top-level key gives a warning and a loaded policy;
   - an unknown nested key gives a warning with its dotted path;
   - a key under a map-typed field gives no warning;
   - a bad value is still an error.
4. **Candidates** (§4.4), with tests:
   - an unknown harness is skipped with a warning, and the others load;
   - an unknown role is skipped with a warning;
   - a duplicate still fails.
5. **Warnings plumbing, doctor `config` row, and daemon logging.**
   - Doctor tests: no warnings → OK; two warnings → Warn listing both.
   - Add no cmd/relay test that spawns a harness or reaches the network (CLAUDE.md). If a cmd/relay test needs config, it writes it under a `t.TempDir()` set as `XDG_CONFIG_HOME` (#235).
6. **DB** (§4.5). Tests in `internal/db`:
   - open, then insert a `schema_version` row with version 99, then Open again → `Newer()` is true and no migration ran (assert the table count is unchanged);
   - two goroutines each Open the same fresh path concurrently → both succeed, and `schema_version` has exactly one row per migration. Run it 20 times in a loop inside the test;
   - `relay db migrate` on a newer DB returns an error: test the function it calls, not the CLI.

   Mutation: remove the in-transaction re-check, and the concurrency test fails. If it doesn't fail reliably, say so; don't weaken the test.
7. **Daemon order and pointer fix** in cmd/relay (§4.5, §4.6).
   - `--check` must not create `relay.db` in a fresh state root. Test through the smallest function you can extract, or assert with a temp `XDG_STATE_HOME` that `cmdDaemon([]string{"--check"})` leaves no `relay.db`. It exits via `os.Exit` today, so extract a helper if needed and say so.
   - Pointer read: test the pure root choice if you extract one. Otherwise note it as covered by review.
8. **Gate:** `make check` and `make e2e`. Commit ending `(#372)`.

Declared scope: §2's files and their tests.
