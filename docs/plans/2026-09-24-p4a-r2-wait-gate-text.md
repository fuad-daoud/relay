# P4a round 2: wait prints the report (pull goes); payloads name `relevo show`, not paths; `gate` replaces unavailable/available; planner prune is automatic; the planner-facing text is updated

Continue on `relevo/p4a`. Round 1 (bind/show/unbind/status merges) is merged to main, and the
planner reset this branch to main. Spec: `docs/specs/2026-09-24-db-as-record-design.md` D7, §6.
**Extend `removedVerbs`; add no second refusal mechanism.**

**Stop rule:** if a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit. Tests change only as §8 sanctions.

## 1. System overview

Four changes, each keeping behaviour and changing spelling or wording:
1. `relevo wait` delivers.
2. Planner-visible texts stop naming state-dir file paths. A closed round's files may be sealed
   into the DB (P3c), so the texts name the `relevo show` command that prints them.
3. One `gate` verb.
4. The daemon prunes dead planners itself.

## 2. File structure

```
internal/relevo/wait.go, pull.go          Wait delivers (§4.1)
cmd/relevo/main.go                        cmdWait prints; pull/unavailable/available removed; cmdGate (§4.3)
cmd/relevo/serve.go                       serve gates/available/unavailable removed (§4.3)
cmd/relevo/planner.go                     planner prune removed; daemon prune (§4.4)
internal/relevo/daemon.go                 hourly planner prune
internal/relevo/{reconcile,headless,stop,remote,consult,capture,gate,drift,push,deliver_agy,drain}.go   texts (§4.2)
cmd/relevo/show.go                        + --gate, --findings ID (§4.2)
internal/mcp/{tools,instructions}.go      the wait command and the text
internal/planner/handoff.md, internal/harness/agents/*   text; then scripts/agents-shipped.sh --write
CLAUDE.md                                 the command names in "Working with builders"
```

## 3. Data structures

None change. `LogEntry.Path` keeps storing the path as an identifier. It is no longer shown to
planners.

## 4. Contracts

### 4.1 wait delivers

- `relevo wait --name N [--any] [--round R] [--timeout D] [--peek]`: the exit codes are unchanged.
- On every exit **except** 124 (timeout) and 4 (binding done), after it prints today's outcome line:
  - print a blank line;
  - print the pending payload for that binding exactly as `pull` printed it (`PushText`), using
    the same selection and the same remote `SyncRemote` first;
  - mark it delivered with route `"wait"`, as `Pull` does with `"pull"`, unless `--peek` is given;
  - with nothing pending, print only the outcome line.
- With `--any`, deliver for the binding that ended the wait.
- `pull` is removed and joins `removedVerbs`: `"pull": "relevo wait (it prints the report)"`.
- **`Pull`'s internals** become the helper `Wait` calls. **Delete** the `--path-only` mode.

### 4.2 Texts name `relevo show`

**`show` gains two sections**, reading through `rt.Store.ReadFile`:
- `--gate` prints the round's gate log (`GateLogPath`);
- `--findings ID` prints a consult's findings (`FindingsPath(name, round, ID)`).

**Payload and hint texts.** Every string below loses its file path and names the show command
`relevo show <name> --round <n> --<section>`:

| site | today | becomes |
|---|---|---|
| `reconcile.go:360` | `"Builder finished round %d. Report: %s"` | `"Builder finished round %d. Report: relevo show %s --round %d --report"` |
| `reconcile.go:368` | `"…but no report at %s."` | `"…but wrote no report."` |
| `headless.go:647-649`, `stop.go:188`, `remote.go:1232` | analogous | analogous |
| `consult.go:197` | the findings path | `relevo show <name> --round <n> --findings <id>` |
| `consult.go:219-220` | the verdict line ending with the findings path | ends with the same show command |
| `capture.go:268-270` `DiffLine` | `"Diff: %s (…)"` | `"Diff: relevo show %s --round %d --diff (…)"` |
| `gate.go:168-186` `gateLine` | `"Output: %s"` | `"Output: relevo show %s --round %d --gate"` |
| `drift.go:109-111` `DriftLine` | the path | `relevo show %s --round %d --drift` |
| `push.go:68` | the truncation tail | `"[truncated at %d KiB -- full text: relevo show %s --round %d --report]"`; for findings, the findings form |
| `deliver_agy.go:311-316` | oversize: `"Read it: %s"` | `"Read it: relevo show …"` |
| `drain.go:92-94` | `meta["path"]` | removed; add `meta["show"]` with the show command |
| `headless.go:803,807` | halts that say `log: %s` / `see %s` | name `relevo show <name> --round <n> --log` |
| `repair.go:105,108` | `see %s` | name `--gate` |
| `repair.go:70-77` (the repair plan text, which the **builder** reads) | | **keeps** its paths: the builder reads files on disk, and the round is not sealed while repair runs |
| `verify.go:37-49` (the reviewer prompt) | | **keeps** its paths, for the same reason |

`PushText` still appends the report or findings **text**; only the head line changes. Where a
helper lacks the binding name or round, thread it. **Do not look it up from the path.**

### 4.3 gate

| form | behaviour | replaces |
|---|---|---|
| `relevo gate` | list active gates (today's `serve gates` rendering, fed by `relevo.Gates(rt)`) | — |
| `relevo gate <token> [--for D] [--reason S]` | exactly `cmdUnavailable` | unavailable |
| `relevo gate --clear <provider\|token>` | exactly `cmdAvailable` | available |
| `relevo gate --serve [--state DIR] …` | the same three forms against the local serve daemon's gates (today's `serve gates`/`serve unavailable`/`serve available`, via `ledgerRuntime`) | serve gates, serve unavailable, serve available |

- `removedVerbs` gains `unavailable` → `relevo gate <token>` and `available` → `relevo gate --clear <provider>`.
- In `cmdServe`, the `gates`/`available`/`unavailable` cases print
  `relevo serve <x> was removed; use relevo gate --serve …` and exit 2.
- The daemon's own texts that tell the planner to run `relevo unavailable` become `relevo gate`.

### 4.4 Planner prune is automatic

- The daemon runs `planner.Prune(reg, rt.ProcStart, bindingCounts, false)` at most once per hour.
  The last run is kv `planner.pruned_at` in the store DB. Each forgotten record gets one
  `slog.Info`.
- `relevo planner prune` prints `was removed; the daemon prunes dead planners hourly` and exits 2.
  `planner list/rename/forget/init` stay.

### 4.5 Planner-facing text

**`internal/mcp/tools.go:85-88` `WaitCommand`** becomes `relevo wait --name <n> --timeout <budget>`.

**`internal/mcp/instructions.go`:**
- :14-17: "a very large report is cut short … naming the full path" → "… naming the `relevo show`
  command that prints it in full".
- :56-60: rewrite the pull paragraph. `relevo wait`'s output **is** the report text, and an
  unmarked or halted round's report is printed by `relevo wait` too.
- Keep every other sentence.

**`internal/planner/handoff.md`, and the same text in the architect definitions:**
- `relevo add --name <name>` → `relevo bind --worktree --name <name>`
- `relevo wait --name <name> --timeout <budget>; relevo pull --name <name>` → `relevo wait --name <name> --timeout <budget>`
- "then `relevo pull <name>`" (the other-harness loop) → drop it; wait prints the report
- `relevo unavailable <token> --reason '…'` → `relevo gate <token> --reason '…'`
- `relevo available <provider>` → `relevo gate --clear <provider>`

Then run `scripts/agents-shipped.sh --write`. The byte-equality test between handoff.md and the
architect copies must pass.

**CLAUDE.md "Working with builders":** apply the same renames, and `gc` → `relevo unbind --done`.

**The grep baseline must come back clean** outside `docs/`, `removedVerbs` and refusal tests:
`grep -rn -e 'relevo pull' -e 'relevo unavailable' -e 'relevo available' -e 'relevo add ' -e 'relevo fork' -e 'relevo gc' -e 'relevo diff' -e 'relevo log ' -e 'relevo statusline' --include='*.go' --include='*.md' --include='*.toml' .`

## 5. Pseudocode

In §4.1.

## 6. Error handling

- A delivery failure inside wait (read or confirm) is printed to stderr, and wait's exit code is
  unchanged.
- The rest is unchanged.

## 7. Working efficiently

- **Read in one batch:**
  - `internal/relevo/{wait,pull,push,drain,reconcile(350-370),headless(640-660,800-810),stop(180-230),remote(1225-1260),consult(190-230),capture(260-275),gate(160-210),drift(100-131),repair(60-110),deliver_agy(300-320),daemon}.go`;
  - `cmd/relevo/{main.go (cmdWait, cmdPull, cmdUnavailable, cmdAvailable, removedVerbs), serve.go (gates/available/unavailable), planner.go (prune), show.go}`;
  - `internal/mcp/{tools,instructions}.go`, `internal/planner/handoff.md`, CLAUDE.md.
- **Script the §4.2 string swaps.**
- **Focused loop:** `go test ./internal/relevo/ ./internal/mcp/ ./internal/planner/ ./internal/harness/ ./cmd/relevo/ ./internal/serve/ -count=1`.
- **Full check** once at the end: `make check`, then `make e2e`.
- **CI has no harness and no network.**

## 8. Ordered steps

**Closed deletion list:**
- **D1:** the `pull`, `unavailable` and `available` dispatch cases; the serve `gates`,
  `available` and `unavailable` subverbs; `planner prune`.
- **D2:** `pull --path-only`.

**Sanctioned ports:**
- Tests asserting any §4.2 string: change the expected text.
- Tests invoking a removed verb: change to the new spelling.
- Tests asserting `meta["path"]` → `meta["show"]`.
- `pull --path-only` tests: delete them, citing D2.

**Any other failing test: stop and report.**

1. **wait delivers; pull removed** (§4.1). Test: a seeded binding with a queued report. `wait`
   prints the outcome and then the report text, and marks it delivered with route `wait`.
   `--peek` leaves it pending. A timeout prints nothing extra.
2. **show `--gate`/`--findings`; the §4.2 texts.**
3. **gate** (§4.3) + a refusal test for the five removed names.
4. **Automatic planner prune** (§4.4), with a pure test of the once-an-hour decision.
5. **§4.5 text**, then `agents-shipped.sh --write`, then the grep.
6. **Full check.** Report:
   - the tails;
   - `git diff --stat`;
   - the grep output;
   - D-numbered deletions;
   - ported tests.

   Commit as one commit. Do not rebase.
