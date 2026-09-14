# Round commit facts: round close records whether the builder committed

**Issue:** #130.
**Depends on:** nothing open. Builds on the completion marker (#117) and
diff capture (`CaptureRoundDiff`).
**Consumed by (later):** #136 `land`, #137 `pause`, #145 `add --branch`,
which #131 orders behind this one because they key on `Branch`, `Base`
and the commit facts.
**Status:** draft; plan at `docs/plans/2026-09-14-round-commit-facts.md`.

## 1. System overview

At round close, `queueReport` snapshots the worktree as a tree and diffs it
against `RoundBaselineTree`. The planner is told `+120/-30 in 6 files`
whether the builder committed three times or left every change
uncommitted. `relay unbind` and `gc` already keep a dirty worktree rather
than delete it (`worktreeTeardown`), so the work is never lost by relay --
but the planner cannot tell, without opening the pane, whether the branch
holds the round's work or the worktree does.

This design records two facts at round close and shows them everywhere the
round's diff is shown:

- **commits** -- how many commits the round added on the builder's branch,
  counted from the HEAD the round started at;
- **tree** -- whether the worktree was clean or dirty when the marker
  appeared.

They are recorded on the existing `diff` log entry, appended to the `Diff:`
line the planner receives, and surfaced as `dirty` on a `relay status` row
whose most recent close left the tree dirty. Alongside them, `add` and
`fork` persist the branch and base commit they already compute, so the
close line can name the branch and later verbs have the facts they need
without re-deriving them from git.

Nothing is refused. The marker still closes the round; the prompt footer
is unchanged (it already says "after every edit, test and commit"); a
plan that wants a commit says so.

### Scope boundary

Three new `Binding` fields, one new `Git` method, one new capture function,
two new `LogEntry` fields, one clause on two existing render functions, and
one word on a status row. No change to `worktreeTeardown`, to the nudge,
fingerprint or scrape paths (so no `make e2e`), to `statusline`, to the
prompt, or to `relay log`'s format (the facts are already inside `Note`).

Out of scope, each with its own issue: refusing to close on a dirty tree;
live `+N/-M` or dirty while the round runs (#143); `relay land` (#136);
naming a branch for `--cwd` bindings by reading git.

## 2. File structure

```
internal/store/types.go           Binding: Branch, Base, RoundBaselineHead
internal/store/log.go             LogEntry: Commits, Tree
internal/git/client.go            RevListCount (new)
internal/git/client_test.go       RevListCount against a real repo
internal/relay/herdr.go           Git interface: RevListCount
internal/relay/capture.go         CaptureBaseline returns (tree, head); CommitResult, CommitFacts (new);
                                  DiffSummary and DiffLine take the commit facts and the branch
internal/relay/capture_test.go    tests in §7
internal/relay/send.go            stores RoundBaselineHead
internal/relay/send_test.go
internal/relay/reconcile.go       queueReport: CommitFacts, fields on the diff entry, clears RoundBaselineHead
internal/relay/reconcile_test.go
internal/relay/add.go             persists Branch and Base
internal/relay/add_test.go
internal/relay/fork.go            persists Branch and Base
internal/relay/fork_test.go
internal/relay/status.go          BindingStatus.LastClose; "dirty" on the row
internal/relay/status_test.go
internal/relay/fake_test.go       fakeGit: RevListCount scripting
```

## 3. Data structures and type definitions

### 3.1 `store.Binding` (three fields added, all `omitempty`)

| field | type | meaning |
|---|---|---|
| `Branch` | `string` `json:"branch,omitempty"` | the branch relay created for this binding's worktree (`relay/<name>`), written by `add` and `fork`. Empty for a `--cwd` binding, an adopted `bind`, and every `bind.json` written before this field existed. Provenance and display only: nothing in this design decides on it. |
| `Base` | `string` `json:"base,omitempty"` | the commit the worktree was cut at -- the `base` `add`/`fork` already pass to `AddWorktree`. Same emptiness rule as `Branch`. Not read by this design; persisted for #136/#137/#145. |
| `RoundBaselineHead` | `string` `json:"round_baseline_head,omitempty"` | the commit HEAD pointed at when the CURRENT round was sent. Written by `Send` with `RoundBaselineTree`, cleared by `queueReport` with it. Empty means no commit count is possible for this round: a non-git tree, an unborn HEAD, git unavailable, or a binding whose round was sent before the field existed. |

`Branch` and `Base` are set once at creation and never rewritten. A
`bind --resume` reloads them with the rest of the binding.

### 3.2 `store.LogEntry` (two fields added, `omitempty`, `diff` entries only)

| field | type | meaning |
|---|---|---|
| `Commits` | `int` `json:"commits,omitempty"` | commits reachable from HEAD at close that were not reachable from `RoundBaselineHead`. Meaningful only when `Tree != ""`. |
| `Tree` | `string` `json:"tree,omitempty"` | `"clean"`, `"dirty"`, or `""` when the facts are unknown. `Tree` is the discriminator: a diff entry with `Tree == ""` carries no commit facts, and `Commits` is then 0 and meaningless. |

One string discriminator rather than two pointers: the two facts are
captured together and are known or unknown together (§4.3), and a JSON
reader can test one field.

Entries written before this change decode with `Tree == ""`.

### 3.3 `relay.CommitResult` (new, `capture.go`)

```
type CommitResult struct {
    Known   bool   // both facts were captured
    Commits int    // rev-list --count RoundBaselineHead..HEAD; 0 when Known is false
    Dirty   bool   // uncommitted or untracked changes; false when Known is false
    Reason  string // why Known is false; "" when it is true
}
```

`Known` is all-or-nothing: if either git call fails, `Known` is false and
`Reason` names the call that failed. A half-known result ("2 commits,
tree unknown") would make every reader branch three ways for nothing.

### 3.4 `relay.CloseInfo` (new, `status.go`)

```
type CloseInfo struct {
    Round   int    `json:"round"`   // the round the diff entry closed
    Commits int    `json:"commits"`
    Tree    string `json:"tree"`    // "clean" | "dirty" | "" (unknown)
}
```

`BindingStatus` gains two fields:

- `LastClose *CloseInfo` `json:"last_close,omitempty"`, taken from the
  newest `KindDiff` entry in the log; nil when the log has no diff entry.
- `Dirty bool` `json:"dirty"`, the rendered rule already decided:
  `LastClose != nil && LastClose.Tree == "dirty" && b.RoundStartedAt.IsZero()`.
  `RenderStatus` prints it; `statusRow` computes it, so the rule is one
  pure decision with one owner.

### 3.5 Rendered text

**`DiffSummary` (the diff entry's `Note`)** gains one clause after the
existing summary, separated by `"; "`:

| facts | clause |
|---|---|
| known, N ≥ 1, clean | `3 commits, clean` (`1 commit` singular) |
| known, N ≥ 1, dirty | `3 commits, dirty` |
| known, 0, dirty | `no commits, dirty` |
| known, 0, clean | `no commits, clean` |
| unknown | `commits unknown (<reason>)` |

The clause is appended in every case except two: an empty diff (`no
changes`), where there is nothing to commit and the clause would be noise;
and unknown facts with an empty `Reason` (git off, or not a repository),
where the diff is equally silent. The `unavailable` summary keeps the
clause: the diff and the facts fail independently. The same two
exceptions apply to `DiffLine` below.

Examples: `6 files, +120 -30; 3 commits, clean` ·
`truncated; no commits, dirty` · `unavailable: no baseline; commits
unknown (no baseline)` · `no changes`.

**`DiffLine` (the payload line the planner is typed)** gains one clause
after ` -- `:

| facts | clause |
|---|---|
| known, N ≥ 1, clean | `3 commits on relay/api-auth, tree clean` |
| known, N ≥ 1, dirty | `3 commits on relay/api-auth, tree dirty` |
| known, 0, dirty | `no commits; changes are uncommitted in the worktree` |
| known, 0, clean | `no commits, tree clean` |
| unknown | `commits unknown (<reason>)` |

`on <branch>` appears only when `b.Branch != ""`; a `--cwd` binding's line
reads `3 commits, tree clean`. `Diff: no file changes` gains nothing. The
empty line (git off, or not a repository) stays empty: a binding that has
no diff has no facts either.

Examples:

```
Diff: /…/007-diff.patch (6 files, +120 -30) -- 3 commits on relay/api-auth, tree clean
Diff: /…/007-diff.patch (6 files, +120 -30) -- no commits; changes are uncommitted in the worktree
Diff: unavailable (snapshot: fatal: …) -- 2 commits on relay/api-auth, tree clean
Diff: no file changes
```

**`RenderStatus` row.** The header line
(`name cwd workspace round N <display>`) gains ` dirty` after the display
word (before ` +Nc`) when `b.Dirty` is set -- the newest close left the
tree dirty and no newer round has been sent (`RoundStartedAt.IsZero()`). Once a new
round is sent the builder is working and a dirty tree is the expected
state, so the word would say nothing; a DONE binding keeps it, which is
exactly the "done, but the work is one `rm -rf` from gone" case the issue
names.

## 4. Interface definitions and component contracts

### 4.1 `Git.RevListCount` (new, `internal/git`, added to `relay.Git`)

Single responsibility: count commits in a range.

```
func (c *Client) RevListCount(ctx context.Context, dir, from, to string) (int, error)
```

Runs `git rev-list --count <from>..<to>` in `dir`.
Preconditions: `from` and `to` resolve in `dir`'s repository.
Postconditions: the number of commits reachable from `to` and not from
`from`; 0 when they are the same commit.
Errors: `ErrNotRepo`, `ErrGitUnavailable`, or a wrapped git failure
(including an unresolvable ref -- a rewritten or garbage-collected
baseline).

### 4.2 `CaptureBaseline` (existing, return type changes)

```
func CaptureBaseline(ctx context.Context, rt Runtime, b store.Binding) (tree, head string)
```

Contract as today for `tree`. `head` is `rt.Git.HeadCommit(ctx, b.CWD)`
or `""` on any error, captured only when `tree != ""` (a tree that cannot
be snapshotted has no useful HEAD either, and the round would then report
`no baseline` for both). Still never errors.

### 4.3 `CommitFacts` (new, `capture.go`)

Single responsibility: capture the two commit facts for a round that is
closing, without ever failing the close.

```
func CommitFacts(ctx context.Context, rt Runtime, b store.Binding) CommitResult
```

Preconditions: none.
Postconditions:

| condition | result |
|---|---|
| `rt.Git == nil` | `Known: false`, `Reason: ""` |
| `b.RoundBaselineHead == ""` | `Known: false`, `Reason: "no baseline"` |
| `HeadCommit` fails | `Known: false`, `Reason: "head: " + brief(err)` |
| `RevListCount` fails | `Known: false`, `Reason: "rev-list: " + brief(err)` |
| `Dirty` fails | `Known: false`, `Reason: "dirty check: " + brief(err)` |
| all succeed | `Known: true`, `Commits`, `Dirty` |

`ErrNotRepo` from any call yields `Known: false, Reason: ""`, matching
`CaptureRoundDiff`'s treatment: a non-repository is not a failure worth a
sentence. The call order is `HeadCommit`, `RevListCount`, `Dirty`, and the
first failure stops the sequence. Runs under the state lock, like
`CaptureRoundDiff`; three cheap git calls against a worktree.

`HeadCommit` is called so the count is `RoundBaselineHead..<that HEAD>`
rather than the symbolic `HEAD`: the same instant, and a failure is
attributed to the right step.

### 4.4 `DiffSummary` and `DiffLine` (existing, signatures change)

```
func DiffSummary(res DiffResult, facts CommitResult) string
func DiffLine(res DiffResult, facts CommitResult, branch string) string
```

Rendering per §3.5. Pure; the only callers are `queueReport` and tests.

### 4.5 `queueReport` (existing, `reconcile.go`)

Inside the existing `if !HasEntry(... KindDiff)` block, after
`CaptureRoundDiff`:

- `facts := CommitFacts(ctx, rt, b)`;
- the diff entry gets `Note: DiffSummary(result, facts)` and, when
  `facts.Known`, `Commits: facts.Commits` and `Tree: "clean"|"dirty"`;
- the payload line comes from `DiffLine(result, facts, b.Branch)`.

After the round advances, `b.RoundBaselineHead = ""` next to
`b.RoundBaselineTree = ""`.

### 4.6 `Send` (existing)

`baseline, head := CaptureBaseline(ctx, rt, hint)`; inside the locked
update, `b.RoundBaselineHead = head` next to `b.RoundBaselineTree =
baseline`.

### 4.7 `Add` and `Fork` (existing)

The `store.Binding` each constructs gains `Branch: branch, Base: base` --
the local variables already in scope, both `""` on the `--cwd` path.
`AddResult`/`ForkResult` and their stdout are unchanged.

### 4.8 `statusRow` and `RenderStatus` (existing)

`statusRow` walks `entries` newest-first for the first `KindDiff` and fills
`LastClose` from it (the same walk `LastPayload` already does). `RenderStatus`
prints ` dirty` per §3.5.

## 5. High-level pseudocode

```
CaptureBaseline(ctx, rt, b) (tree, head):
    if rt.Git == nil or b.CWD == "": return "", ""
    tree, err := SnapshotTree(b.CWD); if err: return "", ""
    head, err := HeadCommit(b.CWD);   if err: return tree, ""
    return tree, head

CommitFacts(ctx, rt, b) CommitResult:
    if rt.Git == nil:               return {Known: false}
    if b.RoundBaselineHead == "":   return {Known: false, Reason: "no baseline"}
    head, err := HeadCommit(b.CWD)
        NotRepo -> {Known: false}; err -> {Known: false, Reason: "head: " + brief}
    n, err := RevListCount(b.CWD, b.RoundBaselineHead, head)
        NotRepo -> {Known: false}; err -> {Known: false, Reason: "rev-list: " + brief}
    dirty, err := Dirty(b.CWD)
        NotRepo -> {Known: false}; err -> {Known: false, Reason: "dirty check: " + brief}
    return {Known: true, Commits: n, Dirty: dirty}

queueReport(...):
    if no diff entry for this round:
        result := CaptureRoundDiff(ctx, rt, b)
        facts  := CommitFacts(ctx, rt, b)
        entry  := diff entry with Note = DiffSummary(result, facts)
        if facts.Known: entry.Commits = facts.Commits; entry.Tree = "clean" or "dirty"
        append entry
        if line := DiffLine(result, facts, b.Branch); line != "": payload += "\n" + line
    ...queue report, advance round as today...
    b.RoundBaselineTree = ""; b.RoundBaselineHead = ""

Send:
    tree, head := CaptureBaseline(ctx, rt, hint)
    ...under lock...
    b.RoundBaselineTree = tree; b.RoundBaselineHead = head

statusRow:
    for e in entries newest-first: if e.Kind == KindDiff:
        row.LastClose = {Round: e.Round, Commits: e.Commits, Tree: e.Tree}; break
    row.Dirty = row.LastClose != nil && row.LastClose.Tree == "dirty" && b.RoundStartedAt.IsZero()

RenderStatus row:
    ...name cwd workspace round N display...
    if b.Dirty: write " dirty"
    if b.Consults > 0: write " +Nc"
```

## 6. Error handling strategy

| error | handling |
|---|---|
| any git failure at close | lands in `CommitResult.Reason`; the round closes, the note and line say `commits unknown (…)` |
| `HeadCommit` fails at `send` (unborn HEAD, git missing) | `RoundBaselineHead` empty; that round reports `commits unknown (no baseline)`; `send` succeeds as today |
| not a repository | facts unknown with empty reason; no clause on the line, matching the diff's silence |
| baseline head no longer resolves (rebased away, gc'd) | `rev-list` fails; unknown with the git message; never a wrong count |
| old `bind.json` | new fields empty; the first round after upgrade reports `no baseline`, the next is normal |
| old `log.jsonl` | `Tree == ""`; status shows no `dirty`; nothing else changes |

No new error type. The facts are informational, and the rule
`CaptureRoundDiff` states -- a round advance must not be blocked by a
failed capture -- applies unchanged.

## 7. Ordered implementation steps

1. **`RevListCount`** in `internal/git` with a real-repo test: 0 for
   `HEAD..HEAD`; N after N commits; error on an unresolvable ref. Add it
   to `relay.Git` and to `fakeGit` (`revListCount int`, `revListErr
   error`, `revListCalls int`, `lastRevListFrom/To string`).
2. **Store fields** (`Branch`, `Base`, `RoundBaselineHead`, `Commits`,
   `Tree`) with round-trip tests in `internal/store` showing `omitempty`
   keeps an unchanged binding and entry byte-identical.
3. **`CaptureBaseline` returns `(tree, head)`; `Send` stores
   `RoundBaselineHead`.** Tests: head set from `fakeGit.headCommitID`;
   empty when `SnapshotTree` fails; empty when `HeadCommit` fails while
   the tree is still set.
4. **`CommitFacts`** with one test per §4.3 row, asserting `Known`,
   `Reason` prefix, and that the sequence stops at the first failure
   (`dirtyCalls == 0` when rev-list fails).
5. **`DiffSummary` / `DiffLine`** with the §3.5 table as cases, with and
   without a branch, including `no changes` gaining nothing and
   `unavailable` keeping the clause.
6. **`queueReport`**: facts on the diff entry, clause on the payload,
   `RoundBaselineHead` cleared. Mutations named in the plan: drop the
   `Dirty` call -> the "none+dirty" case fails; drop the clear -> the
   round-advance test fails.
7. **`Add` / `Fork` persist `Branch` and `Base`**; tests load the stored
   binding and assert both, and assert both empty on the `--cwd` path.
8. **`status`**: `LastClose` from the newest diff entry; `Dirty` true
   only when `Tree == "dirty"` and `RoundStartedAt` is zero (false once a
   round is sent); ` dirty` rendered only from `Dirty`; nil and no word
   for a log with no diff entry or a pre-field entry.
9. **`make check`**; `git diff --stat` against §2.
