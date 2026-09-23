# #303 step 3, test port D: the rest of `internal/relay`, plus pick, store, harness and serve

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

Your branch already carries port A: the relay fixture (`fixture_test.go`,
`seedBound(t)` and friends, off `fakeHerdr`) and the core `internal/relay`
files. **Use A's fixtures; don't write new ones** unless a file needs a
variant, and then build it on A's.

```
internal/relay/ask_test.go             (31 -> 0)
internal/relay/remote_test.go          (57 -> 31)
internal/relay/consult_test.go         (10 -> 0)
internal/relay/consult_followup_test.go (3 -> 2)
internal/relay/daemon_consult_test.go  (2 -> 0)
internal/relay/pick_test.go            (9 -> 0)
internal/relay/edges_test.go           (9 -> 0)
internal/relay/switch_test.go          (8 -> 0)
internal/relay/statusline_test.go      (19 -> 11)
internal/relay/land_test.go            (10 -> 2)
internal/relay/wait_test.go            (10 -> 4)
internal/relay/waiting_test.go         (4 -> 3)
internal/relay/pause_test.go           (6 -> 0)
internal/relay/stop_test.go            (4 -> 1)
internal/relay/usage_test.go           (6 -> 1)
internal/relay/queue_test.go           (5 -> 0)
internal/relay/reconcile_blocked_test.go (5 -> 0)
internal/relay/reconcile_hooks_test.go (4 -> 0)
internal/relay/reconcile_log_test.go   (3 -> 0)
internal/relay/finished_test.go        (4 -> 0)   # FinishedGroup survives if the code still has it; the notify part is item 4
internal/relay/diagnose_test.go        (4 -> 0)
internal/relay/logfollow_test.go       (3 -> 0)
internal/relay/served_test.go          (14 -> 12)
internal/relay/review_test.go          (5 -> 4)
internal/relay/limit_test.go           (3 -> 2)
internal/pick/model_test.go            (14 -> 7)
internal/pick/verb_test.go             (3 -> 2)
internal/store/log_test.go             (16 -> 11)
internal/harness/harness_test.go       (19 -> 17)
internal/serve/admin_test.go           (12 -> 11)
```

These stay deleted (closed list, no need to re-check): `foreign_test.go`
(5), `daemon_events_test.go` (6), `held_test.go` (2), `herdr_test.go` (6),
`coverage_test.go` (5), and `e2e_test.go`, which a later step replaces with
a headless e2e.

**One more, from port A's report.** `internal/relay/status_test.go`'s
`TestStatusJSONCarriesStructuredFields` was deleted because it set
`store.StateHeld`, which no longer exists. The structured JSON fields it
pins **survive**. Restore it from `677a2ba` and use `store.StateActive` (or
whichever surviving state exercises the same fields). It is the only
`status_test.go` change in your scope.

Mutation checks: do these and report each result.
1. Break `edges.go`'s fire-on-close. A ported `edges_test.go` test fails.
2. Break `switch.go`'s gated-candidate walk. A ported `switch_test.go` test
   fails.

Restore by re-editing.
