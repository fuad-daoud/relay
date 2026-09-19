# pane transcript (#184) -- round 2: resolve the round-1 halt, then finish steps 4-8

This round continues `docs/plans/2026-09-19-pane-transcript.md` (the "base
plan"; your worktree already holds steps 1-3 and part of step 4 as
uncommitted edits). Everything in the base plan still applies -- halt rule,
scope guard, one `feat(ui):` commit, the plan copy, the gate -- except the
one correction below. Do not redo steps 1-3; verify they are present
(`git status --short` lists `session.go`, `session_test.go`,
`internal/transcript/record.go`, `record_test.go` untracked and
`headless.go`, `herdr.go`, `reconcile.go`, `send.go`, `switch.go`
modified) and continue.

## Correction to base plan §4 (`switch.go`) and §8 (`TestSwitchRearmsSessionCursor`)

The base plan said to make the `appendLogMarker("switched to …")` line run
for both modes. That contradicts `TestSwitchBuilderPaneWritesNoLog`
(`headless_test.go:810`), which pins that a pane switch creates no log
file -- a rule this feature does not need to break: a pane builder's round
log is created by the daemon's `drainSession` when the record produces its
first line, never by the switch. Planner's decision: **the marker stays
headless-only**, exactly as it is on `main`.

- In `switch.go`, restore the `if b.Builder.Headless() { appendLogMarker(...) }`
  guard as it was. Keep the re-arm you added right after `b.Builder = ep`
  for a non-headless replacement (`StreamRound, StreamOffset, LogPath =
  b.Round, 0, BuilderLogPath(name, round)`).
- `TestSwitchRearmsSessionCursor` (base §8) asserts only the three cursor
  fields after a pane switch; it does **not** assert a marker line, and it
  must assert the log file does not exist after the switch (the same
  `os.Stat` check `TestSwitchBuilderPaneWritesNoLog` makes).
- `headless_test.go` is untouched, and `TestSwitchBuilderPaneWritesNoLog`
  passes.

## Steps for this round

1. Apply the correction. `go test ./internal/relay -run 'Switch' -count=1`
   green, including `TestSwitchBuilderPaneWritesNoLog`.
2. Base plan step 4's remaining tests: `TestSendArmsSessionCursorForPaneBuilder`
   (`send_test.go`), `TestReconcileDrainsPaneSessionRecord`
   (`reconcile_test.go`), `TestSwitchRearmsSessionCursor` (`switch_test.go`,
   per the correction).
3. Base plan steps 5, 6, 7 as written (`cmd/relay/main.go` wiring;
   `internal/ui` fetch/detail/pane + tests; docs).
4. Base plan step 8: copy BOTH plan files into `docs/plans/`
   (`2026-09-19-pane-transcript.md` and this one as
   `2026-09-19-pane-transcript-round2.md`), squash to the one `feat(ui):`
   commit with the base plan's subject, and run the gate exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Report the tail verbatim, `git diff --stat main..HEAD`, and the list of
   tests you ran per step.

## Report

End `NNN-report.md` with the ```relay block. State that `make e2e` and
`make service` are owed by the planner.
