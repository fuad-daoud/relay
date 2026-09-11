# `relay status` hides DONE; `relay gc` archives by default (#56)

**Design spec:** `docs/specs/2026-09-11-status-done-gc-archive-design.md`
**Issue:** #56

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

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
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- relay makes no judgements. Nothing in this task deletes, archives, or
  hides anything except in response to a flag or the documented default.
  No automatic cleanup of any kind (spec §1).
- `relay.Status` keeps returning every binding. Filtering is a separate pure
  function applied by the CLI (spec §4.1).
- Every new exported symbol and field gets a doc comment that explains the
  rationale, in the house style already in these files.
- `bind.json` serialisation does not change. `Report`'s new field carries
  `omitempty` so JSON is byte-identical whenever nothing is hidden.
- Do not touch `internal/ui` (spec §9).
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: status filter, gc default, already-gone worktree

**Files:**
- Modify: `internal/relay/status.go`, `internal/relay/status_test.go`
- Modify: `internal/relay/gc.go`, `internal/relay/gc_test.go`
- Modify: `internal/relay/bind.go` (`worktreeTeardown`, `worktreeOutcome`, `UnbindResult`), `internal/relay/bind_test.go`
- Modify: `cmd/relay/main.go` (`cmdStatus`, `cmdWatch`, `cmdGC`, `cmdUnbind`, `cmdDone`, help text)
- Modify: `README.md`

**Interfaces produced** (spec §3, §4):
- `func HideDone(r Report) Report`
- `Report.DoneHidden int`
- `GCOptions.Delete bool` (replaces `Archive`)
- `GCResult.WorktreeGone string`, `UnbindResult.WorktreeGone string`, `worktreeOutcome.Gone string`

Steps are ordered so every step leaves the suite green.

- [ ] **Step 1: `HideDone` and the footer** (spec §4.1, §4.2)

  In `internal/relay/status.go`:
  - Add `DoneHidden int \`json:"done_hidden,omitempty"\`` to `Report` with
    a comment: the number of DONE rows `HideDone` removed; zero and absent
    whenever nothing was filtered, so a consumer that never learned the
    field sees the document it always did.
  - Add `HideDone(r Report) Report`: pure, copies `r`, keeps rows whose
    `State != string(store.StateDone)` in order, sets `DoneHidden`. Check
    how `BindingStatus.State` is populated (it is a string; compare against
    the store constant, not a literal).
  - `RenderStatus`: if no rows and `DoneHidden == 0` → `no bindings\n` as
    today. If no rows and `DoneHidden > 0` → footer only. After the rows,
    when `DoneHidden > 0`, one footer line `<n> done · relay gc to clear`
    (`1 done` singular). Comment why it says "clear" and not "free"
    (spec §7.4).

  Tests (`status_test.go`):
  - `TestHideDoneRemovesOnlyDoneRows` -- three rows (active, done, broken);
    result has two in the original order, `DoneHidden == 1`, and the input
    `Report` still has three.
  - `TestRenderStatusFooterCountsHidden` -- `DoneHidden: 3` renders the
    footer with `3 done`; `DoneHidden: 1` renders `1 done`; `DoneHidden: 0`
    renders no footer and no "done ·" substring.
  - `TestRenderStatusFooterOnlyWhenEverythingIsDone` -- no rows,
    `DoneHidden: 2`: output contains the footer and does not contain
    `no bindings`.

  Verify: `go test ./internal/relay/ -run 'HideDone|Footer'` green.

- [ ] **Step 2: `status` and `watch` take `--all`** (spec §4.3, §5.1)

  In `cmd/relay/main.go`, `cmdStatus` and `cmdWatch`:
  - Add `all := fs.Bool("all", false, "include bindings marked DONE (hidden by default; relay gc clears them)")`.
  - After `filterReport`, apply the table in spec §4.3: when `target == ""`
    and `!*all`, `rep = relay.HideDone(rep)`. `--name` never filters.
  - `--json` encodes the result of that rule.
  - Update the `status` and `watch` lines in the help text to mention
    `[--all]`.

  Test: if `cmd/relay/main_test.go` has a pattern for running a subcommand
  against a seeded store (look at `TestDiffCommand`), add
  `TestStatusHidesDoneUnlessAllOrNamed`: seed one active and one done
  binding; plain `status --json` has one row and `done_hidden: 1`;
  `status --json --all` has two rows and no `done_hidden`;
  `status --json --name <done>` has the done row and no `done_hidden`. If
  the file has no such pattern, say so in the report and rely on step 1's
  unit tests.

  Verify: `go build ./... && go test ./cmd/relay/` green.

- [ ] **Step 3: the already-gone worktree** (spec §3.4, §4.6, §5.4)

  In `internal/relay/bind.go`:
  - `worktreeOutcome` gains `Gone string`; `UnbindResult` gains
    `WorktreeGone string`. Comments per spec §3.4: exactly one of Removed,
    Kept, Gone is set, or none when the binding never had a worktree.
  - `worktreeTeardown`: after the `rt.Git == nil` check and before
    `rt.Git.Dirty`, `os.Stat(b.Worktree)`; on `errors.Is(err, os.ErrNotExist)`
    return `worktreeOutcome{Gone: b.Worktree}`. Any other stat error falls
    through. Comment: git classifies a `chdir` into a missing directory as
    a missing binary, which is the wrong diagnosis; the caller knows the
    path and checks it first (spec §1).
  - `Unbind` copies `outcome.Gone` into `UnbindResult.WorktreeGone`.

  In `cmd/relay/main.go` `cmdUnbind`: after the removed/kept branches, an
  `else if res.WorktreeGone != ""` printing `worktree <path> was already gone`.

  Test (`bind_test.go`, next to the existing unbind worktree tests):
  `TestUnbindReportsAnAlreadyGoneWorktree` -- seed a binding whose
  `Worktree` points at a path under `t.TempDir()` that does not exist;
  `Unbind` returns `WorktreeGone == that path`, `fakeGit.dirtyCalls == 0`,
  `len(fakeGit.removeWorktreeCalls) == 0`, and the binding is gone from the
  store.

  Verify: `go test ./internal/relay/ -run Unbind` green.

- [ ] **Step 4: `gc` archives by default** (spec §3.2, §3.3, §4.4, §5.3)

  In `internal/relay/gc.go`:
  - Replace `GCOptions.Archive` with `Delete bool`. Comment: archiving is
    the default because every other destruction decision in relay keeps by
    default, and the round logs are the only record of how a feature was
    built (spec §1).
  - `GCResult` gains `WorktreeGone string \`json:"worktree_gone,omitempty"\``.
  - In the loop: copy `outcome.Gone` into the result; `if opts.Delete` →
    `tx.Delete`, else `tx.Archive`.
  - Update the `GC` doc comment.

  In `cmd/relay/main.go` `cmdGC`:
  - `delete := fs.Bool("delete", false, "remove each finished binding's directory instead of archiving it")`.
  - `archive := fs.Bool("archive", false, "no-op; archiving is now the default")` -- parsed, ignored. Comment why it is kept (spec §7.3).
  - Pass `Delete: *delete`.
  - Output: keep the existing verbs. Add, in the archived and deleted
    branches, `else if r.WorktreeGone != ""` printing
    `            worktree <path> was already gone`; in the dry-run branch
    append ` (worktree <path> already gone)`.
  - Help text line for `gc`: `relay gc [--dry-run] [--delete]`.

  Tests (`gc_test.go`):
  - `TestGCArchiveKeepsTheRoundLog` currently passes `Archive: true`; change
    it to `GCOptions{}` and rename to `TestGCArchivesByDefault`. Assert
    `ArchivedTo != ""` and `!Deleted`.
  - `TestGCClearsOnlyDoneBindings` and any other test that expected
    deletion: pass `Delete: true` and keep their assertions.
  - `TestGCDeleteRemovesTheDirectory` -- `Delete: true`; `Deleted`,
    `ArchivedTo == ""`, directory gone, no tarball in `.archive/`.
  - `TestGCReportsAnAlreadyGoneWorktree` -- seed a done binding with a
    `Worktree` path that does not exist; result has `WorktreeGone` set,
    `WorktreeKept == ""`, `fakeGit.dirtyCalls == 0`, and the binding was
    still archived.

  Verify: `go test ./internal/relay/ -run GC` green, then the full suite.

- [ ] **Step 5: `done` hints at `gc`; README** (spec §4.7)

  - `cmdDone`: change the success line to
    `%s marked done; relaying stopped (relay gc archives it when you are finished with it)`.
  - README: in the command list, `relay status [--json] [--name N] [--all]`
    with a clause that DONE bindings are hidden by default and the footer
    names how many; `relay gc [--dry-run] [--delete]` -- archive by
    default, `--delete` to remove, `--archive` accepted as a no-op. In the
    "State on disk" section, update the four-line example block so
    `relay gc` (no flag) archives and `relay gc --delete` removes, and the
    sentence after it. Keep the archive-size paragraph; it is now the
    justification for the default.

  Verify: `make check` (or constituents) green; `grep -n "gc --archive" README.md` finds only the no-op mention.

- [ ] **Step 6: mutation checks -- put the results in your report**

  Do each, run the named test, revert, and record which test failed:
  1. In `HideDone`, drop the `State` comparison (keep every row). Expect
     `TestHideDoneRemovesOnlyDoneRows` to fail.
  2. In `GC`, swap the branch so `Delete == false` deletes. Expect
     `TestGCArchivesByDefault` to fail.
  3. In `worktreeTeardown`, remove the `os.Stat` check. Expect
     `TestGCReportsAnAlreadyGoneWorktree` (or the unbind twin) to fail.

  If a mutation does **not** fail its named test, fix the test, do not
  weaken the code, and say so.

- [ ] **Step 7: commit**

  One commit. Subject:
  `feat(relay): hide DONE bindings from status and archive on gc by default (#56)`.
  Body: the two changes, the already-gone worktree fix, and that
  `--archive` is kept as a no-op. Do not push.

## Report

Your report must contain:

- `git diff --stat` against your base.
- The `make check` (or constituents) output tail.
- The step 6 mutation table: mutation → test that failed.
- Anything you stopped on, or any place the plan and the code disagreed.
