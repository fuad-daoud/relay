# Agent identity by name; a sub-agent's status is not the builder's (#66)

**Design spec:** `docs/specs/2026-09-11-agent-identity-by-name-design.md`
**Issue:** #66

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan. One existing test is deliberately
reversed by this plan (step 2 says which and why); every other existing test
must keep passing unchanged except for the fixture names step 1 adds.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.** CI
  runners have no `herdr` binary. Every test in this plan is in
  `internal/relay` or `internal/herdr` against the fakes.
- `refreshEndpoint` keeps recording a session only when none is recorded.
  Do not make it overwrite on mismatch (spec §4.3).
- `effectiveStatus` overrides `idle` and `done` only. `blocked` passes
  through (spec §7.2).
- `DeliverPending`'s planner checks are untouched (spec §4.4).
- Every new exported symbol gets a doc comment that explains the rationale,
  in the house style. `SameAgent`'s comment is rewritten, not appended to.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: name-first identity and sub-agent-aware status

**Files:**
- Modify: `internal/herdr/types.go`, `internal/herdr/types_test.go`
- Modify: `internal/relay/herdr.go`, `internal/relay/herdr_test.go`
- Modify: `internal/relay/reconcile.go`, `internal/relay/reconcile_test.go`
- Modify: `internal/relay/consult.go`, `internal/relay/consult_test.go`
- Modify: `internal/relay/status.go`, `internal/relay/status_test.go`
- Modify: `internal/relay/diagnose.go`, `internal/relay/diagnose_test.go`
- Modify: `internal/relay/bind.go`, `internal/relay/bind_test.go`
- Modify: `internal/relay/daemon_test.go` or `daemon_consult_test.go` (the tick-level test in step 4)

**Interfaces produced** (spec §3, §4):
- `herdr.Agent.Name string`
- `relay.effectiveStatus(ep store.Endpoint, a herdr.Agent) string`
- `relay.BuilderDiagnosis.Identified bool` (replaces `SessionIdentified`)

- [ ] **Step 1: parse the name; name the fixtures** (spec §3.1)

  `internal/herdr/types.go`: add `Name string \`json:"name"\`` to `Agent`,
  first field, with the comment from spec §3.1. Extend the fixture in
  `TestParseAgentList` with a `"name"` on one agent and assert it is
  carried.

  `internal/relay/reconcile_test.go` `builderAgent`: add `Name:
  "webshop-builder"`. `internal/relay/consult_test.go` `consultAgent`: add
  `Name: "webshop-reviewer-7f2a3c1d"`. Leave `agentAt` (`foreign_test.go`)
  and `plannerAgent` nameless. Run the suite: nothing else changes yet, so
  it must stay green -- if a test fails here, the fixture name disagrees
  with what `Bind`/`Ask` record; check `bind.go:304` and `ask.go` and fix
  the fixture, not the code.

  Verify: `go test ./internal/herdr/ ./internal/relay/` green.

- [ ] **Step 2: `SameAgent` by name, then session with a pane fallback**
  (spec §4.1, §5.1, §7.1)

  Rewrite `SameAgent` in `internal/relay/herdr.go` as spec §5.1. Rewrite
  its doc comment: the rule order; why name comes first; that pane ids are
  not recycled (observed, cite the spec); the accepted risk restated as "a
  nameless endpoint whose pane a human deliberately hands to another agent
  of the same kind is adopted".

  Tests (`herdr_test.go`):
  - Extend `TestSameAgent`'s table with the rows in spec §8 item 1. Name
    every row.
  - **Replace** `TestFindAgentDoesNotFallBackWhenSessionRecorded` with
    `TestFindAgentFallsBackToPaneAndKindWhenSessionDiffers`: same fixture,
    now expects a match; add a sibling case where the kind differs and
    expects no match. The old test pinned the rule spec §1 "Why pane id
    fallback is now acceptable" reverses; say so in the new test's comment.

  Verify: `go test ./internal/relay/ -run 'SameAgent|FindAgent'` green.

- [ ] **Step 3: `effectiveStatus`** (spec §4.2, §7.2)

  In `internal/relay/reconcile.go`, near `refreshEndpoint`, add
  `effectiveStatus` per spec §4.2 with a comment carrying §1's reasoning in
  two or three sentences. Add one sentence to `refreshEndpoint`'s comment:
  it must not overwrite a recorded session on mismatch, or a sub-agent's
  session would be recorded and the parent would look foreign.

  Test (`reconcile_test.go`): `TestEffectiveStatus` table -- same session
  passes idle/done/working/blocked; differing session maps idle→working,
  done→working, working→working, blocked→blocked; empty recorded session
  passes through; empty live session passes through.

  Verify: `go test ./internal/relay/ -run EffectiveStatus` green.

- [ ] **Step 4: use it for the builder** (spec §4.4, §5.2)

  In `Reconcile`, change `switch builder.Status` to
  `switch effectiveStatus(b.Builder, builder)`. Nothing else in the
  function changes; `refreshEndpoint` has already run and recorded the
  first session.

  Tests, through `NewDaemon(rt, time.Second).Tick`, not a direct
  `Reconcile` call:
  - `TestTickIgnoresASubAgentsIdle` -- `sentBinding`, then tick once so
    the builder's session (`builderAgent` needs a `Session` for this; give
    the fixture `Session: herdr.Session{Value: "parent"}`) is recorded;
    advance the clock past `startGrace`; replace the builder agent with one
    carrying `Session.Value: "child"` and `StatusIdle`; tick. Assert no
    prompt was sent to the builder, no report entry, state `active`, and
    `Builder.SessionID == "parent"`.
  - `TestTickStillCapturesASubAgentsBlock` -- same, with `StatusBlocked`
    and `f.readOut` holding a dialog (copy the shape from
    `reconcile_blocked_test.go`): the question entry is queued.
  - `TestTickLocatesABuilderByNameAfterAPaneMove` -- builder agent with the
    fixture name, a *different* `PaneID`, and `Session.Value: "child"`:
    after the tick the binding is `active` and `Builder.PaneID` is the new
    pane.

  Check the `sentBinding` helper: if it already ticks, adapt rather than
  duplicate.

  Verify: `go test ./internal/relay/ -run 'Tick|Reconcile'` green.

- [ ] **Step 5: consults and status** (spec §4.4)

  `internal/relay/consult.go` `reconcileConsults`: compute
  `status := effectiveStatus(b.Consults[i].Endpoint, agent)` once after
  `refreshEndpoint`, and use it for `idle` and the blocked check.

  `internal/relay/status.go`: `row.PlannerStatus = effectiveStatus(b.Planner, a)`
  and the same for the builder. Comment: what relay acts on is what it
  shows (spec §7.4).

  Tests:
  - `consult_test.go` `TestReconcileIgnoresAConsultSubAgentsIdle` -- seed
    via `seedConsult`, tick once to record the session (give `consultAgent`
    a `Session`), then swap in `Session.Value: "child"` + `StatusIdle`, no
    findings: no nudge prompt, state still `running`, `NudgedAt` zero.
  - `status_test.go` `TestStatusShowsWorkingUnderASubAgentSession` --
    binding with a recorded builder session; builder agent with a different
    session and `StatusDone`: `BuilderStatus == "working"` and `Foreign` is
    empty.

  Verify: `go test ./internal/relay/ -run 'Consult|Status'` green.

- [ ] **Step 6: `Identified`** (spec §3.2, §4.5)

  `internal/relay/diagnose.go`: rename `SessionIdentified` to `Identified`,
  `= b.Builder.AgentName != "" || b.Builder.SessionID != ""`. Update the
  field comment and `movedPaneWarning` ("cannot be identified by name or
  session"). Update every reference (`bind.go:146`, `Detail`, tests --
  `grep -rn SessionIdentified` must come back empty).

  Tests (`diagnose_test.go`, `bind_test.go`): extend
  `TestDiagnoseBuilderDerivesBothFacts` with a named, session-less
  endpoint → `Identified`; extend the existing `--assume-dead` gate test in
  `bind_test.go` with the case: builder has `AgentName`, no `SessionID`, a
  round open, agent absent from the list → `Bind --resume --builder` does
  **not** return `ErrBuilderUnverified`.

  Verify: `make check` (or constituents) green; `grep -rn SessionIdentified` empty.

- [ ] **Step 7: mutation checks -- put the results in your report**

  Do each, run the named test, revert, and record which test failed:
  1. Remove the name rule from `SameAgent`. Expect the "name wins over a
     differing session" row of `TestSameAgent` and
     `TestTickLocatesABuilderByNameAfterAPaneMove` to fail.
  2. Make `effectiveStatus` return `a.Status` unconditionally. Expect
     `TestTickIgnoresASubAgentsIdle` to fail.
  3. Make `effectiveStatus` also map `blocked` to `working`. Expect
     `TestTickStillCapturesASubAgentsBlock` to fail.

  If a mutation does **not** fail its named test, fix the test, do not
  weaken the code, and say so.

- [ ] **Step 8: commit**

  One commit. Subject:
  `fix(relay): identify agents by name and read a sub-agent's idle as working (#66)`.
  Body: the herdr foreground-session behaviour, the two rules, the reversed
  test and why. Do not push.

## Report

Your report must contain:

- `git diff --stat` against your base.
- The `make check` (or constituents) output tail.
- The step 7 mutation table: mutation → test that failed.
- Anything you stopped on, or any place the plan and the code disagreed.
