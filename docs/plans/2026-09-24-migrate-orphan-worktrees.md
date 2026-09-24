# Plan: #385: `relevo migrate` also repairs worktrees that no binding records

Issue: #385. Spec context: `docs/specs/2026-09-23-rename-relevo-design.md` §3
("Worktrees live inside the state root").

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 1. Overview

`relevo migrate` moves a state root, and every git worktree under it moves too.
`(*runner).repairWorktrees` (`internal/migrate/migrate.go` ~507-565) then runs
`git worktree repair`, but only for worktrees a **binding record** points at. It
walks `store.New(root).List()` for each of `storeRoots(base)`.

A worktree directory whose binding is gone, or a verify consult's throwaway
worktree under `.worktrees/.verify/`, is never repaired. Its `.git` file keeps
naming the old root, so git refuses to work in it, and `migrate` still reports
success. This happened on contabo during the #292 cutover.

The fix is a second pass over worktree **directories**, after the binding pass.

## 2. Files (closed list)

```
internal/migrate/migrate.go        EDIT repairWorktrees: record repaired paths; call the orphan pass; merge counts and warnings
internal/migrate/orphans.go        NEW  orphanWorktrees (discovery) + adminRepo (the .git -> repo rule)
internal/migrate/orphans_test.go   NEW  unit tests for discovery and adminRepo
internal/migrate/migrate_test.go   EDIT Run-level orphan tests (§5)
internal/git/worktree_repair_test.go EDIT one real-git test (§5)
```

## 3. Data and contracts

**`func orphanWorktrees(base string) []string`**, in `orphans.go`:

- Scans these directories, where each one exists:
  - `base/.worktrees/*` and `base/.worktrees/.verify/*`;
  - for every directory `O` under `base/serve/bindings/`, `O/.worktrees/*` and
    `O/.worktrees/.verify/*`.
- Returns every entry that is a directory and holds a regular **file** named
  `.git`. That is how a linked worktree is marked; a `.git` directory is a full
  clone and is skipped.
- The result is sorted, with absolute cleaned paths.
- **Don't** use `storeRoots` for the top root here. It returns `base` only when
  `base` holds a binding, and a root whose bindings are all gone can still hold
  orphans.

**`func adminRepo(wt, from, to string) (repo, admin string, err error)`**:

1. Read `wt/.git`. It must be one line of the form `gitdir: <path>`; anything
   else is an error.
2. **Map the admin path onto the new root.** If `<path>` is `from` or starts with
   `from + "/"`, replace that prefix with `to`. `from` and `to` are the moved
   state root's old and new paths. In a dry run, and whenever `from == to`, no
   mapping happens.
3. **Derive the repo.** The mapped path must end in `/worktrees/<name>`; if it
   doesn't, return an error. `repo` is the part before `/worktrees/<name>`. If
   `repo` ends in `/.git`, strip that suffix so a non-bare repo is named by its
   worktree root. A bare `….git` repo keeps its name.
4. `admin` is the mapped path. If it doesn't exist on disk, return an error that
   names it.

**`repairWorktrees` changes:**

- The binding pass records every worktree it repaired, or counted in a dry run,
  in a `map[string]bool` keyed by `filepath.Clean`.
- After the binding pass, for each `wt` in `orphanWorktrees(base)` not already in
  that map:
  - `adminRepo(wt, r.opts.StateFrom, r.opts.StateTo)`. When `r.opts.DryRun`, pass
    `from == to`, because nothing has moved yet.
  - An error becomes a warning:
    `worktree <wt>: <err>; run git worktree repair yourself`.
  - Dry run: count it.
  - Otherwise `r.opts.Repair(ctx, repo, wt)`. An error becomes a warning in the
    existing format:
    `worktree <wt>: git -C <repo> worktree repair <wt> failed: <err>`.
    Success increments the same `repaired` counter.
- The step names, the final `switch`, and the step's text stay as they are.

## 5. Tests

`orphans_test.go`, all on `t.TempDir()`:

- **`orphanWorktrees`:**
  - finds `base/.worktrees/a` (with a `.git` file), `base/.worktrees/.verify/b-001`
    and `base/serve/bindings/o1/.worktrees/c`;
  - skips `base/.worktrees/full`, which has a `.git` **directory**, and
    `base/.worktrees/empty`, which has no `.git` at all;
  - works when `base` has no binding at all.
- **`adminRepo`** (table):
  - an admin path inside `from` is mapped to `to`, and `repo` is the bare
    `…/r.git`;
  - an admin path outside `from`, such as `/x/proj/.git/worktrees/n`, is not
    mapped, and `repo` is `/x/proj`;
  - a malformed `.git` file is an error;
  - an admin path with no `/worktrees/<n>` suffix is an error;
  - an admin dir missing on disk is an error.
- **Mutations** (each named test must fail):
  - drop the `/.git` strip;
  - drop the prefix mapping.

`migrate_test.go`, beside `TestRunHappyPath`, using the fake `Repair` recorder
the file already has:

- **`TestRunRepairsOrphanWorktree`:**
  - Set-up: the old state root holds no binding, `.worktrees/x` with a `.git`
    file whose `gitdir:` points into `<StateFrom>/serve/repos/o/r.git/worktrees/x`,
    and that admin dir created.
  - After `Run`: `Repair` was called once with (`<StateTo>/serve/repos/o/r.git`,
    `<StateTo>/.worktrees/x`), and the step says `repaired 1 worktree(s)`.
- **`TestRunOrphanNotRepairedTwice`:** a binding records `.worktrees/y`, which
  also has a `.git` file. `Repair` is called for `y` exactly once.
- **`TestRunOrphanDryRun`:** the same tree as the first test with `DryRun`.
  `Repair` is not called and the step counts it.
- **Mutation:** skip the orphan pass, and `TestRunRepairsOrphanWorktree` must fail.

`internal/git/worktree_repair_test.go`: add
**`TestWorktreeRepairAfterBareRepoAndWorktreeBothMove`**.

1. `git init --bare <tmp>/a/r.git`.
2. Seed it with one commit, through a temp clone and a push.
3. `git -C <tmp>/a/r.git worktree add <tmp>/a/wt main`.
4. `mv <tmp>/a <tmp>/b`.
5. `WorktreeRepair(ctx, <tmp>/b/r.git, <tmp>/b/wt)`.
6. `git -C <tmp>/b/wt status` must succeed.

This pins the claim that repairing from the *moved* bare repo is enough.

## 8. Working efficiently

Read `internal/migrate/migrate.go` ~220-260 and ~500-570,
`internal/migrate/detect.go` ~140-185 and ~250-262, and the helpers at the top of
`migrate_test.go`, once.

- **Focused:** `go test -count=1 ./internal/migrate/ ./internal/git/`,
  `sh scripts/check-name.sh`.
- **Once at the end:** `make check`.
- These tests use temp dirs and real `git` only. They spawn no harness and reach
  no network.

## 9. Steps

1. Write `orphans.go` and `orphans_test.go`. The focused tests must pass, and the
   two mutations must fail.
2. The `repairWorktrees` edit, with the three `migrate_test.go` tests and their
   mutation.
3. The real-git test.
4. `make check` must pass. One commit:
   `fix(migrate): repair worktrees no binding records -- orphans and .verify (#385)`.
   Don't push.

**Declared scope:** §2.

**Report:**
- per-step status;
- each mutation, with the named test that failed;
- `git diff --stat HEAD~1`;
- the result of `make check`.
