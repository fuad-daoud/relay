# File-writes spike: what relevo writes, where, and when

Date: 2026-09-24. HEAD ff04f5d. Spike findings; §9 records what was decided and shipped.
Goal from the user: know every flow in which relevo writes files, and what the files are,
so later changes can be based on that.

Method:
- Static: three readers covered every `os.*` write site in non-test code, 119 of them,
  plus every wrapper, git exec and path handed to a child process.
- Dynamic: one real CLI run of `config`, `config agents`, `planner init`, `daemon`, `bind --worktree`,
  two `send`s, `wait`, `show`, `done` and `unbind --done`.
  - It ran in a sandboxed HOME/XDG/TMPDIR, with a fake `claude` taken from `internal/e2e`.
  - A 50 ms `find` poller logged every file that appeared, changed or vanished.
  - The script is in §8.
- Live: `relevo.db` table sizes (`dbstat`), and the file descriptors of the running daemon.

## 1. Headline findings

1. **Live bug: the daemon leaks one sqlite connection per tick.**
   - `refreshRelease` calls `store.New(root).DB()` on every tick (`internal/relevo/daemon.go:217`).
     This creates a new `Store`, with its own `dbOnce`, and nothing closes it.
   - Measured on the running daemon (pid 3360617, up 24 min): 717 fds on `relevo.db`, and +3 in 6 s.
     At 2 fds per tick and `nofile` = 524288, it runs out of fds after about 6 days of uptime.
   - Every leaked open also runs 3 chmods and 5 migration no-op transactions (`db.go:228-240`, `migrate.go:136-166`).
   - The fix is to use `d.rt.Store.DB()`. The same pattern is in the one-shot CLI at `cmd/relevo/agent.go:27`.
2. **Every round writes 6-8 files to disk, even though the DB is the record.**
   - They go in `$STATE/<binding>/NNN-*`: plan, builder.jsonl, builder.log, report, done, diff.patch, plus gate.log and drift.patch.
   - About 2 s after close, the daemon seals them into `round_file` and deletes them (§2).
   - Disk is the working copy while a round runs, because the builder must read and write real paths.
3. **The DB holds most round data twice.**
   - Of the 422 MB `relevo.db`:
     - `transcript`: 218 MB
     - `round_file`: 149 MB
     - `artifact`: 25 MB
   - The raw builder stream (`round_file` `*-builder.jsonl`, 120 MB) is also stored parsed in
     `transcript` (owner=round, 103 MB).
   - Plan, report, diff, drift and ask are each both a `round_file` row and an `artifact` row.
   - Planner transcripts take 83 MB, and nothing reads them (housekeeping spike §1.3).
   - Nothing ever deletes from `artifact` or `transcript`. Unbind/Delete removes `binding_record`,
     `binding_event` and `round_file` (cascade), but not the mirror tables.
4. **Paths handed out by relevo stop existing about 2 s after the round closes.**
   - Seen in the trace: `relevo wait` printed `…/spk1/001-report.md` at 18:19:05, and the seal deleted it at 18:19:07.
   - Repair rounds (`repair.go:76-77`) tell the builder to read `PlanPath(failed)` and the gate log.
     - `startProcess` moves `StreamRound` to the new round.
     - So the failed round becomes `Sealable` (`seal.go:227`), and the next tick deletes both files
       while the repair builder is still starting.
   - relevo's own readers fall back to the DB (`Store.ReadFile`). External readers (builder, planner) cannot.
5. **Relevo writes outside its state dir in four places.**
   - Role files in 4 harness dotdirs, rewritten on every daemon start (§4.1).
   - Refs, branches and loose objects in user repos, never cleaned up on the client (§4.2).
   - Five kinds of `/tmp` files holding repo contents, which leak on a crash (§4.3).
   - `$CLAUDE_ENV_FILE` appends (§4.4).
6. **Dead code and dead files.**
   - Written but never read by relevo:
     - `land-gate.log`: never sealed, and lost on archive.
     - `NNN-<id>-consult.log`.
     - `NNN-review-plan.md`, without `--send`.
   - Read but never written: `NNN-question.md`.
   - Code with no callers: `store.writeFileAtomic` (`store.go:841`) and `store.copyFile` (`fork.go:148`).

## 2. The round flow, as observed

This is the timeline from the sandbox run. Round 1 closed on its marker. Round 2 was unmarked, because the fake had nothing to commit.
t is seconds after the first DB write; the poll resolves to about 0.1 s.

| t (s) | flow | disk effect |
|---|---|---|
| 0.0 | `relevo config` (first verb) | `$STATE/` (0700 via `openDB`), `relevo.db`, `-wal`, `-shm`. The `~/.config/relevo/{candidates,policy}.json` I dropped in were **imported and deleted**, along with the dir, within 50 ms. |
| 0.4 | `relevo config agents` | 16 role files: `~/.claude/agents/*.md`, `~/.codex/*.config.toml`, `~/.config/opencode/agents/*.md`, `~/.gemini/config/agents/*.md` (4 roles × 4 harnesses) |
| 1.3 | `relevo daemon` | `$STATE/.daemon.lock` (held for the process lifetime) |
| 3.3 | `relevo status` | `$STATE/.lock` (store flock, created on the first `WithLock`) |
| 4.1 | `bind --worktree` | `$STATE/.worktrees/spk1/`. In the repo: `refs/heads/relevo/spk1` and `.git/worktrees/spk1/` (briefly `locked` + `index.lock`) |
| 4.5 | `send` | `$STATE/spk1/` plus `001-plan.md` (a copy of `--file`), `001-builder.jsonl`, `001-builder.log` (empty, opened by proc) |
| 7.6 | builder works | `001-report.md`, `001-done`, stream appended; `fake-round.txt` in the worktree; commit objects in the repo |
| 9.3 | daemon closes the round | `001-diff.patch`, and `001-builder.log` gets the rendered stream. The `wait` output names `001-report.md`. |
| 11.3 | daemon seal (`seal files=6`) | all six `001-*` files **deleted**; the WAL grows |
| 13.3 | `send` (round 2) | `002-plan.md`, `002-builder.{jsonl,log}` |
| 18.2 | `done` | worktree removed (it was clean); `.git/worktrees/spk1` pruned; **branch `relevo/spk1` kept**; an **empty `.worktrees/` dir is left** |
| 19.3 | daemon seal | `002-*` deleted, `$STATE/spk1/` removed (DONE and empty, `daemon.go:407-414`) |

Final state: `.daemon.lock`, `.lock`, `relevo.db*`, an empty `.worktrees/`, and the 16 role files.
`relevo.db` stayed at 4 KB, with 1.1 MB in the WAL (no checkpoint in 25 s).

## 3. Inventory inside `$STATE` (`$XDG_STATE_HOME/relevo`)

### 3.1 Permanent

| path | writer | notes |
|---|---|---|
| `relevo.db`, `-wal`, `-shm` | every verb (`newRuntime` opens it **twice**: `main.go:552` and `:681`), the daemon (3 handles plus the leak), `ui`, `history` and `show` (a third) | chmod 0600 on every open |
| `.lock` | `WithLock` (`store.go:790-798`): O_CREATE + flock on every Store call | cross-process mutex, held up to 90 s |
| `.daemon.lock` | `AcquireDaemonLock` (`daemonlock.go:26`) | probed by status, doctor and migrate |
| `.worktrees/<name>/` | `add.go:256`, `fork.go:243`, `bind.go:239` (resume) | removed only if clean (`bind.go:662-692`); `.worktrees/` itself is never removed |
| `.worktrees/.verify/<name>-NNN/` | `verify.go:226` at round close | removed when the consult ends (`consult.go:242`); a crash leaks it |

The root is created 0755 by `store.go:32,794`, `db.go:32` and `daemonlock.go:66`, but 0700 by `openDB` (`main.go:588`).
Whichever runs first wins. Live `~/.local/state/relevo` is 0755; spec D6 wants 0700.

### 3.2 Per-binding spool `$STATE/<name>/` (transient: sealed, then deleted)

Path helpers are in `store.go:157-312`. Seal: `sealRounds` (`daemon.go:384`), run each tick when `Sealable`
(`seal.go:227`): the round is closed, the stream is drained, and no consult or gate is active on it. Also `SealAll`
(`archive.go:40`) on unbind, gc, archive and delete.

| file | written by | read by (outside the seal) | notes |
|---|---|---|---|
| `NNN-plan.md` | `send.go:354`, `remote.go:559`, `repair.go:116` | builder (path in the prompt), verify, show | a copy of the planner's `--file`; stored 3× (file + `round_file` + `artifact`) |
| `NNN-report.md` | builder, at the path in `builderPrompt` (`send.go:30-49`); remote `remote.go:1053` | `reconcile.go:358,387`, `headless.go:640`, push/pull, show, edges | the path is printed to the planner |
| `NNN-done` | builder (empty) | `os.Stat` only (`reconcile.go:334`, `headless.go:624`) | sealed as an empty row |
| `NNN-builder.jsonl` | proc stdout, O_APPEND (`proc.go:288`); remote `remote.go:1109` | drain every tick, exit code, usage, `StreamDrained` | unbounded; the largest item in the DB (120 MB `round_file` + 103 MB `transcript`) |
| `NNN-builder.log` | proc stderr (`proc.go:283`), drain render (`headless.go:405`), markers (`headless.go:929`); remote: **whole-file rewrite every tick** (`remote.go:760-768`) | `logTail` (`headless.go:422`), limit/denial scans, show | a derived copy of the jsonl |
| `NNN-gate.log` | proc, via `gate.go:119` (the gate runs in `b.CWD`) | exit code, repair tail, show, verify | only with a gate |
| `NNN-diff.patch` | `capture.go:191`; remote `remote.go:1072` | review, show, verify, edges | ≤4 MiB, skipped when empty; could be rebuilt from `RoundBaselineTree`/`RoundClosedTree` |
| `NNN-drift.patch` | `drift.go:63` (on send) | show, `ReadDrift` | filed under the new round's number |
| `NNN-<id>-ask.md` | `ask.go:285`, `verify.go:270` | the consult process | a copy of the planner's file |
| `NNN-<id>-consult.{jsonl,log}` | proc | jsonl: `consult.go:143`; **log: never read** | |
| `NNN-<id>-findings.md` | **relevo** (`consult.go:158`), extracted from the jsonl | push, verify verdict, show | derived; stored 4× |
| `NNN-review-plan.md` | `review.go:128` (the default `--out`) | only `--send` | otherwise written and forgotten |
| `land-gate.log` | `land.go:262` | **nobody** | never sealed; keeps a DONE binding's dir alive; destroyed on archive |
| `log.jsonl` | `fork.go:110` | adopted by `importPresent` on save | orphaned if the caller never saves |
| fork copies of `NNN-*` | `fork.go:127-139`: **DB → disk** | re-sealed under the new record | DB→file→DB round trip; the log paths still point at the source dir |

The directory itself is recreated by `MkdirAll` on every `Save` (`store.go:466`) and `appendLog` (`log.go:335`),
remote bindings included. It is removed only for DONE+empty (`daemon.go:407-414`), archive or delete. The live
`serve-status-json/` dir is an active binding whose round-1 files are already sealed.

### 3.3 "A file that is present is imported" (legacy files, now kv or DB rows)

No current code writes any of these. They are imported and then removed through `db.KVImportFile` (`kv.go:231`).
Once the row exists, **every read still tries an unlink**, and the daemon reads ledger, latency and release every tick.

`daemon.json`, `ledger.json`, `availability.json`/`history.json`, `latency.json`, `release-check.json`,
`ui.json` (the live leftover; only `relevo ui` imports it), `agents-manifest.json`, `planners/*.json`,
`planners/.agy/*`, `channels/*.json`, `hooks.log` (custom import; does not remove a leftover file), `.archive/*.tar.gz`,
`serve/clients.json`, `serve/server.{key,crt}`.

### 3.4 Config dir `~/.config/relevo`

`config/import.go:35` runs on **every CLI verb** (`main.go:562`), on every daemon tick (`reload.go:79`) and in serve.
It imports `{candidates,policy,roles,prices,servers}.json`, `client.key`, `typesafe.key` and `hooks/`.
It then deletes each file it imported, plus `client.pub` and `aliases.json`, and runs `rmdir` on the dir.
This is the drop-in mechanism, so a file you put there is consumed. `*.bak-*` files are ignored, and that is why the live dir survives.

## 4. Inventory outside `$STATE`

### 4.1 Harness role definitions (home dotfiles)

- **Files:** `~/.claude/agents/`, `~/.codex/*.config.toml`, `~/.config/opencode/agents/`, `~/.gemini/config/agents/`.
  Each gets plan-executor, researcher, reviewer and architect (`harness/harness.go:144-219`).
- **How they are written:** atomic temp `.relevo-*` plus rename (`manifest.go:67-93`).
- **When:**
  - **Every daemon start**, including the upgrade re-exec: `main.go:2655` → `refreshRoles`.
  - `config agents`.
  - `config init`.
- **Decision table** (`install.go:180-238`):
  - A missing file is **re-created**, so a deletion is not respected.
  - A file whose hash matches a shipped or manifest entry is overwritten.
  - Any other file is kept unless `--force`.
- **Directories:** they are created for every harness whose binary is on PATH.

### 4.2 User repos (git)

| effect | code | cleaned up? |
|---|---|---|
| branch `relevo/<name>`; `.git/worktrees/<name>` | `add.go:256`, `fork.go:243` | admin entry: yes, on teardown. **Branch: never** (by design) |
| `--branch` tracking branch | `add.go:216`, `remote.go:125` | never |
| loose blobs and trees from `SnapshotTree` (temp index + `add -A` + `write-tree`) | `git/client.go:121-167`, on every send, close and escape check | only by git gc |
| `refs/relevo/<name>/out` | `remote.go:442` (every remote send) | **never** |
| `refs/heads/relevo/<name>`, `refs/relevo/<name>/round-N` | `remote.go:1138` (fetch bundle), `:1169` | **never** |
| repo-wide `git worktree prune` | `client.go:487,539,574,614` on failure/teardown paths | prunes other tools' stale entries too |
| gate side effects | `gate.go:121`, `land.go:255` (`sh -c` in the tree) | n/a |
| `git worktree repair` | `relevo migrate` | one-time |

Nothing writes `.relevo/`, `.gitignore`, `info/exclude` or hooks into a repo.

### 4.3 `/tmp` (`os.TempDir`)

| pattern | code | flow | contents |
|---|---|---|---|
| `relevo-git-index-*/index` | `git/client.go:131` | every send, close and escape check | a copy of the repo index |
| `relevo-probe-*/` | `probe.go:112` | `config` candidate probe | a git repo plus whatever the harness writes; also `~/.claude/projects/-tmp-relevo-probe-*` session dirs |
| `relevo-config-*.json` | `cmd/relevo/config.go:358` | `config edit` | the config doc |
| `relevo-bundle-*.bundle` | `remote/bundle.go:52,97` | remote send and sync | **repo contents** |
| `relevo-start-round-*.tmp` | `remote/client/client.go:403` | remote send | plan + bundle |

All of these are removed by `defer`, so each leaks on SIGKILL or a crash. None is swept at startup.

### 4.4 Other writes outside `$STATE`

- `$CLAUDE_ENV_FILE`: an O_APPEND 0600 `export RELEVO_PLANNER=…` from the plugin's SessionStart hook (`cmd/relevo/planner.go:280-292`).
- `relevo migrate` only:
  - `$XDG_CONFIG_HOME/systemd/user/relevo.service` or the launchd plist, written non-atomically (`migrate/units.go:191`);
  - the old unit and the `relay` binary are deleted.

### 4.5 Serve (`$STATE/serve/`)

- `tmp/req-body-*` and `tmp/plan-*` are swept after 1 h at startup (`serve.go:150-192`).
- `tmp/relevo-bundle-*` is **not swept**, because `serve.go:171` does not match that prefix.
- `repos/<owner>/<id>.git`: bare repos, never removed.
- `bindings/<owner>/.worktrees/<name>`: removed by `cleanup.go:103`.
- TLS and clients are DB rows now; the old files are import-only.
- `relevo doctor` can create `serve/bindings` (`doctor/env.go:193`).

## 5. Defects, ranked

| # | defect | evidence | size |
|---|---|---|---|
| D1 | daemon leaks a DB connection per tick | `daemon.go:217`; live fd count | one-line fix plus a test that counts opens |
| D2 | sealed paths vanish under their readers (repair plan, the planner's report path) | `repair.go:76-77`, `seal.go:227`, trace t=9.3→11.3 | small: keep the failed round unsealable while a repair round is open, or inline the plan |
| D3 | mirror tables (`artifact`, `transcript`) never deleted; duplicate storage | dbstat; no `DELETE` in `internal/db` for them | medium: pick one home per datum |
| D4 | `land-gate.log` is never read, never sealed, lost on archive, and pins a DONE dir | `land.go:169,262`, `seal.go:363` | small |
| D5 | a delete seals first, then the cascade deletes what it just sealed | `store.go:765-771` | small |
| D6 | the state root is 0755 or 0700 depending on which verb ran first | `store.go:32,794`, `main.go:588` | small |
| D7 | empty `.worktrees/` and empty binding dirs left behind; dirs recreated on every Save | trace; `store.go:466`, `log.go:335` | small |
| D8 | ghost unlink on every kv read once the row exists | `kv.go:236` | small |
| D9 | a deleted role file is re-created on every daemon start | `install.go:180-238`, `main.go:2655` | design choice |
| D10 | client refs `refs/relevo/<name>/*` and `relevo/<name>` branches never cleaned | `remote.go:442,1138` | design choice |
| D11 | `/tmp` and `serve/tmp/relevo-bundle-*` crash leaks | §4.3, `serve.go:171` | small: a startup sweep |
| D12 | dead code: `writeFileAtomic`, `copyFile`, `QuestionPath` reader, `consult.log` | §1.6 | trivial |
| D13 | a remote round rewrites the whole builder log each tick | `remote.go:760-768` | small: append the delta |

## 6. What could change: options, not decisions

The file writes fall into three classes, and each has a different lever.

**A. Files that exist because an outside process needs a path**, such as plan, report, done, builder stream, and consult ask/findings.
These cannot become DB-only while builders are CLI harnesses that read and write paths.
The options are about lifetime:
- A1. Seal only when nobody can still hold the path: after delivery, or after the next send, instead of 2 s after close.
  This fixes D2 and lets planners `Read` the path they were given.
- A2. Keep sealing early, but stop printing paths. Print `relevo show …` commands only, as the wait header partly does already.
- A3. Stage the builder's inputs (plan, ask) in the worktree's `.git/relevo/` or a per-round tmp dir, and
  keep `$STATE/<name>/` for outputs only.

**B. Files relevo writes only for itself**: diff.patch, drift.patch, findings.md, builder.log render,
review-plan.md, land-gate.log, fork copies.
- B1. Write these straight to `artifact` rows (spec §5.1 already says so). This removes about half the per-round files
  and the fork round trip.
- B2. Drop `builder.log` as a separate file. Render from the jsonl on demand, and keep stderr in a small separate file.

**C. Duplicate storage in the DB.**
- C1. Choose `round_file` (raw) **or** `artifact`/`transcript` (parsed), not both. Parsed transcript is 103 MB of what
  `round_file` already holds as 120 MB of raw stream.
- C2. Drop planner transcripts (83 MB, unread), or cap them.
- C3. Add retention: delete mirror rows with their binding, and `VACUUM`.

**Outside `$STATE`:** keep the role install, but record deletions in the manifest (D9); delete `refs/relevo/<name>/*` on
unbind/done (D10); sweep `relevo-*` temp files at daemon start (D11).

## 7. Suggested order

1. D1 (live leak), now.
2. D2 + A1, to fix the dangling paths.
3. B1 + B2, to cut per-round files.
4. C1 + C2 + C3, to shrink the DB.
5. The small items D4-D8 and D11-D13 as one cleanup round.

## 8. Reproduce the trace

The scripts `drive.sh` and `poll.sh` are in the appendix below. To run them:
- build `./cmd/relevo` into `bin/`;
- extract `fakeHarnessScript` from `internal/e2e/headless_test.go` into `bin/claude`, adding a `sleep 3`;
- run the flow listed under Method with `HOME`, `XDG_*` and `TMPDIR` under the sandbox;
- poll with `find -printf '%p\t%y\t%s'` every 50 ms, and log the diffs with a phase label.

Two things are needed: `relevo config agents` before bind (otherwise "roles missing"), and
`planner init --kind claude --session S`. Events are labelled with the phase current when the poll saw them, so a
command's writes can carry the next phase's label.

## 9. Outcome

Shipped the same day (each PR verified against its plan's scope, with a mutation check and all CI jobs green):

| PR | Item | Change |
|---|---|---|
| #416 | D1 | the daemon opens the release-check database once instead of once per tick |
| #418 | D2, A1 | a closed round stays on disk until the next round closes (or the binding is DONE) |
| #419 | D3 (C1) | archived `relevo show` reads `round_file`, which also gives it the gate and findings sections |
| #423 | D3 (C1) | ingest stops writing `artifact` rows (except `answer`) and round transcripts; planner transcripts are kept |
| #422 | D3 (C3) | one-time removal of mirror rows proven to duplicate `round_file`, after a `VACUUM INTO` backup |
| #420 | B (consult.log) | a consult's stderr shares its stream file |
| #421 | D11-adjacent | `SnapshotTree` returns `HEAD^{tree}` for a clean tree, with no temp index |
| #424 | D7 (narrow) | empty `.worktrees/` and `.worktrees/.verify/` are removed |
| #425, #427 | B (fork) | a fork writes its copied history straight into the database; an empty round file round-trips |
| #432 | B1 (narrow), D4 | drift goes straight into `round_file`; the land gate log is the sealable `NNN-land-gate.log` |

On the owner's machine the one-time cleanup deleted 1,217 artifact rows and 63 round transcripts
(15,127 rows). `relevo.db` went from 477 MB (the backup) to 394 MB. Most of what remains is planner
transcripts, which were kept on purpose, and round transcripts whose stored rows neither source reproduces
exactly (the pre-rename renderer, or a mix of log and stream rows).

Changed or dropped after a closer look:
- **B1 shrank.** `diff.patch` (the verify consult reads its path), `findings.md` (`relevo ask` prints its
  path) and `review-plan.md` (the planner reads it) have readers outside relevo, so they stay files.
- **D7 in full** (binding directories created only by the writers that need them) broke 356 tests,
  including the e2e tests. Only the narrow `.worktrees` prune shipped.
- **B2** (drop the rendered `builder.log`) and **dropping the `NNN-done` marker** were dropped for now.
  The rendered log is 3 MB in total, and stderr needs a file anyway. The marker is 0 bytes, and removing it
  changes the handoff protocol.

Found along the way:
- `db.RoundFileGet` returned nil for an empty blob, and `RoundFilePut` then bound it as NULL. Fixed in #427.
- A macOS-only race could let a SIGTERM-killed supervisor write its exit trailer. A follow-up adds a TERM trap.

## Appendix: trace scripts

`poll.sh` logs every file that appears, changes or vanishes under the sandbox roots:

```bash
#!/bin/bash
# snapshot poller: logs appear/change/vanish of files under the sandbox roots
S=$1; prev=$S/.prev; : > $prev; out=$S/events.log; : > $out
while [ ! -f $S/.stop ]; do
  find $S/home $S/repo $S/tmp -path '*/.git/objects' -prune -o -path '*/.git/logs' -prune -o -printf '%p\t%y\t%s\n' 2>/dev/null | sort > $S/.cur
  ph=$(cat $S/phase 2>/dev/null)
  diff $prev $S/.cur | grep '^[<>]' | sed "s|^|$(date +%T.%N | cut -c1-12) [$ph] |" >> $out
  mv $S/.cur $prev
  sleep 0.05
done
```

`drive.sh` runs the flow inside a sandboxed `HOME`/`XDG_*`/`TMPDIR`, with the fake `claude` first on `PATH`:

```bash
#!/bin/bash
S=$1
export HOME=$S/home XDG_CONFIG_HOME=$S/home/.config XDG_STATE_HOME=$S/home/.local/state TMPDIR=$S/tmp PATH=$S/bin:$PATH
unset RELEVO_PLANNER CLAUDE_ENV_FILE CLAUDECODE
mkdir -p $HOME $TMPDIR $S/repo
phase(){ echo "$1" > $S/phase; sleep 0.4; echo "=== $1" >> $S/drive.out; }
cd $S/repo && git init -q -b main && git -c user.email=a@b -c user.name=a commit -q --allow-empty -m init
git config user.email a@b; git config user.name a
$S/poll.sh $S & sleep 0.5
phase config
mkdir -p $XDG_CONFIG_HOME/relevo
echo '[{"harness":"claude","provider":"anthropic","model":"fake-e2e","roles":["builder"]}]' > $XDG_CONFIG_HOME/relevo/candidates.json
echo '{"order":{"builder":["claude/anthropic/fake-e2e"]}}' > $XDG_CONFIG_HOME/relevo/policy.json
relevo config >> $S/drive.out 2>&1
phase config-agents
relevo config agents >> $S/drive.out 2>&1
relevo config >> $S/drive.out 2>&1
phase planner-init
relevo planner init --name spike --kind claude --session spike-sess-1 >> $S/drive.out 2>&1
export RELEVO_PLANNER=spike
phase daemon-start
relevo daemon >> $S/daemon.out 2>&1 & DP=$!
sleep 2
phase status
relevo status >> $S/drive.out 2>&1
phase bind-worktree
relevo bind --worktree --name spk1 >> $S/drive.out 2>&1
phase send
printf '# Plan\n\nDo one thing.\n' > $S/plan.md
relevo send --name spk1 --file $S/plan.md >> $S/drive.out 2>&1
phase round-running
timeout 60 relevo wait --name spk1 --timeout 50s >> $S/drive.out 2>&1; echo "wait exit $?" >> $S/drive.out
phase after-wait
sleep 2
phase show
relevo show spk1 --diff >> $S/drive.out 2>&1
relevo history >> $S/drive.out 2>&1
phase send2
printf '# Plan 2\n\nAgain.\n' > $S/plan2.md
relevo send --name spk1 --file $S/plan2.md >> $S/drive.out 2>&1
phase round2-running
timeout 60 relevo wait --name spk1 --timeout 50s >> $S/drive.out 2>&1; echo "wait exit $?" >> $S/drive.out
phase done
relevo done spk1 >> $S/drive.out 2>&1
sleep 2
phase unbind
relevo unbind --done >> $S/drive.out 2>&1
sleep 2
phase daemon-stop
kill $DP; sleep 1
phase end
touch $S/.stop; sleep 0.3
```
