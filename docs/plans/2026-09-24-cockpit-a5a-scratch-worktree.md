# Cockpit A5a: the scratch worktree primitive

Spec: `docs/specs/2026-09-24-cockpit-design.md` §3.4 (the reader round's scratch
worktree) and D6. This round builds and tests the primitive only. Nothing calls it
yet; A5 wires it into reader rounds and the daemon sweep. Line numbers are from
`ff04f5d`.

**One round. If a step is impossible as written or contradicts what you find, stop and
report. Do not improvise.**

CI has no harness and no network, but it does have git: the `internal/git` worktree
tests already run real git in CI without skipping (`TestAddDetachedWorktree`,
`internal/git/client_test.go:1938`). The new real-git tests follow the same pattern.
Nothing here spawns a harness.

Do not touch `internal/ui/**`, `internal/candidate/**`, `internal/roles/**`,
`internal/policy/**` or `internal/config/**`: parallel rounds own them.

## 1. System overview

A reader round (D6) must run in a throwaway worktree that holds exactly what the
binding's tree holds right now, uncommitted work included: tracked edits (staged or
not), deletions, and untracked non-ignored files. When the round ends, the worktree is
thrown away, and nothing the reader did can reach the binding's tree.

The pieces already exist:

- `(*git.Client).SnapshotTree(ctx, dir)` (`internal/git/client.go:121`) writes the
  whole working state of `dir` into an unreferenced tree object. It uses a temp index
  and leaves the repo's own index and HEAD alone.
- `AddDetachedWorktree(ctx, dir, path, commit)` (`client.go:526`) makes a detached
  worktree.
- `RemoveWorktree(ctx, dir, path, force)` (`client.go:604`) removes one. It
  tolerates a path that is already gone.

What is missing is writing a tree into a worktree's files without committing. This
round adds `MaterializeTree`, which runs `git read-tree --reset -u <tree>` and then
`git reset -q`. The result is a worktree whose HEAD is the binding's HEAD and whose
files are the snapshot: every change is unstaged, and untracked files stay untracked.
On top of that it adds three relevo-level functions: `CreateScratch`, `RemoveScratch`
and `SweepScratch`.

## 2. File structure

```
internal/git/client.go            + MaterializeTree
internal/git/client_test.go       + two real-git tests
internal/store/store.go           + ScratchWorktreeDir, ScratchWorktreePath (beside VerifyWorktreePath, :696-702)
internal/relevo/runtime.go        relevo.Git interface (:30-105) + MaterializeTree
internal/relevo/fake_test.go      fakeGit (:102) + MaterializeTree (records calls, optional error)
internal/relevo/scratch.go        NEW  Scratch, CreateScratch, RemoveScratch, SweepScratch, ErrScratch
internal/relevo/scratch_test.go   NEW  fakeGit tests + one real-git test
internal/migrate/orphans.go       orphanWorktrees also scans .worktrees/.scratch/ (as it scans .verify/)
internal/migrate/orphans_test.go  + one case (if the file exists; otherwise add it to the orphan test that exists)
```

## 3. Data structures

```
// internal/relevo/scratch.go
type Scratch struct {
    Path string // <root>/.worktrees/.scratch/<binding>-NNN
    Head string // the binding's HEAD commit at creation
    Tree string // the snapshot tree written into it
}
var ErrScratch = errors.New("scratch worktree")   // every CreateScratch failure wraps it
```

Store paths, as a pair with `VerifyWorktreePath` (`store.go:700-702`):

```
func (s *Store) ScratchWorktreeDir() string                   // filepath.Join(s.WorktreeDir(), ".scratch")
func (s *Store) ScratchWorktreePath(name string, round int) string // filepath.Join(ScratchWorktreeDir(), fmt.Sprintf("%s-%03d", name, round))
```

`ValidName` (`store.go:133-154`) forbids `.` in binding names, so `.scratch` can never
collide with a binding's worktree. The dot prefix also keeps it out of `ListFiles` and
`importAll`, which skip dot-entries.

## 4. Contracts

### 4.1 `(*git.Client) MaterializeTree(ctx context.Context, dir, tree string) error`

- **Precondition:** `dir` is a worktree whose index matches its HEAD, such as a fresh
  `AddDetachedWorktree`. `tree` is a tree id in the same object store.
- **Runs, via `c.run`:**
  1. `git read-tree --reset -u <tree>` in `dir`.
  2. `git reset -q` in `dir`, a mixed reset to HEAD.
- **Postcondition:**
  - `dir`'s files equal `tree`: files missing from the tree are deleted, and every
    file in the tree is present with its content.
  - HEAD is unchanged. The index equals HEAD, so every difference is an unstaged
    change and files not in HEAD are untracked.
- **Errors:** those of `c.run` (`ErrNotRepo`, `ErrGitUnavailable`, or the wrapped git
  error with stderr). On an error, `dir` may be partly written. The caller removes it.
- Put it after `AddDetachedWorktree` in `client.go`, with a doc comment naming this
  plan's §1.

### 4.2 `relevo.Git` and `fakeGit`

Add `MaterializeTree(ctx context.Context, dir, tree string) error` to the interface in
`internal/relevo/runtime.go`, next to `AddDetachedWorktree` (:54). `*git.Client`
satisfies it.

`fakeGit` gets:
- `materializeCalls []struct{dir, tree string}`
- `materializeErr error`

It returns `materializeErr` and touches no disk, the same way its other methods do
(`fake_test.go:102-361`).

### 4.3 `internal/relevo/scratch.go`

```
func CreateScratch(ctx context.Context, rt Runtime, b store.Binding, round int) (Scratch, error)
```

1. Require `rt.Git != nil` and `b.CWD != ""`, else `fmt.Errorf("%w: binding %s has no git tree", ErrScratch, b.Name)`.
2. `path := rt.Store.ScratchWorktreePath(b.Name, round)`.
3. If `path` exists, it is a leftover from a crash. Call
   `rt.Git.RemoveWorktree(ctx, b.CWD, path, true)`, and if the directory is still
   there afterwards, `os.RemoveAll(path)`.
4. `os.MkdirAll(rt.Store.ScratchWorktreeDir(), 0o755)`.
5. `head := rt.Git.HeadCommit(ctx, b.CWD)`, then `tree := rt.Git.SnapshotTree(ctx, b.CWD)`.
   Take HEAD first, so a commit landing between the two calls can only add to the
   snapshot and never drop the new commit's changes.
6. `rt.Git.AddDetachedWorktree(ctx, b.CWD, path, head)`.
7. `rt.Git.MaterializeTree(ctx, path, tree)`. On an error, call
   `rt.Git.RemoveWorktree(ctx, b.CWD, path, true)` (ignoring its error, but
   `slog.Warn` it) and return the wrapped error.
8. Return `Scratch{path, head, tree}`.

Every error wraps `ErrScratch` and names the step:
`scratch worktree: <step>: <err>`, where step is one of `head`, `snapshot`, `add`,
`materialize` or `leftover`.

```
func RemoveScratch(ctx context.Context, rt Runtime, b store.Binding, round int) error
```

`rt.Git.RemoveWorktree(ctx, b.CWD, rt.Store.ScratchWorktreePath(b.Name, round), true)`.
An already-missing path is not an error, because `RemoveWorktree` prunes in that
case (`client.go:604-618`).

```
func SweepScratch(ctx context.Context, rt Runtime, keep func(binding string, round int) bool) ([]string, error)
```

1. List the entries of `ScratchWorktreeDir()`. A missing directory returns
   `nil, nil`.
2. For each directory entry, parse `<name>-<NNN>`: name is everything before the last
   `-`, and NNN is its digits. Skip entries that do not parse, with a `slog.Warn`.
3. If `keep(name, n)` is true, skip it.
4. Otherwise remove it:
   - If `rt.Store.Load(name)` finds the binding, use
     `rt.Git.RemoveWorktree(ctx, b.CWD, path, true)`.
   - If not, read the path's `.git` file. Take its `gitdir: <common>/worktrees/<id>`
     line, `os.RemoveAll(path)`, then `rt.Git.RemoveWorktree(ctx, <common>, path,
     true)`. The path is now missing, so that call only prunes the admin entry.
     Parse the gitdir line the way `internal/migrate/orphans.go` `adminRepo` (:80)
     does. Reuse it if it is exported or can be exported cheaply; otherwise copy its
     few lines.
5. Collect the removed paths and return them in sorted order.
6. Per-entry errors are collected and returned joined (`errors.Join`) after the whole
   sweep. One bad entry never stops the others.

`keep` is supplied by the caller (A5: "this binding has an open reader round N"). This
round only tests the function.

### 4.4 `internal/migrate/orphans.go`

`orphanWorktrees(base)` (:20) lists linked worktrees under `<root>/.worktrees/` and
`<root>/.worktrees/.verify/`. Add `<root>/.worktrees/.scratch/` the same way, so a
migration or repair sees scratch worktrees too. No other change.

## 5. Pseudocode

```
CreateScratch(b, round):
  path = ScratchWorktreePath(b.Name, round)
  if exists(path): RemoveWorktree(b.CWD, path, force); RemoveAll(path) if still there
  head = HeadCommit(b.CWD); tree = SnapshotTree(b.CWD)
  AddDetachedWorktree(b.CWD, path, head)
  if MaterializeTree(path, tree) fails: RemoveWorktree(b.CWD, path, force); return err
  return {path, head, tree}
```

## 6. Error handling

| case | result |
|---|---|
| Any `CreateScratch` step fails | A wrapped `ErrScratch` naming the step, and no worktree left behind. A5 turns this into NEEDS YOU; this round only returns it. |
| `RemoveScratch` on a missing path | nil. |
| `SweepScratch` per-entry failure | Collected; the sweep continues; `errors.Join` at the end. |
| The binding tree is not a git repo | `ErrScratch` wrapping `git.ErrNotRepo`, at step `head`. |

## 7. Tests

### `internal/git/client_test.go`

Use the existing helpers `runGit` (:16), `initRepo` (:1594) and `writeGitFile`
(:2015).

**`TestMaterializeTreeCarriesWorkingState`**

1. Commit `a.txt`, `b.txt`, `c.txt` and `.gitignore` (containing `*.log`).
2. Then, without committing:
   - edit `a.txt` (unstaged);
   - edit `b.txt` and `git add b.txt` (staged);
   - `rm c.txt`;
   - create `d.txt` (untracked);
   - create `e.log` (ignored).
3. Record the source's `status --porcelain` and the bytes of `<source>/.git/index`.
4. `SnapshotTree`, then `AddDetachedWorktree` at HEAD into `t.TempDir()/scratch`,
   then `MaterializeTree`.

Assert:
- **The scratch worktree:**
  - `a.txt` and `b.txt` have the edited contents;
  - `c.txt` is absent;
  - `d.txt` is present;
  - `e.log` is absent;
  - `rev-parse HEAD` equals the source HEAD;
  - `status --porcelain` is exactly ` M a.txt`, ` M b.txt`, ` D c.txt` and
    `?? d.txt`, sorted.
- **The source:**
  - `status --porcelain` is unchanged;
  - the index bytes are unchanged;
  - `branch --list` is unchanged.

**`TestScratchWritesNeverReachTheSource`**

1. Continue from the same setup.
2. Write `z.txt` in the scratch and edit `a.txt` there. The source is unchanged.
3. `RemoveWorktree(force=true)`. The scratch directory is gone, and the source's
   `worktree list --porcelain` does not list it.

### `internal/relevo/scratch_test.go`

**With `fakeGit`**
- `TestCreateScratchOrder`: the calls are HeadCommit, SnapshotTree,
  AddDetachedWorktree(path), MaterializeTree(path, tree), and the returned `Scratch`
  carries the fake's head and tree.
- `TestCreateScratchMaterializeFailureRemoves`: with `materializeErr` set, the error
  wraps `ErrScratch` and names `materialize`, and a `RemoveWorktree(path, force=true)`
  call was recorded.
- `TestCreateScratchRemovesLeftover`: create the path directory beforehand, and a
  `RemoveWorktree` call on it comes before `AddDetachedWorktree`.
- `TestScratchPathShape`: `ScratchWorktreePath("api", 7)` ends with
  `.worktrees/.scratch/api-007`.

**`TestScratchRealGit`**

Use real git through `git.NewClient("git", 0, 0)`, a `store.New(t.TempDir())` store,
and a repo made with a local `runGit` helper. Copy the pattern of
`internal/relevo/served_test.go:109`.

- `CreateScratch` carries a dirty edit.
- `RemoveScratch` removes it, and a second `RemoveScratch` returns nil.
- `SweepScratch` over three leftovers, where `keep` keeps one, removes exactly two and
  returns them sorted.

### `internal/migrate`

A worktree under `.worktrees/.scratch/` is listed by `orphanWorktrees`. Add the case to
the existing orphan test. If there is no orphan test, add
`TestOrphanWorktreesIncludesScratch` with the smallest real-git setup that the verify
case uses.

### Mutation check

Remove the `git reset -q` step from `MaterializeTree`. The porcelain assertion in
`TestMaterializeTreeCarriesWorkingState` must fail, because the changes show as
staged. Report it, then revert.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch independent reads and edits as parallel tool calls in one step.
- Read each file once at the lines named here.
- Make each file's changes in one edit call.
- Iterate on:

  ```
  go test ./internal/git/ -run 'Materialize|ScratchWrites'
  go test ./internal/relevo/ -run Scratch
  go test ./internal/migrate/...
  ```

- Run `make check` once at the end.

## 9. Ordered steps

**1. git.**
- Deliverable: `MaterializeTree` (§4.1) and its two tests.
- Verify: `go test ./internal/git/ -run 'Materialize|ScratchWrites|AddDetached'`.

**2. Store and interface.**
- Deliverable: `ScratchWorktreeDir` and `ScratchWorktreePath`; `relevo.Git` gains
  `MaterializeTree`; `fakeGit` implements it.
- Verify: `go build ./...` and `go vet ./internal/relevo/`.
- Depends on 1.

**3. `scratch.go`.**
- Deliverable: §4.3 and its tests.
- Verify: `go test ./internal/relevo/ -run Scratch`.
- Depends on 2.

**4. Migrate scan.**
- Deliverable: §4.4 and its test.
- Verify: `go test ./internal/migrate/...`.
- Depends on 2.

**5. Check and report.**
- Run the mutation check, then `make check`.
- Report:
  - the new functions with their line ranges;
  - the exact porcelain output the git test asserts;
  - `git diff --stat`, which must touch only the files in §2.
- Depends on 3 and 4.
