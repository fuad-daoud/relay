# E2E on real herdr, round 2: the shim must look `working` after a prompt, then Tasks 2-5 (#114 step 2)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-12-e2e-real-herdr-design.md`.
**Round 1 plan:** `docs/plans/2026-09-12-e2e-real-herdr.md` -- in your
worktree. Tasks 2 (from Step 3), 3, 4 and 5 of this round are executed from
that file, as written, with the corrections listed under Task B.
**Issue:** #114, recommended step 2.

## Where you are

The worktree holds round 1's work:

```
5c3ca8c test(e2e): detached herdr session fixture with scripted agents; smoke (#114 step 2)
```

plus **uncommitted** edits to `internal/relay/e2e_test.go`: round 1's Task 2
Steps 1-2 (`e2eRuntime`, `bindBuilder`, `reconcileAt`, `sendAt`,
`reportEntry`, and the `marker_closes_round` subtest), complete and correct.
Do not revert them.

Round 1 halted at Task 2 Step 3 because `Send` failed with
`prompt w1:p3 stalled twice: herdr prompt stalled`. The cause is in the shim,
not in relay: relay's `Prompt` runs `herdr agent prompt … --wait --until
working --until blocked --timeout 5000`, and herdr returns
`agent_prompt_stalled` unless it *observes* a `working` or `blocked` state
within 5s. The round-1 shim goes from idle straight back to idle, so herdr
never sees `working`. The planner-side delivery uses the same `Prompt`, so
the planner shim (kind `claude`) has the same problem.

Verified fix (spike, herdr 0.9.0): after reading a line, the shim prints two
lines that satisfy both detection manifests -- `⠋ Thinking` matches agy's
`spinner_working` (`^\s*[⠀-⣿]+\s+\w+ing\b`), `✻ Thinking…` matches claude's
`live_turn_working` (`^\s*[*·✢✳✶✻✽]\s+\S.*…\s*$`) -- sleeps one second, then
erases both lines (`ESC[2A ESC[J`) before echoing. With that shim,
`agent prompt` with relay's exact flags returns `working` in ~0.5s for both
kinds, and two seconds later `agent list` reports `done`. relay treats `done`
exactly as `idle` in every path that matters here (`Reconcile`'s status
switch, `Deliver`'s planner check), and the spec's cases never assert the
literal word `idle` on a builder after a prompt.

Round 1's Task 1 Step 4 mutation was mis-specified: dropping the appended
`SHELL=/bin/sh` leaves no `SHELL` at all (the loop above it strips the
inherited one), and herdr then defaults to `/bin/sh`. The correct mutation is
`SHELL=/bin/bash`. Task B re-runs it that way.

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/e2e-herdr` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/e2e-herdr`. |
| `~/.local/state/relay/e2e-herdr` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands, herdr use, global constraints

Exactly as in round 1's plan: `make check` and `go vet -tags e2e
./internal/relay` at the end of every task; `go test -tags e2e -count=1 -run
TestE2E ./internal/relay -v` (and `make e2e` once Task 5 adds it); herdr is
used only through that test; leave no `relay-e2e-*` session behind (check
`herdr session list`, clean up as round 1's plan says, and report it); no
production code changes; one commit per task.

---

### Task B: the shim flashes a `working` state, then goes quiet

**Files:**
- Modify: `internal/relay/testdata/e2e-shim.sh`
- Modify: `internal/relay/e2e_test.go` (one comment; no code)

**Interfaces:**
- Consumes: nothing new.
- Produces: a shim that satisfies `herdr agent prompt --wait --until working --until blocked --timeout 5000` for kinds `agy` and `claude`, and reads as `done` afterwards.

- [ ] **Step 1: Replace the shim body**

Overwrite `internal/relay/testdata/e2e-shim.sh` with exactly:

```sh
#!/bin/sh
# relay e2e agent shim (spec 2026-09-12-e2e-real-herdr §3.2). herdr's
# detection manifests report a known-kind process as idle by default, so this
# only has to exist, echo what it is told, and never write a file. Argv --
# --agent, --dangerously-skip-permissions, anything -- is ignored.
#
# relay prompts with `--wait --until working --until blocked`, so after each
# line the shim must be *seen* working for a moment: the two lines below match
# agy's spinner_working ("<braille> <word>ing") and claude's live_turn_working
# ("<glyph> <text>…") rules respectively. They are erased before the echo so
# herdr returns to done/idle and relay's quiescence check sees a still screen.
echo "shim ready"
while IFS= read -r line; do
  printf '\342\240\213 Thinking\n\342\234\273 Thinking\342\200\246\n'
  sleep 1
  printf '\033[2A\033[J'
  echo "you said: $line"
  echo "I implemented the guard clause but could not write the file."
done
```

The three `printf` byte sequences are, in order: `⠋ Thinking` newline
`✻ Thinking…` newline; cursor up two lines and clear to end of screen. Keep
them as octal escapes so the file has no non-ASCII bytes.

- [ ] **Step 2: Update the fixture comment**

In `internal/relay/e2e_test.go`, the doc comment on `startShim` (or the
`smoke` subtest's error message) says the shim is accepted as a known agent.
Leave the assertions alone. Add one sentence to `startShim`'s doc comment:

```
// After any prompt the shim reads as "done" (it flashes a working line, then
// erases it); relay treats done as idle, and the cases never assert "idle" on
// a prompted builder.
```

- [ ] **Step 3: Run the smoke test and the marker round together**

Run: `go vet -tags e2e ./internal/relay && go test -tags e2e -count=1 -run TestE2E ./internal/relay -v`
Expected: `smoke` and `marker_closes_round` both PASS. That is round 1's
Task 2 Step 3, now unblocked. `herdr session list` shows no `relay-e2e-*` row.

If `marker_closes_round` fails at `waitScreen(planner, "you said: Builder
finished round 1")`, read the planner's screen in the failure message: if it
shows the delivery text but the test still timed out, stop and report --
that is a timing question for the planner, not something to widen.

- [ ] **Step 4: Round 1's Task 2 Step 4 (mutation), and the corrected Task 1 mutation**

1. Comment out the `touch(...)` line in `marker_closes_round`; run; expected `round = 1, want 2`. Revert.
2. Change the appended `"SHELL=/bin/sh"` in `startSession` to `"SHELL=/bin/bash"`; run; expected: `smoke` fails or `startShim` times out, because a login bash drops the shim dir from PATH and the real agent starts (or nothing does). Revert. Record what you observed.

- [ ] **Step 5: `make check`, then commit (this is round 1's Task 2 Step 5 plus the shim)**

```bash
git add internal/relay/testdata/e2e-shim.sh internal/relay/e2e_test.go
git commit -m "test(e2e): shim flashes a working state so relay's --wait prompts land; real round closes on the marker (#114 step 2)"
```

---

### Task 3: Nudge, then `unmarked`

Execute **Task 3 of `docs/plans/2026-09-12-e2e-real-herdr.md`** exactly as
written, Steps 1-4.

One note for its Step 1: after the nudge at `startGrace+5s`, the fingerprint
`nudgeBuilder` takes may or may not include the shim's spinner lines (they
live for one real second). Either way the `startGrace+6s` tick is inside
`nudgeGrace`, and by `startGrace+6s+nudgeGrace+10s` the screen has been still
for longer than `nudgeGrace`. The assertions as written hold in both cases;
do not add sleeps.

- [ ] Task 3 Steps 1-4 done; commit
  `test(e2e): idle builder is nudged once, then closes unmarked on a still real screen (#114 step 2)` exists.

---

### Task 4: Scrape, and a moving screen resets the grace

Execute **Task 4 of `docs/plans/2026-09-12-e2e-real-herdr.md`** exactly as
written, Steps 1-4. The direct `s.herdr.Prompt(..., "keep talking")` in
`screen_movement_resets_grace` now succeeds for the same reason `Send` does.

- [ ] Task 4 Steps 1-4 done; commit
  `test(e2e): a still real screen scrapes; a moving one resets the nudge grace (#114 step 2)` exists.

---

### Task 5: `make e2e`, docs

Execute **Task 5 of `docs/plans/2026-09-12-e2e-real-herdr.md`** exactly as
written, Steps 1-4.

- [ ] Task 5 Steps 1-4 done; `make check && make e2e` green; no `relay-e2e-*` session left.

---

## Report

Write `NNN-report.md` with: the commit list (`git log --oneline main..HEAD`),
`git diff --stat main..HEAD`, the tail of `make check` and of `make e2e`
(per-subtest PASS lines and total time), the observed failure text from each
mutation check, and anything you stopped on. Confirm `herdr session list`
shows no `relay-e2e-*` session. Then create the empty `NNN-done` file the
round prompt names, and reply with only the report path.
