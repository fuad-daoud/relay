# `relay serve` admin verbs: find the daemon's state, refuse an uninitialised root, and see live headless builders

Two defects seen on the contabo serve box today, both in the admin verbs
(`relay serve status|clients|gc|unbind|fingerprint`):

1. The daemon runs with `--state /srv/data/relay-serve`; a bare
   `relay serve status` resolved `~/.local/state/relay/serve`, printed
   `no owners` for an empty, never-initialised directory, and `serve.New`
   created `serve/tmp` there as a side effect.
2. `cmdServeStatus` builds `serve.Config{Root, Now}` with no `Runner`, so
   `headlessStatus` (`internal/relay/headless.go:540`) returns `unknown` for
   every headless builder, alive or not (`builder headless claude unknown pid 66888`).

If any step below is impossible as written or contradicts the code, **halt
and report** -- do not improvise a different design.

**Scope guard.** Another round owns `cmd/relay/main.go`, `cmd/relay/db.go`,
`internal/store/*`, `internal/history/*`, `internal/relay/herdr.go`,
`internal/db/*`, `README.md`. Do not edit any of them. Everything here is in
`cmd/relay/serve.go`, `cmd/relay/serve_test.go`, `internal/serve/pointer.go`
(new), `internal/serve/pointer_test.go` (new), `internal/serve/admin_test.go`.

## 1. System overview

The daemon (`cmdServeRun`, `cmd/relay/serve.go`) records where its state is
by writing a small **pointer file** `daemon.json` into the *default* serve
root (`<store.DefaultRoot()>/serve`) at startup and removing it on exit. An
admin verb run without `--state` reads that pointer; if it names a different
root and the recorded pid is alive, the verb uses that root and says so on
stderr. In every case the verb then checks the root is **initialised**
(has `clients.json`, `server.key`, or a `bindings/` directory) *before*
constructing a `serve.Server`, and refuses with an actionable message
otherwise -- so an admin verb never creates directories in a root nobody
initialised. `init` keeps today's resolution: it is the verb that creates a
root.

Separately, the admin verbs get the same process runner the daemon uses, so
a headless builder on the box reads `working` / `exited N` instead of
`unknown`. `AdminUnbind` already judges "round running" from the log
(`relay.RoundStateOf`), not from the runner, so this is display only.

## 2. File structure

```
internal/serve/pointer.go        DaemonPointer; WritePointer/ReadPointer/RemovePointer; Initialised; ResolveAdminRoot
internal/serve/pointer_test.go   table tests for all of the above
internal/serve/admin_test.go     + TestAdminStatusReportsHeadlessLivenessThroughRunner
cmd/relay/serve.go               defaultServeRoot; adminRoot; serveAdminConfig; daemon writes/removes the pointer; five verbs switch to adminRoot + serveAdminConfig
cmd/relay/serve_test.go          + TestServeAdminConfigHasRunnerAndClock, TestServeStatusRefusesUninitialisedRoot
docs/plans/2026-09-20-serve-admin-root.md   copy of this plan (step 0)
```

## 3. Data structures

### `DaemonPointer` (`internal/serve/pointer.go`)

| field | type | JSON | meaning |
|---|---|---|---|
| `Root` | `string` | `root` | absolute serve root the daemon is using (`<state>/serve`); required |
| `PID` | `int` | `pid` | the daemon's pid; required, > 0 |
| `Listen` | `string` | `listen` | the `--listen` value, display only |
| `StartedAt` | `time.Time` | `started_at` | RFC3339, display only |

File: `<defaultRoot>/daemon.json`, mode 0644, written atomically
(`.tmp` + rename). Constant `PointerFileName = "daemon.json"`.

### Initialised markers

A root is initialised when **any** of these exists: `clients.json` (file),
`server.key` (file; `serve init` writes it, `--insecure-http` boxes never
have it), `bindings` (directory). Constant slice `initialisedMarkers`.

## 4. Interface definitions

All in package `serve`, `internal/serve/pointer.go`:

- `func WritePointer(defaultRoot string, p DaemonPointer) error` --
  `MkdirAll(defaultRoot, 0o755)`, marshal indented, write `daemon.json.tmp`,
  rename. Errors: any I/O error, wrapped `write daemon pointer: %w`.
- `func ReadPointer(defaultRoot string) (p DaemonPointer, ok bool, err error)` --
  `ok == false, err == nil` when the file is missing; `err` for unreadable or
  malformed JSON (wrapped `read daemon pointer: %w`).
- `func RemovePointer(defaultRoot string) error` -- missing file is `nil`.
- `func Initialised(root string) (bool, error)` -- true when any marker
  exists; a missing root directory is `false, nil`; other stat errors
  propagate.
- `func ResolveAdminRoot(explicitState, defaultRoot string, alive func(pid int) bool) (root string, note string, err error)`
  Pure given `alive`. Rules, in order:
  1. `explicitState != ""` -> `filepath.Join(explicitState, "serve")`, note `""`.
  2. `ReadPointer(defaultRoot)`: error -> return it. Missing -> `defaultRoot`, note `""`.
  3. Pointer present, `alive(p.PID)` true, `p.Root != defaultRoot` ->
     `p.Root`, note `using the running daemon's state: <p.Root> (pid N)`.
  4. Pointer present, `alive` true, `p.Root == defaultRoot` -> `defaultRoot`, note `""`.
  5. Pointer present, `alive` false -> `defaultRoot`, note
     `stale daemon pointer: <p.Root> (pid N is not running)`.
  `ResolveAdminRoot` does **not** check `Initialised`; the caller does, so
  `init` can share nothing with it and the refusal text lives in one place.

In `cmd/relay/serve.go`:

- `func defaultServeRoot() (string, error)` -- `store.DefaultRoot()` + `/serve`.
  `serveRoot(fs)` keeps its current body but calls this for its default branch.
- `func pidAlive(pid int) bool` -- `os.FindProcess(pid)` then
  `p.Signal(syscall.Signal(0)) == nil`. On Windows this is always false
  (a comment says so: the pointer is then ignored and the default root is
  used). `cmd/relay` already imports `syscall` in `main.go`; this file may
  import it too. Must compile for `windows/amd64` (CI cross-compiles it).
- `func adminRoot(fs *flag.FlagSet) (string, error)` -- reads the `state`
  flag, calls `defaultServeRoot`, `serve.ResolveAdminRoot(state, def, pidAlive)`;
  prints a non-empty note to **stderr** as `relay serve: <note>`; then
  `serve.Initialised(root)`; when false returns
  `fmt.Errorf("no serve state at %s: run relay serve init, or pass --state <dir> matching the daemon's", root)`.
  Used by `status`, `clients`, `gc`, `unbind`, `fingerprint`. **Not** by
  `init` and **not** by the daemon (`cmdServeRun`), which keep `serveRoot`.
- `func serveAdminConfig(root string) serve.Config` -- pure constructor:
  `{Root: root, Runner: proc.New(), Now: time.Now}`. Used by `status`,
  `gc`, `unbind` in place of their inline `serve.Config{Root, Now}` literals
  (grep for every `serve.Config{` in `serve.go` outside `cmdServeRun`).
- Daemon: in `cmdServeRun`, right after `serve.New(cfg)` succeeds and before
  `signal.NotifyContext`: compute `def, _ := defaultServeRoot()`; call
  `serve.WritePointer(def, serve.DaemonPointer{Root: root, PID: os.Getpid(), Listen: sf.listen, StartedAt: time.Now()})`;
  on error `slog.Warn("daemon pointer not written", "err", err)` and carry on
  (the pointer is a convenience, never a gate); `defer serve.RemovePointer(def)`
  (ignore its error). `root` here is already `<state>/serve` from `serveRoot`,
  and must be absolute: apply `filepath.Abs` to it before writing.

## 5. Pseudocode

```
adminRoot(fs):
    state <- fs "state" value
    def <- defaultServeRoot()
    root, note <- ResolveAdminRoot(state, def, pidAlive)   ; error -> return
    if note != "": stderr "relay serve: " + note
    if !Initialised(root): return error "no serve state at <root>: ..."
    return root

cmdServeStatus / gc / unbind:
    root <- adminRoot(fs)                                  ; error -> return (exit 1)
    srv <- serve.New(serveAdminConfig(root))
    (rest unchanged)

cmdServeClients / cmdServeFingerprint:
    root <- adminRoot(fs)                                  ; instead of serveRoot
    (rest unchanged)

cmdServeRun:
    root <- serveRoot(fs); abs
    ... serve.New(cfg) ...
    WritePointer(def, {root, pid, listen, now})  ; warn on error
    defer RemovePointer(def)
    (rest unchanged)
```

## 6. Error handling

- Uninitialised root: non-recoverable for the verb, exit 1 through the
  returned error, message names the root and both remedies.
- Malformed pointer: non-recoverable for the verb (returned), so a corrupt
  file is noticed rather than silently ignored; the message names the file.
- Stale pointer: recoverable -- note on stderr, default root used.
- Pointer write failure in the daemon: recoverable -- `slog.Warn`, daemon runs.

## 7. Ordered implementation steps

Run from the worktree root. Do not commit until step 7.

### Step 0 -- keep the plan

Copy the plan file you were given (the round's `NNN-plan.md`) to
`docs/plans/2026-09-20-serve-admin-root.md`. It is part of the commit.

### Step 1 -- Part A test first: runner reaches AdminStatus (`internal/serve/admin_test.go`)

Add a package-local `aliveRunner` type implementing `relay.Runner` (see
`internal/relay/runner.go:38`): `Start` returns `relay.ProcHandle{}, nil`;
`Alive` returns `true, nil`; `ExitCode` returns `0, false`; `Kill` returns
`nil`. Add `TestAdminStatusReportsHeadlessLivenessThroughRunner`, modelled
on `TestAdminStatusAllOwners` (same client/binding fixture pattern): one
owner, one binding whose `Builder` endpoint is headless with `PID: 4242`
(look at how existing serve tests or `internal/relay/headless.go` build a
headless `store.Endpoint` -- `Mode`, `PID`, `LogPath` -- and follow that).
With `Config{Root, Now, Runner: aliveRunner{}}`, `AdminStatus` reports that
binding's `BuilderStatus == "working"`; with `Runner` nil it reports
`"unknown"`. Both assertions in the one test.

**Verify:** the test fails on the `Runner: aliveRunner{}` half only if the
plumbing is broken -- it should already pass, because `runtimeAt` copies
`s.cfg.Runner`. That is fine: this test pins the plumbing for Part A's
cmd-level change. If it fails, halt and report.

### Step 2 -- Part A: `serveAdminConfig` (`cmd/relay/serve.go`, `serve_test.go`)

Test first in `cmd/relay/serve_test.go`, next to `TestServeTierRuntimeHasClock`:
`TestServeAdminConfigHasRunnerAndClock` -- `serveAdminConfig("/x")` has
`Root == "/x"`, `Runner != nil`, `Now != nil`. Pure; reaches neither herdr
nor the filesystem (CI has no herdr).

Then add `serveAdminConfig` and use it in `status`, `gc`, `unbind`.

**Verify:** `go test ./cmd/relay/ -run TestServeAdminConfig` green; `go build ./...`.

### Step 3 -- Part B tests first (`internal/serve/pointer_test.go`)

- `TestPointerRoundTrip`: write, read (`ok`, fields equal, `StartedAt`
  round-trips to the second), remove, read again (`ok == false`, `err == nil`).
  Also: `ReadPointer` on a directory with a malformed `daemon.json` returns a
  non-nil error.
- `TestInitialised`: table over `clients.json` file / `server.key` file /
  `bindings` dir / empty dir / missing dir -> true, true, true, false, false.
- `TestResolveAdminRoot`: table over the five rules in §4 with
  `alive := func(int) bool { return true }` and `false` variants; assert
  `root` and `note` exactly (note strings as written in §4, with the
  concrete root and pid substituted).

**Verify:** `go test ./internal/serve/ -run 'TestPointer|TestInitialised|TestResolveAdminRoot'`
fails to compile.

### Step 4 -- Part B: `internal/serve/pointer.go`

Implement §3/§4. Doc comment on the file: why the pointer lives in the
default root (an admin's shell has no way to know the unit's `--state`).

**Verify:** the step 3 tests pass; `go vet ./internal/serve/`.

### Step 5 -- Part B: cmd wiring (`cmd/relay/serve.go`, `serve_test.go`)

Test first: `TestServeStatusRefusesUninitialisedRoot` -- `dir := t.TempDir()`;
`err := cmdServeStatus([]string{"--state", dir})`; `err != nil` and
`err.Error()` contains `"no serve state at "` and `filepath.Join(dir, "serve")`;
and `filepath.Join(dir, "serve", "tmp")` does **not** exist afterwards (this
is the side-effect regression). This subcommand reaches only the
filesystem, never herdr, so it is safe in CI. Note `cmd/relay` tests may
read the real `~/.config/relay/candidates.json` (#235); `cmdServeStatus`
does not load candidates, so no fixture is needed -- if it turns out to,
halt and report.

Then: `defaultServeRoot`, `pidAlive`, `adminRoot`; switch the five verbs;
add the daemon's write/remove.

**Verify:** `go test ./cmd/relay/` green; `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...` succeeds.

### Step 6 -- mutation checks (re-edit, never `git checkout`)

1. In `adminRoot`, skip the `Initialised` check: `TestServeStatusRefusesUninitialisedRoot` fails. Restore.
2. In `ResolveAdminRoot`, drop the `alive` test so a stale pointer is followed: the stale row of `TestResolveAdminRoot` fails. Restore.
3. In `serveAdminConfig`, drop `Runner`: `TestServeAdminConfigHasRunnerAndClock` fails. Restore.

### Step 7 -- full check and commit

```
make check
```

`git diff --stat` must list only the six files in §2 (five code/test files
plus the plan copy). Anything else: halt and report.

Squash to **one** commit:

```
feat(serve): admin verbs follow the daemon's state via a pointer file, refuse an uninitialised root, and read headless liveness

relay serve status on the contabo box said "no owners" because the daemon
runs with --state and the verb defaulted to XDG; serve.New also created
serve/tmp in that empty root. The daemon now writes <default>/serve/daemon.json
at startup; admin verbs without --state follow it when its pid is alive, and
every admin verb refuses a root with no clients.json, server.key or bindings/
before building a Server. The admin config also carries proc.New(), so a
headless builder reads working/exited instead of unknown.
```

## Report

Per-step state, `make check` tail, `git diff --stat`, the three mutation
lines, and the exact stderr note text you saw from a manual
`relay serve status` run against a temp `--state` with and without a
hand-written `daemon.json` in a temp `XDG_STATE_HOME`.
