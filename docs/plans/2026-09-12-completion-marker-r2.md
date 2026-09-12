# Completion marker, round 2: seven test setups, then Tasks 5-7 (#114 step 4)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-12-completion-marker-design.md`.
**Round 1 plan:** `docs/plans/2026-09-12-completion-marker.md` — in your
worktree. Tasks 6 and 7 of this round are executed from that file, as written.
**Issue:** #114, recommended step 4.

## Where you are

The worktree already holds round 1's work:

```
8f839f0 feat(relay): pane path closes the round on the marker every tick (#114)
4ebea02 feat(relay): closeOnMarker closes an open round on NNN-done (#114)
54f4d93 feat(relay): builder and nudge prompts name the completion marker (#114)
d342df6 feat(store): DonePath names the round's completion marker (#114)
```

plus **uncommitted** edits to `internal/relay/reconcile.go` and
`internal/relay/reconcile_test.go`: round 1's Task 5 Steps 1–4, complete and
correct. Do not revert them. Round 1 halted at Task 5 Step 5 because seven
existing tests use "report on disk + idle builder" as the *setup* for a closed
round; after Task 5 that setup no longer closes on the first tick. Those tests
exercise what happens **after** a close (daemon persistence, hook emission,
recovery, diff capture, tree ids), not how the close happens, so each gets the
marker added to its setup. That is Task A below. Tasks 5, 6 and 7 then run to
the end.

The `roundFile` guard round 1 added in Task 1 (`ext != ""`) is accepted; leave
it.

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/completion-marker` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/completion-marker`. |
| `~/.local/state/relay/completion-marker` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself.

## Global constraints

Round 1's apply unchanged: relay only ever `os.Stat`s `NNN-done`; the report
`Note` vocabulary is exactly `""`, `scraped`, `unmarked`, `noreport`; prompt
texts are the spec's verbatim; `queueReport`, `CaptureRoundDiff`,
`CaptureDrift`, `deliverAndSettle`, `switchBuilder`, `builderQuiescent`,
`scrapeLines`, `startGrace`, `nudgeGrace` are not modified; no new binding
`State`; one commit per task.

Plus, for Task A: **only the setup line changes.** Every assertion in the
seven tests stays exactly as it is. If an assertion still fails after the
marker is added, stop and report it — that is a real regression, not a setup
problem.

---

### Task A: the seven post-close tests write the marker in their setup

**Files:**
- Modify: `internal/relay/daemon_test.go` (2 tests), `internal/relay/headless_test.go` (1), `internal/relay/reconcile_hooks_test.go` (1), `internal/relay/reconcile_test.go` (3, one with three subtests)

**Interfaces:**
- Consumes: `touch(t *testing.T, path string)` — already in `reconcile_test.go` (round 1, Task 3); `Store.DonePath`.
- Produces: nothing new.

- [ ] **Step 1: Confirm the starting point**

Run: `git status --short && go test -count=1 ./internal/relay 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'`
Expected: `M internal/relay/reconcile.go`, `M internal/relay/reconcile_test.go`, and exactly these failures:
`TestTickReconcilesAndPersists`, `TestTickContinuesPastFailingBinding`,
`TestReconcilePanePathUntouchedByHeadless`, `TestReconcile_EmitsRoundStartedOnReport`,
`TestReconcileRecoveredBindingProceedsNormally`, `TestReconcileDiffCapture`,
`TestQueueReport_RoundClosedTree` (three subtests). Anything else failing: stop and report.

- [ ] **Step 2: Add the marker after each report write**

In each location below, the setup writes the report with `os.WriteFile(... ReportPath(...) ...)`. Directly **after** that `if err := os.WriteFile(...) { ... }` block, add one line that creates the marker for the same binding and round. The seven edits:

1. `internal/relay/daemon_test.go`, `TestTickReconcilesAndPersists` (report write near line 21, binding `webshop`, round 1):
   ```go
   	touch(t, rt.Store.DonePath("webshop", 1))
   ```
2. `internal/relay/daemon_test.go`, `TestTickContinuesPastFailingBinding` (report write near line 131, binding **`kobe`**, round 1):
   ```go
   	touch(t, rt.Store.DonePath("kobe", 1))
   ```
3. `internal/relay/headless_test.go`, `TestReconcilePanePathUntouchedByHeadless` (report write near line 802):
   ```go
   	touch(t, rt.Store.DonePath("webshop", 1))
   ```
4. `internal/relay/reconcile_hooks_test.go`, `TestReconcile_EmitsRoundStartedOnReport` (report write near line 80; the report path is in a local `reportPath`, and the binding/round are `webshop`/1):
   ```go
   	touch(t, rt.Store.DonePath("webshop", 1))
   ```
   If `rt` is not the runtime variable's name in that test, use whatever the test calls its `Runtime`.
5. `internal/relay/reconcile_test.go`, `TestReconcileRecoveredBindingProceedsNormally` (report write near line 615):
   ```go
   	touch(t, rt.Store.DonePath("webshop", 1))
   ```
6. `internal/relay/reconcile_test.go`, `TestReconcileDiffCapture` (report write into `reportFile` near line 703):
   ```go
   	touch(t, rt.Store.DonePath("webshop", 1))
   ```
7. `internal/relay/reconcile_test.go`, `TestQueueReport_RoundClosedTree`, all three subtests (`ordinary_close` near line 987, `retry_path` near 1032, `non-git_tree` near 1060). Each writes `reportFile := rt.Store.ReportPath(b.Name, b.Round)`; after each write block add:
   ```go
   		touch(t, rt.Store.DonePath(b.Name, b.Round))
   ```

Do not touch any assertion. Do not add the marker to any other test.

- [ ] **Step 3: Run the package tests**

Run: `go vet ./internal/relay && go test -count=1 ./internal/relay`
Expected: PASS — every test in the package, including round 1's Task 5 tests
(`TestReconcileReportWithoutMarkerIsNotAClose`,
`TestReconcileQuiescentWithReportClosesUnmarked`,
`TestReconcileQuiescentWithoutReportStillScrapes`). If one of the seven still
fails, stop and report which assertion.

- [ ] **Step 4: Commit — this is round 1's Task 5 Step 6 plus the setups**

```bash
git add internal/relay/reconcile.go internal/relay/reconcile_test.go internal/relay/daemon_test.go internal/relay/headless_test.go internal/relay/reconcile_hooks_test.go
git commit -m "feat(relay): idle fallback closes unmarked with the report, scrapes without (#114)

Seven post-close tests write the marker in their setup: they pin what
happens after a round closes, and a report alone no longer closes one."
```

---

### Task 6: Headless path

Execute **Task 6 of `docs/plans/2026-09-12-completion-marker.md`** exactly as
written there, Steps 1–6. It is self-contained: three new tests appended to
`internal/relay/headless_test.go`, the gate swap and exit case in
`internal/relay/headless.go`, two existing tests updated, one commit.

One correction to its Step 4: `TestReconcileHeadlessReportFinishesTheRoundAndClearsTheHandle`
is deleted as that step says, **and** `TestReconcilePanePathUntouchedByHeadless`
(which Task A above already gave a marker) is left alone.

- [ ] Task 6 Steps 1–6 done; commit
  `feat(relay): headless round closes on the marker; exit with a report closes unmarked (#114)` exists.

---

### Task 7: Docs

Execute **Task 7 of `docs/plans/2026-09-12-completion-marker.md`** exactly as
written there, Steps 1–4: README Quick start and Headless builders, the two
`docs/design.md` handoff blocks, the spec's `**Status:**` line, then
`make check` and the commit
`docs: the round closes on the builder's completion marker (#114)`.

- [ ] Task 7 Steps 1–4 done; `make check` green.

---

## Report

Write `NNN-report.md` with: the commit list (`git log --oneline main..HEAD`),
`git diff --stat main..HEAD`, the `make check` output tail, and anything you
stopped on. Then create the empty `NNN-done` file the round prompt names, and
reply with only the report path.
