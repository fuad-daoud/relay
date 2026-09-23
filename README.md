# relay

[![ci](https://github.com/fuad-daoud/relay/actions/workflows/ci.yml/badge.svg)](https://github.com/fuad-daoud/relay/actions/workflows/ci.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Site: [relay-site.fuad-daoud.com](https://relay-site.fuad-daoud.com) (source in [fuad-daoud/relay-site](https://github.com/fuad-daoud/relay-site), together with the `DESIGN.md` and `PRODUCT.md` that govern the page).

`relay` automates the plan/report handoff between two AI coding agents. A
human talks to a **planner** agent; the planner hands work to a **builder**
agent; relay moves the files between them so the human never copy-pastes a
plan or a report by hand. Builders are headless or remote processes, and
relay no longer integrates with herdr.

Relay makes no judgements. It moves files, starts builders, and reports what
each round did — whether a report is good, whether a question needs a human,
whether the work is done, is a decision that stays with the planner (or the
human) at every step.

## Requirements

- **Two agent harnesses** — one for the planner, one for the builder. relay
  knows how to start `opencode`, `claude`, `agy` and `codex`; you tell it
  which models in [Candidates](#candidates).
- **`git` on `PATH` (optional).** Required for automatic round diff capture; without it, relay works normally but rounds produce no diffs.
- **Linux or macOS.** See [Platform support](#platform-support).
- **Go 1.22+**, to build from source. Not needed if you install a release
  binary.

## Install

Prebuilt binaries for Linux and macOS (amd64 and arm64) are attached to every
[release](https://github.com/fuad-daoud/relay/releases); unpack one and put
`relay` on your `PATH`.

With a Go toolchain:

```
go install github.com/fuad-daoud/relay/cmd/relay@latest
```

That drops `relay` in `$(go env GOPATH)/bin` — make sure it is on your `PATH`.

A release binary and a `go install` both know how they were installed. `relay
doctor` warns when a newer release exists and prints the update step for that
install: the archive and `checksums.txt` to download, or the `go install`
command. `relay status` shows one line when a newer release exists. A local
build is never called stale.

Or from a clone, which also stamps the binary with the current tag so
`relay version` is meaningful:

```
git clone https://github.com/fuad-daoud/relay
cd relay
make install        # builds and installs ~/.local/bin/relay
```

To install the built binary by hand, use the same two commands `make install`
does. The rename is atomic within the directory, so a running daemon never sees
a half-written file:

```
install -m755 relay ~/.local/bin/relay.new
mv -f ~/.local/bin/relay.new ~/.local/bin/relay
```

`make check` runs the full gate — `gofmt -l .`, `go vet ./...`, and
`go test -count=1 ./...` — and `make install` runs it first.

To run the reconciler as a background service:

```
make service        # systemd user unit on Linux, LaunchAgent on macOS
```

### Upgrading

A running daemon moves onto a newly installed binary by itself within a few
seconds, and a round in flight is not interrupted: builders, gates and consults
run in their own systemd scopes and survive the restart. A daemon started
before this release needs one manual restart to start following upgrades —
`make service`, or `systemctl --user restart relay.service`. `relay doctor`
shows what the daemon is running. The daemon also refreshes the role
definitions relay wrote for each harness on every start, and leaves a file you
edited alone; a planner session's `relay mcp` notices the upgrade too -- it
appends a line to every tool result saying to reconnect it (`/mcp`), so the
session loads the new server without a restart.

### The Claude Code plugin

A Claude Code planner installs relay as a plugin. The plugin provides the
`relay mcp` MCP server and a `SessionStart` hook that runs
`relay planner init`, so relay knows which planner session is calling:

    /plugin marketplace add fuad-daoud/relay
    /plugin install relay@relay

The plugin ships with relay's releases. Claude Code caches an installed plugin
by version, so after upgrading relay, update the plugin to match (`relay
doctor` warns when they differ):

    claude plugin marketplace update relay && claude plugin update relay@relay

A change under `claude-plugin/` that is not released yet never reaches an
installed plugin this way -- `update` sees the same version and skips it. To
try one, reinstall: `claude plugin uninstall relay@relay && claude plugin
install relay@relay`.

See [Claude Code plugin](#claude-code-plugin) below for how a report reaches
the planner.

## First run on a clean machine

On a clean machine, set up prerequisites and preflight with `relay init` and
`relay doctor`:

1. Install relay (see [Install](#install)).
2. Run `relay init` to write a starter configuration from the harnesses on
   `PATH`:
   ```
   relay init
   ```
   It finds the harness binaries on `PATH`, writes one builder candidate per
   harness to `~/.config/relay/candidates.json`, writes
   `~/.config/relay/policy.json` ordering them, and installs the role
   definitions into each of those harnesses. It refuses to overwrite either
   config file without `--force`, and `--no-roles` skips the definitions. It
   says what it wrote and the command to run next, e.g.:
   ```
   wrote ~/.config/relay/candidates.json (2 candidates: claude, opencode)
   wrote ~/.config/relay/policy.json (order.builder: claude/anthropic/sonnet, opencode/openrouter/z-ai/glm-5.3-flash)
   wrote  ~/.claude/agents/plan-executor.md
   wrote  ~/.config/opencode/agents/plan-executor.md
   next: edit the model names in ~/.config/relay/candidates.json, then run: relay doctor
   ```

   **Edit the model names it wrote** to the models your accounts may run (see
   [Candidates](#candidates)); `relay candidates` prints what you configured.
   When more than one candidate serves `builder`, `relay policy` shows the
   order relay would pick them in and says `would refuse` until the file's
   order suits you (see [Policy](#policy)).
3. Run `relay doctor` to check your environment:
   ```
   relay doctor
   ```
   Doctor inspects the background daemon, each harness binary on `PATH`, the relay plugin and its hook in Claude Code, and the builder role files.
4. Run the literal fix commands `relay doctor` prints for any missing items.
5. Re-run `relay doctor` to confirm `0 failures`.
6. Start the daemon (e.g. `relay daemon &` or `make service`).
7. Bind your first agent from the planner session:
   ```
   relay bind --builder claude/anthropic/sonnet
   ```
   (or, with one candidate, `relay bind`).

To write these files by hand instead, install the role definitions into each
harness on `PATH` — the daemon refreshes unmodified definitions on every start
and upgrade, a file you edited is kept, and `relay agent install --force`
replaces it:
```
relay agent install
```
One line per file says `wrote`, `updated (unchanged since relay wrote it)`,
`kept (identical)` or `kept (differs; --force to overwrite)`. Pass `--kind` to
name a harness that is not on `PATH` yet, `--role` for one definition,
`--dry-run` to look first.
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
that pin drifts.

Then write `~/.config/relay/candidates.json` (see [Candidates](#candidates))
and check it with `relay candidates`. If more than one candidate serves
`builder`, write `~/.config/relay/policy.json` with the order to try them in
(see [Policy](#policy)); `relay policy` shows what relay would pick and says
`would refuse` until you do. `relay doctor` warns about this too and prints a
starter file built from your candidates.

## Quick start

On a clean machine, seed your configuration first (see
[First run on a clean machine](#first-run-on-a-clean-machine)):

```
relay init                        # write candidates.json and policy.json, install the role definitions
```

From the planner session, in the repository you want worked on:

```
relay bind --builder claude/anthropic/sonnet     # start a builder on this tree
relay send --file plan.md         # hand it the plan; the builder starts working
relay status                      # watch the round
relay wait                        # block until the round closes or needs you
relay pull                        # print the report the builder wrote back
relay done <name>                 # stop relaying when you are satisfied
```

The builder writes `NNN-report.md` when it has finished and then creates an
empty `NNN-done` as its last action; relay closes the round on that marker.
A builder that exits without the marker still closes the round, and relay
delivers its report flagged `unmarked`.

`relay bind` identifies the calling planner through `RELAY_PLANNER`, which
the relay plugin's `SessionStart` hook exports, or through the harness
process the `relay mcp` server shares with the session. Run
`relay planner list` to see the planners relay knows.

## Command surface

- `relay bind [--name N] [--builder CANDIDATE] [--resume [--rebind]] [--timeout D] [--feature LABEL]`
  — start a binding between the calling planner and a builder. `--builder` is
  a candidate token. A name that already exists is refused rather than reused:
  only `bind.json` would be rewritten, so a fresh round 1 would collide with
  the previous session's round log. `--resume --name N` re-points that
  existing binding's planner side at the calling planner without touching the
  builder; `relay unbind N` is the other way out. `--headless` is accepted and
  ignored with a one-line stderr note: headless is the only local mode (see
  "Headless builders" below).
- `relay send [NAME|--name N] --file PATH [--dry-run]` — stage the file as the current round's
  plan and hand it to the builder as the prompt of a fresh process started in
  the binding's tree.
  A headless binding whose previous round's process is still running refuses
  the send; wait for its report or `relay done` it.
  `--dry-run` checks every precondition a send would and prints what it would
  do, writing nothing: no plan staged, no log entry, no prompt, no process
  started. A precondition that fails is the same error `relay send` gives, exit
  1, with nothing written. A local binding names the exact command line it
  would run, and a remote one the server and branch without contacting it:
  ```
  would send round 5 to api-auth
    builder   headless agy/google/gemini-3.8-flash-high
    where     /usr/bin/agy -p
    tier      yolo
    plan      /home/me/.local/state/relay/api-auth/005-plan.md  (staged from ./plan.md, 4.1 KiB)
    report    /home/me/.local/state/relay/api-auth/005-report.md
    marker    /home/me/.local/state/relay/api-auth/005-done
    prompt    relay: round 5 · to builder "api-auth" · from the planner (not the human)
              Your working tree is: /home/me/.worktrees/api-auth
  ```
- `relay pull [NAME|--name N] [--path-only]` — print the oldest pending
  report's text to stdout (the report's pointer line, a blank line, then the
  report itself, capped at 64 KiB) and mark it delivered. This is how a
  planner fetches a report directly, and what the background wait's
  `relay pull` uses. `--path-only` prints just the pointer line for scripts.
- `relay diff [NAME|--name N] [--round R] [--stat] [--drift] [--anchors]` — print a round's
  captured patch to stdout, or its diffstat summary with `--stat`. Pass `--drift`
  to inspect between-rounds drift instead of the round's diff; `--drift` composes
  with `--stat` and `--round`, and defaults to the currently open round where plain
  `relay diff` defaults to the newest completed one. Pass `--anchors` to prefix
  each hunk and each `' '`/`'+'` line with its `path:line`, ready to quote into a
  review comments file (see "Reviewing a round" below).
- `relay review [NAME|--name N] --file comments.md [--round R] [--out PATH] [--send]` —
  turn a `path:line: comment` comments file into a follow-up plan, one task per
  anchored comment quoting its hunk from the round's diff. Not destructive, so
  it falls back to the CWD's binding like `diff`. See "Reviewing a round" below.
- `relay status [NAME|--name N] [--json] [--all]` — one row per binding: round, display state, the builder's own status, the last relayed event and anything pending. Rows are attention-first -- NEEDS YOU, ACTIVE, PAUSED, DONE, stale first within a group, newest last-event first -- the same order `relay ui` has always used, so the two never disagree. Naming a binding shows only that one. Bindings marked DONE are hidden by default and the footer names how many are hidden.
  While a round is open a row also shows the round's live diff against its baseline (`+120/-30 in 6`, `(shared tree)` for a `--cwd` binding sharing the planner's own working tree), an ACTIVE row's `quiet <age>` since its last progress sample, and `●new` when the binding's newest report is unread (see `.viewed` below).
  `--json` also carries fields the prose above does not spell out:
  ```
  branch      the binding's worktree branch; absent for a --cwd binding
  waiting     set when the binding is stalled on a human: cause, line, since, hint
  last_seq    the Seq of the binding's newest log entry; 0 when the log is empty
  live        the round's live diff stat against its baseline tree (files/added/removed,
              shared true for a --cwd binding); absent when no round is open
  quiet_for   an ACTIVE row's age since its last progress sample; absent otherwise
  unread      true when the binding's newest report is newer than its .viewed stamp
              (or there is no stamp and a report exists)
  ```
- `relay log NAME [--round N] [--after N] [--json] [--follow]` — the binding's append-only round log. Every entry carries a 1-based `seq`, monotonic within the binding and never rewritten; `--round N` shows one round, `--after N` shows only entries with a greater `seq`, `--json` prints one compact JSON object per line (NDJSON, `seq` included), and `--follow` keeps printing new entries until the binding is DONE or gone. A file written before `seq` existed reads back with `seq` equal to the line number, so nothing is rewritten.
  A hook or script that has already seen up to a known `seq` asks only for the rest:
  ```bash
  last_seq=$(relay status --json | jq -r '.bindings[] | select(.name == "NAME") | .last_seq')
  relay log NAME --after $(last_seq) --json
  ```
- `relay history [--here|--repo <url|dir>] [--feature L] [--binding N] [--planner S] [--harness K] [--provider P] [--model M] [--candidate T] [--outcome O] [--since D] [--until D] [--archived|--live] [--limit N] [--json] [-q "<query>"] [--by <axis>] [--rows]` —
  one line per round across every binding relay has ever recorded, live or archived, newest first. `-q` filters with the query language and `--by` regroups the result. See "The database" below.
- `relay show <name> [--round N] [--plan|--report|--diff|--drift|--log|--transcript] [--json]` —
  one round's plan, report, diff, drift, log or transcript, from a live binding's files or, for anything not live, from the database. See "The database" below.
- `relay tab [--since 7d|24h|2026-09-01] [--by binding|model|provider] [--json]` —
  tokens and cost across bindings, archived ones included.
- `relay stats [--since 7d] [--json]` —
  rounds, outcomes, switches, gate results and consults across bindings,
  archived ones included, read from the round logs; the last 30 days of
  provider blocks come from the availability history. See "Usage stats" below.
- `relay wait [NAME|--name N] [--any N1 N2 ...] [--round R] [--timeout D]` — block
  until the round closes or the binding needs you, reading relay's own state only.
  Exit 0: closed on the marker, stdout is the report path. 2: closed
  without it (`unmarked`, `noreport` — verify before trusting), report
  path or `-`. 3: needs you, stdout is one line saying what it is waiting on. 4:
  the binding is DONE or was unbound. 5: closed, but the builder's report says
  halted or blocked -- read it before sending again; stdout is the report path.
  6: the round has no plan entry -- it was never sent, so nothing is in flight;
  stdout says so. 124: `--timeout` (default 10m) elapsed. `--any` waits on several and prints the
  winner's name first. A Claude Code planner runs `relay wait N; relay pull N` as a
  background command and ends its turn: Claude Code wakes the session when the
  command exits (see [Claude Code plugin](#claude-code-plugin)).
- `relay ui [--interval D] [--dashboard]` — interactive reader: at 110 columns or more, a rail
  of bindings grouped by state beside a pane showing the selected binding's
  report, terminal, diff or log; narrower terminals get the list-then-detail
  flow. `--dashboard` opens on the dashboard screen (`d` reaches it from the
  fleet).
- `relay add --name N [--builder CANDIDATE] [--cwd DIR] [--feature LABEL]` — attach an
  additional builder to this planner on its own git worktree, starting at
  round 1. This is how one planner drives several builders at once.
  `--headless` is accepted and ignored, as for `bind`.
  `relay add --name N --server S [--base REF]` runs that builder on a
  configured remote server instead (see "Remote builders: the client" below);
  `--cwd` cannot be combined with `--server`.
  `relay add --branch B` checks an existing branch out into relay's own
  worktree instead of cutting `relay/<name>`: a local `B` is used first, and
  `origin/B` is made a local tracking branch only when no local `B` exists
  (a branch on neither is refused). A branch already checked out in another
  worktree is refused; free it first. `--name` is optional with `--branch`
  and defaults to the branch's last path segment, lowercased and reduced to
  the characters a binding name accepts. The binding records
  `existing_branch: true`, and relay never deletes a branch it did not
  create. Works with `--server`; not with `--cwd`.
  For a `--server` binding the server's own `refs/heads/relay/<name>` ref is
  kept in the repository beside the adopted branch: every closed round
  absorbs it and fast-forwards `B` to it, and relay deletes neither.
  `relay fork --branch` is not available yet.
- `relay fork <source> --round R --new-name N [--builder CANDIDATE] [--cwd DIR] [--feature LABEL]` —
  branch a new binding from an earlier round of an existing binding, copying
  round history and artifacts through round R and launching a fresh builder in a
  dedicated git worktree (or in `--cwd`). `--feature` defaults to the source
  binding's own.
- `relay candidates` — list the configured candidates and the roles each serves.
- `relay policy` — show, per role, the candidates in the order relay would
  try them, which one it would pick right now, and any gap between
  `policy.json` and `candidates.json`.
- `relay roles` — list every role's shape, candidates, tier and per-kind agent
  definitions; `relay roles init` writes `roles.json` from `candidates.json`
  and `policy.json` (`--dry-run`, `--force`).
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
- `relay daemon [--interval D]` — the long-running reconciler; this is what the
  service unit runs. It reconciles builders, queues reports for the planner
  and syncs remote bindings.
- `relay serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N]` — run the remote-builder server (listener + daemon).
- `relay serve init|enroll|clients|revoke|fingerprint|status|gates|available|unavailable|gc|unbind` — server administration, on the server host.
- `relay serve gates [--state DIR]` — list the gates on the server's own ledger.
- `relay serve available <provider|token> [--state DIR]` — clear a recorded rate limit on the server's ledger.
- `relay serve unavailable <token> [--for D] [--reason S] [--state DIR]` — record a provider rate limit on the server's ledger.
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
`--name`; naming it both ways at once is refused. `send`, `pull`, `diff` and
`review` fall back to whichever binding owns the current working directory,
and a bare `relay status` lists them all. Naming one is **required** for
`answer`, `done` and `unbind`: those act on a specific loop — `answer` types
into a live dialog, the other two end one — and they refuse to guess (see
below).

### Reviewing a round

`relay diff --anchors` prints a round's patch with a `path:line` gutter on
every hunk header and every `' '`/`'+'` line, so a comment can quote a line
straight off the printed diff instead of counting by hand:

```
$ relay diff --name api-auth --anchors
diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
auth.go:41  @@ -38,6 +38,7 @@ func Login(ctx context.Context, u string) error {
auth.go:41  	if u == "" {
auth.go:42 +		return errEmptyUser
auth.go:43  	}
```

A comments file is one `path:line: text` per line; a line without an anchor
is a general comment:

```
auth.go:42: return a typed error, not errEmptyUser directly
auth.go:43: this brace can go too, see the hunk above
wire the new error into the CLI's exit code table
```

`relay review NAME --file comments.md [--round R] [--out PATH] [--send]`
validates every anchor against that round's diff — an anchor the diff does
not have is refused before anything is written — and renders a plan with one
task per anchored comment, quoting the anchored hunk (three lines of context
either side) and the comment verbatim, plus a "General comments" section for
the unanchored lines. It writes the plan to `--out`, or by default
`<binding dir>/NNN-review-plan.md` (`NNN` the reviewed round), and prints the
path. `--send` hands that plan to the builder as the next round, the same as
`relay send --file`. The rendered plan opens with "Fix ONLY what these
comments ask" — delete that line if the round should be broader.

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

It is strictly **read-only**: it never mutates state and never appends to
round logs. It holds the state lock only for the duration of a read, exactly
as `relay status` does.

At 110 columns or more, a rail of bindings grouped by state sits beside a
pane showing the selected binding's plan, report, terminal, diff or log
(`⏎` focuses the pane, `s` toggles attention and name order); narrower
terminals get the list-then-detail flow. The pane's five tabs:
- **plan** — the round's own plan file, first in the order (#183).
- **report** — that round's planner-bound report or question payload.
- **terminal** — recent live terminal output from the builder's log.
- **diff** — the captured git patch from the round.
- **log** — the formatted append-only round log, scoped to the round.

`[` and `]` step the round a tab reads, from round 1 through the binding's
current round -- every tab refetches for the new round. Stepping onto a
live binding's own open (not yet closed) round reads as prose, not an
error: the report and diff tabs say so ("round N is open; report arrives
when it closes", "diff is captured when round N closes") rather than
showing stale content.


### `relay ui`'s dashboard: every round, filtered and regrouped

`d` opens a second screen: every round relay's database has recorded, live or
archived, as a grid. It is the same data `relay history` reads, with the query
language as a filter line and the sums of `--by` above the rows. `d` or `esc`
returns to the fleet with the query intact; `relay ui --dashboard` opens here
directly.

The first line is the applied query and the regroup axis; under it a tiles
line — `rounds 57   cost $14.20 (3 unknown)   tokens 41.2M   halted 4 · exited
2   median 23m   bindings 12 · builders 3` — then the grid:

```
started           binding       rnd  builder                               outcome        commits  tree    gate   tokens   cost     duration
2026-09-20 22:01  persist       r5   claude/anthropic/sonnet               reported       +1       clean   pass   1.2M     $0.42    27m
```

`/` opens the query line, prefilled with the current query; `enter` applies it,
`esc` cancels, and a parse error is shown under the input with the previous
query kept. The grammar is exactly `relay history -q`'s:

```
/ harness:agy outcome:halted since:30d
/ auth cost>1            (then b to regroup by builder)
/ by:day since:14d
```

`b` regroups by the next axis (binding, repo, feature, builder, harness,
provider, model, day, outcome); groups carry rounds, reported, halted, commits,
tokens, cost and the last round's date, and `enter` on a group expands its
rounds beneath it. `s` cycles the sort column for the level under the cursor
(rounds: started, cost, tokens, duration, commits; groups: cost, rounds,
halted, last) and `S` flips the direction; `r` re-queries now, and the fleet's
tick re-queries at most every 10 seconds while the screen is up. `enter` on a
round row opens the fleet pointed at that binding and round -- scope `all` is
turned on first when the binding is not live. The query text and the sort
column are remembered in `ui.json`.

Below 140 columns the grid drops `gate`, below 120 `tree` and `commits`, below
100 `tokens`; `cost` always stays. A round's cost reads `unknown` as `?`, a
round with no usage at all as `-`; archived rounds are dim; the same filter
grammar is documented in full under `relay history`.

### `relay ui`'s `all` scope: every binding, not just today's

`a` toggles the rail between `live` (today's bindings, the default) and
`all` -- every binding the database has ever recorded (#172), read through
the same `relay.Bindings` query `relay history --here` uses: inside a git
repo, only that repo's bindings; otherwise every one. `all` is persisted in
`ui.json`, so the ui reopens in whichever scope you left it.

In `all` scope the rail is the live rows exactly as `live` shows them,
followed by every database row not already live, dimmed and never
reordered by attention: a name, `archived <date>` where the state word
goes (or `done` for a binding the database recorded but no tarball ever
archived), and a second line `rN · <age> · feature <label>` (the feature
clause only when one is set). Selecting an archived row opens the same
five tabs, reading the database instead of files -- `terminal` renders the
round's stored transcript rows and does not follow a tail, since nothing
about an archived round is still moving. A database relay cannot open
shows `no database: <err>` in the rail and the scope stays on `live`;
`relay ui` never exits over it.

### Headless builders

A builder is a process relay runs, one fresh process per round; each
`relay send` starts the harness's non-interactive form -- `agy -p …`,
`claude -p …`, `opencode run …` -- in the binding's tree with the round's
prompt, writes the harness's streamed JSON events to
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

What this means in practice:

- **No dialogs.** The process runs with stdin closed. If a harness needs
  permission prompts answered, put its `--dangerously-skip-permissions`/`--auto`
  extra arg in `candidates.json`.
- **No memory across rounds.** Every round is a fresh process. relay plans
  already carry their own context (worktree table, conventions, "stop rather
  than improvise"); headless makes that a hard requirement.
- **`relay status`** shows `builder  headless  <kind>  <idle|working|exited N>
  pid P since HH:MM` and the log's last three lines as `log` rows.
  `relay ui`'s terminal tab shows the log file.
- **Exit without a report** is logged as an `exit` entry (exit code and the
  log's last 20 lines) and the daemon switches builders, up to `max_switches`
  (a switch caused by a rate-limit gate is not counted); then
  `NEEDS YOU`. The one exception is a builder whose
  supervisor died with the daemon itself (a systemd restart, `kill -9` of
  the process tree): relay tells that apart from a real builder death and
  relaunches the same candidate on the same round instead, uncounted.
- **`done` and `unbind` stop the process** if a round is running. A stop that
  fails is reported, and the binding is still done or unbound. The round budget
  never kills anything: it flags `NEEDS YOU` and leaves the process alone.
- **`relay unavailable`** on the provider mid-round kills the running process
  and starts the next candidate on the same round.
- **opencode 2.x** headless builders launch `run` with `--standalone` (#256):
  each headless round gets its own private server instead of the one
  `opencode serve --service` shared by every `opencode run` on that machine,
  so a kill, `relay done`/`unbind`, a `relay stop`, or a mid-round switch
  stops the agent for real -- before the fix, the client process died but the
  agent session kept running inside the shared service, still editing the
  worktree relay had already switched away from. `relay doctor` notes the shared
  service (and, when readable, its session count from opencode.db) whenever
  `~/.config/opencode/service.json` exists.

**Scopes.** A local headless round runs in its own transient systemd scope
named `relay-round-local-<binding>-<round>` (an owned remote binding uses its
owner's id where `local` sits). It is a *sibling* of `relay.service`, not a
child: `systemctl --user restart relay` -- what `make service` does -- leaves a
running round alone instead of killing it, so a restart no longer looks to
relay like a builder that "exited without a report". With no `scope` block
configured this is the whole change: a local headless builder moves out of
`relay.service`'s cgroup into its own scope, with no quota and no memory cap --
only its location, and with it restart survival. The gate, a consult and the
verify reviewer each get a scope from the same template too, with a unit name
that says what it is: `relay-gate-local-<binding>-<round>`,
`relay-consult-local-<binding>-<round>-<consult-id>` and
`relay-verify-local-<binding>-<round>-<consult-id>` (an owned remote binding
uses its owner's id where `local` sits, exactly as for a round). `policy.json`'s
top-level `scope` block configures the scope (`enabled`, `slice`, `cpu_weight`,
`cpu_quota`, `gate_cpu_quota`, `memory_max`, `tasks_max`); `gate_cpu_quota` is
the gate's own CPU ceiling, and defaults to `cpu_quota`. `serve.scope` replaces
that block entirely for served rounds, and `scope: {"enabled": false}` opts
out. `allowed_cpus` is a pool of cores (`"0-2"`), and each round is pinned to
one core from it: the gate runs on its round's core, while a consult and the
verify reviewer run on the whole pool. When every core is taken, a round runs
on the whole pool instead. Pinning needs `cpuset` delegated to your user
manager through a root drop-in on `user@.service`, and `relay doctor` checks
this; if systemd refuses it, relay logs one warning and runs unpinned. #314
measured a CPU-bound job pinned to one core using 10–18% less CPU time than the
same job left to float. Every scoped builder, gate, consult and verify reviewer
also gets `GOMAXPROCS` set to the CPUs its scope allows -- 1 for a pinned round,
`ceil(quota)` otherwise -- replacing any `GOMAXPROCS` the daemon inherited, such
as a shell-wide export; this affects Go processes only, and Go's `-p` and
`-parallel` follow it. On a host without a
usable systemd user manager relay logs one warning and runs builders unscoped,
in `relay.service`'s cgroup, exactly as before.

### Progress labels

relay keeps a progress clock on every local binding with an open round and
labels what it sees. The labels are observations, never actions: relay never
kills, nudges, switches or halts on them, and the round budget stays the only
automatic halt. Killing a stalled builder stays the human's decision -- `relay
done`, `relay unbind`, or `relay stop`.

The signals are the working tree (its fingerprint is `HEAD` plus `git status
--porcelain`, hashed -- no diff, no snapshot) and the builder's output: the
builder's stream file (`NNN-builder.jsonl`) mtime. Each signal keeps the time it last changed, and the daemon samples at
most once every `progress_interval_ms` (default thirty seconds).

- **`stalled <age>`** -- no signal has moved for `stall_after_ms` (default
  fifteen minutes) while the round is open and the builder is not blocked. The
  label replaces `working` in `relay status` and `relay ui`; a stalled binding
  is still `ACTIVE` with `relay wait` still waiting. The label clears when a
  signal moves again or the process exits, and the daemon fires one
  `builder_stalled` hook event per episode and none when it clears.
- **`exploring <age>`** -- the output or screen is changing but the tree has
  not for `explore_after_ms` (default twenty minutes). A label only: some plans
  are read-heavy, so it fires no hook event and no notification.
- **`stale <age>`** -- a `NEEDS YOU` binding has sat unacted for
  `stale_after_ms` (default four hours). The age is measured from the halt, or
  from the newest log entry when the binding has none. The word follows the
  state word in `relay status`, joins the card in `relay ui`, and puts the row
  first inside its attention group; the daemon fires one `binding_stale` hook
  event per episode.

A binding with no readable signal at all -- no git and no stream -- is never
labelled.

When a round stops -- exit without the marker or the round budget -- relay
scans the builder's last output for the harness's
rate-limit text and records a `rate_limited` gate (`source relay`, the
matched line) on a match, parsing the line's own reset time when it names
one (`Resets in 2h48m52s`, `resets 7pm`) or using `limit_gate_default_ms`
otherwise, then switches uncounted toward `max_switches`. `relay
unavailable` still overrides; `relay available` undoes a false positive.

### Remote builders: the server

`relay serve` runs a remote-builder server: an HTTPS listener over enrolled clients and an autonomous daemon loop that runs headless builders on the server host without a local planner or GUI.

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
- `relay serve log --owner <label|id> <name>` prints that owner's binding log, with `relay log`'s `--round`, `--after`, `--json` and `--follow`. Read-only: `--owner` is an exact label or an exact client id, and nothing is stamped or created.
- `relay serve show --owner <label|id> <name>` prints one round's plan, report, diff, drift, log or transcript, with `relay show`'s flags. Read-only, and it reads live bindings only: it never opens the database, so a non-live binding reads as "binding not found".
- `relay serve tab [--owner <label|id>] [--since 7d] [--by binding|model|provider|owner]` sums recorded usage: with `--owner` for that one owner, and without it for every owner, where a binding group reads `<label>/<name>` and `--by owner` groups by owner label. Reads only; it creates nothing.
- `relay serve clients` lists enrolled clients and their revocation status.
- `relay serve gc --abandoned <duration>` prunes abandoned bindings whose last activity is older than the threshold by archiving them (running rounds are never touched).

What `relay serve` does not do: it runs no planner and provides no administrative verbs over the network (administration happens on the server host). Tenants are protected from each other over the wire and from a passive network, but not from the server admin or from each other at the OS level where all builders run under the same unix user. Tenant isolation by unix user or container is tracked in #204.

### Remote builders: the client

A remote binding is an ordinary binding whose builder runs on someone else's
machine, over a signed, pinned HTTPS connection instead of a local process.
It has no worktree of its own: `relay send` ships a bundle of your branch's
history alongside the plan, and the daemon polls the server for the round's
state the same way it polls a local builder.

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
only, exactly like a local builder's worktree branch. If the round closed with
uncommitted changes on the server, they land on a side ref,
`refs/relay/<name>/round-<N>`, whose parent is that round's commit on
`relay/<name>`; the report names it. If that fast-forward collides with a
branch you have checked out locally, relay retries quietly next tick --
check out something else, then `relay pull`.

`relay send` also ships your repository's tags as data beside the bundle, and
the server sets each one whose commit it already has, so a tagged server
worktree can `git describe --tags`. Catch-up fetches the builder's own stream
file (`NNN-builder.jsonl`) alongside the report, diff and log, so a remote
round's failure carries its detail to the client.

What is refused: `--cwd` cannot be combined with `--server` (a remote binding
is add-only, never bound to an existing directory); `relay ask`
("consults are local-only"); `relay fork` from a remote source ("fork across
servers is not supported"); and `relay bind --resume --rebind` (or
`--builder`) against a remote binding ("cannot change a remote
builder; unbind and add" -- a binding's mode is fixed at creation, the same
rule a headless binding follows). `relay done` and `relay unbind` tell the
server first, and only change anything locally once it agrees (a 404 from
the server is treated as already gone, and proceeds).

What `status` and `doctor` show: `relay status` and `relay ui` name a
remote binding's builder by its server (`zen`), with the
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

A headless builder that exits without writing its report file still closes the
round, and relay delivers its report flagged `unmarked`. Nothing is scraped and
nothing is guessed.

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

Every mutating verb (`bind`, `add`, `fork`, `send`, `done`, `unbind`) ends by
listing, on stderr, every *other* binding that is waiting on a human — a halt,
a dead builder, a lost planner — with how long and the verb that resolves it,
e.g. `waiting on you: api round 4 halted 23m -- relay status --name api`. The
exit code is unchanged; it is a reminder, not a refusal.

Relay does not sequence them and does not merge their trees. The planner
decides how many builders it needs, which run in parallel and which wait, and
integrates the results — relay only carries plans out and reports back.

**`relay add` is not `relay fork`.** A fork continues a timeline: it copies
round history through a chosen round and starts at the round after it. A peer
starts at round 1 with an empty log, because it is not a continuation of
anything.

Headless builders are the cheap way to run several: no terminal per builder,
no idle harness holding memory. `relay add --name api` gives a peer its own
worktree and a fresh process per round.

### Edges (triggers)

When a plan splits work across two bindings -- one writes the contract,
another builds against it -- the planner otherwise sits in the middle of
every handoff: wait for the report, read it, `relay send --name other --file
…`. An **edge** declares that handoff up front:

```
relay edge add api --when report --then send --target client --prompt ./client-plan.md
```

This says: when `api`'s current round closes and its report exists, hand
`client-plan.md` to `client` as its next round. Relay never inspects the
artifact; it checks existence -- the same fact `queueReport` checks for the
report itself -- and delivers a prompt the planner wrote ahead of time.
`--when` names the artifact: `report`, `diff`, `done` or `gate`. `--then`
is `send` -- the only thing an edge does today; `then: ask` is a follow-up.

**Queue by default, fire on request.** `--mode queue`, the default, holds the
handoff in front of the planner as a payload naming the exact command to run
-- the same held-payload delivery a report gets. `--mode fire` runs the send
itself, unattended, for a chain the planner has run before and trusts. A fire
that cannot reach its target (busy, gone, gated) is never lost: it is
downgraded to the same queued payload, with the failure named, so the planner
still sees the handoff. Either way the edge fires exactly once, on its own
round's close -- an edge missing its artifact at close is marked skipped and
never reconsidered, since a round's artifacts are final once it closes.

`relay edge list <source>` prints one line per declared edge, Fired and
Result included; `relay edge rm <source> <id>` drops one -- removing a fired
edge is fine. Builders never declare edges; the CLI is the planner's.

### Forking a binding

`relay fork` branches a new binding from an earlier round of an existing binding:

```
relay fork webshop --round 2 --new-name webshop-alt
```

- **What is copied:** Round history up through `--round`: `log.jsonl` entries (marked confirmed with no pending delivery) and all round artifacts (`NNN-plan.md`, `NNN-report.md`, `NNN-question.md`, `NNN-diff.patch`).
- **What is not copied:** Working tree code state is not rewound. By default, relay creates a fresh git worktree at `~/.local/state/relay/.worktrees/<new-name>` on a new branch `relay/<new-name>` cut from current `HEAD` of the source repository. Pass `--cwd DIR` to bind to an existing directory instead.
- **State layout:** Relay keeps worktrees it creates under `.worktrees/` directly beside `.archive/` in the state root (`~/.local/state/relay/.worktrees/`).
- **Teardown rule:** Relay removes a worktree it created only when it is clean (`git status` reports no untracked or uncommitted changes), and never removes the branch. If uncommitted edits remain or git is unavailable, `relay unbind` and `relay gc` leave the worktree untouched and report the exact command to inspect or remove it manually.


### Pausing a binding

`relay pause` is the third lifecycle state between ACTIVE and DONE. It
releases what an idle binding is holding — its worktree on disk and its
builder process — while keeping the branch and the round log.
`relay bind --resume --name <n>` brings it back at the same path on the same
branch.

```
relay pause webshop            # release the worktree
relay pause webshop --commit   # commit everything on the binding branch first, then release
relay bind --resume --name webshop   # restore the worktree and spawn a fresh builder
```

- **Refused while a round is open**: wait for it, or `relay done`. There is a
  builder that may still be writing.
- **A dirty tree is refused** unless `--commit`. With `--commit`, everything in
  the tree is committed on the binding's branch (message
  `[relay] <name>: paused after round N`) before the worktree is removed. relay
  prints the commit's short sha.
- **The branch is never removed** and neither is the log; pausing keeps the
  record and the commits, exactly as `done` does.
- **Nothing is closed**: a paused binding's builder is a process, and pause
  simply releases the worktree; there is no terminal to close.
- **Resume rebinds**: a paused binding has no builder identity left, so
  `bind --resume` implies `--rebind` and starts a fresh builder on the candidate
  order (or a fresh headless endpoint). The round number is unchanged and the
  log records `resumed`.
- **Remote and `--cwd` bindings are refused**: a remote binding has no local
  worktree (`relay done` ends it), and a `--cwd` binding drives a tree
  relay did not create, so there is nothing to release.
- **`gc` leaves PAUSED alone** — it sweeps only `DONE`. `relay unbind` works on
  a paused binding when you want it gone; use `--archive` to keep the log.

### Stopping a round

`relay stop` ends an open round on purpose. The builder runs with no stdin, so
there is nothing to ask it: `relay stop` kills the process and closes the round
without a report (`noreport stopped`).

```
relay stop webshop               # end the round now
```

- **A stop is not a failure**, so it never charges a switch and never excludes
  a candidate, unlike an exit-without-report.
- **`stop` then `pause`** is the sequence for parking a binding between
  rounds: `stop` ends the round, and `pause` then releases the worktree.
- **Send and the round close clear the request**, so a stop never outlives
  the round it was made for. `stop` refuses a binding that is already `DONE`
  or `PAUSED`.

A remote binding's round is stopped on its server, and the binding stays: on
both sides only the round ends. A round still queued on the server is dropped
from the queue instead, since there is no process to kill. A server that
predates this refuses the request with a pointer to `relay unbind webshop`,
which stops the round and drops the binding.

### Landing a branch

`relay land <name>` is the mechanical version of the sequence a planner
types by hand after a green round. It runs, in order, and stops at the first
failure with **nothing pushed** except where noted:

```
relay land webshop            # fetch, rebase onto origin/main, gate, push, print the PR command
relay land webshop --pr       # ... and run `gh pr create --head relay/webshop --base main --fill`
relay land webshop --merge    # merge origin/main in instead of rebasing (no rewrite, no force)
relay land webshop --onto trunk   # for a binding that recorded no base branch
relay land webshop --force    # land while a round is still open
relay land webshop --no-gate  # skip the gate for this land (the result says "skipped")
```

1. **Preconditions.** The binding must be a local binding with a worktree of
   its own (`--cwd` bindings have none), must not be `PAUSED` or `DONE`, and
   must have a clean tree -- `land` will not commit for you, and it will not
   rebase a tree a builder is still writing to. A round still open is refused
   unless you pass `--force`.
2. **Fetch and rebase.** `git fetch origin <base>`, then
   `git rebase origin/<base>`. A conflict aborts the rebase, lists the
   conflicting paths, changes nothing and exits **3**.
3. **Gate.** The binding's gate runs on the rebased tree (`sh -c "<gate>
   2>&1"`), with its output in `~/.local/state/relay/<name>/land-gate.log`. A
   failing gate pushes nothing and exits **2**.
4. **Push.** `git push -u origin <branch>`, with `--force-with-lease` when the
   branch already exists on the remote -- the rebase rewrote it.
5. **PR.** With `--pr` and `gh` on PATH, `gh pr create --head <branch> --base
   <base> --fill` runs and its URL is recorded. Otherwise the exact command is
   printed for you to run. If `gh` fails *after* the push, relay says so: the
   branch is on origin, and only the PR is missing.
6. **Log.** `landed <branch> -> <base> [pr <url>]`, with the time recorded on
   the binding. `relay status` then shows `landed` on the round line until the
   next `relay send` clears it.

The base *branch* is recorded at `add`/`fork` time: the branch the source
checkout had checked out (e.g. `main`). An older binding, a `--cwd` binding,
and a `--branch` adoption have none, so `land` asks you for `--onto`. Use
`--merge` when the branch must keep its history or when someone else may be
working on top of it; the base is then merged in rather than rebased onto, and
because nothing is rewritten the push needs no lease.

**`land` never merges a pull request, and never deletes a branch or a
worktree.** It is the mechanical half of the job and stops at the PR. Merging
is a separate, deliberate act: merge only after `gh pr checks <n> --watch`
has finished with every job passing -- a green first job is not a green CI
(the rule in `CLAUDE.md`, repeated here because `land` is where it is easiest
to forget).

### Cleaning up finished bindings

A binding leaves `$XDG_STATE_HOME/relay/<name>/` behind (defaulting to
`~/.local/state/relay/<name>/`): `bind.json`, `log.jsonl`, and every round's
plan, report, patch (`NNN-diff.patch`) and captured dialog. It also holds a
`.viewed` sidecar (#143): `relay diff`, `relay log` and `relay show` each
stamp its mtime after a successful print of a live binding, and `status`'s
`unread`/`●new` compares the binding's newest report against that stamp.
`relay ui` never writes it directly -- it stamps through the same call `diff`/
`log`/`show` use, keeping `ui` itself read-only of `bind.json` and everything
else in the directory. `relay done` stops relaying and, when the binding's worktree is clean and
no round is open, removes the worktree so its branch can be checked out
in the main repo (`removed worktree ... (branch relay/x is free to check
out)`); a dirty tree or an open round is kept and `relay gc` retries
when it is clean. The binding directory itself is never removed by `done`
— the log is the record of what the planner actually told the builder.
`relay bind --resume <name>` puts a removed worktree back on the same
branch at the same path; a DONE binding may then be rebound with
`--rebind`, since the old builder cannot work in the recreated directory.

relay deletes a branch in **zero** places: not at `unbind`, `gc`, `done`, nor
on an add rollback. The one exception is a `relay/<name>` branch `add --server`
created seconds earlier and must undo because the server refused the binding.

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

`gc` only touches bindings the planner marked `DONE`. A `PAUSED`, a `BROKEN`
or an `ORPHANED` one is left alone: the broken/orphaned pair still needs a
human, and clearing it would throw away the state that explains why it stopped,
while a paused binding is released but alive — `relay bind --resume` brings it
back, and `relay unbind` is how to clear it.

Snapshot tree objects created during round diff capture are written directly to
git's object database unreferenced. They never alter repository refs, branches,
or the working index, and they are reclaimed automatically by the repository's
own `git gc`.

**What a binding records.** Beyond its round history and live state, a fresh
`bind`, `add` or `fork` fills in four more facts about the binding: which
repository it works in (the origin URL, normalised, and the git common
directory — best-effort, so a directory git can't read leaves this blank
rather than failing the command), the `--feature` label grouping it with
other bindings (a fork inherits its source's unless you pass your own), which
binding and round it was forked from, and the planner's own harness
transcript file path, when relay can locate one at bind time. None of this
changes what you see day to day; it exists for the coming history database
below.

### The database

relay keeps a pure-Go sqlite database at `~/.local/state/relay/relay.db`,
beside `ledger.json` and `availability.json`. Files stay the write side --
`bind.json`, `log.jsonl` and every round file are still what relay itself
reads and writes day to day -- but an ingester reads a binding's directory,
live or archived, and upserts everything it holds (the repo, the planner,
the binding, every round with its outcome and builder, every log event,
every plan/report/diff/drift/gate-log/question/answer/consult artifact, and
every transcript record) into the database. The daemon runs it over every
live binding at the end of each tick, with per-file cursors so an idle tick
writes nothing; `relay db backfill` runs it once over every archived
tarball and live directory on a machine, and is safe to re-run -- the same
cursors make a second pass a no-op.

```
relay db path                                          print the database path
relay db migrate                                       open the database (creating and migrating it if needed) and print its schema version
relay db stats                                          row counts per table, on-disk size, schema version, and the newest round
relay db backfill [--dry-run] [--archive-only|--live-only]
                                                         ingest every archived tarball (oldest first) and every live binding once
```

`relay db backfill` prints one line per source -- `<name>  <stamp>  rounds N
events N artifacts N transcript N`, or `<name>  FAILED: <err>` for a source
it could not read -- and exits 1 if any source failed, after finishing the
rest. `--dry-run` opens every source and ingests into a scratch database
instead of the real one, so the printed counts (prefixed `would`) are real
without writing anything the machine keeps; `--archive-only` and
`--live-only` narrow it to one half of the state directory. A database built
before events were linked to their rounds has every `event.round_id` null;
delete `relay.db` and run `relay db backfill` again to rebuild it with the
links so `relay show --log` and a past binding's `log` tab scope correctly.

### relay history

`relay history` reads the database, not the filesystem: one line per round
across every binding relay has ever recorded -- live or archived -- newest
first.

```
relay history [--here|--repo <url|dir>]   filter to a repo: --here resolves the current directory's
                                           origin url (or its git common dir with no remote);
                                           --repo takes either form directly
              [--feature LABEL]           filter to a --feature label
              [--binding NAME]            filter to one binding name
              [--planner SESSION]         filter to one planner session id
              [--harness K] [--provider P] [--model M]
                                           filter to the round's builder columns
              [--candidate TOKEN]         filter to one harness/provider/model token
              [--outcome O]               filter to one round outcome: reported, halted, exited,
                                           switched, done_no_report, open
              [--since D] [--until D]     only rounds started in this window: 24h, 7d, or YYYY-MM-DD
              [--archived|--live]         archived bindings only, or live bindings only (default: both)
              [--limit N]                 max rows to print; 0 = all (default 200)
              [--json]                    a JSON array of RoundRow, `[]` when empty
              [-q "<query>"]              filter with the query language below
              [--by <axis>]               regroup the result: none, binding, repo, feature, builder,
                                           harness, provider, model, day, outcome
              [--rows]                    with --json --by, include each group's Rows
```

Every flag is one field of the shared `Filter` the ui's coming `all` scope
and dashboard query too. `--archived` and `--live` together, and an
`--outcome` outside the enum above, are usage errors. A plain line looks
like:

```
2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  $0.42  (archived)
```

started time in the local zone; the binding name, truncated with `…` past
12 characters; the round; the builder candidate; the outcome; commits
(`-` when unknown); the worktree's tree state at close; cost (`$0.42`
measured, `~$0.42` estimated, `unknown`, or `-` when the round recorded no
usage at all); `(archived)` for a round from a binding `gc` has packed away.

`-q` takes the query language the dashboard's filter line shares
(`docs/specs/2026-09-21-dashboard-design.md` §3):

```
query  := token*                              whitespace separated
token  := key ":" value                       equality; "quoted" for spaces
        | numkey op number                    op in > < >= <= = 
        | word                                case-insensitive substring of binding, repo or feature
key    := binding repo feature planner harness provider model candidate outcome
          report state gate basis server mode since until archived by
numkey := cost tokens commits duration round
values : outcome reported|halted|exited|switched|done_no_report|open
         report  done|halted|blocked|deferred|unstructured
         gate    pass|fail|timeout|error
         basis   measured|estimated|unknown
         mode    pane|headless|remote   (pane: history only)
         archived true|false
         since/until 24h|7d|YYYY-MM-DD
         by      none|binding|repo|feature|builder|harness|provider|model|day|outcome
         duration minutes; tokens = in+cache+write+out; cost in USD
```

`by:` (or `--by`) regroups the result instead of printing one line per
round: one row per axis value with rounds, reported, halted, commits,
tokens, cost and the last round's date. A flag overrides the same key in
`-q` and prints `note: --<flag> overrides <key>:<value> from -q` on stderr.
An unknown key, a bad enum value or a bad number is a usage error (exit 2).

```
$ relay history -q "harness:agy outcome:halted since:30d"
$ relay history -q "auth cost>1" --by builder
$ relay history --by day --since 14d
```

### relay show

`relay show` prints one round's plan, report, diff, drift, log or
transcript. A live binding is read straight from its files, exactly as
today; anything not live -- an archived binding, or one this machine's
database otherwise knows about -- is read from the database instead, so a
round from months ago renders the same way a live one does.

```
relay show <name> [--round N]                      the round to read; default: the newest completed one
                   [--plan|--report|--diff|--drift|--log|--transcript]
                                                     which section; default: --plan; only one may be given
                   [--json]                         the ShowResult as JSON (Events included for --log)
```

A header line goes to stderr -- `<name> round <N> of <Rounds> · <section>`,
with `· archived <date>` appended for a non-live binding -- so stdout is
always just the section itself and safe to pipe. A round with no such
section (an open round with no diff yet, say) prints `no <section> for
round N` and exits 0 rather than erroring. `--log` prints the round's
events exactly as `relay log` does; `--transcript` prints the builder's
rendered stream. Reading `--round N` outside the binding's round count, or
naming a binding neither live nor in the database, exits 1:

```
$ relay show api-auth --report
api-auth round 3 of 4 · report
report text here
```

### done and unbind are the destructive verbs

`relay done` and `relay unbind` both require a binding name (`relay done ai`, or
`--name ai`) or `--pick`, which lists the bindings and runs the verb on the one
you choose. Neither resolves the current directory for you: a bare `relay done` once ended a live
loop by accident, and the recovery is `relay bind --resume --name <name>`.
`--pick` is explicit for the same reason -- a bare verb never opens a picker,
so a planner agent can never fall into one.
And because a popup takes focus the instant it opens, `Enter` on a binding
that is not `DONE` asks first -- `mark webshop done? it is ACTIVE in round 5`
-- and only `y` proceeds; any other key returns to the list.

## Status line

`relay statusline` shows this planner's live bindings, one row each, under
the Claude Code prompt; it shows nothing on error and never probes a builder.

Add this to `~/.claude/settings.json`:

```json
"statusLine": { "type": "command", "command": "relay statusline", "refreshInterval": 1 }
```

One precondition: `relay` must be on the `PATH` of the Claude Code process.
The planner session is identified by `RELAY_PLANNER`, which the relay plugin's
hook exports.

Claude Code renders a few cells less than `COLUMNS`; relay subtracts 4 by
default (measured on the fullscreen TUI). If the right-hand `age · STATE`
cell is clipped or sits short of the edge, measure yours and set
`RELAY_STATUSLINE_MARGIN` in the environment Claude Code starts from. To
measure, put this in `statusLine.command` for one refresh and count the
cells before Claude Code's `…`:

    sh -c 'printf "%s" "$(seq -s . 1 $COLUMNS | cut -c1-$COLUMNS)"'

Each row is `○ name  rN · builder · what relay is waiting on  …  age · STATE`,
where `builder` is the harness segment of the candidate token, and `age` is
time since the last plan, report or question crossed.

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
- `dialog_patterns` — extra regexes appended to the harness defaults, matched against a runner's output to detect a blocking dialog; a match refuses `send` as blocked; extend-only.

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

Under `workspace-write`, codex also cannot write to Go's default build cache (`~/.cache/go-build`), so a Go plan fails at `go build` unless the plan sets `GOCACHE` inside the worktree or `/tmp`, or your `~/.codex/config.toml` lists it under `sandbox_workspace_write.writable_roots`. relay adds only its own state directory.

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

`relay candidates` prints the configured tokens with their roles.

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
  "stall_after_ms": 900000,
  "progress_interval_ms": 30000,
  "explore_after_ms": 1200000,
  "stale_after_ms": 14400000,
  "scan_patterns": ["(?i)<instruction-tag"],
  "classify": { "provider": "jev", "model": "jev-latest", "injection_threshold": 0.7, "timeout_ms": 4000 },
  "gate": { "default": "make check", "timeout_ms": 600000, "regate": 0 }
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
defaults to one hour. `stall_after_ms` is how long a live headless
builder's stream may go without an event before `relay status` and
`relay ui` label it `stalled`; absent defaults to fifteen minutes, and
must be `> 0` when present. `progress_interval_ms` is how often the
daemon samples a binding's progress signals while its round is open;
absent defaults to thirty seconds, and must be `> 0` when present.
`explore_after_ms` is how long a builder's output or screen may keep
moving while its tree has not before relay labels it `exploring`; absent
defaults to twenty minutes, and must be `> 0` when present.
`stale_after_ms` is how long a `NEEDS YOU` or `HELD` binding may sit
unacted before relay labels it `stale`; absent defaults to four hours,
and must be `> 0` when present. `scan_patterns` is an optional list of extra
regular expressions appended to relay's built-in instruction-shaped scan list;
each pattern must compile. `gate` configures the default acceptance command
(see [Gate](#gate) below): `default` is the command a binding gets when it
does not set `--gate` or `--no-gate` itself, absent or `""` meaning no gate;
`timeout_ms` bounds one gate run, absent defaulting to ten minutes, and must
be `> 0` when present. `regate` is how many automatic repair rounds a new
binding may open after a failing gate (see [Repair
rounds](#repair-rounds) below), absent or `0` meaning none, and must be
`>= 0` when present.

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

### Roles

A **role** is what a job *is*: its shape (`writer` or `reader`), whether it is
gated, and the agent definition it resolves per harness kind. A **candidate**
is one way to fill a role -- a `harness/provider/model`. Each role picks its
own candidates, in order, at its own tier. Three roles are built in: `builder`,
`reviewer` and `researcher`.

Roles live in `$XDG_CONFIG_HOME/relay/roles.json` (default
`~/.config/relay/roles.json`), an object keyed by role name:

```json
{
  "builder": {
    "candidates": ["claude/anthropic/sonnet", "opencode/openrouter/z-ai/glm-5.3-flash"],
    "tier": "edit",
    "definitions": { "claude": { "agent": "plan-executor", "requires": ["researcher"] } }
  },
  "reviewer": {
    "candidates": ["claude/anthropic/opus"]
  },
  "scout": {
    "shape": "reader",
    "candidates": ["claude/anthropic/haiku"],
    "definitions": { "claude": { "agent": "my-scout" } }
  }
}
```

- `shape` -- `writer` or `reader`. The built-in rows have one already; a new
  role must give it, and must be a `reader` for now: it runs with `relay ask
  --role <name>`.
- `gate` -- for a writer, whether its round closes on a gate.
- `definitions.<kind>.agent` -- the definition relay launches for that harness
  kind; `requires` names the definitions that agent dispatches to.
- `candidates` -- the role's own candidate tokens, most preferred first.
- `tier` -- the role's permission tier.

The built-in rows (builder, reviewer, researcher) are defaults, and a row
overrides them field by field. Unknown keys are warnings, not errors.

**Custom definitions.** A definition is *custom* when its name -- or a name it
requires -- is not one relay ships. relay never installs or refreshes a custom
definition; each harness looks for it in its own place:

- claude: `~/.claude/agents/<n>.md`
- opencode: `~/.config/opencode/agents/<n>.md`
- agy: `~/.gemini/config/agents/<n>.md`
- codex: `~/.codex/<n>.config.toml`

A missing custom definition gates its candidates for that role only, and
`relay doctor` lists it.

**Bring your own agent.** Everything relay's round protocol needs travels in
the prompt relay sends: the working tree and its `git status` check, the plan
path, the report path, the done marker and the closing `relay` block. A custom
definition only shapes behaviour; `requires` names the definitions your agent
dispatches to (the shipped builder requires `researcher`).

**Migrating.** Without `roles.json`, relay keeps reading `candidates.json`'s
`roles`/`tier` and `policy.json`'s `order`/`tier`, and nothing changes. `relay
roles init` writes `roles.json` from them (`--dry-run`, `--force`). Once the
file exists, those fields are ignored and `relay doctor` lists them -- so
delete them only after every relay process on the machine is upgraded: an
older relay does not know `roles.json`.

**Seeing it.** `relay roles` lists each role's shape, candidates, tier and
definitions; `relay policy` adds `(roles.json)` per role; `relay status --json`
has `builder_definition` for a custom builder.

**Remote builders.** A server resolves roles from its *own* config, not the
client's.

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

`relay available` also clears the gate on every server your bindings name and
prints each server's answer; on a box running `relay serve`, use `relay serve
available`.

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

A candidate whose harness role files are missing on disk is gated the same
way (`roles missing` in `relay policy`, `relay candidates`, `relay
doctor`), fixed with `relay agent install --kind <kind>` -- except an
explicit `--builder` pick of it is **refused**, not allowed to proceed,
because it cannot succeed. `relay serve` logs each configured harness
kind's role coverage once at startup.

#### Mid-round switching

A builder relay spawned can be replaced by the daemon while a round is
open, in two cases:

- its process exits without writing a report;
- you gate its provider with `relay unavailable` -- which is how you
  tell relay a running builder hit its limit. The command names the
  bindings the daemon will switch.

The daemon resolves `builder` again through `policy.json` order and the
ledger (an omitted token, so the order applies even to a builder you
named), starts the pick in the **same** tree, and hands it the **same**
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
the next candidate. The halt always states its reason, even on a round
that has already notified once; a `relay send` re-send resets the
round's switch budget, since the human asked for another attempt.

A builder gone between rounds is `BROKEN` as before: `relay bind --resume`
starts a fresh one. relay selects a role for every builder it starts, with
`--agent` on the launch line.

`aliases.json` from earlier versions is no longer read.

#### History

Every gate relay records -- a limit you report, a spawn failure it hit
-- is also kept for 30 days in `~/.local/state/relay/availability.json`, by
provider and local hour. `relay policy` shows it twice: a `limited 3x
around 21:00 (30d)` note on a candidate whose provider was limited
within an hour of now, and a `history` block with a 24-hour row per
provider. It changes nothing about which candidate is picked; it is the
cue to write a different order, or to `relay unavailable` a provider
before it bites. Older installs are migrated on first read.

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
| codex | (none) | refuse | `-s workspace-write -c sandbox_workspace_write.writable_roots=["<binding state dir>"]` | `--dangerously-bypass-approvals-and-sandbox` |

opencode does not support `read` or `edit` tiers because it has no read-only or edit-only CLI flag. Choosing `read` or `edit` for an opencode candidate is refused immediately with an error directing you to use `--tier harness` (where `opencode.jsonc` decides) or `--tier yolo` (`--auto`).

codex does not support the `read` tier: `-s read-only` cannot write the report, marker, question and findings files relay stages under `~/.local/state/relay/<binding>/`, and codex ignores `writable_roots` under read-only. Choosing `read` for a codex candidate is refused with an error directing you to `--tier edit` or `--tier harness`. At `edit` relay adds the binding's state directory as a writable root; that is the only path outside the worktree the sandbox lets the builder write.

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

### Per-round tiers

Every builder runs a new process for each round, so a round may temporarily override the tier with `relay send --tier <tier> [--allow-yolo]`. The override applies to that round only, and resets to the binding's default tier when the round completes.

`relay send --builder <token>` moves the binding to another configured candidate from this round on. It is refused while a round is open (stop it first with `relay stop`). An explicit pick of a gated candidate is recorded and proceeds, as with `relay add --builder`. The binding's tier is re-derived for the new candidate. On a remote binding the server must advertise the `builder` feature.

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

A round's report entry also carries its **builder session**, `builder_session`
in the JSON that `relay log --json` and `relay show --json` print: the harness
session that built the closed round, so a report read two rounds later can
still name the session that wrote it. It is the session the round's stream
announced in its first event. It is absent when the harness named none -- relay
never guesses. The one-line form appends ` session=<kind>:<id8>`, and it is what
`ask --round` resumes.

### Verify

`relay send --verify` -- or `policy.json` `"verify": {"default": true}` -- marks
the round: when it closes, **after the gate** so the reviewer sees the gate's
own output, relay runs a read-only **reviewer** over the finished round and
records its verdict. `--no-verify` overrides the policy default for one send;
the two flags are exclusive.

The reviewer is an ordinary consult (`relay ask --role reviewer`) with two
differences. It runs **headless**, so its findings are its final message rather
than a file, and it runs in a **throwaway worktree**: relay creates a detached
worktree at the builder's HEAD under
`~/.local/state/relay/.worktrees/.verify/<name>-<NNN>`, launches the reviewer
there, and removes the tree once the consult reaches any terminal state. That
isolation is what lets the reviewer run tests without touching the builder's
tree or the planner's checkout, and it is why the reviewer consult's tier is
`policy.json` `tier.reviewer` when set, else the candidate's, else **yolo** --
for this consult only, in this tree only. The reviewer's role definition still
tells it not to edit; relay cannot observe writes.

The question names the round's plan, report, diff and gate log, and asks the
reviewer to end its findings with exactly this block:

```
verdict: accepted | rejected
reasons: ["..."]
```

relay parses the last ` ```relay ` block for those two lines. Anything else --
no block, an unreadable one, a verdict that is neither word -- is recorded as
`unstructured` and delivered as prose for the planner to read. `verdict` and
`reasons` ride on the findings entry, and the newest verdict shows in
`relay status` as `verdict: rejected (2 reasons)` for as long as it judges the
round just closed.

**A verdict decides nothing.** `rejected` does not reopen the round, stop the
binding, or summon a human: the report is delivered exactly as it always was,
and the human still judges. `relay wait --verdict` is a follow-up.

A round whose reviewer could not be started -- no reviewer candidate, every one
of them gated, no runner, a worktree that could not be created -- closes
normally with one `verify skipped: <why>` note in the log. A crashed relay can
leave a tree under `.worktrees/.verify/`; remove it with
`git worktree remove <path>`.

### Repair rounds

A failing gate does nothing on its own: the round closes, the report goes to
the planner, and a human judges the diff. A binding can opt into a **repair
round** instead, with `--regate N` on `relay bind`, `relay add`, `relay fork`
or `relay send`, or with `"regate": N` under `gate` in `policy.json` (the
default for new bindings, which `relay fork` inherits from its source). `N` is
how many repair rounds relay may open after failing gates; `0` -- the default
-- turns the loop off, and `--regate` on a binding with no gate is accepted and
inert, since a binding with no gate never fails one.

When a round closes with `gate=fail` and the budget is not yet spent, relay
stages round N+1 in the same tick, after the report has been queued. Its plan
file is written for the builder rather than by the planner: it names the failed
round's acceptance check and the original plan, and carries the last 200
non-empty lines of `NNN-gate.log`, instructing the builder to fix ONLY what the
check reports and to halt and report if no code change can fix it. The hand-off
is exactly a send's -- a fresh builder process -- and the new round's plan
entry is logged with `repair k/M`.

Two bounds end the loop with `NEEDS YOU` instead of another repair round:

- the budget is spent: `gate failed after M repair round(s) (regate N)`;
- the new failure's normalised output equals the previous failure's -- the
  builder changed nothing that mattered -- reported as `gate output unchanged
  after repair`. Timestamps, durations, large integers, hex digests and `/tmp`
  paths are stripped before the two are compared.

The failed round stays closed either way: its own report, diff and `gate=fail`
stand, and relay never writes or removes `NNN-done`. A passing gate or a human
`relay send` resets the count, so the next failure gets a fresh budget.
Headless bindings are the intended case -- the server runs the same reconcile,
so a remote headless binding gets repair rounds too.

## Consults: asking a reviewer

A **consult** is a one-shot agent spawned beside a binding to answer one
question. Unlike a builder, it is not persistent, does not advance the round,
and does not count against the one-writer-per-tree rule: it is a separate
record on the binding, not a binding of its own.

The planner runs, from its own session:

```
relay ask --role reviewer --file q.md webshop
```

relay stages the question and starts the role. The consult reads the staged question, writes its findings to a
file, and replies with only that path. Findings land under the binding's state
directory as `NNN-<id>-findings.md` — the exact path is printed when you ask —
and relay queues them to the planner like any other report, once the file
exists. That file's existence is the only completion gate: relay makes no
judgements about what the findings say.

A consult is a one-shot process: relay starts the harness in its print form,
and the consult's **final message** becomes the findings, which relay writes
to `NNN-<id>-findings.md` and queues to the planner (`--headless` is accepted
and ignored). The same form resumes a closed round's builder session and runs
a verifier at round close. The process is the only thing relay can observe: it is killed at the
consult timeout (10m), and a process that exits without a final message is
reported silent with its exit code and the stream to read. The resolved tier
still gates the pick: at `read`, claude and agy
can run (claude `--permission-mode plan`, agy `--mode plan`), while opencode
and codex cannot honour `read` and are refused.

While consults are running, `relay status` appends ` +Nc` to the binding's row
— only when non-zero, so a healthy binding looks no different. A finished
consult's record is dropped once its findings have been queued. Terminal
consults are not work in flight, so the count does not include them.

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
findings in-band to it, while a reviewer runs as its own relay consult and
hands back a file path.

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

| harness | headless |
|---|---|
| claude | the round's stream (`measured`) |
| agy | the round's stream (`estimated`) |
| opencode | the round's stream (`measured`) |

A binding on `--cwd` shares the planner's directory, so its rounds
are `unknown` (`shared cwd`) rather than counting the planner's spend.

`prices.json` is `{"as_of": "YYYY-MM-DD", "source": "...", "models":
{"<provider>/<model>": {"in": …, "cache_read": …, "cache_write": …,
"out": …}}}` in USD per million tokens, overlaid on the table relay ships;
a model with no row is `unknown`, never `$0`. Mark a subscription lane
with `"plan": true` on its candidate: its rounds record `cost.plan` and
are shown as a quota draw, never as free.

Where you see it: `relay log` prints the round line under each report
(`⎿ opencode/cline-pass/glm-5.3-flash  14m  in 2k  cache 166k (91%)  write 14k  out 12k  $0.41`);
`relay status` adds a `usage` row (newest round) and a `spend` row
(the binding's total: `4 rounds +2c · $1.23 · ~$0.40 · 2 unknown · 2.1M tok`), both
on `--json` as `last_usage` and `spend`; `relay ui` shows the total on
the card and in the header.

A remote builder's round is measured on the server, from the builder's
own stream there, and shipped with the round: the client keeps the
server's figure verbatim instead of reading a record it does not have. A
server built before this ships no figure, and its rounds print
`unknown · remote: server sent no usage`.

While a round is running, `relay status` and `relay ui` show a `live`
figure read from the harness's record on each refresh, and
`relay statusline` appends `live $0.02 · 41k tok` to the row. On
`status --json` it is carried as `live_usage`. The live figure is
estimated (`~$`) unless the harness reports dollars per step (opencode).
It is never recorded and never added to `spend`. Across bindings:

```
relay tab [--since 7d|24h|2026-09-01] [--by binding|model|provider] [--json]
```

sums every round relay has recorded, including bindings `gc` has
archived, one row per group and a total, with the four token columns
`in`, `cache`, `write`, `out` (a sum across models has no meaningful
ratio, so `cache` carries no percentage). Measured and estimated dollars
never share a column; `plan` and `unknown` are counts of rounds. `tab`
is the second exception to the #114 verb freeze, taken because its
sums exist regardless (they are on `status --json`) and a cross-binding
view has no other home.

### Usage stats

```
relay stats [--since 7d] [--json]
```

`relay stats` answers the questions `relay tab`'s money sums do not. It reads
the same logs and archives as `relay tab`, live and archived, locally only:
nothing leaves the machine. `--since` cuts on a round's start, so a round that
started before the cut is not counted.

Each counted round has one builder -- `harness/provider/model`, taken from the
report's usage record when it has one, else from the round's pick, else
`unknown` -- and one outcome:

| outcome | meaning |
|---|---|
| `done` | the report's relay block said `done` |
| `halted` | the report's block said `halted` |
| `blocked` | the report's block said `blocked` |
| `deferred` | the report's block said `deferred` |
| `unstructured` | a report arrived without a usable block |
| `noreport` | the report was noted `noreport` |
| `stopped` | no report; the round was stopped |
| `exited` | no report; the builder exited |
| `open` | no report, no stop and no exit: the round is still open |

`needs-you` and `stalled` are not outcomes because they are live state, not a
property of a finished round: relay records neither in the round log, so there
is nothing to count after the fact. They stay where they are visible, on
`relay status` and in `relay ui`.

`switches` counts the rounds that changed builder mid-round, attributes each to
the provider it left with a reason (`rate-limited`, `exited`, `gated`, `remote`
or `other`), and says how many of the rounds that was. `gate` counts pass, fail,
timeout and error over the rounds that ran a gate. `consults` counts asks per
role (`session` for `ask --round`) with the findings' models beside them, plus
the verifies that were skipped.

`blocked` comes from `availability.json`, which keeps 30 days: rate-limit and
spawn-failure events in that window, and the gates a human cleared with
`relay available`. Only a gate cleared by hand has a recorded length -- an
expired gate keeps no expiry time, so it counts as an event with no duration.

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

### Asking a past round's builder

A closed round's report names the harness session that built it, so you can ask
that session what it did and why without reopening the round:

```
relay ask --round 1 -q "why did you stop at the second migration?" webshop
```

`--round N` looks the session up on round N's report entry and resumes it as a
headless consult with the harness's own resume form — claude `--resume`, agy
`--conversation`, opencode `run --session … --fork`. The answer is the
process's final message, which relay writes to the usual
`NNN-<id>-findings.md` and queues to the planner exactly as any consult's
findings are. `--role` and `--candidate` are ignored (with a note on stderr if
you passed one): resuming a session fixes both. The question comes from
`--file` or `-q`, and exactly one of them is required.

Round N must be closed. Resuming the open round's builder would put two writers
in one session, so `--round` refuses the current round and says which; a round
whose report names no session — built before relay recorded sessions, or by a
harness that printed none — is refused too, because relay will not guess which
session to resume.

Resuming mutates the session on claude and agy: the resumed turn is appended to
it. That is why relay reaches for this only once the round has closed, and why
the prompt tells the builder to change nothing and run no writing tool — but it
is the session's own history that changes, not the tree. opencode has no
read-only flag, so its round consult runs at `harness` tier and its `--fork`
leaves the original session untouched: the resumed turn lands in a copy. codex
has no verified resume form, and `relay ask --round` refuses it by name.

## The planner: architect

relay ships one more definition it never launches: `architect`, the planner's
persona. The planner is the session you drive -- the one you run `relay bind`
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

The `relay planner init` SessionStart hook also injects the relay handoff
rules, so a planner session running an agent other than `architect` still
receives them.

**Wait for the report after every send.** In Claude Code the planner starts
`relay wait <name> --timeout <budget>; relay pull <name>` as a **background**
command and ends its turn: Claude Code wakes the session when the command exits,
and the `relay mcp` send result prints the exact command for that binding. Other
harnesses run `relay wait <name> --timeout 9m` in a loop while it exits 124,
then `relay pull <name>`, and end their turn only when no binding has a round in
flight.

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

`relay status` collapses the binding's internal state into five:

- **ACTIVE** — someone is working (planner or builder), nothing needs a human
  yet.
- **NEEDS YOU** — relay has stopped and a person must act. Covers a dead
  builder process, a round that ran past its timeout, and a binding that hit
  its round cap.
- **PAUSED** — `relay pause` released the binding's worktree between rounds;
  the branch and the round log stay, and `relay bind --resume`
  restores it. Nothing needs a human, and `gc` leaves it alone.
- **DONE** — the planner declared the work verified via `relay done`, and
  relaying has stopped deliberately, not because anything went wrong: unlike
  NEEDS YOU, nothing needs a human here. `Reconcile` returns immediately for
  a done binding — no reports are queued and no timeouts are flagged. The binding and its round log stay on disk (`relay log <name>`
  still works as an audit trail) until `relay unbind` or `relay gc` removes them;
  a clean worktree is released at `done` so the branch is free to review.

## Running the daemon

`relay daemon` is the reconciler: it watches builders, queues reports back to
the planner, and flags stalled rounds. Nothing else needs it running — the CLI
works on its own — but without it, reports are only delivered when you run
`relay pull` by hand.

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
- `builder_stalled` (`~/.config/relay/hooks/builder_stalled.d/`) — fires once when a live local builder's tree and stream or screen have been quiet for `stall_after_ms` (#252, generalised by #135). Clearing the stall fires nothing.
- `binding_stale` (`~/.config/relay/hooks/binding_stale.d/`) — fires once when a `NEEDS YOU` or `HELD` binding has sat unacted for `stale_after_ms` (#135). Clearing the stamp fires nothing.

### Hook execution & environment

Each hook script is executed asynchronously in a detached process with a 10-second timeout. relay injects the following environment variables:

- `RELAY_EVENT`: The event type name (`state_changed`, `round_started`, `builder_stalled`, `binding_stale`).
- `RELAY_BINDING`: The name of the binding.
- `RELAY_STATE`: The current state of the binding.
- `RELAY_OLD_STATE`: The previous state of the binding.
- `RELAY_ROUND`: The current round number.

Hook stdout, stderr, and execution failures are logged to `~/.local/state/relay/hooks.log` (or `$XDG_STATE_HOME/relay/hooks.log`).

Scripts must have their executable bit set (`chmod +x`). If `~/.config/relay/hooks/` or an event directory does not exist, event dispatch is a silent no-op.

### Webhooks

Beside hook scripts, `policy.json`'s `notify.webhooks` posts lifecycle events straight to a URL -- a Slack incoming webhook, a Discord webhook, or any endpoint that accepts a JSON POST -- with no script required:

```json
"notify": { "webhooks": [
  { "url": "https://hooks.slack.com/services/…", "format": "slack", "events": ["state_changed:needs_you", "binding_stale", "builder_stalled"] }
] }
```

- `url` (required) -- where the event is POSTed; must be `http://` or `https://`.
- `events` -- which events reach this webhook; omit (or leave empty) to receive every event. `state_changed:<state>` matches only a `state_changed` event whose new state is `<state>` -- `state_changed:needs_you` is the one most people want.
- `format` -- `json` (default: the event and its rendered text as a JSON object), `slack` (`{"text": ...}`), or `discord` (`{"content": ...}`).

One sentence per event name, for a webhook filter:

- `state_changed` -- a binding transitioned between states; filter to one target state with `state_changed:<state>`.
- `round_started` -- a new round began.
- `fork_created` -- a new binding was branched from an earlier round of an existing binding.
- `builder_stalled` -- a live headless builder's stream went quiet for `stall_after_ms`.
- `binding_stale` -- a NEEDS YOU or HELD binding sat unacted for `stale_after_ms`.

Each matching webhook POSTs in its own goroutine with a 5-second timeout and never blocks a daemon tick. A failure -- a non-2xx response or a transport error -- is logged to `hooks.log` with the URL's host only, never the full URL, since a webhook URL is a secret. There are no retries and no queue: a webhook is best-effort, exactly like a hook script.

## Setting up your agent harnesses

A harness needs nothing installed beyond its own binary on `PATH` and the role
definitions relay installs (`relay agent install`). relay starts each builder
as a non-interactive process and reads the round from the harness's own stream,
so there is no lifecycle hook to install for it.

### opencode permission allowlist

relay stages plans and reports under `~/.local/state/relay/<binding>/`, outside
the repo the builder is working in, so a fresh opencode builder blocks on an
"Access external directory" dialog on its first round. To skip it entirely, add
this to `~/.config/opencode/opencode.jsonc`:

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

relay doctor warns when this entry is missing (row external_directory under opencode).

## Recovering a broken binding

If the builder's process dies without a report, relay switches to the next
candidate or, when none serves the binding, goes `NEEDS YOU` — see "Headless
builders". A binding whose builder is gone between rounds is `BROKEN` and
relaying stops until you point it at a new builder:

```bash
relay bind --resume --name N --rebind                                       # start a fresh builder, picked by policy order
relay bind --resume --name N --builder agy/google/gemini-3.8-flash-high     # start a fresh builder, naming it
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
halted it (see "Headless builders"). To move a binding to a fresh process by
hand, `relay send` the staged plan again once `relay status` shows the builder
`exited`.

## Platform support

**Linux and macOS.** Both are exercised in CI, on the Go 1.22 floor and on
current stable.

Windows is not supported. State locking is behind a build tag
(`internal/store/lock_unix.go`) and could be implemented there, but the
harnesses and process supervision relay relies on are unix-shaped. The tree
still cross-compiles for `windows/amd64` (CI checks it), and relay will refuse
at runtime with a clear error rather than running without a state lock.

## Claude Code plugin

The relay plugin gives a Claude Code planner two things: the `relay mcp` MCP
server (`relay` from `PATH`), and a `SessionStart` hook that runs
`relay planner init`. The hook exports `RELAY_PLANNER` and tells the model its
planner name. Install it once per machine:

    /plugin marketplace add fuad-daoud/relay
    /plugin install relay@relay

The plugin also carries three slash commands over relay's read verbs:

- `/relay:status [--name <binding>] [--all]` -- the bindings, round and state.
- `/relay:diff [<binding>] [--round N]` -- what a builder changed in a round.
- `/relay:log <binding> [--round N]` -- a binding's append-only round log.

Then launch Claude Code normally:

    claude --agent architect --model opus

**The background wait is the default.** After each `relay send`, the planner
runs

    relay wait --name <n> --timeout <budget>; relay pull --name <n>

as a background Bash command and ends its turn. Claude Code wakes the session
when the command exits, and its output is the report (or the reason the round
stopped). The `relay mcp` send result prints that exact command for the
binding, so the model does not have to remember it. Act on the pull output
after every exit except `WaitTimeout`; on a timeout, run `relay status --name
<n>` and start the wait again if the round is still running.

**The channel is an opt-in upgrade.** With the channel enabled, reports,
consult answers and edge artifacts arrive as `<channel source="relay">` events
the moment the daemon has them, instead of being fetched by the wait. Turn it
on by launching with the development flag, which asks for confirmation at
every start:

    claude --agent architect --model opus --dangerously-load-development-channels plugin:relay@relay

During the research preview `--channels` only registers plugins on an
Anthropic-curated allowlist, and relay is not on it. A Team or Enterprise admin
can instead add `{"marketplace": "relay", "plugin": "relay"}` under
`allowedChannelPlugins` (with `channelsEnabled: true`) in managed settings,
which replaces Anthropic's list for that org and makes plain
`--channels plugin:relay@relay` work. Without either, `relay mcp` runs in
tools mode: the tools work and nothing is pushed, which is the background wait
above.

**How reports arrive.** relay identifies the planner session itself -- the
plugin's hook registers it, and `relay planner list` shows the records -- so no
verb has to guess who is calling. A report then reaches the planner by exactly
one of four routes: the background wait (the Claude Code default), the channel
(opt-in, above), a deliverer for a harness that has one (opencode, agy), or
`relay pull` by hand. Nothing is ever typed into a terminal.

An agy planner runs `relay planner init` once inside agy, with no flags: relay
detects the session from agy's own environment, so nothing has to be exported by
hand. Every relay command that planner runs refreshes the session's local
agentapi credentials, which relay keeps 0600 under its state directory and never
prints. A report relay pushes through those credentials wakes the idle agy
session, so an agy planner is woken by a report rather than polling for it --
and that wake-up costs one turn of the agy session.

## Design

[`docs/design.md`](docs/design.md) is the architecture document written before
relay was built. It explains why the CLI and the daemon are split and what was
deliberately left out. It is a historical record, not maintained against the
code.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: open an issue first, keep it
stdlib-only, write the test, and make sure `make check` passes.

`make e2e` runs one headless relay round end to end -- planner init, bind,
send, delivery -- with a fake harness binary on `PATH`. It runs in CI and is not
part of `make check`.

## License

[MIT](LICENSE) © Fuad Daoud
