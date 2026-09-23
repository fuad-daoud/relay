# Plan: restart hardening, round 1b: finish round 1 (#370)

This round continues `/home/fuad/projects/relay/docs/plans/2026-09-23-restart-hardening-r1.md` in the same worktree. Read that plan and the spec it names (`docs/specs/2026-09-23-restart-hardening-design.md`, already copied into your tree). They are normative except for the one amendment below.

Round 1 halted correctly at step 4, and its work is uncommitted in the tree. Steps 1–3 and step 4's code are done and were reviewed by the planner. Keep them as they are.

**Stop rather than improvise** still applies to every remaining step, with its own halt clauses.

## Amendment to step 4 (the planner's error, now resolved)

Step 4's "the existing #244 relaunch tests still pass without edits" contradicted §4.3: appending the interrupted note necessarily changes the relaunch argv. You may edit exactly **one** assertion: `TestReconcileHeadlessLostToDaemonRestartRelaunches`, `headless_test.go:1383-1385` (the `reflect.DeepEqual(fr.specs[1].Argv, fr.specs[0].Argv)` check).

Replace it with an assertion that:
- the two argv slices have equal length, and every element is equal **except the prompt element**;
- the relaunch's prompt element equals the first prompt element + `"\n\n" + interruptedNote(rt.StartedAt)`;
- the prompt element is located by index. Find the index where the two slices differ, and assert there is exactly one such index.

Leave every other assertion in that test, and every other #244 test, unedited. If any other existing test needs a change, halt as before.

## Remaining work

Do steps 5–10 of round 1 exactly as written: gate, consult, panic recovery, the step-8 tests with the four mutation checks, the cmd/relay daemon wiring, then `make check` plus `make e2e`, and the commit ending `(#370)`.

Also, in step 8's relaunch test, assert that the prompt contains the interrupted note and that `RoundStartedAt` is unchanged. That test may be the amended test above, extended, rather than a new one.

Declared scope: round 1's §2 file list, plus `internal/relay/headless_test.go` for the amended assertion and step 8's tests.
