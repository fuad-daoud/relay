# Agent install: relay writes its shipped definitions into the harness dirs

**Issue:** #174.
**Depends on:** nothing open. Builds on the embedded definitions
(`harness.AgentDoc`), the role table (`Harness.Roles`, `Role.Path`,
`Role.ExpectModel`) and doctor's definition rows (#166).
**Consumed by (later):** #167 (`relay init` may call this as a step);
#164 (fresh-install experience).
**Status:** draft; plan at `docs/plans/2026-09-17-agent-install.md`.

## 1. System overview

A fresh `herdr plugin install fuad-daoud/relay` lands the binary, the UI
overlay, the pickers and `install-service`, and no agent definition. The
definitions are embedded in the binary, but they reach a harness only when
the user runs, per kind and per role, `mkdir -p <dir> && relay agent print
--kind <k> --role <r> > <path>` -- twelve redirects across three kinds, and
step 4 of the README's first run. `relay doctor` prints that line as the fix
for a missing definition, but only for definitions some candidate would
load (#166): `architect` is never checked, so the planner's definition is
simply absent on a new machine and nothing says so.

relay owns the bytes (`AgentDoc`), knows the install path per kind
(`Role.Path`) and knows which kinds are on this machine (`Harness.Binary`
on `PATH`). This design has relay do the write:

- **`relay agent install`** -- a second mode of the existing `agent` verb,
  next to `print`. With no flags it writes every definition every
  `PATH`-resident kind ships, `architect` included, creating the
  directories. An existing file is kept unless it is identical or `--force`
  is given. One line per file says what happened.
- **Doctor points at it.** The missing-definition fix and the agy drift fix
  become `relay agent install` invocations.
- **The plugin runs it at install and update.** A `[[build]]` step after
  the version check, without `--force`, so a repinned `model:` survives an
  update and a definition change ships with the release that made it.
- **The README's step 4 collapses to one command.**

### Decisions taken on the issue's open questions

1. **`agent install` does not count against the #114 freeze.** `agent` is
   an existing verb; `install` is a second mode of it, as `print` is the
   first. The freeze is on verbs, and relay's verb list does not grow.
2. **One overwrite rule for every kind: keep unless `--force`.** The issue
   floated overwriting agy without `--force` because relay owns those bytes
   outright (`ExpectModel: inherit`). Rejected: the behaviour would diverge
   by kind, a user's agy edit would vanish silently, and doctor already
   warns on agy drift (#91) -- its fix now names `--force`, which is the
   explicit consent the overwrite needs.
3. **Plugin wiring is a `[[build]]` step, not an action.** An action needs a
   click on every new device and never refreshes on update; `install-service`
   couples definitions to the daemon unit. A build step runs on install and
   on every update, and with no `--force` it is safe to run repeatedly.
4. **Comparison is the trailing-whitespace-trimmed equality doctor already
   uses.** One rule, factored into `harness.DocEqual` and used by both.
5. **`--kind` overrides `PATH`.** Naming a kind is an instruction; the user
   may be installing for a harness they are about to install. Only the
   no-`--kind` path consults `PATH`.

### Scope boundary

One new file pair in `internal/harness` (`install.go`, `install_test.go`);
one new case in `cmd/relay/agent.go` with tests in `cmd/relay/agent_test.go`
(nothing here reaches herdr); two fix strings and one comparison in
`internal/doctor/doctor.go`; one `[[build]]` entry in each of the two
`herdr-plugin.toml` manifests; README edits in three places.

Out of scope (per #174): shell aliases or a planner launcher; making
`architect` a relay role; a doctor row for `architect`; `relay init`
(#167); a `--home` flag (tests override `$HOME`).

## 2. File structure

```
internal/harness/
  install.go            InstallEnv, InstallOptions, InstallOutcome, InstallResult,
                        DocEqual, Install, OSInstallEnv, (InstallResult).Line
  install_test.go       fake InstallEnv over maps; table tests for every outcome
cmd/relay/
  agent.go              case "install" -> cmdAgentInstall; usage strings
  agent_test.go         install tests under a temp $HOME (no herdr)
internal/doctor/
  doctor.go             roleCheck: two Fix strings; DocEqual replaces the local trim
  doctor_test.go        three expected Fix strings updated
herdr-plugin.toml       [[build]] step: ./relay agent install
from-source/herdr-plugin.toml
                        same step
README.md               first-run step 4; reviewer section; architect section
```

## 3. Data structures

### 3.1 `InstallOptions`

| field | type | meaning |
| --- | --- | --- |
| `Kind` | `string` | `""`: every known kind whose `Binary` resolves on `PATH`. Otherwise exactly this kind, `PATH` not consulted. Must be a `harness.Lookup` hit. |
| `Role` | `string` | `""`: every role the selected kind ships (`Harness.Roles`, table order). Otherwise exactly this role. Must be shipped by at least one known kind (any kind when `Kind` is `""`; that kind otherwise). |
| `Force` | `bool` | overwrite a differing file. |
| `DryRun` | `bool` | compute outcomes, touch nothing. |

### 3.2 `InstallOutcome`

A string enum. The value is what the CLI prints as the first column.

| value | when |
| --- | --- |
| `wrote` | file was absent; written (directory created as needed). |
| `overwrote` | file differed and `Force` was set; written. |
| `kept (identical)` | file present and `DocEqual` to the shipped bytes. Never rewritten, even with `Force`. |
| `kept (differs; --force to overwrite)` | file present, differs, `Force` unset. |
| `would write` | `DryRun` and the file is absent. |
| `would overwrite` | `DryRun`, file differs, `Force` set. |
| `error` | `MkdirAll` or `WriteFile` failed; `Err` holds why. |

Under `DryRun` the two `kept` outcomes are reported exactly as they would be
without it.

### 3.3 `InstallResult`

| field | type | meaning |
| --- | --- | --- |
| `Kind` | `string` | harness kind. |
| `Role` | `string` | role name. |
| `Path` | `string` | `Role.Path`, home-relative, no leading `~/`. |
| `Outcome` | `InstallOutcome` | §3.2. |
| `Err` | `string` | `""` unless `Outcome == error`; then the failing call's error text. |

`(r InstallResult) Line() string` renders one output line:
`<outcome>  ~/<path>` for every outcome but `error`, and
`error  ~/<path>: <err>` for that one. No padding: outcomes vary in length
and the line is read, not aligned.

### 3.4 `InstallEnv`

The filesystem and `PATH` seams, so `Install` runs against maps in tests.

| method | contract |
| --- | --- |
| `LookPath(binary string) (string, error)` | `exec.LookPath` semantics; any error means "not on PATH". |
| `HomePath(rel string) (string, error)` | joins `rel` under the user's home. An error aborts the whole install (nothing can be written). |
| `ReadFile(path string) ([]byte, error)` | returns an error satisfying `errors.Is(err, fs.ErrNotExist)` when absent. Any other error is treated as "present, differs" -- relay never overwrites what it cannot read without `Force`. |
| `MkdirAll(dir string) error` | create `dir` and parents, 0755. |
| `WriteFile(path string, data []byte) error` | write whole file, 0644, truncating. |

`OSInstallEnv()` returns the `os`/`exec`-backed implementation.
`os.UserHomeDir` honours `$HOME`, which is how tests redirect it.

## 4. Interfaces

### 4.1 `harness.Install`

```
func Install(env InstallEnv, opts InstallOptions) ([]InstallResult, error)
```

Single responsibility: decide, per (kind, role), whether the shipped
definition lands on disk, and land it.

- Returns a non-nil error, and no results, only for a selection failure:
  `ErrUnknownKind` (wrapped, naming the kind) when `opts.Kind` is not a
  known kind; `ErrUnknownRole` (wrapped, naming the role and the roles that
  exist) when `opts.Role` is shipped by no selected kind; or the
  `HomePath` error.
- Otherwise returns one result per (kind, role) selected, kinds in
  `harness.All()` order, roles in `Harness.Roles` order, and a nil error.
  Per-file failures are `error` outcomes, never a returned error: one bad
  directory must not stop the other kinds.
- An empty slice with a nil error means no kind was selected (no `--kind`
  and no harness binary on `PATH`).
- Postcondition without `DryRun`: every result with outcome `wrote` or
  `overwrote` has the shipped bytes on disk at `HomePath(Path)`, byte for
  byte (untrimmed). With `DryRun`: `MkdirAll` and `WriteFile` are never
  called.

### 4.2 `harness.DocEqual`

```
func DocEqual(shipped, installed []byte) bool
```

True when the two are equal after trimming trailing `" \t\r\n"` from each.
This is the rule `doctor.roleCheck` applies today with a local closure;
doctor switches to this function so the two never drift.

### 4.3 `cmd/relay`: `relay agent install`

```
relay agent install [--kind <agy|claude|opencode>] [--role <name>] [--force] [--dry-run]
```

- Parses flags with a `flag.FlagSet` named `relay agent install`,
  `ContinueOnError`, output to stderr, as `cmdAgentPrint` does.
- Calls `harness.Install(harness.OSInstallEnv(), opts)`.
- Selection error -> the message on stderr, exit 2 (matches `print`'s
  unknown-kind and unknown-role exits).
- Empty results -> stdout `no harness binaries on PATH (agy, claude,
  opencode); nothing to install`, exit 0. The plugin build step must never
  fail on a machine that has no harness yet.
- Otherwise one `Line()` per result on stdout, in order. Exit 1 if any
  outcome is `error`, else 0.
- The `agent` usage line becomes
  `usage: relay agent <print|install> ...` and lists both forms.

### 4.4 Doctor

`roleCheck` (`internal/doctor/doctor.go`) changes in three places and
nowhere else:

| row | today | after |
| --- | --- | --- |
| missing | `mkdir -p <dir> && relay agent print --kind k --role r > <path>` | `relay agent install --kind k --role r` |
| agy differs from shipped | `relay agent print --kind k --role r > <path>` | `relay agent install --kind k --role r --force` |
| comparison | local `trim` closure | `harness.DocEqual(shipped, raw)` |

The `set model: inherit in <path>` fix on the pin row is unchanged: it is
the narrower instruction and it precedes the drift check.

### 4.5 Plugin manifests

Both `herdr-plugin.toml` and `from-source/herdr-plugin.toml` gain, after the
`["./relay", "version"]` build step:

```toml
[[build]]
command = ["./relay", "agent", "install"]
```

No `--force`. Its stdout is the per-file report, which herdr shows in the
plugin's build log.

## 5. High-level pseudocode

### 5.1 `Install`

```
Install(env, opts):
  kinds := []
  if opts.Kind != "":
    h, ok := Lookup(opts.Kind); if !ok: return nil, wrap(ErrUnknownKind, opts.Kind)
    kinds = [h]
  else:
    for h in All(): if env.LookPath(h.Binary) succeeds: kinds += h

  if opts.Role != "":
    if no h in kinds (or, when kinds is empty and opts.Kind == "", no h in All()) has Role(opts.Role):
      return nil, wrap(ErrUnknownRole, opts.Role, names of roles those kinds ship)

  results := []
  for h in kinds:
    for r in h.Roles:
      if opts.Role != "" and r.Name != opts.Role: continue
      results += installOne(env, opts, h.Kind, r)   -- may return (result, homeErr)
      on homeErr: return nil, homeErr
  return results, nil

installOne(env, opts, kind, r):
  shipped := AgentDoc(r.Name, kind)          -- cannot fail for a table row; treat failure as error outcome
  full, err := env.HomePath(r.Path); on err: return _, err
  res := {Kind: kind, Role: r.Name, Path: r.Path}
  existing, rerr := env.ReadFile(full)
  switch:
    rerr is ErrNotExist:
      res.Outcome = DryRun ? "would write" : write(env, full, shipped, res, "wrote")
    rerr == nil and DocEqual(shipped, existing):
      res.Outcome = "kept (identical)"
    otherwise (differs, or unreadable):
      if !opts.Force: res.Outcome = "kept (differs; --force to overwrite)"
      else: res.Outcome = DryRun ? "would overwrite" : write(env, full, shipped, res, "overwrote")
  return res, nil

write(env, full, data, res, success):
  if err := env.MkdirAll(dir(full)); err != nil: res.Err = err.Error(); return "error"
  if err := env.WriteFile(full, data); err != nil: res.Err = err.Error(); return "error"
  return success
```

### 5.2 `cmdAgentInstall`

```
parse flags -> opts
results, err := Install(OSInstallEnv(), opts)
if err: stderr "relay: <err>"; exit 2
if len(results) == 0: stdout "no harness binaries on PATH (agy, claude, opencode); nothing to install"; exit 0
failed := false
for r in results: stdout r.Line(); if r.Outcome == error: failed = true
exit failed ? 1 : 0
```

## 6. Error handling

| category | example | recoverable | surfaces as |
| --- | --- | --- | --- |
| selection | unknown `--kind`, unknown `--role` | no | `Install` error; CLI exit 2 |
| environment | home dir unresolvable | no | `Install` error; CLI exit 2 |
| per-file | `MkdirAll`/`WriteFile` failure (permissions, read-only home) | yes -- other files proceed | `error` outcome with `Err`; CLI exit 1 after printing every line |
| unreadable existing file | `ReadFile` error that is not `ErrNotExist` | yes | treated as "differs": kept without `Force`, overwritten with it |
| nothing selected | no harness on `PATH` | yes | empty results; CLI prints one line, exit 0 |

No logging: this is a one-shot CLI whose stdout is the record. No hook
events: nothing about a binding changes.

## 7. Verification

All behaviour tests are pure, in `internal/harness/install_test.go`, over a
fake `InstallEnv` built on maps (`lookPaths map[string]string`, `files
map[string][]byte`, `home string`, `mkdirErr`, `writeErr`, plus a `writes`
log). Cases:

1. Fresh home, no `--kind`, two of three binaries on `PATH`: every role of
   those two kinds is `wrote`; the third kind is absent from results;
   directories were created; each written file equals `AgentDoc` untrimmed.
2. Identical file present: `kept (identical)`; no write, even with `Force`.
3. Differing file present: `kept (differs; --force to overwrite)` without
   `Force`; `overwrote` with it, and the file now equals the shipped bytes.
4. `DryRun`: `would write` / `would overwrite` / the two `kept` values;
   zero calls to `MkdirAll` and `WriteFile`.
5. `Kind: "claude"` with an empty `lookPaths`: still installs (PATH not
   consulted).
6. No `--kind`, empty `lookPaths`: empty results, nil error.
7. Unknown kind -> `errors.Is(err, ErrUnknownKind)`; unknown role ->
   `errors.Is(err, ErrUnknownRole)` and the message lists the shipped roles.
8. `WriteFile` failing for one path: that result is `error` with `Err` set;
   the other results are unaffected; `Install` returns nil error.
9. `DocEqual`: equal after trailing-whitespace trim is true; a leading
   difference is false; a differing `model:` line is false.
10. `Line()`: the exact strings for `wrote`, `kept (differs; --force to
    overwrite)` and `error` with an `Err`.

`cmd/relay/agent_test.go`, under `t.Setenv("HOME", t.TempDir())` and never
without `--kind` (the machine's `PATH` is not the test's business):
`--kind claude --dry-run` prints four `would write` lines and leaves the
temp home empty; `--kind agy` writes four files byte-identical to
`AgentDoc` and a second run prints four `kept (identical)`; unknown kind
exits 2. None of these reach herdr.

Doctor tests pin the two new fix strings. Mutation targets, each named in
the plan: drop the trim inside `DocEqual` (case 9 fails); drop the `Force`
branch (case 3 fails); consult `PATH` under `--kind` (case 5 fails).
