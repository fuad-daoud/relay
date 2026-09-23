# #303 step 3, test port C: `internal/doctor` and `cmd/relay` doctor tests

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

```
internal/doctor/doctor_test.go (32 -> 4)
internal/doctor/env_test.go    (compare with 677a2ba)
cmd/relay/doctor_test.go       (22 -> 8)
```

Only closed-list item 7 (herdr rows) and the `HerdrClient`/`NewEnv(client, …)`
plumbing are deleted. Every other doctor row keeps its tests: candidates,
roles, the daemon, the release/staleness row, usage, the store, and the
plugin/plugin hook/planner rows. `cmd/relay` tests must not run a subcommand
that spawns a harness or reaches the network, and must not read the real
HOME (TestMain isolates it). Put rules in `internal/doctor`.

Mutation check: break the release row's staleness comparison. A ported
test fails. Report it, and restore by re-editing.
