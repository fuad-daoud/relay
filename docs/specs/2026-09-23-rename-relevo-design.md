# Rename relay -> relevo (#292)

Status: approved 2026-09-23. Supersedes the plan in #292's body. The issue predates
the herdr removal, the Claude Code plugin and MCP server, scopes and slices, served
remotes, and #372's format guard, so its inventory and its "the compiler catches
every miss" claim no longer hold.

## 0. Decisions

1. **Clean break.** Every name, including the ones stored in state and sent over the
   wire, becomes `relevo`. There is no dual-write and no alias binary. Precondition:
   no open round anywhere at cutover, and the laptop and contabo cut over the same
   evening.
2. **`relevo migrate`**, an explicit command, moves an install's config and state,
   and also switches its services. Doctor names the command. The daemon refuses to
   start on an unmigrated install. Nothing moves on its own.
3. **Scripted sweep + a legacy table.** The rename is a checked-in script
   (`scripts/rename-relevo.sh`). Old names survive only in `internal/legacy`, and
   only where something still needs them.
4. **Three repos.** Rounds run in relay, servers/contabo and relay-site, plus a
   cutover runbook (§5). GitHub repo renames happen only on the owner's go.

## 1. Names

| area | old | new |
|---|---|---|
| module | `github.com/fuad-daoud/relay` | `github.com/fuad-daoud/relevo` |
| packages | `cmd/relay`, `internal/relay` (package `relay`) | `cmd/relevo`, `internal/relevo` (package `relevo`) |
| binary / tarballs | `relay`, `relay_<v>_<os>_<arch>.tar.gz` | `relevo`, `relevo_<v>_<os>_<arch>.tar.gz` |
| config root | `$XDG_CONFIG_HOME/relay` | `$XDG_CONFIG_HOME/relevo` |
| state root | `$XDG_STATE_HOME/relay` (`~/.local/state/relay`) | `.../relevo` |
| database | `relay.db` (+`-wal`, `-shm`) | `relevo.db` |
| units | `relay.service`, `relay-serve.service`, `com.github.fuad-daoud.relay`, `relay.slice` | `relevo.service`, `relevo-serve.service`, `com.github.fuad-daoud.relevo`, `relevo.slice` |
| scopes | `relay-{round,gate,verify,consult}-*.scope` | `relevo-...` |
| env | `RELAY_*` (`RELAY_PLANNER` and 10 others) | `RELEVO_*` |
| git | branches `relay/<name>`, refs `refs/relay/<name>/{out,round-N}` | `relevo/<name>`, `refs/relevo/...` |
| wire | `Relay-{Client,Timestamp,Nonce,Signature}` | `Relevo-...` |
| log markers | `relay-exit:`, `relay-rusage:`, `relay-supervisor` | `relevo-...` |
| planner side | MCP server `relay`; plugin and marketplace `relay` 0.11.0 | `relevo`; `relevo` 0.12.0 |
| ledger | `Source: "relay"` | `Source: "relevo"` |

**Kept, and nothing else is (closed list):**

1. `docs/plans/**`, `docs/specs/**` and `docs/superpowers/**`: historical records.
2. The verb forms *relays*, *relayed* and *relaying*.
3. Stored data from before the cutover. A DONE binding's recorded `relay/<name>`
   branch is used as stored.
4. Hostnames: `relay-site.fuad-daoud.com` and the serve ingress. DNS is its own
   change.
5. `internal/harness/install_test.go`'s `olderArchitectDoc`: the exact bytes of a
   shipped blob.
6. `relayd` in `docs/design.md`, the herdr-era design.

**Legacy reads** (the new code still understands old names, via `internal/legacy`):

- Exit code, rusage and supervisor markers in round logs written before the
  cutover: every reader of `relevo-exit:`/`relevo-rusage:` also accepts the
  `relay-` form.
- Ledger entries with `Source: "relay"` read as `"relevo"`. Without this, #372's
  lenient reader would keep them as `Other` and ignore them, and every rate-limit
  gate recorded before the cutover would silently lapse.
- `relevo migrate` and `relevo doctor`, which look for the old roots, units and
  binary.

## 2. R1: the sweep

`scripts/rename-relevo.sh` (planner-authored, committed first on the branch):

1. `git mv` the six relay-named paths. The script refuses if any other relay-named
   path is tracked outside the kept docs.
2. Token rules, applied in order to every tracked text file outside the kept docs,
   `go.sum` and `shipped.sha256`:
   - protect the hostname;
   - module path;
   - `RELAY` not followed by a lowercase letter → `RELEVO`;
   - `Relay` likewise → `Relevo`;
   - `relay` not followed by a lowercase letter, and either not preceded by a letter
     or preceded by a `\n`/`\t` escape → `relevo`.
3. Restore `olderArchitectDoc` from HEAD.
4. `gofmt -w`, then `scripts/agents-shipped.sh --write`.

A trial on `7e607ea`:

- 509 paths changed; build and vet were clean.
- Left to do by hand:
  - two UI golden sets (`-update`: the title is one column wider);
  - `TestCardLinesShapes`, where `relevo/api` no longer fits the rail, so the
    fixture branch becomes `relevo/io`.
- After those fixes: tests, gofmt, tidy, plugin-version, shellcheck, the script
  tests and e2e all passed.

**Re-runnable by design.** Any branch that lands first (for example roles S1) is
absorbed by re-running the script on the rebased tree, not by merging.

## 3. R2: `relevo migrate` and legacy reads

**`relevo migrate [--dry-run] [--keep-old-binary]`** is one ordered procedure. Each
step is reported, and every step either succeeds or leaves a state that re-running
`migrate` resumes from.

1. **Detect.**
   - Old roots: `$XDG_CONFIG_HOME/relay` and `$XDG_STATE_HOME/relay` (default
     `~/.local/state/relay`).
   - New roots: the same paths with `relevo`.
   - Old units: the user-level systemd `relay.service` and `relay-serve.service` in
     `$XDG_CONFIG_HOME/systemd/user/`, and on macOS
     `~/Library/LaunchAgents/com.github.fuad-daoud.relay.plist`.
   - The old binary: `relay` beside the running executable, if one is there.
   - Nothing old → "nothing to migrate", exit 0.
2. **Refuse** (exit non-zero, the reason, and the fix) when:
   - a new root already exists and is non-empty while the matching old root also
     exists;
   - an old and a new root are on different filesystems;
   - any binding in the old state root (including `serve/` bindings on a server)
     has an open round, or any consult is running. List them.
3. **Stop the old services.** `stop` each old unit found, then take the old store's
   daemon lock to prove nothing still holds it. If the lock is held, refuse.
4. **Move.** `rename(2)` the config root, then the state root, then
   `relay.db`/`-wal`/`-shm` → `relevo.db`/... inside it.
5. **Rewrite stored absolute paths** that start with the old state root, in a
   closed set of fields:
   - `bind.json`: `Endpoint.LogPath`, `Serve.BareRepo`, `Worktree` (served
     bindings only);
   - consult records: `AskPath`, `FindingsPath`, `Findings`;
   - any DB column holding such a path.

   R2's step 0 confirms this list against the code and halts if it is materially
   different. A record whose format is newer than the binary is left untouched
   and reported, following #372's rule.
6. **Install the new client unit.**
   - Linux: write `relevo.service` from the copy embedded in the binary (a Go
     package `dist` embeds `dist/relevo.service` and the plist template), then
     `daemon-reload`, `enable --now`.
   - macOS: render the plist, then `launchctl load -w`.
   - Only if an old client unit existed. `relevo-serve.service` is not installed:
     its content is host-specific, and servers/contabo's deploy owns it. `migrate`
     prints that.
7. **Retire the old services.** `disable` each old unit and delete its unit file
   (`daemon-reload`, or `launchctl unload` plus delete the plist).
8. **Remove the old binary** unless `--keep-old-binary` is given. A lingering
   `relay` started by a stale plugin would otherwise recreate empty old roots.
9. **Report next steps:**
   - reinstall the plugin as `relevo@relevo`;
   - `relevo agent install`;
   - restart planner sessions.

Service calls go through an interface, so tests never run systemctl or launchctl.
CI has neither.

**Doctor** gets a `rename` row with three states:

- **ok:** no old root.
- **fail:** an old root with no new one. The fix is `relevo migrate`.
- **warn:** both roots exist, meaning an old binary recreated one. The fix names the
  stale plugin or binary.

**Daemon refusal.** `relevo daemon` and `relevo serve` refuse to start while an old
state root exists and the new one does not. They name both roots and
`relevo migrate`.

**Legacy readers**, as listed in §1.

## 4. R3: the guard and the planner side

- `scripts/check-name.sh`, run by `make check`, fails on any token that §2's rules
  would rewrite, outside §1's kept list, `internal/legacy`, migrate's tests and
  `scripts/rename-relevo.sh`. This catches in-flight branches that bring back
  `relay`.
- Plugin and marketplace: name `relevo`, MCP server key `relevo`, version 0.12.0.
  `check-plugin-version.sh` stays green.
- A prose pass over README, CONTRIBUTING, CLAUDE.md and the agent definitions for
  anything the sweep made read wrongly, then `agents-shipped.sh --write`.
- Release notes line for v0.12.0: the rename headline and the `relevo migrate` step.

## 5. Other repos and cutover

**servers/contabo round:**

- `services/relay-serve` → `services/relevo-serve`;
- the unit name, `ExecStart` and `Slice=relevo.slice`;
- `slices/relay.slice` → `relevo.slice`;
- `srv.fish` and `srv-remote.sh` names, including the deploy gate's scope counting;
- `services/relay-site` → `services/relevo-site`. The hostname stays.

**relay-site round:** module path, name and links. Content is out of scope.

**Cutover runbook** (owner-driven, one evening):

1. Merge relay, servers and relay-site. Tag v0.12.0.
2. With no open rounds anywhere, install `relevo` on the laptop, then run
   `relevo migrate --dry-run` and `relevo migrate`.
3. Reinstall the plugin: remove `relay@relay`, add the marketplace, install
   `relevo@relevo`. Then `relevo agent install` and restart planner sessions.
4. On contabo: `srv.fish` deploys `relevo-serve`, then `relevo migrate` runs on the
   box (it stops and retires `relay-serve.service`).
5. `gh repo rename` for `fuad-daoud/relay` → `relevo` and `relay-site` →
   `relevo-site`, only on the owner's go. Rename, never recreate: GitHub's redirect
   keeps old clones working.
6. `relevo doctor` shows `rename ok` on both machines.

## 6. Out of scope

- A compatibility `relay` alias.
- DNS or domain purchase.
- relay-site content.
- PRODUCT.md's "makes no judgements" drift.
- Renaming stored branch names of DONE bindings.
