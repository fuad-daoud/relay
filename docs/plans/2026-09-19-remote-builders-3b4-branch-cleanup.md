# Remote builders, plan 3b.4: add --server removes the local branch it cut when the save fails (#100)

Round 4 on the same branch. Round 3 made `addRemote` remove the *server*
binding when the local half fails. The local branch `relay/<name>`, cut
by `CreateBranch` before `Save`, is still left behind on a save failure,
and the next `add` with that name is refused with `branch relay/<name>
exists; delete it or pick another name`. relay created it; relay removes
it.

**Halt rule for the builder.** If any step below is impossible as written
or contradicts the code you find, stop, write the report saying which step
and why, create the done marker, and do nothing else. Do not rebase or
move this branch.

**Scope guard.** Touch only `internal/git/client.go`,
`internal/git/client_test.go`, `internal/relay/herdr.go`,
`internal/relay/fake_test.go`, `internal/relay/remote.go`,
`internal/relay/remote_test.go`, and copy this plan to
`docs/plans/2026-09-19-remote-builders-3b4-branch-cleanup.md`. Foreground
only, no sub-agents.

## 1. Interfaces

`internal/git`:

```
DeleteBranch(ctx, dir, branch string) error
    git branch -D <branch>; a missing branch is nil (idempotent), anything else a wrapped error.
```

`relay.Git` gains `DeleteBranch`; `fakeGit` records it.

`addRemote`: the existing deferred cleanup gains a second flag,
`branchCreated`, set right after a successful `CreateBranch`. On a later
failure, after the server unbind, `rt.Git.DeleteBranch(ctx, opts.Repo,
branch)`; a failure is `slog.Warn` and appended to the error text as
`; local branch <branch> could not be removed: <err>`. Order: server
first, then branch -- the server binding is the one another client could
see.

## 2. Steps

**Step 1.** Copy the plan. `TestDeleteBranch` in `internal/git`
(existing branch removed; missing branch nil). Commit: `git: DeleteBranch
(#100)`.

**Step 2.** `TestAddRemoteCleansUpLocalBranchOnSaveFailure` in
`remote_test.go`: make `Save` fail after `CreateBranch` succeeded (the
same provocation `TestAddRemoteCleansUpServerOnSaveFailure` uses); assert
`fakeGit` recorded `DeleteBranch(repo, "relay/<name>")` after
`fakeRemote` recorded `Unbind`; mutation target: drop the `DeleteBranch`
call and it fails. Implement §1. Commit: `relay: add --server removes the
branch it cut when the save fails (#100)`.

**Step 3.** `make check` green. Report the mutation result. Done marker.
