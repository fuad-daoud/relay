# Housekeeping spike: commands, data, and what to cut

Date: 2026-09-24. HEAD 7541ef7. Read-only spike; nothing here is decided yet.
Goal from the user: fewer files, fewer commands, no config files, and the
database as the store of record instead of files.

## 1. Headline findings

1. **About 76 entry points.** There are 42 top-level verbs (`cmd/relevo/main.go:313-402`)
   and 33 subverbs under 7 dispatchers. The verbs that planners actually use are about 10 of them.
2. **The DB is a mirror, not the record.** `relevo.db` (202 MB) is filled only by
   `internal/ingest`, which the daemon runs at the end of each tick over live bindings. The
   files are the source of truth (phase 1 of `2026-09-20-persistence-design.md`).
3. **The mirror is wrong today, and live data confirms it:**
   - `gc`/`unbind` archive or delete a binding without ingesting it first (`internal/relevo/gc.go:76`,
     `bind.go:763`). As a result, 6 tarballs are missing from the DB, 3 rows still say `active`, and a
     deleted binding still says `needs_you`.
   - `log.jsonl` is rewritten in place (`store/log.go:451-525`), but ingest assumes it only grows
     (`ingest/cursor.go:55`, `INSERT OR IGNORE`). Confirmed and delivered counts drift: roles-s1 has 25
     in the file and 17 in the DB.
   - `relevo serve` has no DB at all (`internal/serve/serve.go:184`).
   - Usage is NULL for 238 of 350 rounds (only report entries since 2026-09-18 carry it).
   - Planner transcripts (23k rows) are written and never read.
4. **Two read paths for the same numbers.**
   - `tab` and `stats` read the logs and tarballs (`internal/relevo/tab.go:59`).
   - `history`, `show` (archived) and the `ui` dashboard read the DB.
   - `status`, `log`, `pull`, `diff` and `wait` read the files.
5. **About 12 separate persistence mechanisms.**
   - The temp-file-plus-rename pattern is written out 17 separate times in 13 packages.
   - Locking uses 3 file locks plus one in-process mutex.
   - Three near-identical JSON logs: `ledger.json`, `availability.json`, `latency.json`.
6. **The config repeats itself.** `roles.json` repeats `policy.json` order/tier and
   `candidates.json` roles/tier. `relevo policy` still reads `policy.OrderFor`
   (`internal/relevo/policy_view.go:41`). About 30 knobs can only be changed by hand-editing.

## 2. Command inventory

Callers: H human, P planner session, PL plugin (MCP or slash command), HK hook, SL statusLine,
SD systemd/launchd, RX re-exec, SRV server admin.

### Planner loop
| verb | caller | overlap |
|---|---|---|
| bind | H P | add/fork create bindings too |
| add | H P | `add --cwd` is bind in another dir |
| fork | H | add seeded from a round |
| send | H P PL | MCP `send` |
| wait | P | always followed by pull |
| pull | P | is `show --report` + the delivered mark |
| status | H P PL | statusline is a filtered compact status |
| statusline | SL | — |
| ask | H P | — |
| unavailable / available | H P | twins of `serve unavailable/available` |
| policy | H P | overlaps roles, candidates, doctor |
| done | H P PL | stop + DONE + worktree release |

### Reading rounds
| verb | overlap |
|---|---|
| show | superset per round of diff/log/report |
| diff | `show --diff` (+ `--stat`, `--anchors`) |
| log | `show --log` (+ `--follow`, `--after`) |
| history | DB; `--by` duplicates tab |
| tab | logs; duplicates `history --by` |
| stats | same `TabEntries` as tab |
| ui | TUI over show/diff/log/status |
| review | path:line comments → plan |

### Lifecycle
stop (process half of done), pause (worktree half of done), unbind, gc (`done`+`gc` = `unbind --archive`), land.

### Config and setup
init, candidates [--probe], roles, roles init (legacy converter), agent print, agent install
(also runs on every daemon start), db path/migrate/stats/backfill (migrate is a no-op: every open
migrates; backfill exists to repair the gaps in §1.3), doctor, migrate (relay→relevo only).

### Planner records
planner init (HK), list, rename, forget, prune (not in README, never run automatically).

### Edges
edge add, list, rm.

### Remote
- Client: client init, add-server, rm-server; servers (the same probe as doctor's server rows).
- Server: serve (run), init, enroll, clients, revoke, fingerprint, status, log, show, tab, gates,
  available, unavailable, ui, unbind, gc.

### Infrastructure
daemon (SD; `--preflight` RX; `--check` has no caller outside the README and tests), mcp (PL: tools status/send/done),
help, version.

### Dead or stale
- `relevo answer` hint (`internal/relevo/waiting.go:80`): no such verb exists.
- `relevo watch` hint (`internal/ui/source.go:124`): no such verb exists.
- The marketplace.json description lists an "answer" tool that doesn't exist.
- `--headless`, accepted and ignored on 4 verbs.
- `gc --archive`, a no-op.
- `aliases.json`: never read (`main.go:541`).
- `NNN-question.md`: nothing has written it since #303.
- `candidate.DialogPatterns`: ignored.
- A 25 MB untracked `relay` binary in the repo root.

### Bugs found along the way
- 16 serve handlers, `agent`, `migrate` and `init` use raw `fs.Parse`, so a flag after a positional
  argument is silently dropped (the #48 bug class). For example, `serve log --owner X name --round 2` loses `--round`.
- Doctor reads `~/.config/opencode/service.json` (`internal/doctor/opencode.go:15`), but the
  deliverer reads `$XDG_STATE_HOME/opencode/service.json` (`cmd/relevo/main.go:475`).
- Bare `relevo serve` exits 2, although the usage text says it runs the server.

## 3. Data map

### Config: `~/.config/relevo`
| file | edited by | could be DB? |
|---|---|---|
| candidates.json | hand (init writes a starter) | yes, with a setter/editor verb |
| policy.json (~30 knobs) | hand | yes |
| roles.json | hand (roles init writes it once) | yes |
| prices.json | hand | yes |
| servers.json | `client add-server/rm-server` | yes |
| client.key / client.pub | `client init` | secret (decision) |
| typesafe.key | hand | secret (decision) |
| hooks/<event>.d/* | hand (executables) | the list could be; scripts are files |
| aliases.json, *.bak-* | nothing | delete |

### State: `~/.local/state/relevo`
| item | DB copy today | could be DB? |
|---|---|---|
| relevo.db | — | — |
| `.lock` (state flock) | — | replaced by sqlite transactions |
| `.daemon.lock` | — | **stays**: process-lifetime flock |
| daemon.json | no | yes (keep cheap to read) |
| ledger.json + availability.json (mirror each other) | no | yes, one table |
| latency.json | no | yes |
| release-check.json, ui.json, agents-manifest.json | no | yes |
| hooks.log | no | yes, or drop |
| planners/<id>.json + planners/.lock | partial (`planner`) | yes |
| planners/.agy/<conv>.json | no | secret cache (decision) |
| channels/<planner>.json | no | yes |
| `.archive/*.tar.gz` (182, never pruned) | yes after backfill | yes: archived = a row flag |
| `.worktrees/<name>` | paths only | **stays**: git |
| `<name>/bind.json` | partial | yes (the phase-3 target) |
| `<name>/log.jsonl` | yes (`event`) | yes |
| `<name>/NNN-plan.md` | yes | **file while the round is open**: the builder reads it |
| `<name>/NNN-report.md`, `NNN-done` | yes / outcome | **file while open**: the builder writes them |
| `<name>/NNN-builder.{log,jsonl}`, `NNN-gate.log`, consult streams | yes / partial | **file while running**: child processes append O_APPEND |
| `<name>/NNN-<id>-ask.md` | yes | **file while open**: the consult reads it |
| `<name>/NNN-diff.patch`, `NNN-drift.patch`, findings, review-plan, land-gate.log, `.viewed` | mostly | yes |

### Serve: `<state>/serve` (no DB)
clients.json, server.key/crt (TLS), daemon.json (a pointer, with a different schema from the state
root's daemon.json), bindings/<owner>/ (a full store per owner), repos/<owner>/*.git (git: stays),
tmp/, ledger.json, availability.json, ui.json.

### Outside relevo's roots (these stay files by contract)
- Agent definitions in `~/.claude/agents`, `~/.config/opencode/agents` and so on (the harness reads them).
- `$CLAUDE_ENV_FILE`.
- Systemd and launchd units.
- git refs `refs/heads/relevo/<name>`, `refs/relevo/<name>/{out,round-N}`.

## 4. The floor: what must stay a file

1. The open round's I/O: plan, ask, report, done marker, and the builder/gate/consult streams. The
   builder and child processes are separate programs that speak files, and they survive daemon
   restarts in their own scopes.
2. `.daemon.lock` (a process-lifetime flock that must not need sqlite).
3. git worktrees and refs.
4. Agent definitions installed into harness config dirs, plus service units.
5. `relevo.db` itself.
6. Secrets, **if** the user wants them kept out of the DB: client.key, server.key/crt,
   typesafe.key, agy creds.

Everything else can be a row. So the steady state could be: `~/.config/relevo/` empty or gone;
`~/.local/state/relevo/` = `relevo.db`, `.daemon.lock`, `.worktrees/`, and a spool
dir per *open* round that is ingested into the DB and deleted when the round closes.

## 5. Proposed direction (for the design, not decided)

- **DB becomes the record.** Every verb writes rows in a transaction. That removes ingest, the
  cursors, the tarball archives, `db backfill`, the `.lock` flock, the planner registry lock, all
  17 atomic-write copies, and the gc/unbind and log-rewrite drift bugs. serve gets the same DB, with
  an owner column.
- **Config becomes rows.** Add `relevo config` (show / set / edit, where edit round-trips through
  $EDITOR as one JSON doc and validates before it writes). This replaces candidates.json,
  policy.json, roles.json and prices.json, plus the `candidates`, `policy`, `roles`, `roles init`
  and `init` verbs. Hot reload switches from checking file mtimes to a config version stored in the DB.
- **The command set shrinks** (~76 → ~25). Candidate merges:
  - bind absorbs add and fork;
  - show absorbs diff and log;
  - wait absorbs pull (`wait` prints the report);
  - history absorbs tab and stats, once the DB is complete;
  - unbind absorbs gc;
  - drop pause;
  - one `gate` verb for unavailable/available and the serve twins;
  - `server add/rm/list` for client and servers;
  - `db *` goes into doctor;
  - `agent` becomes automatic (`doctor --fix`);
  - planner prune becomes automatic;
  - serve log/show/tab become `--owner` on the normal verbs.
- **Delete the legacy code:** relay→relevo migrate, the rename guard, internal/legacy, `roles init`,
  `--headless`, `gc --archive`, and the dead hints.

Suggested order:
1. **Cleanup and bugs.** Dead code and the §2 bugs. Small and independent.
2. **Config → DB** with `relevo config`, plus import from the existing files once.
3. **State → DB**: bindings, events, gates, planners, channels, the small JSONs; the round spool.
4. **Command consolidation.** It comes after step 3 so `history` can absorb tab/stats. It also
   updates handoff.md, the architect definitions, the MCP instructions and the plugin commands.
5. **serve on the DB.**

## 6. Decisions (user, 2026-09-24)

1. **Legacy relay→relevo code stays for one more release.** It is deleted in the first release
   after the next tag. It is not part of phase 1.
2. **Everything goes in the DB, secrets included:** client.key, the server TLS key and cert,
   typesafe.key and the agy creds. The consequences: relevo.db (and serve's DB) must be 0600, and
   any copy or backup of the DB carries the keys. The only files left are the open-round spool,
   `.daemon.lock`, git, harness agent definitions, service units and the DB itself.
3. **Commands: a clean break.** An old name exits non-zero and names its replacement.
   handoff.md, the architect definitions (via `scripts/agents-shipped.sh --write`), the MCP
   instructions and the plugin commands change in the same PR.
4. **Closed rounds live in the DB only.** At close, the round spool is ingested in the same
   transaction that closes the round, and then deleted. The planner payload carries the report
   text, not a path. `relevo show` is how a human reads a round.

## 7. The serve side, live on contabo (2026-09-24, v0.12.0)

Layout: the `relevo-serve.service` user unit runs `~/srv/bin/relevo-serve serve --listen
127.0.0.1:7777 --insecure-http --state /srv/data/relevo-serve`, in `relevo.slice` with a cap of 3 builders.

| where | what | finding |
|---|---|---|
| `/srv/data/relevo-serve/serve/` | clients.json (zen, laptop), ledger.json, availability.json, ui.json, bindings/<owner>/, repos/<owner>/*.git, tmp/ | **no DB**. 130 MB, 610 files, not counting worktrees and repos |
| `serve/bindings/<owner>/` | zen 47 bindings (94 MB), laptop 16 (23 MB) | **all 63 are DONE and none were ever collected**. `.archive` holds another 20 tarballs. Only `serve gc --abandoned` removes anything |
| `relevo serve status` | 61 bindings show `pending report round 1 -> planner` | **suspected bug**: reports on DONE bindings were never acked, so the server keeps them for good and status lists all 63 |
| `repos/<zen>/536fe….git/opencode` | a 40-byte file holding a commit sha, at the top of a bare repo | **suspected bug**: a ref written without the `refs/` prefix, probably by a builder or the harness running git in the wrong dir |
| `~/.local/state/relevo/` (contabo's own client root) | relevo.db (0 bindings, Sep 20), latency.json, ledger.json, agents-manifest.json, ui.json, serve/daemon.json (the pointer to the serve root), serve/tmp | a **second state root** on the server, and nearly all of it is unused |
| `~/.local/state/relevo/.worktrees/` | 30 dirs, 312 MB, relevo source trees **with no `.git`**, no binding dirs | **garbage**, left from the rename/migrate copy (see the memory note on worktree repair from a copy) |
| `~/.config/relevo/` | candidates.json, policy.json, policy.json.bak-* | serve reads the **home** config, not config under its `--state` root |
| unit ExecStartPre | copies the binary to ~/.local/bin/relevo, runs `relevo migrate` **twice** (home root, and relay-serve→relevo-serve), then `agent install --force` | 2 copies of the binary; migrate runs on every restart (goes when the legacy code goes); agent install duplicates the daemon's own refresh |
| `/srv/data/relay-site`, `relevo-site`, `site` | empty dirs | leftovers |

What this adds to the design:
- **Serve on the DB** (phase 5) is where the drift and cost are largest: 610 loose files, with no DB and no collection at all.
- **DONE on the server must end in cleanup.** When a binding is DONE and its last round is acked, the
  server drops the rows and the worktree. Only the git refs the client has not fetched are kept.
  The ack gap (61 pending) needs a root cause first.
- **One root per machine.** serve's `--state` and the home root should collapse into one DB.
  Config belongs in that DB, not in `~/.config`, so the pointer file goes as well.
