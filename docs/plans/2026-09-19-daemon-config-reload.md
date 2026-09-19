# Daemon reloads candidates.json and policy.json on change (#209)

Closes #209. Design in this file; no separate spec.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`
(the planner runs it: this touches the daemon tick). Do not add a CLI verb
or flag. Do not add a module dependency. Do not touch `internal/serve` (its
static config is a separate issue). Run every command in the foreground;
dispatch no sub-agents. Do not widen any exported signature §4 does not
name.

**Commits.** Squash to ONE commit before the gate, subject starting
`fix(daemon):`.

**Commit the plan with the work.** Copy this plan file to
`docs/plans/2026-09-19-daemon-config-reload.md` in your worktree and
include it.

**Before step 1**: `git status --short` and `git branch --show-current`; you
must be on `relay/<binding-name>` under `~/.local/state/relay/.worktrees/`.
Otherwise halt.

## 1. System overview

`relay daemon` calls `newRuntime()` once at start (`cmd/relay/main.go`
`cmdDaemon`), which loads `candidates.json` and `policy.json` into
`Runtime.Candidates` and `Runtime.Policy` and, since #211, resolves
`Runtime.Classify` from the policy's `classify` block. Every mid-round
switch, switch limit, scan pattern and classifier decision the daemon makes
then uses that snapshot until restart, while every CLI verb loads the files
fresh -- so `relay policy` says one thing and the switch note says another.

This plan gives the daemon a **config watcher**: at the top of every tick it
`stat`s both files; when either's mtime or size changed since the last
successful load, it reloads both, re-resolves the classifier, and replaces
the three fields on the Runtime the tick uses. An unchanged file costs one
`stat`. A file that fails to load keeps the last good values and logs one
warning per distinct error text, not one per tick, so a half-saved edit does
not flip `resolveCandidate` into `ErrAmbiguousCandidate` mid-round and does
not flood the journal.

The rule is a pure struct with injected `stat`/`load` seams so the whole
behaviour is tested without files or herdr; `cmdDaemon` wires the real ones.
`Runtime` stays a value type: the watcher returns a modified copy and the
daemon stores it.

## 2. File structure

```
internal/relay/
  reload.go          NEW  ConfigPaths, ConfigWatcher, NewConfigWatcher, Refresh; fileStamp
  reload_test.go     NEW  table over Refresh with fake seams
  daemon.go          EDIT Daemon.refresh field; WithRefresh; Tick calls it first
  daemon_test.go     EDIT TestTickRefreshesRuntimeBeforeReconcile (fake store + fake herdr already exist in this file or fake_test.go -- use them)
cmd/relay/
  main.go            EDIT cmdDaemon wires NewConfigWatcher with the paths newRuntime already composes
docs/plans/2026-09-19-daemon-config-reload.md   NEW  this file
```

## 3. Data structures & type definitions

```go
// ConfigPaths names the files the daemon reloads and what Classify
// resolution needs. Composed by cmd/relay from userConfigRoot(); nothing in
// internal/relay builds a config path itself (CLAUDE.md).
type ConfigPaths struct {
    Candidates string              // .../relay/candidates.json
    Policy     string              // .../relay/policy.json
    ConfigDir  string              // the dir classify.Resolve takes
    Getenv     func(string) string // os.Getenv in production
}

// fileStamp is what changes when a file is edited: mtime and size. A
// missing file has a zero stamp and exists == false.
type fileStamp struct {
    mtime  time.Time
    size   int64
    exists bool
}

// ConfigWatcher reloads candidates.json and policy.json when they change.
type ConfigWatcher struct {
    paths ConfigPaths
    cand, pol fileStamp   // stamps at the last SUCCESSFUL load
    lastErr string        // last warning logged; "" after a good load
    loaded bool           // false until the first successful load through Refresh

    // seams; production values set by NewConfigWatcher, tests replace them
    stat           func(path string) (fileStamp, error)
    loadCandidates func(path string) (*candidate.Set, error)
    loadPolicy     func(path string) (policy.Policy, error)
    resolve        func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier
    warn           func(msg string, args ...any)   // slog.Warn
}
```

## 4. Interface definitions & component contracts

```go
// NewConfigWatcher returns a watcher with production seams: os.Stat,
// candidate.Load, policy.Load, classify.Resolve (first return only),
// slog.Warn. It does not read anything yet.
func NewConfigWatcher(paths ConfigPaths) *ConfigWatcher

// Refresh returns rt with Candidates, Policy and Classify replaced from disk
// when either file's stamp differs from the stamp at the last successful
// load, or when no load has happened yet through this watcher. Unchanged
// stamps -> rt returned as given (no load). Both files are always loaded
// together: policy validation is independent of candidates, but a switch
// that sees a new order with old candidates is the bug this fixes.
//
// Failure: a stat error other than not-exist, or a load error, keeps rt's
// current values, does NOT update the stamps (so the next tick retries),
// and calls warn once per distinct error string: "config reload: <err>
// (keeping the copy loaded at HH:MM:SS)" -- the time is rt.Now() of the
// last good load, or "startup" when this watcher never loaded. A missing
// file is not an error: candidate.Load / policy.Load already treat absence
// as empty, and the stamp records exists == false.
//
// Postconditions on success: stamps updated, lastErr = "", loaded = true.
func (w *ConfigWatcher) Refresh(rt Runtime) Runtime

// WithRefresh installs a per-tick refresh on the daemon. nil (the default)
// keeps today's behaviour: the Runtime given to NewDaemon is used as-is.
// Returns d for chaining.
func (d *Daemon) WithRefresh(f func(Runtime) Runtime) *Daemon
```

`Tick`: first statement becomes `if d.refresh != nil { d.rt = d.refresh(d.rt) }`,
before `Store.List()`, so a tick with zero bindings still keeps the daemon
current (two stats every two seconds is nothing, and the first send after an
edit then resolves correctly).

`cmdDaemon`:
```go
watcher := relay.NewConfigWatcher(relay.ConfigPaths{
    Candidates: filepath.Join(configDir, "relay", "candidates.json"),
    Policy:     filepath.Join(configDir, "relay", "policy.json"),
    ConfigDir:  configDir,
    Getenv:     os.Getenv,
})
return relay.NewDaemon(rt, *interval).WithRefresh(watcher.Refresh).Run(ctx)
```
`configDir` comes from the same `userConfigRoot()` call `newRuntime` uses;
if `cmdDaemon` does not have it in scope, call `userConfigRoot()` once
there. Paths must be composed exactly as `newRuntime` composes them (read
`newRuntime`; do not guess).

## 5. High-level pseudocode

```
Refresh(rt):
  cs, errC = stat(paths.Candidates); ps, errP = stat(paths.Policy)
  if errC != nil or errP != nil: return fail(rt, first non-nil err)
  if w.loaded and cs == w.cand and ps == w.pol: return rt
  cands, err = loadCandidates(paths.Candidates); if err: return fail(rt, err)
  pol, err = loadPolicy(paths.Policy);           if err: return fail(rt, err)
  rt.Candidates = cands; rt.Policy = pol
  rt.Classify = resolve(pol.Classify, paths.ConfigDir, paths.Getenv)
  w.cand, w.pol = cs, ps; w.loaded = true; w.lastErr = ""; w.loadedAt = rt.Now()
  return rt

fail(rt, err):
  msg = err.Error()
  if msg != w.lastErr:
    when = "startup"; if w.loaded: when = w.loadedAt.Local().Format("15:04:05")
    warn("config reload failed; keeping the copy loaded at " + when, "err", err)
    w.lastErr = msg
  return rt

stat seam (production): os.Stat -> fileStamp{mtime: ModTime(), size: Size(), exists: true};
  errors.Is(err, os.ErrNotExist) -> fileStamp{}, nil; other err -> err
```

Note on the first tick: `newRuntime` already loaded both files, so the first
`Refresh` reloads them once more (stamps unknown). That is one redundant
parse at startup and keeps the watcher's stamps honest; do not try to seed
the stamps from `newRuntime`.

## 6. Error handling strategy

No new error types. Load failures never propagate out of `Tick`: the tick
proceeds on the last good config. A persistent failure logs exactly once
until the error text changes or a load succeeds. `Refresh` never panics on
a nil `Getenv` (pass through to `classify.Resolve`, which tolerates nil).

## 7. Documentation

None beyond code comments: the README describes the files, not the
daemon's caching, and the deferred item in
`docs/specs/2026-09-11-policy-order-design.md` is resolved by referencing
this plan in the commit message, not by editing a historical spec.

## 8. Tests (`internal/relay/reload_test.go`, all with fake seams)

- `TestRefreshLoadsOnFirstCall`: fresh watcher, stamps present -> load called once for each file, rt fields replaced, `Classify` from the resolve seam.
- `TestRefreshSkipsWhenUnchanged`: second call with identical stamps -> no load calls, rt returned unchanged (pointer-equal `Candidates`).
- `TestRefreshReloadsOnPolicyChange`: only policy stamp changes -> BOTH files reloaded; new `Policy.Order` visible; `Classify` re-resolved (resolve seam call count 2).
- `TestRefreshReloadsOnCandidatesChange`: only candidates stamp changes -> both reloaded.
- `TestRefreshKeepsLastGoodOnBadPolicy`: after a good load, policy load seam returns `ErrBadPolicy` -> rt unchanged (same `Policy`, same `Candidates`, same `Classify`), stamps NOT advanced (a third call with the same bad stamp tries the load again), warn called once with text containing `keeping the copy loaded at`.
- `TestRefreshWarnsOncePerDistinctError`: same error three ticks -> one warn; error text changes -> second warn; a good load then the same error again -> a third warn.
- `TestRefreshMissingFileIsEmptyNotError`: stat seam returns `exists false` for policy -> load seam still called with the path (production `policy.Load` returns zero Policy for a missing file) and no warn.
- `TestRefreshStartupFailureSaysStartup`: first call fails -> warn text contains `startup`.

`internal/relay/daemon_test.go`:
- `TestTickRefreshesRuntimeBeforeReconcile`: `NewDaemon(rt, interval).WithRefresh(f)` where `f` swaps `Policy.Order["builder"]` for a marker and records the call; one binding present; after `Tick`, `f` was called exactly once and the `Reconcile` path observed the swapped policy -- easiest observable: `f` also sets `rt.Now` to a fixed time and the log entry Tick writes carries it, OR assert via the `d.rt` field after Tick (same package, so `d.rt.Policy.Order["builder"][0] == marker`). **Mutation check (run and report):** remove the `d.refresh` call from `Tick`; this test fails; restore by re-editing; passes.
- `TestTickWithoutRefreshIsUnchanged`: no `WithRefresh` -> `Tick` behaves as before (an existing daemon test still passes; name which one you relied on).

## 9. Ordered implementation steps

1. `reload.go` types, `NewConfigWatcher`, `Refresh`, `fail`; `reload_test.go` all cases. `go test ./internal/relay -run TestRefresh`.
2. `daemon.go`: field, `WithRefresh`, `Tick` first line; `daemon_test.go` two tests; run the mutation check and record both outcomes.
3. `cmd/relay/main.go` `cmdDaemon` wiring per §4. `go build ./... && go vet ./cmd/relay`.
4. Squash to one `fix(daemon):` commit including the plan copy; mention in the body that it closes the deferred hot-reload item of `docs/specs/2026-09-11-policy-order-design.md`. Gate, exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Report the tail verbatim, the mutation outcomes, and `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block (`status`, `halted_at`,
`changed_paths`, `commands_run`, `not_done`).
