# #303 step 2: builders are headless or remote only

This plan stands alone: everything you need is in this file and in the tree.
The design is `docs/specs/2026-09-22-drop-herdr-design.md` (§1.2 D2, D3, D7;
§5.6; §6.3). If a step is impossible as written or contradicts the code,
**halt and report**. Do not improvise around it.

**Work directly: do not dispatch sub-agents or explore agents.**

## Scope, and what must survive

Delete every code path that runs a **builder** in a herdr pane. After this
round, a local builder is always headless and `add --server` is remote.

**Keep, untouched in behaviour.** These are later rounds:
- **Planner-side pane delivery**: deliver.go's pane path, held.go, the
  HELD state, the planner screen fingerprint, `promptWithRetry`, `Target`,
  `ErrPromptLate` and `lateScanLines` in send.go (deliver.go:141 uses them),
  notifications/`Notify`, daemon `ListAgents`/`FindAgent`/`SameAgent`,
  `refreshEndpoint`, `effectiveStatus`, foreign rows, coverage.go, and the
  `internal/herdr` package.
- **Pane consults**: `ask` opening a tab or pane, consult.go:164-240,
  reap.go, `cmdReap`, `ask --workspace`, `openTab` in bind.go,
  `harness.Launch.PaneArgs`/`Launch.Args`, and `usage.ModePane`/`readPane`
  (used by `consultSource`). Another round removes them.
- **Planner identity**: `endpointOf`, and the planner lookups in add.go
  (137-144) and fork.go (124-131).
- `SessionLocator`/`HomeSessionLocator`/`roundSession`, `drainFile`,
  `nudgeNote` (old logs carry nudge entries; wait.go:58 and waiting.go:55
  filter on it), and held.go's `fingerprint`, which `screenFingerprint`
  shares: keep `fingerprint`.

Another builder is working in parallel on `internal/planner`,
`cmd/relay/planner.go`, `internal/db`, `internal/ingest` and
`claude-plugin/hooks`. **Do not touch those.**

Line numbers below are from `c1d037e`. Trust the code over the number.

## 1. Decide the mode once: headless, always

- `resolveBuilder` (bind.go) always returns a headless endpoint. Delete the
  adopt path (637-647) and the pane launch (667-712).
- **Keep the early permission-tier refusal.** Today `ErrTierUnsupported` and
  `ErrExtraArgsPermission` fire at bind/add/fork only through the pane
  path's `h.Launch` (bind.go:674-677). Headless defers them to `send`
  (send.go:240, 371-376). In the headless branch of `resolveBuilder`, run
  the same validation, via `headlessLaunch` (headless.go:96) or `h.Launch`
  with the headless args, so bind/add/fork refuse before a worktree is
  kept. `TestBindOpencodeCandidateTierReadRefused` (bind_test.go:2293) must
  pass on a headless fixture.
- `fork` builds a headless builder. `switchBuilder` keeps inheriting the
  mode.
- Delete `BindOptions.BuilderPane`, `AssumeDead` and `WorkspaceID`,
  `ErrBuilderUnverified`, `ErrHeadlessAdopt` with its check (118-120),
  `ErrHeadlessResume` with its check (122-124), and `WorkspaceID` in
  add.go and fork.go. In `resume`, delete `OrphanedPane` (232-234) and the
  pane liveness / `DiagnoseBuilder` / assume-dead block (272-302).
- `builderWhere` prints `headless` for local builders.
- `cmd/relay/main.go`:
  - bind/add/fork **keep** `--headless` as an accepted no-op. Print once to
    stderr: `relay: --headless is the default and only local mode; the flag
    is ignored`. Delete the conflict checks (830-835).
  - Delete `--builder <pane>` (`isPaneID` 626, branches 885-889 and
    897-908), `--assume-dead` (806), and the `$HERDR_WORKSPACE_ID` reads in
    bind/add/fork (864, 1006, 1100). `ask --workspace` stays.
  - Update the usage text to match.

## 2. Legacy pane bindings retire (spec §5.6)

At the top of `Reconcile`'s per-binding path (reconcile.go around 252),
before any builder handling: when `b.State != DONE` and `b.Builder.Mode` is
`""` or `"pane"` and the binding is not remote, append
`LogEntry{Kind: "retired", Note: "pane builders were removed (#303); rebind
with relay add"}`, set `State = DONE`, and save. Leave the worktree exactly
as it is: no release, no gc. The entry kind `retired` is new; add it where
the tree enumerates entry kinds.

Paused bindings are included: a paused pane binding is also retired.
`relay status` shows it DONE, with that note as its last entry.

## 3. Delete, by file

- **reconcile.go**
  - The pane body of `Reconcile` after the headless/remote dispatch
    (257-434).
  - `handleBlockedBuilder`, `handleIdleBuilder`, `screenFingerprint`,
    `builderQuiescent`, `nudgeBuilder`, `scrapeReport`, `nudgeTime`.
  - The constants `scrapeLines`, `nudgeFingerprint`, `startGrace`,
    `nudgeGrace` and `nudgePrompt`. Keep `nudgeNote`.
  - Keep `haltBinding`, `checkRoundTimeout`, `closeOnMarker`, `queueReport`
    (drop only its `BuilderScreen` resets), `deliverAndSettle`, `HasEntry`,
    `refreshEndpoint` and `effectiveStatus`.
- **stop.go**
  - The pane branch (148-202), `stopPrompt`, the `stopRequest`, `stopWait`
    and `stopAbandon` states, and `StopOptions.Grace`/`Now`.
  - The matching code in reconcile.go (389-404), status.go (446-451) and
    text.go (the "requested"/"abandoned" `StopText` lines).
  - `stop --grace/--now` in main.go (2098-2099).
  - Keep the headless kill and `closeStopped`.
- **answer.go / `relay answer`**: delete the `Answer` implementation, the
  `answer` verb and its usage line, `AnswerText`, `DialogSource` and
  `DialogLines`. The MCP `answer` tool goes too: tools.go (36-44, 115-125,
  161-178), server.go (242-252) and verbs.go (16, 93-101). In
  `internal/mcp/instructions.go`, remove the lines about answering a
  blocked dialog and "builder pane is gone" (21-24, 38-39), and keep the
  rest.
- **pick**: delete `pick/answer.go`, `VerbAnswer` and every branch it
  feeds in verb.go and model.go.
- **dialog.go**: delete the file, its caller in send.go (393-394), and
  `harness.DialogPatterns`. `candidate.go`'s `dialog_patterns` field stays
  **decodable**, so existing `candidates.json` files still load, but unused.
  Give it a `// ignored since #303` comment.
- **send.go**:
  - Delete `preflight.builder`/`located` and the pane checks (205-207,
    245-256, 338-350, 391-408, 414-416).
  - Delete `ErrBuilderBlocked`, `ErrBuilderGone`, `ErrBuilderNotBlocked`,
    `ErrTierPaneFixed` (tier.go:15), and the `"pane"` defaults in
    `dryRunMode`/`dryRunWhere`.
  - **Keep** `Target`, `promptWithRetry`, `ErrPromptLate` and
    `lateScanLines`.
- **session.go**: delete `armSessionCursor`, `drainSession`, and the pane
  branch of `builderSessionOf`.
- **switch.go**: delete the pane `ClosePane` (134-138), the pane session
  cursor (167-172), the pane prompt (199-206) and `switchGrace`.
- **pause.go**: delete the pane close (122-127) and the pane lines in
  `PauseText`.
- **limit.go** (the pane read in `limitText`, 211-217), **repair.go** (the
  pane branch, 130-140), **progress.go** (the pane sample 46-58 and
  111-113, and the `signals.output`/`blocked` fields).
- **tokens.go**: delete the whole file (sidebar tokens are builder-pane
  only), the daemon's `applied` map and its token sync (daemon.go ~49, 88,
  327), and the `Builder.PaneID` match at daemon.go:208. In
  daemon_events.go, delete lines 100-102 (the builder half).
- **status.go**:
  - Delete the pane builder lookup (401-404), the working label (428-430),
    and the nudge block (546-562) with `NudgeInfo`/`NudgeText` and their
    render (859-862).
  - Remove the JSON fields fed only by deleted code (`nudge`, and the
    `stop_*` grace fields), and update every consumer in the tree
    (statusline, `internal/ui`, serve `FlatStatus`).
  - **Trap:** the `Done` case near 993 uses `!Headless()` and today also
    matches remote bindings. Keep remote behaviour identical, and add a test
    pinning it.
- **text.go**: delete the `OrphanedPane` lines (40-42), `AnswerText`, and
  the pane `StopText`/`PauseText` lines.
- **ui/fetch.go**: delete the herdr read of a builder's terminal
  (413-493).
  - A headless builder keeps its `NNN-builder.log` view.
  - **Trap:** remote builders fall into this path today. Give them their
    own branch that renders the local builder log when relay has one, and
    otherwise the single line `remote builder on <server>: relay log <name>`.
  - Add a test for the remote branch.
- **usage.go**: `roundSource` defaults to headless (usage.go:55).
  `consultSource` stays as it is.

After these deletions, `go vet` reports some herdr-interface methods as
unused by production code (`SendKeys`, `ReportMetadata`, `ReadAgent`).
Leave them.

## 4. Tests

- **Delete as pane-builder-only:**
  - whole files: answer_test.go, answer_blocked_test.go, dialog_test.go,
    pick/answer_test.go, tokens_test.go, and session_test.go except
    `HomeSessionLocatorGlobsAnySlug`;
  - the pane-builder functions in reconcile_test.go,
    reconcile_blocked_test.go, reconcile_log_test.go, send_test.go,
    bind_test.go, switch_test.go, stop_test.go, pause_test.go,
    status_test.go, daemon_test.go, progress_test.go, headless_test.go,
    pick_test.go, daemon_events_test.go, cmd/relay/main_test.go and
    ui/fetch_test.go.
  
  Candidate names: `*Nudge*`, `*Scrape*`, `*Quiescen*`, `*StartGrace*`,
  `*Broken*`/`*Unbreaks*` on builder gone, `*Adopt*`, `*AssumeDead*`,
  `StopPane*`, `*ClosesPane*`, `ShowsNudgeClock`, `TestIsPaneID`,
  `BindHeadlessConflicts*`, `StopGraceMustBePositive`, `Answer*`,
  `PickRejectsAnswerFlags`, `FetchTerminal*Pane*`, `DrainsPaneSessionRecord`,
  `ArmsSessionCursor*`, `SurfacesBlocked*`, `DryRunPane*`.
  
  Delete a test only when the behaviour it asserts is itself deleted by
  this plan.
- **Port to a headless builder fixture** (behaviour survives): the shared
  fixtures `seedBound` (send_test.go:20), `sentBinding`/`builderAgent`
  (reconcile_test.go:21-47), `queuedBinding`, `seedForAsk`,
  `edgeSourceBinding`, `startVerifyRound`, `sentSwitchable`,
  `timedOutBinding`, and the pane `Builder` literals in the gc, drain,
  finished, foreign, statusline, status and daemon tests. Everything
  resting on them must keep passing on a **headless builder with the
  planner still a pane**:
  - all of deliver_test.go;
  - daemon_test.go `TickInjectsOncePerPlannerPane` and
    `TickReconcilesAndPersists`;
  - the timeout and round-cap tests;
  - the marker, gate, verify, repair, edge and usage close tests;
  - `Settle*`, `LogsHaltOncePerRound`, the hooks, edges, consult, ask,
    usage, wait, pull and review tests;
  - status hold-clock tests;
  - `Gated*`, `StopDecisionTable` and `SendClearsStopRequest`;
  - the tier tests.
- **New tests:**
  - `TestReconcileRetiresLegacyPaneBinding`: an ACTIVE `Mode ""` binding
    becomes DONE with one `retired` entry. A second tick adds none, a
    remote binding is untouched, and a PAUSED pane binding is retired.
  - `TestBindHeadlessFlagIsNoOp`: this is a rule test. If you add a
    `cmd/relay` test, it **must not run a subcommand that reaches herdr**
    (CI has no herdr). Test the flag handling as a pure function instead.
  - `TestAddRefusesUnsupportedTierBeforeWorktree`.
  - The remote `Done` pin from §3's status.go trap, and the remote ui/fetch
    branch test.
- **e2e** (`internal/relay/e2e_test.go`, build tag `e2e`):
  - Delete the subtests `idle_without_marker_nudges_once`,
    `still_screen_closes_unmarked`, `still_screen_scrapes` and
    `screen_movement_resets_grace`, and the `nudgePrompt` reference in
    `prompt_last_lines`.
  - Convert `marker_closes_round` to a **headless** builder. The planner
    stays a real pane: it is the only check that injection works against a
    real herdr session.
  - Keep `testdata/e2e-shim.sh` (the planner uses it).
  - You can't run e2e on your machine. It must **compile**: run
    `go vet -tags e2e ./internal/relay/`. The planner runs it.

Mutation checks: do these and report each result.
1. Remove the retire branch. `TestReconcileRetiresLegacyPaneBinding` fails.
2. Remove the tier validation you added to `resolveBuilder`.
   `TestAddRefusesUnsupportedTierBeforeWorktree` fails.

Restore by re-editing, not with `git checkout`.

## 5. Verification before you report

- `make check` passes, including the gofmt check,
  `test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }`.
- `go vet -tags e2e ./internal/relay/` passes.
- `grep -rn 'Builder.PaneID' --include=*.go internal cmd | grep -v _test`
  lists no production reader of a builder pane. Pane *consults*
  (consult.go, ask.go) may still read their own endpoints: say which lines
  remain and why.
- **Report**, in the plan's section order:
  - test functions deleted as pane-only, one line each, grouped by file;
  - tests ported;
  - tests added;
  - both mutation results;
  - `git diff --shortstat`;
  - every departure from this plan, with the reason.
- Commit on the current branch with `feat(builder): …`. Don't rebase and
  don't push.
