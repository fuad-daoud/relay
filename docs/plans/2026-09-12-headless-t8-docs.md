# Headless builders, step 8: README, CLAUDE.md, spec "Amends" (#99)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #99. **Spec:** `docs/specs/2026-09-12-headless-builders-design.md`
-- read §1, the **Amends** line at its top, §4.6, §4.8 and §7 step 8 before
starting. Steps 1-7 are on `main`; this plan changes documentation only.
**Depends on:** nothing open.

**Goal:** the README and CLAUDE.md describe headless builders as they now
work -- what `--headless` does, where the builder "appears", what `done`,
`unbind`, `answer`, `status` and `ui` do differently, how recovery differs --
and the spec's "Amends" line is discharged.

**Architecture:** prose only. Every claim below is checked against the merged
code: the flag names in `cmd/relay/main.go`, the refusal messages in
`internal/relay/bind.go` and `answer.go`, the status line format in
`internal/relay/status.go`, and the kill sites in `status.go` (`Done`),
`bind.go` (`Unbind`) and `switch.go`. Where the README states a rule that
headless changes ("relay never kills a process"), the rule is amended, not
contradicted elsewhere.

**Tech stack:** Markdown. Verification is `make check` (docs cannot break it,
but it confirms nothing else moved) plus a read-through against the code.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-t8` | **the git worktree. Every edit goes here.** It is your shell's cwd. Branch `relay/headless-t8`, cut from `main`. |
| `~/.local/state/relay/headless-t8` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. In particular: if a flag,
message or format quoted below does not match what the code on `main` does,
stop and report the discrepancy rather than documenting either version.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself.

## Global constraints

- Files touched: `README.md`, `CLAUDE.md`,
  `docs/specs/2026-09-12-headless-builders-design.md`. No Go file changes.
- Pane behaviour text stays as it is; headless is added beside it, never
  replacing it (spec §1: both shapes stay).
- One commit per task, on the worktree's branch.

---

### Task 1: README

**Files:**
- Modify: `README.md` (Command surface; Panes are yours, always; Where the builder appears; Running several builders at once; Recovering a broken binding)

- [ ] **Step 1: Verify the facts you are about to write**

Run and read; do not skip:

```bash
grep -n '"headless"' cmd/relay/main.go
grep -n 'ErrHeadlessAdopt\|ErrHeadlessResume\|ErrHeadlessNoDialog\|ErrBuilderBusy\|ErrStopFailed' internal/relay/*.go | grep 'errors.New'
grep -n 'builder  %-14s\|"headless"\|  log      ' internal/relay/status.go
grep -n 'stopProcess' internal/relay/status.go internal/relay/bind.go
```

Expected: `--headless` on `bind`, `add`, `fork`; the five sentinels; a
status line that prints `headless` in the pane column and `log` rows; and
`stopProcess` called from `Done` and `Unbind`. If any is missing, stop.

- [ ] **Step 2: Command surface**

In `README.md`, under `## Command surface`:

Change the `relay bind` bullet's first line from

```
- `relay bind [--name N] [--builder CANDIDATE|PANE_ID] [--resume [--rebind]] [--timeout D]`
```

to

```
- `relay bind [--name N] [--builder CANDIDATE|PANE_ID] [--headless] [--resume [--rebind]] [--timeout D]`
```

and append to that bullet, after "`relay unbind N` is the other way out.":

```
  `--headless` makes the builder a process relay runs itself, one fresh
  process per round, instead of a pane it watches (see "Headless builders"
  below). It cannot adopt a pane and cannot be added to an existing binding
  with `--resume`: a binding's shape is fixed when it is created.
```

Change the `relay send` bullet to:

```
- `relay send [NAME|--name N] --file PATH` — stage the file as the current round's
  plan and hand it to the builder: typed into its pane, or, for a headless
  binding, as the prompt of a fresh process started in the binding's tree.
  A headless binding whose previous round's process is still running refuses
  the send; wait for its report or `relay done` it.
```

In the `relay answer` bullet, append after "that is `herdr agent send-keys`
..." (the end of the bullet):

```
  A headless builder takes no dialogs; `answer` is refused and points at the
  round's log.
```

In the `relay add` and `relay fork` bullets, add `[--headless]` after
`[--builder CANDIDATE]` in each signature, and append to `relay add`'s
bullet: "`--headless` applies as for `bind`."

- [ ] **Step 3: "Panes are yours, always" and "Where the builder appears"**

Replace the `### Panes are yours, always` section's first sentence

```
relay never opens, closes or kills a pane except the one builder pane it spawns
for you at `bind`. In particular:
```

with

```
relay never opens, closes or kills a pane except the one builder pane it spawns
for you at `bind`. The one process it stops is a *headless* builder it started
itself (below). For pane builders:
```

Replace the `### Where the builder appears` section body with:

```
Every agent relay spawns as a pane -- builders from `bind`, `add`, `fork` and
consults from `ask` -- opens in its own herdr tab in the planner's workspace,
labelled with the agent's name, without moving focus. There is no split option:
side-by-side panes stop being readable at two or three builders, and tabs scale.

### Headless builders

`relay bind --headless` (also `add --headless`, `fork --headless`) makes the
builder a process instead of a pane. Nothing is opened at bind. Each
`relay send` starts the harness's non-interactive form -- `agy -p …`,
`claude -p …`, `opencode run …` -- in the binding's tree with the same prompt a
pane builder would be typed, appends its stdout and stderr to
`~/.local/state/relay/<name>/NNN-builder.log` beside the round's plan and
report, and returns. The process exits when it has written the report, or when
it fails; between rounds a headless binding has no process and is idle, not
broken. The report file is the whole contract: a process that wrote its report
and then exited non-zero has done its job.

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
  log's last 20 lines) and the daemon switches builders, up to `max_switches`,
  exactly as a vanished pane does; then `NEEDS YOU`.
- **`done` and `unbind` stop the process** if a round is running. A stop that
  fails is reported, and the binding is still done or unbound. The round budget
  never kills anything, for headless as for panes: it flags `NEEDS YOU` and
  leaves the process alone.
- **`relay unavailable`** on the provider mid-round kills the running process
  and starts the next candidate on the same round.

Rate limits are still yours to declare: relay shows the log, it never reads it
for meaning.
```

- [ ] **Step 4: "Running several builders at once"**

Append to the end of the `### Running several builders at once` section
(before `### Forking a binding`):

```
Headless builders are the cheap way to run several: no tab per builder, no
idle harness holding memory. `relay add --name api --headless` gives a peer its
own worktree and no pane.
```

- [ ] **Step 5: "Recovering a broken binding"**

Append to the end of `## Recovering a broken binding` (before
`## Platform support`):

```
A headless binding is never `BROKEN` for lack of a process: between rounds
there is none. If its process died mid-round the daemon already switched or
halted it (see "Headless builders"). To move a headless binding to a fresh
process by hand, `relay send` the staged plan again once `relay status` shows
the builder `exited`; `--resume --headless` is refused, so changing a pane
binding into a headless one is `relay unbind` and a fresh `relay bind --headless`.
```

- [ ] **Step 6: Read it back, verify and commit**

Read the changed sections once against the code you grepped in Step 1.
Every flag, message and format must match.

Run: `make check`
Expected: green.

```bash
git add README.md
git commit -m "docs(readme): headless builders -- command surface, shape, status, recovery (#99 step 8)"
```

---

### Task 2: CLAUDE.md and the spec's Amends line

**Files:**
- Modify: `CLAUDE.md` ("Working with builders")
- Modify: `docs/specs/2026-09-12-headless-builders-design.md` (the **Amends** line)

- [ ] **Step 1: CLAUDE.md**

In `CLAUDE.md`, `## Working with builders`, replace the bullet beginning
"relay closes a pane in exactly two places" with:

```
- relay closes a pane in exactly two places: `relay reap` (a terminal
  consult pane it spawned) and a mid-round builder switch (the replaced
  builder's pane, when it is still open). It stops a *process* in exactly
  three: `relay done` and `relay unbind` on a headless binding whose
  round is running, and a mid-round switch of a headless builder whose
  provider you gated with `relay unavailable`. After an `unbind`, a
  mis-bind, or any `--assume-dead` rebind of a pane builder, close the
  orphaned builder pane yourself with `herdr pane close <id>` or it holds
  memory indefinitely (an idle opencode builder is roughly 800 MB).
```

Add a new bullet after it:

```
- Prefer `--headless` on `add` for peers nobody will watch: no tab, no idle
  harness in memory, and the round log is at
  `~/.local/state/relay/<name>/NNN-builder.log`. A headless builder takes
  no dialogs (`relay answer` is refused) and has no memory across rounds,
  so its plans must be round-complete -- which relay plans already are.
```

- [ ] **Step 2: Discharge the spec's Amends line**

In `docs/specs/2026-09-12-headless-builders-design.md`, change the line

```
**Amends:** CLAUDE.md "Working with builders" (relay now stops one more kind of
builder: a headless process it started, on `done`/`unbind`); README "Where the
builder appears", "Command surface", "Recovery"
```

to

```
**Amends (applied at step 8):** CLAUDE.md "Working with builders" (relay now
stops one more kind of builder: a headless process it started, on
`done`/`unbind`); README "Command surface", "Panes are yours, always", "Where
the builder appears" (+ new "Headless builders"), "Running several builders at
once", "Recovering a broken binding"
```

- [ ] **Step 3: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add CLAUDE.md docs/specs/2026-09-12-headless-builders-design.md
git commit -m "docs: headless builders in CLAUDE.md; spec Amends applied (#99 step 8)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), and `git diff --stat main..HEAD`. Quote the Step 1
grep output from Task 1 so the planner can see the facts you checked
against. If any documented behaviour did not match the code, say which and
stop -- do not document around it.
