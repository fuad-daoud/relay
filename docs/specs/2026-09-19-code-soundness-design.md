# Code soundness: an enforced standard and a phased restructure of `internal/relay` and `cmd/relay`

**Issue:** none yet; open one when the first plan is written.
**Depends on:** remote builders (#100, `2026-09-19-remote-builders-design.md`)
plans 2--4 merging first. Phase 0 (rules only) may land before them.
**Status:** approved design; plans deferred. Every phase below is
re-examined against the tree after remote builders lands, then written as
`docs/plans/2026-09-XX-soundness-<phase>.md`, one plan per phase, one
headless round each.

## 1. System overview

relay is ~26k lines of Go, 57k with tests. The weight sits in two places:
`internal/relay`, a flat 43-file, 8.6k-line package that holds planner
verbs, the daemon's per-tick state machine, terminal rendering and
protocol text side by side, all as `func Verb(ctx, rt Runtime, ...)`; and
`cmd/relay/main.go`, 1656 lines of dispatch, flag parsing, wiring and 26
command handlers. The other packages are proportionate.

The symptoms, measured on `main` at d0d57cf:

- Orchestrators are long and inline: `Fork` 200 lines, `Ask` 170, `Send`
  165, `reconcileHeadless` 164, `resume` 159, `Reconcile` 136,
  `statusRow` 130. Each feature adds a branch inside one of them.
- Nothing in a file name says whether the file is a verb, a round step, a
  renderer or a parser. 26 functions are pure text rendering and are called
  only from `cmd/`, `ui/` and `pick/`, yet live beside `Reconcile`.
- Comments are 22% of non-test lines and carry 102 issue references. They
  are well-written incident reports ("a headless agy builder has been
  observed to..."), not descriptions of the system as it is. A reader
  without GitHub, human or model, gets narrative rather than a contract.
- Tests are 2.5x the code and cluster in 2k-line files
  (`headless_test.go`, `bind_test.go`, `reconcile_test.go`). They pin
  behaviour through the exported API -- only 11 of 124 unexported
  functions are called from tests, and all fakes live in `fake_test.go`
  -- so internals can move without rewriting them.
- `make check` is gofmt + vet + test + tidy. Nothing measures function
  length, complexity, doc-comment shape or package boundaries, so drift is
  invisible until someone feels it.

The goal is a codebase that is *sound*: a standard short enough to hold in
one's head, enforced by `make check` so it cannot regrow, and a one-time
restructure that brings the existing code under it with **no behaviour
change**. Human- and model-friendly means the same thing here: a file's
name says its role, a function fits on a screen, a comment states a rule.

## 2. The standard

This section becomes `docs/conventions.md`, linked from CLAUDE.md. Six
rules, each with its enforcement.

1. **A function does one step and fits on a screen.** Hard cap 60 lines
   and cyclomatic complexity 15 (linter). Orchestrators (`Send`,
   `Reconcile`, `Fork`...) are a readable sequence of named phase calls;
   the phases hold the logic.
2. **A file has one role and its name says so.** In `internal/relay`:
   `verb_<name>.go` (a planner-facing command), `round_<name>.go` (the
   daemon's per-tick state machine), `daemon.go`, and a handful of shared
   nouns (`runtime.go`, `errors.go`, `protocol.go`...). In `cmd/relay`:
   `cmd_<name>.go` per command family, `flags.go`, `runtime.go`. Test
   files mirror sources 1:1; fakes in `fake_test.go`, shared helpers in
   `helpers_test.go`. (Layout test, §3c.)
3. **Rendering is pure; protocol text is not rendering.** Text for a
   terminal lives in `internal/render`, which takes data and returns
   strings and never imports `herdr`, `git`, `proc` or `hooks`.
   `internal/relay` never imports `render`. Text the daemon writes into
   files and panes as part of the handoff -- the builder prompt, origin
   lines, log lines, the resolution note a switch writes into a payload --
   is protocol, stays in `relay`, and lives in `protocol.go`. (Import
   test, §3b.)
4. **A comment states the rule, not the story.** Doc comments are
   present-tense contracts: what it does, what it requires, what it
   guarantees or refuses. No issue numbers, no "has been observed to", no
   names of harnesses that misbehaved. A "why" that a reader would
   otherwise reverse goes in `docs/decisions/` (§5) and the comment ends
   with `Decision: <slug>.` Every exported symbol has a doc comment.
   (`revive` exported rule; issue-reference check in the arch test.)
5. **Dependencies are visible at the seam.** `Runtime` remains the single
   wiring struct, but a function that needs one collaborator receives that
   collaborator, not the whole `Runtime`; only orchestrators and the
   daemon take `Runtime`. (Convention and review; no tool checks this.)
6. **No dead code, no shadowed errors, no unchecked errors.** (`unused`,
   `errcheck`, `govet shadow`.)

Non-goals: no behaviour change anywhere; no new abstractions for their own
sake (no repositories, no single-implementation interfaces beyond the
existing `Herdr`/`Git`/`Runner` seams); no rename of exported API that
`ui`, `pick` or `proc` consume except for the moves into `render`.

## 3. Enforcement

All three fail `make check`.

### 3a. golangci-lint

`.golangci.yml`, pinned version, run from `make check`; CI gains one
`go install` step. Enabled linters and nothing else:

- `funlen` (60 lines, statement count ignored), `gocyclo` (15).
- `revive`, rules: `exported` (doc comment on every exported symbol,
  starting with its name), `package-comments`, `unused-parameter`,
  `early-return`.
- `errcheck`, `unused`, `govet` including `shadow`, `gofmt`, `goimports`
  with local prefix `github.com/fuad-daoud/relay`.
- `_test.go`: `funlen` and `gocyclo` excluded (table tests are long by
  nature); everything else applies.

From Phase 0 until Phase 6 the linter runs with `--new-from-rev=origin/main`
so new lines are gated while legacy code is not yet failing. Phase 0 records
the whole-tree violation count as the target; Phase 6 removes the flag.

### 3b. Architecture test

`internal/arch/arch_test.go`, pure `go/build` + `go/parser`, no external
tool. Asserts:

- `internal/render` imports none of `herdr`, `git`, `proc`, `hooks`, and
  no function in it has a parameter of type `relay.Runtime`.
- `internal/relay` does not import `internal/render`, `internal/ui` or
  `internal/pick`.
- `internal/proc` is imported only by `cmd/relay` (it implements `relay.Runner`; `relay` never imports it).
- `internal/ui` and `internal/pick` are imported only by `cmd/relay`.
- No non-test Go file contains `#[0-9]+` inside a comment (enabled in
  Phase 6, once the rewrite is complete).

### 3c. Layout test

Same package. Every non-test file in `internal/relay` and `cmd/relay`
matches the allowed name set from §2 rule 2, and every source file has a
same-named `_test.go` unless listed in a small explicit exemption slice
(`errors.go`, `runtime.go`, `runner.go` and the like). A wrongly named
file fails with a message that quotes the rule. Lands in Phase 3 with the
renames.

Both tests are pure functions over the source tree; nothing executes a
subcommand, so they run on CI without `herdr` (CLAUDE.md, "Merging and
CI").

## 4. Target layout

### 4a. `internal/render` (new)

Terminal text only. Moves from `relay`, with their tests:

| file | functions |
|---|---|
| `status.go` | `RenderStatus`, `HoldText`, `NudgeText` |
| `statusline.go` | `RenderStatusLine`, `StatusLineWidth`, `AgeText` |
| `tab.go` | `RenderTab`, `TabRows` |
| `policy.go` | `FormatPolicy` |
| `candidates.go` | `FormatCandidates` |
| `text.go` | `DoneText`, `UnbindText`, `AnswerText`, `RestoreText`, `GateUntilText` |
| `diff.go` | `DriftSummary`, `DriftLine`, `DiffSummary`, `DiffLine` |

The types they render (`Report`, `BindingStatus`, `DoneResult`,
`Resolution`, `DriftResult`, `DiffResult`, `TabReport`...) stay in `relay`;
`render` imports `relay`, never the reverse. Every function above is called
only from `cmd/`, `ui/` or `pick/` today, so no cycle arises.

### 4b. `internal/report` (new)

`ParseReportTail` and `ReportTail`: the parser for the ` ```relay ` trailer
a builder ends its report with. Zero dependencies today; it is the
builder-side contract and deserves its own package doc.

`limit.go` was considered for extraction and rejected: it touches `Runtime`
nine times and is round logic. It becomes `round_limit.go`.

### 4c. `internal/relay` (restructured in place)

Same package, same exported API minus the moves above.

- `verb_*.go`, one planner command each: `verb_bind.go`, `verb_add.go`,
  `verb_fork.go`, `verb_unbind.go` (split out of `bind.go`),
  `verb_send.go`, `verb_ask.go`, `verb_answer.go`, `verb_done.go`,
  `verb_gc.go`, `verb_reap.go`, `verb_pull.go`, `verb_wait.go`,
  `verb_status.go` (`PlannerStatus`/`statusRow`: computation, not
  rendering), `verb_ledger.go` (unavailable/available).
- `round_*.go`, the daemon state machine: `round_reconcile.go`,
  `round_headless.go`, `round_consult.go`, `round_switch.go`,
  `round_escape.go`, `round_held.go`, `round_waiting.go`,
  `round_deliver.go`, `round_capture.go`, `round_drift.go`,
  `round_limit.go`, `round_foreign.go`, `round_dialog.go`,
  `round_scan.go`, `round_diagnose.go`, `round_usage.go`,
  `round_coverage.go`. Remote builders' `remote.go` becomes
  `round_remote.go` (or splits into a verb and a round file if plans 2--4
  give it both halves).
- Shared nouns, unprefixed, one concept each: `runtime.go` (`Runtime`,
  `Herdr`, `Git`; from today's `herdr.go`), `runner.go` (`Runner`,
  `ProcSpec`, `ProcHandle`), `errors.go` (every `Err*` variable),
  `daemon.go`, `candidate.go` (resolution), `protocol.go` (`builderPrompt`,
  `OriginLine`, `WithOrigin`, `LogLine`, `ExplainResolution`), `names.go`,
  `sort.go`.

**Orchestrator shape.** Every exported `verb_*` function reads as:
validate options -> resolve binding/endpoint -> act (herdr/git/store) ->
record (log entry, hooks) -> build result. Each arrow is a named unexported
function in the same file, under the caps. `Send` is the exemplar the Phase
4 plan spells out; `Fork`, `Ask`, `Add`, `Bind`/`resume` get the same
treatment in Phase 4, and `Reconcile`, `reconcileHeadless`,
`reconcileConsults`, `switchBuilder`, `queueReport`, `statusRow` in
Phase 5.

### 4d. `cmd/relay`

`main.go` keeps `main`, `run` and `usage`. New: `flags.go`; `runtime.go`
(`newRuntime`, `userConfigRoot`, `resolveHooksConfig`); `cmd_tree.go`
(bind/add/fork/unbind/gc/reap/done); `cmd_round.go`
(send/ask/answer/pull/diff/wait); `cmd_view.go`
(status/statusline/log/tab/ui); `cmd_policy.go`
(candidates/policy/unavailable/available); `cmd_daemon.go`. Existing
`doctor.go`, `tab.go`, `agent.go` and remote builders' `serve.go`,
`client.go` are renamed `cmd_*.go`. Tests mirror.

## 5. Comments and the decisions log

**Rewrite rule**, applied to every file a restructure round touches (not as
a separate sweep; Phase 6 covers the remainder):

- Doc comment = contract. First sentence names the symbol and says what it
  does; following sentences say what it requires and what it guarantees or
  refuses. Present tense.
- Inline comments only where the code cannot say it: a non-obvious
  ordering, a deliberate no-op, a boundary condition. Target is roughly
  half today's volume; review judges this, no tool does.
- A comment whose "why" a reader would otherwise reverse becomes
  `docs/decisions/NNNN-<slug>.md`, and the comment ends with
  `Decision: <slug>.`

**Decision entry**: at most one page, fixed headings -- *Context* (2--4
sentences), *Decision* (the rule, one paragraph), *Consequences* (what it
costs or forbids), *Supersedes* (optional). Numbered sequentially, never
edited after merge; a changed decision is a new entry that supersedes the
old one. `docs/decisions/README.md` indexes them, one line each.

**Seeding**: the 102 issue references are the candidate list. An issue
reference becomes a decision entry only if the comment explains a
constraint that is not derivable from the code and tests; otherwise the
reference is dropped and the sentence rewritten as a rule. Expect 15--25
entries. Specs in `docs/specs/` are unchanged; a decision may cite a spec
section but does not replace it.

## 6. Tests

- Mirror rule (§3c): `verb_send.go` <-> `verb_send_test.go`. The 2k-line
  files split along their sources' lines. Test bodies do not change; the
  move is `git mv` plus cut/paste, verified by an identical `go test -v`
  test count before and after.
- Fakes stay in `fake_test.go`; test-only helpers (binding builders, temp
  stores, clock) consolidate in `helpers_test.go`, deduplicating what the
  large files currently repeat.
- `internal/render` tests move with their functions; they are table-driven
  string tests and need no fakes.
- The 11 unexported functions tests reach into today (`appendLogMarker`,
  `create`, `dialogTail`, `effectiveStatus`, `fingerprint`, `gatedNote`,
  `inputEmpty`, `knownEndpoints`, `parseReset`, `resolveCandidate`,
  `withinTree`) keep their names and package so those tests do not move
  or change.
- Safety net: every round ends with `make check` and the test-count
  comparison. Phase 5 additionally runs `make e2e` (it touches
  `reconcile.go`'s nudge/fingerprint/scrape path) and a mutation check on
  `Reconcile` and `reconcileHeadless`: break one branch, name the test
  that fails.

## 7. Phasing

One plan per phase, one headless round each, strictly sequential (every
phase touches `cmd/relay` or importers of `internal/relay`). Each round is
verified by `make check` and a diff-vs-scope comparison before the next
plan is written. **Plans are not written until remote builders plans 2--4
have merged**; each phase is re-examined against that tree first.

| phase | scope | nature |
|---|---|---|
| 0 -- Rules | `docs/conventions.md`; `.golangci.yml` + `make check` with `--new-from-rev`; CI install step; `internal/arch` with the rules that already hold; `docs/decisions/README.md` + entry 0001 (worktree-and-halt rule) as template; CLAUDE.md pointer. No source changes. | may precede plans 2--4 |
| 1 -- Leaves | `internal/render`, `internal/report`; `protocol.go`; repoint `cmd`/`ui`/`pick`; arch test gains render rules. | pure move |
| 2 -- cmd split | `main.go` -> `main.go`/`flags.go`/`runtime.go`/`cmd_*.go`; side files renamed; tests mirrored. | pure move |
| 3 -- relay layout | `verb_`/`round_` renames; `bind.go` split; `errors.go`/`runtime.go`/`runner.go`; test files split and helpers deduped; layout test lands. | pure move; test count identical |
| 4 -- verbs | `Send`, `Fork`, `Ask`, `Add`, `Bind`/`resume` decomposed under the caps; comments in touched files rewritten; decisions extracted. | logic reshaped, behaviour pinned |
| 5 -- rounds | `Reconcile`, `reconcileHeadless`, `reconcileConsults`, `switchBuilder`, `queueReport`, `statusRow`; same comment rule; `make e2e`; mutation checks. | highest risk |
| 6 -- close | comment sweep over untouched files; decisions index complete; issue-ref rule on; linter whole-tree, `--new-from-rev` removed. | closes the loop |

Phases 1--3 are mechanical. Phases 4--5 are where the plan must spell out
the exemplar decomposition and where the halt rule matters most: a builder
that finds a decomposition impossible without changing behaviour stops and
reports rather than bending a test.

## 8. Failure modes

- **A move breaks an importer** (`ui`, `pick`, `proc`): the compiler
  catches it; the plan lists every consumer symbol from §4a so the builder
  repoints them in the same round.
- **A decomposition changes behaviour**: the existing suite is the pin;
  Phase 5's mutation check confirms the pin is real for the two functions
  whose control flow changes most.
- **The linter's caps are wrong for this code**: Phase 0 measures the
  whole-tree count. If Phase 4 or 5 finds a function that cannot honestly
  fit 60/15, the builder halts and the cap is revisited in the spec, not
  silenced with a `nolint`.
- **The layout test rejects a legitimate new file**: the exemption slice
  is the escape hatch, and adding to it is a visible diff a reviewer sees.
- **Comments lose a real "why"**: the seeding rule in §5 errs toward
  writing a decision; a dropped reference is still one `git log -S` away.
