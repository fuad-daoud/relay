# Done releases the worktree; resume restores it

**Issue:** #137 (the no-verb half, from its 2026-09-17 comment). The
`pause`/`PAUSED` half waits for the #114 surface freeze (~2026-10-12) and
builds on this.
**Depends on:** nothing open. Builds on `worktreeTeardown` (`gc`/`unbind`,
#56), headless `done` (#99 §4.6), resume/rebind (#14, #119, #92).
**Consumed by (later):** #137's `pause` verb (same restore step on
resume); #145 `add --branch` (same `CheckoutWorktree` git call).
**Status:** draft; plan at `docs/plans/2026-09-17-done-releases-worktree.md`.

## 1. System overview

A finished round's worktree locks its branch against the human. On
2026-09-17, with round 1 closed, the PR open and the tree clean, `gh pr
checkout` in the main repo was refused: `'relay/agent-install' is already
used by worktree at ~/.local/state/relay/.worktrees/agent-install`. That is
git's one-checkout-per-branch rule; relay holding the worktree is what holds
the branch. The only sanctioned release today is `relay done` **then**
`relay gc`, and `gc` also archives the binding -- so reviewing a branch in
an editor costs the binding's live record.

This design moves the worktree release from `gc` to `done`, and teaches
`bind --resume` to put the tree back:

- **`relay done` releases a clean worktree.** After DONE is saved, the same
  teardown `gc` and `unbind` run today decides: clean -> removed, the
  branch kept; dirty -> kept with the reason; already missing -> gone. Two
  guards keep the tree even when clean: a pane binding with an open round
  (nothing stopped its builder) and a headless binding whose process stop
  failed. `done` prints what it did. `gc` is unchanged: it archives, and
  its own teardown finds the tree gone.
- **`relay bind --resume` restores a missing worktree.** A binding whose
  recorded `Worktree` no longer exists on disk gets it back at the same
  path from its recorded `Branch` before anything is spawned -- via a new
  `Git.CheckoutWorktree` (the existing-branch form of `git worktree add`;
  `AddWorktree` only creates branches). Git's "already checked out" refusal
  surfaces as `git.ErrBranchCheckedOut`.
- **Rebind on a DONE binding is permitted when the tree was restored.** #14
  refused every rebind of a DONE binding. A restored pane binding's old
  builder cannot work (its cwd is a deleted inode, even after the path is
  recreated), so a fresh builder is the only working resume: the alive and
  unverified checks are skipped for it, and `bind` names the orphaned pane
  so the human closes it.

### Decisions taken

1. **No verb, no state.** `done` and `bind --resume` exist; `PAUSED` waits
   for the freeze. A DONE binding whose tree is gone is the record `gc`
   will archive, not a new lifecycle state.
2. **Pane builders are not stopped and not closed.** `done` never touched
   a pane; it still does not. The open-round guard is what keeps a working
   pane builder's tree under it. CLAUDE.md's "exactly two places" list is
   unchanged.
3. **Restore is state-independent; the refusal lifts are DONE-specific.**
   A hand-removed tree on an ACTIVE binding is the same situation, and
   restoring it on resume is the right thing there too. Only the #14
   refusal and the alive/unverified checks key on "restored".
4. **No `Branch` recorded, no restore.** A `bind.json` from before `Branch`
   existed, or a `--cwd` binding, has no branch to check out; resume
   refuses with a message rather than guessing `relay/<name>`.
5. **`done` prints; it does not log.** `unbind` and `gc` log nothing about
   the tree either. #172 owns the record question.
6. **Teardown runs inside the lock**, as `gc`'s does, so a daemon tick
   cannot interleave between DONE being saved and the tree being judged.

### Scope boundary

`internal/relay/status.go` (`Done`, new `DoneResult`),
`internal/relay/text.go` (`DoneText`), `internal/relay/bind.go` (`resume`:
restore step, two refusal sites, one skip), `internal/relay/candidate.go`
(three fields on `Resolution`), `internal/relay/herdr.go` (`Git` interface),
`internal/git/client.go` + `types.go` (`CheckoutWorktree`,
`ErrBranchCheckedOut`), `cmd/relay/main.go` (`cmdDone` prints the result;
`cmdBind` prints restore lines), `internal/pick/model.go` (the `done` verb's
text), README and CLAUDE.md. `make e2e` is not required: nothing on the
reconcile, nudge, fingerprint or scrape path changes.

Out of scope: `relay pause`, `PAUSED`, `--commit`, closing the orphaned
pane, pushing, resuming into a different path, `add --branch` (#145).

## 2. File structure

```
internal/git/
  types.go          + ErrBranchCheckedOut
  client.go         + (*Client).CheckoutWorktree
internal/relay/
  herdr.go          Git interface + CheckoutWorktree
  fake_test.go      fakeGit + checkoutWorktreeCalls / checkoutWorktreeErr
  status.go         Done returns DoneResult; release step
  text.go           DoneText(name, res)
  bind.go           resume: restoreWorktree, refusal lifts, RestoreText
  candidate.go      Resolution + RestoredWorktree, OrphanedPane
  status_test.go    done release cases
  bind_test.go      restore cases
  text_test.go      DoneText / RestoreText cases
  gc_test.go        gc after done: gone, still archived
cmd/relay/main.go   cmdDone, cmdBind output
internal/pick/model.go
                    VerbDone text
README.md           cleanup paragraph; DONE bullet; resume note
CLAUDE.md           done/gc sentence
```

## 3. Data structures

### 3.1 `DoneResult` (`internal/relay/status.go`)

Exactly one of the three path fields is set, or none when the binding has
no `Worktree` (a `--cwd` or adopted binding). Same shape and meaning as the
worktree fields of `UnbindResult`.

| field | type | meaning |
| --- | --- | --- |
| `WorktreeRemoved` | `string` | path relay removed; the branch survives. |
| `WorktreeKept` | `string` | path relay left in place. |
| `KeptReason` | `string` | why; `""` unless `WorktreeKept` is set. |
| `WorktreeGone` | `string` | recorded path that no longer exists. |
| `Branch` | `string` | `b.Branch`, for the message; may be `""`. |

Kept reasons, exact strings: `uncommitted changes`; `dirty check failed:
<brief>`; `git unavailable`; a wrapped `RemoveWorktree` error via `brief`;
and the two `done`-specific ones: `round N open; the builder may still
write` (pane binding, `!b.RoundStartedAt.IsZero()`, N = `b.Round`) and
`builder process still running` (headless, `stopProcess` returned an
error).

### 3.2 `Resolution` additions (`internal/relay/candidate.go`)

| field | type | meaning |
| --- | --- | --- |
| `RestoredWorktree` | `string` | path re-added on this resume; `""` when nothing was restored. |
| `RestoredBranch` | `string` | branch it was checked out from; set with `RestoredWorktree`. |
| `OrphanedPane` | `string` | the previous builder's pane id whenever a pane binding's worktree was restored (planner-only resume included: that pane cannot work in the recreated directory); `""` otherwise. |

All three are `omitempty` if `Resolution` is ever serialised; today it is
not.

### 3.3 `git.ErrBranchCheckedOut`

`errors.New("branch is checked out in another worktree")`. Returned by
`CheckoutWorktree` when git's stderr contains `is already checked out` or
`is already used by worktree` (git 2.42+ wording).

## 4. Interfaces

### 4.1 `Git.CheckoutWorktree`

```
CheckoutWorktree(ctx context.Context, dir, path, branch string) error
```

Runs `git -C <dir> worktree add <path> <branch>` for an **existing**
branch. Preconditions: `branch` exists; `path` does not. Postconditions:
`path` is a worktree of `dir`'s repository with `branch` checked out.
Errors: `ErrBranchCheckedOut`; `ErrNotRepo`; `ErrGitUnavailable`; a wrapped
failure. On failure a `path` that did not exist before is removed and
`worktree prune` is run, as `AddWorktree` does. `path` is absolutised
against `dir` when relative, as `AddWorktree` does. The `fakeGit` records
`checkoutWorktreeCalls []checkoutWorktreeCall{Dir, Path, Branch}` and
returns `checkoutWorktreeErr`.

### 4.2 `relay.Done`

```
func Done(ctx context.Context, rt Runtime, name string) (DoneResult, error)
```

Single responsibility: end relaying for a binding and give its worktree
back when that is safe.

- Everything up to and including the hooks dispatch is as today, inside
  `WithLock`. The `ErrStopFailed` wrap is still returned -- but now
  **after** the release step, and the release step sees the failure.
- Release step (inside the lock, after `tx.Save`):
  - `b.Worktree == ""` -> zero result.
  - headless and `stopErr != nil` -> kept, `builder process still running`.
  - pane and `!b.RoundStartedAt.IsZero()` -> kept, `round N open; the
    builder may still write`.
  - otherwise `worktreeTeardown(ctx, rt, b, false)` mapped onto the result.
- `DoneResult.Branch = b.Branch` always.
- Postcondition: `State == StateDone` is saved whether or not the tree was
  released; a teardown failure is a kept reason, never an error.

### 4.3 `relay.DoneText`

```
func DoneText(name string, res DoneResult) string
```

Line 1 unchanged: `<name> marked done; relaying stopped (relay gc archives
it when you are finished with it)`. Then at most one of:

| case | line |
| --- | --- |
| removed, branch known | `removed worktree <p> (branch <b> is free to check out)` |
| removed, branch `""` | `removed worktree <p>` |
| kept | `kept worktree <p> (<reason>); relay gc retries when it is clean` |
| gone | `worktree <p> was already gone` |

Joined with `\n`, no trailing newline (the callers add one).

### 4.4 `resume` (`internal/relay/bind.go`) -- restore step

Runs first, before the `rebinding` block, on the loaded binding:

- `restore := b.Worktree != "" && os.Stat(b.Worktree)` is `ErrNotExist`.
  Any other stat error is "present" (relay never re-adds over something it
  cannot read).
- If `restore` and `b.Branch == ""`: return `fmt.Errorf("binding %q: worktree
  %s is gone and no branch is recorded; relay add to start fresh", name,
  b.Worktree)`.
- If `restore`: `rt.Git == nil` -> error `git unavailable`. The repository
  to check out from is **`opts.CWD`, the caller's cwd** -- an `add`
  binding's `b.CWD` is the worktree itself (the directory that is gone) and
  the repository it was cut from is not recorded. So first
  `rt.Git.BranchExists(ctx, opts.CWD, b.Branch)`: an error is wrapped
  `restore worktree: %w`; `false` refuses with `binding %q: branch %s is
  not in %s; run resume from the repository the worktree was cut from`.
  Then `rt.Git.CheckoutWorktree(ctx, opts.CWD, b.Worktree, b.Branch)`;
  `ErrBranchCheckedOut` -> `fmt.Errorf("binding %q: branch %s is checked out
  in another worktree (git worktree list); free it, then resume", ...)`,
  other errors wrapped as `restore worktree: %w`. On success
  `res.RestoredWorktree = b.Worktree`, `res.RestoredBranch = b.Branch`.
- Then the existing flow with three changes, each keyed on `restore`:
  1. the pre-spawn `StateDone` refusal is skipped when `restore`;
  2. for a **pane** binding when `restore`: the `ListAgents`/`FindAgent`
     alive check and the `ErrBuilderUnverified` guard are skipped, and
     `res.OrphanedPane = b.Builder.PaneID` (when non-empty). Headless is
     unchanged: a process is either alive or not, and `done` stopped it.
  3. the in-lock `StateDone` refusal is skipped when `restore`.
- Planner-only resume (not rebinding) with `restore`: the tree is back and
  State becomes Active as today; for a pane binding `res.OrphanedPane` is
  still set, because the old pane cannot work in the recreated directory
  and the human must know that before the next `send`.

### 4.5 `relay.RestoreText`

```
func RestoreText(res Resolution) string
```

`""` when `RestoredWorktree == ""`. Otherwise `restored worktree <p> on
<branch>` and, when `OrphanedPane != ""`, a second line `old builder pane
<id> is in the removed directory; close it: herdr pane close <id>`. Joined
with `\n`.

### 4.6 CLI

- `cmdDone`: `res, err := relay.Done(...)`; on `err` still return it (the
  `ErrStopFailed` case marks DONE and reports the pid, as today) -- but
  print `relay.DoneText(target, res)` **before** returning a non-nil
  `ErrStopFailed` error, so the tree outcome is not lost behind the pid
  message. Other errors print nothing.
- `internal/pick/model.go` `VerbDone`: same call, `DoneText(name, res)`.
- `cmdBind`: after `BindResolved`, print `relay.RestoreText(res)` when
  non-empty, before the existing `rebound`/`bound` lines, in both branches.

## 5. High-level pseudocode

### 5.1 `Done`

```
Done(ctx, rt, name):
  var out DoneResult
  err := WithLock(tx):
    b := tx.Load(name)
    oldState := b.State; b.State = Done
    pid, stopErr := stopProcess(ctx, rt, b.Builder, "done")
    if stopErr == nil: b.Builder = clearProcess(b.Builder)
    tx.Save(b)
    hooks dispatch as today
    out.Branch = b.Branch
    switch:
      b.Worktree == "":                        -- nothing
      b.Builder.Headless() && stopErr != nil:  out.kept(b.Worktree, "builder process still running")
      !b.Builder.Headless() && !b.RoundStartedAt.IsZero():
                                               out.kept(b.Worktree, "round N open; the builder may still write")
      default:                                 out.from(worktreeTeardown(ctx, rt, b, false))
    if stopErr != nil: return ErrStopFailed wrap (as today)
    return nil
  return out, err
```

Note `out` is filled before the `ErrStopFailed` return so the caller has
both.

### 5.2 `resume` restore step

```
resume(ctx, rt, opts, planner):
  b := rt.Store.Load(opts.Name)                      -- moved up; today loaded inside `if rebinding`
  restore := b.Worktree != "" && stat(b.Worktree) is ErrNotExist
  var res Resolution
  if restore:
    if b.Branch == "": return error "no branch is recorded"
    if rt.Git == nil:  return error "git unavailable"
    exists := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)   -- opts.CWD: b.CWD is the gone worktree
    !exists -> "run resume from the repository the worktree was cut from"
    err := rt.Git.CheckoutWorktree(ctx, opts.CWD, b.Worktree, b.Branch)
    ErrBranchCheckedOut -> "checked out in another worktree" error; other -> wrap
    res.RestoredWorktree, res.RestoredBranch = b.Worktree, b.Branch
    if !b.Builder.Headless(): res.OrphanedPane = b.Builder.PaneID
  if rebinding:
    if b.State == Done && !restore: refuse (today's message)
    headless: as today
    pane: if !restore { alive check; unverified guard }   -- both skipped on restore
    builder, res2 := resolveBuilder(...); merge res2 into res (res2's fields win; the three restore fields are carried over)
  WithLock: as today, with `if rebinding && b.State == Done && !restore` at the in-lock refusal
```

`resolveBuilder` returns a fresh `Resolution`; the restore step's three
fields are copied onto it after it returns. A planner-only resume returns
the restore-only `Resolution`.

## 6. Error handling

| category | example | recoverable | surfaces as |
| --- | --- | --- | --- |
| release refused | dirty tree, open pane round, failed headless stop, git missing | yes -- `gc` retries | `DoneResult.WorktreeKept` + reason; printed; never an error |
| restore precondition | no `Branch`; `rt.Git == nil`; branch not in the caller's repo | no | `resume` error before any spawn; exit non-zero |
| restore refused by git | branch checked out elsewhere | yes -- free it and re-run | `ErrBranchCheckedOut` wrapped with the `git worktree list` hint |
| restore failed | any other `CheckoutWorktree` error | no | wrapped `restore worktree: <err>`; `AddWorktree`-style cleanup of a half-made path |
| existing | `ErrStopFailed`, `ErrBuilderAlive`, `ErrHeadlessAdopt`, `ErrRunnerUnavailable`, `store.ErrNotFound` | as today | unchanged |

No logging, no hook events beyond today's `state_changed` on `done`.

## 7. Verification

Pure tests in `internal/relay` over `fakeGit`, `fakeHerdr`, `fakeRunner`,
temp-dir worktrees (the `gc_test.go` pattern: `Worktree: t.TempDir()`
exists; a joined-but-uncreated path is "gone"):

1. `done` on a pane binding, round closed, clean tree: `WorktreeRemoved`
   set, one `RemoveWorktree` call with `Dir == b.CWD`, `Path ==
   b.Worktree`, `Force == false`; `Branch` carried; State DONE.
2. dirty tree: `WorktreeKept`, reason `uncommitted changes`, no remove.
3. pane binding, `RoundStartedAt` set: kept, reason `round 1 open; the
   builder may still write`, `Dirty` never called.
4. headless binding, runner stop fails: kept, `builder process still
   running`, the returned error still wraps `ErrStopFailed`, State DONE.
5. headless binding, stop succeeds: removed.
6. `Worktree == ""`: zero result, no git calls.
7. recorded path missing: `WorktreeGone`.
8. `gc` after (1): the gc row reports `WorktreeGone`, the binding is
   archived; `gc` output text unchanged for it.
9. resume, tree missing, `Branch` set, planner-only, seeded in the real
   shape (`CWD == Worktree`): one `CheckoutWorktree` call `{opts.CWD,
   b.Worktree, b.Branch}` after `BranchExists(opts.CWD, b.Branch)`;
   `Resolution.RestoredWorktree`/`RestoredBranch` set; pane binding ->
   `OrphanedPane == old pane id`; headless -> `OrphanedPane == ""`; State
   Active.
10. resume, tree missing, `Branch == ""`: error mentions `no branch is
    recorded`; no git call; state unchanged.
10b. resume, tree missing, `BranchExists(opts.CWD, ...)` false: error
    mentions `run resume from the repository`; no checkout call; state
    unchanged. (Found by the live smoke test: the fakes had been seeded
    with `CWD: "/repo"`, which is not an `add` binding's shape.)
11. resume, `checkoutWorktreeErr = git.ErrBranchCheckedOut`: error mentions
    `git worktree list`; no tab, no start.
12. resume, tree present: no `CheckoutWorktree` call; existing behaviour.
13. rebind on DONE, tree present: refused with today's message (the
    existing subtest stays green).
14. rebind on DONE, tree missing, pane binding whose old pane is **still
    listed alive** by `fakeHerdr`: not refused, no `ErrBuilderAlive`, a
    new builder spawned, `OrphanedPane` set, State Active, mode pane.
15. rebind on DONE, tree missing, headless binding: the headless path is
    unchanged (no alive skip; `done` already cleared the pid), new headless
    endpoint, no `OrphanedPane`.
16. `DoneText` four cases; `RestoreText` three cases (none / restored /
    restored + orphaned).
17. `git.Client.CheckoutWorktree` error mapping: a unit test in
    `internal/git` over a real temp repository (the package already tests
    against real git): checkout of an existing branch succeeds; a second
    checkout of the same branch returns `ErrBranchCheckedOut`.

Mutation targets, named in the plan: drop the open-round guard (case 3);
drop `!restore` from the DONE refusal (case 14); drop the `Branch == ""`
check (case 10).
