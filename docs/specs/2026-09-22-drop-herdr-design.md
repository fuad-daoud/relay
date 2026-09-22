# Drop herdr: relay-owned planner identity, headless-only builders, channel delivery (#303)

Status: design. Tracked as #303; replaces #205's direction. **No round of
this spec is dispatched before #300 (opencode delivery, binding
`oc-deliver`) merges** -- §7 says why.

Scope in one line: relay's code stops calling, reading, packaging or
documenting herdr. The human keeps using herdr as a terminal multiplexer;
relay no longer knows it is there.

## 1. System overview

relay moves plans and reports between a planner and a builder. Today three
of its jobs go through herdr: it **finds the planner** (`$HERDR_PANE_ID`,
then `herdr agent list` for the planner's harness kind and session id,
`bind.go:715` `endpointOf`), it **runs pane builders** (typed prompts,
screen scraping, fingerprints, dialog answering), and it **types reports
into the planner's pane** (`deliver.go`, `held.go`).

After this change:

- **Planner identity is relay's.** relay mints a planner id, keeps a
  planner record, and maps the planner's harness session onto it. `relay
  mcp`, which Claude Code starts once per session, registers the planner
  and claims its channel under the planner id.
- **Builders are headless or remote only.** Pane mode, `relay answer`, and
  `relay stop`'s typed wrap-up are deleted.
- **Planner delivery is push-only through a route that isn't a pane**: the
  MCP channel (Claude Code), a #300 deliverer (opencode), or nothing (the
  report waits for `relay wait` / `relay pull`). `relay doctor` fails when
  a Claude Code planner has no relay plugin enabled.

### 1.1 Evidence this rests on (verified 2026-09-22)

- The `relay mcp` process and the planner's Bash tool see the **same**
  `CLAUDE_CODE_SESSION_ID`: `relay mcp` pid 2668801 (parent = the Claude
  Code process 2668686) and this session's shell both carry
  `7e5d80d0-416b-4ad8-a9a5-66212d58e7f1`. That is the join between a CLI
  call and its channel.
- `relay.db` already has `planner(id PK, harness_kind, session_id,
  transcript_locator, first_seen, last_seen)`, unique on
  `(harness_kind, session_id)` (`internal/db/migrations/001_initial.sql`).
  Today `UpsertPlanner` mints `id`; ingest fills it from `bind.json`'s
  `Planner.SessionID`, which herdr supplied.
- #300's `OpencodeDeliverer` reads the opencode session id from
  `b.Planner.SessionID` (spec `2026-09-22-opencode-delivery-design.md`
  §4, "herdr's opencode integration ... reports"). Planner identity must
  keep filling that field.
- Spike, 2026-09-22 (worktree branches `worktree-agent-ab573741dd87c1045`
  for Go and `worktree-agent-a47b65411336ce6a7` for everything else, base
  `15e2b74`, not pushed): the full deletion built, vetted and passed
  `make check`. 158 Go files, +2,303 / −21,700 lines. Tests went from 1,951
  to 1,553; some of the 398 removed tests cover behaviour that survives and
  must be ported, not dropped (§6.3). It left 17 planner-identity stubs and
  10 decision stubs; this spec settles every one of them.

### 1.2 Decisions settled with the human (2026-09-22)

| # | Decision |
|---|---|
| D1 | Planner identity is relay-minted and relay-managed, and it integrates with the MCP server. |
| D2 | `relay answer` (verb, MCP tool, `pick` answer flow) is deleted. A builder asks its question by halting in its report; the planner answers with the next round. |
| D3 | `relay stop` loses the pane wrap-up (`stopPrompt`, `--grace`, `--now`). It already kills a headless round straight away (`stop.go:73`). |
| D4 | Halt, stall, stale and "all rounds finished" notifications are dropped. The `LogEntry` records stay. |
| D5 | ORPHANED detection and the `herdr plugin install` path are dropped without a replacement. |
| D6 | A planner with no push route gets reports through `relay wait` / `relay pull` only. The MCP channel is expected to always be installed, and `relay doctor` enforces it for Claude Code. |
| D7 | Pane-mode bindings: history rows stay readable; a pane binding still active at upgrade is closed with a message (§5.6). |

## 2. File structure

New:

```
internal/planner/
  planner.go        Record, ID, Name rules, errors; no I/O
  registry.go       Registry interface + FileRegistry ($STATE/planners/<id>.json)
  resolve.go        Resolve(): flag > env > harness session > register
  ident.go          HarnessIdent: which harness am I in, what is my session id
cmd/relay/planner.go  `relay planner list|register|adopt|rename|forget`
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
`internal/relay` (bind/add/fork/ask/status/daemon/drain/channel/usage),
`internal/mcp` (instructions, tools, status filter), `internal/ingest`,
`internal/db` (planner id from the record), `internal/harness` (drop
`PaneArgs`, `Integration`, `SubAgents`, coverage), `internal/ui`,
`internal/release/provenance.go` (drop the two plugin kinds), `cmd/relay`,
`Makefile`, `.github/workflows/release.yml`, README, CONTRIBUTING, CLAUDE.md,
`internal/harness/agents/{architect,reviewer}.*.md`, `.github/ISSUE_TEMPLATE/*`,
`dist/*` comments.

## 3. Data structures

### 3.1 `planner.Record` (write side: `$XDG_STATE_HOME/relay/planners/<id>.json`)

Persistence phase 1 (`2026-09-20-persistence-design.md` §2) keeps files as
the write side and ingests them into the db, so the planner record is a file
and the `planner` table is filled from it.

| field | type | constraint | purpose |
|---|---|---|---|
| `id` | string | required; `pl_` + 12 lowercase base32 chars; immutable | the identity every binding, claim and db row keys on |
| `name` | string | optional; `[a-z][a-z0-9-]{0,31}`; unique among records | human handle for `--planner`, status grouping |
| `harness_kind` | string | required; a `harness` kind (`claude`, `opencode`, `agy`, ...) | picks the delivery route and the ident rule |
| `session_id` | string | required; non-empty; format per kind (`^ses_[A-Za-z0-9]+$` for opencode) | the harness session currently attached |
| `sessions` | []SessionRef | append-only; max 20, oldest dropped | earlier sessions this planner was adopted from, so history still joins |
| `cwd` | string | required; absolute | where it registered; informational, status grouping |
| `transcript_locator` | string | optional | #172; kept from today's Endpoint |
| `created_at` | time | required | |
| `seen_at` | time | required; refreshed by `relay mcp` polls and by every resolving CLI call, at most once per minute | "last heard from" in `relay planner list` |

`SessionRef{ session_id string, from time, to time }`.

Invariant: at most one record holds a given `(harness_kind, session_id)`
as its current `session_id`. `adopt` moves a session and appends the old one
to `sessions`.

### 3.2 `store.Binding` changes

- **Add** `PlannerID string json:"planner_id,omitempty"`. It's required on
  every binding written after PR 1; empty only on older files.
- **Keep** `Planner Endpoint`, and fill its `Kind`, `SessionID` and
  `TranscriptLocator` from the record at bind/add/fork, so ingest and #300's
  deliverer keep working unchanged. `Planner.PaneID` stays as a field only
  so old `bind.json` files load. It gains `omitempty` and nothing new writes
  it.
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

### 3.4 db

- `planner.id` is **the record's id**; ingest reads `planners/*.json` and
  upserts by id. `UpsertPlanner` gains an id-first path. The natural-key
  unique index stays.
- Existing planner rows keep their ids. When `Resolve` registers a new
  record for a `(kind, session)` that already has a db row, the record
  **reuses that row's id** instead of minting one. That keeps history
  joined across the upgrade.
- `binding.builder_mode` keeps `'pane'` as a valid historical value;
  nothing new writes it. `usage.ModePane` stays as a read-only constant
  for history. Remote builders and consults that defaulted to it
  (`usage.go:55,73`) get their real mode.

### 3.5 Status JSON (`relay status --json`, statusline, `relay ui`, serve `FlatStatus`)

Removed: `planner_pane`, `planner_status`, `planner_focused`, `workspace`,
`builder_pane`, `foreign`, `sub_agents`, `nudge`, `hold`, `stop_grace*`,
`herdr_error`. Added: `planner_id`, `planner_name`, `planner_route`
(`channel` | `deliverer` | `none`), `planner_route_live` (bool). Every
consumer in the tree is updated in the same PR. The remote wire protocol
(`internal/remote/proto.go`) carries none of these fields and doesn't change
(spike and audit agree).

## 4. Interfaces and contracts

### 4.1 `planner.Registry`: owns the planner records

```
Get(id string) (Record, error)                    -> ErrNotFound
ByName(name string) (Record, error)               -> ErrNotFound
BySession(kind, sessionID string) (Record, error) -> ErrNotFound
List() ([]Record, error)
Create(r Record) (Record, error)       -> ErrNameTaken, ErrSessionTaken, ErrInvalid
Adopt(id, kind, sessionID string, now time.Time) (Record, error)
                                       -> ErrNotFound, ErrSessionTaken (held by a
                                          different record: caller must forget it first)
Rename(id, name string) (Record, error) -> ErrNameTaken, ErrInvalid
Touch(id string, now time.Time) error  -- best effort; never fails a caller's verb
Forget(id string) error                -> ErrInUse when a non-DONE binding names it
```

Writes are atomic (temp file + rename) under the store lock the verbs
already take. `FileRegistry{Root}` is the only implementation; tests use it
on a `t.TempDir()`.

### 4.2 `planner.HarnessIdent`: which harness session is calling

```
Detect(env func(string) string) (kind, sessionID string, ok bool)
```

This is a table of rules, first match wins:

| kind | rule | status |
|---|---|---|
| `claude` | `CLAUDECODE=1` and `CLAUDE_CODE_SESSION_ID` non-empty | verified §1.1 |
| `opencode` | to be established in Step 0 (§7) | **unverified** |
| `agy` | none; explicit `relay planner register` only | by design until #300's agy spec |

A kind with no rule can only resolve through `--planner` / `$RELAY_PLANNER`.
`Detect` never shells out and never reads a file.

### 4.3 `planner.Resolve`: the only way a verb gets its planner

```
Resolve(reg Registry, in ResolveInput) (Record, Resolution, error)

ResolveInput{ Flag string; Env func(string) string; CWD string;
              Register bool; Now time.Time; Ident HarnessIdent }
Resolution = "flag" | "env" | "session" | "registered"
```

Errors: `ErrNoPlanner` (nothing resolved and `Register` is false),
`ErrUnknownPlanner{Ref}` (a flag or env value that matches no id or name),
`ErrNoSession` (`Register` is true but `Detect` found no session: "run this
from a planner session or pass --planner").

Who passes `Register: true`: `bind`, `add`, `fork`, `ask`, and `relay mcp`.
Everyone else passes false: `send`, `wait`, `pull`, `status`, `done`,
`stop`, `log`, `show`, `diff` and `statusline`. A verb that is given a
binding by name doesn't need a planner and never resolves one.

### 4.4 `relay mcp` contract changes

- On start: `Resolve(Register: true)`, then `Registry.Touch`, then claim
  `channels/<planner-id>.json`. `--pane` is removed; `--planner <id|name>`
  overrides. When no planner can be resolved, it runs tools-only and says so
  on stderr, the same as today's "no claim store" path.
- The `status` tool filters by planner id, not pane.
- The `answer` tool is deleted (D2). The instructions text
  (`internal/mcp/instructions.go`) loses `broken` and `orphaned`, keeps
  `report` and `needs_you`, and says that a report's body is the report
  (with #297; see §7).

### 4.5 Planner delivery (`DeliverPending`, after PR 3)

```
DeliverPending(ctx, rt, b) (Binding, Delivery, error)
Delivery.Route = "channel" | "deliverer:<kind>" | "none"
```

Postcondition: a pending entry is either handed to exactly one route and
marked delivered, or left pending with `Delivery.Reason` set. It is never
typed anywhere.

### 4.6 `relay planner` verbs (CLI; tests exercise the rules in `internal/planner`, no herdr, per CLAUDE.md)

```
relay planner list [--json]                      id, name, kind, session, cwd, seen, route
relay planner register [--kind K --session S] [--name N]   default: Detect()
relay planner adopt <id|name>                    attach the calling session to an existing planner
relay planner rename <id|name> <new-name>
relay planner forget <id|name>                   refuses while a live binding names it
```

`adopt` is how a restarted or resumed planner session keeps its bindings.
It replaces today's `--assume-dead` rebind.

### 4.7 `relay doctor` checks (replace every herdr check)

| check | when | severity |
|---|---|---|
| relay plugin enabled in Claude Code (`enabledPlugins["relay@relay"] == true` in user or project settings) | a `claude` candidate or planner exists | **FAIL** (D6) |
| the calling session has a live channel claim | run from a Claude Code session (`Detect` = claude) | FAIL |
| opencode server reachable (`$XDG_STATE_HOME/opencode/service.json`, loopback) | an opencode planner record exists | WARN |
| planner records whose `seen_at` is older than 7 days and that no live binding names | always | INFO: suggest `relay planner forget` |

`herdr.MinVersion` and every `HERDR_*` read are removed. `UsableBuilder`
means "binary on PATH and the candidate parses".

## 5. Pseudocode

### 5.1 Resolve

```
Resolve(reg, in):
  if in.Flag != "":   return lookup(reg, in.Flag), "flag"     # id or name, else ErrUnknownPlanner
  if v := in.Env("RELAY_PLANNER"); v != "": return lookup(reg, v), "env"
  kind, sess, ok := in.Ident.Detect(in.Env)
  if ok:
    if r, err := reg.BySession(kind, sess); err == nil: reg.Touch(r.id); return r, "session"
    if !in.Register: return ErrNoPlanner
    id := dbPlannerID(kind, sess) or mint()                  # §3.4: reuse history's id
    return reg.Create({id, kind, sess, cwd: in.CWD, now}), "registered"
  if in.Register: return ErrNoSession
  return ErrNoPlanner
```

### 5.2 bind / add / fork / ask

```
r := Resolve(Register: true)
b.PlannerID = r.id
b.Planner = Endpoint{Kind: r.harness_kind, SessionID: r.session_id,
                     TranscriptLocator: r.transcript_locator}
builder: headless or remote only; --headless is accepted and ignored with a
  one-line stderr note ("headless is the only local mode") for one minor release
```

### 5.3 Daemon tick (after PR 3; `ListAgents` is gone)

```
for each binding b not DONE:
  if legacyPane(b): retire(b); continue                      # §5.6
  reconcile builder: headless (process, stream, markers) | remote (poll) -- unchanged
  if b has a pending planner entry: DeliverPending(b)
```

### 5.4 DeliverPending

```
e := PendingForPlanner(b); if none: return
if c := rt.Channels.Live(b.PlannerID, now); c != nil:
    enqueue(mailbox, e); mark delivered, route=channel; return
if d := rt.Deliverers[b.Planner.Kind]; d != nil:              # #300
    res := d.Deliver(ctx, b, e)
    if res.Confirmed: mark delivered, route=deliverer:<kind>; return
    leave pending, reason=res.Reason; return
leave pending, reason="no push route for planner <id> (<kind>): relay wait/pull"
```

A pending entry stays pending until a route takes it or `relay pull` reads
it. `relay pull` marks it delivered with `route=pull`.

### 5.5 relay mcp start

```
r, how, err := Resolve(Register: true, Flag: --planner)
if err: tools-only, stderr note, return
rt.Channels.Write(Claim{Planner: r.id, PID, StartedAt, SeenAt, CWD, Version})
  ErrClaimHeld -> exit, as today
poll: Touch(r.id) at most once a minute; drain mailbox for bindings where PlannerID == r.id
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

`relay status` shows retired bindings as DONE with the note. History rows
are untouched.

## 6. Error handling and verification

### 6.1 Errors

| error | recoverable | surfaced as |
|---|---|---|
| `ErrNoPlanner`, `ErrNoSession`, `ErrUnknownPlanner` | yes: pass `--planner` or run from a planner session | CLI exit 1 with the fix in the message |
| `ErrNameTaken`, `ErrSessionTaken`, `ErrInUse` | yes | CLI exit 1 |
| registry file corrupt | no for that record | `doctor` FAIL naming the file; verbs that don't resolve are unaffected |
| no push route | yes | a status `planner_route=none` row; the report waits for `pull` |
| deliverer refusal (#300's taxonomy) | per #300 | unchanged |

### 6.2 Observability

Every resolution logs `planner=<id> via=<resolution>` at debug. Every
delivery `LogEntry` gains `route`. `relay planner list` is the view.

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
`PATH` writes the report and done marker. The scenario: register a planner
via env (`CLAUDECODE=1`, `CLAUDE_CODE_SESSION_ID=<fixed>`), start an
in-process `relay mcp` stdio client, bind, send, receive the report on the
channel, pull, done. The herdr-session e2e (`internal/relay/e2e_test.go`)
and the fake herdr in `internal/e2e/fakes_test.go` are deleted. CLAUDE.md's
"run `make e2e` after reconcile/reporttail changes" rule becomes "CI runs
it".

## 7. Ordered delivery

**Gate on everything below: #300 merged.** #300 adds its deliverer inside
`DeliverPending` and reorders `PendingForPlanner` above the `FindAgent` gate.
PR 1 rewrites the claim guard in the same function, and PRs 2–3 delete the
gate. Cutting any of them before #300 merges means a rebase through the one
function that both sides change.

Also hold **#293 part 2** (`relay update` self-update). #310 detects
`plugin-release` and `plugin-source` installs, and this spec deletes both.

| step | owner | deliverable | depends on | done when |
|---|---|---|---|---|
| 0 | planner, not a builder | establish `HarnessIdent`'s opencode rule: the env an opencode planner's tool subprocess sees, and whether it names the `ses_` id; also whether a Claude Code `--resume` keeps `CLAUDE_CODE_SESSION_ID` | -- | a row in §4.2 marked verified, or "explicit-only" recorded |
| 1 | builder | **Planner identity**, with herdr still in the tree: `internal/planner`, `relay planner` verbs, `Binding.PlannerID`, bind/add/fork/ask use `Resolve` (herdr is still a source of `PaneID` for the pane path), `relay mcp` registers and claims by planner id, MCP `status` filters by planner, ingest uses record ids | #300 merged, step 0 | `make check`; a binding made from a Claude session shows `planner_id`; a second `relay mcp` in the same session is refused by planner id |
| 2 | builder | **Headless-only builders**: delete pane builder mode, `relay answer` + MCP tool + `pick` answer, `stop --grace/--now` and `stopPrompt`, ui pane capture, harness `PaneArgs`/`Integration`/`SubAgents`/coverage, the pane usage reader, the `--builder <pane>`/`--workspace`/`--assume-dead`/`--held-grace` flags; `--headless` becomes a no-op; the §5.6 retire rule | 1 | `make check`; no builder code path reads a pane; the ported tests listed |
| 3 | builder | **Planner delivery without herdr**: §5.4 routes, delete pane delivery and `held.go`, the notifications (D4), daemon `ListAgents`, ORPHANED/BROKEN-from-herdr, status JSON §3.5 and every consumer, doctor §4.7, the `internal/herdr` package, every `HERDR_*` read | 2 | `grep -rli herdr --include=*.go . \| grep -v _test` is empty; `make check` |
| 4 | builder | **Everything that isn't Go**: packaging, `scripts/plugin-*`, `check-plugin-version` reduced to the Claude plugin manifests, `make release` and `release.yml`, README/CONTRIBUTING/CLAUDE.md, architect and reviewer definitions (and `TestArchitectHandoffIsSharedAcrossKinds`), `release.Provenance` without the plugin kinds, issue templates, `dist/` comments | 3 | `make check`; `grep -rli herdr . --exclude-dir=.git` lists only `docs/specs`, `docs/plans` and `docs/superpowers` history |
| 5 | builder | **Headless e2e in CI** (§6.4) | 3 | the CI job runs it; mutation check: break §5.4's channel route, and the test fails |

After step 5: #303 closes. #205 closes as superseded. #292 (rename) can
start on the smaller tree. #297 is independent and may land at any point;
it's worth landing before step 3, because the channel becomes the only push
route.

Every plan cut from this spec states: if a step is impossible as written or
contradicts the code, halt and report. CI runners have no `herdr`; after
step 3, no test may reference one.
