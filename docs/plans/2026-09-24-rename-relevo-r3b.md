# Plan: #292 round 3b: `relevo migrate`, the verb (units, old binary, CLI)

Spec: `docs/specs/2026-09-23-rename-relevo-design.md` §3 (and its "Revised
2026-09-24" block). Round 3a built `internal/migrate.Run` (the core), plus
`git.(*Client).WorktreeRepair` and `db.RewritePathPrefix`. Round 2 built
`internal/legacy`, `dist.ClientUnit`/`dist.LaunchdPlist`, and the guard in `run()`
that exempts `migrate`.

This round wires the core into a verb that also switches the client daemon's
service unit and removes the old binary.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 1. System overview

`relevo migrate` on a user's machine runs, in order:

1. a dry-run pre-check (no daemon check, because the old daemon is still up);
2. stop `relay.service` (or unload the launchd plist) if it is installed;
3. the real core run;
4. install and start `relevo.service` (or the relevo plist) if an old one was
   installed;
5. disable and remove the old unit;
6. remove the old `relay` binary beside the running `relevo`;
7. print the next steps.

`--state-from/--state-to` runs only the core on an explicit pair. That's for
contabo's `--state /srv/data/relay-serve`; units and binaries are not touched in
that mode.

## 2. File structure

```
internal/migrate/services.go       NEW  Unit, Services interface
internal/migrate/services_os.go    NEW  systemd (systemctl --user) and launchd (launchctl) implementations
internal/migrate/units.go          NEW  ClientUnits (old/new unit paths per platform), StopOld, SwapClient, RemoveOldBinary
internal/migrate/units_test.go     NEW  fake Services; temp dirs
cmd/relevo/migrate.go              NEW  cmdMigrate: flags, wiring, the ordered flow above
cmd/relevo/migrate_test.go         NEW  flag rules + a dry run on a temp tree (spawns nothing)
cmd/relevo/main.go                 EDIT the dispatch switch (~300-390): case "migrate"; the usage text: one line
README.md                          EDIT a short "Upgrading from relay" section (§5 text)
```

## 3. Data structures

```
type Unit struct {
    Name string // "relay.service" | "relevo.service" | launchd label
    Path string // absolute unit file (systemd) or plist (launchd)
}

type Services interface {
    Stop(ctx context.Context, u Unit) error    // systemd: systemctl --user stop <Name>;         launchd: launchctl unload <Path>
    Start(ctx context.Context, u Unit) error   // systemd: systemctl --user start <Name>;        launchd: launchctl load <Path>
    Enable(ctx context.Context, u Unit) error  // systemd: systemctl --user enable --now <Name>; launchd: launchctl load -w <Path>
    Disable(ctx context.Context, u Unit) error // systemd: systemctl --user disable <Name>;      launchd: launchctl unload -w <Path>
    Reload(ctx context.Context) error          // systemd: systemctl --user daemon-reload;       launchd: no-op
    // State reports whether u runs now and whether it starts at login.
    // systemd: `systemctl --user is-active <Name>` == "active", `is-enabled <Name>` == "enabled" (a non-zero exit is just false);
    // launchd: `launchctl list <Name>` exit 0 -> both true, else both false.
    State(ctx context.Context, u Unit) (active, enabled bool, err error)
}

type ClientUnits struct {
    Platform string // "linux" | "darwin"; anything else: no units (all funcs no-op)
    Old, New Unit
}

type UnitOptions struct {
    Units          ClientUnits
    Services       Services
    ClientUnitText string // dist.ClientUnit
    PlistTemplate  string // dist.LaunchdPlist (@BIN@, @HOME@)
    Exe, Home      string // running executable (resolved), $HOME
    DryRun         bool
    Out            io.Writer
}
```

## 4. Interfaces

- **`func OSServices(goos string) Services`** returns the systemd implementation
  for `linux`, the launchd one for `darwin`, and a no-op otherwise. It runs
  `exec.CommandContext`, and a failure returns an error carrying the combined
  output. It must compile on every GOOS (CI cross-compiles).
- **`func DefaultClientUnits(goos, configHome, home string) ClientUnits`**
  - linux:
    - Old = `{legacy.ClientUnit, <configHome>/systemd/user/relay.service}`;
    - New = `{"relevo.service", <configHome>/systemd/user/relevo.service}`.
  - darwin:
    - Old = `{legacy.LaunchdLabel, <home>/Library/LaunchAgents/<legacy.LaunchdLabel>.plist}`;
    - New = `{"com.github.fuad-daoud.relevo", .../com.github.fuad-daoud.relevo.plist}`.
  - Build `relay` strings from `internal/legacy` only.
- **`func StopOld(ctx, o UnitOptions) (step migrate.Step, old OldUnit, err error)`**,
  where `type OldUnit struct{ Installed, WasActive, WasEnabled bool }`.
  - Old unit file absent → Skipped step, zero `OldUnit`.
  - Else `State(Old)`, recorded in `OldUnit` with `Installed=true`.
  - When `WasActive`: `Stop` (or, in a dry run, only the step saying it would).
  - An installed but inactive unit is not stopped. Its step says `installed,
    not running`.
  - Why: contabo has an installed but inactive, disabled `relay.service`.
    Migrating must not start a relevo daemon there that nobody ran.
- **`func SwapClient(ctx, o UnitOptions, old OldUnit) ([]migrate.Step, error)`**
  - Only when `old.Installed`. Otherwise return one Skipped step, "no client
    unit was installed".
  - **Install** (linux): write `New.Path` from `ClientUnitText` (mkdir -p its
    dir), then `Reload`. Then `Enable(New)` **only if** `old.WasActive ||
    old.WasEnabled`. Otherwise the step says `installed, left disabled like the
    old unit`.
  - **Install** (darwin): write `New.Path` from `PlistTemplate` with
    `@BIN@`→`Exe` and `@HOME@`→`Home`. Then `Enable(New)` under the same
    condition.
  - **Retire:** `Disable(Old)`, remove `Old.Path`, and `Reload` on linux.
  - Idempotent: a new unit file already equal to what would be written is not
    rewritten, and an absent old file skips the retire.
  - Dry run: steps only.
- **`func RemoveOldBinary(exe string, keep, dryRun bool) (migrate.Step, error)`**
  - `sibling = filepath.Join(filepath.Dir(exe), legacy.Binary)`.
  - Absent, or not a regular file → Skipped.
  - `os.SameFile` as `exe` → Skipped, "is this binary".
  - `keep` → Skipped, "kept (--keep-old-binary)".
  - Dry run → the step only. Else `os.Remove`.
- **`func cmdMigrate(args []string) error`**
  - Flags: `--dry-run`, `--keep-old-binary`, `--state-from DIR`, `--state-to DIR`.
  - Exactly one of the pair set → usage error, exit 2.

## 5. Pseudocode: `cmdMigrate`

```
parse flags
exe := os.Executable() resolved with filepath.EvalSymlinks
repair  := func(ctx, repo, wt) = git.NewClient("git", 60*time.Second, git.DefaultMaxPatchBytes).WorktreeRepair(ctx, repo, wt)   (as newRuntime builds it, main.go ~573, longer timeout)
rewrite := adapter from migrate.Prefix to db.Prefix around db.RewritePathPrefix
alive   := the existing pidAlive helper (git grep -n 'func pidAlive' cmd/relevo)
if explicit pair:
    o := migrate.Options{StateFrom, StateTo, DryRun, Alive, Repair, RewriteDB, Out: os.Stdout}
    run core; print result; exit (refused -> 1)
roots := renameRoots()                               (round 2, cmd/relevo/rename.go)
o := migrate.Options{StateFrom: roots.OldState, StateTo: roots.NewState, ConfigFrom: roots.OldConfig, ConfigTo: roots.NewConfig,
                     DefaultServeRoot: roots.OldState + "/serve", Alive, Repair, RewriteDB, Out: os.Stdout}
units := migrate.DefaultClientUnits(runtime.GOOS, userConfigRoot(), home)
uo := migrate.UnitOptions{Units, Services: migrate.OSServices(runtime.GOOS), ClientUnitText: dist.ClientUnit, PlistTemplate: dist.LaunchdPlist, Exe: exe, Home: home, DryRun, Out}
1. pre := migrate.Run(ctx, o with DryRun=true, SkipDaemonCheck=true)
       refused -> print; exit 1 (nothing touched)
       pre.Nothing && no old unit file && no old binary -> print "nothing to migrate"; exit 0
   if --dry-run: also print StopOld/SwapClient/RemoveOldBinary steps in dry-run form; print next steps; exit 0
2. _, old, err := StopOld(uo)              err -> exit 1 (nothing moved)
3. res, err := migrate.Run(ctx, o)         (real, daemon check on)
       err (refusal or other) -> if old.WasActive: Start(Old) again and say so; print; exit 1
3b. slice value: in <ConfigTo>/policy.json (if present), replace the exact byte sequence `"` + legacy.Slice + `"`
    with `"relevo.slice"` (atomic write, same mode; unchanged file untouched). Report it as step "rename-slice".
    Why: `serve.scope.slice` is "relay.slice" on the laptop and on contabo, where relay.slice (the 6G cap) is
    retired and replaced by relevo.slice. In explicit-pair mode this step does not run. Dry run: step only.
4. SwapClient(uo, old)                 err -> print exact manual commands for what did not happen; exit 1 (data is already migrated; say so)
5. RemoveOldBinary(exe, keep, false)
6. print every Warn step again under "warnings:", then next steps:
     relevo doctor
     relevo agent install
     reinstall the planner plugin as relevo (README: Upgrading from relay)
     restart planner sessions
exit 0
```

**README section.** Add it under the install section, titled `### Upgrading from
relay`: 5-8 lines saying relay was renamed relevo in v0.12.0 and giving the order.

1. Finish or pause every binding.
2. Install `relevo`.
3. `relevo migrate --dry-run`, then `relevo migrate`.
4. Reinstall the Claude plugin: remove `relay@relay`, then add the marketplace from
   `fuad-daoud/relevo` and install `relevo@relevo`. Use the exact `claude plugin`
   commands that match how the README already tells users to install the plugin.
   Find that section, and use its style.
5. `relevo agent install`.
6. Restart planner sessions.

Build every old name in Go strings from `internal/legacy`. In README prose,
`relay` appears only in this section (round 4's guard allowlists the section by
its heading).

## 6. Error handling

- Nothing is touched before step 2, and a refusal at step 1 changes nothing.
- A failure after step 2 and before the core moves anything restarts the old unit.
- A failure after the core succeeded never rolls the data back (that would be a
  second migration). It prints what remains, as literal commands.

## 8. Working efficiently

Read these once:

- `internal/migrate/*.go` from round 3a;
- `cmd/relevo/rename.go` from round 2;
- `cmd/relevo/main.go`: dispatch ~300-390, usage text (grep `usage =`), `newRuntime` ~526-580.

- **Focused loop:** `go test ./internal/migrate/ ./cmd/relevo/ -run 'Migrate|Units|OldBinary|Swap|Stop'`.
- **Full check, once at the end:** `make check`, and also
  `GOOS=windows go build ./...` and `GOOS=darwin go build ./...`.

**CLI tests (CI has no harness, no network, and no systemd user bus):**

- `cmd/relevo/migrate_test.go` may call `cmdMigrate` only with `--dry-run`, or with
  a flag error. With `--dry-run`, `Services` is never called.
- Set `XDG_STATE_HOME`, `XDG_CONFIG_HOME` and `HOME` to `t.TempDir()` roots, and
  create a relay-era tree there with a `systemd/user/relay.service` file.
- Assert the output names every step and the tree is unchanged.
- Everything else is tested in `internal/migrate` with a fake `Services` that
  records calls.

## 9. Ordered implementation steps

**Step 1: `services.go`, `services_os.go`.** No tests for the OS implementation
beyond compiling. The fake in step 2's tests covers the calls.

**Step 2: `units.go`**, with tests using a recording fake.

- **Linux happy path:** old file present and the fake says active+enabled →
  `StopOld` stops it. `SwapClient` writes the new file from the text, then calls
  `Reload`, `Enable(new)`, `Disable(old)` and `Reload`, and the old file is gone.
- **Installed but inactive and disabled** (the contabo case): `StopOld` calls no
  `Stop`. `SwapClient` writes the new file, calls **no** `Enable(new)`, still
  retires the old one, and the old file is gone.
- **Darwin template:** the written plist has no `@BIN@`/`@HOME@`, and contains
  `Exe`.
- **No old unit:** `SwapClient(OldUnit{})` is one Skipped step with no calls.
- **Idempotent re-run:** the new file is already right and the old one is gone → no
  write, and no retire calls.
- **Dry run:** no calls and no writes.
- **`RemoveOldBinary`:** sibling present → removed; same file (a hard link to exe)
  → kept; `keep` → kept; absent → Skipped.

Mutation-test:
- the `old.Installed` guard;
- the `WasActive || WasEnabled` guard on `Enable(new)`;
- the `SameFile` check;
- the dry-run guard in `SwapClient`.

**Step 3: `cmd/relevo/migrate.go`, dispatch and usage.**

- Tests:
  - the flag pair rule;
  - the dry run on a temp relay-era tree (above): output lists pre-check, stop,
    move-config, move-state, rename-db, rewrite-json, rewrite-db,
    repair-worktrees, install, retire and old-binary, and the tree is byte-identical
    afterwards.
- Also run by hand, and paste the output into the report:

  ```
  XDG_STATE_HOME=$T/s XDG_CONFIG_HOME=$T/c HOME=$T go run ./cmd/relevo migrate --dry-run
  ```

  on a temp relay-era tree you create. Then run it without `--dry-run` **on the
  same temp tree**, with no unit file present, so nothing calls systemctl. Paste
  that output and `ls` the result.

**Step 3b: `rename-slice`.** Implement it as `migrate.RenameSliceValue(configDir
string, dryRun bool) (migrate.Step, error)` in `internal/migrate/units.go`.

Tests:
- a policy.json holding `"slice": "relay.slice"` gets rewritten, and every other
  byte stays equal;
- one without it is untouched, mtime included;
- an absent file → Skipped.

Mutation: the exact-quote match. The test string `"slice": "relay.slicer"` must
stay unchanged.

**Step 4: README section.**

**Step 5: full check and commit.**
- `make check` plus the two cross-builds must pass.
- One commit:
  `feat(migrate): relevo migrate -- moves relay-era state, switches the client unit, removes the old binary (#292)`.
- Don't push.

**Declared scope:** exactly §2's files.

**Report:**
- per-step status;
- mutations;
- the two pasted runs;
- `git diff --stat HEAD~1`;
- the results of the checks.
