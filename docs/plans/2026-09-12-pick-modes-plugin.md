# Pick modes: plugin surface and docs (#15)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #15. **Spec:** `docs/specs/2026-09-12-pick-modes-design.md` --
read §3 and §7 before starting; section numbers below refer to it. The Go side
(`internal/pick`, the `--pick` flag) is a separate plan
(`docs/plans/2026-09-12-pick-modes.md`) running in another worktree; **this
plan touches no `.go` file.** The two plans share no files and merge
independently.
**Depends on:** nothing open. The manifests reference `./relay done --pick`
etc.; those flags land with the Go plan and the manifest is inert until a
release carries both, which is how the existing `ui` pane entry was added.

**Goal:** the herdr plugin declares one popup pane and one action per verb
(`pick-done`, `pick-unbind`, `pick-answer`), the pane-opening script takes
the entrypoint as an argument, and the README documents the flags and the
actions.

**Architecture:** `scripts/plugin-open-ui.sh` becomes
`scripts/plugin-open-pane.sh <entrypoint>`; the existing `open-ui` action
keeps its id and calls the new script with `ui`, so bindings on it survive.
Both manifests (`herdr-plugin.toml`, `from-source/herdr-plugin.toml`) gain the
same three `[[panes]]` and three `[[actions]]`; the `from-source` copy keeps
its `../scripts/` prefix, as every action there already does.

**Tech stack:** POSIX `sh`, TOML, Markdown. Verification is `make check`
(which runs `shellcheck scripts/*.sh` when shellcheck is installed) plus a
`git grep` that no reference to the old script name survives.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
command -v shellcheck >/dev/null && shellcheck scripts/*.sh
```

Do **not** run `herdr` yourself, and do not `herdr plugin link` this tree.
The popup behaviours in spec §11 are verified by the planner, after merge.

## Global constraints

- The `open-ui` action keeps its id `open-ui` and its title. Only its
  `command` changes.
- The `ui` pane entry is untouched.
- Action ids are exactly `pick-done`, `pick-unbind`, `pick-answer`; pane ids
  are the same three strings. herdr keybindings name actions by
  `fuad-daoud.relay.<id>`, so these are user-facing and must match the spec.
- No `.go` file is modified.
- One commit per task, on the worktree's branch.

---

### Task 1: the pane-open script takes its entrypoint as an argument

**Files:**
- Rename: `scripts/plugin-open-ui.sh` → `scripts/plugin-open-pane.sh` (`git mv`)
- Modify: `herdr-plugin.toml:17-20`, `from-source/herdr-plugin.toml:17-20`
  (the `open-ui` action's `command`)
- Modify: `docs/specs/2026-09-08-relay-herdr-plugin-design.md:101,141`
  (the two mentions of the old file name)

**Interfaces:**
- Consumes: `herdr plugin pane open --plugin fuad-daoud.relay --entrypoint <id>`
  (herdr 0.8.2, already used by the script).
- Produces: `sh scripts/plugin-open-pane.sh <entrypoint-id>`, exit 0 always;
  used by every action in Task 2.

- [ ] **Step 1: Rename the script**

```bash
git mv scripts/plugin-open-ui.sh scripts/plugin-open-pane.sh
```

- [ ] **Step 2: Make it take the entrypoint from `$1`**

Replace the whole of `scripts/plugin-open-pane.sh` with:

```sh
#!/bin/sh
# herdr action: open one of relay's plugin panes.
#
# usage: plugin-open-pane.sh <entrypoint-id>
#
# Actions exist because a herdr keybinding can only target an action, never a
# pane entrypoint. Every relay action that opens a pane goes through this one
# script: `ui` for the reader, `pick-done` / `pick-unbind` / `pick-answer` for
# the pickers (#15).
set -eu

entrypoint=${1:?usage: plugin-open-pane.sh <entrypoint-id>}
herdr_bin=${HERDR_BIN_PATH:-herdr}

if err=$("$herdr_bin" plugin pane open --plugin fuad-daoud.relay --entrypoint "$entrypoint" 2>&1); then
	exit 0
fi

# ui_busy means Settings, Copy mode, or another herdr modal is open. Say so
# rather than failing silently -- the user pressed a key and deserves an answer.
case "$err" in
	*ui_busy*)
		"$herdr_bin" notification show "relay" \
			--body "close the open herdr dialog first, then try again" || true
		;;
	*)
		"$herdr_bin" notification show "relay: could not open $entrypoint" \
			--body "$err" --sound request || true
		;;
esac

exit 0
```

- [ ] **Step 3: Check it refuses a missing argument and lints clean**

Run:

```bash
sh scripts/plugin-open-pane.sh; echo "exit=$?"
command -v shellcheck >/dev/null && shellcheck scripts/plugin-open-pane.sh && echo lint-ok
```

Expected: the first line prints `scripts/plugin-open-pane.sh: 1: entrypoint:
usage: plugin-open-pane.sh <entrypoint-id>` (wording varies by shell) and
`exit=2`; the second prints `lint-ok` (or nothing if shellcheck is not
installed -- say so in the report).

- [ ] **Step 4: Point the `open-ui` action at the new script**

In `herdr-plugin.toml` change the `open-ui` action to:

```toml
[[actions]]
id = "open-ui"
title = "Open relay reader"
command = ["sh", "scripts/plugin-open-pane.sh", "ui"]
```

In `from-source/herdr-plugin.toml`:

```toml
[[actions]]
id = "open-ui"
title = "Open relay reader"
command = ["sh", "../scripts/plugin-open-pane.sh", "ui"]
```

In `docs/specs/2026-09-08-relay-herdr-plugin-design.md`, line 101 becomes
`    plugin-open-pane.sh        # open-ui action (and the pick-* actions, #15)`
and line 141 becomes `command = ["sh", "scripts/plugin-open-pane.sh", "ui"]`.

- [ ] **Step 5: Verify no reference to the old name survives, and commit**

Run: `git grep -n plugin-open-ui -- ':!plan-herdr-plugin.md' ':!docs/plans'`
Expected: no output. (`plan-herdr-plugin.md` at the repo root is a historical
plan and is left as it was.)

Run: `make check`
Expected: green.

```bash
git add scripts/plugin-open-pane.sh herdr-plugin.toml from-source/herdr-plugin.toml \
        docs/specs/2026-09-08-relay-herdr-plugin-design.md
git commit -m "refactor(plugin): open-ui action goes through plugin-open-pane.sh <entrypoint> (#15 task 1)"
```

---

### Task 2: three popup panes and three actions

**Files:**
- Modify: `herdr-plugin.toml` (append after the `ui` pane),
  `from-source/herdr-plugin.toml` (same)

**Interfaces:**
- Consumes (Task 1): `scripts/plugin-open-pane.sh <id>`.
- Produces: actions `fuad-daoud.relay.pick-done`, `.pick-unbind`,
  `.pick-answer`, bindable with `type = "plugin_action"`; popup panes with
  the same ids running `./relay <verb> --pick`.

- [ ] **Step 1: Append to `herdr-plugin.toml`**

After the existing `[[panes]]` block for `ui`, append (spec §7, verbatim):

```toml

[[panes]]
id = "pick-done"
title = "relay done"
placement = "popup"
command = ["./relay", "done", "--pick"]

[[panes]]
id = "pick-unbind"
title = "relay unbind"
placement = "popup"
command = ["./relay", "unbind", "--pick"]

[[panes]]
id = "pick-answer"
title = "relay answer"
placement = "popup"
command = ["./relay", "answer", "--pick"]

[[actions]]
id = "pick-done"
title = "Mark a binding done"
command = ["sh", "scripts/plugin-open-pane.sh", "pick-done"]

[[actions]]
id = "pick-unbind"
title = "Unbind a binding"
command = ["sh", "scripts/plugin-open-pane.sh", "pick-unbind"]

[[actions]]
id = "pick-answer"
title = "Answer the blocked builder"
command = ["sh", "scripts/plugin-open-pane.sh", "pick-answer"]
```

- [ ] **Step 2: Append the same to `from-source/herdr-plugin.toml`**

Identical, except each action's script path is `"../scripts/plugin-open-pane.sh"`
(the pane `command` arrays are unchanged: `./relay` is relative to the plugin
root in both manifests, as the `ui` pane already shows).

- [ ] **Step 3: Check the two manifests differ only where they always did**

Run: `diff herdr-plugin.toml from-source/herdr-plugin.toml`
Expected: exactly these differing lines -- the `description`, the `[[build]]`
command, and every `scripts/` → `../scripts/` action path (now seven of them:
daemon-check, open-ui, install-service, pick-done, pick-unbind, pick-answer,
and the build line). No pane block differs.

Run: `python3 -c "import tomllib,sys; [tomllib.load(open(p,'rb')) for p in ('herdr-plugin.toml','from-source/herdr-plugin.toml')]; print('toml-ok')"`
Expected: `toml-ok`. If `tomllib` is missing (Python < 3.11), say so in the
report and confirm by eye that every block has `id`, `title`, `command`, and
each pane has `placement`.

- [ ] **Step 4: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add herdr-plugin.toml from-source/herdr-plugin.toml
git commit -m "feat(plugin): pick-done, pick-unbind, pick-answer popup panes and actions (#15 task 2)"
```

---

### Task 3: README

**Files:**
- Modify: `README.md` -- "As a herdr plugin" (lines ~45-58), the command
  surface bullets for `answer` (~179), `done` (~209), `unbind` (~210), and the
  "done and unbind are the destructive verbs" section (~353-358)

**Interfaces:** none; documentation of Tasks 1-2 and of the Go plan's flags.

- [ ] **Step 1: The plugin section**

In "As a herdr plugin", change the bullet list under "Either way you get:" to:

```markdown
- a `relay` overlay pane running `relay ui`, opened by the `open-ui` action
- three popup pickers -- `pick-done`, `pick-unbind`, `pick-answer` -- each
  an action that opens `relay <verb> --pick` in a popup: choose the binding
  from a list, and for `answer`, read the builder's dialog and type the
  answer there
- an `install-service` action that installs the binary to `~/.local/bin/relay`
  and registers the daemon with systemd or launchd
- a startup check that tells you if the reconciler is not running
```

Replace the keybinding example that follows ("Bind the reader to a key...")
with:

```markdown
Bind the reader and the pickers to keys in herdr's `config.toml`:

    [[keys.command]]
    key = "prefix+r"
    type = "plugin_action"
    command = "fuad-daoud.relay.open-ui"
    description = "open relay"

    [[keys.command]]
    key = "prefix+a"
    type = "plugin_action"
    command = "fuad-daoud.relay.pick-answer"
    description = "answer the blocked builder"

`pick-done` and `pick-unbind` bind the same way. A picker exits 0 when the
verb ran, 1 when you cancelled, nothing was listed, or the verb failed; the
result stays on screen until you press a key, then the popup closes.
```

- [ ] **Step 2: The command surface bullets**

`answer` bullet: after the sentence ending "...that is `herdr agent send-keys
<pane> <keys>`." (or wherever the bullet ends -- keep everything that is
there), append one sentence:

```markdown
  `--pick` instead of a name opens a popup-friendly picker: the blocked
  builders in a list (skipped when there is exactly one), the dialog text
  above, one input line below; a number is a `--choice`, `enter`/`esc`/`tab`/
  `up`/`down`/`space` are `--keys`, anything else is `--text`.
```

`done` bullet becomes:

```markdown
- `relay done NAME|--name N|--pick` — mark a binding done; relaying stops.
  `--pick` chooses from a list in the terminal.
```

`unbind` bullet becomes:

```markdown
- `relay unbind NAME|--name N|--pick [--archive]` — forget a binding, deleting its directory or
  packing it into `.archive/` first. `--pick` chooses from a list in the terminal.
```

- [ ] **Step 3: The destructive-verbs section**

Change the first paragraph of "done and unbind are the destructive verbs" to:

```markdown
`relay done` and `relay unbind` both require a binding name (`relay done ai`, or
`--name ai`) or `--pick`, which lists the bindings and runs the verb on the one
you choose. Neither resolves the current directory for you: a bare `relay done` once ended a live
loop by accident, and the recovery is `relay bind --resume --name <name>`.
`--pick` is explicit for the same reason -- a bare verb never opens a picker,
so the planner agent, whose pane is also a terminal, can never fall into one.
```

- [ ] **Step 4: Verify and commit**

Run: `make check`
Expected: green (README changes do not affect it; this confirms nothing else
was touched).

Run: `git diff --stat`
Expected: only `README.md`.

```bash
git add README.md
git commit -m "docs(readme): --pick on done/unbind/answer and the three plugin picker actions (#15 task 3)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), whether shellcheck ran, and `git diff --stat
main..HEAD`. If any step was impossible as written, say which and why -- do
not work around it.
