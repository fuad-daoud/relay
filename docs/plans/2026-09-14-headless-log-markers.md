# Headless log markers: relay says in the log when it ends or replaces a builder (#153)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-14-headless-log-markers-design.md`. Section
numbers below (§) refer to it.
**Issue:** #153. Closes it.
**Depends on:** nothing open.

**Goal:** Every time relay itself ends or replaces a headless builder's
process -- a mid-round switch, `relay done`, `relay unbind` -- it appends one
line to that round's `NNN-builder.log` saying so, so the file reads as the
round's record and a human can tell one builder's output from the next.

**Architecture:** One helper, `appendLogMarker(path, now, text)`, that never
returns an error. `stopProcess` gains a `why` argument and writes
`stopped: <why>` after a successful kill; its two callers pass `"done"` and
`"unbind"`. `switchBuilder` writes `switched to <token> (<reason>)` after
the ledger's switch entry and before starting the replacement, headless
only. `proc` is unchanged except a comment; nothing parses the marker.

**Tech stack:** Go 1.22. Verification is the `make check` constituent set
(runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/log-markers` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/log-markers`, cut from `main`. |
| `~/.local/state/relay/log-markers` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself. **No test is added under `cmd/relay`** (CI
runners have no `herdr`; CLAUDE.md). `internal/proc`'s tests start real
processes; this plan does not add one there.

## Global constraints

- `appendLogMarker` has no return value and never panics. Failure is a
  `slog.Warn`, nothing else (§4.1, §6).
- The marker line is exactly `"--- relay " + HH:MM:SS + ": " + text + " ---\n"`
  with `HH:MM:SS` from `now.Local().Format("15:04:05")` (§3.1). No other
  format; no prefix that begins with `relay-exit:`.
- Marker texts are exactly: `switched to <token> (<reason>)`, `stopped: done`,
  `stopped: unbind` (§3.1).
- No marker is written for a pane builder, for `LogPath == ""`, or after a
  failed kill.
- `internal/proc/proc.go` changes by comment only. `ExitCode`, `logTail`,
  ledger entries, `Kill` are not modified.
- One commit per task, on the worktree's branch.

---

### Task 1: `appendLogMarker`

**Files:**
- Modify: `internal/relay/headless.go` (new function; place directly before `stopProcess` at line ~300)
- Modify: `internal/relay/headless_test.go` (new test at the end)

**Interfaces:**
- Produces: `func appendLogMarker(path string, now time.Time, text string)` (§4.1, §5).

- [ ] **Step 1: Write `TestAppendLogMarker`**, three subtests using `t.TempDir()`:

- `appends in order`: write `"first line\n"` to `p`, call
  `appendLogMarker(p, t1, "stopped: done")` then
  `appendLogMarker(p, t2, "switched to x/y/z (why)")`; the file's lines are
  exactly `first line`, `--- relay <t1 15:04:05>: stopped: done ---`,
  `--- relay <t2 15:04:05>: switched to x/y/z (why) ---`. Use fixed
  `time.Date(...)` values so the `HH:MM:SS` text is known (format them
  with `.Local().Format("15:04:05")` in the test too, so the test does not
  depend on the machine's zone).
- `creates the file`: `p` does not exist; after one call it exists with one
  line.
- `empty path is a no-op` and `unwritable path does not panic`: call with
  `""`, then with a path that is an existing directory (`t.TempDir()`
  itself); no panic, and the directory is still a directory.

- [ ] **Step 2: Run to verify it fails** -- compile error, undefined.

- [ ] **Step 3: Implement** per §5: `os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)`,
one `WriteString` of the whole line, close; any error ->
`slog.Warn("builder log marker", "path", path, "err", err)` and return.

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay -run TestAppendLogMarker`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/headless.go internal/relay/headless_test.go
git commit -m "feat(relay): appendLogMarker, one relay line in a builder log that never fails the caller (#153)"
```

---

### Task 2: `stopProcess(why)` for done and unbind

**Files:**
- Modify: `internal/relay/headless.go:300` (`stopProcess` signature and body)
- Modify: `internal/relay/status.go:543` (`Done`: `stopProcess(ctx, rt, b.Builder, "done")`)
- Modify: `internal/relay/bind.go:556` (`Unbind`: `stopProcess(ctx, rt, b.Builder, "unbind")`)
- Modify: `internal/relay/headless_test.go`

**Interfaces:**
- Modifies: `func stopProcess(ctx context.Context, rt Runtime, e store.Endpoint, why string) (int, error)` (§4.2):
  after `rt.Runner.Kill` returns nil, `appendLogMarker(e.LogPath, rt.Now(), "stopped: "+why)`.

- [ ] **Step 1: Write the tests**

- `TestDoneHeadlessMarksTheLog`: as `TestDoneHeadlessStopsTheLiveProcess`
  (line 786) but capture `logPath := b.Builder.LogPath` **before** `Done`
  (Done clears it on the endpoint), then after `Done` read `logPath`; its
  last line is `--- relay <HH:MM:SS>: stopped: done ---` where `HH:MM:SS`
  is `rt.Now().Local().Format("15:04:05")` (the test runtime's `Now` is the
  fixed `baseTime`). The fake runner writes nothing to the log, so the file
  may not exist before `Done`; that is fine -- the helper creates it.
- `TestStopProcessMarksUnbind`: call `stopProcess` directly on a
  `sentHeadless` binding's `Builder` with `why: "unbind"`; the log's last
  line is `--- relay …: stopped: unbind ---`. (`Unbind` itself deletes or
  archives the directory, so the end-to-end file is gone by the time it
  returns; the call site is verified by reading `bind.go`, the behaviour by
  this test.)
- `TestStopProcessKillFailureWritesNoMarker`: `fr.killErr = errors.New("boom")`;
  `stopProcess(..., "done")` returns the error and the log file does not
  exist (or, if `sentHeadless` created it, contains no `--- relay` line).
- `TestStopProcessIdleWritesNoMarker`: endpoint with `PID == 0` (use
  `seedHeadless`, line 23, which binds without sending); returns `0, nil`;
  no log file.

- [ ] **Step 2: Run to verify they fail** -- compile error (`stopProcess` takes three arguments).

- [ ] **Step 3: Change the signature, add the marker call, update the two callers.**

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay -run 'TestDone|TestUnbind|TestStopProcess'`. Expected: PASS, existing done/unbind tests unedited.

- [ ] **Step 5: Mutation check.** Remove the `appendLogMarker` call from
`stopProcess`. Run `go test -count=1 ./internal/relay -run TestDoneHeadlessMarksTheLog`.
Expected: FAIL. Restore; PASS. Say so in your report.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/headless.go internal/relay/status.go internal/relay/bind.go internal/relay/headless_test.go
git commit -m "feat(relay): done and unbind leave a stopped marker in the headless log (#153)"
```

---

### Task 3: the switch marker

**Files:**
- Modify: `internal/relay/switch.go:134-136` (after the `switchEntry` append succeeds)
- Modify: `internal/relay/headless_test.go`

**Interfaces:**
- Modifies `switchBuilder` (§4.3): after `tx.AppendLog(b.Name, switchEntry(...))` returns nil and before `composePrompt`:

  ```
  if b.Builder.Headless() {
      appendLogMarker(rt.Store.BuilderLogPath(b.Name, b.Round), now, "switched to "+res.Token()+" ("+reason+")")
  }
  ```

- [ ] **Step 1: Write the tests**

- `TestSwitchBuilderHeadlessMarksTheLog`: as
  `TestSwitchBuilderHeadlessCloseOldKillsTheProcess` (line 407); before the
  switch, assert `rt.Store.BuilderLogPath("webshop", b.Round)` has no
  `--- relay` line (or does not exist); after `switchHeadless(t, rt, b,
  "rate-limited", true)` succeeds, the file's last line is
  `--- relay <HH:MM:SS>: switched to <testClaudeRef> (rate-limited) ---`
  (the policy in that test orders `testClaudeRef` first, so the switch picks
  it; assert with `strings.HasSuffix` on `": switched to "+testClaudeRef+" (rate-limited) ---"`).
- `TestSwitchBuilderPaneWritesNoLog`: a pane switch (use whatever helper
  `TestGoneSwitchesAfterGrace` at `switch_test.go:109` uses to reach
  `switchBuilder` for a pane binding); afterwards
  `rt.Store.BuilderLogPath(name, round)` does not exist.
- `TestSwitchBuilderHeadlessMarkerSurvivesStartFailure`: as
  `TestSwitchBuilderHeadlessStartFailureHalts` (line 425); after the halt,
  the marker line is present in the log all the same (§4.3, last
  paragraph).

- [ ] **Step 2: Run to verify they fail.**

- [ ] **Step 3: Add the call.**

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay -run 'TestSwitch|TestGone|TestGated'`. Expected: PASS, existing switch tests unedited.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/switch.go internal/relay/headless_test.go
git commit -m "feat(relay): a mid-round switch marks the headless log before the replacement starts (#153)"
```

---

### Task 4: comment and README

**Files:**
- Modify: `internal/proc/proc.go:43-44` (the `supervisorScript` comment)
- Modify: `README.md:301-314` ("Headless builders")

- [ ] **Step 1: `proc.go`.** Replace the sentence ending "still leaves a
trailer." with:
"The builder stays a child of this sh (no exec) so an OOM kill of the
builder still leaves a trailer. A group kill from Kill takes the sh with
it and leaves none; relay writes its own marker line for every process it
stops (relay.appendLogMarker)."

- [ ] **Step 2: README.** After the sentence ending "and returns." in the
"Headless builders" paragraph, add:
"When relay itself stops or replaces that process -- a mid-round switch,
`relay done`, `relay unbind` -- it appends one line to the same log saying
so (`--- relay 23:13:51: switched to claude/anthropic/sonnet (rate-limited …) ---`),
so two builders' output in one round is never ambiguous."

- [ ] **Step 3: Full constituent set.** Expected: green.

- [ ] **Step 4: Commit**

```bash
git add internal/proc/proc.go README.md
git commit -m "docs: headless log markers; the trailer survives an OOM, not a group kill (#153)"
```

---

## Report

Your report is `NNN-report.md` in the drop directory, then the empty
`NNN-done`. It lists: each task's commit hash; the Task 2 mutation check
and which test failed; the final constituent-set output; and any step
where you stopped, with the reason.
