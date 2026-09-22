# #303 step 1a: the planner registry, `relay planner`, and the plugin's SessionStart hook

This plan stands alone: everything you need is in this file and in the tree.
The design is `docs/specs/2026-09-22-drop-herdr-design.md` (§3.1, §3.4,
§3.5, §4.1–§4.4, §4.7, §5.1). Read those sections first; where this plan
and the spec disagree, **this plan wins**, and you say so in your report.
If a step is impossible as written or contradicts the code, **halt and
report**. Do not improvise around it.

**Work directly: do not dispatch sub-agents or explore agents.** The spec is
on this branch at `docs/specs/2026-09-22-drop-herdr-design.md`.

## Scope

This round **adds** the planner registry and the verbs that manage it. It
does **not** wire the registry into `bind`/`add`/`fork`/`ask`, `relay mcp`,
channel claims, `DeliverPending` or `doctor`: that is step 1b, a later
round. Nothing in this round removes or changes herdr code. Another builder
is deleting pane-mode *builder* code in parallel (step 2). Stay out of
`internal/relay/{reconcile,bind,add,fork,ask,stop,answer,switch,pause,consult,dialog,limit,repair,progress}.go`,
`internal/pick`, `internal/ui` and `internal/harness`, so the two rounds
merge cleanly.

Files you may create or change:

```
internal/planner/planner.go       new
internal/planner/registry.go      new
internal/planner/resolve.go       new
internal/planner/hook.go          new
internal/planner/ident.go         new
internal/planner/*_test.go        new
internal/store/store.go           add PlannersDir() only
internal/store/types.go           add Binding.PlannerID only
internal/db/types.go, write.go, read.go   id-first UpsertPlanner; PlannerBySession
internal/db/*_test.go
internal/ingest/ingest.go         pass b.PlannerID into UpsertPlanner
internal/ingest/*_test.go
cmd/relay/planner.go              new: `relay planner ...`
cmd/relay/planner_test.go         new
cmd/relay/main.go                 one dispatch line + usage text
claude-plugin/hooks/hooks.json    new
```

If you find you must touch a file outside this list, halt and report
which file and why.

## 1. Data: `internal/planner/planner.go` (no I/O)

- `type Record struct` with exactly the spec §3.1 fields and JSON names:
  `id`, `name`, `harness_kind`, `session_id`, `sessions`, `host_pid`,
  `host_started_at`, `cwd`, `transcript_locator` (omitempty), `created_at`,
  `seen_at`.
- `type SessionRef struct { SessionID string json:"session_id"; From time.Time json:"from"; To time.Time json:"to" }`.
- `NewID(rand io.Reader) (string, error)`: `pl_` + 12 chars from lowercase
  RFC 4648 base32 (`a-z2-7`), taken from `crypto/rand` in production.
- `ValidID(string) error`: matches `^pl_[a-z2-7]{12}$`.
- `ValidName(string) error`: matches `^[a-z][a-z0-9-]{0,31}$`.
- `DefaultName(agent, kind string, taken func(string) bool) string`: the
  first free `<base>-<n>` for n = 1, 2, …, where base = `agent` when it's a
  valid name prefix, else `kind`.
- `(r Record) Validate() error`: every "required" constraint in §3.1, the
  opencode `^ses_[A-Za-z0-9]+$` session rule, and `host_started_at == 0`
  whenever `host_pid == 0`.
- Sentinel errors: `ErrNotFound`, `ErrNameTaken`, `ErrSessionTaken`,
  `ErrHostTaken`, `ErrInUse`, `ErrInvalid`, `ErrNoPlanner`, plus
  `type ErrUnknownPlanner struct{ Ref string }` with an `Error()` method.
- The `sessions` history is capped at 20: appending a 21st drops the oldest.

## 2. `internal/planner/registry.go`

`Registry` is the interface in spec §4.1, **including** `ByHost`,
`MoveSession` and `SetHost`. `FileRegistry{Root string; Now func()
time.Time}` stores one file per record at `<Root>/<id>.json`, written as a
temp file plus rename. It also:
- enforces the §3.1 invariants on every write, returning `ErrSessionTaken`
  or `ErrHostTaken` when another record holds the current session or a
  non-zero host;
- makes `MoveSession` append the old session to `sessions` with `To = now`;
- has `Forget` take an `inUse func(id string) bool` callback supplied by the
  caller (the CLI passes one that scans non-DONE bindings for
  `PlannerID == id`).

Locking: every mutating method takes an exclusive `flock` on
`<Root>/.lock` for the read-modify-write, so `relay planner init` and a
concurrent process serialise. Use `syscall.Flock`, guarded by build tags the
same way the tree already guards its store lock. Find how `internal/store`
locks `.lock` and mirror it; don't invent a new scheme.

`ByHost(pid, startedAt)` matches only when **both** are equal and pid > 0.

## 3. `internal/planner/ident.go` and `resolve.go`

- `Detect(env func(string) string, ppid int) (Ident, bool)`, exactly as in
  spec §4.2. For claude, `HostPID` = `strconv.Atoi(env("CLAUDE_PID"))` when
  that parses and is > 0, else `ppid`. Only `claude` is detected; every
  other harness returns `false`.
- `Resolve(reg Registry, in ResolveInput) (Record, Resolution, error)`,
  exactly as in spec §4.3. **It never creates a record.** The order is flag,
  `RELAY_PLANNER`, host, then session. A flag or env value is looked up as an
  id first (when `ValidID`), else as a name. A miss is `ErrUnknownPlanner`.
  `ProcStart` errors on the host step mean "no host match" and fall through
  to the session step. They are not an error.
- On any hit, call `reg.Touch` (best effort; ignore its error).

## 4. `internal/planner/hook.go`

- `type HookInput struct` with the spec §3.4 fields; `ParseHookInput(r
  io.Reader) (HookInput, error)` requires `session_id` and `cwd` and ignores
  unknown fields.
- `HookOutput(r Record) []byte`: the exact JSON in spec §3.4, with the
  `additionalContext` text written verbatim. `HookNote(msg string) []byte`
  is the same envelope with `msg` as the context, used on failure.
- `EnvLine(id string) string` = `export RELAY_PLANNER=<id>\n`.
- `Init(reg Registry, in InitInput) (Record, InitResult, error)`
  implements spec §5.1 as a pure function over its inputs:
  `InitInput{ Kind, SessionID, TranscriptPath, CWD, Name, Agent string;
  HostPID int; HostStartedAt int64; Now time.Time; PriorID func(kind,
  session string) (string, bool) }`. `InitResult` is `"created" |
  "reattached" | "moved"`. `PriorID` is how the CLI supplies §3.5's "reuse
  the db row's id"; nil means none. Do all lookups and writes under one
  registry lock (add an unexported `withLock` helper). `Name` non-empty on
  an existing record renames it.

## 5. `cmd/relay/planner.go`: the verbs

```
relay planner init [--name N] [--kind K --session S] [--hook claude]
relay planner list [--json]
relay planner rename <id|name> <new-name>
relay planner forget <id|name>
```

- **Registry root** is `store.PlannersDir()` =
  `filepath.Join(s.root, "planners")`. Create it on first write, with mode
  0700.
- **`init --hook claude`**:
  1. read stdin, then `ParseHookInput`;
  2. host = `os.Getppid()`, and host start time via the same `ps -o lstart=`
     read that `internal/proc` uses. Export a small `proc.StartTime(ctx,
     pid) (time.Time, error)` from `internal/proc` if none is exported;
     that is the one allowed change outside §Scope, and you name it in the
     report;
  3. `PriorID` = `db.PlannerBySession` on the relay db. If the db can't be
     opened within 2 s, `PriorID` is nil;
  4. `Init`, with `Agent` taken from `CLAUDE_CODE_AGENT`;
  5. append `EnvLine` to `$CLAUDE_ENV_FILE` when set;
  6. print `HookOutput`.
  
  **Always exit 0.** Any error prints `HookNote("relay planner init
  failed: <err>")` on stdout and the error on stderr.
- **`init --kind K --session S`**: explicit registration with `HostPID` 0.
  It prints `planner <name> (<id>) <created|reattached|moved>` and, on a
  second line, `export RELAY_PLANNER=<id>`.
- **`init`** with no flags uses `Detect(os.Getenv, os.Getppid())`. When
  that fails, exit 1 with "not in a detectable planner session: pass --kind
  and --session".
- **`list`** is a table of `name id kind session host cwd seen`, sorted by
  name. `--json` prints the records array.
- **`rename`** and **`forget`** print one confirmation line. `forget`'s
  `inUse` walks the store's bindings (the same listing `relay status --all`
  uses) and treats any non-DONE binding whose `PlannerID` matches as in use.
- Register `case "planner":` in `main.go`'s dispatch next to `"policy"`,
  and add the verb to the usage text in the style the others use.

**CI constraint:** `cmd/relay` tests must not run a subcommand that reaches
herdr. `relay planner` never touches herdr, so `cmd/relay/planner_test.go`
may run the verbs in-process. It must set `XDG_STATE_HOME` to a
`t.TempDir()` (TestMain already isolates HOME/XDG). Put rule tests in
`internal/planner`.

## 6. db and ingest

- `db.UpsertPlanner`: when `p.ID != ""`, upsert **by id** (insert with that
  id, or update `harness_kind`, `session_id`, `last_seen`, and
  `transcript_locator` when set). The natural-key unique index still
  applies, so a conflicting `(kind, session)` under another id returns an
  error wrapping `ErrInvalid`. When `p.ID == ""`, behaviour is unchanged.
- Add `func (d *DB) PlannerBySession(kind, session string) (Planner, bool, error)`.
- `store.Binding` gains `PlannerID string json:"planner_id,omitempty"`,
  with a doc comment pointing at the spec. Nothing sets it in this round.
- `internal/ingest/ingest.go` passes `ID: b.PlannerID` in its
  `UpsertPlanner` call. That is a no-op until 1b sets the field.

## 7. `claude-plugin/hooks/hooks.json`

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "relay planner init --hook claude" } ] }
    ]
  }
}
```

Don't change `plugin.json` or any manifest version.
`scripts/check-plugin-version.sh` must still pass.

## 8. Tests you must add (named, each must fail if its rule is removed)

`internal/planner`:
- `TestNewIDShape`, `TestValidName`.
- `TestDefaultNamePicksSmallestFree`: `architect-1` taken → `architect-2`;
  an agent that isn't a valid prefix → the kind.
- `TestRegistryCreateRejectsSessionTaken`, `TestRegistryCreateRejectsHostTaken`.
- `TestMoveSessionAppendsHistoryAndCapsAt20`.
- `TestByHostRequiresStartTimeMatch`: same pid, different start → `ErrNotFound`.
- `TestResolveOrder`: one table covering flag > env > host > session, a
  host `ProcStart` error falling through to session, and an unknown flag
  giving `ErrUnknownPlanner`.
- `TestResolveNeverCreates`: an empty registry gives `ErrNoPlanner`, and
  the directory has no files.
- `TestInitCreatesThenReattachesSameHost`: a second `Init` with the same
  host and a new session gives `"moved"`, the same id, and the old session
  in `sessions`. This is the `/clear` case.
- `TestInitReattachesBySessionInNewProcess`: a new host with the same
  session gives the same id and an updated host. This is `--resume`.
- `TestInitReusesPriorDBID`.
- `TestHookOutputExactJSON`: golden bytes.
- `TestConcurrentInitSerialises`: two goroutines `Init` the same host and
  session through two `FileRegistry` values on one root, ending with
  exactly one record.

`internal/db`: `TestUpsertPlannerByID`, `TestUpsertPlannerByIDConflictingNaturalKey`, `TestPlannerBySession`.

`cmd/relay`:
- `TestPlannerInitHookAlwaysExitsZero`: malformed stdin exits 0 and prints
  the note envelope.
- `TestPlannerInitHookWritesEnvFile`: `CLAUDE_ENV_FILE` points at a temp
  file that then contains the export line.

Mutation checks: do these two and report both results.
1. In `Resolve`, delete the host step. `TestResolveOrder` must fail.
2. In `Init`, mint a new id instead of reusing the host match.
   `TestInitCreatesThenReattachesSameHost` must fail.

Restore the code after each mutation by **re-editing**, not with `git
checkout`.

## 9. Verification before you report

- `make check` passes, including the gofmt check,
  `test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }`.
- `git diff --stat` touches only §Scope's files, plus `internal/proc` if
  you exported `StartTime`.
- Report: files changed, the tests added, both mutation results, and any
  place you departed from this plan or the spec, with the reason.
- Commit on the current branch with a conventional message, `feat(planner):
  …`. Don't rebase and don't push; relay collects the branch.
