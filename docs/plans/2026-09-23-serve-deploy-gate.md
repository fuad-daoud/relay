# Plan: the contabo deploy gate counts only work a restart would kill (relay #373, servers repo)

This round runs in a worktree of **`/home/fuad/projects/servers`** (default branch `master`), not the relay repo. Read `CLAUDE.md` and `contabo/README.md` there first.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. **Do not deploy, install, ssh to contabo or run `srv.fish` in any mode.** This round only edits files and runs local tests.

## 1. System overview

`contabo/srv-remote.sh`'s `quiet_or_die` (lines 17-28) refuses a deploy, rollback or restart of a service while the service's main process has children (`pgrep -P "$pid"`). For `relay-serve` that refuses **every** deploy while any served round runs. But since relay #309/#370, each served builder runs in its own transient `relay-round-*.scope`.

Verified on the box on 2026-09-23:
- `relay-serve.service`'s cgroup holds only the server process;
- the builder was in `relay-round-8ef156ea-h-locks-1.scope`;
- so a restart of the unit does not touch it.

The builders are still *process* children of the server, so `pgrep -P` sees them.

The gate should refuse only when a process that a restart **would** kill is running, meaning another process inside the unit's own cgroup. It should report scoped rounds as informational.

Also, the unit's `ExecStartPre` copies the new binary over `~/.local/bin/relay` non-atomically (`install` unlinks, then writes). relay ≥ #371's daemon watches that file. Make the copy atomic.

## 2. Files

```
contabo/srv-remote.sh              quiet_or_die: cgroup-based; a pure helper for the decision
contabo/srv-remote_test.sh         NEW  sh test of the helper with fake cgroup.procs files
contabo/services/relay-serve/relay-serve.service   atomic ExecStartPre copy
contabo/README.md                  one gotcha line: the gate counts processes in the unit's cgroup; scoped relay rounds survive a restart
```

## 3. Contracts

- **`unit_cgroup_kids <main_pid> <procs_file>`** prints every pid listed in `procs_file` except `main_pid`, one per line.
  - It is a pure function of its arguments: no `systemctl` inside.
  - A missing or unreadable file prints nothing and returns 0.
- **`quiet_or_die <name> <verb>`:**
  - `pid` = MainPID, as today; `pid <= 0` → return 0;
  - `cg=$(systemctl --user show -p ControlGroup --value "$n")`, `procs=/sys/fs/cgroup$cg/cgroup.procs`;
  - `kids=$(unit_cgroup_kids "$pid" "$procs")`, with each pid shown as `pid cmdline`, via `ps -o pid=,args= -p <list>`, or `tr` on `/proc/<pid>/cmdline`;
  - `SRV_FORCE`, the `die` message and the call sites stay as today, but the `die` text says "processes in <unit>'s cgroup";
  - when `n = relay-serve` and there are no kids: if `systemctl --user list-units --plain --no-legend --state=running 'relay-round-*.scope'` shows N > 0 units, print `srv-remote: N served round(s) run in their own scopes; they survive the restart` to stderr and return 0.
- **ExecStartPre in relay-serve.service:**

  `ExecStartPre=/bin/sh -c 'install -m755 %h/srv/bin/relay-serve %h/.local/bin/relay.new && mv -f %h/.local/bin/relay.new %h/.local/bin/relay'`

  It replaces the plain `install -m755 …` line. Keep the `agent install --force` line after it.

## 4. Steps

1. Implement the helper and the new `quiet_or_die`. Keep the script POSIX- and bash-compatible, whichever its shebang says. Run `shellcheck contabo/srv-remote.sh` if shellcheck is installed.
2. `contabo/srv-remote_test.sh`: source only the helper. If sourcing the whole script would execute its `case` dispatch, move the helper into a small sourced file, e.g. `contabo/lib/cgroup.sh`, that `srv-remote.sh` sources, and check how `srv.fish` ships `srv-remote.sh` to the box so the new file ships too. **If shipping a second file needs `srv.fish` changes, keep the helper inline instead**, and test it by extracting the function with `sed` between marker comments.

   Cases:
   - a procs file holding only the main pid → nothing;
   - main plus 2 others → those 2;
   - a missing file → nothing, exit 0.

   Mutation: make the helper print every pid, and the first case fails.
3. The unit change and the README gotcha line. Check the unit with `systemd-analyze verify` only if it runs offline on this machine. Otherwise skip it and say so.
4. Commit on this branch: `contabo: the deploy gate counts only processes a restart would kill (relay#373)`. Don't push.

Declared scope: §2's files (plus `contabo/lib/cgroup.sh` only under step 2's condition).
