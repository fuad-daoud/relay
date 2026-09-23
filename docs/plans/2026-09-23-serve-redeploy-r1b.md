# Plan: invisible serve redeploys, round 1b: finish round 1 (#373)

This continues `/home/fuad/projects/relay/docs/plans/2026-09-23-serve-redeploy-r1.md` in the same worktree. Its work is uncommitted there. Steps 1–2 and step 3's code are done: keep them as they are. Read that plan and the spec it names; both are normative except for the amendment below.

**Stop rather than improvise** still applies, with every halt clause.

## Amendment to step 3 (the halt was anticipated; this is the decision)

`TestRoundStartWhileRunningIs409` (`internal/serve/serve_test.go` ~1856) resends the identical plan for the running round. It encoded the pre-#373 rule that any start while running is a 409. The new rule is that an identical retry is 200, which `TestRoundStartRunningSamePlanIs200` now asserts.

You may edit exactly this in that test: its second request's **plan text** changes to a different plan, e.g. `"# Plan 1 (changed)"`. The test then keeps its meaning, "a *new* start while a round runs is 409 `round_open`". Keep its name and every other assertion. Say in the report exactly what changed.

If `TestRoundStartRunningDifferentPlanIs409` (yours) then duplicates it, keep both. Don't delete tests.

## Remaining work

- Finish step 3's verification and its mutation check.
- Step 4, the temp sweep: resolve the call site in `serve.go` (the startup path, before listening), and say where you put it.
- Step 5, client version logging.
- Step 6: `make check`, `make e2e`, and a commit ending `(#373)`.

Declared scope: round 1's §2, plus that one test edit.
