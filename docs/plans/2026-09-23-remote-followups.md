# Plan: builders report git surgery, and a changed_paths mismatch reaches the planner (#216)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. In
particular, halt and report if any of these turns out to be false:

- **The server computes the paths clause.** A served binding's round closes
  through the same `queueReport` (`internal/relay/reconcile.go` ~343), with
  the report tail parsed from the server's copy of the report. So the server's
  `KindDiff` entry, and therefore `BindingView.DiffNote`
  (`relay.ServedView`, `internal/relay/served.go`), carries the
  `paths: report N, diff M` clause when the counts differ. If the served close
  does not run that code with a parsed tail, the remote half of item B has
  nothing to read, and the design is wrong.
- **`parseListValue("[]")` returns nil** (`internal/relay/reporttail.go` ~232).
  So `changed_paths: []`, the exact #208 case, leaves `tail.ChangedPaths == nil`,
  and today's check (`tail.ChangedPaths != nil`, `reconcile.go` ~407) skips
  it. If a `[]` value in fact produces a non-nil slice, item B's first half is
  already done. Say so and do only the payload half.
- **No test requires the four `plan-executor.*` bodies to be byte-identical.**
  They are not: agy's names no researcher (#191). If such a test exists, keep
  all kinds identical under it.

Item A of the brief, a client-facing per-owner usage view over the wire, is
**not in this plan**. The client already has that data: `catchUp`
(`internal/relay/remote.go`) records the server's `view.Usage` and
`view.Rusage` on the client's own report entry through `queueReport`, and
`relay tab` sums report entries, archives included. A remote round's spend
therefore already shows in plain `relay tab` once it is collected. A wire view
would differ only for a round not yet collected, which is a different thing.
The planner recommends closing that bullet on #216.

`--seed-from` is out of scope. It needs a trust decision from the owner.

This round has two independent items. Do them in order, one commit each:
**C** (definitions), then **B** (the paths cross-check).

## 1. System Overview

**C: report git surgery.** In #208's round, a builder rebased its branch onto
`origin/main` and did not say so. Nothing in the `plan-executor` definition
asks a builder to report what it did to its own branch's history. This item
adds one required line to the definition's report format, in all four kinds.
It is prose only: the trailing `relay` block gets no new key, because nothing
would consume one yet.

**B: a changed_paths mismatch reaches the planner.** #198 added a
cross-check: when the report's `changed_paths` count differs from the diff's
file count, the round's `KindDiff` log entry gets a note
`paths: report N, diff M`. Two gaps kept #208's `changed_paths: []` against a
24-file diff invisible:

1. `[]` parses to nil, and the check requires non-nil, so **an empty list is
   never compared**. That is exactly the case of a builder that leaves the
   skeleton's default.
2. The note lives only on the log entry. The planner's payload shows the
   `Diff:` line. Locally that line comes from `DiffLine`, which never sees the
   note. Remotely it comes from `DiffLineFromNote`, which cuts the note at its
   first `"; "`, dropping the clause. So **the planner is never told**.

The fix:
- Record whether the `changed_paths` key was present, even when empty, and
  compare whenever it was.
- Add one payload line when the counts differ, both locally and in `catchUp`
  (built from the note the server sends).

## 2. File Structure

```
internal/harness/agents/plan-executor.claude.md   MODIFY  one report bullet (C)
internal/harness/agents/plan-executor.opencode.md MODIFY  same bullet (C)
internal/harness/agents/plan-executor.agy.md      MODIFY  same bullet (C)
internal/harness/agents/plan-executor.codex.toml  MODIFY  same bullet, inside the ''' string (C)
internal/harness/harness_test.go                  MODIFY  one test: every kind carries the bullet (C)
internal/relay/reporttail.go                      MODIFY  ReportTail.ChangedPathsSet; set on the key (B)
internal/relay/reporttail_test.go                 MODIFY  presence rows (B)
internal/relay/capture.go                         MODIFY  PathsLine, PathsLineFromNote (B)
internal/relay/capture_test.go                    MODIFY  tables for both (B)
internal/relay/reconcile.go                       MODIFY  the check uses ChangedPathsSet; payload gains the line (B)
internal/relay/reconcile_test.go                  MODIFY  empty-list row; payload line (B)
internal/relay/remote.go                          MODIFY  catchUp appends PathsLineFromNote (B)
internal/relay/remote_test.go                     MODIFY  catchUp payload rows (B)
```

## 3. Data Structures & Exact Strings

### `ReportTail` (`internal/relay/reporttail.go`)

Add `ChangedPathsSet bool`, doc comment:
`// the changed_paths key was present, even with an empty list (#216)`.
`parseReportTail` sets it to true when it meets the `changed_paths` key,
whether the value is inline (`[]`, `[a, b]`, a scalar) or a block list with
zero or more items. `ChangedPaths` itself keeps today's values: nil for an
empty list.

### Strings

| Where | Text |
|---|---|
| C: report bullet, placed right after `- Files created/modified, mapped to the steps that produced them.` in all four files | `- Git surgery on your branch: every rebase, reset, amend, cherry-pick, merge, force-push or branch switch you ran, each with its command and why -- or "none". Report it even when the plan asked for it.` |
| B: KindDiff note clause (unchanged) | `paths: report %d, diff %d` |
| B: `PathsLine(report, diff int) string` | `"Paths: the report's changed_paths lists %d, the diff has %s -- check the diff, not the list"`, with `report`, `formatFiles(diff)` |
| B: `pathsClauseRe` | `` `paths: report (\d+), diff (\d+)` `` |

Use `--`, not an em dash, in the bullet, so the text is identical in all four
files. Codex's body is a TOML `'''` literal string, so the text needs no
escaping there.

## 4. Interface Definitions & Component Contracts

- `PathsLine(report, diff int) string` (pure, `capture.go`): the string in the
  table.
- `PathsLineFromNote(note string) string` (pure, `capture.go`): finds
  `pathsClauseRe` anywhere in `note`. On a match it returns
  `PathsLine(n, m)`; otherwise `""`. It must still find the clause after
  `joinNotes` has put it after the commit clause (joined with a space, e.g.
  `24 files, +1 -2; 1 commit on relay/x, tree clean paths: report 0, diff 24`).
- `queueReport` (`reconcile.go` ~407): the condition becomes
  `result.Available && ok && tail.ChangedPathsSet && len(tail.ChangedPaths) != result.Stat.FilesChanged`.
  When it holds, the note gets its clause as today, **and** the payload gets
  `"\n" + PathsLine(len(tail.ChangedPaths), result.Stat.FilesChanged)`
  appended right after the `Diff:` line. That is inside the same
  `!HasEntry(... KindDiff)` block, so it is added exactly once.
- `catchUp` (`remote.go`): right after the existing
  `DiffLineFromNote(view.DiffNote, …)` append, if
  `line := PathsLineFromNote(view.DiffNote); line != ""`, append
  `"\n" + line`. Nothing else in `catchUp` changes.

## 5. High-Level Pseudocode

```
parseReportTail: on key "changed_paths" → tail.ChangedPathsSet = true; (list handling unchanged)

queueReport (no diff entry yet):
    result, facts = CaptureRoundDiff, CommitFacts
    note = DiffSummary(result, facts)
    mismatch = result.Available && ok && tail.ChangedPathsSet && len(tail.ChangedPaths) != FilesChanged
    if mismatch: note = joinNotes(note, "paths: report N, diff M")
    append KindDiff entry(note)
    payload += "\n" + DiffLine(...)              (unchanged, when non-empty)
    if mismatch: payload += "\n" + PathsLine(N, M)

catchUp:
    payload += "\n" + DiffLineFromNote(view.DiffNote, …)   (unchanged)
    payload += "\n" + PathsLineFromNote(view.DiffNote)     (new, only when non-empty)
```

A report with **no** `changed_paths` key at all (unstructured, or an older
builder) is never compared, as today. Only a key that is present counts.

## 6. Error Handling

- Nothing can fail: both new functions are pure, and a note without the clause
  yields `""`.
- No new log lines.
- The note's format is unchanged, so every existing reader of `KindDiff`
  notes, including `relay show` and the existing
  `paths: report 2, diff 3` test, sees the same text.

## 7. Ordered Implementation Steps

Run `make check` after each step. Also run
`gofmt -l $(git ls-files '*.go')`, which must print nothing. All tests go in
`internal/harness` and `internal/relay`. **Add no test in `cmd/relay`.** CI
has no harness binary and no network, and none of this needs `cmd/relay`.

1. **C: the bullet.** Add the bullet from §3 to all four `plan-executor.*`
   files, in the same position. Add
   `TestPlanExecutorReportsGitSurgeryOnEveryKind` to `harness_test.go`. For
   every `h` in `All()`, `AgentDoc("plan-executor", h.Kind)` must contain
   `Git surgery on your branch:`.
   *Verify:* `make check` passes, and the existing
   `TestPlanExecutorDispatchesResearcherOnEveryKind` still passes.
   *Mutation:* remove the bullet from `plan-executor.agy.md` only. The new test
   must fail naming `agy`. Restore it.
   Commit: `docs(agents): a builder reports any git surgery on its branch (#216)`.

2. **B: presence.** Add `ChangedPathsSet` and set it in `parseReportTail`.
   *Verify:* new `reporttail_test.go` rows:
   - `changed_paths: []` gives Set true, and `ChangedPaths` nil;
   - `changed_paths: [a, b]` gives Set true, 2 paths;
   - a block form `changed_paths:` with no items gives Set true;
   - a block form with two `- x` items gives Set true, 2 paths;
   - the key absent gives Set false.

   Existing tail tests pass unchanged.

3. **B: the two pure functions.** Add `PathsLine`, `pathsClauseRe` and
   `PathsLineFromNote` to `capture.go`.
   *Verify:* a table in `capture_test.go`:
   - `PathsLine(0, 24)` equals the exact §3 text with `24 files`;
   - `PathsLine(2, 1)` ends `1 file -- check the diff, not the list`;
   - `PathsLineFromNote` on the joined example in §4 returns `PathsLine(0, 24)`;
   - on `"3 files, +1 -1"` it returns `""`;
   - on `""` it returns `""`.

4. **B: local.** Change the `queueReport` condition and append the payload
   line.
   *Verify:* in `reconcile_test.go`, next to the existing
   `paths: report 2, diff 3` subtest:
   - A report whose tail is `changed_paths: []`, against a fake diff of 3
     files, gives a `KindDiff` note ending `paths: report 0, diff 3`, and the
     queued report entry's payload contains `PathsLine(0, 3)`.
   - The existing 2-vs-3 subtest additionally asserts the payload contains
     `PathsLine(2, 3)`.
   - A report with no `changed_paths` key gives no clause and no `Paths:`
     line.
   - The existing equal-count subtest still gives no clause, and now also
     asserts no `Paths:` line.

   *Mutation:* change the condition back to `tail.ChangedPaths != nil`. The
   `changed_paths: []` subtest must fail. Restore it.

5. **B: remote.** Append `PathsLineFromNote(view.DiffNote)` in `catchUp`.
   *Verify:* in `remote_test.go`, modelled on
   `TestCatchUpWritesDiffEntryFromView`:
   - A view whose `DiffNote` is the §4 joined example gives a queued payload
     containing `PathsLine(0, 24)`, after its `Diff:` line.
   - A `DiffNote` without the clause gives a payload with no `Paths:` line.

   *Mutation:* delete the new append. The first test must fail. Restore it.
   Commit:
   `fix(report): a changed_paths mismatch, empty list included, reaches the planner's payload (#216)`.

## Report

Include:
- the files touched per item;
- both halt conditions checked, with evidence: the file and line where the
  served close parses the tail, and what `parseListValue("[]")` returns;
- each mutation, what you changed and the failing test's name and message;
- anything you left out, under `not_done`.
