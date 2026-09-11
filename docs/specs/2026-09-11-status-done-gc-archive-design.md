# `relay status` hides DONE; `relay gc` archives by default

**Issue:** #56

## 1. System overview

Finished bindings accumulate until someone runs `relay gc`, and nothing in
relay says so: `relay status` renders every DONE row above the one live
binding, and `gc` is discoverable only from `relay help`. Executing #36 left
21 DONE rows over one live one; every status check went through `grep`.

This design changes two things and refuses a third.

1. `relay status` (and `relay watch`, which is status on a timer) hides DONE
   bindings by default and prints one footer line saying how many are hidden
   and that `relay gc` clears them. `--all` shows them; `--name` finds one by
   name regardless. The same rule applies to `--json`, with the hidden count
   carried as a field, so the two output formats never disagree about what
   the command shows.
2. `relay gc` archives by default and deletes only on `--delete`. Every other
   destruction decision in relay keeps by default -- `worktreeTeardown`
   refuses a dirty tree, branches are never removed, panes close only in
   `reap` -- and `gc` was the one exception. Archiving is cheap (21 bindings
   compressed to 624 K) and the round logs are the only record of how a
   feature was actually built.

Refused, per the issue: any automatic or scheduled cleanup. Deciding a
human's work directory is disposable is a judgement, and scheduling is a
stated non-goal. `relay done` keeping the record stays as it is.

One adjacent defect is fixed because the same function is touched: when a
binding's worktree directory is already gone, `worktreeTeardown` hands the
missing path to git and reports `dirty check failed: git binary unavailable:
chdir …: no such file or directory` -- a misdiagnosis produced by the git
client classifying a `chdir` ENOENT as a missing binary. The teardown now
stats the directory first and reports it as already gone.

### Scope boundary

In scope: the status filter and footer, the `--all` flag on `status` and
`watch`, `Report.DoneHidden`, the `gc` default flip and `--delete`, the
already-gone worktree outcome, README.

Out of scope, unchanged:

- `relay done` and `relay unbind` semantics. `unbind --archive` keeps its
  current opt-in shape: `unbind` is "forget it", and the issue leaves it.
- `relay ui`'s list (#18). It calls `relay.Status` directly and can adopt
  `HideDone` later; this design does not touch `internal/ui`.
- Archive retention or pruning.
- The git client's error classification. The fix here is in the caller,
  which knows the path it is about to hand over.

## 2. File structure

```
internal/relay/status.go          HideDone; Report.DoneHidden; RenderStatus footer
internal/relay/status_test.go     filter and footer tests
internal/relay/gc.go              GCOptions.Delete replaces Archive; default archives
internal/relay/gc_test.go         default-archives / --delete tests
internal/relay/bind.go            worktreeTeardown: already-gone outcome; worktreeOutcome.Gone;
                                  UnbindResult.WorktreeGone
internal/relay/gc_test.go         already-gone worktree test (gc path)
cmd/relay/main.go                 status/watch --all; gc --delete, --archive no-op; output lines;
                                  done hint
cmd/relay/main_test.go            flag parsing tests if the file already covers status/gc flags
README.md                         gc section; status section mentions --all and the footer
```

## 3. Data structures and type definitions

### 3.1 `relay.Report` (modified)

```
type Report struct {
    Bindings   []BindingStatus `json:"bindings"`
    DoneHidden int             `json:"done_hidden,omitempty"`   // DONE rows HideDone removed
}
```

`DoneHidden` is zero, and absent from JSON, when nothing was filtered --
including when `--all` is given -- so a consumer that never learned the field
sees the same document it always did whenever nothing is hidden.

### 3.2 `relay.GCOptions` (modified)

```
type GCOptions struct {
    Delete bool   // remove each finished binding's directory instead of archiving it
    DryRun bool
}
```

`Archive` is removed from the struct. The CLI keeps accepting `--archive` as
a no-op so a script written against the old default does not break; it is
the flag's *meaning* that was backwards, and a no-op is the honest
compatibility shape for "this is now what happens anyway".

### 3.3 `relay.GCResult` (modified)

```
type GCResult struct {
    Name            string `json:"name"`
    CWD             string `json:"cwd"`
    Rounds          int    `json:"rounds"`
    ArchivedTo      string `json:"archived_to,omitempty"`
    Deleted         bool   `json:"deleted"`
    WorktreeRemoved string `json:"worktree_removed,omitempty"`
    WorktreeKept    string `json:"worktree_kept,omitempty"`
    KeptReason      string `json:"kept_reason,omitempty"`
    WorktreeGone    string `json:"worktree_gone,omitempty"`   // recorded worktree whose directory no longer exists
}
```

### 3.4 `relay.worktreeOutcome` and `relay.UnbindResult` (modified)

Both gain `Gone string`, the recorded worktree path when its directory does
not exist. Exactly one of `Removed`, `Kept`, `Gone` is set, or none when the
binding never had a worktree.

## 4. Interface definitions and component contracts

### 4.1 `relay.HideDone` (new, `status.go`)

```
func HideDone(r Report) Report
```

Pure. Returns a copy of `r` whose `Bindings` excludes every row with
`State == store.StateDone`, with `DoneHidden` set to the number excluded.
Order of the remaining rows is preserved. Does not touch the input.

It lives in `relay`, not `cmd/relay`, so `internal/ui` can call it when #18
is picked up.

### 4.2 `relay.RenderStatus` (modified)

- When `Bindings` is empty and `DoneHidden > 0`: no "no bindings" line;
  render only the footer. "no bindings" would be false.
- When `Bindings` is empty and `DoneHidden == 0`: `no bindings\n`, as today.
- After the rows, when `DoneHidden > 0`, one footer line:

  ```
  3 done · relay gc to clear
  ```

  Singular `1 done`. Nothing about disk: `gc` frees disk only for
  relay-created worktrees, and the footer must not overpromise.

### 4.3 `cmd/relay` `status` and `watch` (modified)

Flags: `--all` (bool) on both. `--name` unchanged. Rule, applied after
`filterReport`:

| `--name` | `--all` | result |
| --- | --- | --- |
| set | any | that binding, DONE or not; `DoneHidden` stays 0 |
| unset | false | `HideDone(rep)` |
| unset | true | `rep` untouched |

`--json` encodes whatever that rule produced. The `status -h` text for
`--all` reads: `include bindings marked DONE (hidden by default; relay gc
clears them)`.

### 4.4 `relay.GC` (modified)

Behaviour table:

| `Delete` | `DryRun` | per DONE binding |
| --- | --- | --- |
| false | false | `tx.Archive`; `ArchivedTo` set |
| true | false | `tx.Delete`; `Deleted` true |
| any | true | nothing changes; result reports the worktree outcome only |

The worktree teardown runs in every case, as today, and its outcome is
reported as `WorktreeRemoved`, `WorktreeKept`+`KeptReason`, or
`WorktreeGone`.

### 4.5 `cmd/relay` `gc` (modified)

Flags: `--delete` (bool), `--dry-run` (unchanged), `--archive` (bool,
accepted, ignored; help text: `no-op; archiving is now the default`).

Output per binding keeps the current verbs (`archived`, `deleted`, `would
clear`) and adds, for `WorktreeGone`, the line
`            worktree <path> was already gone`. The dry-run form appends
`(worktree <path> already gone)`.

When nothing is done: `no finished bindings to clear`, unchanged.

### 4.6 `relay.worktreeTeardown` (modified)

Before `rt.Git.Dirty`, `os.Stat(b.Worktree)`. On `os.ErrNotExist` return
`worktreeOutcome{Gone: b.Worktree}` without calling git. Any other stat
error falls through to the existing path, which will report it as a failed
dirty check -- that is a real failure and should keep looking like one.

`Unbind` and `GC` both consume the outcome; both surface `Gone` in their
results. The `unbind` CLI prints `worktree <path> was already gone`.

### 4.7 `cmd/relay` `done` (modified, one line)

The success line gains a trailing hint: `<name> marked done; relaying
stopped (relay gc archives it when you are finished with it)`. This is the
other place the issue names as silent about `gc`.

## 5. High-level pseudocode

### 5.1 `status` / `watch`

```
parse flags (--json, --name, --all)
rep := relay.Status(ctx, rt)
rep = filterReport(rep, target)          -- unchanged; --name narrows to one
if target == "" && !all:
    rep = relay.HideDone(rep)
emit rep as JSON or RenderStatus(rep)
```

### 5.2 `RenderStatus`

```
if no rows and DoneHidden == 0: return "no bindings\n"
render each row as today
if DoneHidden > 0: append footer "<n> done · relay gc to clear\n"
```

### 5.3 `GC`, per DONE binding

```
outcome := worktreeTeardown(...)        -- may now be Gone
copy outcome into result (Removed / Kept+Reason / Gone)
if DryRun: record, continue
if Delete: tx.Delete; Deleted = true
else:      tx.Archive; ArchivedTo = dest
```

### 5.4 `worktreeTeardown`

```
if no recorded worktree: return {}
if Git unavailable: return Kept "git unavailable"
if stat(worktree) is ErrNotExist: return Gone
dirty? -> Kept "uncommitted changes" / "dirty check failed: …"
if dryRun: return Removed
remove; on error Kept with reason; else Removed
```

## 6. Error handling strategy

No new error types. The one behavioural change in error handling is §4.6: a
missing directory is an outcome (`Gone`), not an error, because there is
nothing to recover and nothing to warn about -- the human, or a branch
deletion, already removed it.

`gc --delete` on a binding whose archive would have failed is not a new
case; delete and archive fail independently and each returns a wrapped
store error naming the binding, as today.

## 7. Behavioural rules and their rationale

### 7.1 `--json` hides DONE too

Rejected: keeping JSON unfiltered "for machines". A statusline is the
motivating machine consumer and it wants exactly the live set. Two formats
of one command that disagree about what exists are a bug waiting for the
first person who reads one and acts on the other. `DoneHidden` in the JSON
carries the count the footer prints, so nothing is lost.

### 7.2 `--name` beats the filter

A DONE binding must stay reachable by name -- for `relay log`, for reading
its report, for deciding whether to gc it. Filtering by name is already a
request for one specific thing; hiding it would be relay answering a
different question.

### 7.3 `--archive` becomes a no-op rather than an error

Erroring would break any script written against today's flag for no gain.
Silently ignoring it is safe because the behaviour it asked for is now the
default: the script gets what it wanted.

### 7.4 The footer says "clear", not "free"

`gc` archives state and removes only worktrees relay created and that are
clean. For a `--cwd` binding it frees nothing. "clear" is true in every
case.

### 7.5 "Already gone" is reported, not hidden

`worktreeTeardown` could silently treat a missing directory as removed. It
should not: the record said relay created a tree there, and the human may
want to know something else took it. One line, no judgement.

## 8. Testing requirements

`internal/relay`:

1. `HideDone` removes exactly the DONE rows, preserves order, sets
   `DoneHidden`, leaves the input untouched.
2. `RenderStatus`: footer present with the right count and singular/plural;
   absent when `DoneHidden == 0`; "no bindings" absent when only the footer
   applies.
3. `GC` default archives (no `Delete`): `ArchivedTo` set, directory moved,
   `.archive/` has the tarball. Existing delete test now sets `Delete: true`.
4. `GC` with a recorded worktree whose directory is missing: `WorktreeGone`
   set, `fakeGit.dirtyCalls == 0`, `removeWorktreeCalls` empty, and the
   binding still archived.
5. `Unbind` same missing-worktree case: `WorktreeGone` set.

`cmd/relay` (where flag tests already exist; otherwise skip and say so):

6. `status --all` bypasses the filter; `status --name <done>` returns the
   DONE row with `done_hidden` absent.

Mutation checks for the report: remove the `State == StateDone` comparison
in `HideDone` (test 1 must fail); make `gc` delete by default (test 3 must
fail); remove the `os.Stat` in `worktreeTeardown` (test 4 must fail).

## 9. Explicitly out of scope

- Auto-gc, age-based sweeps, gc on `done`.
- `unbind` defaulting to archive.
- `internal/ui` adopting the filter (#18's territory).
- Archive pruning.
- `internal/git`'s classification of a `chdir` failure.
