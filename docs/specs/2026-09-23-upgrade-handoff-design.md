# Upgrade handoff (#371)

Status: approved design, 2026-09-23. Decision: **the daemon re-execs itself onto a replaced binary.** Implementation: `docs/plans/2026-09-23-upgrade-handoff-r1.md` and `-r2.md`.

## 1. System overview

Installing a new relay today changes only the file on disk. The running daemon keeps running the deleted old build indefinitely, and nothing shows it. `relay mcp` inside every planner session stays old for that session's whole life. Role definitions are never refreshed. The binary swap isn't atomic.

With several releases a day, "install it and everything is on the new version, with no round disturbed" has to be the default. That is only safe because builders, gates and consults survive a daemon restart (#370 spike, 2026-09-23). A re-exec is a restart that keeps the PID.

Rounds:
- **R1**, the daemon side: `daemon.json`; drain on SIGTERM; re-exec onto a replaced binary behind a debounce and a preflight; reaping inherited children; atomic `make install`; `doctor` and `status` show the daemon's version.
- **R2**, everything else a binary upgrade leaves stale: role definitions self-update when unmodified; `relay mcp` tells the planner to reconnect; the release check backs off on failure.

Out of scope:
- a self-downloading `relay update` (deferred by the #293 decision);
- the plugin version bump policy;
- `relay serve` (#373) and the state-file format (#372);
- rewriting an installed systemd unit file.

## 2. Components and files

```
internal/store/daemoninfo.go     NEW (R1) DaemonInfo, FileID, ReexecFailure; Write/ReadDaemonInfo (daemon.json, atomic)
internal/upgrade/                NEW package (R1)
  exe.go                         ResolveExe; ExeIdentity (unix build tag; !unix stub returns ErrUnsupported)
  watch.go                       Watcher + Decision: debounce, preflight, sticky refusal (pure, injected stat/preflight)
internal/proc/reap.go            NEW (R1, unix) InheritedReaper; ParseProcStatPPID (pure)
internal/proc/reap_other.go      NEW (R1, !unix) no-op reaper
internal/relay/daemon.go         Run: drain the in-flight tick on cancel; WithUpgrade hook; ErrReexec (R1)
cmd/relay/main.go                cmdDaemon: --preflight; daemon.json; watcher/reaper wiring; syscall.Exec on ErrReexec;
                                 status notice for daemon version skew (R1)
internal/doctor/doctor.go (+env.go)  daemon check reads DaemonInfo (R1); role staleness for every harness (R2)
Makefile                         install via temp + rename (R1)
README.md                        upgrade text; correct the role-definition claim (R1 upgrade text, R2 roles claim)
internal/harness/install.go      manifest of written hashes; OutcomeUpdated/WouldUpdate; atomic WriteFile (R2)
internal/mcp/server.go           Server.Notice func() string appended to every tool result (R2)
cmd/relay/mcp.go                 wires Notice from daemon.json (R2)
internal/relay/daemon.go         refreshRelease failure backoff (R2)
```

## 3. Data structures

**`store.FileID`**, JSON `{dev, ino, size, mod_time}`
- `Dev uint64`, `Ino uint64`, `Size int64`, `ModTime time.Time`.
- It is comparable with `==`. The zero value means "unknown", and it never equals a real identity.

**`store.ReexecFailure`**, JSON `{exe_id, at, reason}`
- `ExeID FileID`: the identity that failed preflight.
- `At time.Time`.
- `Reason string`: the first line of the preflight's stderr, or its error, at most 300 bytes.

**`store.DaemonInfo`**, the file `<state root>/daemon.json`
- `Version string`: `buildVersion()` of the running image.
- `PID int`.
- `StartedAt time.Time`: the start of this *image*, so a re-exec resets it.
- `Exe string`: the resolved executable path, with no " (deleted)" suffix.
- `ExeID FileID`: the identity of `Exe` when this image started.
- `ReexecFrom string`, `omitempty`: the previous image's version, when this image came from a re-exec.
- `ReexecFailed *ReexecFailure`, `omitempty`: set while the file at `Exe` is a build this daemon refused.

It is written atomically (temp file in the root, then rename, mode 0644) at image start, and again whenever `ReexecFailed` changes. It is removed on a clean shutdown, not on a re-exec. A missing file with the daemon lock held means "a daemon older than #371".

**`upgrade.Decision`**
- `Action`: one of `None`, `Wait`, `Reexec` and `Refused`.
- `Reason string`: set on `Refused`.

**R2: the role manifest**, `<state root>/agents-manifest.json`
- A `map[string]string` from a home-relative path to the lowercase hex sha256 of the exact bytes relay last wrote there, or found identical to what it ships.

## 4. Contracts

### 4.1 `upgrade.ResolveExe() (string, error)`

`os.Executable`, then `filepath.EvalSymlinks`, then strip a trailing `" (deleted)"`. It is called once at image start, *before* any possible replacement.

### 4.2 `upgrade.ExeIdentity(path string) (store.FileID, error)`

`os.Stat` plus `syscall.Stat_t` (dev, ino) under `//go:build unix`. The `!unix` build returns `errors.ErrUnsupported`.

### 4.3 `upgrade.Watcher`

- Fields: `Path string`, `Started store.FileID`, `Stat func(string) (store.FileID, error)` and `Preflight func(ctx context.Context, path string) error`. Private state: `pending *store.FileID` and `refused *store.FileID`.
- `func (w *Watcher) Check(ctx context.Context) Decision`:
  1. `cur, err := Stat(Path)`. An error (the file is missing mid-install) → `Wait`, and `pending` is cleared.
  2. `cur == Started` → `None`, and `pending` is cleared. The binary is back to ours, as after a rollback.
  3. `refused != nil && cur == *refused` → `None`. The same build is not tried again.
  4. `pending == nil || *pending != cur` → set `pending = cur` and return `Wait`. This is the debounce: an identity has to be seen unchanged on two consecutive checks.
  5. `Preflight(ctx, Path)` with a 30 s timeout:
     - error → `refused = cur`, `Refused{Reason}`;
     - nil → `Reexec`.
- `func (w *Watcher) Refused() *store.FileID` is exposed for `daemon.json`.

### 4.4 `relay daemon --preflight`

- It is hidden: it doesn't appear in usage text.
- It runs `newRuntime()` exactly as the daemon does (candidates, policy, servers and hooks config all parsed and validated), prints `ok <version>` and exits 0.
- Any error → the error on stderr, exit 1.
- It takes **no** lock, opens **no** DB (opening the DB runs migrations), starts no process, and makes no network call. It returns before the DB open in `cmdDaemon`.

### 4.5 `Daemon.Run` (R1)

- Each tick runs under `context.WithoutCancel(ctx)`. When `ctx` is cancelled during a tick, that tick completes and `Run` then returns `nil`. It never starts another tick after cancellation.
- `func (d *Daemon) WithUpgrade(f func(ctx context.Context) bool) *Daemon`. After every completed tick, and only while `ctx` is not cancelled, `Run` calls `f`. True → `Run` returns `ErrReexec`.
- `var ErrReexec = errors.New("relay daemon: re-exec onto a new binary")`.

### 4.6 `proc.InheritedReaper` (R1, unix)

- `func NewInheritedReaper(self int, procRoot string) *InheritedReaper`. It scans `procRoot` (`/proc` on Linux) once for processes whose parent is `self`. On darwin, or with no `procRoot`, it uses `ps -A -o pid=,ppid=`.
- It must be constructed before this image starts any child.
- `func (r *InheritedReaper) Reap() []int`: `syscall.Wait4(pid, WNOHANG)` for each inherited pid, dropping it on a reap or on `ECHILD`, and returning the reaped pids. It never waits on a pid it did not inherit, so it can't steal an `exec.Cmd`'s status.
- `func ParseProcStatPPID(stat string) (ppid int, ok bool)`: pure. It parses the field after the last `)`.

### 4.7 cmdDaemon order (R1)

```
parse flags
newRuntime                       (a --preflight run returns here: print ok, exit 0/1)
rt.StartedAt, rt.Watched         (#370)
reaper := NewInheritedReaper(os.Getpid(), "/proc")     -- before anything can spawn
exe, exeID := ResolveExe, ExeIdentity  (failure: log Warn, re-exec disabled, the daemon still runs)
openDB, config watcher, --check (unchanged)
AcquireDaemonLock
WriteDaemonInfo{Version, PID, StartedAt, Exe, ExeID, ReexecFrom: $RELAY_REEXEC_FROM}
hooks
daemon := NewDaemon(...).WithRefresh(...).WithUpgrade(upgradeHook)
err := daemon.Run(ctx)
if errors.Is(err, relay.ErrReexec):
    close DB, stop the signal context, close the lock      (explicit, not relying on CLOEXEC)
    env := os.Environ() with RELAY_REEXEC_FROM=<this version> set (replacing any existing value)
    syscall.Exec(exe, append([]string{exe}, os.Args[1:]...), env)
    // only reached on failure:
    return fmt.Errorf("re-exec %s: %w", exe, execErr)
    // a non-zero exit, so systemd's Restart=on-failure starts the new binary anyway
on a clean return: remove daemon.json
```

`upgradeHook(ctx)` does three things:
- `reaper.Reap()`;
- `d := watcher.Check(ctx)`. On `Refused`, it logs Warn once and rewrites `daemon.json` with `ReexecFailed`. On `None` after an earlier refusal was cleared, it rewrites `daemon.json` without it;
- it returns `d.Action == Reexec`, logging Info `re-exec onto <exe> (<old id> -> <new id>)`.

The `syscall.Exec` call lives in a `//go:build unix` file in cmd/relay. On `!unix` the hook is never installed.

### 4.8 Doctor and status (R1)

- `doctor.Env` gains `DaemonInfo() (store.DaemonInfo, bool, error)`.
- The daemon row, when the daemon is running:
  - info missing → Warn: `running, but started before relay recorded its version: it will not follow upgrades until restarted once`, Fix `systemctl --user restart relay.service`, or `make service`.
  - `info.ReexecFailed != nil` → Warn: `runs <info.Version>; the relay binary at <exe> failed preflight (<reason>) and was not loaded`, Fix `fix the error above; the daemon retries when the file changes`.
  - `info.Version != cli` and no failure → OK: `runs <v>; switching to <cli> within seconds`.
  - equal → OK: `running <v>`.
- `relay status` notice, from the pure `daemonNotice(cli string, info store.DaemonInfo, ok, running bool) string`. It returns "" unless running and one of these holds:
  - `!ok` → `relay daemon predates version tracking -- restart it once (relay doctor)`;
  - `info.ReexecFailed != nil` → `relay daemon refused the new binary -- relay doctor`.

  A plain version difference prints nothing, because it is transient during a re-exec.

### 4.9 Makefile install (R1)

`install -m755 relay $(BIN).new && mv -f $(BIN).new $(BIN)`, after the existing `mkdir -p`. A rename within the directory is atomic. The README's manual install gets the same two lines, plus one sentence: the running daemon picks up the new binary by itself, and a planner session reconnects its MCP server once it is told to.

### 4.10 R2 contracts

- **Roles.** `harness.InstallEnv` gains `LoadManifest() (map[string]string, error)` and `SaveManifest(map[string]string) error`. A missing manifest is an empty map. `installOne`:
  - missing → write, record;
  - `DocEqual` → `KeptIdentical`, record the sha of the *existing* bytes;
  - differs and `manifest[path] == sha(existing)` → overwrite, `OutcomeUpdated = "updated (unchanged since relay wrote it)"`, record. Under DryRun it is `OutcomeWouldUpdate = "would update"`;
  - differs, not in the manifest or a different sha → today's `KeptDiffers`, or `Overwrote` with `--force`, which records.

  `osInstallEnv.WriteFile` writes through a temp file in the same directory, then renames. The manifest is saved once per `Install` call, and only when it changed.
- **The daemon refreshes roles** once per image start, after the lock: `harness.Install(OSInstallEnv, InstallOptions{})` over the kinds `relay agent install` would pick. `Wrote`/`Updated` are logged at Info, `KeptDiffers` at Info (`<path> was edited; relay agent install --force replaces it`) and errors at Warn. It is never fatal.
- **Doctor roles.** For every harness whose binary is on PATH, a dry-run Install with the manifest. `WouldUpdate` or `WouldWrite` → Warn `role definitions are stale; the daemon refreshes them on its next start, or run relay agent install`. `KeptDiffers` → OK with the detail `edited by you (kept)`. Keep the existing agy model check.
- **MCP.**
  - `mcp.Server` gains `Notice func() string`. When it is non-nil and returns "", nothing changes. Otherwise `handleToolsCall` appends one more text content item with the notice to the tool result, errors included.
  - The wiring in cmd/relay caches the `daemon.json` read for 30 s and uses the pure `mcpNotice(own string, info store.DaemonInfo, ok bool) string`. It returns "" unless `ok && info.Version != own && info.ReexecFailed == nil`. Otherwise it returns `note: relay was upgraded to <info.Version>; this session's relay MCP server is still <own>. Reconnect it (/mcp) or restart the session to load the new version.`
- **Release check backoff.** A failed fetch sets an in-memory `releaseRetryAt = now + 1h`, and `refreshRelease` returns early while `now < releaseRetryAt`. A success clears it. It stays synchronous. This is the only change to `refreshRelease`.

## 5. Flow: `make install` while a round runs

```
t0  install writes relay.new, renames over relay     (atomic; running processes are unaffected)
t0+2s  tick N ends -> hook: Stat != Started -> pending=cur -> Wait
t0+4s  tick N+1 ends -> hook: same cur -> Preflight(new binary) ok -> Reexec
       Run returns ErrReexec -> close DB, lock -> exec(new)   (same PID; builder scopes untouched)
new image: reaper scans the old image's children; lock; daemon.json{Version:new, ReexecFrom:old}
       first tick: builder Alive (its scope survived) -> Mark -> round continues
round closes normally; the builder's exit is reaped by the InheritedReaper
```

## 6. Errors and observability

| Situation | Behaviour |
|---|---|
| Preflight fails | Warn once per refused identity; `daemon.json` `ReexecFailed`; doctor Warn; status notice. The old image keeps running. |
| `syscall.Exec` fails | The error is returned and the process exits non-zero. systemd restarts it, on the new file. Logged at Error. |
| `ResolveExe` or `ExeIdentity` fails at start | Warn `re-exec disabled: <err>`; the daemon runs normally. |
| `WriteDaemonInfo` fails | Warn; the daemon runs normally, and doctor then reports the info as missing. |
| Reaper errors other than `ECHILD` | Debug. |

CI: no test runs `syscall.Exec`, a harness or the network. The Watcher, `daemonNotice`, `mcpNotice`, `ParseProcStatPPID`, the manifest decisions and `Run`'s `ErrReexec` and drain are pure or use temp dirs. The `--preflight` cmd/relay test uses a `t.TempDir()` `XDG_CONFIG_HOME` (CLAUDE.md, #235) and spawns nothing.
