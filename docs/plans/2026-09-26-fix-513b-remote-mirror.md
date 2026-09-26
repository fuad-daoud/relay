# Fix #513 (part B), round 2 -- align SyncRemote's state filter with the daemon, then finish change B

## 0. Rules

- This tree is `relevo/fix-513b` with **uncommitted round-1 work**: change A
  (the `mirrorLog` empty-body early return in `internal/relevo/remote.go`) and
  its test `TestMirrorLogUnchangedLogWritesNothing` in `remote_test.go`. Keep
  it; do not reset or stash it.
- First: `git fetch origin`, then commit the round-1 work as a WIP commit
  (`git add -A && git commit -m wip`), `git rebase origin/main`, then
  `git reset --soft HEAD~1` to un-commit it again. If the rebase conflicts,
  stop and report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it.
- Comments: *why* only, no issue numbers, no `§`, no history. Functions <= 70 lines.
- Touch only: `internal/relevo/remote.go`, `internal/relevo/remote_test.go`,
  `internal/relevo/wait.go`, `cmd/relevo/status.go`, and the plan file (§4).
- `cmd/relevo` tests must not spawn a harness or reach the network: the new
  rule is tested in `internal/relevo`.

## 1. Context

Remote bindings' builder logs are mirrored into the local database by
`mirrorLog` from `observeRemote`, which runs from the daemon's reconcile and
from `SyncRemote` (`internal/relevo/remote.go` ~1036). `relevo status`
(`cmd/relevo/status.go` ~93) and every poll of `relevo wait`
(`internal/relevo/wait.go` ~190-196) call `SyncRemote`, each under the global
state lock with HTTP calls inside it, while the daemon is already doing the
same work. Round 1 made `mirrorLog` skip rewriting an unchanged log (change A,
done, uncommitted). Change B -- read verbs skip `SyncRemote` when a daemon is
running -- was halted because the filters differ:

- `Reconcile` (`internal/relevo/reconcile.go` ~172) returns early for
  `StateDone` **and `StatePaused`**, so the daemon never observes a paused
  remote binding.
- `SyncRemote` (`remote.go` ~1049) skips only `StateDone`.

Decision: `StatePaused` is a legacy state (no verb sets it any more; only old
data carries it) and a paused binding is by definition not being relayed. So
`SyncRemote` skips `StatePaused` too, matching the daemon. Then the daemon
covers every binding `SyncRemote` does, and change B is safe.

## 2. Changes

```
remote.go SyncRemote loop filter:
  if !b.Builder.Remote() || b.State == store.StateDone || b.State == store.StatePaused { continue }
  (one short why-comment: a paused binding is not relayed, the daemon skips it too)

remote.go, next to SyncRemote:
SyncRemoteUnlessDaemon(ctx context.Context, rt Runtime) (synced int, skipped bool, err error)
  rt.Remote == nil                    -> (0, false, nil)
  running, perr := rt.Store.DaemonRunning()   (internal/store/daemonlock.go ~48)
  perr == nil && running              -> (0, true, nil)   no network, no lock
  otherwise                           -> synced, err := SyncRemote(ctx, rt); return synced, false, err
  (a failed probe syncs: a missed skip costs contention, a missed sync could
   leave wait blind with no daemon)

cmd/relevo/status.go ~93: replace the relevo.SyncRemote call with
  relevo.SyncRemoteUnlessDaemon; the stderr message text stays identical.
internal/relevo/wait.go ~195: `_, _, _ = SyncRemoteUnlessDaemon(ctx, rt)`;
  keep the comment's why (with no daemon the poll is the only collector).
```

## 3. Tests (`internal/relevo/remote_test.go`, using `fakeRemote` ~135)

1. `TestSyncRemoteSkipsPausedBinding`: a remote binding in `StatePaused`;
   `SyncRemote` makes no `GetBinding` call for it.
2. `TestSyncRemoteUnlessDaemonSkipsWhileDaemonRuns`: hold
   `rt.Store.AcquireDaemonLock()` (release in `t.Cleanup`); one running remote
   binding. Want `skipped == true`, `fakeRemote.calls` empty.
3. `TestSyncRemoteUnlessDaemonSyncsWithoutDaemon`: same, no daemon lock. Want
   `skipped == false` and a `GetBinding` call for it.

Mutation checks: (a) remove the empty-body early return -- 
`TestMirrorLogUnchangedLogWritesNothing` fails; (b) ignore `DaemonRunning` --
test 2 fails; (c) drop the `StatePaused` skip -- test 1 fails. Restore after each.

## 4. Steps

1. Rebase with the round-1 work preserved (§0). `go build ./...` passes.
2. §2 and §3. Focused: `go build ./... && go test ./internal/relevo -run 'Mirror|SyncRemote|Wait' -count=1`,
   then `go test ./cmd/relevo -count=1` once (no golden may change; if one
   does, stop and report).
3. Mutation checks (§3).
4. `make check` in the foreground. `git diff --stat` shows only the §0 files.
   Commit: `fix(remote): mirror writes only when the log grew; status and wait leave remote sync to a running daemon (#513)`.
5. Save this plan to `docs/plans/2026-09-26-fix-513b-remote-mirror.md` and commit it.

Report: the diff stat, the mutation checks' failing test names, and the
`make check` result.
