# Completion marker: the builder says when the round is over

**Issue:** #114, recommended step 4 ("replace the file-exists gate with a
completion marker the builder writes last"). Adopts idea 1 from the
outsourcerer review in the narrowest form: existence only.
**Depends on:** nothing open. #99 (headless builders, #111) landed; the
headless branch is one of the two gates this design changes.
**Amends:** README "The round" / "Headless builders" (the builder now writes
two files); `docs/design.md` command table ("report is the contract" becomes
"report plus marker"); #37 (triggers) should gate its "artifact appears" edge
on `NNN-done`, not `NNN-report.md`, when it is specced.

## 1. System overview

A round ends when the builder has written its report. Today relay learns that
by `os.Stat` on `NNN-report.md`, in two places: the pane path checks it once
herdr reports the builder idle (`reconcile.go`, `handleIdleBuilder`), and the
headless path checks it every tick while the process runs (`headless.go`).
Both take "the file exists" as "the builder is finished", and neither is true
often enough:

- `os.Stat` succeeds the moment the file is created. A report still being
  streamed to disk is captured half-written.
- A builder that writes its report and then keeps working (runs `make check`,
  fixes, commits) has its round closed under it. The per-round diff is
  snapshotted too early, and the later edits are recorded as *between-rounds
  drift* (`CaptureDrift`), which is the wrong label: nothing happened between
  rounds, relay closed the round early.
- In the headless path this also orphans the process: `queueReport` runs,
  `clearProcess` drops the PID, and a still-running builder is no longer
  tracked by anyone.

This design gives the builder one explicit way to say "I am finished with the
tree": it creates an empty file, `NNN-done`, as its last action. Relay closes
the round when that file exists, in both paths, on every tick, regardless of
what herdr or the process table says. The report file stays the artefact the
planner reads; the marker is only the fact that it is complete.

Everything downstream of the gate -- `queueReport`, `CaptureRoundDiff`, the
round counter, drift baselines, delivery -- is untouched. When a builder does
not write the marker, relay degrades to today's behaviour with the omission
named, never to something worse.

### Scope boundary

The marker carries no content and relay never opens it. A halt ("step 3 is
impossible as written") is still a report that says so, followed by the
marker; the planner reads the report, relay does not. `DONE`/`BLOCKED`/
`PROGRESS` words, `relay wait`, delivery receipts, and any change to how relay
detects idleness (`builderQuiescent`, `scrapeLines`, nudge timing) are out of
scope. This is the completion gate and nothing else.

## 2. File structure

No new files. Changes:

```
internal/store/store.go          DonePath(name, round)  -- NNN-done beside NNN-report.md
internal/relay/send.go           builderPrompt, composePrompt: third path (the marker)
internal/relay/switch.go         composePrompt call site: passes the marker path
internal/relay/reconcile.go      marker gate hoisted above the status switch;
                                 handleIdleBuilder loses its report gate, gains the
                                 unmarked close; nudgePrompt reworded
internal/relay/headless.go       report gate becomes the marker gate; exited-with-
                                 report-but-no-marker case added
internal/relay/*_test.go         tests in §7
README.md, docs/design.md        amends in the header
```

Per-round files in `$XDG_STATE_HOME/relay/<name>/` after this lands:

```
NNN-plan.md        planner -> builder (existing)
NNN-report.md      builder -> planner (existing)
NNN-done           builder's completion marker (new, empty)
NNN-diff.patch     relay, at round close (existing)
NNN-drift.patch    relay, at next send (existing)
NNN-builder.log    headless only (existing)
```

`ForkState` copies every `NNN-*` file by prefix (`roundOfFile`), and `gc`,
`unbind` and fork rollback `RemoveAll` the binding directory, so the marker is
forked and collected with no change to either.

## 3. Data structures and type definitions

### 3.1 `Store.DonePath`

```
func (s *Store) DonePath(name string, round int) string
    returns s.roundFile(name, round, "done", "")     -- "<dir>/<NNN>-done"
```

Same precondition as `ReportPath`: `round >= 1`.

### 3.2 Report log entry notes

`store.LogEntry.Note` on a `KindReport` entry (`DirToPlanner`) takes one of:

| value      | meaning                                                                 |
|------------|-------------------------------------------------------------------------|
| `""`       | marker present, report present: a normal close (unchanged)              |
| `scraped`  | no report, no marker; terminal scraped after quiescence (unchanged)     |
| `unmarked` | report present, no marker; closed after quiescence (pane) or exit (headless) |
| `noreport` | marker present, no report; closed on the marker                         |

`status`, `ui` and `log` already render `Note` on the report row; the two new
words appear there without renderer changes.

### 3.3 Prompts

`builderPrompt` (verbatim; `%d` round, then plan path, report path, marker path):

```
Round %d from the planner.
Read: %s
When you are done, write your report to: %s
Then, as the very last thing you do -- after every edit, test and commit --
create this empty file: %s
Reply here with only the report path.
```

`nudgePrompt` (report path, marker path):

```
You went idle without finishing.
Write your report to %s if you have not, then create the empty file %s as
your last action, and reply with only the report path.
```

`composePrompt(b, planPath, reportPath, donePath string) string` renders the
first. Both call sites (`Send`, `switchBuilder`) and the headless `-p` prompt
go through it, so a pane builder and a headless builder receive the same
instructions.

## 4. Interface definitions and component contracts

### 4.1 `closeOnMarker` (new, `reconcile.go`)

One function decides whether an open round is closed by the marker. It is the
single place both paths call so they cannot disagree.

```
func closeOnMarker(ctx, rt, tx, b, entries) (next store.Binding, closed bool, err error)
```

Preconditions: the round is open (`HasEntry(plan)` and `!HasEntry(report)`
for `b.Round`).
Postconditions:

- `DonePath` missing: `closed == false`, `b` returned unchanged, no I/O beyond
  the stat.
- `DonePath` present and `ReportPath` present: `queueReport(reportPath,
  "Builder finished round N. Report: <path>", "")`; `closed == true`.
- `DonePath` present and `ReportPath` missing: `queueReport(reportPath,
  "Builder wrote its completion marker for round N but no report at <path>.",
  "noreport")`; `closed == true`. No terminal read: the builder said it was
  done, and a scrape would be a worse artefact than an honest empty slot.
- Errors are `queueReport`'s, wrapped, and leave the round open.

Dependencies: `rt.Store` (paths), `queueReport`.

### 4.2 Pane path (`reconcileBinding`, existing)

The marker check runs before the `effectiveStatus` switch, every tick the
round is open:

```
if round open:
    next, closed, err := closeOnMarker(...)
    if err: return err
    if closed: return deliverAndSettle(next)
switch effectiveStatus(...):
    idle/done  -> handleIdleBuilder     (fallback path, §4.3)
    blocked    -> handleBlockedBuilder  (unchanged)
    default    -> checkRoundTimeout     (unchanged)
```

### 4.3 `handleIdleBuilder` (existing, contract changes)

Loses the `os.Stat(reportPath)` gate. Keeps: nothing sent yet -> return;
already handled -> return; not nudged -> `startGrace` then `nudgeBuilder`;
nudged -> `builderQuiescent`. After quiescence:

```
if ReportPath exists:
    queueReport(reportPath,
        "Builder finished round N but never confirmed completion (no NNN-done). " +
        "Report: <path>. The diff may be premature.",
        "unmarked")
else:
    scrapeReport(...)                          -- unchanged
```

The marker-present case cannot reach here: §4.2 closes it first.

### 4.4 Headless path (`reconcileHeadless`, existing, contract changes)

```
if round open:
    next, closed, err := closeOnMarker(...)
    if closed: next.Builder = clearProcess(next.Builder); deliverAndSettle(next)
if PID == 0: return                            -- unchanged
alive := Runner.Alive(...)                     -- unchanged
if alive:
    -- report present or not, no marker: keep waiting. This is the fix.
    unchanged screen-from-log / switch handling
else:
    code := Runner.ExitCode(...)
    if ReportPath exists:
        queueReport(reportPath,
            "Builder exited (code N) after writing its report but never confirmed " +
            "completion (no NNN-done). Report: <path>.",
            "unmarked")
        clearProcess; deliverAndSettle
    else:
        existing "exited without a report" branch, unchanged
```

Exit is a hard fact: a process that has exited cannot be mid-write, so the
report is trusted with the omission noted.

### 4.5 `nudgeBuilder` (existing)

Signature gains the marker path: `nudgeBuilder(ctx, rt, tx, b, reportPath,
donePath string)`. Log entry and once-per-round gating unchanged.

## 5. High-level pseudocode

Per-tick flow for one binding with an open round, both paths:

```
marker exists?
  yes -> close now (report, or noreport)            -- §4.1, every tick
  no  ->
    pane:
      status blocked  -> dialog handling (unchanged)
      status working  -> round timeout check (unchanged)
      status idle     -> not nudged: wait startGrace, nudge (both files named)
                         nudged: wait for screen quiescence
                         quiescent: report exists ? close "unmarked" : scrape
    headless:
      alive           -> wait (unchanged switch/log handling)
      exited          -> report exists ? close "unmarked" : exit-without-report
```

Round close (`queueReport`) is unchanged: capture the round diff, append the
`report` entry, `Round++`, reset `RoundStartedAt`. Because the marker is the
builder's own assertion that the tree is final, the diff captured at that
moment is the builder's intended diff, and anything the tree does afterwards is
correctly a drift.

## 6. Error handling strategy

- `os.Stat` on the marker: only `err == nil` counts as present. Any other error
  (permission, transient) is "absent this tick"; the next tick retries. This
  matches how the report stat is treated today.
- `queueReport` failures propagate as today and leave the round open; the
  marker is still on disk, so the next tick retries the close.
- No new error types. No new `State` values: an `unmarked` or `noreport` close
  is a normal close with a note, not NEEDS YOU. The planner sees the note in
  the payload and in `status`/`log` and decides.
- Logging: `slog.Info("round closed by marker", binding, round)` on the normal
  close; `slog.Warn` with the same keys plus `note` for `unmarked` and
  `noreport`.

## 7. Ordered implementation steps

Every test is in `internal/relay` (or `internal/store`) against the fake herdr
and fake runner. No CLI test: no subcommand changes, and CI runners have no
herdr. Each behaviour test names the mutation that must make it fail.

1. **`Store.DonePath`.** Test: names `<dir>/007-done` for round 7. Depends on
   nothing.
2. **Prompts.** `builderPrompt`, `nudgePrompt`, `composePrompt` with the third
   path; update `Send`, `switchBuilder` and the headless prompt. Test: the
   composed prompt contains plan, report and done paths in that order; the
   existing `composePrompt` tests in `headless_test.go` updated. Depends on 1.
3. **`closeOnMarker`.** Tests: marker + report -> report entry, note `""`,
   diff captured; marker alone -> note `noreport`, no `ReadAgent` call
   (mutation: fall through to scrape -> fails); no marker -> `closed == false`
   and no log entries. Depends on 1.
4. **Pane gate hoisted.** Wire §4.2. Test: marker present while status is
   `working` -> round closes that tick (mutation: move the check inside the
   idle branch -> fails). Depends on 3.
5. **Idle fallback.** §4.3 and §4.5. Tests: report present, no marker, idle ->
   no close on that tick, nudge after `startGrace` with both paths in the
   prompt (mutation: gate on the report -> fails); report + no marker +
   quiescent -> close with note `unmarked` and the payload warning (mutation:
   drop the note -> fails); no report + quiescent -> scraped (regression pin).
   Depends on 4.
6. **Headless gate.** §4.4. Tests: alive + report + no marker -> round open,
   PID retained (mutation: stat the report -> fails); exited + report + no
   marker -> close `unmarked`, PID cleared; exited + nothing -> existing exit
   entry (regression pin); marker present -> close, PID cleared. Depends on 3.
7. **Docs.** README and `docs/design.md` amends from the header; a note on
   #37 that its edge gate is `NNN-done`. Depends on 6.

Verification for the whole change: `make check` on zen (now with `-race`);
`git diff --stat` touches only the files in §2; then one real round on this
machine with a pane builder, confirming `relay status` shows the round close
on the marker before the pane goes idle and `relay log` shows an empty note.
