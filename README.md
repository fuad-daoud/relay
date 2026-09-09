# relay

[![ci](https://github.com/fuad-daoud/relay/actions/workflows/ci.yml/badge.svg)](https://github.com/fuad-daoud/relay/actions/workflows/ci.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

`relay` automates the plan/report handoff between two AI coding agent panes
running under [herdr](https://github.com/herdrdev/herdr), a terminal workspace
manager for coding agents. A human talks to a **planner** agent; the planner
hands work to a **builder** agent; relay moves the files between them so the
human never copy-pastes a plan or a report by hand.

Relay makes no judgements. It moves files, types prompts, and watches herdr's
live agent state — whether a report is good, whether a question needs a
human, whether the work is done, is a decision that stays with the planner
(or the human) at every step.

## Requirements

- **[herdr](https://github.com/herdrdev/herdr) 0.8.2 or newer, on `PATH`.**
  This is a hard runtime dependency, not an integration: herdr owns the panes,
  and every single thing relay observes or controls goes through the `herdr`
  CLI. relay is useless without it.
- **Two agent harnesses that herdr can drive** — one for the planner, one for
  the builder. relay ships example aliases for `opencode`, `claude` and `agy`;
  see [Builder aliases](#builder-aliases).
- **`git` on `PATH` (optional).** Required for automatic round diff capture; without it, relay works normally but rounds produce no diffs.
- **Linux or macOS.** See [Platform support](#platform-support).
- **Go 1.22+**, to build from source. Not needed if you install a release
  binary.

## Install

### As a herdr plugin

If you already run herdr 0.8.2 or newer, install relay as a plugin and skip the
manual binary and service setup:

    herdr plugin install fuad-daoud/relay

That downloads the release binary matching the plugin manifest and verifies its
checksum. To build from source instead, which needs a Go toolchain:

    herdr plugin install fuad-daoud/relay/from-source

Either way you get:

- a `relay` overlay pane running `relay ui`, opened by the `open-ui` action
- an `install-service` action that installs the binary to `~/.local/bin/relay`
  and registers the daemon with systemd or launchd
- a startup check that tells you if the reconciler is not running

Bind the reader to a key in herdr's `config.toml`:

    [[keys.command]]
    key = "prefix+r"
    type = "plugin_action"
    command = "fuad-daoud.relay.open-ui"
    description = "open relay"

herdr does not sandbox plugins, and its install preview lists the commands that
will run but not their contents. The scripts are `scripts/plugin-*.sh` in this
repository -- read them before installing.

Until you run `install-service`, the plugin's binary and any `relay` already on
your `PATH` are two binaries sharing one state directory. Running it makes the
plugin's binary the `PATH` binary and removes the skew.

Prebuilt binaries for Linux and macOS (amd64 and arm64) are attached to every
[release](https://github.com/fuad-daoud/relay/releases); unpack one and put
`relay` on your `PATH`.

With a Go toolchain:

```
go install github.com/fuad-daoud/relay/cmd/relay@latest
```

That drops `relay` in `$(go env GOPATH)/bin` — make sure it is on your `PATH`.

Or from a clone, which also stamps the binary with the current tag so
`relay version` is meaningful:

```
git clone https://github.com/fuad-daoud/relay
cd relay
make install        # builds and installs ~/.local/bin/relay
```

`make check` runs the full gate — `gofmt -l .`, `go vet ./...`, and
`go test -count=1 ./...` — and `make install` runs it first.

To run the reconciler as a background service, see
[Running the daemon](#running-the-daemon).

## First run on a clean machine

On a clean machine, set up prerequisites and preflight with `relay doctor`:

1. Install relay (see [Install](#install)).
2. Run `relay doctor` to check your environment:
   ```
   relay doctor
   ```
   Doctor inspects herdr, the background daemon, each harness binary on `PATH`, herdr integrations, and the builder role files.
3. Run the literal fix commands `relay doctor` prints for any missing items, such as installing a harness integration:
   ```
   herdr integration install claude
   ```
4. Emit the builder's `plan-executor` role definition directly into your harness's config directory:
   ```
   relay agent print --kind claude > ~/.claude/agents/plan-executor.md
   ```
   (For `opencode`, redirect to `~/.config/opencode/agents/plan-executor.md`. For `agy`, the role is selected by preamble on the first prompt, so no role file is needed.)
5. Re-run `relay doctor` to confirm `0 failures`.
6. Start the daemon (e.g. `relay daemon &` or `make service`).
7. Bind your first agent from inside a herdr planner pane:
   ```
   relay bind --builder cbuilder
   ```

## Quick start

From inside the planner's herdr pane, in the repository you want worked on:

```
relay bind --builder cbuilder     # split a builder pane and bind it to this tree
relay send --file plan.md         # hand it the plan; the builder starts working
relay status                      # watch the round
relay pull                        # print the report the builder wrote back
relay done <name>                 # stop relaying when you are satisfied
```

`relay bind` reads the planner's pane from `$HERDR_PANE_ID`, which herdr sets
inside every pane it manages, so it has to be run from inside one.

## Command surface

- `relay bind [--name N] [--builder ALIAS|PANE_ID] [--resume] [--tab] [--timeout D]`
  — start a binding between the calling planner pane (read from
  `$HERDR_PANE_ID`) and a builder. `--builder` is looked up as an alias unless
  it contains `:`, in which case it is treated as a herdr pane id and that pane
  is **adopted** instead of spawned. A name that already exists is refused
  rather than reused: only `bind.json` would be rewritten, so a fresh round 1
  would collide with the previous session's round log. `--resume --name N`
  re-points that existing binding's planner side at the calling pane without
  touching the builder; `relay unbind N` is the other way out.
- `relay send --file PATH [--name N]` — stage the file as the current round's
  plan and prompt the builder with it.
- `relay pull [--name N]` — print the newest pending payload to stdout and
  mark it delivered, without typing into any pane. This is the safe way for
  the planner to fetch a report mid-turn.
- `relay diff [--name N] [--round R] [--stat]` — print a round's captured patch
  to stdout, or its diffstat summary with `--stat`. Defaults to the newest
  completed round.
- `relay answer [--name N] (--keys K | --choice N | --text S)` — answer a
  builder that's blocked at a dialog, via `send-keys` rather than a typed
  prompt (herdr refuses `agent prompt` against a blocked agent).
- `relay status [--json]` — one row per binding: round, display state, both
  panes' live herdr status, the last relayed event, and anything pending.
- `relay log NAME` — the binding's append-only round log.
- `relay watch [--interval D]` — `status`, redrawn on a timer, default 2s.
- `relay ui [--interval D]` — interactive reader: report, terminal, diff and log tabs.
- `relay fork <source> --round R --new-name N [--builder ALIAS] [--tab] [--cwd DIR]` —
  branch a new binding from an earlier round of an existing binding, copying
  round history and artifacts through round R and launching a fresh builder in a
  dedicated git worktree (or in `--cwd`).
- `relay done NAME|--name N` — mark a binding done; relaying stops.
- `relay unbind NAME|--name N [--archive]` — forget a binding, deleting its directory or
  packing it into `.archive/` first.

- `relay gc [--dry-run] [--archive]` — clear every binding the planner marked
  `DONE`, in one pass.
- `relay daemon [--interval D]` — the long-running reconciler; this is what
  the service unit runs.
- `relay help` — the command list. `relay <command> -h` prints that command's
  flags.
- `relay version` — the build's version.

`--name` defaults to whichever binding owns the current working directory for
`send`, `pull`, `diff`, `answer` and `status`. It is **required** for `done` and
`unbind`: those are the destructive verbs and they refuse to guess (see below).

### Interactive reader: relay ui

`relay ui` is a full-screen terminal reader for live bindings. `relay watch`
remains the tool for shell pipes and scripts; `relay ui` is the interactive
sibling that lets you inspect substance instead of just state.

It is strictly **read-only**: it never mutates state, never types into panes,
and never appends to round logs. It holds the state lock only for the duration
of a read, exactly as `relay status` does.

Opening a binding displays four full-width tabs:
- **report** — the newest planner-bound report or question payload.
- **terminal** — recent live terminal output from the builder agent's pane.
- **diff** — the captured git patch from the newest completed round.
- **log** — the formatted append-only round log.

### Panes are yours, always

relay never opens, closes or kills a pane except the one builder pane it spawns
for you at `bind`. In particular:

- `done` and `unbind` leave the builder running. Its terminal is often the only
  record of *why* a round went wrong, and throwing that away automatically is
  worse than leaving a process up.
- a `BROKEN`, `ORPHANED` or timed-out binding is flagged and reported, never
  cleaned up. relay stops relaying and waits for you.
- closing builder panes when you are finished with them is a manual step, and
  worth remembering: an idle opencode builder holds roughly 800 MB.

### Where the builder appears

By default `relay bind` splits the planner's pane, so you can watch the builder
work beside you. `relay bind --tab` opens it in its own herdr tab instead —
the planner keeps full width, at the cost of not seeing the builder live.

### Round budget

Each round carries a budget; past it, relay flags the binding `NEEDS YOU` and
notifies once. It never kills anything — a builder working a real stage of a
plan runs for hours, so the budget is a runaway guard, not a progress estimate.
The default is 24 hours; `relay bind --timeout 2h` sets it per binding.

When a builder goes idle without writing its report file, relay nudges it once.
Relay abandons a round only when the builder's terminal has been still for the
grace period, falling back to a labeled scrape of the builder's terminal. If the
screen moves, the grace resets: a builder waiting on subagents is therefore no
longer mistaken for a finished one.

### Forking a binding

`relay fork` branches a new binding from an earlier round of an existing binding:

```
relay fork webshop --round 2 --new-name webshop-alt
```

- **What is copied:** Round history up through `--round`: `log.jsonl` entries (marked confirmed with no pending delivery) and all round artifacts (`NNN-plan.md`, `NNN-report.md`, `NNN-question.md`, `NNN-diff.patch`).
- **What is not copied:** Working tree code state is not rewound. By default, relay creates a fresh git worktree at `~/.local/state/relay/.worktrees/<new-name>` on a new branch `relay/<new-name>` cut from current `HEAD` of the source repository. Pass `--cwd DIR` to bind to an existing directory instead.
- **State layout:** Relay keeps worktrees it creates under `.worktrees/` directly beside `.archive/` in the state root (`~/.local/state/relay/.worktrees/`).
- **Teardown rule:** Relay removes a worktree it created only when it is clean (`git status` reports no untracked or uncommitted changes), and never removes the branch. If uncommitted edits remain or git is unavailable, `relay unbind` and `relay gc` leave the worktree untouched and report the exact command to inspect or remove it manually.


### Cleaning up finished bindings

A binding leaves `$XDG_STATE_HOME/relay/<name>/` behind (defaulting to
`~/.local/state/relay/<name>/`): `bind.json`, `log.jsonl`, and every round's
plan, report, patch (`NNN-diff.patch`) and captured dialog. `relay done` stops relaying but removes
nothing — the log is the record of what the planner actually told the builder.

```
relay unbind ai              # delete the binding and its whole directory
relay unbind ai --archive    # pack it into .archive/ai-<date>.tar.gz, keeping the log
relay gc --dry-run           # list every DONE binding that would be cleared
relay gc --archive           # archive them all in one go
```

Archives are gzipped tarballs under `~/.local/state/relay/.archive/`. A typical
eight-round binding compresses about 60x — a thousand of them is under 2 MB — so
archiving is effectively free. To read one back:

```
tar -xzf ~/.local/state/relay/.archive/ai-20260905-121500.tar.gz -O ai/log.jsonl
```

`gc` only touches bindings the planner marked `DONE`. A `BROKEN` or `ORPHANED`
one is left alone: it still needs a human, and clearing it would throw away the
state that explains why it stopped.

Snapshot tree objects created during round diff capture are written directly to
git's object database unreferenced. They never alter repository refs, branches,
or the working index, and they are reclaimed automatically by the repository's
own `git gc`.

Neither command closes a pane — the builder's terminal stays where it is, for
you to read and close yourself.

### done and unbind are the destructive verbs

`relay done` and `relay unbind` both require a binding name (`relay done ai`, or
`--name ai`). Neither resolves the current directory for you: a bare `relay done` once ended a live
loop by accident, and the recovery is `relay bind --resume --name <name>`.

## Builder aliases

An alias says how to start one builder: which herdr agent kind, and which
native arguments to launch it with. `relay bind --builder cbuilder` spawns the
`cbuilder` alias; there is no default, because guessing would silently start
the wrong (and possibly expensive) agent.

relay ships three aliases, and they are **worked examples, not a supported
set**:

| alias      | kind     | model                              | role selection |
|------------|----------|-------------------------------------|----------------|
| `builder`  | opencode | `openrouter/z-ai/glm-5.3-flash`     | `--agent plan-executor` |
| `cbuilder` | claude   | `sonnet`                            | `--agent plan-executor` |
| `abuilder` | agy      | `gemini-3.8-flash-high`             | preamble on the first prompt |

Each one assumes things about the machine relay runs on: that the harness is
installed, that its provider is configured for that model, and that a
`plan-executor` role definition exists in it. Relay can now emit that role
definition for you with `relay agent print --kind claude` or `--kind opencode`
(see [First run on a clean machine](#first-run-on-a-clean-machine)), while `agy`
selects its role via the preamble on the first prompt. Run `relay doctor` to
check which ones are installed.

> **Note on `abuilder`:** it passes `--dangerously-skip-permissions`, which
> lets the builder act without approval prompts. That is what makes an
> unattended relay loop work, and it is a real grant of trust. Keep it only for
> a working tree you are willing to let an agent edit freely.

Override or extend the table in `~/.config/relay/aliases.json`. Config lives in
`$XDG_CONFIG_HOME/relay` (default `~/.config/relay`). It is a JSON array
layered over the built-ins, so an entry reusing a built-in name replaces it:

```json
[
  {
    "name": "builder",
    "kind": "opencode",
    "args": ["--agent", "plan-executor", "-m", "anthropic/claude-sonnet-5"]
  },
  {
    "name": "local",
    "kind": "opencode",
    "args": ["-m", "ollama/qwen3-coder"],
    "preamble": "Act as a plan execution specialist. Implement exactly what the plan specifies."
  }
]
```

- `name` — what you pass to `relay bind --builder`. Required.
- `kind` — the herdr agent kind (`herdr agent start --kind`). Required.
- `args` — native arguments passed through to the harness.
- `preamble` — prepended to round 1's prompt only, for harnesses with no way to
  select a role at launch. `agy` has no `--agent` flag, which is why `abuilder`
  uses one.

An **adopted** pane (bind by pane id, or `--resume`) gets no preamble and needs
no alias at all: you launched that agent yourself, so it is already in whatever
role you put it in.

## Display states

`relay status` collapses the binding's internal state into four:

- **ACTIVE** — someone is working (planner or builder), nothing needs a human
  yet.
- **NEEDS YOU** — relay has stopped and a person must act. Covers a blocked
  builder (answer its dialog), a dead builder pane, a lost planner pane, a
  round that ran past its timeout, and a binding that hit its round cap.
- **HELD** — a payload is ready for the planner, but the planner pane is
  focused, so relay is holding it rather than typing into it.
- **DONE** — the planner declared the work verified via `relay done`, and
  relaying has stopped deliberately, not because anything went wrong: unlike
  NEEDS YOU, nothing needs a human here. `Reconcile` returns immediately for
  a done binding — no reports are queued, no dialogs captured, no timeouts
  flagged. The binding and its round log stay on disk (`relay log <name>`
  still works as an audit trail) until `relay unbind` removes them.

## The anti-clobber rule

`herdr agent prompt` types text into a pane and presses Enter. Since relay
cannot see what a human has half-typed, it treats a focused planner pane as
unsafe to inject into: it holds the payload and sends one herdr notification
(not one per tick) instead of typing over the human. As soon as the human's
focus moves to another pane, the daemon delivers the held payload on its next
tick. `relay pull` bypasses this entirely — it prints the payload to stdout
instead of injecting it, so it's safe to run from inside the focused planner
pane at any time.

## Running the daemon

`relay daemon` is the reconciler: it polls herdr, queues reports back to the
planner, captures blocking dialogs, and flags stalled rounds. Nothing else
needs it running — the CLI works on its own — but without it, reports are only
delivered when you run `relay pull` by hand.

You can just run `relay daemon` in any spare terminal. To have it start with
your session:

```
make service     # systemd user unit on Linux, LaunchAgent on macOS
make uninstall   # stop it and remove both the binary and the unit
```

On Linux that installs `dist/relay.service` to
`~/.config/systemd/user/relay.service` and enables it. On macOS it renders
`dist/com.github.fuad-daoud.relay.plist.in` into `~/Library/LaunchAgents/` and
loads it, logging to `~/Library/Logs/relay.log`.

Both run `relay daemon` outside any herdr-managed pane, so it starts with none
of the `HERDR_*` environment variables herdr injects into a pane it manages.
The cheap way to confirm this is fine before trusting the service: if
`relay status` works from a plain terminal (not inside a herdr pane), the
daemon will work there too, since both resolve the running herdr session the
same way.

Only one daemon runs at a time. `relay daemon` takes an exclusive lock on
`$XDG_STATE_HOME/relay/.daemon.lock` and refuses to start if another one holds
it, so starting a second by hand next to the service is an error rather than
two reconcilers racing. `relay daemon --check` exits 0 if a daemon is running
and 1 if not, printing nothing.

## Lifecycle hooks

relay supports user-defined hook scripts dispatched during binding lifecycle events. When state changes or a new round begins, `relay daemon` executes scripts located in `~/.config/relay/hooks/<event_type>.d/`. Config lives in `$XDG_CONFIG_HOME/relay` (default `~/.config/relay`).

### Supported events

- `state_changed` (`~/.config/relay/hooks/state_changed.d/`) — fires whenever a binding transitions between states (`ACTIVE`, `NEEDS YOU`, `HELD`, `DONE`, `BROKEN`, `ORPHANED`).
- `round_started` (`~/.config/relay/hooks/round_started.d/`) — fires whenever a new round starts.

### Hook execution & environment

Each hook script is executed asynchronously in a detached process with a 10-second timeout. relay injects the following environment variables:

- `RELAY_EVENT`: The event type name (`state_changed`, `round_started`).
- `RELAY_BINDING`: The name of the binding.
- `RELAY_STATE`: The current state of the binding.
- `RELAY_OLD_STATE`: The previous state of the binding.
- `RELAY_ROUND`: The current round number.

Hook stdout, stderr, and execution failures are logged to `~/.local/state/relay/hooks.log` (or `$XDG_STATE_HOME/relay/hooks.log`).

Scripts must have their executable bit set (`chmod +x`). If `~/.config/relay/hooks/` or an event directory does not exist, event dispatch is a silent no-op.

## Setting up your agent harnesses

### herdr lifecycle integrations

relay reads lifecycle state (`idle` / `working` / `blocked` / `done` /
`unknown`) from herdr, which learns it from a hook each harness installs. Check
what herdr can install with `herdr integration install --help`; the hooks land
in the harness's own config directory, for example:

```
opencode   ~/.config/opencode/plugins/herdr-agent-state.js
claude     ~/.claude/hooks/herdr-agent-state.sh
agy        ~/.gemini/config/hooks/herdr-agent-state.sh
```

Without a harness's integration, herdr falls back to heuristic screen
detection and reports `unknown`. relay never treats `unknown` as done, so such
a binding stalls rather than misbehaving — but it does stall. A binding whose
builder reports no session id also never self-heals from `BROKEN`, since
recovery requires matching the same herdr session (see below).

### opencode permission allowlist

relay stages plans and reports under `~/.local/state/relay/<binding>/`, outside
the repo the builder is working in, so a fresh opencode builder blocks on an
"Access external directory" dialog on its first round. relay handles it — the
daemon captures the dialog and the planner answers with `relay answer` — but to
skip it entirely, add this to `~/.config/opencode/opencode.jsonc`:

```jsonc
"permission": {
  "external_directory": {
    "/home/you/.local/state/relay/*": "allow",
    "/home/you/.local/state/relay/**": "allow"
  }
}
```

Substitute your real home directory: opencode does not expand `~` or `$HOME`
in these patterns. Claude builders (`cbuilder`) have their own permission model
and are not covered by that entry.

## Recovering a broken binding

If relay cannot find the builder — a detection flicker, a restarted agent — the
binding goes `BROKEN` and relaying stops. It clears itself only when the **same
herdr session id** reappears, never on a pane-id match alone: a pane you closed
and reused for something else must never start receiving plans meant for a
builder. Spawned builders record their session id at `bind` on a best-effort
basis, so a harness reporting none simply never self-heals, which is the safe
direction.

If the builder is gone, point the binding at a new builder:

```bash
relay bind --resume --name N --builder abuilder     # spawn a fresh builder
relay bind --resume --name N --builder w2:p4        # adopt an existing pane
```

The binding keeps its name, round number, round log, working directory, and diff
baseline. Because the replacement builder is a new session that has not seen the
alias preamble, relay re-sends the preamble on the next prompt even after round 1.
Relay does not automatically re-send the current plan: it prints the `relay send`
command pointing at the staged plan so you can hand over the round when ready.

If only the planner moved or restarted, `relay bind --resume --name N` re-points
the planner without touching the builder. If you want to start over from scratch,
use `relay unbind N` and bind fresh.

## Platform support

**Linux and macOS.** Both are exercised in CI, on the Go 1.22 floor and on
current stable.

Windows is not supported. The blocker is not really relay — state locking is
behind a build tag and could be implemented there — but herdr, which relay
cannot work without. The tree still cross-compiles for `windows/amd64` (CI
checks it), and relay will refuse at runtime with a clear error rather than
running without a state lock.

## Design

[`docs/design.md`](docs/design.md) is the architecture document written before
relay was built. It explains why the CLI and the daemon are split, why delivery
holds on a focused pane, and what was deliberately left out. It is a historical
record, not maintained against the code.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: open an issue first, keep it
stdlib-only, write the test, and make sure `make check` passes.

## License

[MIT](LICENSE) © Fuad Daoud
