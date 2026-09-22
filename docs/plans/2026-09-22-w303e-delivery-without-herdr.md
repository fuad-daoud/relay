# #303 step 3: planner delivery without herdr, and `internal/herdr` goes

This plan stands alone: everything you need is in this file and in the tree.
The design is `docs/specs/2026-09-22-drop-herdr-design.md` (§1.2 D4–D7,
§3.6, §4.6, §4.8, §5.4, §5.5). Steps 1a, 1b, 2 and 2b have merged:
- the planner registry and `Resolve` are in `internal/planner`;
- channel claims are keyed by planner id;
- builders and consults are headless or remote only.

If a step is impossible as written or contradicts the code, **halt and
report**. Do not improvise around it.

**Work directly: do not dispatch sub-agents or explore agents.**

## Done means

- `grep -rli herdr --include='*.go' . | grep -v _test` is **empty**.
- No production code reads `HERDR_PANE_ID`, `HERDR_WORKSPACE_ID`,
  `HERDR_SOCKET_PATH` or `HERDR_ENV`.
- `make check` passes.

Tests may still say "herdr" only in comments explaining history. No test
may construct or fake a herdr client: CI has no herdr.

## 1. Delivery (spec §5.4)

`DeliverPending` becomes exactly the §5.4 pseudocode:
1. the channel route (a live claim for `b.PlannerID`);
2. then `rt.Deliverers[b.Planner.Kind]`, #300's port, **unchanged**;
3. otherwise the entry stays pending with `Delivery.Reason = "no push route
   for planner <name> (<kind>): relay wait/pull"`.

Every `LogEntry` for a delivery gains `route` (`channel`,
`deliverer:<kind>` or `pull`). `relay pull` marks a pending entry
delivered with `route=pull`.

Delete:
- the pane path: `FindAgent`, the planner-status gate, `promptWithRetry`,
  `Target`, `ErrPromptLate` and `lateScanLines`;
- #300's pane fallback: `OutcomeNotMine` past `FallbackAfter` stops falling
  through to a pane. `OutcomeNotMine` now leaves the entry pending with the
  deliverer's reason. Keep the `Outcome` type and every other value;
- `held.go`, the `HELD` state, `--held-grace`, `PlannerScreen`, the
  planner-screen fingerprint and the hold clock in status;
- **ORPHANED**: the state, and everything that sets it. A binding whose
  planner has no route is simply pending. `BROKEN` stays **only** if a
  non-herdr path still sets it (a headless builder's process vanishing);
  otherwise delete it too, and say which in the report.

States that vanish must still **load**. A `bind.json` with `state: held`
or `state: orphaned` loads as `active`, with a one-line debug log. Add the
mapping where `store` decodes `State`.

## 2. Notifications and the daemon (D4)

- Delete `Notify` and every caller: halt, stall, stale and all-finished
  (finished.go, daemon_events.go). Keep the `LogEntry` records they sat
  beside.
- Delete the daemon's per-tick `ListAgents` and the socket event
  subscription (#146, `Subscribe`, `daemon_events.go`). The daemon ticks on
  its interval alone.
- Delete foreign-agent rows (`foreign.go`) and sub-agent coverage
  (`coverage.go`), and their status lines.

## 3. The port, the package, the env

- Delete `relay.Herdr` (`internal/relay/herdr.go`; keep whatever else that
  file holds, renamed `runtime.go` if it becomes only the `Runtime`
  struct), `Runtime.Herdr`, and every herdr type leaking into other
  packages: `herdr.Agent`, `Sound`, `PaneMetadata`, `Event`,
  `ErrNoSocket`, `IntegrationState`, `MinVersion`, `SoundRequest`. Then
  `git rm -r internal/herdr`.
- `internal/serve/herdr.go` and `HerdrError` (serve admin): delete.
- `names.go` `MaxAgentNameLen` came from herdr's 32-character agent-name
  cap. `store.ValidName` keeps its **current** rule, so no existing binding
  name becomes invalid; move the constant into `store` with a comment
  saying where the number came from.
- `internal/harness`: delete `Integration` (herdr integration install) and
  `SubAgents`, plus anything in `relay agent` / `relay init` that installs
  a herdr integration.
- Every remaining `HERDR_*` read: delete. `Planner.PaneID` is written by
  nothing (it stays loadable; spec §3.2).

## 4. Status JSON (spec §3.6)

- **Remove:** `planner_pane`, `planner_status`, `planner_focused`,
  `workspace`, `builder_pane`, `foreign`, `sub_agents`, `hold`,
  `herdr_error`, and any other field left fed by deleted code.
- **Add:** `planner_route` (`channel` | `deliverer` | `none`) and
  `planner_route_live` (bool).
- Update **every** consumer in the tree in this round: `relay status`
  text, `statusline`, `internal/ui` (rail and detail pane, including the
  foreign rows and the "terminal" tab source line if it still names a
  pane), serve `FlatStatus`, and the MCP `status` tool.
- The remote wire protocol (`internal/remote/proto.go`) must not change.
  Check that with `git diff`.

## 5. doctor (spec §4.8)

Delete every herdr row: the binary, the version, `MinVersion`, the socket,
integrations and pane env. Keep the `plugin`, `plugin hook` and `planner`
rows from step 1b, and add the opencode-server WARN row and the stale
planner INFO row if 1b didn't. `UsableBuilder` means "binary on PATH and
the candidate parses".

## 6. MCP

- `internal/mcp/instructions.go`: drop every `broken`/`orphaned` mention.
  Keep `report` and `needs_you`.
- The `status` tool's rows follow §4.

## 7. Tests

- **Delete** tests whose behaviour is deleted here: pane delivery, held
  and HELD, the grace clock, ORPHANED, notifications, socket events,
  foreign/coverage rows, herdr doctor rows, and `internal/herdr`'s own
  tests.
- **Port** (behaviour survives): the delivery tests become channel /
  deliverer / pending tests; the daemon tick tests lose `ListAgents`; the
  status goldens are regenerated from the new JSON (review each golden
  diff, and say in the report that you did); `FlatStatus`.
- **New:**
  - `TestDeliverPendingNoRouteStaysPending`;
  - `TestDeliverPendingDelivererNotMineStaysPending` (the old fallback is
    gone);
  - `TestPullMarksDeliveredRoutePull`;
  - `TestLoadMapsHeldAndOrphanedToActive`;
  - `TestStatusJSONHasRouteFields`.
- Remove the fake herdr from every test package. Where a test only needed
  it to satisfy `Runtime.Herdr`, drop the field. The `internal/e2e` remote
  tests keep running without it: assert on the queued report / mailbox
  instead of a typed prompt.
- `internal/relay/e2e_test.go` (build tag `e2e`) drives a real herdr
  session. **Delete the file and its `testdata/e2e-shim.sh`.** Step 5
  replaces them with a CI-run headless e2e. Leave the Makefile's `e2e`
  target alone; step 4 handles it.

Mutation checks: do these and report each result.
1. Put back a fall-through from `OutcomeNotMine` to "delivered".
   `TestDeliverPendingDelivererNotMineStaysPending` fails.
2. Drop the held/orphaned load mapping. `TestLoadMapsHeldAndOrphanedToActive`
   fails.

Restore by re-editing.

## 8. Verification before you report

- `make check` passes, including `test -z "$(gofmt -l .)" || { gofmt -l .;
  exit 1; }`.
- The two `grep` lines under "Done means" are empty; paste them.
- `git diff --stat -- internal/remote/proto.go` is empty.
- Report:
  - tests deleted, one line each, grouped by file;
  - tests ported;
  - tests added;
  - golden diffs reviewed;
  - both mutation results;
  - `git diff --shortstat`;
  - every departure with its reason.
- Commit with `feat(deliver): …` (or split into several commits, one per
  section). Don't rebase and don't push.
