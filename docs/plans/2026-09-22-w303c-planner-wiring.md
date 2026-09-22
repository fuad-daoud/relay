# #303 step 1b: bindings, `relay mcp`, channel claims and doctor run on the planner registry

This plan stands alone: everything you need is in this file and in the tree.
The design is `docs/specs/2026-09-22-drop-herdr-design.md` (§3.2, §3.3,
§4.3, §4.5, §4.8, §5.2, §5.3, §5.6's last paragraph). The registry it
builds on, `internal/planner` and `relay planner init|list|rename|forget`,
**is already in the tree**. Read `internal/planner/*.go` before anything
else and use its API as it is. If this plan names a function differently
from the code, the code wins; say so in the report. If a step is impossible
as written or contradicts the code, **halt and report**. Do not improvise
around it.

**Work directly: do not dispatch sub-agents or explore agents.**

## Working efficiently (read this before step 1)

Your provider has about **3 s of round-trip latency per model step**,
whatever the step does. Generation itself is fast. The last comparable
round took 307 steps, and 261 of them made a single tool call. The cost
of this round is its **number of steps**, so:

1. **Batch independent tool calls in one step.** When you need several
   files, ranges or searches that don't depend on each other, issue them
   all as parallel tool calls in the same response. The same goes for
   independent edits to different files.
2. **Read once, from the locations given.** This plan names files,
   functions and line ranges. Read each file you will edit once (the named
   ranges, or the whole file if it's under ~400 lines) before editing it.
   Don't grep for things the plan already located, and don't re-read a
   range you have already seen unless you edited it.
3. **Edit in as few calls as possible.** Make every change to a file in one
   edit call (several hunks), or rewrite the file when most of it changes.
   Prefer one scripted edit (a short `python3` or `perl` over named ranges)
   to many single-hunk edits when the change is mechanical.
4. **One tight build/test loop.**
   - Iterate with `go build ./... 2>&1 | head -80` and focused
     `go test ./<pkg> -run '<names>'`.
   - Fix **every** error a run reports before running again.
   - Run `make check` once when you believe you are done, and once more
     only if it fails.
5. Don't run `git status`/`git diff` between edits for reassurance. Run
   them at the start and before the commit.

## Scope

Herdr stays in the tree this round. The pane **delivery** path is
unchanged: a binding still records `Planner.PaneID` when `$HERDR_PANE_ID`
is set, and `DeliverPending` still types into that pane when no other route
takes the payload. What changes is that **the planner's identity comes
from the registry**, and **channel claims are keyed by planner id**.

## 1. bind / add / fork / ask resolve their planner

- Every `$HERDR_PANE_ID` read in `cmd/relay/main.go` (~859, 1005, 1098,
  1478) stays, but only to fill `Planner.PaneID` for the pane delivery
  path. It stops being required.
- Each verb calls `planner.Resolve(reg, ResolveInput{Flag: --planner, Env:
  os.Getenv, PPID: os.Getppid(), ProcStart: <the proc start-time helper
  used by relay planner init>, Now})`. Add `--planner <id|name>` to bind,
  add, fork and ask.
- `ErrNoPlanner` is a **hard error**, exit 1, printing exactly:
  `relay: no relay planner for this session. Run "relay planner init" once
  here, or enable the relay plugin (relay doctor).` The old `"no planner
  pane; is HERDR_PANE_ID set"` errors (add.go:108, ask.go:135, bind.go:112,
  fork.go:105) go away. Their options structs get `PlannerID string`
  (required) next to `PlannerPane` (now optional).
- On success: `b.PlannerID = r.ID`; `b.Planner.Kind`, `SessionID` and
  `TranscriptLocator` come from the record. `b.Planner.PaneID` comes from
  `$HERDR_PANE_ID` when set. Where the code today fills `Kind`/`SessionID`
  from `herdr agent list` via `endpointOf`, **the record's values win**.
  Keep `endpointOf` only for the `PaneID`/`AgentName` it still supplies.

## 2. Claims keyed by planner id (`internal/relay/channel.go`)

- `Claim.Pane` becomes `Claim.Planner` (`json:"planner"`). Add `HostPID
  int json:"host_pid"` and `HostStartedAt int64 json:"host_started_at"`.
  `ClaimFileName`, `path`, `Live`, `Write` and `Remove` take a planner id,
  so the file is `channels/<planner-id>.json`.
- **Old pane-keyed claim files.** A file in `channels/` whose name isn't a
  valid planner id is ignored by `Live`. On `relay mcp` start, it is
  removed **only if its pid is dead**. Other planner sessions may still run
  an older `relay mcp` during the upgrade, and deleting their live claims
  would make them rewrite every poll.
- `internal/relay/deliver.go:69`: `rt.Channels.Live(b.PlannerID, …)` when
  `b.PlannerID != ""`. A binding without a `PlannerID` skips the channel
  route. The daemon back-fill below fixes that on the next tick.
- `internal/relay/drain.go`: `DrainState.Pane` becomes
  `DrainState.Planner`, and the filter at ~56 becomes `b.PlannerID ==
  st.Planner && b.Owner == ""`.
- `internal/mcp/verbs.go`: `RelayVerbs.Pane` becomes `Planner`, and the
  `status` filter (~40) matches `planner_id`. Add `PlannerID string
  json:"planner_id"` and `PlannerName string json:"planner_name"` to the
  status row type that carries `PlannerPane` today, and fill them.

## 3. `relay mcp` (`cmd/relay/mcp.go`)

- Delete `--pane` and the `HERDR_PANE_ID` requirement (27-40). Add
  `--planner <id|name>`.
- Resolve with `planner.Resolve` using `PPID: os.Getppid()`. On
  `ErrNoPlanner`, retry every 500 ms for up to 10 s: the plugin's
  `SessionStart` hook may still be running. After that, run **tools-only**
  with the stderr line `relay mcp: no relay planner for this session;
  tools-only (run "relay planner init")`, and exit code 0 behaviour
  unchanged.
- The claim written is `Claim{Planner: r.ID, PID: os.Getpid(), HostPID:
  os.Getppid(), HostStartedAt: <start time of ppid>, …}`.
- Every poll re-reads the record by id, so a rename or session move made by
  a later `relay planner init --hook` (after `/clear`) is picked up without
  a restart.
- The startup stderr line becomes `relay mcp: planner <name> (<id>) mode
  <mode>`.

## 4. Daemon back-fill (spec §5.6, last paragraph)

On each tick, for every non-DONE binding with `PlannerID == ""` and a
non-empty `Planner.SessionID`: if `reg.BySession(Planner.Kind,
Planner.SessionID)` hits, set `PlannerID` and save. Do it under the lock
the tick already holds for that binding. Log `planner=<id> backfilled` at
debug. A miss does nothing.

## 5. status / statusline

- `relay status` with no name: resolve the planner, **without** erroring on
  `ErrNoPlanner`. On a hit, filter to bindings with that `PlannerID`; on a
  miss, keep today's behaviour. The same goes for the statusline filter at
  `cmd/relay/main.go:~1781`.
- `status --json` rows gain `planner_id` and `planner_name`.
  `planner_pane` stays this round.

## 6. doctor (`internal/doctor`)

Add the rows from spec §4.8's first three lines. The fourth (opencode) is
WARN. Add the fifth (INFO about stale records).
- `plugin`: FAIL when a `claude` candidate or planner record exists and
  neither `~/.claude/settings.json` nor `<repo>/.claude/settings.json` has
  `enabledPlugins["relay@relay"] == true`. Resolve `~` through `HOME`, so
  the cmd/relay TestMain isolation holds. Another planner's #123 will add
  two rows **beside** this one later: keep the row name `plugin` and the
  group layout simple.
- `plugin hook`: FAIL when the installed plugin has no `SessionStart` hook
  running `relay planner init`. Locate the installed plugin under
  `~/.claude/plugins/` by reading `~/.claude/plugins/installed_plugins.json`
  when present. If the file or the plugin can't be found, report `not
  checked` (OK), not FAIL. The builder can't see a real install; the
  planner verifies this row live.
- `planner`: only when `Detect()` says claude. FAIL when `Resolve` misses,
  or when no live claim exists for the resolved id.
- Leave every herdr row as it is. They go in step 3.

## 7. Tests (named; each must fail with its rule removed)

`cmd/relay` tests must not run a subcommand that reaches herdr (CI has
none), and must not read the real HOME (TestMain isolates it). Put rules in
`internal/relay`, `internal/planner` and `internal/doctor`.

- `TestBindRecordsPlannerFromRegistry`: `PlannerID`, `Kind` and
  `SessionID` come from the record, and `PaneID` from the pane env.
- `TestBindNoPlannerIsHardError`: exact error text.
- `TestClaimKeyedByPlannerID`, and `TestClaimLiveIgnoresPaneKeyedFile`.
- `TestMCPStartRemovesOnlyDeadPaneKeyedClaims`, as a pure-function test of
  the cleanup rule.
- `TestDeliverPendingChannelByPlannerID`: a binding with `PlannerID` and a
  live claim for it takes the channel route. The same binding **without**
  `PlannerID` takes the pane route.
- `TestDrainFiltersByPlannerID`.
- `TestDaemonBackfillsPlannerID`: a hit sets it; a miss leaves it empty.
- `TestMCPStatusFiltersByPlanner`.
- `TestDoctorPluginRow`: enabled → OK; missing → FAIL; no claude candidate
  → the row is absent.
- `TestDoctorPlannerRow`.
- Update every test that built a `Claim{Pane: …}` or `DrainState{Pane: …}`.

Mutation checks: do these and report each result.
1. Make `DeliverPending` use `Planner.PaneID` for the claim lookup again.
   `TestDeliverPendingChannelByPlannerID` fails.
2. Drop the dead-pid guard from the claim cleanup.
   `TestMCPStartRemovesOnlyDeadPaneKeyedClaims` fails.

Restore by re-editing.

## 8. Verification before you report

- `make check` passes, including `test -z "$(gofmt -l .)" || { gofmt -l .;
  exit 1; }`.
- `go vet -tags e2e ./internal/relay/` passes.
- Report: files changed, tests added and updated, both mutation results,
  and every departure with its reason.
- Commit with `feat(planner): …`. Don't rebase and don't push.
