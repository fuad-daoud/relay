# Wave 2 chain J, step 2: status/ui rows -- attention-first everywhere, live +N/-M from the round baseline, quiet age on active rows, unread since viewed (#143)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (the planner runs it); run the gate commands in §7
exactly as written. Every command in the foreground; no sub-agents for
edits. No git fetch/rebase (this worktree has no origin).

## 1. System overview

Most of #143 exists piecemeal: `relay.SortRows(rows, attention)`
(`internal/relay/sort.go`) already orders NEEDS YOU -> HELD -> ACTIVE ->
PAUSED -> DONE, stale first, newest `Last.TS` first -- but only `relay ui`
calls it; `buildReport` (`status.go:300`) still sorts by name, so `status`,
`status --json` and the status line disagree with `ui`. `ui`'s `s` toggle
and its `ui.json` `sort` pref exist. `Branch` is on the row. #135 gave
`LastProgressAt`. What is missing: `status` on `SortRows`; a **live**
`+N/-M in F` from the round baseline while a round is open (numstat only,
tree vs worktree, cached for the poll interval); `quiet <age>` on ACTIVE
rows from `LastProgressAt`; and **unread**: a `.viewed` sidecar per binding
that `ui` stamps when a binding is opened and `relay diff`/`log`/`show`
stamp after printing (design question 2: sidecar, so `ui` stays read-only
of `bind.json`), with `●` when the newest report is newer than the stamp.
Design question 1: `--cwd` bindings show the live diff labelled
`(shared tree)`.

## 2. File structure

```
internal/relay/herdr.go            Git + DiffWorktreeStat(ctx, dir, tree string) (git.Stat, error)
internal/git/client.go             + DiffWorktreeStat: git diff --numstat <tree> (tree vs working tree), parsed like DiffTrees' numstat
internal/git/client_test.go        + TestDiffWorktreeStat
internal/relay/fake_test.go        fakeGit + worktreeStat git.Stat, worktreeStatErr
internal/store/store.go            + ViewedPath(name) <Dir(name)>/.viewed; MarkViewed(name, at) error (touch/utimes); ViewedAt(name) (time.Time, bool)
internal/store/store_test.go       + round-trip test
internal/relay/status.go           buildReport: SortRows(rows, true); row.Live *LiveDiff, row.QuietFor, row.Unread; RenderStatus prints them
internal/relay/livestat.go         + liveStat(ctx, rt, b) *LiveDiff with a 5 s per-binding cache (package-level, keyed by name+RoundBaselineTree)
internal/relay/status_test.go      + tests
internal/relay/sort_test.go        + TestStatusUsesAttentionOrder (or in status_test)
internal/ui/rail.go                facts(): live "+N −M in F" first (the reserved slot); unreadSlot -> "● " when b.Unread; ACTIVE whatAge appends " · quiet <age>"
internal/ui/model.go               pointDetailAt: stamp viewed (through Source; plannerSource -> rt.Store.MarkViewed; serverSource -> no-op)
internal/ui/source.go              Source + MarkViewed(name string)
internal/ui/*_test.go              rail/golden/model tests
cmd/relay/main.go                  cmdDiff / cmdLog (non-follow and follow end) / show.go cmdShow: MarkViewed after a successful print
README.md                          status row fields + status --json keys; the .viewed sidecar
docs/plans/2026-09-21-w2j2-status-rows.md   copy of this plan
```

## 3. Data structures

```
// internal/relay/status.go
type LiveDiff struct {
    Files, Added, Removed int  `json:"files"`, `json:"added"`, `json:"removed"`
    Shared bool `json:"shared,omitempty"`   // --cwd binding: the planner's own tree
}
BindingStatus.Live     *LiveDiff `json:"live,omitempty"`      // open round with a baseline only; nil otherwise or when git failed
BindingStatus.QuietFor string    `json:"quiet_for,omitempty"` // ACTIVE rows with an open round: AgeText(now - LastProgressAt); "" before the first sample
BindingStatus.Unread   bool      `json:"unread"`              // newest KindReport entry newer than the .viewed stamp (or no stamp and a report exists)
```

## 4. Interfaces

```
// internal/git/client.go
func (c *Client) DiffWorktreeStat(ctx, dir, tree string) (Stat, error)   // git diff --numstat <tree>; empty -> zero Stat, nil

// internal/relay/livestat.go
func liveStat(ctx context.Context, rt Runtime, b store.Binding) *LiveDiff
    // nil when rt.Git == nil || b.RoundStartedAt.IsZero() || b.RoundBaselineTree == "" || b.CWD == ""
    // cache: key name+"@"+RoundBaselineTree -> {at, stat}; reuse when rt.Now()-at < 5s (ui polls at 2 s; status is one-shot)
    // stat, err := rt.Git.DiffWorktreeStat(ctx, b.CWD, b.RoundBaselineTree); err -> nil (never an error in status)
    // Shared = b.Worktree == ""

// status.go statusRow: row.Live = liveStat(...); row.QuietFor = AgeText(now-LastProgressAt) when Display=="ACTIVE" && !RoundStartedAt.IsZero() && !LastProgressAt.IsZero()
//                     row.Unread: newest KindReport TS vs rt.Store.ViewedAt(name)
// buildReport: rows = SortRows(rows, true)   (drop the name sort; the ui's name mode still calls SortRows(rows, false))
// RenderStatus row line: after the state word and builder status: "  +120/-30 in 6" (+ " (shared tree)") when Live; "  quiet 12s" when QuietFor; "  ●new" when Unread
// store: MarkViewed writes/touches <dir>/.viewed with mtime = at (os.Chtimes after create); ViewedAt stats it
// ui: pointDetailAt -> src.MarkViewed(name) (plannerSource: rt.Store.MarkViewed(name, time.Now()); ignore errors); rail card: "● " in unreadSlot when Unread; facts(): live first "+120 −30 in 6" (unicode minus as the diff tab uses); ACTIVE whatAge: " · quiet 12s"
// cmd: cmdDiff after writing the patch: rt.Store.MarkViewed(target, time.Now()); cmdLog when not --follow after printing, and on follow exit; cmdShow after printing (live bindings only)
```

## 5. Pseudocode

Covered by §4.

## 6. Error handling

- Every new row field degrades to empty/nil/false on any error; `Status`
  never fails because of them.
- `.viewed` write failures are ignored (a read verb must not fail on a
  stamp).

## 7. Ordered implementation steps

Commit prefix: exactly **one** `feat(status):` commit for the code (squash
step commits, or `chore:`/`test:` per step); the plan copy may be its own
`chore(plans):` commit.

### Task 1 -- git, store, fake

**Tests first:** `TestDiffWorktreeStat` (temp repo: tree = `write-tree` of HEAD; edit a tracked file, add an untracked one, `git add` it; stat shows the added/removed counts; against the same tree with a clean worktree -> zero). `TestViewedRoundTrip` (`MarkViewed` then `ViewedAt` within a second; no stamp -> ok false).

**Verify:** `go test -count=1 ./internal/git/ ./internal/store/ ./internal/relay/`.

### Task 2 -- status rows

**Tests first** (`status_test.go`, fixtures as `TestStatusHeadlessStalledLabel`/the three-row test from #135):
- `TestStatusRowsAreAttentionOrdered`: three bindings named a(ACTIVE) b(NEEDS YOU) c(DONE) -> `Status` returns b, a, c; `status --json` (marshal the report) in that order. **Mutation check:** restore the name sort and this fails.
- `TestStatusLiveDiffWhileRoundOpen`: `fakeGit.worktreeStat = {Files:6, Added:120, Removed:30}`, binding with `RoundStartedAt` and `RoundBaselineTree` set -> `Live == {6,120,30,false}`; a `--cwd` binding (Worktree "") -> `Shared`; closed round -> nil; `RenderStatus` contains `+120/-30 in 6`.
- `TestStatusLiveDiffCached`: two `Status` calls within 5 s (fake clock) -> one `DiffWorktreeStat` call; after 6 s -> two.
- `TestStatusQuietForOnActive`: `Progress.TreeAt = now-12s` -> `QuietFor == AgeText(12s)`; NEEDS YOU row -> "".
- `TestStatusUnreadUntilViewed`: a report entry -> `Unread`; `MarkViewed(now)` -> false; a newer report -> true again.

**Then** the code. **Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 3 -- ui, cmd, README, plan copy, gate

- ui tests: extend `TestRailLinesGroupsAndTags` for the `●` slot and a live-diff fact; a model test that `pointDetailAt` calls `MarkViewed` on the fake source (add the method to the ui fake source). Goldens: regenerate only if they change and say so.
- cmd: the three stamp sites; `TestDiffCommand` (`main_test.go:191`) gains an assertion that `.viewed` exists after `diff` (store-only; reaches no herdr).
- README: the row line and the four json keys; the `.viewed` sidecar (what writes it, that `ui` never writes `bind.json`).

Copy the plan file to `docs/plans/2026-09-21-w2j2-status-rows.md` and commit.

Gate, in the foreground, stop at the first failure:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Final commit message: `feat(status): attention-first rows everywhere, live +N/-M from the round baseline, quiet age on active rows, unread since viewed (#143)`

## Report

Per task: what was done, test names, verify output, the mutation check's
failing test, whether goldens changed. Commit shas (one `feat:`). If any
step was impossible as written, say which and stop there.
