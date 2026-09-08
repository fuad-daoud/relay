# relay as a herdr plugin

Status: design approved, not yet implemented.
Issue: [#16](https://github.com/fuad-daoud/relay/issues/16).
Date: 2026-09-08.

## 1. System overview

relay is installed by hand today: unpack a release binary or `go install`, put
it on `PATH`, then run `make service` to get the reconciler started with your
session. herdr 0.7.3+ ships a plugin system that can collapse that into one
command and list relay in a marketplace of community plugins.

This spec packages relay as a herdr plugin. It is **packaging, not features**.
The plugin adds no new relay behaviour; it makes the relay that already exists
installable in one step and reachable from herdr's UI. herdr gains ownership of
installation, manifest validation, the pane surface, and keybindings. relay
keeps everything else exactly as it is.

Exactly one relay change is in scope, because the design cannot be honest
without it: a daemon lock. Section 7 explains why.

## 2. What a herdr plugin is, and what it is not

This section is the constraint set the rest of the spec is derived from. It
matters because almost every obvious design here is ruled out by one of these.

A plugin is a directory containing a `herdr-plugin.toml` manifest and argv
commands that herdr spawns as subprocesses. From herdr's plugin documentation:

> There is no separate plugin SDK or restricted command set. The entire Herdr
> CLI is the plugin API.

There is no in-process loading, no WASM, and no sandbox. A plugin's commands
run as your user and inherit your environment. herdr validates the manifest and
gives each plugin its own config and state directories; it does not review the
code.

The attachment points are fixed and declared in the manifest. There are six:
`[[build]]`, `[[startup]]`, `[[actions]]`, `[[panes]]`, `[[events]]`, and
`[[link_handlers]]`. Quoting the same page:

> Runtime action registration and native non-terminal plugin UI are not part of
> plugin v1.

Five constraints follow from that, and each one shapes a decision below:

1. **A plugin cannot modify herdr's UI.** "Plugin UI" means a terminal pane
   running your program, placed as `overlay`, `popup`, `split`, `tab` or
   `zoomed`. There is no way to add a sidebar entry or alter the tab bar.
2. **`[[build]]` runs only on `herdr plugin install` from GitHub, never on
   `herdr plugin link`.** Local development never builds; you build your working
   tree yourself.
3. **Build commands receive no runtime plugin context** and therefore no
   `HERDR_PLUGIN_STATE_DIR`. A build step can only write into the checkout, so
   the binary must land in the plugin root and be referenced relatively.
4. **A failing build command aborts the install** and the plugin is never
   registered. "Warn and continue" requires an explicit `exit 0`.
5. **Keybindings can only target actions.** The documented binding form is
   `type = "plugin_action"`. Opening a pane from a key requires an action that
   opens the pane.

6. **A build command cannot prompt the user.** Verified against 0.8.2 with a
   probe plugin: stdin, stdout and stderr are all non-terminals, `/dev/tty` is
   not writable, and a blocking `read` returns EOF immediately. This is why the
   download-or-build choice is made by which install command you run.
7. **Build commands receive no `HERDR_*` environment at all.** Also verified;
   the probe's environment dump was empty. Build scripts cannot call
   `$HERDR_BIN_PATH` and must be entirely self-contained.

Build commands run in a temporary directory (`.tmp-install-<pid>-<ms>/checkout`)
which herdr moves to `~/.config/herdr/plugins/github/<plugin-id>-<hash>/` once
the install succeeds. Files written during the build survive that move, which is
what makes writing the binary into the checkout viable at all. For a
subdirectory variant the plugin root is the subdirectory inside that checkout,
so the binary must be written to the plugin root, not the repository root.

### Version provenance

herdr's published plugin documentation is for v0.9.0. relay's floor is herdr
0.8.2, which is what this was designed against and verified against. The
manifest in section 4 was linked against 0.8.2 in full -- including `contexts`
on actions, `placement = "overlay"`, and `[[link_handlers]]` -- and returned no
warnings. Section 11 records the verification in full.

## 3. Repository layout

Two manifests in one repository. The choice between downloading a release
binary and building from source is made by **which install command you run**,
not by a prompt (constraint 6) and not by a hidden environment variable.

```text
relay/
  herdr-plugin.toml            # release variant  -> install fuad-daoud/relay
  from-source/
    herdr-plugin.toml          # source variant   -> install fuad-daoud/relay/from-source
  scripts/
    plugin-fetch.sh            # [[build]] download + checksum verify
    plugin-build.sh            # [[build]] go build
    plugin-daemon-check.sh     # [[startup]]
    plugin-open-ui.sh          # open-ui action
    plugin-install-service.sh  # install-service action
```

herdr's marketplace indexes a repository's manifests at the root and in
subdirectories, and "one repository card can contain multiple separately
installable plugins", so both variants surface from a single card.

The release variant lives at the root deliberately: the shortest install command
should be the recommended one.

Both manifests declare the **same plugin id**, `fuad-daoud.relay`. They are the
same plugin acquired two ways, not two plugins, and a shared id means installing
one replaces the other rather than registering two relays that both provide an
`open-ui` action. Verify this holds in practice (section 11).

## 4. Manifests

The release variant, at the repository root:

```toml
id = "fuad-daoud.relay"
name = "relay"
version = "0.x.y"
min_herdr_version = "0.8.2"
description = "Planner/builder handoff for herdr agent panes"
platforms = ["linux", "macos"]

[[build]]
command = ["sh", "scripts/plugin-fetch.sh"]

[[build]]
command = ["./relay", "version"]

[[startup]]
command = ["sh", "scripts/plugin-daemon-check.sh"]

[[actions]]
id = "open-ui"
title = "Open relay reader"
command = ["sh", "scripts/plugin-open-ui.sh"]

[[actions]]
id = "install-service"
title = "Install relay daemon service"
command = ["sh", "scripts/plugin-install-service.sh"]

[[panes]]
id = "ui"
title = "relay"
placement = "overlay"
command = ["./relay", "ui"]
```

The source variant is identical except that its first `[[build]]` entry runs
`scripts/plugin-build.sh` instead of `scripts/plugin-fetch.sh`. It keeps the
second `["./relay", "version"]` entry unchanged: verification matters more for a
source build, not less. Its plugin
root is `from-source/`, one level below the repository root, so its build script
must locate the repository root explicitly rather than assuming its working
directory contains `cmd/relay`.

`version` tracks the relay release the manifest is published alongside.

`platforms` omits Windows. relay already refuses to run there, because herdr
does; declaring it here means herdr returns `platform_unsupported` before any
script executes, which is a better error than relay's runtime refusal.

The second `[[build]]` entry is a verification step, not a build step. Running
`./relay version` immediately after producing the binary means a corrupt
download, a wrong-architecture asset, or a broken build aborts the install
cleanly instead of registering a plugin whose commands all fail later.

## 5. Build scripts

**`plugin-fetch.sh`** detects OS and architecture, downloads
`relay_<version>_<goos>_<goarch>.tar.gz` and `checksums.txt` from the release
matching the manifest `version`, verifies the archive's sha256 against
`checksums.txt`, extracts it, and writes `./relay` into the plugin root. Any
failure exits non-zero.

Both assets already exist: `.github/workflows/release.yml` builds
`linux/amd64`, `linux/arm64`, `darwin/amd64` and `darwin/arm64` archives and
publishes `checksums.txt` alongside them. The release pipeline needs no change,
and the fetch script must stay pinned to that naming scheme -- if the archive
name changes, the plugin breaks for every already-published version.

**`plugin-build.sh`** requires a Go toolchain. If `go` is absent it exits
non-zero with a message naming `go` and the version floor from the README. It
does not warn and continue: a registered plugin with no binary is worse than a
failed install, and per constraint 4 the choice is explicit. It changes to the
repository root, builds `./cmd/relay`, and writes the binary into the plugin
root.

Neither script may modify `herdr-plugin.toml`; herdr aborts the install if the
manifest changes after the install preview.

herdr's install preview lists each build command's argv and each action, but
**not the contents of the scripts they run**. A user confirming an install sees
`sh scripts/plugin-fetch.sh`, not what that script does. The preview is
therefore a manifest of what will execute, not a security review; the review has
to happen in the repository. Both scripts must stay short enough that reading
them there is realistic, and the README should point at them by path.

## 6. Runtime components

### Startup hook

`plugin-daemon-check.sh` runs `./relay daemon --check`. If no daemon is live it
fires a notification via `$HERDR_BIN_PATH notification show`. It always exits 0.

This is deliberately not a supervisor. herdr's documentation is explicit:

> Startup hooks are one-shot initialization commands rather than supervised
> daemons. A hook should restore plugin-owned state, call any required Herdr
> APIs, and exit.

Startup hooks also re-run when a new server takes over during live handoff, so a
hook that spawned `relay daemon` would eventually run two reconcilers against
one state directory. Supervision stays with systemd and launchd, which already
provide `Restart=on-failure`. The hook's job is to notice and say so.

### `open-ui` action

Runs `$HERDR_BIN_PATH plugin pane open --plugin fuad-daoud.relay --entrypoint
ui`. It exists as an action rather than being invoked as a pane directly because
of constraint 5: a keybinding can only target an action. Users bind it with

```toml
[[keys.command]]
key = "prefix+r"
type = "plugin_action"
command = "fuad-daoud.relay.open-ui"
description = "open relay"
```

`plugin.pane.open` returns `ui_busy` when Settings, Copy mode, or another herdr
modal is active. The script surfaces that as a notification rather than failing
silently.

Commands call herdr through `HERDR_BIN_PATH`, never through a bare `herdr` on
`PATH` and never through the raw socket. herdr's documentation recommends this
because the socket transport differs between Unix sockets and Windows named
pipes; here it also guarantees the plugin talks to the server that spawned it.

### `ui` pane

Runs `./relay ui` with `placement = "overlay"`, a temporary zoomed overlay that
restores the previous focus and zoom when it closes. `relay ui` is already a
pure reader that never mutates state, which makes it safe to open from anywhere,
including over a pane the user is working in.

The reader covers the entire read surface -- report, terminal, diff, and log --
which is why no `status`, `log`, or `diff` actions are needed. An action's
stdout goes to the plugin command log rather than a terminal, so a printing
action would produce nothing a user can see.

### `install-service` action

Copies `./relay` to `~/.local/bin/relay`, then installs the existing systemd
user unit or LaunchAgent pointing at that path, matching what `make install &&
make service` does today.

Two reasons it is an action and not part of the build. First, writing outside
herdr's own directories deserves explicit consent, and an action the user
invokes is that consent. Second, it resolves a real hazard: an installed plugin
carries its own relay binary while the user may already have another on `PATH`,
and both write `~/.local/state/relay`. Two relay versions sharing one state
format is a corruption vector. After `install-service` runs, the plugin's binary
*is* the `PATH` binary, and the skew is gone.

A user who never runs the action keeps two binaries. That is acceptable and must
be documented in the README.

## 7. The daemon lock

`relay daemon` has no singleton guard today. `store.WithLock` takes an exclusive
flock for individual state operations, but nothing prevents two daemons from
running and reconciling the same bindings.

This spec adds a lock at `$XDG_STATE_HOME/relay/daemon.lock`:

- `relay daemon` acquires it at startup and refuses to start, with a clear
  error, if it is already held.
- `relay daemon --check` exits 0 if a daemon holds the lock and 1 if not,
  printing nothing.

The startup hook needs `--check`. The refusal is a fix relay wants regardless of
this plugin -- the hole exists today and `make service` plus a manually started
daemon is enough to hit it.

This is the only relay source change in scope.

## 8. Error handling

| Failure | Behaviour |
| --- | --- |
| Download fails, checksum mismatch, unsupported arch | non-zero exit; install aborts, plugin never registered |
| `go` missing in the source variant | non-zero exit naming `go` and the version floor |
| Binary present but does not run | caught by the `./relay version` build step; install aborts |
| Build modifies `herdr-plugin.toml` | herdr aborts the install itself |
| Daemon not running at startup | herdr notification; hook exits 0 |
| `plugin.pane.open` returns `ui_busy` | surfaced as a notification; action exits 0 |
| Windows | `platform_unsupported` from herdr, before any script runs |
| A second `relay daemon` | refused with a clear error naming the lock path |

Nothing in the plugin retries. An install that half-succeeded is worse than one
that failed loudly, and every failure above is one the user can act on.

## 9. Testing

- `shellcheck` over `scripts/*.sh`, wired into `make check` alongside the
  existing `gofmt`, `go vet` and `go test` gate.
- `herdr plugin link` against the working tree validates the manifest and
  returns warnings in its response. This is both the local development loop and
  a CI check wherever herdr is available.
- A CI matrix job runs `plugin-fetch.sh` for each supported platform and
  architecture and asserts `relay version` succeeds.
- Go tests for the daemon lock: acquire, contend, release, and `--check` in both
  states.
- The `[[build]]` path cannot be exercised without a real GitHub install, since
  `plugin link` never runs build commands. One manual pass against a prerelease
  tag, recorded in the PR.

## 10. Out of scope

- Interactive pickers and the full action surface. Tracked in
  [#15](https://github.com/fuad-daoud/relay/issues/15).
- Round URLs and `[[link_handlers]]`, which would make relay artifacts
  Ctrl+clickable in a terminal.
- Remote and headless builders. Tracked in
  [#6](https://github.com/fuad-daoud/relay/issues/6). Note that herdr 0.9.0's
  `herdr machine` covers part of its premise, but pane ids and agent names are
  scoped to one server, so relay must exist on both hosts regardless.
- Sharing planners and builders across people and machines.
- Replacing the reconciler with herdr `[[events]]` hooks. Architecturally the
  best fit for herdr, but a core redesign of polling, diff capture, hook
  dispatch and stall detection. It also overlaps relay's existing
  `~/.config/relay/hooks/` mechanism and must reconcile with it rather than
  stack on top of it.

## 11. Verification

All four questions this spec originally left open were settled against herdr
0.8.2 on 2026-09-08, using a throwaway probe plugin whose build command recorded
its environment and exited 0.

1. **Does 0.8.2 accept `contexts` on `[[actions]]`?** Yes. `herdr plugin link`
   accepted it and echoed it back with no warnings. The full section 4 manifest
   also links clean, `[[link_handlers]]` and `placement = "overlay"` included.
2. **Do two subdirectory variants sharing one plugin id replace cleanly?** Yes,
   and herdr says so explicitly. Installing the second variant printed
   `replaces: probe.relay from github:<owner>/<repo>@<commit>` in the preview,
   and `plugin list` reported one plugin afterwards.
3. **Can a subdirectory variant's build reach the repository root?** Yes. herdr
   clones the whole repository; the plugin root is the subdirectory; `..` is the
   repository root, and `go build ../cmd/<pkg>` succeeded from there.
   `git rev-parse --show-toplevel` also resolves to the checkout root, which is
   the more robust way for `plugin-build.sh` to locate it.
4. **Can a `[[build]]` command prompt?** No. stdin, stdout and stderr are all
   non-terminals, `/dev/tty` is not writable, and a blocking `read` returns EOF
   immediately. The two-manifest design in section 3 exists because of this.

Two further findings came out of the same probe and are folded into section 2:
build commands receive no `HERDR_*` environment, and build artifacts written
into the checkout survive the move to the managed plugin directory.

Nothing in this spec now rests on an unverified assumption about herdr.
