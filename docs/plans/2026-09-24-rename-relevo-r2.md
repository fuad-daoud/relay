# Plan: #292 round 2: the legacy table, old-marker readers, the embedded units, the doctor row, the unmigrated guard

Spec: `docs/specs/2026-09-23-rename-relevo-design.md` (§1 legacy reads, §3). Round 1
(`9c66486`) renamed everything. This round adds what a renamed binary needs in
order to live beside relay-era state. `relevo migrate` itself is round 3; do not
start it here.

## 1. System overview

After round 1, relevo knows no old name at all. That breaks three things on an
upgraded machine:

1. **Old round logs.** Pre-cutover logs end in `relay-exit:<n>` and carry
   `relay-rusage:` lines. The usage reader re-reads old streams and calls one
   without the new marker "stream still open", so old rounds' cost figures go
   wrong. The gate log tail and `proc`'s readers have the same blind spot.
2. **The ledger.** Rate-limit gates recorded before the cutover have `"source":
   "relay"`. Since #372, an unknown source is kept in `Other` and ignored, so
   every pre-cutover gate would silently lapse.
3. **Old roots.** A `relevo` run before `relevo migrate` sees empty
   `~/.config/relevo` and `~/.local/state/relevo` and starts creating them. It
   looks like a fresh install while the real state sits beside it. Worse, it then
   blocks `migrate`, which refuses to move into a non-empty new root.

This round adds:

- `internal/legacy`: the only home of old names;
- readers that accept the old markers and the old ledger source;
- `dist`: a Go package that embeds the unit files for round 3;
- a doctor `rename` row;
- a guard in `run()` that refuses every verb except `help`, `version`, `doctor`
  and `migrate` while an old root exists without its new one.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 2. File structure

```
internal/legacy/legacy.go        NEW  old names + root probing (leaf package: imports only stdlib)
internal/legacy/legacy_test.go   NEW
dist/dist.go                     NEW  package dist: go:embed of relevo.service and the plist template
dist/dist_test.go                NEW
internal/doctor/rename.go        NEW  RenameCheck (pure)
internal/doctor/rename_test.go   NEW
cmd/relevo/rename.go             NEW  renameRoots, guardExempt, refuseUnmigrated
cmd/relevo/rename_test.go        NEW
internal/proc/proc.go            EDIT ExitCode (~409-419), Rusage (~460-470)
internal/proc/scope.go           EDIT ParseRusageTrailer (~124-140)
internal/usage/source.go         EDIT streamClosed (~96-120) + a legacy const beside exitTrailer (~84)
internal/relevo/gate.go          EDIT tailLines (~190-205)
internal/ledger/ledger.go        EDIT Load's loop (~115-120)
internal/doctor/doctor.go        no edit (RenameCheck rides WithExtraChecks)
cmd/relevo/doctor.go             EDIT append RenameCheck to extraChecks (~268-280)
cmd/relevo/main.go               EDIT run() (~287-297): the guard before captureAgyEnv
+ tests beside each EDIT
```

## 3. Data structures

**`internal/legacy`.** Constants, whose values are exact:

| const | value |
|---|---|
| `Name` | `"relay"` |
| `Binary` | `"relay"` |
| `ExitTrailer` | `"relay-exit:"` |
| `RusageTrailer` | `"relay-rusage:"` |
| `LedgerSource` | `"relay"` |
| `DBFile` | `"relay.db"` |
| `ClientUnit` | `"relay.service"` |
| `ServeUnit` | `"relay-serve.service"` |
| `LaunchdLabel` | `"com.github.fuad-daoud.relay"` |
| `Slice` | `"relay.slice"` |

Every constant has a doc comment naming its relevo counterpart and the reader that
still needs it. Round 3 uses the ones this round doesn't read.

```
type Roots struct {
    OldState, NewState   string // absolute: <XDG_STATE_HOME or ~/.local/state>/{relay,relevo}
    OldConfig, NewConfig string // absolute: <XDG_CONFIG_HOME or ~/.config>/{relay,relevo}
}

type Status struct {
    OldState, NewState, OldConfig, NewConfig bool // the directory exists
}
```

**`dist`:**

- `var ClientUnit string`, embedded from `relevo.service`;
- `var LaunchdPlist string`, embedded from `com.github.fuad-daoud.relevo.plist.in`.

The plist keeps its `@BIN@`/`@HOME@` placeholders.

## 4. Interfaces

`internal/legacy`:

- `func StateRoot(getenv func(string) string, home string) string`
  - Returns `$XDG_STATE_HOME/relay` if `getenv("XDG_STATE_HOME") != ""`, else
    `home/.local/state/relay`.
  - Mirrors `store.DefaultRoot` (`internal/store/store.go` ~90-101) with the old
    name.
- `func ConfigRoot(configHome string) string` returns `filepath.Join(configHome,
  "relay")`. `configHome` is what `userConfigRoot()` returns.
- `func Probe(r Roots) (Status, error)`
  - Stats the four paths. `ErrNotExist` → false.
  - Any other stat error → a returned error naming the path.
  - A path that exists but is not a directory → true. The root is taken; migrate
    decides what to do with it.
- `func (s Status) Unmigrated() bool` is `(s.OldState && !s.NewState) ||
  (s.OldConfig && !s.NewConfig)`.
- `func (s Status) Stale() bool` is `(s.OldState && s.NewState) || (s.OldConfig &&
  s.NewConfig)`: an old root beside its new one.

`internal/doctor`:

- `func RenameCheck(r legacy.Roots, s legacy.Status) Check`, with `Name: "rename"`
  and `Group: ""`. In order:
  1. **Unmigrated.**
     - `SevFail`.
     - Detail: `"relay-era state at <each old root that lacks its new one, comma-separated> has not been migrated"`.
     - Fix: `"relevo migrate --dry-run && relevo migrate"`.
  2. **Stale.**
     - `SevWarn`.
     - Detail: `"<old> exists beside <new>: an old relay binary or plugin recreated it"`, for the first stale pair (state before config).
     - Fix: `"ls -la <old>"`.
  3. **Neither.** `SevOK`, Detail `"no relay-era state"`, Fix `""`.

`cmd/relevo/rename.go`:

- `func renameRoots() (legacy.Roots, error)`
  - `NewState` from `store.DefaultRoot()`, `OldState` from
    `legacy.StateRoot(os.Getenv, home)`.
  - `NewConfig` is `filepath.Join(userConfigRoot(), "relevo")` and `OldConfig` is
    `legacy.ConfigRoot(userConfigRoot())`.
  - Compose through `userConfigRoot()` as CLAUDE.md requires.
- `func guardExempt(verb string) bool` is true for `help`, `-h`, `--help`,
  `version`, `-v`, `--version`, `doctor` and `migrate`. (`migrate` doesn't exist
  until round 3. Exempting it now is deliberate.)
- `func refuseUnmigrated() error`
  - Calls `renameRoots` and `legacy.Probe`.
  - If `Unmigrated()`, return an error whose text is exactly:
    `relevo: relay-era state at <old roots lacking their new root, comma-separated> is not migrated; run relevo migrate --dry-run, then relevo migrate`.
  - A probe error is returned wrapped.
  - Otherwise nil.

`cmd/relevo/main.go` `run()`: after the `len(args) == 0` check and **before**
`captureAgyEnv()`:

```
if !guardExempt(args[0]):
    if err := refuseUnmigrated(); err != nil: print it to stderr; return exitCodeErr{code: 1}
```

`captureAgyEnv` moves below the guard. It can write into the state root, which
is exactly what the guard prevents.

## 5. Pseudocode: the readers

- **`proc.(*Runner).ExitCode`** (`internal/proc/proc.go` ~409): take the last line.
  - Prefix `ExitTrailer` → parse after it.
  - Else prefix `legacy.ExitTrailer` → parse after that.
  - Else `(0, false)`.
  - `proc_other.go` is unchanged.
- **`proc.(*Runner).Rusage`** (~460): the backward scan matches a line with prefix
  `RusageTrailer` **or** `legacy.RusageTrailer`, then passes it to
  `ParseRusageTrailer`.
- **`ParseRusageTrailer`** (`internal/proc/scope.go` ~128): accept either prefix,
  and strip whichever matched. Behaviour for the new prefix is unchanged.
- **`usage.streamClosed`** (`internal/usage/source.go` ~119): closed when the last
  line starts with `exitTrailer` or `legacyExitTrailer`.
  - Add `const legacyExitTrailer = "relay-exit:"` beside `exitTrailer` (~84),
    copied rather than imported, following the file's own rule for `exitTrailer`.
  - Also add `func LegacyExitTrailerForTest() string`.
  - Extend the pin test at `internal/proc/proc_test.go` ~289 so the copy equals
    `legacy.ExitTrailer`.
- **`relevo.tailLines`** (`internal/relevo/gate.go` ~198): also skip lines with
  prefix `legacy.RusageTrailer`.
- **`ledger.Load`** (`internal/ledger/ledger.go` ~115, right after `json.Unmarshal(raw,
  &e)` succeeds):
  - `if e.Source == legacy.LedgerSource { e.Source = "relevo" }`, before the
    `knownKind`/`knownSource` test.
  - `knownSource` is unchanged, since round 1 made it `"relevo" || "planner"`.
  - Update the doc comment: pre-rename entries are read as relevo's own, so a
    `Save` rewrites them. Nothing else in the ledger changes.

## 6. Error handling

- A `Probe` error in the guard fails the verb with the wrapped error and exit 1.
  A root relevo cannot stat is not safe to run beside.
- `RenameCheck` never errors. `cmd/relevo/doctor.go` turns a `Probe` error into a
  `SevWarn` `rename` row with `ProbeFailed: true` and Detail `probe error: <err>`.
- Readers never error on legacy input. They either recognise it or behave as
  before.

## 8. Working efficiently

Every location is named above, so read each file once and batch the reads.

- **Focused loop:**
  `go test ./internal/legacy/ ./internal/proc/ ./internal/usage/ ./internal/ledger/ ./internal/doctor/ ./dist/`
  and `go test ./internal/relevo/ -run 'TailLines|Gate'` and
  `go test ./cmd/relevo/ -run 'Rename|Guard|Unmigrated'`.
- **Full check, once at the end:** `make check`.
- **CLI tests:** CI has no harness and no network. The cmd tests here only stat
  directories and must never execute a subcommand that spawns a harness.
  `TestMain` already points `HOME`/`XDG_*` at a temp root. A test that needs a
  relay-era root sets its own `t.TempDir()` as `XDG_STATE_HOME` or
  `XDG_CONFIG_HOME` (CLAUDE.md, #235).

## 9. Ordered implementation steps

**Step 1: `internal/legacy`.** Build the constants, `StateRoot`, `ConfigRoot`,
`Probe`, `Unmigrated` and `Stale`.

Tests:
- `StateRoot` with and without `XDG_STATE_HOME`;
- `Probe` over a temp tree covering all four absent, all present, a non-dir file,
  and an unreadable parent (skip that case as root);
- the `Unmigrated`/`Stale` truth table over all 16 `Status` values.

Done when `go test ./internal/legacy/` passes.

**Step 2: readers.** Apply the five edits in §5.

Tests (each named, each failing if its clause is removed):
- `TestExitCodeReadsLegacyTrailer`: a log ending `relay-exit:3` → `(3, true)`.
- `TestRusageReadsLegacyTrailer`, and a `ParseRusageTrailer` row with the legacy
  prefix.
- `TestStreamClosedLegacyTrailer` in `internal/usage`.
- A `tailLines` case in the existing table (`internal/relevo/gate_test.go` ~97):
  `"a\nb\n\nrelay-rusage:cpu_usec=1 mem_peak=2\n\nrelay-exit:2\n"` → `[a b
  relay-exit:2]`. The legacy exit line is gate output and stays in, exactly as a
  new `relevo-exit:` line does today.
- `TestLoadReadsLegacySource`: a file whose entry has `"source": "relay"` loads
  into `Entries` with `Source == "relevo"` and `Other` empty. Saving writes
  `"relevo"`.

**Mutation-test each one yourself** (remove the clause, see the named test fail,
restore) and list them in the report.

**Step 3: `dist`.** Create `dist/dist.go` (package `dist`, `//go:embed` of the two
files) and `dist/dist_test.go`:
- `ClientUnit` contains `ExecStart=%h/.local/bin/relevo daemon`;
- `LaunchdPlist` contains `@BIN@`.

Check that `.gitignore`'s `/dist/*.tar.gz` doesn't hide the new files, and that
`release.yml` building into `dist/relevo` doesn't collide with the package. Both
are just files in the directory, but say so in the report.

**Step 4: the doctor row.**
- `internal/doctor/rename.go`, with a table test over the three states plus the
  ordering rule (unmigrated wins over stale).
- In `cmd/relevo/doctor.go`, before `doctor.Run` (~287): build the row from
  `renameRoots()` and `legacy.Probe`, or the probe-error row from §6, and append
  it to `extraChecks`.

**Step 5: the guard.**
- Create `cmd/relevo/rename.go` and apply the `run()` edit.
- Tests in `cmd/relevo/rename_test.go`, as plain functions:
  - `guardExempt` table.
  - `refuseUnmigrated` with `XDG_STATE_HOME` set to a temp dir holding `relay/`
    and no `relevo/` → an error with the exact text.
  - The same with both present → nil.
  - Neither present → nil.
- Also run `go run ./cmd/relevo version` and `go run ./cmd/relevo status` with
  `XDG_STATE_HOME` pointing at a temp dir that holds only `relay/`, and paste both
  outputs. The first must print the version and the second must refuse.
  `status` spawns nothing, but use a temp `HOME` so no real state is touched.

**Step 6: full check and commit.**
- `make check` must pass.
- One commit:
  `feat(rename): relevo reads relay-era logs and ledger, refuses an unmigrated install, doctor rename row (#292)`.
- Don't push.

**Declared scope:** exactly the files in §2. Anything else, halt and report.

**Report:**
- per-step status;
- the mutation list;
- the two `go run` outputs;
- `git diff --stat HEAD~1`;
- the result of `make check`.
