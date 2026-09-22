# #303 step 2b: consults (`relay ask`) are headless only, and `relay reap` goes

This plan stands alone: everything you need is in this file and in the tree.
The design is `docs/specs/2026-09-22-drop-herdr-design.md` (#303 scope:
"Consult/review panes ... become headless consults"). Builders are already
headless-only (step 2, merged). If a step is impossible as written or
contradicts the code, **halt and report**. Do not improvise around it.

**Work directly: do not dispatch sub-agents or explore agents.**

## Scope

A consult (`relay ask`, and the verify reviewer, which is already headless)
never opens a herdr tab or pane again. The headless consult path that
exists today (`AskOptions.Headless`, ask.go:200-230, `consultHeadlessPrompt`)
becomes the only path.

**Keep, untouched in behaviour.** These are step 3:
- the **planner's** pane delivery and everything herdr does for the
  planner: deliver.go, held.go, `promptWithRetry`, notifications, daemon
  `ListAgents`/`FindAgent`, foreign rows, coverage, and the
  `internal/herdr` package itself;
- planner identity (`internal/planner`, `Resolve` in ask).

Another builder is wiring the planner registry (step 1b) in parallel, and
it touches `ask.go`'s planner lookup (~130-140) and `AskOptions`
planner fields. **Do not edit those lines.** Confine your ask.go edits to
the consult launch (~150-270) and `AskOptions.WorkspaceID`/`Headless`.

## 1. Delete

- **ask.go**
  - The pane consult: `openTab`, `CreateTab`, `StartAgent` (~233-270), and
    the "a pane exists (CreateTab itself failed)" branches in the doc
    comment (~120).
  - `AskOptions.WorkspaceID`. `AskOptions.Headless` becomes implied: delete
    the field and its checks, and make every consult take the headless
    branch.
- **bind.go `openTab`**: delete it if the ask pane path was its last
  caller. Check with grep.
- **consult.go**: in `reconcileConsults`, delete the pane-consult
  reconcile (~164-240: pane liveness, findings-file polling for a pane
  consult, closing the consult pane). The headless consult reconcile
  stays. If `reconcileConsults` no longer needs its `agents []herdr.Agent`
  parameter, drop it and update the one caller.
- **reap.go** and `cmdReap`: delete them, including the `reap` verb in
  `cmd/relay/main.go`'s dispatch and usage text. Nothing spawns a pane any
  more, so there is nothing to reap.
- **cmd/relay/main.go `ask`**:
  - Delete `--workspace` and `workspaceOrEnv` (~1268-1280).
  - **Keep** `--headless` as an accepted no-op, printing once to stderr:
    `relay: --headless is the default and only consult mode; the flag is
    ignored`.
- **harness**: delete `Launch.Args` (the interactive form) and `PaneArgs`
  (harness.go ~343-353, 472+), and every per-harness `Args` definition
  that fed them. Keep `Print`/`PrintArgs`. When a harness test asserted
  `Args`, assert `Print` or delete it if it only covered `Args`.
- **usage**:
  - `consultSource` (usage.go:73) defaults to headless.
  - Delete `readPane` (usage/source.go:242-270), and the sqlite3 doctor row
    if its only purpose was `readPane`'s opencode.db read. Check with grep.
  - **Keep the constant `usage.ModePane`**: the db's history rows carry
    `builder_mode='pane'` and readers must still parse it. Give it a
    `// history only since #303` comment.
- **store**: a consult endpoint with `Mode ""` in an existing `bind.json`
  (a pane consult from before this round) is closed as `abandoned` with the
  note `pane consults were removed (#303)` by the next reconcile, not
  reconciled as a pane. Find how `finishConsult` records a state and reuse
  it.

## 2. Tests

- Delete tests that only assert pane consult behaviour: tab creation,
  StartAgent, the consult pane being closed, the reap verb and rules, and
  `--workspace`. Delete a test only when the behaviour it asserts is
  deleted.
- Port the consult and ask tests of surviving behaviour: findings recorded,
  question/answer files, consult timeouts, usage attribution and the
  verify reviewer. Every consult becomes headless; use the existing
  headless consult fixtures.
- New tests:
  - `TestAskIsAlwaysHeadless`: no `CreateTab`/`StartAgent` is called on
    the fake herdr, and the consult endpoint's `Mode` is headless.
  - `TestReconcileAbandonsLegacyPaneConsult`.
  - `TestAskHeadlessFlagIsNoOp`: a pure-function test of the flag
    handling. **No `cmd/relay` test may run a subcommand that reaches
    herdr** (CI has none).

Mutation checks: do these and report each result.
1. Restore the pane branch in ask as dead code behind `if false`, then flip
   it to `if true`. `TestAskIsAlwaysHeadless` fails.
2. Skip the legacy abandon. `TestReconcileAbandonsLegacyPaneConsult` fails.

Restore by re-editing.

## 3. Verification before you report

- `make check` passes, including `test -z "$(gofmt -l .)" || { gofmt -l .;
  exit 1; }`.
- `go vet -tags e2e ./internal/relay/` passes.
- `grep -rn 'CreateTab\|StartAgent\|PaneArgs' --include=*.go internal cmd
  | grep -v _test` lists only `internal/herdr` itself. Report what remains.
- Report:
  - tests deleted as pane-only, one line each, grouped by file;
  - tests ported;
  - tests added;
  - both mutation results;
  - `git diff --shortstat`;
  - every departure with its reason.
- Commit with `feat(consult): …`. Don't rebase and don't push.
