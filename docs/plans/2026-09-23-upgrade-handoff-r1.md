# Plan: upgrade handoff, round 1 of 2: the daemon follows a new binary (#371)

**Read first:** `/home/fuad/projects/relay/docs/specs/2026-09-23-upgrade-handoff-design.md`. That file is the design, and this plan orders R1's part of it. Where the two disagree, the spec wins. Stop and report rather than pick.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, write the report saying which step and why, and create the done marker. Don't bend a test to fit. Specifically, halt if:
- `newRuntime()` does more than read and validate config: it writes files, opens the DB, spawns a process or reaches the network. `--preflight` depends on it having no side effects;
- `cmdDaemon`'s order differs materially from spec §4.7's starting point.

## 1. System overview

Installing a new relay binary must move the running daemon onto it without disturbing any round. Builders, gates and consults survive a daemon restart because each runs in its own systemd scope (#370 spike). A re-exec is a restart that keeps the PID.

In this round the daemon:
- records what it runs (`daemon.json`);
- finishes its in-flight tick on SIGTERM;
- watches its own executable, and after a two-tick debounce and a `--preflight` run of the new file, execs into it;
- refuses a new binary that fails preflight, without looping;
- reaps children it inherited from the previous image.

`make install` swaps the file atomically, and `doctor`/`status` report the daemon's version state. Role definitions, the MCP notice and the release-check backoff are round 2.

**A parallel branch (#370) edits `internal/relay/daemon.go`** (`tickOne` and `Tick`: panic recovery) and `internal/proc/proc.go` (psInfo). In `daemon.go`, touch only `Run`, the `Daemon` struct, `NewDaemon`/`With*` and a new `ErrReexec`. Leave `Tick` and `tickOne` alone. In `internal/proc`, add new files and don't edit `proc.go`.

## 2. File structure

```
docs/specs/2026-09-23-upgrade-handoff-design.md  COPY from the absolute path above (commit it)
internal/store/daemoninfo.go        NEW  FileID, ReexecFailure, DaemonInfo; (*Store) WriteDaemonInfo, ReadDaemonInfo, RemoveDaemonInfo
internal/store/daemoninfo_test.go   NEW
internal/upgrade/exe.go             NEW  //go:build unix  ResolveExe, ExeIdentity
internal/upgrade/exe_other.go       NEW  //go:build !unix  ExeIdentity -> errors.ErrUnsupported (ResolveExe may be shared in a no-tag file)
internal/upgrade/watch.go           NEW  Decision, Action consts, Watcher, Check, Refused (no build tag: pure)
internal/upgrade/watch_test.go      NEW
internal/proc/reap.go               NEW  //go:build unix  InheritedReaper, NewInheritedReaper, Reap
internal/proc/reap_other.go         NEW  //go:build !unix  no-op InheritedReaper with the same API
internal/proc/procstat.go           NEW  ParseProcStatPPID (pure, no tag)
internal/proc/procstat_test.go      NEW
internal/relay/daemon.go            Run drain + WithUpgrade + ErrReexec ONLY
internal/relay/daemon_run_test.go   NEW  Run returns ErrReexec; drain on cancel
cmd/relay/main.go                   cmdDaemon: --preflight, reaper, exe identity, daemon.json, upgrade hook, re-exec; statusNotice caller gains daemonNotice
cmd/relay/reexec_unix.go            NEW  //go:build unix  reexec(exe string, args, env []string) error  (syscall.Exec)
cmd/relay/reexec_other.go           NEW  //go:build !unix  reexec returns errors.ErrUnsupported
cmd/relay/daemon_notice.go          NEW  daemonNotice (pure) + its test file
cmd/relay/main_test.go or new test  --preflight test (temp XDG dirs; spawns nothing)
internal/doctor/doctor.go, env.go   Env.DaemonInfo; daemon row per spec §4.8; tests in doctor_test.go
Makefile                            install via relay.new + mv (spec §4.9)
README.md                           manual-install lines + one "upgrades are picked up automatically" paragraph
```

## 3. Data structures

As in spec §3: `store.FileID`, `store.ReexecFailure`, `store.DaemonInfo` and `upgrade.Decision`/`Action`. JSON tags are snake_case, exactly as the spec lists them. `DaemonInfo` lives at `<state root>/daemon.json`. Add a `daemonInfoFileName` const next to `daemonLockFileName`.

## 4. Interfaces and contracts

- `func (s *Store) WriteDaemonInfo(info DaemonInfo) error`: temp file in the root, then `os.Rename`, 0644.
- `func (s *Store) ReadDaemonInfo() (DaemonInfo, bool, error)`: a missing file is `(zero, false, nil)`, and malformed JSON is an error.
- `func (s *Store) RemoveDaemonInfo() error`: not-exist is nil.
- `upgrade.ResolveExe`, `ExeIdentity`, `Watcher.Check` and `Watcher.Refused`: spec §4.1–4.3. The Watcher's preflight timeout is a package const, `PreflightTimeout = 30 * time.Second`, applied inside `Check`.
- `proc.NewInheritedReaper(self int, procRoot string) *InheritedReaper` and `(*InheritedReaper).Reap() []int`: spec §4.6. On Linux, scan `procRoot/<pid>/stat` with `ParseProcStatPPID`. When `procRoot` can't be read (darwin), fall back to `ps -A -o pid=,ppid=` parsed by a pure helper.
- `relay.ErrReexec` and `(*Daemon).WithUpgrade(f func(ctx context.Context) bool) *Daemon`: spec §4.5.
- `daemonNotice(cli string, info store.DaemonInfo, ok, running bool) string`: spec §4.8.
- `doctor.Env.DaemonInfo() (store.DaemonInfo, bool, error)`. The real env reads the store, and test fakes return fixed values.
- `reexec(exe string, argv, env []string) error`: a thin wrapper over `syscall.Exec`. It is not unit-tested.

Pre- and postconditions:
- `NewInheritedReaper` is called before the image starts any child process (before `openDB`, the config watcher, hooks or the first tick).
- After `Run` returns `ErrReexec`, the DB is closed, the daemon lock fd is closed and the signal context is stopped before `reexec` is called.
- `daemon.json` exists whenever this image holds the daemon lock, except when the write failed, which is logged. It is removed on a clean return from `Run`, and not on the re-exec path.

## 5. High-level pseudocode

```
Daemon.Run(ctx):
  ticker
  loop:
    select:
      ctx.Done -> return nil (Canceled) / ctx.Err()   (unchanged)
      tick:
        tctx := context.WithoutCancel(ctx)
        if err := d.Tick(tctx); err != nil { log }        // a cancel mid-tick lets it finish
        if ctx.Err() != nil -> return nil                   // no new tick, no upgrade check after cancel
        if d.upgrade != nil && d.upgrade(ctx) -> return ErrReexec

cmdDaemon (spec §4.7 is normative for order):
  flags: --interval, --check, --preflight (hidden: define it, and keep it out of README and usage examples)
  rt, err := newRuntime(); if *preflight { if err -> print err to stderr, exit 1; print "ok "+buildVersion(); return nil }
  if err -> return err
  rt.StartedAt ... (unchanged, plus #370's rt.Watched line if it is present on main when you rebase; do not add it yourself)
  reaper := proc.NewInheritedReaper(os.Getpid(), "/proc")
  exe, exeErr := upgrade.ResolveExe(); exeID, idErr := upgrade.ExeIdentity(exe)
  reexecOK := exeErr == nil && idErr == nil ; else slog.Warn("re-exec disabled", ...)
  openDB ... (unchanged) ; watcher (config) ... ; --check ... ; lock ...
  info := DaemonInfo{Version: buildVersion(), PID: os.Getpid(), StartedAt: time.Now(), Exe: exe, ExeID: exeID, ReexecFrom: os.Getenv("RELAY_REEXEC_FROM")}
  WriteDaemonInfo(info) (Warn on error)
  if info.ReexecFrom != "" -> slog.Info("re-exec'd", "from", info.ReexecFrom, "to", info.Version)
  hooks ...
  w := &upgrade.Watcher{Path: exe, Started: exeID, Stat: upgrade.ExeIdentity,
        Preflight: func(ctx, p) error { cmd := exec.CommandContext(ctx, p, "daemon", "--preflight"); CombinedOutput; err -> errors.New(first line of output or err) }}
  hook := func(ctx) bool {
     reaper.Reap()
     if !reexecOK { return false }
     prev := w.Refused()
     d := w.Check(ctx)
     if w.Refused() != prev (changed) -> info.ReexecFailed = (nil or {*w.Refused(), now, d.Reason}); WriteDaemonInfo(info)
     if d.Action == upgrade.Refused -> slog.Warn("new relay binary refused", "exe", exe, "reason", d.Reason)   // only logged when it changes
     if d.Action == upgrade.Reexec -> slog.Info("re-exec onto new binary", "exe", exe); return true
     return false }
  err = relay.NewDaemon(rt, *interval).WithRefresh(watcher.Refresh).WithUpgrade(hook).Run(ctx)
  if errors.Is(err, relay.ErrReexec):
     close DB (if open); stop(); lock.Close()
     env := withEnv(os.Environ(), "RELAY_REEXEC_FROM", buildVersion())     // replace, don't duplicate
     execErr := reexec(exe, append([]string{exe}, os.Args[1:]...), env)
     return fmt.Errorf("re-exec %s: %w", exe, execErr)
  _ = rt.Store.RemoveDaemonInfo()
  return err
```

The existing `defer lock.Close()` and `defer d.Close()` must not double-close into an error path that matters. Make both closes idempotent (a `sync.Once`, or a nil-check wrapper), or restructure the defers. Either is fine, but say which you chose in the report.

`withEnv(env []string, key, val string) []string` is a pure helper with a test: it replaces an existing `KEY=`, or appends.

## 6. Error handling strategy

Spec §6 is normative. In summary:
- A preflight failure keeps the old image running, sets `ReexecFailed` and logs once. The same identity is never retried; a different identity is.
- An exec failure makes the process exit non-zero, so systemd's `Restart=on-failure` starts the new file.
- An exe identity failure disables re-exec with a Warn. The daemon keeps running.
- Nothing in this round adds a halt, a `NEEDS YOU` or a changed round outcome.

## 7. Ordered implementation steps

**Step 1: bring the spec in.** Copy the spec file into `docs/specs/`.

**Step 2: `store.DaemonInfo`** and its read, write and remove functions, with tests:
- a round trip;
- a missing file is `(false, nil)`;
- malformed JSON is an error;
- the write is atomic: after `WriteDaemonInfo`, no temp file remains in the root.

Verify: `go test ./internal/store/`.

**Step 3: `internal/upgrade`.** `exe.go`, `exe_other.go` and `watch.go`. Watcher table tests with a fake `Stat` and `Preflight`:
- (a) unchanged → None;
- (b) a first change → Wait, and the same identity next check → Reexec when preflight is ok;
- (c) a change, then a different identity → Wait again (the debounce restarts);
- (d) preflight fails → Refused, and the same identity next check → None without calling Preflight (assert the call count);
- (e) a refused identity followed by a newer identity → Wait, then Reexec;
- (f) Stat error → Wait, with pending cleared;
- (g) back to Started → None.

`ExeIdentity` test on a temp file: rewriting it via rename changes the identity; a no-op doesn't.
Verify: `go test ./internal/upgrade/`, and the windows cross-compile `GOOS=windows go build ./...`.

**Step 4: `ParseProcStatPPID`** (pure; the test covers a command name containing spaces and parentheses, e.g. `123 (a) b) S 77 ...`) **and `InheritedReaper`**.
Reaper test (unix): start `sleep 0.2` with `exec.Command` *before* constructing the reaper with `os.Getpid()`, construct the reaper, wait 400 ms, and `Reap()` returns that pid. A child started *after* construction is never reaped by it: start one, `Reap()`, then `cmd.Wait()` must succeed (not ECHILD).
Verify: `go test -race ./internal/proc/`.

**Step 5: `Daemon.Run`** drain, `WithUpgrade` and `ErrReexec`. Tests in `internal/relay/daemon_run_test.go`, using a Runtime over an empty `t.TempDir()` store (the pattern existing daemon tests use):
- a hook returning true on its first call → `Run` returns `ErrReexec` after exactly one tick;
- with a refresh func that blocks until released while `ctx` is cancelled, then released → `Run` returns nil, the refresh saw a non-cancelled ctx (`Tick` got `WithoutCancel`), and the hook was never called after cancellation.

Mutation: remove `WithoutCancel` and confirm the drain test fails.

**Step 6: `daemonNotice`** (pure) plus its table test (spec §4.8). Wire it into `relay status` next to `statusNotice` at main.go ~1748. It reads `DaemonRunning` and `ReadDaemonInfo`. A read error prints nothing.

**Step 7: doctor.** `Env.DaemonInfo`, and the daemon row per spec §4.8. Extend the doctor tests with the four states: missing info, refused, version differs, equal. Keep the existing daemon rows (not running, probe error) byte-identical.

**Step 8: cmdDaemon wiring** (spec §4.7 and the pseudocode above): `--preflight`, the reaper, exe identity, `daemon.json`, the hook, the re-exec path, `withEnv` and its test.
- `--preflight` test in cmd/relay: point `XDG_CONFIG_HOME` and `XDG_STATE_HOME` at `t.TempDir()`s (valid config → `cmdDaemon([]string{"--preflight"})` returns nil; a malformed `policy.json` → it returns an error, depending on how cmdDaemon exits, so test through the function that does the work if `os.Exit` gets in the way).
- Assert the state root has **no** `relay.db` and no `daemon.lock` afterwards: preflight opened nothing.
- This test must not execute a harness or reach the network (CLAUDE.md).

**Step 9: Makefile and README.** Makefile `install:` becomes `mkdir -p`, `install -m755 relay $(BIN).new` and `mv -f $(BIN).new $(BIN)`. README: the manual install uses the same two commands, plus one paragraph under install/upgrade:
- the daemon moves onto a newly installed binary by itself within a few seconds, and running rounds are not interrupted;
- a daemon started before this release needs one manual restart (`make service`, or `systemctl --user restart relay.service`);
- `relay doctor` shows what the daemon runs.

**Step 10: gate.** `make check` and `make e2e`. Commit with a message ending `(#371)`.

**Manual acceptance (report what you observed; do not skip):** build twice into a temp dir with different `-ldflags "-X main.version=..."` values. Run the first as `relay daemon` with `XDG_STATE_HOME` and `XDG_CONFIG_HOME` pointing at temp dirs, in the background. `mv` the second build over it. Within about 10 s:
- `daemon.json` shows the second version, with `reexec_from` set to the first;
- the PID is unchanged;
- the log shows "re-exec".

Then `mv` a file that exits 1 (a shell script) over it: `daemon.json` gains `reexec_failed`, and the daemon keeps ticking. Kill the test daemon afterwards.
Use the temp state dirs only. **Never touch `~/.local/state/relay`, `relay.service` or the installed `~/.local/bin/relay`.**

Declared scope: exactly the files in §2.
