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
  the builder. relay knows how to start `opencode`, `claude` and `agy`; you tell it
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
4. Emit the builder's role definitions directly into your harness's config directory:
   ```
   # agy
   relay agent print --kind agy --role plan-executor > ~/.gemini/config/agents/plan-executor.md
   relay agent print --kind agy --role researcher    > ~/.gemini/config/agents/researcher.md

   # claude
   relay agent print --kind claude --role plan-executor > ~/.claude/agents/plan-executor.md
   relay agent print --kind claude --role researcher    > ~/.claude/agents/researcher.md

   # opencode
   relay agent print --kind opencode --role plan-executor > ~/.config/opencode/agents/plan-executor.md
   relay agent print --kind opencode --role researcher    > ~/.config/opencode/agents/researcher.md
   ```

   `researcher` is the read-only role the builder's own sub-agents run as. It
   exists because exactly one agent may write to a working tree: research can fan
   out safely, implementation cannot. The claude and opencode definitions pin a
   `model:` in their front matter as a worked example, chosen so neither needs a
   provider the rest of relay does not already assume; that line is the first
   thing to change for your own setup. The agy definitions pin `model: inherit`
   and that is not an example: on agy the key is a tier (`inherit`, `flash`,
   `pro`) that would override the `--model` relay passes at launch. `relay
   doctor` reports the pin each installed definition carries, and warns when an
   agy copy pins a tier.
5. Write `~/.config/relay/candidates.json` (see [Candidates](#candidates)) and check it with `relay candidates`.
6. Re-run `relay doctor` to confirm `0 failures`.
7. Start the daemon (e.g. `relay daemon &` or `make service`).
8. Bind your first agent from inside a herdr planner pane:
   ```
   relay bind --builder claude/anthropic/sonnet
   ```
   (or, with one candidate, `relay bind`).

## Quick start

From inside the planner's herdr pane, in the repository you want worked on:

```
relay bind --builder claude/anthropic/sonnet     # split a builder pane and bind it to this tree
relay send --file plan.md         # hand it the plan; the builder starts working
relay status                      # watch the round
relay pull                        # print the report the builder wrote back
relay done <name>                 # stop relaying when you are satisfied
```

`relay bind` reads the planner's pane from `$HERDR_PANE_ID`, which herdr sets
inside every pane it manages, so it has to be run from inside one.

## Command surface

- `relay bind [--name N] [--builder CANDIDATE|PANE_ID] [--resume] [--tab] [--timeout D]`
  — start a binding between the calling planner pane (read from
  `$HERDR_PANE_ID`) and a builder. `--builder` is a candidate token unless
  it contains `:` and no `/`, in which case it is treated as a herdr pane id and that pane
  is **adopted** instead of spawned. A name that already exists is refused
  rather than reused: only `bind.json` would be rewritten, so a fresh round 1
  would collide with the previous session's round log. `--resume --name N`
  re-points that existing binding's planner side at the calling pane without
  touching the builder; `relay unbind N` is the other way out.
- `relay send [NAME|--name N] --file PATH` — stage the file as the current round's
  plan and prompt the builder with it.
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
- `relay status [NAME|--name N] [--json] [--all]` — one row per binding: round, display state, both
  panes' live herdr status, the last relayed event, anything pending, and for a nudged builder how long its terminal has been quiet against the grace after which relay scrapes it. Naming a binding shows only that one. Bindings marked DONE are hidden by default and the footer names how many are hidden.
- `relay log NAME` — the binding's append-only round log.
- `relay watch [--interval D] [--all]` — `status`, redrawn on a timer, default 2s.
- `relay ui [--interval D]` — interactive reader: report, terminal, diff and log tabs.
- `relay add --name N [--builder CANDIDATE] [--tab] [--cwd DIR]` — attach an
  additional builder to this planner on its own git worktree, starting at
  round 1. This is how one planner drives several builders at once.
- `relay fork <source> --round R --new-name N [--builder CANDIDATE] [--tab] [--cwd DIR]` —
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
- `relay done NAME|--name N` — mark a binding done; relaying stops.
- `relay unbind NAME|--name N [--archive]` — forget a binding, deleting its directory or
  packing it into `.archive/` first.

- `relay gc [--dry-run] [--delete]` — clear every binding the planner marked
  `DONE`, in one pass. Archives by default; pass `--delete` to remove each binding's directory instead (`relay gc --archive` is accepted as a no-op).
- `relay daemon [--interval D] [--held-grace D]` — the long-running reconciler; this is what
  the service unit runs. A held payload is injected into a focused planner once its input
  box is empty or its screen has been quiet for `--held-grace` (default 60s).
- `relay help` — the command list. `relay <command> -h` prints that command's
  flags.
- `relay version` — the build's version.

Every binding-scoped command takes its binding either positionally or as
`--name`; naming it both ways at once is refused. `send`, `pull` and `diff`
fall back to whichever binding owns the current working directory, and a bare
`relay status` lists them all. Naming one is **required** for `answer`, `done`
and `unbind`: those act on a specific loop — `answer` types into a live dialog,
the other two end one — and they refuse to guess (see below).

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

Relay does not sequence them and does not merge their trees. The planner
decides how many builders it needs, which run in parallel and which wait, and
integrates the results — relay only carries plans out and reports back.

**`relay add` is not `relay fork`.** A fork continues a timeline: it copies
round history through a chosen round and starts at the round after it. A peer
starts at round 1 with an empty log, because it is not a continuation of
anything.

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
`--name ai`). Neither resolves the current directory for you: a bare `relay done` once ended a live
loop by accident, and the recovery is `relay bind --resume --name <name>`.

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

- `harness` — a kind relay knows: `agy`, `claude`, `opencode`. Required.
- `provider` — who enforces the quota; free text. Required.
- `model` — passed to the harness as-is. Required.
- `roles` — non-empty list of roles from `builder`, `reviewer`, `researcher`. Required.
- `tree` — `binding` (the default) or `none` (which `relay ask` refuses today).
- `extra_args` — appended verbatim after what relay renders.

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
  "max_switches": 2
}
```

Roles you leave out are unordered, and an omitted `--builder` keeps
refusing for them when several candidates serve the role. A candidate
you add to `candidates.json` without adding it here is tried last, after
everything listed. An entry here that names a candidate that is not
configured, or one that does not serve the role, is skipped -- never an
error, because removing a candidate must not stop every command -- and
`relay policy` and `relay doctor` warn about it. `max_switches` bounds
how many times the daemon may replace a builder mid-round before the
binding goes `NEEDS YOU`; absent defaults to 2, `0` turns switching off.

`relay policy` shows what relay would do right now:

```
builder  (order set in ~/.config/relay/policy.json)
  1  agy/google/gemini-3.8-flash-high        order     rate-limited until 20:28
  2  claude/anthropic/sonnet                 order     <- would pick
  3  opencode/openrouter/z-ai/glm-5.3-flash  unlisted
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

## Consults: asking a reviewer

A **consult** is a one-shot agent spawned beside a binding to answer one
question. Unlike a builder, it is not persistent, does not advance the round,
and does not count against the one-writer-per-tree rule: it is a separate
record on the binding, not a binding of its own.

The planner runs, from its own pane:

```
relay ask --role reviewer --file q.md webshop
```

relay stages the question, splits a pane beside the planner, and starts the
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

Emit the definitions into the harness's agent directory the same way as the
other roles:

```
# agy
relay agent print --kind agy      --role reviewer > ~/.gemini/config/agents/reviewer.md
# claude
relay agent print --kind claude   --role reviewer > ~/.claude/agents/reviewer.md
# opencode
relay agent print --kind opencode --role reviewer > ~/.config/opencode/agents/reviewer.md
```

`relay doctor` reports whether the definition landed, on every kind.

Read-only is a property of the role's configuration — the definition pins a
read-only tool set and the candidate's `tree` decides where it runs — not
something relay enforces. On agy the definition's `tools:` allowlist makes it
a property the harness enforces: a write tool that is not listed is not
offered. relay cannot observe writes; it reports what is in a tree and no
more. Note also that `reviewer` is deliberately not the `researcher`
role: `researcher` is dispatched by a builder's own plan-executor and returns
findings in-band to it, while a reviewer runs in its own relay pane and hands
back a file path.

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
  still works as an audit trail) until `relay unbind` removes them.

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
relay bind --resume --name N --builder agy/google/gemini-3.8-flash-high     # spawn a fresh builder
relay bind --resume --name N --builder w2:p4        # adopt an existing pane
```

The binding keeps its name, round number, round log, working directory, and diff
baseline. The replacement builder is started with its role on the launch line,
like any builder relay spawns. Relay does not automatically re-send the current
plan: it prints the `relay send` command pointing at the staged plan so you can
hand over the round when ready.

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
