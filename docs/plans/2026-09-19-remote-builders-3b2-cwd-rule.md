# Remote builders, plan 3b.2: several remote bindings per repo (#100)

Round 2 on the same branch. Found on the planner's manual run of plan 3b:
`relay add --name e2e3 --server zen` in a repo that already had a remote
binding `e2e2` was refused with `working tree already bound`. The store's
one-binding-per-CWD rule (`assertCWDFree`, `FindByCWD` in
`internal/store/store.go`) exists because a CWD is a builder's working
tree and two builders in one tree race. A remote builder never works in
its CWD -- `CWD` is only the repo the branch is cut from and the results
are fetched into -- so the rule must not apply to it, exactly as it does
not stop two `relay add` worktrees from the same repo.

**Halt rule for the builder.** If any step below is impossible as written
or contradicts the code you find, stop, write the report saying which step
and why, create the done marker, and do nothing else. Do not rebase or
move this branch.

**Scope guard.** Touch only `internal/store/store.go`,
`internal/store/store_test.go` (or the store's existing test file for
these two functions), `internal/relay/remote_test.go`, and copy this plan
to `docs/plans/2026-09-19-remote-builders-3b2-cwd-rule.md`. Foreground
only, no sub-agents.

## 1. The rule

A binding whose builder is remote (`Builder.Remote()`) is **exempt from
CWD uniqueness on both sides**:

- `assertCWDFree(b)`: skip when `b.Builder.Remote()`; and when iterating
  `other`, skip any `other.Builder.Remote()`. So a remote binding may share
  a CWD with any number of remote bindings and with one local binding, and
  a local binding is never refused because a remote one names its repo.
- `FindByCWD(cwd)`: never returns a remote binding. The cwd-addressed verbs
  (`relay send` with no `--name`, `relay status` for "this tree") resolve
  to the tree's local binding or to nothing; remote bindings are always
  addressed by `--name`. Document that in the doc comment.

## 2. Steps

**Step 1.** Copy the plan. Tests first, in the store's test file:
`TestAssertCWDFreeIgnoresRemote` (local `a` at `/repo`; saving remote `b`
at `/repo` succeeds; saving remote `c` at `/repo` succeeds; saving local
`d` at `/repo` is refused naming `a`, not `b`); `TestFindByCWDSkipsRemote`
(only remote bindings at `/repo` -> not found; local + remote -> the
local one). Run them red.

**Step 2.** Implement §1. Run green. In `internal/relay/remote_test.go`
add `TestAddRemoteTwicePerRepo` with `fakeRemote` and `fakeGit`: two
`addRemote` calls with different names and the same `Repo` both succeed
(mutation target: revert the `assertCWDFree` exemption and this fails).

**Step 3.** `make check` green. One commit: `store: remote bindings are
exempt from the one-binding-per-cwd rule (#100)` with the plan file.
Report the mutation result. Done marker.
