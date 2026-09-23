# Plan: relay#292 in the servers repo: relay-serve -> relevo-serve, relay-site -> relevo-site, relay.slice -> relevo.slice

Repo: `~/projects/servers` (this worktree), branch `relevo-292`. Spec: relay's
`docs/specs/2026-09-23-rename-relevo-design.md` §5. relay is being renamed
relevo in one clean break: binary `relevo`, units `relevo*`, config
`~/.config/relevo`, state `~/.local/state/relevo`. relevo ships `relevo migrate`:

- **`relevo migrate`** (no flags) moves `~/.config/relay` → `~/.config/relevo`
  (merging with files already there) and `~/.local/state/relay` →
  `~/.local/state/relevo`. It also switches a client `relay.service` if one is
  installed, and removes `~/.local/bin/relay` next to the running relevo.
  - It refuses while a relay daemon or a `relay serve` still runs, or any binding
    is open.
  - It is a no-op once done (exit 0).
- **`relevo migrate --state-from A --state-to B`** does the same move, path
  rewrite and worktree repair for an explicit serve `--state` directory. It is a
  no-op when A is absent (exit 0).

The **public hostnames do not change**: `relay.fuad-daoud.com` (the API) and
`relay-site.fuad-daoud.com` (the site). DNS is its own later change.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the repo, halt and report.

## 1. Overview

The planner committed `contabo/rename-relevo.sh`, a mechanical sweep, as this
branch's first commit. A trial run on `0c6f2fb` gave 25 changed paths, left no
stray `relay` token outside the two hostnames, and passed `srv-remote_test.sh`
and `fish -n srv.fish`.

This round runs it, then makes the hand changes the new name needs:

- the unit migrates the box's relay-era state on its first start;
- `srv` gets a `retire` verb, so the old units can be removed through the tool and
  not by hand (the repo's rule: never edit units on the box by hand);
- `NOTES.md` documents the one-time cutover.

## 2. Files

```
contabo/rename-relevo.sh                          (committed by the planner; run it, don't edit it)
contabo/services/relevo-serve/relevo-serve.service EDIT two ExecStartPre lines (§3)
contabo/srv-remote.sh                             EDIT `retire` verb (§4)
contabo/srv-remote_test.sh                        EDIT a retire case if the test's harness can express one (see §4)
contabo/srv.fish                                  EDIT `retire <name>` verb + its usage line in the header
contabo/services/relevo-serve/NOTES.md            EDIT "Cutover from relay-serve (relay#292)" section (§5)
contabo/services/relevo-site/NOTES.md             CHECK the build line reads `cd ~/projects/relay-site && ... -o /tmp/relevo-site .` (the sweep does it; the checkout dir keeps its name)
```

## 3. The unit (`contabo/services/relevo-serve/relevo-serve.service`)

Insert, **between** the `install -m755 ... relevo` ExecStartPre line and the
`relevo agent install --force` line:

```
# One-time relay -> relevo (relay#292): move the box's relay-era client state
# (~/.config/relay, ~/.local/state/relay; removes ~/.local/bin/relay) and this
# server's data dir. Both are no-ops once done, and both refuse -- failing the
# start, loudly -- while anything relay-era still runs. Keep them until every
# box that ran relay-serve has started relevo-serve once; then delete both lines.
ExecStartPre=%h/.local/bin/relevo migrate
ExecStartPre=%h/.local/bin/relevo migrate --state-from /srv/data/relay-serve --state-to /srv/data/relevo-serve
```

The order matters. `agent install` writes into `~/.local/state/relevo`, and
relevo refuses every verb but `migrate` while `~/.local/state/relay` exists
without its new root.

## 4. `retire` (`srv-remote.sh` + `srv.fish`)

**`srv-remote.sh`**, a new case beside `restart`:

```
retire)  # <unit>... ; stop, disable and delete units this repo no longer ships (e.g. relay-serve.service after relay#292)
    shift
    for u in "$@"; do
        case "$u" in *.service|*.slice|*.timer) ;; *) die "retire: $u: give the full unit name (.service/.slice/.timer)";; esac
        [ -f "$U/$u" ] || { echo "srv-remote: $u: not installed; nothing to retire"; continue; }
        case "$u" in *.service) quiet_or_die "${u%.service}" retire ;; esac
        systemctl --user disable --now "$u" >/dev/null 2>&1 || true
        rm -f "$U/$u"
        echo "srv-remote: retired $u"
    done
    systemctl --user daemon-reload ;;
```

`quiet_or_die` already exists. Read its signature (~line 30-55) and match the
existing callers' second argument. **Also** in the `install` case, the `start` line
and both `installed ...` echo lines must now say `relevo.slice` (the sweep did
this; confirm it).

**`srv.fish`:**

- A `retire` subcommand, `./srv.fish retire <unit>...`, that runs the helper's
  `retire` on the box, in the same way the file's existing `restart` wrapper
  calls the helper. Read that wrapper and copy its shape, including
  `SRV_FORCE` passthrough if `restart` passes it.
- A header usage line under `restart`:
  `#   ./srv.fish retire <unit>...        stop, disable, delete units no longer shipped (refused while in flight; SRV_FORCE=1)`.

**`srv-remote_test.sh`.** Look at what it tests today. If it can call the
script's cases with a fake `systemctl` on PATH (it tests `quiet_or_die`, so
there's likely a fake already), add these cases:

- retire of a missing unit → the "nothing to retire" line, exit 0;
- a name without a suffix → dies.

If the harness can't express them without a real systemd, add none, and say why in
the report.

## 5. NOTES: the cutover section

Append this to `contabo/services/relevo-serve/NOTES.md`, verbatim except for
fixing any path that disagrees with the repo:

```
## Cutover from relay-serve (relay#292, 2026-09-24)

Once, from the laptop, after the laptop itself ran `relevo migrate` (its seed files now live
in ~/.config/relevo):

    ./srv.fish retire relay-serve.service relay-site.service relay.slice   # old units off the box
    ./srv.fish install                                                   # relevo.slice + relevo-serve/relevo-site units; seeds ~/.config/relevo
    cd ~/projects/relay && go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o ~/.local/bin/relevo-build ./cmd/relevo
    ./srv.fish deploy relevo-serve ~/.local/bin/relevo-build              # first start runs both `relevo migrate` lines
    ./srv.fish logs relevo-serve -n 60                                    # the migrate steps are in the journal
    mv ~/backups/contabo-01/srv-data/relay-serve ~/backups/contabo-01/srv-data/relevo-serve   # keep the local mirror's name in step

`install` seeding ~/.config/relevo before the first start is fine: migrate merges the config
root (identical files are dropped from the old root; a differing one refuses, naming it).
The box's old `relay.service` (installed, never enabled) is retired by migrate itself.
```

## 8. Working efficiently

Read `srv-remote.sh`, `srv.fish` (header and the `restart` wrapper), the unit, and
`srv-remote_test.sh` once.

- **Checks:** `sh contabo/srv-remote_test.sh`, `shellcheck contabo/srv-remote.sh
  contabo/rename-relevo.sh`, `fish -n contabo/srv.fish`.
- **Nothing touches the box:** no `srv.fish` verb that ssh's may run in this
  round.

## 9. Steps

**Step 0.** `git status` must be clean on branch `relevo-292`, with HEAD
`contabo: the rename tool relay -> relevo (relay#292)`. Otherwise halt.

**Step 1.** `sh contabo/rename-relevo.sh`. It ends with `rename-relevo: done`.
Then run:

```
git grep -nIE '(RELAY|Relay)([^a-z]|$)|(^|[^A-Za-z]|\\[nt])relay([^a-z]|$)' -- . ':!docs/superpowers/**' ':!contabo/rename-relevo.sh' | grep -v 'relay-site.fuad-daoud.com' | grep -v 'relay.fuad-daoud.com' | grep -v 'projects/relay'
```

It must print nothing. The checkout paths `~/projects/relay` and
`~/projects/relay-site` are protected on purpose, because the clones keep their
directory names. Commit:
`contabo: rename relay -> relevo, the mechanical sweep (relay#292)`.

**Step 2.** §3, the unit.

**Step 3.** §4, `retire`. Checks must pass.

**Step 4.** §5, NOTES, and check the relevo-site NOTES build line.

**Step 5.** All three checks must pass. Commit:
`contabo: relevo-serve migrates relay-era state on first start; srv retire (relay#292)`.

Don't push.

**Declared scope:** §2.

**Report:**
- the step 1 output;
- the diffs of the unit and of the `retire` code;
- the three checks;
- `git log --oneline -3`.
