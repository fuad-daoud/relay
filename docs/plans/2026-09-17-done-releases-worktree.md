# Done releases the worktree; resume restores it (#137, no-verb half)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-17-done-releases-worktree-design.md`. Section
numbers below (§) refer to it.
**Issue:** #137. Does **not** close it: the `pause`/`PAUSED` half stays
open for after the surface freeze. The PR body says "Refs #137".
**Depends on:** nothing open.

**Goal:** `relay done` gives a clean worktree back (branch kept) so the
human can check the branch out in the main repo without `gc`; `relay bind
--resume` puts a missing worktree back at its recorded path from its
recorded branch, and a DONE binding whose tree was restored may be rebound
to a fresh builder.

**Architecture:** `Done` returns a `DoneResult` and, inside its existing
lock after saving DONE, runs the `worktreeTeardown` that `gc`/`unbind`
already use, behind two `done`-specific keep guards (open pane round;
failed headless stop). `resume` gains a restore step before any spawn using
a new `Git.CheckoutWorktree` (existing-branch `git worktree add`), and keys
three existing checks on `restore`. `Resolution` carries what was restored
so `cmd/relay` can print it. No new verb, state, log kind or hook event.

**Tech stack:** Go 1.22. Verification is the `make check` constituent set
(runs `-race`). `internal/git` tests run against real git in temp repos.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/done-release` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/done-release`, cut from `main`. |
| `~/.local/state/relay/done-release` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Resuming a round another builder started

The branch may already carry commits from an earlier builder on this same
round. Before Task 1, run `git log --oneline main..HEAD`. A task whose
commit message is already there is **done**: run its final "Run" step to
confirm it is green, tick it, and continue with the next task. Do not redo
it, do not amend it. An untracked or modified file from an unfinished task
is yours to finish or replace as that task's steps say.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` or `make e2e` yourself: nothing here touches the
reconcile, nudge, fingerprint or scrape path. **No test is added under
`cmd/relay`** (CI runners have no `herdr`; CLAUDE.md). Every new test is a
pure function or a fake-backed call in `internal/relay`, or a real-git
temp-repo test in `internal/git`.

## Global constraints

- **No new verb, state, log kind or hook event** (§1 decision 1, 5).
  `main.go`'s command `switch` is not edited.
- **`done` never stops or closes a pane** (§1 decision 2). CLAUDE.md's
  "exactly two places" and "exactly three" lists are not edited.
- **The release step runs inside `Done`'s existing `WithLock`, after
  `tx.Save`** (§1 decision 6, §5.1). It never returns an error of its own:
  a failed teardown is a kept reason (§4.2).
- **Keep guards, exact reasons** (§3.1): pane + `!b.RoundStartedAt.IsZero()`
  -> `round N open; the builder may still write` (N = `b.Round`);
  headless + `stopErr != nil` -> `builder process still running`. Both are
  checked **before** `worktreeTeardown` so `Dirty` is not consulted.
- **`ErrStopFailed` is still returned** from `Done`, after the release step
  has filled the result (§5.1).
- **Restore runs before any spawn, on the loaded binding, for any state**
  (§1 decision 3, §4.4). `restore` is true only when `b.Worktree != ""` and
  `os.Stat` reports `ErrNotExist`.
- **No `Branch`, no restore** (§1 decision 4): the exact error wording is
  in §4.4.
- **Exactly three checks key on `restore`** (§4.4): the two `StateDone`
  refusals and, for a pane binding only, the alive check + unverified
  guard. The headless path is untouched.
- Text is exactly §4.3 and §4.5.
- `internal/relay/e2e_test.go`, `reconcile.go`, `send.go`, `headless.go`
  are not edited.
- One commit per task, on the worktree's branch.

---

### Task 1: `Git.CheckoutWorktree` and `ErrBranchCheckedOut`

**Files:**
- Modify: `internal/git/types.go` (new sentinel after `ErrWorktreeDirty`)
- Modify: `internal/git/client.go` (new method after `AddWorktree`)
- Modify: `internal/git/client_test.go` (new test after `TestWorktreeLifecycle`, line 255)
- Modify: `internal/relay/herdr.go` (`Git` interface, line 33-42: add the method after `AddWorktree`)
- Modify: `internal/relay/fake_test.go` (`fakeGit`: fields + method after `AddWorktree`, line 118)

**Interfaces:**
- Produces: `var ErrBranchCheckedOut = errors.New("branch is checked out in another worktree")` (§3.3);
  `func (c *Client) CheckoutWorktree(ctx context.Context, dir, path, branch string) error` (§4.1);
  `Git.CheckoutWorktree(ctx context.Context, dir, path, branch string) error` on the relay interface;
  `fakeGit.checkoutWorktreeCalls []checkoutWorktreeCall` with `type checkoutWorktreeCall struct{ Dir, Path, Branch string }`, and `fakeGit.checkoutWorktreeErr error`.

- [ ] **Step 1: Write the failing real-git test** `TestCheckoutWorktree` in
  `client_test.go`, using the file's existing helpers to init a temp repo
  with one commit (read `TestWorktreeLifecycle` for how it builds one):
  create branch `feature` via `AddWorktree(ctx, repo, wt1, "feature", "HEAD")`,
  then `RemoveWorktree(ctx, repo, wt1, false)`. Assert:
  (a) `CheckoutWorktree(ctx, repo, wt2, "feature")` returns nil and
  `wt2/.git` exists and `git -C wt2 rev-parse --abbrev-ref HEAD` prints
  `feature`; (b) `CheckoutWorktree(ctx, repo, wt3, "feature")` -- the branch
  is now checked out in `wt2` -- returns an error with
  `errors.Is(err, ErrBranchCheckedOut)` and `wt3` does not exist afterwards
  (cleanup ran); (c) `CheckoutWorktree(ctx, repo, wt4, "no-such-branch")`
  returns a non-nil error that is **not** `ErrBranchCheckedOut`.

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1 ./internal/git
  -run TestCheckoutWorktree`: compile error, `CheckoutWorktree` undefined.

- [ ] **Step 3: Implement.** In `types.go` add `ErrBranchCheckedOut` with a
  doc comment. In `client.go` add `CheckoutWorktree` modelled on
  `AddWorktree`: absolutise `path` against `dir`; record whether it
  existed; the same deferred cleanup (`os.RemoveAll` + `worktree prune`
  on error when it did not exist); `branchName :=
  strings.TrimPrefix(branch, "refs/heads/")`; run `worktree add <absPath>
  <branchName>` (no `-b`, no commit); on error lower-case the text and
  return `ErrBranchCheckedOut` when it contains `is already checked out`
  or `is already used by worktree`, else the error as is. Doc comment per
  §4.1. In `herdr.go` add the method to the `Git` interface with a one-line
  comment: existing-branch form of `git worktree add`; `AddWorktree`
  creates the branch, this one checks it out. In `fake_test.go` add the
  call type, the two fields, and the recording method.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/git ./internal/relay`
  and `go vet ./...`. Expected: PASS (relay compiles again because the
  fake satisfies the widened interface).

- [ ] **Step 5: Commit**

```bash
git add internal/git/types.go internal/git/client.go internal/git/client_test.go internal/relay/herdr.go internal/relay/fake_test.go
git commit -m "feat(git): CheckoutWorktree adds a worktree on an existing branch (#137)"
```

---

### Task 2: `Done` releases the worktree; `DoneText` says so

**Files:**
- Modify: `internal/relay/status.go` (`Done`, line 550-596; new `DoneResult` above it)
- Modify: `internal/relay/text.go` (`DoneText`, line 14-16)
- Modify: `internal/relay/status_test.go` (`TestDoneStopsRelaying`, line 290; new tests after it)
- Modify: `internal/relay/text_test.go` (`TestDoneText`, line 9)
- Modify: `internal/relay/headless_test.go` (the five `TestDoneHeadless*` / `TestDonePane*` tests, lines 1364-1510: adapt to the new return arity only)
- Modify: `internal/relay/reconcile_hooks_test.go:147` (`TestDone_EmitsStateChanged`: return arity only)
- Modify: `internal/relay/gc_test.go` (new test after `TestGCWorktreeTeardown`)
- Modify: `cmd/relay/main.go` (`cmdDone`, line 1460-1465)
- Modify: `internal/pick/model.go` (line 84-87)

**Interfaces:**
- Consumes: `worktreeTeardown(ctx, rt, b, false) worktreeOutcome` (bind.go:491; fields `Removed`, `Kept`, `Reason`, `Gone`); `stopProcess`, `clearProcess`; `store.Binding.RoundStartedAt`, `.Worktree`, `.Branch`; `(store.Endpoint).Headless()`.
- Produces: `type DoneResult struct { WorktreeRemoved, WorktreeKept, KeptReason, WorktreeGone, Branch string }` (§3.1);
  `func Done(ctx context.Context, rt Runtime, name string) (DoneResult, error)` (§4.2);
  `func DoneText(name string, res DoneResult) string` (§4.3).

- [ ] **Step 1: Write the failing tests.**
  - In `text_test.go`, rewrite `TestDoneText` as a table over §4.3:
    zero result -> exactly the old one-line string; `{WorktreeRemoved:
    "/w", Branch: "relay/x"}` -> old line + `\nremoved worktree /w (branch
    relay/x is free to check out)`; `{WorktreeRemoved: "/w"}` -> old line
    + `\nremoved worktree /w`; `{WorktreeKept: "/w", KeptReason:
    "uncommitted changes"}` -> old line + `\nkept worktree /w (uncommitted
    changes); relay gc retries when it is clean`; `{WorktreeGone: "/w"}` ->
    old line + `\nworktree /w was already gone`.
  - In `status_test.go`, after `TestDoneStopsRelaying` (which changes only
    to `_, err := Done(...)`), add, each building on `sentBinding` /
    `newRuntime` + `rt.Git = &fakeGit{}` and saving a modified binding with
    `rt.Store.Save` before calling `Done`:
    - `TestDoneReleasesCleanWorktree`: `b.Worktree = t.TempDir()`, `b.Branch
      = "relay/webshop"`, `b.RoundStartedAt = time.Time{}`, `fg.dirtyResult
      = false`. Assert nil error; `res.WorktreeRemoved == b.Worktree`;
      `res.Branch == "relay/webshop"`; `len(fg.removeWorktreeCalls) == 1`
      with `Dir == b.CWD`, `Path == b.Worktree`, `Force == false`; loaded
      State is `StateDone`.
    - `TestDoneKeepsDirtyWorktree`: same but `fg.dirtyResult = true`.
      Assert `res.WorktreeKept == b.Worktree`, `res.KeptReason ==
      "uncommitted changes"`, no remove call.
    - `TestDoneKeepsWorktreeWhileRoundOpen`: pane binding, `b.RoundStartedAt
      = rt.Now()` (or any non-zero time), `b.Round = 1`. Assert kept,
      reason `round 1 open; the builder may still write`, `fg.dirtyCalls
      == 0`, no remove call.
    - `TestDoneNoWorktreeIsZeroResult`: `b.Worktree = ""`. Assert
      `res == DoneResult{Branch: b.Branch}` and `fg.dirtyCalls == 0`.
    - `TestDoneReportsGoneWorktree`: `b.Worktree = filepath.Join(t.TempDir(),
      "missing")`. Assert `res.WorktreeGone == b.Worktree`.
  - In `headless_test.go`, next to `TestDoneHeadlessKillFailureStillMarksDone`,
    add `TestDoneHeadlessKillFailureKeepsWorktree`: same setup as that
    test plus `b.Worktree = t.TempDir()` saved; assert the error still
    `errors.Is(err, ErrStopFailed)`, `res.WorktreeKept == b.Worktree`,
    `res.KeptReason == "builder process still running"`, and no
    `RemoveWorktree` call. And `TestDoneHeadlessStopReleasesWorktree`:
    same as `TestDoneHeadlessStopsTheLiveProcess` plus a temp worktree and
    `fg.dirtyResult = false`; assert `res.WorktreeRemoved == b.Worktree`.
  - In `gc_test.go`, `TestGCAfterDoneReportsGone`: seed a binding with a
    temp worktree, State Active, `Branch` set; call `Done`; assert
    `WorktreeRemoved`; then `GC(ctx, rt, GCOptions{})`; assert the row for
    it has `WorktreeGone == wt` and `ArchivedTo != ""`. (The temp dir still
    exists on disk because `fakeGit` removes nothing -- so before `GC`, do
    `os.RemoveAll(wt)` to model what real git did, and say so in a
    comment.)

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1
  ./internal/relay -run 'TestDone|TestGCAfterDone'`: compile errors
  (`DoneResult` undefined; `Done` returns one value).

- [ ] **Step 3: Implement.** In `status.go` add `DoneResult` with the §3.1
  doc comment, then change `Done` per §5.1: declare `var out DoneResult`
  outside the lock; inside, after the hooks dispatch, `out.Branch =
  b.Branch` and the four-way switch (`b.Worktree == ""` -> nothing;
  headless + `stopErr != nil` -> kept `builder process still running`;
  pane + `!b.RoundStartedAt.IsZero()` -> kept `fmt.Sprintf("round %d open;
  the builder may still write", b.Round)`; default -> map
  `worktreeTeardown(ctx, rt, b, false)` onto `out`); keep the existing
  `ErrStopFailed` return after it; return `out, err`. Update the `Done` doc
  comment: "and gives a clean worktree back (§4.2)". In `text.go` change
  `DoneText` to the §4.3 shape. Fix the compile at every caller: the two
  test files that only need `_, err :=`; `cmd/relay/main.go` `cmdDone`
  becomes

  ```
  res, err := relay.Done(ctx, rt, target)
  if err != nil && !errors.Is(err, relay.ErrStopFailed) { return err }
  fmt.Println(relay.DoneText(target, res))
  if err != nil { return err }
  warnWaitingOnYou(rt, target)
  return nil
  ```

  (check `errors` is imported there -- it is, `explicitBinding` uses it);
  `internal/pick/model.go` `VerbDone` becomes `res, err :=
  relay.Done(...)`; on `err` return `verbDoneMsg{err: err}`; else
  `verbDoneMsg{text: relay.DoneText(name, res)}`.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay
  ./internal/pick ./cmd/relay` and `go vet ./...`. Expected: PASS.

- [ ] **Step 5: Mutation check** (do not commit): delete the open-round
  guard -> `TestDoneKeepsWorktreeWhileRoundOpen` fails (it reports
  `WorktreeRemoved`). Revert; re-run Step 4. Name it in your report.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/status.go internal/relay/text.go internal/relay/status_test.go internal/relay/text_test.go internal/relay/headless_test.go internal/relay/reconcile_hooks_test.go internal/relay/gc_test.go cmd/relay/main.go internal/pick/model.go
git commit -m "feat(done): release a clean worktree at done, keeping the branch (#137)"
```

---

### Task 3: `bind --resume` restores a missing worktree

**Files:**
- Modify: `internal/relay/candidate.go` (`Resolution`, line 58-75: three fields)
- Modify: `internal/relay/bind.go` (`resume`, line 149-260)
- Modify: `internal/relay/text.go` (new `RestoreText` after `DoneText`)
- Modify: `internal/relay/bind_test.go` (new tests after the `rebind of DONE binding is refused` subtest's parent test, ~line 740)
- Modify: `internal/relay/text_test.go` (new `TestRestoreText`)
- Modify: `cmd/relay/main.go` (`cmdBind`, line 591-620)

**Interfaces:**
- Consumes: `Git.CheckoutWorktree` (Task 1), `git.ErrBranchCheckedOut`, `FindAgent`, `DiagnoseBuilder`, `resolveBuilder`, `store.Binding.Worktree/Branch/CWD/Builder`.
- Produces: `Resolution.RestoredWorktree`, `Resolution.RestoredBranch`, `Resolution.OrphanedPane` (`string`, §3.2);
  `func RestoreText(res Resolution) string` (§4.5).

- [ ] **Step 1: Write the failing tests.**
  - `text_test.go` `TestRestoreText`: zero `Resolution` -> `""`;
    `{RestoredWorktree: "/w", RestoredBranch: "relay/x"}` -> `restored
    worktree /w on relay/x`; plus `OrphanedPane: "w2:p4"` -> that line +
    `\nold builder pane w2:p4 is in the removed directory; close it: herdr
    pane close w2:p4`.
  - `bind_test.go`, each on `newRuntime(t, f)` with `rt.Git = fg` and a
    saved binding `webshop` whose `Worktree` is
    `filepath.Join(t.TempDir(), "gone")` (never created), `Branch:
    "relay/webshop"`, `CWD: "/repo"`, `Builder: {PaneID: "w2:p4"}`:
    - `TestResumeRestoresMissingWorktree` (planner-only: `BindOptions{Name,
      Resume: true, PlannerPane: "w2:p3", CWD: "/repo"}`, State Done):
      nil error; `len(fg.checkoutWorktreeCalls) == 1` equal to `{Dir:
      "/repo", Path: <worktree>, Branch: "relay/webshop"}`;
      `res.RestoredWorktree == <worktree>`, `res.RestoredBranch ==
      "relay/webshop"`, `res.OrphanedPane == "w2:p4"`; loaded State
      `StateActive`. Use `BindResolved` to get `res`.
    - `TestResumeRestoreHeadlessHasNoOrphan`: same with `Builder: {Mode:
      store.ModeHeadless}` and a `fakeRunner` on `rt` (see `headless_test.go`
      for how one is wired): `res.OrphanedPane == ""`, restore call made.
    - `TestResumeRefusesRestoreWithoutBranch`: `Branch: ""`. Error contains
      `no branch is recorded`; `len(fg.checkoutWorktreeCalls) == 0`; State
      unchanged.
    - `TestResumeSurfacesBranchCheckedOut`: `fg.checkoutWorktreeErr =
      git.ErrBranchCheckedOut`. Error contains `git worktree list`; `len(f.tabs)
      == 0 && len(f.starts) == 0`.
    - `TestResumePresentWorktreeIsNotRestored`: `Worktree: t.TempDir()`
      (exists). `len(fg.checkoutWorktreeCalls) == 0`; `res.RestoredWorktree
      == ""`.
    - `TestRebindOnDoneWithRestoredWorktree`: State Done, worktree missing,
      `f.agents` lists the planner **and** the old builder pane `w2:p4`
      alive (`builderAgent(herdr.StatusIdle)` or the file's equivalent),
      `f.newPane = "w2:p5"`, `BindOptions{..., Resume: true, Rebind: true}`
      with a candidate set resolvable through the test policy (copy the
      setup of the passing rebind test in this file). Assert: nil error
      (no `ErrBuilderAlive`); one checkout call; `res.OrphanedPane ==
      "w2:p4"`; loaded `Builder.PaneID == "w2:p5"`; State Active; the
      binding is still a pane binding.
    - The existing subtest `rebind of DONE binding is refused` keeps its
      present worktree (it has none, `Worktree == ""`, so `restore` is
      false) and must stay green unchanged.

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1
  ./internal/relay -run 'TestResume|TestRebindOnDone|TestRestoreText'`:
  compile errors (`RestoredWorktree`, `RestoreText` undefined).

- [ ] **Step 3: Implement** per §4.4 / §5.2.
  - `candidate.go`: three fields on `Resolution` with the §3.2 comments.
  - `text.go`: `RestoreText`.
  - `bind.go` `resume`: hoist the `rt.Store.Load(opts.Name)` to the top of
    the function (before `rebinding` is computed) -- the `if rebinding`
    block then uses that `b` instead of loading again. Compute `restore`
    with `os.Stat` + `errors.Is(err, os.ErrNotExist)`. When `restore`:
    the `Branch == ""` refusal, the `rt.Git == nil` refusal (`errors.New("git
    unavailable; cannot restore worktree")`), the `CheckoutWorktree` call
    with the two error mappings, then set the three `Resolution` fields on
    a local `res` (`OrphanedPane` only when `!b.Builder.Headless() &&
    b.Builder.PaneID != ""`). In the `rebinding` block: the pre-spawn
    `StateDone` refusal gains `&& !restore`; in the pane branch, wrap the
    `ListAgents`/`FindAgent` alive check **and** the `DiagnoseBuilder`
    guard in `if !restore { ... }` with a comment: a pane builder whose
    worktree was released is gone by definition -- its cwd is a deleted
    inode even after the path is recreated (§1). After `resolveBuilder`
    returns `res2`, copy the three restore fields from `res` onto it and
    assign `res = res2`. In the lock, the second refusal gains `&&
    !restore`. Return `res` in both the rebinding and planner-only paths.
  - `cmd/relay/main.go` `cmdBind`: right after the `BindResolved` error
    check, `if t := relay.RestoreText(res); t != "" { fmt.Println(t) }`.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay ./cmd/relay`
  and `go vet ./...`. Expected: PASS, including every pre-existing bind
  test.

- [ ] **Step 5: Mutation checks** (do not commit): (a) drop `&& !restore`
  from the pre-spawn `StateDone` refusal ->
  `TestRebindOnDoneWithRestoredWorktree` fails with the "is done" message;
  (b) drop the `Branch == ""` check -> `TestResumeRefusesRestoreWithoutBranch`
  fails (a checkout call is recorded with an empty branch). Revert each;
  re-run Step 4. Name both in your report.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/candidate.go internal/relay/bind.go internal/relay/text.go internal/relay/bind_test.go internal/relay/text_test.go cmd/relay/main.go
git commit -m "feat(bind): resume restores a missing worktree; rebind on DONE allowed after restore (#137)"
```

---

### Task 4: README and CLAUDE.md

**Files:**
- Modify: `README.md:436-439` (the "Cleaning up finished bindings" paragraph), `README.md:849-854` (the DONE bullet)
- Modify: `CLAUDE.md:36-42` (the "stops a *process*" sentence's neighbourhood: one added sentence, nothing removed)

- [ ] **Step 1: README cleanup paragraph.** Replace the sentence
  `` `relay done` stops relaying but removes nothing — the log is the record of what the planner actually told the builder. ``
  with:

  ```
  `relay done` stops relaying and, when the binding's worktree is clean and
  no round is open, removes the worktree so its branch can be checked out
  in the main repo (`removed worktree ... (branch relay/x is free to check
  out)`); a dirty tree or an open pane round is kept and `relay gc` retries
  when it is clean. The binding directory itself is never removed by `done`
  — the log is the record of what the planner actually told the builder.
  `relay bind --resume <name>` puts a removed worktree back on the same
  branch at the same path; a DONE binding may then be rebound with
  `--rebind`, since the old builder pane cannot work in the recreated
  directory (`bind` names it so you can close it).
  ```

- [ ] **Step 2: README DONE bullet.** In the `- **DONE**` bullet replace
  `The binding and its round log stay on disk (`relay log <name>` still
  works as an audit trail) until `relay unbind` removes them.` with
  `The binding and its round log stay on disk (`relay log <name>` still
  works as an audit trail) until `relay unbind` or `relay gc` removes them;
  a clean worktree is released at `done` so the branch is free to review.`

- [ ] **Step 3: CLAUDE.md.** In "Working with builders", directly after
  the bullet that ends `(an idle opencode builder is roughly 800 MB).`,
  add one bullet:

  ```
  - `relay done` releases a clean worktree (the branch survives) so you can
    `gh pr checkout` in the main repo without `gc`; a dirty tree or an open
    pane round is kept and `gc` retries. `relay bind --resume` restores a
    released worktree; rebind a DONE binding only after that restore.
  ```

  Do not edit the "exactly two places" / "exactly three" sentences.

- [ ] **Step 4: Run the full `make check` constituent set** from "Running
  commands". Expected: clean. Then `git diff --stat main..HEAD` must list
  only: `internal/git/types.go`, `internal/git/client.go`,
  `internal/git/client_test.go`, `internal/relay/herdr.go`,
  `internal/relay/fake_test.go`, `internal/relay/status.go`,
  `internal/relay/text.go`, `internal/relay/status_test.go`,
  `internal/relay/text_test.go`, `internal/relay/headless_test.go`,
  `internal/relay/reconcile_hooks_test.go`, `internal/relay/gc_test.go`,
  `internal/relay/candidate.go`, `internal/relay/bind.go`,
  `internal/relay/bind_test.go`, `cmd/relay/main.go`,
  `internal/pick/model.go`, `README.md`, `CLAUDE.md`.

- [ ] **Step 5: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: done releases the worktree; resume restores it (#137)"
```

---

## Report

Your report (`NNN-report.md`) states: each task's commit hash; the four
`make check` constituent commands and that each passed (the gofmt line
must exit 0 -- it now fails on unformatted files); the three mutations by
name and the test each one failed; the `git diff --stat` against the list
in Task 4 Step 4; and any place you stopped rather than improvised, with
what you found. Then create the `NNN-done` marker.
