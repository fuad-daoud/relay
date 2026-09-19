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
  the builder. relay knows how to start `opencode`, `claude`, `agy` and `codex`; you tell it
  which models in [Candidates](#candidates).
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
- three popup pickers -- `pick-done`, `pick-unbind`, `pick-answer` -- each
  an action that opens `relay <verb> --pick` in a popup: choose the binding
  from a list, and for `answer`, read the builder's dialog and type the
  answer there
- an `install-service` action that installs the binary to `~/.local/bin/relay`
  and registers the daemon with systemd or launchd
- a startup check that tells you if the reconciler is not running

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
4. Install the role definitions into each harness on `PATH` (the plugin
   does this for you at install and update):
   ```
   relay agent install
   ```
   One line per file says `wrote`, `kept (identical)` or `kept (differs;
   --force to overwrite)`. Pass `--kind` to name a harness that is not on
   `PATH` yet, `--role` for one definition, `--dry-run` to look first.
   This writes `plan-executor`, `researcher`, `reviewer` and `architect`
   for every kind; `relay agent print --kind <k> --role <r>` still emits
   one to stdout.

   `researcher` is the read-only role the builder's own sub-agents run as. It
   exists because exactly one agent may write to a working tree: research can fan
   out safely, implementation cannot. The claude and opencode definitions pin a
   `model:` in their front matter as a worked example, chosen so neither needs a
   provider the rest of relay does not already assume; that line is the first
   thing to change for your own setup, and a plain `relay agent install`
   keeps your edit. The agy definitions pin `model: inherit`
   and that is not an example: on agy the key is a tier (`inherit`, `flash`,
   `pro`) that would override the `--model` relay passes at launch. `relay
   doctor` reports the pin each installed definition carries, warns when an
   agy copy pins a tier or differs from what relay ships, and names the
   `relay agent install ... --force` that restores it.

   codex roles are TOML profiles at `~/.codex/<role>.config.toml` selected
   with `-p`; the researcher profile pins `gpt-5.6-luna` at `medium` for
   every codex builder's research sub-agents and `relay doctor` warns when
   that pin drifts. Pane builders also need `herdr integration install
   codex`.
5. Write `~/.config/relay/candidates.json` (see [Candidates](#candidates)) and check it with `relay candidates`.
6. If more than one candidate serves `builder`, write `~/.config/relay/policy.json`
   with the order to try them in (see [Policy](#policy)); `relay policy` shows
   what relay would pick and says `would refuse` until you do. `relay doctor`
   warns about this too and prints a starter file built from your candidates.
7. Re-run `relay doctor` to confirm `0 failures`.
8. Start the daemon (e.g. `relay daemon &` or `make service`).
9. Bind your first agent from inside a herdr planner pane:
   ```
   relay bind --builder claude/anthropic/sonnet
   ```
   (or, with one candidate, `relay bind`).

## Quick start

From inside the planner's herdr pane, in the repository you want worked on:

```
relay bind --builder claude/anthropic/sonnet     # open a builder tab and bind it to this tree
relay send --file plan.md         # hand it the plan; the builder starts working
relay status                      # watch the round
relay wait                        # block until the round closes or needs you
relay pull                        # print the report the builder wrote back
relay done <name>                 # stop relaying when you are satisfied
```

The builder writes `NNN-report.md` when it has finished and then creates an
empty `NNN-done` as its last action; relay closes the round on that marker.
A builder that goes idle without the marker is nudged once, then its report
is delivered flagged `unmarked` (or its terminal scraped if there is no
report at all).

`relay bind` reads the planner's pane from `$HERDR_PANE_ID`, which herdr sets
inside every pane it manages, so it has to be run from inside one.

## Command surface

- `relay bind [--name N] [--builder CANDIDATE|PANE_ID] [--headless] [--resume [--rebind]] [--timeout D]`
  — start a binding between the calling planner pane (read from
  `$HERDR_PANE_ID`) and a builder. `--builder` is a candidate token unless
  it contains `:` and no `/`, in which case it is treated as a herdr pane id and that pane
  is **adopted** instead of spawned. A name that already exists is refused
  rather than reused: only `bind.json` would be rewritten, so a fresh round 1
  would collide with the previous session's round log. `--resume --name N`
  re-points that existing binding's planner side at the calling pane without
  touching the builder; `relay unbind N` is the other way out.
  `--headless` makes the builder a process relay runs itself, one fresh
  process per round, instead of a pane it watches (see "Headless builders"
  below). It cannot adopt a pane and cannot be added to an existing binding
  with `--resume`: a binding's shape is fixed when it is created.
- `relay send [NAME|--name N] --file PATH` — stage the file as the current round's
  plan and hand it to the builder: typed into its pane, or, for a headless
  binding, as the prompt of a fresh process started in the binding's tree.
  A headless binding whose previous round's process is still running refuses
  the send; wait for its report or `relay done` it.
- `relay pull [NAME|--name N]` — print the newest pending payload to stdout and
  mark it delivered, without typing into any pane. This is the safe way for
  the planner to fetch a report mid-turn.
- `relay diff [NAME|--name N] [--round R] [--stat] [--drift]` — print a round's
  captured patch to stdout, or its diffstat summary with `--stat`. Pass `--drift`
  to inspect between-rounds drift instead of the round's diff; `--drift` composes
  with `--stat` and `--round`, and defaults to the currently open round where plain
  `relay diff` defaults to the newest completed one.
- `relay answer NAME|--name N (--keys K | --choice N | --text S)` — answer a
  builder that's blocked at a dialog, via `send-keys` rather than a typed
  prompt (herdr refuses `agent prompt` against a blocked agent). The binding
  is **required**: answering types a key into a live dialog, so relay will not
  guess which builder you meant. relay also re-checks that herdr still reports
  the builder `blocked` and refuses otherwise, because herdr's screen detection
  can false-positive and the gap between the notice and your answer is
  unbounded. If you genuinely mean to type into a running agent, that is
  `herdr agent send-keys <pane> <keys>`, not relay.
  `--pick` instead of a name opens a popup-friendly picker: the blocked
  builders in a list (skipped when there is exactly one), the dialog text
  above, one input line below; a number is a `--choice`, `enter`/`esc`/`tab`/
  `up`/`down`/`space` are `--keys`, anything else is `--text`.
  A headless builder takes no dialogs; `answer` is refused and points at the
  round's log.
- `relay status [NAME|--name N] [--json] [--all]` — one row per binding: round, display state, both
  panes' live herdr status, the last relayed event, anything pending, and for a nudged builder how long its terminal has been quiet against the grace after which relay scrapes it. Naming a binding shows only that one. Bindings marked DONE are hidden by default and the footer names how many are hidden.
  `--json` also carries two fields the prose above does not spell out:
  ```
  branch      the binding's worktree branch; absent for a --cwd binding
  waiting     set when the binding is stalled on a human: cause, line, since, hint
  ```
- `relay log NAME` — the binding's append-only round log. `late` on an entry means herdr reported the prompt stalled but the screen showed it had landed, so it was not re-sent.
- `relay tab [--since 7d|24h|2026-09-01] [--by binding|model|provider] [--json]` —
  tokens and cost across bindings, archived ones included.
- `relay wait [NAME|--name N] [--any N1 N2 ...] [--round R] [--timeout D]` — block
  until the round closes or the binding needs you, reading relay's own state only
  (never herdr). Exit 0: closed on the marker, stdout is the report path. 2: closed
  without it (`unmarked`, `scraped`, `noreport` — verify before trusting), report
  path or `-`. 3: needs you, stdout is one line saying what it is waiting on. 4:
  the binding is DONE or was unbound. 5: closed, but the builder's report says
  halted or blocked -- read it before sending again; stdout is the report path.
  124: `--timeout` (default 10m) elapsed. `--any` waits on several and prints the
  winner's name first. A pane planner that does not want the report typed afterwards
  runs `relay wait N && relay pull N`.
- `relay ui [--interval D]` — interactive reader: at 110 columns or more, a rail
  of bindings grouped by state beside a pane showing the selected binding's
  report, terminal, diff or log; narrower terminals get the list-then-detail
  flow.
- `relay add --name N [--builder CANDIDATE] [--headless] [--cwd DIR]` — attach an
  additional builder to this planner on its own git worktree, starting at
  round 1. This is how one planner drives several builders at once.
  `--headless` applies as for `bind`.
  `relay add --name N --server S [--base REF]` runs that builder on a
  configured remote server instead (see "Remote builders: the client" below);
  `--cwd` cannot be combined with `--server`.
- `relay fork <source> --round R --new-name N [--builder CANDIDATE] [--headless] [--cwd DIR]` —
  branch a new binding from an earlier round of an existing binding, copying
  round history and artifacts through round R and launching a fresh builder in a
  dedicated git worktree (or in `--cwd`).
- `relay candidates` — list the configured candidates and the roles each serves.
- `relay policy` — show, per role, the candidates in the order relay would
  try them, which one it would pick right now, and any gap between
  `policy.json` and `candidates.json`.
- `relay unavailable <harness/provider/model> [--for D] [--reason S]` — record
  that a candidate's provider is rate-limited; gates every candidate on that
  provider until `--for` elapses, or until `relay available` clears it.
- `relay available <provider|harness/provider/model>` — clear a recorded rate
  limit on a provider.
- `relay done NAME|--name N|--pick` — mark a binding done; relaying stops.
  `--pick` chooses from a list in the terminal.
- `relay unbind NAME|--name N|--pick [--archive]` — forget a binding, deleting its directory or
  packing it into `.archive/` first. `--pick` chooses from a list in the terminal.

- `relay gc [--dry-run] [--delete]` — clear every binding the planner marked
  `DONE`, in one pass. Archives by default; pass `--delete` to remove each binding's directory instead (`relay gc --archive` is accepted as a no-op).
- `relay daemon [--interval D] [--held-grace D]` — the long-running reconciler; this is what
  the service unit runs. A held payload is injected into a focused planner once its input
  box is empty or its screen has been quiet for `--held-grace` (default 60s).
- `relay serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N]` — run the remote-builder server (listener + daemon).
- `relay serve init|enroll|clients|revoke|fingerprint|status|gc|unbind` — server administration, on the server host.
- `relay client init` — generate this machine's remote-builder identity (an
  ed25519 keypair); prints the enrollment line a server admin runs
  `relay serve enroll --key "<line>"` with.
- `relay client add-server NAME URL (--fingerprint sha256:HEX | --ca system | --insecure)` —
  record a remote server in `servers.json`; with `--fingerprint`, checks
  enrollment once.
- `relay client rm-server NAME` — forget a configured server; refused while
  any binding still names it.
- `relay servers` — one row per configured server: name, url, and this
  client's enrollment on it.
- `relay help` — the command list. `relay <command> -h` prints that command's
  flags.
- `relay version` — the build's version.

Every binding-scoped command takes its binding either positionally or as
`--name`; naming it both ways at once is refused. `send`, `pull` and `diff`
fall back to whichever binding owns the current working directory, and a bare
`relay status` lists them all. Naming one is **required** for `answer`, `done`
and `unbind`: those act on a specific loop — `answer` types into a live dialog,
the other two end one — and they refuse to guess (see below).

### The report block

The builder ends its report with a fenced `relay` block:

```relay
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # commands you ran, e.g. ["make check"]
not_done: []            # adjacent work you deliberately left
```

The four valid statuses are `done`, `halted`, `blocked`, and `deferred`. A missing or malformed block closes the round as `unstructured` without error or refusal. When `changed_paths` does not match git's count of changed files, relay notes the discrepancy as `paths: report N, diff M` on the diff entry.

### Interactive reader: relay ui

`relay ui` is a full-screen terminal reader for live bindings. `relay status`
remains the tool for shell pipes and scripts (`watch -n2 relay status` for a
ticker); `relay ui` is the interactive sibling that lets you inspect substance
instead of just state.

It is strictly **read-only**: it never mutates state, never types into panes,
and never appends to round logs. It holds the state lock only for the duration
of a read, exactly as `relay status` does.

At 110 columns or more, a rail of bindings grouped by state sits beside a
pane showing the selected binding's report, terminal, diff or log (`⏎`
focuses the pane, `s` toggles attention and name order); narrower
terminals get the list-then-detail flow. The pane's four tabs:
- **report** — the newest planner-bound report or question payload.
- **terminal** — recent live terminal output from the builder agent's pane.
- **diff** — the captured git patch from the newest completed round.
- **log** — the formatted append-only round log.

### Panes are yours, always

relay never opens, closes or kills a pane except the one builder pane it spawns
for you at `bind`. The one process it stops is a *headless* builder it started
itself (below). For pane builders:

- `done` and `unbind` leave the builder running. Its terminal is often the only
  record of *why* a round went wrong, and throwing that away automatically is
  worse than leaving a process up.
- a `BROKEN`, `ORPHANED` or timed-out binding is flagged and reported, never
  cleaned up. relay stops relaying and waits for you.
- closing builder panes when you are finished with them is a manual step, and
  worth remembering: an idle opencode builder holds roughly 800 MB.

### Where the builder appears

Every agent relay spawns as a pane -- builders from `bind`, `add`, `fork` and
consults from `ask` -- opens in its own herdr tab in the planner's workspace,
labelled with the agent's name, without moving focus. There is no split option:
side-by-side panes stop being readable at two or three builders, and tabs scale.

### Headless builders

`relay bind --headless` (also `add --headless`, `fork --headless`) makes the
builder a process instead of a pane. Nothing is opened at bind. Each
`relay send` starts the harness's non-interactive form -- `agy -p …`,
`claude -p …`, `opencode run …` -- in the binding's tree with the same prompt a
pane builder would be typed, writes the harness's streamed JSON events to
`~/.local/state/relay/<name>/NNN-builder.jsonl` and its stderr to
`NNN-builder.log`, both beside the round's plan and report, and returns.
The daemon renders the stream into the `.log` as it grows -- one line per
tool call (`● Bash go test ./...`), its result with the first line of what it printed (`  ⎿ ok: ok  github.com/… 0.4s`, `  ⎿ error: …`),
the builder's text, any denied permission, and the final answer -- so
`relay ui`'s terminal tab, `relay status` and `tail -f` on the `.log` show
the round live, about two seconds behind. Between rounds the tab keeps
the last round's log. The `.jsonl` is the raw record;
relay never reads it for meaning. When relay itself stops or replaces that process -- a
mid-round switch, `relay done`, `relay unbind` -- it appends one line to the
same log saying so (`--- relay 23:13:51: switched to claude/anthropic/sonnet (rate-limited …) ---`),
so two builders' output in one round is never ambiguous. The process exits when
it has written the report, or when
it fails; between rounds a headless binding has no process and is idle, not
broken. The completion marker is the contract: a process that wrote its report and
created `NNN-done`, then exited non-zero, has done its job. A process that
exits with a report but no marker closes the round too, flagged `unmarked`;
one that exits with neither is the "exited without a report" case below.

What is different from a pane builder:

- **No dialogs.** The process runs with stdin closed. `relay answer` is refused.
  If a harness needs permission prompts answered, use a pane builder or its
  `--dangerously-skip-permissions`/`--auto` extra arg in `candidates.json`.
- **No memory across rounds.** Every round is a fresh process. relay plans
  already carry their own context (worktree table, conventions, "stop rather
  than improvise"); headless makes that a hard requirement.
- **`relay status`** shows `builder  headless  <kind>  <idle|working|exited N>
  pid P since HH:MM` and the log's last three lines as `log` rows.
  `relay ui`'s terminal tab shows the log file.
- **Exit without a report** is logged as an `exit` entry (exit code and the
  log's last 20 lines) and the daemon switches builders, up to `max_switches`
  (a switch caused by a rate-limit gate is not counted), exactly as a
  vanished pane does; then `NEEDS YOU`.
- **`done` and `unbind` stop the process** if a round is running. A stop that
  fails is reported, and the binding is still done or unbound. The round budget
  never kills anything, for headless as for panes: it flags `NEEDS YOU` and
  leaves the process alone.
- **`relay unavailable`** on the provider mid-round kills the running process
  and starts the next candidate on the same round.

When a round stops -- exit without the marker, a quiescent pane, or the
round budget -- relay scans the builder's last output for the harness's
rate-limit text and records a `rate_limited` gate (`source relay`, the
matched line) on a match, parsing the line's own reset time when it names
one (`Resets in 2h48m52s`, `resets 7pm`) or using `limit_gate_default_ms`
otherwise, then switches uncounted toward `max_switches`. `relay
unavailable` still overrides; `relay available` undoes a false positive.

### Remote builders: the server

`relay serve` runs a remote-builder server: an HTTPS listener over enrolled clients and an autonomous daemon loop that runs headless builders on the server host without a local planner or GUI panes.

On a fresh server host, the first run looks like:
1. `relay serve init --host <hostname>` generates a server private key and self-signed certificate, printing the SHA-256 fingerprint that clients pin.
2. Copy the fingerprint to share with clients.
3. Enrol each client's public key: `relay serve enroll --label <client-label> --key "<public key line>"`.
4. Copy the service unit to `~/.config/systemd/user/relay-serve.service` and enable it:
   ```
   cp dist/relay-serve.service ~/.config/systemd/user/
   systemctl --user daemon-reload
   systemctl --user enable --now relay-serve
   ```

On the server machine, the admin can inspect enrolled clients and all owners' active bindings:
- `relay serve status` displays active bindings across all owners, sorted by owner label.
- `relay serve clients` lists enrolled clients and their revocation status.
- `relay serve gc --abandoned <duration>` prunes abandoned bindings whose last activity is older than the threshold by archiving them (running rounds are never touched).

What `relay serve` does not do: it runs no planner, opens no tmux/herdr panes, and provides no administrative verbs over the network (administration happens on the server host). Tenants are protected from each other over the wire and from a passive network, but not from the server admin or from each other at the OS level where all builders run under the same unix user. Tenant isolation by unix user or container is tracked in #204.

### Remote builders: the client

A remote binding is an ordinary binding whose builder runs on someone else's
machine, over a signed, pinned HTTPS connection instead of a local pane or
process. It has no worktree and no pane of its own: `relay send` ships a
bundle of your branch's history instead of typing into a terminal, and the
daemon polls the server for the round's state the same way it polls a pane.

Set up once per machine:
1. `relay client init` generates this client's ed25519 keypair (in
   `~/.config/relay/client.key` / `.pub`) and prints two lines: the client's
   id, and an enrollment line (`ed25519 <base64> <user>@<host>`) to hand the
   server admin.
2. The admin runs `relay serve enroll --label <you> --key "<enrollment
   line>"` on the server, and shares back that server's certificate
   fingerprint (printed by `relay serve init` there).
3. `relay client add-server <name> <url> --fingerprint sha256:<hex>` records
   the server in `~/.config/relay/servers.json` and, having a fingerprint to
   pin the connection with, checks enrollment immediately: "enrolled as
   `<label>`", or "not enrolled on `<name>`: give the admin: `<enrollment
   line>`" if step 2 has not happened yet. `--ca system` trusts the system CA
   pool instead of pinning a fingerprint; `--insecure` allows plain HTTP, for
   a server reachable only over an already-trusted tunnel.
4. `relay servers` lists every configured server and this client's
   enrollment on each: `enrolled as <label>`, `not enrolled`, `unreachable`,
   or `cert changed` (the pinned fingerprint no longer matches -- a hard
   refusal the client never overrides silently).

Then, from any repository:
```
relay add --name api --server zen         # creates the server binding and
                                           # the local branch relay/api, no worktree
relay send --name api --file plan.md      # ships plan.md and a bundle of relay/api
relay status                              # round state comes from the server, polled
```

What comes back as `relay/<name>`: the result of a closed round is fetched
into your repository's own `refs/heads/relay/<name>` branch -- fast-forward
only, exactly like a pane builder's worktree branch. If the round closed with
uncommitted changes on the server, they land on a side ref,
`refs/relay/<name>/round-<N>`, whose parent is that round's commit on
`relay/<name>`; the report names it. If that fast-forward collides with a
branch you have checked out locally, relay retries quietly next tick --
check out something else, then `relay pull`.

What is refused: `--cwd` cannot be combined with `--server` (a remote binding
is add-only, never bound to an existing directory); `relay answer` ("remote
builders take no dialogs" -- there is no pane to send keys into); `relay ask`
("consults are local-only"); `relay fork` from a remote source ("fork across
servers is not supported"); and `relay bind --resume --rebind` (or
`--builder`/`--headless`) against a remote binding ("cannot change a remote
builder; unbind and add" -- a binding's mode is fixed at creation, the same
rule a headless binding follows). `relay done` and `relay unbind` tell the
server first, and only change anything locally once it agrees (a 404 from
the server is treated as already gone, and proceeds).

What `status` and `doctor` show: `relay status` and `relay ui` name a
remote binding's builder by its server (`zen`, not a pane id), with the
last round state the daemon observed there (`running`, `idle`, `closed`,
`needs_you`, `unreachable`, `cert`) in the status column -- read from the
store, never over the network, so it costs nothing extra. `relay status`,
`relay pull` and each `relay wait` poll additionally sync every remote
binding first, so a round the server closed while your daemon was not
running (or was never started) is collected without it -- a laptop closed
overnight still shows the finished round on the next `relay status`.
`relay doctor` adds one row per configured server: reachable and enrolled
(`enrolled as <label>`), not yet enrolled (with the line to give the
admin), unreachable, or a certificate that no longer matches the pinned
fingerprint.

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

### Running several builders at once

`relay bind` gives the planner one builder over the current tree. `relay add`
attaches more, each on its own git worktree, so they never contend for files:

```
relay bind --builder claude/anthropic/sonnet --name api
relay add  --name frontend --builder claude/anthropic/sonnet
relay add  --name backend  --builder opencode/openrouter/z-ai/glm-5.3-flash

relay send --name frontend --file ui_plan.md
relay send --name backend  --file api_plan.md
```

Each peer is an ordinary binding: its own round counter, round log, captured
diffs and budget. `relay status` lists them all, and every verb that acts on a
binding takes `--name`.

Every mutating verb (`bind`, `add`, `fork`, `send`, `answer`, `done`, `unbind`)
ends by listing, on stderr, every *other* binding that is waiting on a human —
a blocked dialog, a halt, a dead builder, a lost planner — with how long and
the verb that resolves it, e.g. `waiting on you: api round 4 blocked 23m --
Do you want to proceed? > 1. Yes  (relay answer --name api)`. The exit code is
unchanged; it is a reminder, not a refusal.

Relay does not sequence them and does not merge their trees. The planner
decides how many builders it needs, which run in parallel and which wait, and
integrates the results — relay only carries plans out and reports back.

**`relay add` is not `relay fork`.** A fork continues a timeline: it copies
round history through a chosen round and starts at the round after it. A peer
starts at round 1 with an empty log, because it is not a continuation of
anything.

Headless builders are the cheap way to run several: no tab per builder, no
idle harness holding memory. `relay add --name api --headless` gives a peer its
own worktree and no pane.

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
plan, report, patch (`NNN-diff.patch`) and captured dialog. `relay done` stops relaying and, when the binding's worktree is clean and
no round is open, removes the worktree so its branch can be checked out
in the main repo (`removed worktree ... (branch relay/x is free to check
out)`); a dirty tree or an open pane round is kept and `relay gc` retries
when it is clean. The binding directory itself is never removed by `done`
— the log is the record of what the planner actually told the builder.
`relay bind --resume <name>` puts a removed worktree back on the same
branch at the same path; a DONE binding may then be rebound with
`--rebind`, since the old builder pane cannot work in the recreated
directory (`bind` names it so you can close it).

```
relay unbind ai              # delete the binding and its whole directory
relay unbind ai --archive    # pack it into .archive/ai-<date>.tar.gz, keeping the log
relay gc                     # archive every DONE binding into .archive/
relay gc --delete            # remove them instead
```

Archives are gzipped tarballs under `~/.local/state/relay/.archive/`. Archiving is the default because every other destruction decision in relay keeps by default. A typical
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
`--name ai`) or `--pick`, which lists the bindings and runs the verb on the one
you choose. Neither resolves the current directory for you: a bare `relay done` once ended a live
loop by accident, and the recovery is `relay bind --resume --name <name>`.
`--pick` is explicit for the same reason -- a bare verb never opens a picker,
so the planner agent, whose pane is also a terminal, can never fall into one.
And because a popup takes focus the instant it opens, `Enter` on a binding
that is not `DONE` asks first -- `mark webshop done? it is ACTIVE in round 5`
-- and only `y` proceeds; any other key returns to the list.

## Status line

`relay statusline` shows this planner's live bindings, one row each, under
the Claude Code prompt; it shows nothing outside herdr, nothing on error, and
never probes herdr.

Add this to `~/.claude/settings.json`:

```json
"statusLine": { "type": "command", "command": "relay statusline", "refreshInterval": 1 }
```

Two preconditions: `relay` must be on the `PATH` of the Claude Code process,
and Claude Code must be started inside a herdr pane, so `HERDR_PANE_ID` is
inherited.

Claude Code renders a few cells less than `COLUMNS`; relay subtracts 4 by
default (measured on the fullscreen TUI). If the right-hand `age · STATE`
cell is clipped or sits short of the edge, measure yours and set
`RELAY_STATUSLINE_MARGIN` in the environment Claude Code starts from. To
measure, put this in `statusLine.command` for one refresh and count the
cells before Claude Code's `…`:

    sh -c 'printf "%s" "$(seq -s . 1 $COLUMNS | cut -c1-$COLUMNS)"'

Each row is `○ name  rN · builder · what relay is waiting on  …  age · STATE`,
where `builder` is the harness segment of the candidate token, and `age` is
time since the last plan, report, question or answer crossed.

## Candidates

A candidate is one way to fill a role, named by the token `harness/provider/model`. `harness` and `provider` are single segments; `model` is the rest, so `opencode/openrouter/z-ai/glm-5.3-flash` is one token. **relay ships no candidates**: which model you are entitled to run is a fact about your accounts, not about relay.

Candidates are configured in `$XDG_CONFIG_HOME/relay/candidates.json` (default `~/.config/relay/candidates.json`), a JSON array:

```json
[
  {
    "harness":  "claude",
    "provider": "anthropic",
    "model":    "sonnet",
    "roles":    ["builder", "reviewer"]
  },
  {
    "harness":  "opencode",
    "provider": "openrouter",
    "model":    "z-ai/glm-5.3-flash",
    "roles":    ["builder"],
    "extra_args": ["--auto"]
  },
  {
    "harness":    "agy",
    "provider":   "google",
    "model":      "gemini-3.8-flash-high",
    "roles":      ["builder"],
    "extra_args": ["--dangerously-skip-permissions"]
  }
]
```

- `harness` — a kind relay knows: `agy`, `claude`, `opencode`, `codex`. Required.
- `provider` — who enforces the quota; free text. Required.
- `model` — passed to the harness as-is. Required.
- `roles` — non-empty list of roles from `builder`, `reviewer`, `researcher`. Required.
- `tree` — `binding` (the default) or `none` (which `relay ask` refuses today).
- `extra_args` — appended verbatim after what relay renders.
- `tier` — default permission tier for this candidate: `harness`, `read`, `edit`, `yolo`. Optional; defaults to role default in policy, else `harness`.
- `denial_patterns` — regexes that replace the harness default denial patterns for this candidate when detecting permission-blocked exits. Optional.
- `limit_patterns` — extra regexes, appended to the harness defaults, for the text this candidate's provider prints when it closes a session on quota. Extend-only; a default that misfires is a bug to report.
- `dialog_patterns` — extra regexes appended to the harness defaults, matched against the builder's visible screen only when herdr reports its status as `unknown`; a match refuses `send` as blocked; extend-only.

A file that does not validate stops every relay command with a message naming the entry; a missing file is zero candidates.

| Name | Shape | Definition |
| --- | --- | --- |
| `builder` | builder | `plan-executor` |
| `reviewer` | consult | `reviewer` |
| `researcher` | consult | `researcher` |

A role is relay's name for a job; the harness definition it selects is what `relay agent print` emits.

### How relay launches one

| kind | args |
| --- | --- |
| `agy` | `--model <model> --agent <role.Definition>` |
| `claude` | `--model <model> --agent <role.Definition>` |
| `opencode` | `--agent <role.Definition> -m <provider>/<model>` |
| `codex` | `-p <role.Definition> -m <id> -c model_provider=<provider> [-c model_reasoning_effort=<effort>]` |

For `codex` the candidate's `model` is `<id>[:<effort>]`: `gpt-5.6-terra:high` runs `-m gpt-5.6-terra -c model_reasoning_effort=high`, and the suffix stays in the token so two efforts are two candidates.

Any `extra_args` are appended verbatim after what relay renders. Because relay renders the argv, the token in `relay status` is exactly what was started.

> **Note on `--dangerously-skip-permissions`:** it lets the builder act without approval prompts, which makes an unattended relay loop work, but it is a real grant of trust. It is an `extra_args` entry you add once you have watched a few rounds and trust the loop with that tree; relay never adds it.

### Choosing a candidate

Pass the token to `relay bind --builder claude/anthropic/sonnet` and relay
starts exactly that, gated or not (with a `note:` on stderr if it is).

With `--builder` omitted, relay decides, by one rule:

- exactly one configured candidate serves the role → that one, unless it
  is gated;
- several serve it and `policy.json` orders them (see [Policy](#policy))
  → the first in that order that is not gated, then any serving
  candidate the order does not list, in token order;
- several serve it and nothing is ordered → relay refuses and lists
  them. Name one, or write the order.

When every candidate serving the role is gated, relay refuses and says
why each one is; an explicit `--builder` still bypasses that. The same
rule applies to `relay add`, `relay fork` (which first inherits the
source's candidate -- an inherited token counts as explicit) and `relay
ask --candidate`.

Every choice is written down. `bind`, `add`, `fork` and `ask` print one
line saying what was picked and why, and the same line lands in the
binding's log as a `pick` entry, so `relay log` shows it later:

```
picked claude/anthropic/sonnet for builder: order #2; skipped agy/google/gemini-3.8-flash-high (rate-limited until 20:28)
```

`relay candidates` prints the configured tokens with their roles. A
`--builder` value containing `:` and no `/` is a herdr pane id to adopt.

### Policy

`~/.config/relay/policy.json` is where you tell relay the order to try
candidates in, per role:

```json
{
  "order": {
    "builder": ["agy/google/gemini-3.8-flash-high",
                "claude/anthropic/sonnet",
                "opencode/openrouter/z-ai/glm-5.3-flash"]
  },
  "tier": {
    "builder": "edit",
    "reviewer": "read"
  },
  "max_tier": "edit",
  "max_switches": 2,
  "limit_gate_default_ms": 3600000,
  "scan_patterns": ["(?i)<instruction-tag"],
  "classify": { "provider": "jev", "model": "jev-latest", "injection_threshold": 0.7, "timeout_ms": 4000 },
  "gate": { "default": "make check", "timeout_ms": 600000 }
}
```

Roles you leave out are unordered, and an omitted `--builder` keeps
refusing for them when several candidates serve the role. A candidate
you add to `candidates.json` without adding it here is tried last, after
everything listed. An entry here that names a candidate that is not
configured, or one that does not serve the role, is skipped -- never an
error, because removing a candidate must not stop every command -- and
`relay policy` and `relay doctor` warn about it. Both also say when several
candidates serve a role and no order is set, since an omitted `--builder`
refuses in that state. `tier` specifies default permission tiers per role
(`builder`, `reviewer`, `researcher`), defaulting to `harness`. `max_tier`
bounds permission autonomy across all commands (`read`, `edit`, `yolo`),
defaulting to `edit`; commands requesting a tier above `max_tier` require
`--allow-yolo` or raising `max_tier`. `max_switches` bounds
how many times the daemon may replace a builder mid-round before the
binding goes `NEEDS YOU`; absent defaults to 2, `0` turns switching off.
`limit_gate_default_ms` is how long a rate limit relay detects itself
gates the provider when the matched line names no reset time; absent
defaults to one hour. `scan_patterns` is an optional list of extra
regular expressions appended to relay's built-in instruction-shaped scan list;
each pattern must compile. `gate` configures the default acceptance command
(see [Gate](#gate) below): `default` is the command a binding gets when it
does not set `--gate` or `--no-gate` itself, absent or `""` meaning no gate;
`timeout_ms` bounds one gate run, absent defaulting to ten minutes, and must
be `> 0` when present.

`classify` configures an optional classifier (TypeSafe's Jev model) to run
beside the regex scan. When absent, relay scans with regexes only. The block
requires `"provider": "jev"`; `model` defaults to `"jev-latest"`,
`injection_threshold` defaults to `0.7`, and `timeout_ms` defaults to `4000`.
The API key is read from the `TYPESAFE_API_KEY` environment variable or from
`~/.config/relay/typesafe.key`. The key is stripped from every builder's
environment so that agents running arbitrary plans never inherit relay's own secrets.
The key file exists because the daemon runs as a systemd user unit that inherits no login
environment (`systemctl --user set-environment TYPESAFE_API_KEY=...` also works). The key
file must be mode 0600 and is ignored otherwise, with `relay doctor` naming it. `relay doctor`
reports which key source was found or warns if neither is set. In `relay log`, an entry like
`flagged=3 by=both p=0.94` records the de-duplicated union of regex-hit lines
and classifier paragraphs at or above the threshold, which judge flagged the
content (`regex`, `jev`, or `both`), and the maximum probability seen across
all paragraphs. The 0.7 threshold is provisional pending `make jev`. Model
output is never altered and delivery is never held: a high probability flags the
entry for the planner to see, but never halts delivery.

`relay policy` shows what relay would do right now:

```
builder  (order set in ~/.config/relay/policy.json)
  1  agy/google/gemini-3.8-flash-high        order     rate-limited until 20:28
  2  claude/anthropic/sonnet                 order     <- would pick
  3  opencode/openrouter/z-ai/glm-5.3-flash  unlisted  limited 2x around 14:00 (30d)
reviewer  (no order set)
  1  claude/anthropic/opus                   sole      <- would pick
```

The marker is computed by the same code `bind` runs, so it cannot
disagree with what `bind` does next. There is no `relay policy set`:
edit the file. The rest of #61 -- scoring for unordered roles, peak
windows, mid-round switching -- will add keys to this file as it
lands.

### Availability

relay keeps a ledger of when a candidate could not be used: spawn failures it
observed itself, rate limits you report. It shows the ledger, and an omitted
`--builder` skips what the ledger gates (see [Choosing a
candidate](#choosing-a-candidate)). The file is
`~/.local/state/relay/ledger.json`.

Report a limit with:

```
relay unavailable claude/anthropic/sonnet --reason "5-hour window"
relay unavailable claude/anthropic/sonnet --for 2h
relay available anthropic
```

A limit gates the **provider** (every candidate with `provider: anthropic`),
because that is who enforces the quota, not the model. Without `--for` it
stays gated until you run `relay available`, because relay does not know
your provider's reset schedule.

Spawn failures need no command: relay records one itself when starting an
agent fails, gating that one candidate for ten minutes, and it expires on
its own.

Where it shows: `relay status` gains a `candidates` block only while
something is gated; `relay candidates` marks gated rows `unavailable:`;
`relay doctor` warns per gated candidate with the command that clears it.
`bind`/`add`/`fork`/`ask` with an explicit token print a `note:` on
stderr when the candidate is gated and **proceed** -- you named it. With
the token omitted they skip gated candidates and refuse when nothing
ungated serves the role.

#### Mid-round switching

A builder relay spawned can be replaced by the daemon while a round is
open, in two cases:

- its pane is gone for 30 seconds (a detection flicker shorter than
  that clears itself);
- you gate its provider with `relay unavailable` -- which is how you
  tell relay a running builder hit its limit. The command names the
  bindings the daemon will switch.

The daemon resolves `builder` again through `policy.json` order and the
ledger (an omitted token, so the order applies even to a builder you
named), closes the replaced pane if it is still open, starts the pick
beside the planner in the **same** tree, and hands it the **same**
round's plan. The round number does not change; the round clock
restarts. The new builder inherits whatever the old one left in the
tree. A `switch` entry in the log says what was tried and why:

```
switched builder (rate-limited: 5h window): picked opencode/openrouter/z-ai/glm-5.3-flash for builder: order #3; skipped claude/anthropic/sonnet (rate-limited until cleared)
```

`relay status` shows `switched 1x` on the builder line. After
`max_switches` replacements in one round (default 2; set it in
`policy.json`, `0` turns switching off), or when nothing ungated
serves `builder`, the binding goes `NEEDS YOU` with the reason, and
recovers on its own once `relay available` clears a provider. A
failed replacement spawn counts as a switch and the daemon walks to
the next candidate.

Adopted builders (bound by pane id) are never switched; a builder
gone between rounds is `BROKEN` as before -- `relay bind --resume`.

An **adopted** pane (bind by pane id, or `--resume`) needs no candidate: you launched that agent yourself, so it is already in whatever role you put it in. relay selects a role only for agents it starts, with `--agent` on the launch line.

`aliases.json` from earlier versions is no longer read.

#### History

Every gate relay records -- a limit you report, a spawn failure it hit
-- is also kept for 30 days in `~/.local/state/relay/history.json`, by
provider and local hour. `relay policy` shows it twice: a `limited 3x
around 21:00 (30d)` note on a candidate whose provider was limited
within an hour of now, and a `history` block with a 24-hour row per
provider. It changes nothing about which candidate is picked; it is the
cue to write a different order, or to `relay unavailable` a provider
before it bites.

## Permission tiers

relay defines four permission tiers that control how autonomously agents may use tools and make changes:

- `harness` — relay passes no permission flags to the harness; the harness CLI defaults or configuration files decide (relay default). Outside the tier order.
- `read` — read-only inspection and planning; write operations are denied.
- `edit` — file editing and standard workspace modification commands permitted.
- `yolo` — full autonomy; interactive permission prompts and confirmation dialogs bypassed.

The flags rendered for each harness kind (verified 2026-09-19 on claude 2.1.278, agy 1.2.7, opencode 2.0.8, codex 0.155.1):

| kind | harness | read | edit | yolo |
|---|---|---|---|---|
| claude | (none) | `--permission-mode plan` | `--permission-mode acceptEdits` | `--dangerously-skip-permissions` |
| agy | (none) | `--mode plan` | `--mode accept-edits` | `--dangerously-skip-permissions` |
| opencode | (none) | refuse | refuse | `--auto` |
| codex | (none) | `-s read-only` | `-s workspace-write` | `--dangerously-bypass-approvals-and-sandbox` |

opencode does not support `read` or `edit` tiers because it has no read-only or edit-only CLI flag. Choosing `read` or `edit` for an opencode candidate is refused immediately with an error directing you to use `--tier harness` (where `opencode.jsonc` decides) or `--tier yolo` (`--auto`).

### Ceiling semantics and ordering

Tiers are ordered as `read < edit < yolo`. `harness` is outside this hierarchy and is never compared as above or below other tiers.

`max_tier` in `policy.json` defines the permission ceiling across all commands, defaulting to `edit`. Any command requesting a tier above `max_tier` (such as `yolo` under default policy) is refused unless:
- The command includes `--allow-yolo` on the command line (e.g. `relay bind --tier yolo --allow-yolo` or `relay send --tier yolo --allow-yolo`), or
- `max_tier` is explicitly raised to `"yolo"` in `policy.json`.

`max_tier` cannot be set to `"harness"` because `"harness"` is outside the rank order and does not represent a ceiling.

### Resolution chain

When starting an agent, relay resolves the permission tier through a precedence chain:
1. Explicit CLI flag: `--tier <tier>` passed to `bind`, `add`, `fork`, or `send`.
2. Candidate configuration: `"tier"` set on the candidate in `candidates.json`.
3. Policy configuration: `"tier"` mapped for the active role (`builder`, `reviewer`, `researcher`) in `policy.json`.
4. Fallback default: `harness`.

When forking a binding (`relay fork`), if `--tier` is omitted, the new binding inherits the source binding's configured `tier`.

For consults (`relay ask`), tier resolves from the candidate's `tier`, policy `tier.<role>`, or `harness`. Consults accept no `--tier` flag, and a consult requesting `yolo` requires `max_tier: "yolo"` in `policy.json` since `ask` has no `--allow-yolo` flag.

### Headless vs. pane builder tiers

- **Pane builders**: A pane builder is a persistent terminal process spawned when the binding is created. Its CLI flags are fixed at spawn. Passing `--tier` to `relay send` on a pane binding is refused because an active interactive harness cannot be re-flagged mid-flight. To change the tier of a pane builder, re-bind it with `relay bind --resume --rebind --tier <tier>`.
- **Headless builders**: Headless builders execute a new process for each round. A round may temporarily override the tier using `relay send --tier <tier> [--allow-yolo]`. The round override takes precedence during that round, and is automatically reset to the binding's default tier when the round completes.

### Permission-blocked exits

When a headless builder exits without producing a report file, relay inspects the tail of its stdout/stderr log against the harness's default denial patterns (or the candidate's `denial_patterns` if configured).

If a permission denial pattern matches (for example, if a tool was refused because the agent attempted an edit while in `read` mode, or executed a command without required approvals):
- The binding halts and transitions to `NEEDS YOU`.
- The halt message quotes the matching denial line and instructs the planner to re-send with a higher tier or adjust harness allow lists.
- The ledger records an exit entry with note suffix `; permission-blocked: <line>`.
- relay does **not** switch to another candidate (which would waste quota on a configuration error) and does **not** record a rate-limit gate.

To recover from a permission-blocked halt, adjust the tier (e.g. `relay send --tier edit` or `relay send --tier yolo --allow-yolo`) or update permissions, then re-send the plan.

### Migration from extra_args

Previously, permission bypass flags were often passed via `extra_args` in `candidates.json` (such as `"--dangerously-skip-permissions"` or `"--auto"`).

When any explicit tier (`read`, `edit`, `yolo`) is active, relay validates that `extra_args` does not contain conflicting permission flags (e.g. `--permission-mode`, `--dangerously-skip-permissions`, `--mode`, `--auto`). If detected, relay refuses to launch.

To migrate:
- Move `--dangerously-skip-permissions` (or `--auto`) from `extra_args` to `"tier": "yolo"` on the candidate in `candidates.json`, and set `"max_tier": "yolo"` in `policy.json` (or use `--allow-yolo` on CLI commands).
- Alternatively, leave `tier` unset (or set to `"harness"`), and relay will leave `extra_args` untouched.

## Gate

A binding can carry a **gate command**: a shell line relay runs in the
worktree the instant the builder's completion marker appears, against the
tree exactly as the builder left it, the same moment the round's diff is
captured. The gate never decides anything -- it annotates. The round still
closes on the marker, the report is still delivered, and the human still
judges the diff; the gate only adds a `gate=<result>` note and a `Gate:`
line to the payload, plus a structured record on the report's log entry.
There is no repair loop yet: a failing gate is reported, not re-sent or
auto-fixed (a planned follow-up adds an opt-in repair round).

While the gate runs, the round is held: nothing else acts on the
builder -- no nudge, no "exited without a report" handling, no round-timeout
halt -- until the gate finishes or times out.

Configure it with `--gate '<cmd>'` on `relay bind`, `relay add`, or `relay
fork`; `--no-gate` opts a binding out of `policy.json`'s `gate.default` (see
above) even when one is configured machine-wide. `relay fork` without
`--gate`/`--no-gate` inherits the source binding's gate. Omitting both flags
on `bind`/`add` falls back to `gate.default`, `""` meaning no gate at all --
bindings written before this feature have no gate and are unaffected.

The gate's full output -- and the supervisor's exit trailer -- lives at
`NNN-gate.log` next to the round's plan and report. Its result is one of
`pass`, `fail`, `timeout`, or `error` (the last for a gate that could not
start, or whose exit code could not be read):

```
Gate: make check -- PASS (exit 0, 1m40s). Output: /path/to/003-gate.log
```

A failing gate's payload line also carries the last few non-empty lines of
the log, so the planner sees why without opening the file:

```
Gate: make check -- FAIL (exit 2, 1m40s). Output: /path/to/003-gate.log
  ok  	github.com/example/pkg	0.01s
  FAIL	github.com/example/pkg2	0.02s
  ...
```

`relay status` shows `gating <age>` in place of the builder's own status
while the gate is running, and `relay log` appends ` gate=<result>` to the
round's report entry.

## Consults: asking a reviewer

A **consult** is a one-shot agent spawned beside a binding to answer one
question. Unlike a builder, it is not persistent, does not advance the round,
and does not count against the one-writer-per-tree rule: it is a separate
record on the binding, not a binding of its own.

The planner runs, from its own pane:

```
relay ask --role reviewer --file q.md webshop
```

relay stages the question, opens a tab in the planner's workspace, and starts the
role there. The consult reads the staged question, writes its findings to a
file, and replies with only that path. Findings land under the binding's state
directory as `NNN-<id>-findings.md` — the exact path is printed when you ask —
and relay queues them to the planner like any other report, once the file
exists. That file's existence is the only completion gate: relay makes no
judgements about what the findings say.

While consults are running, `relay status` appends ` +Nc` to the binding's row
— only when non-zero, so a healthy binding looks no different. A finished
consult's pane stays open until you run `relay reap [NAME] [--dry-run]`, which
closes the panes of finished consults and drops their records. Terminal ones
are a reap chore, not work in flight, so the count does not include them.

`relay agent install` writes the reviewer definition with the other
roles; to install just this one:

```
relay agent install --role reviewer
```

`relay doctor` reports whether the definition landed, on every kind.

Read-only is a property of the role's configuration — the definition pins a
read-only tool set and the candidate's `tree` decides where it runs — not
something relay enforces. On agy the definition's `tools:` allowlist makes it
a property the harness enforces: a write tool that is not listed is not
offered. The list is also load-bearing the other way: a definition with no
`tools:` gets no write or shell tool at all, and one naming a tool agy does
not have does not start -- so every agy definition relay ships carries an
explicit, verified list, and `relay doctor` warns when the installed agy
copy differs from it (on claude and opencode the copy is yours to edit, and
doctor leaves it alone). relay cannot observe writes; it reports what is in a tree
and no more. Note also that `reviewer` is deliberately not the `researcher`
role: `researcher` is dispatched by a builder's own plan-executor and returns
findings in-band to it, while a reviewer runs in its own relay pane and hands
back a file path.

### Round usage

At every round close relay records what the round consumed on the
round's `report` entry in `log.jsonl` (and a consult's on its `findings`
entry): harness, provider, model, duration, tokens (`in`, `cache_read`,
`cache_write`, `out` -- `out` includes thinking), and a cost with its
provenance:

| `cost.basis` | meaning |
|---|---|
| `measured` | the harness reported dollars itself (claude's `total_cost_usd`, opencode's `cost`) |
| `estimated` | relay multiplied the harness's token counts by `~/.config/relay/prices.json` |
| `unknown` | no record, no price row, or no way to read; `note` says which |

`unknown` is an answer, not a failure. Where each figure comes from:

| harness | headless | pane |
|---|---|---|
| claude | the round's stream (`measured`) | `~/.claude/projects/<cwd>/` transcripts inside the round's window (`estimated`) |
| agy | the round's stream (`estimated`) | agy keeps no usage record (`unknown`) |
| opencode | the round's stream (`measured`) | `opencode.db` through `sqlite3` (`measured`); `relay doctor` says if `sqlite3` is missing |

A binding on `--cwd` shares the planner's directory, so its pane rounds
are `unknown` (`shared cwd`) rather than counting the planner's spend.

`prices.json` is `{"as_of": "YYYY-MM-DD", "source": "...", "models":
{"<provider>/<model>": {"in": …, "cache_read": …, "cache_write": …,
"out": …}}}` in USD per million tokens, overlaid on the table relay ships;
a model with no row is `unknown`, never `$0`. Mark a subscription lane
with `"plan": true` on its candidate: its rounds record `cost.plan` and
are shown as a quota draw, never as free.

Where you see it: `relay log` prints the round line under each report
(`⎿ claude/anthropic/claude-sonnet-5  14m  in 182k (cache 91%)  out 12k  ~$0.41`);
`relay status` adds a `usage` row (newest round) and a `spend` row
(the binding's total: `4 rounds +2c · $1.23 · ~$0.40 · 2 unknown`), both
on `--json` as `last_usage` and `spend`; `relay ui` shows the total on
the card and in the header. Across bindings:

```
relay tab [--since 7d|24h|2026-09-01] [--by binding|model|provider] [--json]
```

sums every round relay has recorded, including bindings `gc` has
archived, one row per group and a total. Measured and estimated dollars
never share a column; `plan` and `unknown` are counts of rounds. `tab`
is the second exception to the #114 verb freeze, taken because its
sums exist regardless (they are on `status --json`) and a cross-binding
view has no other home.

### Consult candidates

`relay ask --role reviewer` resolves `reviewer` through the role table and then
picks a candidate whose `roles` include it, by the same rule as `--builder`.

In `~/.config/relay/candidates.json`:

```json
{"harness": "claude", "provider": "anthropic", "model": "opus", "roles": ["reviewer"]}
```

```bash
relay ask --role reviewer --candidate claude/anthropic/opus --file q.md webshop
```

Until a candidate lists `reviewer`, `ask` fails with
`no configured candidate serves role "reviewer"`.

## The planner: architect

relay ships one more definition it never launches: `architect`, the planner's
persona. The planner is the session you drive -- the pane you run `relay bind`
and `relay ask` from -- and relay does not pick its harness or start it. What
relay provides is the definition, so the same architect runs on any kind:

```
relay agent install --role architect
```

(`relay agent install` with no flags writes it too.)

Then start the planner with the harness's own `--agent` flag, for example:

```
claude   --agent architect --model opus
opencode --agent architect -m openrouter/deepseek/deepseek-v4-pro
```

The architect designs and never implements: it produces a system overview,
file structure, data structures, interface contracts, pseudocode, an error
handling strategy and ordered implementation steps -- the plan a builder's
plan-executor takes as written. Its agy copy pins `model: inherit` and a
read-plus-`write_to_file` tool set, enough to read the tree and write the plan
and nothing more; the claude and opencode copies leave `model:` unset so the
launch line's flag decides.

`architect` is not a relay role: it is absent from the role table, so
`relay ask --role architect` is refused, candidates cannot list it, and
`relay doctor` does not check for it -- doctor reports only the definitions
some candidate would load, and no candidate loads the planner.

## Display states

`relay status` collapses the binding's internal state into four:

- **ACTIVE** — someone is working (planner or builder), nothing needs a human
  yet.
- **NEEDS YOU** — relay has stopped and a person must act. Covers a blocked
  builder (answer its dialog), a dead builder pane, a lost planner pane, a
  round that ran past its timeout, and a binding that hit its round cap.
- **HELD** — a payload is ready for the planner, but the planner pane is
  focused, so relay is holding it rather than typing into it. `relay status`
  shows the hold's clock on the `pending` line: how long the planner's screen
  has been quiet against `--held-grace`, or that the clock has not started
  because the screen could not be read.
- **DONE** — the planner declared the work verified via `relay done`, and
  relaying has stopped deliberately, not because anything went wrong: unlike
  NEEDS YOU, nothing needs a human here. `Reconcile` returns immediately for
  a done binding — no reports are queued, no dialogs captured, no timeouts
  flagged. The binding and its round log stay on disk (`relay log <name>`
  still works as an audit trail) until `relay unbind` or `relay gc` removes them;
  a clean worktree is released at `done` so the branch is free to review.

## The anti-clobber rule

`herdr agent prompt` types text into a pane and presses Enter. Since relay
cannot see what a human has half-typed, it treats a focused planner pane as
unsafe to inject into: it holds the payload and sends one herdr notification
(not one per tick) instead of typing over the human. A held payload leaves the
hold three ways: your focus moves to another pane, so the daemon delivers on
its next tick; the planner's input box is seen empty (claude only today), so
there is nothing to clobber; or the planner's visible screen has not changed
for `--held-grace`, in which case an abandoned draft gets the payload appended
— accepted on purpose, since it beats a payload that never arrives. `relay
pull` bypasses this entirely — it prints the payload to stdout instead of
injecting it, so it's safe to run from inside the focused planner pane at any
time.

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

relay supports user-defined hook scripts dispatched during binding lifecycle events. When state changes or a new round begins, `relay daemon` executes scripts located in `$XDG_CONFIG_HOME/relay/hooks/<event_type>.d/` (default `~/.config/relay/hooks/<event_type>.d/`).

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
in these patterns. Claude builders (`claude/anthropic/sonnet`) have their own permission model
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
relay bind --resume --name N --rebind                                       # spawn a fresh builder, picked by policy order
relay bind --resume --name N --builder agy/google/gemini-3.8-flash-high     # spawn a fresh builder, naming it
relay bind --resume --name N --builder w2:p4        # adopt an existing pane
```

The binding keeps its name, round number, round log, working directory, and diff
baseline. The replacement builder is started with its role on the launch line,
like any builder relay spawns. With `--rebind` the candidate is resolved through
`policy.json` order and the ledger, and the pick is logged, exactly as a fresh
bind with `--builder` omitted. Relay does not automatically re-send the current
plan: it prints the `relay send` command pointing at the staged plan so you can
hand over the round when ready.

If only the planner moved or restarted, `relay bind --resume --name N` re-points
the planner without touching the builder. If you want to start over from scratch,
use `relay unbind N` and bind fresh.

A headless binding is never `BROKEN` for lack of a process: between rounds
there is none. If its process died mid-round the daemon already switched or
halted it (see "Headless builders"). To move a headless binding to a fresh
process by hand, `relay send` the staged plan again once `relay status` shows
the builder `exited`; `--resume --headless` is refused, so changing a pane
binding into a headless one is `relay unbind` and a fresh `relay bind --headless`.

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

`make e2e` runs one relay round -- bind, send, reconcile, delivery -- against
a private herdr session it creates and deletes, with two shell scripts
standing in for the agents. It needs `herdr` on PATH and skips otherwise; it
is not part of `make check` and does not run in CI. `RELAY_E2E_KEEP=1` leaves
the session up for a look.

## License

[MIT](LICENSE) © Fuad Daoud
