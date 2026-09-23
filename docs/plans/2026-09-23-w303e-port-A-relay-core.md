# #303 step 3, test port A: the relay test fixture and `internal/relay` core

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report**. Do not improvise around it.

**Work directly: do not dispatch sub-agents or explore agents.**

## Context

Your branch is `relay/h-deliv`'s tip (`c8b1b27`, #303 step 3): relay no
longer has any herdr code. That round deleted **692 of 1,853 test
functions**, far beyond the behaviour it removed, because most tests seeded
their world through the deleted `fakeHerdr` and `BindOptions.PlannerPane`.
Your job is to **port the tests of behaviour that survives** back, for the
files named in your scope. The pre-deletion versions are in git at commit
`677a2ba`: `git show 677a2ba:<path>`.

## What counts as deleted behaviour (the closed list)

A test may stay deleted **only** if what it asserts is one of these:
1. typing into a planner pane: `promptWithRetry`, `Target`, late landing,
   `lateScanLines`, the pane delivery path of `DeliverPending`;
2. HELD, the hold clock, the planner screen fingerprint, the grace period;
3. ORPHANED;
4. `Notify`: sounds, the halt/stall/stale/all-finished notifications (the
   `LogEntry` beside them **survives**: port an assertion on it);
5. foreign-agent rows and sub-agent coverage;
6. herdr socket events, `agentCache`, resubscription, and the herdr client
   itself;
7. herdr doctor rows: binary, version, `MinVersion`, socket, integrations,
   pane env;
8. pane ids as data in status/ui rows (the **row** survives: assert on
   what it shows now);
9. `ListAgents`-driven endpoint refresh.

**Everything else is surviving behaviour and gets ported**: headless
rounds, reconcile, markers, gates, verify, repair, switches, timeouts,
queueing, status/statusline text and JSON, bind/add/fork/ask/send/stop/
pause/done, edges, consults, pick, land, the ui views, doctor's other rows.
A test that asserted a surviving behaviour **through** a deleted mechanism
(e.g. "halt notifies" → assert the halt `LogEntry`; "status shows planner
pane %1" → assert the planner name and route) is ported by changing the
assertion, not deleted.

## Working efficiently

Each model step costs about 3 s of round trip, so:
- Restore a whole file in one command (`git show 677a2ba:<path> > <path>`),
  then `go vet ./<pkg>/ 2>&1 | head -150` and fix every error in one pass
  per file, with one edit call per file (several hunks), or a scripted edit
  (`python3`/`perl`) for repeated mechanical changes such as a fixture
  signature.
- Batch independent reads as parallel tool calls.
- Loop with `go test ./<pkg>/ -run '<names>'`, then `make check` once at the
  end.

## Report

- Per file in your scope: the test-function count at `677a2ba`, at
  `c8b1b27`, and now.
- Every test still deleted, with the number of the closed-list item that
  justifies it. **A deletion without a list number is a failed step.**
- The fixture changes, and `make check` passing.
- Commit with `test(<pkg>): port … off fakeHerdr (#303 step 3)`. Don't
  rebase and don't push.

## Scope

These files only. Two other builders are porting `internal/ui` and
`internal/doctor` in parallel, and a later round ports the rest of
`internal/relay`.

```
internal/relay/fake_test.go, bind_test.go (fixtures)
internal/relay/headless_test.go   (87 -> 7)
internal/relay/status_test.go     (78 -> 18)
internal/relay/bind_test.go       (53 -> 3)
internal/relay/reconcile_test.go  (33 -> 0)
internal/relay/deliver_test.go    (26 -> 5)
internal/relay/daemon_test.go     (23 -> 2)
internal/relay/add_test.go        (20 -> 1)
internal/relay/send_test.go       (20 -> 2)
internal/relay/fork_test.go       (11 -> 0)
```

## 1. The fixture (do this first)

In `internal/relay` test code (a `fixture_test.go`, or the existing
`fake_test.go`/`bind_test.go` helpers):
- `newRuntime(t)` stays. Give it a `planner.FileRegistry` on a
  `t.TempDir()` holding one record: `ID: "pl_aaaaaaaaaaaa"`,
  `Name: "architect-1"`, `HarnessKind: "claude"`,
  `SessionID: "sess-architect"`, `CWD: "/repo"`. Also give it a
  `fakeRunner`, and a `ProcStart` that returns an error. Export the record
  id and name as test constants.
- `seedBound(t) (Runtime, store.Binding)` is the old helper without
  `*fakeHerdr`. It binds `webshop` headless with `Planner: "architect-1"`
  (or however `BindOptions` names the planner flag now; check `bind.go`),
  `Candidate: testAgyRef`, `CWD: "/repo"`.
- Rebuild `sentBinding`, `queuedBinding`, `timedOutBinding`,
  `startVerifyRound` and `sentSwitchable` (and any other fixture the
  restored files need) on top of `seedBound`, as `c8b1b27`'s surviving
  tests and `677a2ba`'s originals use them.

Other packages don't see these helpers. Round D will reuse them in the same
package.

## 2. Port

For each file in scope: restore it from `677a2ba`, keep the tests
`c8b1b27` added or rewrote (merge them in, don't lose them), then fix
compile errors by moving onto the fixture. Delete only closed-list tests.

Mutation checks: do these and report each result.
1. In `reconcile.go`, break the marker close path (e.g. skip
   `closeOnMarker`). At least one ported `reconcile_test.go` test fails.
2. In `headless.go`, break the round-timeout check. A ported
   `headless_test.go` test fails.

Restore by re-editing.
