# Drop herdr: relay-owned planner identity, headless-only builders, channel delivery (#303)

Status: design. Tracked as #303; replaces #205's direction. The gate this
spec was held on, #300 (opencode delivery), merged as #316 on 2026-09-22.

Scope in one line: relay's code stops calling, reading, packaging or
documenting herdr. The human keeps using herdr as a terminal multiplexer;
relay no longer knows it is there.

## 1. System overview

relay moves plans and reports between a planner and a builder. Today three
of its jobs go through herdr: it **finds the planner** (`$HERDR_PANE_ID`,
then `herdr agent list` for the planner's harness kind and session id,
`bind.go` `endpointOf`), it **runs pane builders** (typed prompts, screen
scraping, fingerprints, dialog answering), and it **types reports into the
planner's pane** (`deliver.go`, `held.go`).

After this change:

- **Planner identity is relay's, and the plugin creates it.** `relay planner
  init` is the one command that registers or re-attaches a planner. The relay
  Claude Code plugin runs it from a `SessionStart` hook, which exports
  `RELAY_PLANNER=<id>` into every Bash call in the session and tells the
  model its planner name. `relay mcp` joins the same record through the
  Claude Code process they share. No verb guesses who its planner is.
- **Builders are headless or remote only.** Pane mode, `relay answer`, and
  `relay stop`'s typed wrap-up are deleted.
- **Planner delivery never goes through a pane.** It uses the MCP channel
  (Claude Code launched with the channel flag), a #300 deliverer (opencode),
  or the **background wait**. The background wait is the default for Claude
  Code: after each `send`, the planner runs `relay wait` as a background Bash
  command, and Claude Code wakes the session when that command exits (D6,
  §4.5). A planner with none of the three still gets the report through
  `relay pull`. `relay doctor` fails when a Claude Code planner has no relay
  plugin, or no hook, enabled.

### 1.1 Evidence this rests on (all verified 2026-09-22, Claude Code 2.1.280, opencode 2.0.12)

**Claude Code session id: an attribute, not a key.**
- `--resume` and `--continue` keep `CLAUDE_CODE_SESSION_ID`: a `-p` run and
  both follow-ups all read `d473a011-…`, in the env and in the JSON
  `session_id`.
- **`/clear` changes it, and `relay mcp` isn't restarted.** In an
  interactive pane, before `/clear` the shell and `relay mcp` both read
  `406634b1-…`. After it, the shell read `00a4c025-…` while the same `relay
  mcp` process still had `406634b1-…`.
- **The Claude Code process is stable.** `CLAUDE_PID` in the Bash tool
  stays the same across `/clear`, and it is `relay mcp`'s parent pid (`ps -o
  ppid=`, `comm=claude`).

**A `SessionStart` hook can own registration.** The probe was a
project-settings hook that logs its stdin and env and writes to
`$CLAUDE_ENV_FILE`:
- The hook's stdin carries `hook_event_name`, `source`, `session_id`,
  `transcript_path` and `cwd`.
- `source` was `startup` on a fresh session, `resume` after `--resume`, and
  `clear` after `/clear`.
- The hook's `$PPID` is the Claude Code process: the same pid as `CLAUDE_PID`
  and as `relay mcp`'s parent.
- A line `export RELAY_PLANNER=…` written to `$CLAUDE_ENV_FILE` was present
  in every later Bash call: after startup, after `--resume`, and after
  `/clear`, where the `clear` hook's value replaced the `startup` one.
- **The `clear` hook fires lazily**: at the first prompt submitted after
  `/clear`, not at `/clear` itself. A `!` shell command run between the two
  still saw the previous session's env. That is harmless here, because the
  `clear` hook re-attaches the **same** planner id (§5.1).

**opencode.**
- Tools get no session id: the only matching env var in a tool subprocess is
  `OPENCODE_TERMINAL=1`, while the run's session was `ses_f35add…`.
- The plugin API (`@opencode-ai/plugin` types) has a `"shell.env"` hook that
  receives `{cwd, sessionID?, callID?}` and returns `{env}`. That is the
  opencode equivalent of `CLAUDE_ENV_FILE`.
- opencode 2.0 plugins need `export default {id, server, setup}`, the shape
  herdr's own plugin uses. A plugin without it logs `failed to load plugin`.
- **Unverified:** every `opencode run --standalone` with a valid probe plugin
  hung before reaching its shell tool (4 attempts, 300 s each), while runs
  where the plugin failed to load completed. Whether `shell.env` reaches
  opencode's shell tool is therefore **not established**. opencode planners
  register explicitly in this spec (§4.2), and the relay opencode plugin is
  a follow-up (§7).

**Existing structure.**
- `relay.db` already has `planner(id PK, harness_kind, session_id,
  transcript_locator, first_seen, last_seen)`, unique on
  `(harness_kind, session_id)`. Today `UpsertPlanner` mints `id` and ingest
  fills it from `bind.json`'s `Planner.SessionID`, which herdr supplied.
- #300's `PlannerDeliverer.Deliver(ctx, planner store.Endpoint, …)`
  (`internal/relay/deliverer.go`) takes the opencode session id from
  `b.Planner.SessionID`. Planner identity must keep filling that field.
- On `main`, `DeliverPending` first checks `rt.Channels.Live(b.Planner.PaneID)`,
  then `PendingForPlanner`, then `rt.Deliverers[b.Planner.Kind]`, and only
  then `FindAgent` and the pane path (`deliver.go:68–103`).

**The channel needs a flag most users won't pass** (Claude Code docs,
`code.claude.com/docs/en/channels` and `/channels-reference`, read
2026-09-22):
- During the research preview, `--channels` registers only plugins on an
  allowlist. Anthropic's default list is "the channel plugins in
  `claude-plugins-official`, which Anthropic curates at its discretion". The
  plugin directory submission forms feed the community marketplace, "which
  is not on the channel allowlist". The only route the docs name is an
  Anthropic partner contact.
- A Team or Enterprise admin can set `allowedChannelPlugins` (with
  `channelsEnabled: true`) in managed settings. That list replaces
  Anthropic's, so `{"marketplace": "relay", "plugin": "relay"}` there makes
  plain `--channels plugin:relay@relay` work for that org.
- Everyone else needs `--dangerously-load-development-channels`, which asks
  for confirmation at every start.
- Without either flag, `relay mcp` runs in tools mode (`internal/mcp/mode.go`
  reads the parent's argv): the tools work and nothing is pushed. That is how
  a plain `claude` launch runs today. It is the case every new user is in.

**Background commands wake an idle session** (step 0b, first half). The
Bash tool's `run_in_background` documents that a background command
"re-invokes you when it exits". Probe, in an interactive Claude Code 2.1.280
session in tools mode (a plain `claude` launch):
- The command was `sleep 300; echo relay-wake-probe`, started mid-turn.
- The turn then ended, and the session sat idle.
- The command exited at 20:13:46 UTC.
- A new turn opened carrying the task-completion notice. The command's output
  was readable from it by 20:13:51.

**The background wait delivers a real round to an idle planner** (step 0b,
second half, 2026-09-22):
- **Setup.** Binding `wake-probe`: a headless opencode builder
  (`cline-pass/deepseek-v4.1-flash`), no gate, no reviewer. The planner was
  an interactive Claude Code 2.1.280 session in tools mode, in herdr pane
  `w0:p5`.
- **Pane delivery was switched off.** A stand-in channel claim held
  `channels/w0_p5.json`, so `DeliverPending` returned "planner has a
  channel" and never typed into the pane, as after step 3.
- **The waiter.** After `relay send`, the planner started
  `relay wait --name wake-probe --timeout 30m; relay pull --name wake-probe`
  with `run_in_background` and ended its turn.
- **Round 2 timeline (UTC).** The planner's turn ended at 20:18:40. The
  builder slept 120 s, and the waiter exited at 20:20:51. A new turn opened
  at 20:20:52, and the waiter's output (the `relay pull` payload with the
  report path) was in it. `relay pull` found the entry still pending, which
  shows no other route had taken it.
- **Round 1 doesn't count.** Its round closed 14 s after the send, while the
  planner's turn was still running (it ended at 20:18:11, and the waiter
  exited at 20:18:08). Claude Code queued the notice and delivered it at the
  end of the turn. That is the right behaviour, but it doesn't show an idle
  wake.
- **Both rounds exited `WaitUnmarked` (2)**: this builder closed without the
  completion marker. `relay pull` delivered the report anyway. The §4.5
  instructions therefore treat every wait exit except `WaitTimeout` as "act
  on the pull output", not only `WaitClosed`.

**`relay wait` is already the right primitive** (`cmd/relay/main.go`
`cmdWait`, `internal/relay/wait.go`): it blocks until the round closes or
needs a human, prints the report path, and exits with a code for each outcome
(`WaitClosed`, `WaitHalted`, `WaitNeedsYou`, `WaitGone`, `WaitTimeout`, …).
Its `--timeout` defaults to 10 minutes and must be positive, which is
shorter than many rounds.

**Spike** (local branches `worktree-agent-ab573741dd87c1045` for Go and
`worktree-agent-a47b65411336ce6a7` for everything else, base `15e2b74`, not
pushed): the full deletion built, vetted and passed `make check`. 158 Go
files, +2,303 / −21,700 lines. Tests went from 1,951 to 1,553; some of the
398 removed tests cover behaviour that survives and must be ported (§6.3).

### 1.2 Decisions settled with the human (2026-09-22)

| # | Decision |
|---|---|
| D1 | Planner identity is relay-minted and relay-managed, and it integrates with the plugin and the MCP server. |
| D2 | `relay answer` (verb, MCP tool, `pick` answer flow) is deleted. A builder asks its question by halting in its report; the planner answers with the next round. |
| D3 | `relay stop` loses the pane wrap-up (`stopPrompt`, `--grace`, `--now`). It already kills a headless round straight away. |
| D4 | Halt, stall, stale and "all rounds finished" notifications are dropped. The `LogEntry` records stay. |
| D5 | ORPHANED detection and the `herdr plugin install` path are dropped without a replacement. |
| D6 | *Revised 2026-09-22 (§1.1, the channel allowlist).* A Claude Code planner that isn't on a channel gets each report through a **background wait**: `relay mcp` in tools mode tells the model, in its instructions and in every `send` result, to run `relay wait` for that binding as a background Bash command. Claude Code wakes the session when the command exits. The background wait is the default. The channel is an opt-in upgrade for users who pass the development flag or whose org allowlists relay. A planner with neither route still has `relay pull`. The relay plugin and its hook must always be installed, and `relay doctor` enforces that for Claude Code. A live channel isn't required. |
| D7 | Pane-mode bindings: history rows stay readable; a pane binding still active at upgrade is closed with a message (§5.6). |
| D8 | The planner doesn't pass its name by hand on every call. The hook exports it; `--planner` stays as an override only. |

## 2. File structure

New:

```
internal/planner/
  planner.go        Record, SessionRef, ID and Name rules, errors; no I/O
  registry.go       Registry interface + FileRegistry ($STATE/planners/<id>.json)
  resolve.go        Resolve(): flag > env > host > session
  hook.go           HookInput (Claude SessionStart stdin), HookOutput, EnvLine
  ident.go          Detect(): which harness process and session is calling
cmd/relay/planner.go  `relay planner init|list|rename|forget`
claude-plugin/hooks/hooks.json   SessionStart -> `relay planner init --hook claude`
internal/e2e/headless_test.go   the CI-run end-to-end round (§6.4)
```

Deleted (whole files or packages; the spike's list, re-measured against
the base of each PR):

```
internal/herdr/                       the package
internal/relay/held.go, deliver.go's pane path, answer.go, the pane halves of
  reconcile.go (nudge, fingerprint, scrape), limit.go's screen read,
  repair.go's prompt, stop.go's stopPrompt, progress.go's blocked check
internal/pick/answer*.go
internal/serve/herdr.go
herdr-plugin.toml, from-source/herdr-plugin.toml
scripts/plugin-{build,daemon-check,fetch,install-service,open-pane}.sh (+ tests)
```

Changed, by area: `internal/store` (Binding.PlannerID, legacy fields),
`internal/relay` (bind/add/fork/ask/status/daemon/drain/channel/deliver/usage),
`internal/mcp` (instructions, tools, status filter), `internal/ingest`,
`internal/db` (planner id from the record), `internal/harness` (drop
`PaneArgs`, `Integration`, `SubAgents`, coverage), `internal/ui`,
`internal/release/provenance.go` (drop the two plugin kinds),
`claude-plugin/.claude-plugin/plugin.json` (hooks), `cmd/relay`, `Makefile`,
`.github/workflows/release.yml`, README, CONTRIBUTING, CLAUDE.md,
`internal/harness/agents/{architect,reviewer}.*.md`,
`.github/ISSUE_TEMPLATE/*`, `dist/*` comments.

## 3. Data structures

### 3.1 `planner.Record` (write side: `$XDG_STATE_HOME/relay/planners/<id>.json`)

Persistence phase 1 (`2026-09-20-persistence-design.md` §2) keeps files as
the write side and ingests them into the db, so the planner record is a file
and the `planner` table is filled from it.

| field | type | constraint | purpose |
|---|---|---|---|
| `id` | string | required; `pl_` + 12 lowercase base32 chars; immutable | the identity every binding, claim and db row keys on |
| `name` | string | required; `[a-z][a-z0-9-]{0,31}`; unique among records | the handle the model is told, `--planner` accepts, and status groups by |
| `harness_kind` | string | required; a `harness` kind (`claude`, `opencode`, `agy`, ...) | picks the delivery route |
| `session_id` | string | required; non-empty; `^ses_[A-Za-z0-9]+$` for opencode | the harness session currently attached |
| `sessions` | []SessionRef | append-only; max 20, oldest dropped | earlier sessions of this planner, so history still joins |
| `host_pid` | int | ≥ 0; 0 = unknown (explicit registration) | the harness process: for Claude, the hook's `$PPID` = `relay mcp`'s ppid |
| `host_started_at` | int64 | Unix seconds; 0 when `host_pid` is 0 | pid-reuse defence, as `Endpoint.StartedAt` does |
| `cwd` | string | required; absolute | where it registered; informational |
| `transcript_locator` | string | optional | #172: the hook's `transcript_path` |
| `created_at` | time | required | |
| `seen_at` | time | required; refreshed by `init`, by `relay mcp` polls, and by resolving CLI calls, at most once per minute | "last heard from" in `relay planner list` |

`SessionRef{ session_id string, from time, to time }`.

Invariants:
- At most one record holds a given `(harness_kind, session_id)` as its
  current `session_id`.
- At most one record holds a given live `(host_pid, host_started_at)`.
- Moving a session (§5.1) appends the old one to `sessions`.

Default name: `<agent>-<n>`, with the smallest free `n`. `<agent>` is
`$CLAUDE_CODE_AGENT` when the hook's env has it (e.g. `architect-1`), else
the harness kind (`claude-1`). `--name` overrides the default.

### 3.2 `store.Binding` changes

- **Add** `PlannerID string json:"planner_id,omitempty"`. It's required on
  every binding written after PR 1; empty only on older files.
- **Keep** `Planner Endpoint`, and fill its `Kind`, `SessionID` and
  `TranscriptLocator` from the record at bind/add/fork, so ingest and #300's
  deliverer keep working unchanged. When a record's session moves (§5.1),
  every non-DONE binding naming it gets its `Planner.SessionID` refreshed
  under the store lock. `Planner.PaneID` stays as a field only so old
  `bind.json` files load. It gains `omitempty` and nothing new writes it.
- Pane-only fields `BuilderScreen`, `PlannerScreen`, `HeldGrace` and the
  `Output` fingerprint stay loadable but are never written, with
  `// legacy: pre-#303 bind.json` comments. They are removed after one
  minor release (a follow-up issue, not this spec).
- `Mode ""` currently means pane. After PR 2, loading a binding whose builder
  `Mode` is `""` or `"pane"` and whose state is not DONE triggers §5.6.

### 3.3 `relay.Claim` (channel claim, `channels/<planner-id>.json`)

`Pane string` becomes `Planner string json:"planner"` (required; a planner
id). The other fields are unchanged. `ClaimStore.Live`, `Write` and
`Remove` take a planner id. Old pane-keyed claim files expire under
`ClaimTTL`; the first `relay mcp` after upgrade removes any file in
`channels/` that doesn't parse as a planner-keyed claim.

### 3.4 `planner.HookInput` / `HookOutput` (Claude Code `SessionStart`)

`HookInput` (stdin, JSON; unknown fields ignored): `hook_event_name`
(must be `SessionStart`), `source` (`startup` | `resume` | `clear` |
`compact`; any other value is treated as `startup`), `session_id`
(required), `transcript_path`, `cwd` (required).

`HookOutput` (stdout, JSON): `{"hookSpecificOutput": {"hookEventName":
"SessionStart", "additionalContext": "<text>"}}`, where the text is: `You are
relay planner <name> (<id>). RELAY_PLANNER is set in your shell; pass
--planner <name> only to act as another planner.` Step 1's done-criteria
check that this text reaches the model. If only plain stdout does, the
builder switches to plain stdout and reports it. That is not a halt.

`EnvLine`: `export RELAY_PLANNER=<id>\n`, appended to `$CLAUDE_ENV_FILE`
when that variable is set. If it isn't set, `init --hook` still registers,
exits 0, and says so in `additionalContext`: "RELAY_PLANNER could not be
exported; relay resolves this session through its host process."

### 3.5 db

- `planner.id` is **the record's id**; ingest reads `planners/*.json` and
  upserts by id. `UpsertPlanner` gains an id-first path. The natural-key
  unique index stays.
- Existing planner rows keep their ids. When `init` registers a new record
  for a `(kind, session)` that already has a db row, the record **reuses
  that row's id** instead of minting one. That keeps history joined across
  the upgrade.
- `binding.builder_mode` keeps `'pane'` as a valid historical value;
  nothing new writes it. `usage.ModePane` stays as a read-only constant for
  history. Remote builders and consults that defaulted to it get their real
  mode.

### 3.6 Status JSON (`relay status --json`, statusline, `relay ui`, serve `FlatStatus`)

Removed: `planner_pane`, `planner_status`, `planner_focused`, `workspace`,
`builder_pane`, `foreign`, `sub_agents`, `nudge`, `hold`, `stop_grace*`,
`herdr_error`. Added: `planner_id`, `planner_name`, `planner_route`
(`channel` | `deliverer` | `pull`), `planner_route_live` (bool). `pull`
covers the background wait (D6). The daemon can't tell whether a background
wait is running, so `pull` is reported as a route, not as a fault. Every
consumer in the tree is updated in the same PR. The remote wire protocol
(`internal/remote/proto.go`) carries none of these fields and doesn't change.

## 4. Interfaces and contracts

### 4.1 `planner.Registry`: owns the planner records

```
Get(id string) (Record, error)                    -> ErrNotFound
ByName(name string) (Record, error)               -> ErrNotFound
BySession(kind, sessionID string) (Record, error) -> ErrNotFound
ByHost(pid int, startedAt int64) (Record, error)  -> ErrNotFound
List() ([]Record, error)
Create(r Record) (Record, error)       -> ErrNameTaken, ErrSessionTaken, ErrHostTaken, ErrInvalid
MoveSession(id, sessionID, transcript string, now time.Time) (Record, error)
                                       -> ErrNotFound, ErrSessionTaken
SetHost(id string, pid int, startedAt int64) (Record, error) -> ErrNotFound, ErrHostTaken
Rename(id, name string) (Record, error) -> ErrNameTaken, ErrInvalid
Touch(id string, now time.Time) error  -- best effort; never fails a caller's verb
Forget(id string) error                -> ErrInUse when a non-DONE binding names it
```

Writes are atomic (temp file + rename) under the store lock the verbs
already take. So `init --hook` and `relay mcp`, which start concurrently
(§5.2), serialise, and whichever runs second finds the first's record.
`FileRegistry{Root}` is the only implementation; tests use it on a
`t.TempDir()`.

### 4.2 `planner.Detect`: which harness process is calling

```
Detect(env func(string) string, ppid int) (Ident, bool)
Ident{ Kind string; SessionID string; HostPID int }
```

| kind | rule | status |
|---|---|---|
| `claude` | `CLAUDECODE=1`. `SessionID` = `CLAUDE_CODE_SESSION_ID`. `HostPID` = `CLAUDE_PID` when set (Bash tool), else `ppid` (inside `relay mcp` and the hook, whose parent is the Claude process) | verified §1.1 |
| `opencode` | not detected: tools see only `OPENCODE_TERMINAL=1`. Explicit `relay planner init --kind opencode --session ses_…` | verified §1.1 |
| `agy` | not detected; explicit `init` only | by design until an agy deliverer exists |

`Detect` never shells out and never reads a file. Host start time comes
from a `ProcStart func(pid int) (int64, error)` passed by the caller: the
same OS read `Endpoint.StartedAt` already uses.

### 4.3 `planner.Resolve`: how every verb except `init` gets its planner

```
Resolve(reg Registry, in ResolveInput) (Record, Resolution, error)
ResolveInput{ Flag string; Env func(string) string; PPID int;
              ProcStart func(int) (int64, error); Now time.Time }
Resolution = "flag" | "env" | "host" | "session"
```

Order, first hit wins:
1. `--planner` (id or name)
2. `$RELAY_PLANNER` (set by the hook)
3. `ByHost(Detect().HostPID, ProcStart(HostPID))`, which covers `relay mcp`
   and a session whose hook couldn't export
4. `BySession(kind, sessionID)`

Errors:
- `ErrUnknownPlanner{Ref}`: a flag or env value that matches no record.
- `ErrNoPlanner`: nothing resolved. The message is the fix: "no relay
  planner for this session: is the relay plugin enabled (`relay doctor`)?
  Or run `relay planner init`."

**`Resolve` never registers.** Only `relay planner init` creates records.
`bind`, `add`, `fork` and `ask` fail with `ErrNoPlanner` rather than
creating a planner implicitly. A missing hook is then loud, not a silently
unnamed planner.

A verb that is given a binding by name doesn't need a planner and never
resolves one: `send`, `wait`, `pull`, `done`, `stop`, `log`, `show` and
`diff` with a name. `status` with no name resolves and filters by planner;
with no planner it lists everything.

### 4.4 `relay planner init`: the one registration command

```
relay planner init [--name N] [--kind K --session S] [--hook claude]
```

- `--hook claude`: reads `HookInput` from stdin, writes `EnvLine` and
  `HookOutput`, and always exits 0. A hook failure must never block a
  Claude Code session: errors go into `additionalContext` and to stderr.
- `--kind/--session`: explicit registration (opencode, agy, or by hand).
  Prints the record and the export line for the human.
- No flags: `Detect()` from the calling Bash tool.

Postconditions: exactly one record matches the caller's host (when known)
and session; its `session_id` is the caller's; its `seen_at` is now.

### 4.5 `relay mcp` contract changes

- On start: `Resolve()`. If that returns `ErrNoPlanner` (it started before
  the hook finished), retry every 500 ms for up to 10 s, then run
  tools-only with a stderr note. Then claim `channels/<planner-id>.json`.
  `--pane` is removed; `--planner <id|name>` overrides.
- Each poll re-reads the record, so a `/clear` rename or session move is
  picked up without a restart.
- The `status` tool filters by planner id. The `answer` tool is deleted
  (D2). The instructions text loses `broken` and `orphaned` and keeps
  `report` and `needs_you`.
- **The instructions depend on the mode** (D6). The mode is known before
  `initialize` is answered, so `relay mcp` serves one of two texts:
  - *channel*: today's text (events arrive as `<channel source="relay">`
    blocks);
  - *tools*: no events arrive. After every `send`, start the background wait
    for that binding and end the turn. When it exits, its output is the
    report or the reason it stopped; act on it as you would a `report` or
    `needs_you` event.
- **In tools mode, the `send` tool result carries the command**, so the model
  doesn't have to remember it from the instructions:

  ```
  background wait (run with run_in_background, then end your turn):
    relay wait --name <name> --timeout <budget>; relay pull --name <name>
  ```

  `<budget>` is the binding's round budget (default 24h, `cmd/relay/main.go`
  `send --timeout`), so the wait doesn't time out before the round does.
  `relay pull` prints the report text and marks the entry delivered
  (`route=pull`, §5.4). The model acts on the pull output after every exit
  except `WaitTimeout`, including `WaitUnmarked` and `WaitHalted`: a real
  builder closed a round unmarked twice in step 0b (§1.1), and the report
  was still there. On `WaitNeedsYou` pull prints `nothing pending`, and the
  wait's own line gives the reason. On `WaitTimeout` the model runs
  `relay status --name <name>`, and starts the background wait again if the
  round is still running.
- In channel mode the `send` result doesn't include the command. A report
  must not arrive twice, once by channel and once by pull. If it does
  anyway, `relay pull` prints `nothing pending`, which is harmless.

### 4.6 Planner delivery (`DeliverPending`, after PR 3)

```
DeliverPending(ctx, rt, b) (Binding, Delivery, error)
Delivery.Route = "channel" | "deliverer:<kind>" | "pull"   # "pull": left pending for relay pull
```

Postcondition: a pending entry is either handed to exactly one route and
marked delivered, or left pending with `Delivery.Reason` set. It is never
typed anywhere.

### 4.7 `relay planner` verbs (CLI; tests exercise the rules in `internal/planner`, no herdr, per CLAUDE.md)

```
relay planner init   (§4.4)
relay planner list [--json]            id, name, kind, session, host, cwd, seen, route
relay planner rename <id|name> <new-name>
relay planner forget <id|name>         refuses while a live binding names it
```

`init` against an existing record (same host, or `--name` of an existing
record plus `--kind/--session`) re-attaches that record. That is how an
opencode planner restarted by hand keeps its bindings, and it replaces
today's `--assume-dead` rebind. There is no separate `adopt` verb.

### 4.8 `relay doctor` checks (replace every herdr check)

| check | when | severity |
|---|---|---|
| relay plugin enabled in Claude Code (`enabledPlugins["relay@relay"] == true` in user or project settings) | a `claude` candidate or planner exists | **FAIL** (D6) |
| the installed plugin's version ships the `SessionStart` hook | same | FAIL |
| run from a Claude Code session: `Resolve()` succeeds, and a `relay mcp` process is a child of the planner's host process | `Detect` = claude | FAIL |
| same session: a live channel claim exists for that planner | `Detect` = claude | INFO when absent, never FAIL (D6): "tools mode: reports arrive by background wait. For push, launch with `--dangerously-load-development-channels plugin:relay@relay`, or have an org admin add relay to `allowedChannelPlugins`" |
| opencode server reachable (`$XDG_STATE_HOME/opencode/service.json`, loopback) | an opencode planner record exists | WARN |
| planner records with `seen_at` older than 7 days and no live binding | always | INFO: suggest `relay planner forget` |

`herdr.MinVersion` and every `HERDR_*` read are removed. `UsableBuilder`
means "binary on PATH and the candidate parses".

## 5. Pseudocode

### 5.1 `relay planner init --hook claude`

```
in := parse stdin as HookInput           # malformed -> context note, exit 0
host := ppid; hostStart := ProcStart(host)
under store lock:
  r, err := reg.ByHost(host, hostStart)                       # same Claude process
  if err: r, err = reg.BySession("claude", in.session_id)     # --resume in a new process
  if found:
    if r.session_id != in.session_id:                         # /clear, or a new process
      r = reg.MoveSession(r.id, in.session_id, in.transcript_path, now)
      refresh Planner.SessionID on r's non-DONE bindings
    if r.host_pid != host: r = reg.SetHost(r.id, host, hostStart)
  else:
    id := dbPlannerID("claude", in.session_id) or mint()      # §3.5
    r = reg.Create({id, name: flagOrDefault(), "claude", in.session_id,
                    host, hostStart, in.cwd, in.transcript_path, now})
append EnvLine(r.id) to $CLAUDE_ENV_FILE
print HookOutput(r)
exit 0
```

`source` is logged and never branches the logic. `ByHost` then `BySession`
covers `startup`, `resume` (same or new process), `clear` and `compact` alike.

### 5.2 Session start ordering (Claude Code)

```
claude starts ─┬─ SessionStart hook: planner init --hook   (creates or re-attaches)
               └─ relay mcp: Resolve() by host, retrying ≤10 s   (joins, then claims)
model's Bash:  RELAY_PLANNER from CLAUDE_ENV_FILE -> Resolve() "env"
after /clear:  the hook runs again at the next prompt -> same id; MoveSession
```

### 5.3 bind / add / fork / ask

```
r := Resolve()                       # ErrNoPlanner is a hard error (§4.3)
b.PlannerID = r.id
b.Planner = Endpoint{Kind: r.harness_kind, SessionID: r.session_id,
                     TranscriptLocator: r.transcript_locator}
builder: headless or remote only; --headless is accepted and ignored with a
  one-line stderr note ("headless is the only local mode") for one minor release
```

### 5.4 DeliverPending (after PR 3)

```
e := PendingForPlanner(b); if none: return
if c := rt.Channels.Live(b.PlannerID, now); c != nil:
    enqueue(mailbox, e); mark delivered, route=channel; return
if d := rt.Deliverers[b.Planner.Kind]; d != nil:              # #300, unchanged
    out, reason := d.Deliver(ctx, b.Planner, PushText(e), e.Path, e.TS)
    if out confirmed: mark delivered, route=deliverer:<kind>; return
    leave pending, reason; return
leave pending, reason="awaiting pull for planner <name> (<kind>)"
```

A pending entry stays pending until a route takes it or `relay pull` reads
it. `relay pull` marks it delivered with `route=pull`. For a Claude Code
planner in tools mode, this is the normal path, not a fault: the background
wait's `relay pull` is what delivers the report (D6, §4.5). There is no
`Deliverers["claude"]`.

### 5.5 Daemon tick (after PR 3; `ListAgents` is gone)

```
for each binding b not DONE:
  if legacyPane(b): retire(b); continue                      # §5.6
  reconcile builder: headless (process, stream, markers) | remote (poll) -- unchanged
  if b has a pending planner entry: DeliverPending(b)
```

### 5.6 Legacy pane binding retire (D7)

```
legacyPane(b) := b.State != DONE and b.Builder.Mode in {"", "pane"} and !b.Builder.Remote()
retire(b):
  append LogEntry{Kind: "retired", Note: "pane builders were removed in <version> (#303);
                 rebind with relay add"}
  b.State = DONE   # the worktree is left exactly as it is: no release, no gc
  save
```

A binding with no `PlannerID` (written before PR 1) that is not retired
keeps working through `Planner.SessionID`. The first resolving call from
its planner's session (`BySession`) back-fills `PlannerID`.

## 6. Error handling and verification

### 6.1 Errors

| error | recoverable | surfaced as |
|---|---|---|
| `ErrNoPlanner`, `ErrUnknownPlanner` | yes: enable the plugin, run `relay planner init`, or pass `--planner` | CLI exit 1 with the fix in the message |
| `ErrNameTaken`, `ErrSessionTaken`, `ErrHostTaken`, `ErrInUse` | yes | CLI exit 1 |
| any error inside `init --hook` | yes | never an exit ≠ 0; `additionalContext` note + stderr; `doctor` FAIL on the next run |
| registry file corrupt | no for that record | `doctor` FAIL naming the file; verbs that don't resolve are unaffected |
| no push route | yes | a status `planner_route=pull` row; the report waits for `relay pull`. For a Claude Code planner in tools mode that is the background wait, and not an error (D6) |
| deliverer refusal (#300's taxonomy) | per #300 | unchanged |

### 6.2 Observability

Every resolution logs `planner=<id> via=<resolution>` at debug. `init`
logs `source`, `session`, `host` and whether it created, re-attached or
moved a session. Every delivery `LogEntry` gains `route`. `relay planner
list` is the view.

### 6.3 Tests that are deleted vs ported

A PR that deletes a test lists it in its description under one of two
headings: **pane-only** (behaviour deleted) or **ported** (behaviour
survives, test rewritten). The spike deleted these with surviving
behaviour, and they must be ported: halts asserted through notifications
(assert the `LogEntry` instead), the status golden views (regenerate from
the new JSON), and `FlatStatus`. A PR that shrinks the test count without
that list fails review.

### 6.4 `make e2e` becomes CI

`internal/e2e/headless_test.go`, no build tag. A fake harness binary on
`PATH` writes the report and done marker. The scenario:
1. run `relay planner init --hook claude` on a canned `HookInput`, with a
   temp `CLAUDE_ENV_FILE`;
2. start an in-process `relay mcp` stdio client with `CLAUDECODE=1` and
   `--mode channel`. The test binary's argv carries no channel flag, so
   the mode is set explicitly;
3. bind, send, receive the report on the channel, pull, done;
4. run the hook again with `source: clear` and a new `session_id`, and
   assert the same planner id and a moved session;
5. **tools mode (D6)**: start `relay mcp --mode tools`, bind, and call
   `send`. Assert that the result carries the §4.5 background-wait command
   with this binding's name and budget. Run that command as a subprocess and
   assert that its output is the report text, that the entry is marked
   delivered with `route=pull`, and that nothing was written to a channel
   mailbox.

The herdr-session e2e (`internal/relay/e2e_test.go`) and the fake herdr in
`internal/e2e/fakes_test.go` are deleted. CLAUDE.md's "run `make e2e` after
reconcile/reporttail changes" rule becomes "CI runs it".

## 7. Ordered delivery

**The gate is met:** #300 merged as #316. **#293 part 2 is held** by the
human until step 4, because #310 detects `plugin-release` and
`plugin-source` installs and this spec deletes both.

| step | owner | deliverable | depends on | done when |
|---|---|---|---|---|
| 0 | planner | **done 2026-09-22** (§1.1): Claude session and hook behaviour verified; opencode `shell.env` unverified | -- | -- |
| 0b | planner | **done 2026-09-22** (§1.1): both halves passed. **Background-wait probe** (D6): in an interactive Claude Code 2.1.280 session in tools mode, start a background Bash command that exits after the turn has ended, and record whether the idle session is re-invoked with its output. Then repeat with `relay wait` on a real headless round. Record the result in §1.1 | -- | both runs wake the idle session. **If either doesn't, halt**: D6 goes back to the human, and steps 3–5 don't start |
| 1 | builder | **Planner identity**, with herdr still in the tree: `internal/planner`, `relay planner init/list/rename/forget`, the plugin's `SessionStart` hook, `Binding.PlannerID`, bind/add/fork/ask use `Resolve` (herdr is still the pane path's source of `PaneID`), `relay mcp` joins by host and claims by planner id, MCP `status` filters by planner, ingest uses record ids, doctor's plugin + hook checks | #300 ✓ | `make check`. **Live check by the planner, not the builder:** a fresh Claude session in a pane gets `RELAY_PLANNER` and the "You are relay planner" context; `/clear` then a prompt keeps the same id; `relay mcp`'s claim names that id |
| 2 | builder | **Headless-only builders**: delete pane builder mode, `relay answer` + MCP tool + `pick` answer, `stop --grace/--now` and `stopPrompt`, ui pane capture, harness `PaneArgs`/`Integration`/`SubAgents`/coverage, the pane usage reader, the `--builder <pane>`/`--workspace`/`--assume-dead`/`--held-grace` flags; `--headless` becomes a no-op; the §5.6 retire rule | 1 | `make check`; no builder code path reads a pane; the ported tests listed |
| 3 | builder | **Planner delivery without herdr**: §5.4 routes, delete pane delivery and `held.go`, the notifications (D4), daemon `ListAgents`, ORPHANED/BROKEN-from-herdr, status JSON §3.6 and every consumer, the `internal/herdr` package, every `HERDR_*` read; `relay mcp`'s mode-dependent instructions and the tools-mode `send` result (§4.5); doctor's mcp-child and channel-INFO rows (§4.8) | 2, 0b | `grep -rli herdr --include=*.go . \| grep -v _test` is empty; `make check` |
| 4 | builder | **Everything that isn't Go**: packaging, `scripts/plugin-*`, `check-plugin-version` reduced to the Claude plugin manifests, `make release` and `release.yml`, README/CONTRIBUTING/CLAUDE.md (the README's Claude Code section leads with a plain `claude` launch and the background wait. The channel flag and the `allowedChannelPlugins` snippet are presented as the opt-in upgrade, with the allowlist facts from §1.1), architect and reviewer definitions (the Claude architect's "Handing off" names the background wait after every send) (and `TestArchitectHandoffIsSharedAcrossKinds`), `release.Provenance` without the plugin kinds, issue templates, `dist/` comments | 3 | `make check`; `grep -rli herdr . --exclude-dir=.git` lists only `docs/specs`, `docs/plans` and `docs/superpowers` history |
| 5 | builder | **Headless e2e in CI** (§6.4) | 3 | the CI job runs it; mutation checks: break §5.4's channel route, and the test fails; drop the command from the tools-mode `send` result, and scenario 5 fails |
| 6 | follow-up issue | **relay opencode plugin**: `shell.env` exports `RELAY_PLANNER`; `chat.message`/`event` supply the `ses_` id to `relay planner init --kind opencode`. It first needs a probe that gets an `opencode run` to finish with a valid plugin loaded (§1.1) | 1 | a probe transcript in the issue |

After step 5: #303 closes. #205 closes as superseded. #292 (rename) can
start on the smaller tree.

Every plan cut from this spec states: if a step is impossible as written or
contradicts the code, halt and report. CI runners have no `herdr`, and
after step 3 no test may reference one. No `cmd/relay` test may run a
subcommand that reaches herdr, or read the user's real config or state
(CLAUDE.md).
