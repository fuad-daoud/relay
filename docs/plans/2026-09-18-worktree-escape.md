# Worktree escape: pin, name, detect, exclude (#192, #191)

Closes #192 and #191. Design approved in chat 2026-09-18; no separate spec.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`.
Do not edit the claude or opencode plan-executor definitions. Do not add a
CLI verb or flag. Run every command in the foreground; dispatch no
sub-agents.

## 1. System overview

On 2026-09-18 a headless agy builder, started with `cmd.Dir` set to its
worktree, ran its shell somewhere else, `cd`-ed into the planner's main
checkout and executed the whole plan there. relay saw a clean worktree, an
exit with no report, and re-dispatched the same candidate twice more onto
the same dirty main checkout. Four changes close this, all inside existing
paths:

1. **Pin** -- the agy print form passes the round's directory as
   `--add-dir`, so agy's workspace is the worktree.
2. **Name** -- every round prompt (pane and headless) states the absolute
   working tree and a halt rule: `git status` there first; if it fails or
   shows another tree, stop and report.
3. **Detect** -- at every headless round close, if the round's tree
   snapshot equals its baseline (nothing changed in the worktree) while the
   binding's source repo is dirty, the round is marked `escaped`: a note on
   the report entry when there is a report, a `NEEDS YOU` halt (no switch)
   when the process exited without one.
4. **Exclude** -- a headless builder that exits without a report is excluded
   from the pick for the rest of that round, so the switch lands on a
   different candidate or halts when none is left.

Plus the agy plan-executor definition stops dispatching sub-agents and
running background work, because an idle root agent is an exit.

## 2. File structure

```
internal/harness/harness.go                 DirPlaceholder; agy print args gain --add-dir; PrintArgs(prompt, budget, dir)
internal/harness/harness_test.go            PrintArgs fills dir; agy Print contains --add-dir <dir>; claude/opencode unchanged
internal/harness/agents/plan-executor.agy.md  no sub-agents, foreground only, report before marker
internal/relay/headless.go                  headlessLaunch passes b.CWD; escape check on the three closes; exclusion before switch
internal/relay/headless_test.go             tests for launch argv, escape note, escape halt, exclusion
internal/relay/escape.go                    NEW: escapeOutcome (pure) + escapeCheck (runs git) + escapeDiagnosis text
internal/relay/escape_test.go               NEW: table test for escapeOutcome (mutation-pinned)
internal/relay/send.go                      builderPrompt names the tree + halt rule; composePrompt takes the tree path
internal/relay/send_test.go                 prompt contains tree path and halt rule
internal/relay/reconcile.go                 closeOnMarker takes extraNote; finishRound clears RoundExcluded; joinNotes helper
internal/relay/reconcile_test.go            pane path passes "" and is unchanged (existing tests keep passing)
internal/relay/switch.go                    switchBuilder merges RoundExcluded into the gate list
internal/relay/switch_test.go               excluded candidate is skipped; all-excluded halts
internal/relay/candidate.go                 (no change expected; skipsFor already renders any Gate)
internal/relay/ledger.go                    GateKindText case for ledger.ExitedNoReport
internal/ledger/ledger.go                   Kind ExitedNoReport = "exited_no_report"
internal/store/types.go                     Binding.Repo, Binding.RoundExcluded
internal/relay/add.go                       writes Binding.Repo = opts.Repo
internal/relay/fork.go                      writes Binding.Repo = src.Repo (both bindings it constructs)
```

Read before editing: `internal/relay/headless.go` (whole file),
`internal/relay/reconcile.go` lines 370-400 and 600-645,
`internal/relay/switch.go` lines 80-140, `internal/relay/candidate.go`
lines 130-240, `internal/harness/harness.go` lines 260-360,
`internal/relay/send.go` lines 40-50 and 265-275, `internal/store/types.go`
lines 70-200.

## 3. Data structures & type definitions

### 3.1 `store.Binding` (internal/store/types.go) -- two new fields

| Field | Type | JSON | Meaning |
|---|---|---|---|
| `Repo` | `string` | `repo,omitempty` | The source checkout the worktree was cut from (the caller's cwd at `add`/`fork`). Empty for `--cwd`, adopted and pre-field bindings; empty disables escape detection. Placed directly after `Base`. |
| `RoundExcluded` | `[]string` | `round_excluded,omitempty` | Candidate tokens (canonical ref strings, as `BuilderCandidate` holds them) that exited without a report during the CURRENT round. Read by `switchBuilder`, cleared by `finishRound` next to `RoundSwitches`. Placed directly after `RoundSwitches`. |

Both `omitempty`: every bind.json written before the fields existed stays
byte-identical until the field is first set (same reasoning as `Consults`).

### 3.2 `ledger.Kind` (internal/ledger/ledger.go)

Add `ExitedNoReport Kind = "exited_no_report"` beside `SpawnFailed` and
`RateLimited`, with a doc comment: never written to the ledger file; only
synthesised in memory by `switchBuilder` from `Binding.RoundExcluded`.
`GateKindText` (internal/relay/ledger.go) renders it as
`"exited without a report"`.

### 3.3 `EscapeOutcome` (internal/relay/escape.go)

```
type EscapeOutcome int
const (
    EscapeNone EscapeOutcome = iota   // nothing to say
    EscapeNote                        // annotate the report entry with "escaped"
    EscapeHalt                        // halt NEEDS YOU instead of switching
)
const escapeNote = "escaped"
```

### 3.4 `harness.Launch` (internal/harness/harness.go)

New placeholder constant `DirPlaceholder = "<dir>"` next to
`PromptPlaceholder` and `BudgetPlaceholder`. The agy print form becomes:

```
{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition,
 "--output-format", "stream-json", "--print-timeout", BudgetPlaceholder,
 "--add-dir", DirPlaceholder}
```

claude and opencode print forms are unchanged.

## 4. Interface definitions & component contracts

### 4.1 `harness.Launch.PrintArgs`

```
func (l Launch) PrintArgs(prompt string, budget time.Duration, dir string) []string
```
- Fills `PromptPlaceholder`, `BudgetPlaceholder` and `DirPlaceholder`.
  A kind whose Print has no `DirPlaceholder` ignores `dir` (same rule as
  budget). Returns a fresh slice.
- Precondition: `dir` is absolute or empty. Postcondition: no placeholder
  string remains in the result.
- Update every caller (`headlessLaunch` in headless.go and any test) --
  `grep -rn 'PrintArgs(' internal cmd` must show only the new arity.

Doc comment on `case "agy"` must record: *2026-09-18 probe: agy's stream
`init` event reports `cwd` = the process directory, yet its first
`run_command` ran outside any repository and the model `cd`-ed into the
planner's main checkout (#192). `--add-dir <cwd>` pins the workspace;
`--project`/`--new-project` were not used because they name agy-side
project records, not a directory.*

### 4.2 `relay.headlessLaunch`

```
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, budget time.Duration, prompt, dir string) ([]string, error)
```
Passes `dir` through to `PrintArgs`. The one caller (`startRound`) passes
`b.CWD`.

### 4.3 `relay.composePrompt` and `builderPrompt` (send.go)

```
func composePrompt(b store.Binding, planPath, reportPath, donePath string) string
```
Signature unchanged; it now also interpolates `b.CWD`. `builderPrompt`
becomes (exact text; `%s` order: tree, round, plan, report, done):

```
Your working tree is: %s
It is the only tree you may touch. Before anything else, run `git status`
there. If that fails, or reports a different directory or branch than you
expect for this tree, stop: write a report saying so, create the done marker,
and do nothing else.

Round %d from the planner.
Read: %s
When you are done, write your report to: %s
Then, as the very last thing you do -- after every edit, test and commit --
create this empty file: %s
Reply here with only the report path.
```

Update the doc comment: "Nothing is interpolated except the tree, the round
number and the three paths."

### 4.4 `relay.escapeOutcome` (escape.go) -- pure, the mutation target

```
func escapeOutcome(treeUnchanged, repoDirty, hasReport bool) EscapeOutcome
```
- `!treeUnchanged || !repoDirty` -> `EscapeNone`
- `treeUnchanged && repoDirty && hasReport` -> `EscapeNote`
- `treeUnchanged && repoDirty && !hasReport` -> `EscapeHalt`

### 4.5 `relay.escapeCheck` (escape.go) -- gathers the inputs

```
func escapeCheck(ctx context.Context, rt Runtime, b store.Binding, hasReport bool) EscapeOutcome
```
Preconditions for running at all (otherwise return `EscapeNone` without
touching git): `b.Builder.Headless()`, `b.Repo != ""`,
`b.RoundBaselineTree != ""`, `rt.Git != nil`.
- `treeUnchanged` := `rt.Git.SnapshotTree(ctx, b.CWD) == b.RoundBaselineTree`.
  Any error -> `EscapeNone` (log at Warn, never fail the close).
- `repoDirty` := `rt.Git.Dirty(ctx, b.Repo)`. Any error -> `EscapeNone`
  (log at Warn).
- Returns `escapeOutcome(treeUnchanged, repoDirty, hasReport)`.

### 4.6 `relay.escapeDiagnosis` (escape.go)

```
func escapeDiagnosis(b store.Binding, codeText string) string
```
Returns
`"<name>: builder exited (code <code>) without a report; worktree <cwd> unchanged since the round began while <repo> is dirty -- the builder likely worked outside its tree; see <logpath>"`.

### 4.7 `relay.joinNotes` (reconcile.go)

```
func joinNotes(a, b string) string
```
Space-joins the non-empty ones (`"unmarked"` + `"escaped"` ->
`"unmarked escaped"`; `""` + `"escaped"` -> `"escaped"`).

### 4.8 `relay.closeOnMarker` (reconcile.go)

```
func closeOnMarker(ctx, rt, tx, b, entries, extraNote string) (store.Binding, bool, error)
```
Both `queueReport` calls inside pass `joinNotes(<existing note>, extraNote)`.
The pane caller (reconcile.go ~line 233) passes `""`; the headless caller
passes the escape note computed in §5.2.

### 4.9 `relay.switchBuilder` (switch.go)

Signature unchanged. The `resolveCandidate` call passes
`append(Gates(rt), roundExclusionGates(b)...)` where

```
func roundExclusionGates(b store.Binding) []ledger.Gate
```
returns one `ledger.Gate{Token: t, Kind: ledger.ExitedNoReport, Since: <zero>, Until: <zero>, Note: "round " + itoa(b.Round), Binding: b.Name}` per `t` in `b.RoundExcluded`. Pure.

`gatedBuilder` is NOT changed: it looks only at `RateLimited`, so an
exclusion never triggers a switch by itself.

### 4.10 `relay.reconcileHeadless` exit-without-report branch (headless.go ~381-397)

Before `switchBuilder`: append `b.BuilderCandidate` to `b.RoundExcluded`
if not already present. Before that, run the escape check (§5.2).

## 5. High-level pseudocode

### 5.1 startRound (headless.go)
```
argv, err := headlessLaunch(c, role, roundBudget(b), prompt, b.CWD)
```
Nothing else changes.

### 5.2 reconcileHeadless closes
```
// marker close (line ~302)
note := ""
if escapeCheck(ctx, rt, b, hasReport=true) == EscapeNote { note = escapeNote }
next, closed, err := closeOnMarker(ctx, rt, tx, b, entries, note)
...

// exited with a report but no marker (line ~357)
note := "unmarked"
if escapeCheck(ctx, rt, b, true) == EscapeNote { note = joinNotes(note, escapeNote) }
... queueReport(..., note)

// exited without a report (line ~381)
append exit log entry (unchanged)
if escapeCheck(ctx, rt, b, false) == EscapeHalt:
    return haltBinding(ctx, rt, b, escapeDiagnosis(b, codeText))
gateOnLimit (unchanged)
if !switchable: halt (unchanged)
b.RoundExcluded = appendUnique(b.RoundExcluded, b.BuilderCandidate)
return switchBuilder(ctx, rt, tx, b, "exited (code N) without a report", false, true)
```
Order matters: the escape halt comes before `gateOnLimit` so a limit line
in the log of an escaped round does not turn a halt into a switch. The
exclusion is appended to the `b` that `switchBuilder` receives so the
replacement inherits it and the field is persisted with the switch.

Note on the marker-close path: `escapeCheck` runs before `closeOnMarker`
queues the report because `queueReport` clears `RoundBaselineTree`; the
snapshot must be compared while the baseline is still on the binding.

### 5.3 switchBuilder pick
```
gates := append(Gates(rt), roundExclusionGates(b)...)
res, err := resolveCandidate(rt.Candidates, rt.Policy, gates, "", "builder")
if err != nil: haltBinding(... "cannot switch: <err>")   // unchanged; ErrAllGated names the exclusions via skipText
```

### 5.4 finishRound
```
b.RoundSwitches = 0
b.RoundExcluded = nil
```

### 5.5 add / fork
`add.go` binding literal: `Repo: opts.Repo`. `fork.go`: both binding
literals get `Repo: src.Repo` (a fork's tree is cut from `src.CWD`, but the
*source repo* of the lineage is `src.Repo`; when `src.Repo == ""` the fork's
stays empty).

### 5.6 agy plan-executor definition
- Remove `invoke_subagent` and `manage_subagents` from `tools:`.
- Replace the "Research delegation protocol" and "Research sub-agent
  prompting standards" sections with one section **"One process, in the
  foreground"** stating: you read files yourself, sequentially; you never
  dispatch a sub-agent of any kind; every command runs in the foreground and
  you wait for it (no background tasks, no `&`, no detached `make e2e`);
  and the report file is written before the done marker, which is the last
  action. Give the reason in one paragraph: on agy an idle root agent is an
  exit, and relay treats an exit without a report as a failed builder and
  switches (#191).
- Rewrite "Why the tools list is this" so it no longer mentions
  `invoke_subagent`/`manage_subagents` or `researcher`.
- Keep every other section verbatim.

## 6. Error handling strategy

| Situation | Category | Handling |
|---|---|---|
| `SnapshotTree`/`Dirty` fails inside `escapeCheck` | recoverable, observational | `slog.Warn("escape check skipped", ...)`, return `EscapeNone`. Detection is a tell, never a reason to fail a close. |
| `b.Repo` empty (old/`--cwd`/adopted binding) | not an error | `EscapeNone`, no git call, no log line. |
| Escape + no report | non-recoverable for the round | `haltBinding` with `escapeDiagnosis`; state NEEDS YOU; no switch, no re-dispatch. Human clears with a rebind or `relay done`. |
| Escape + report | recoverable | note `escaped` on the report entry; status/statusline render it via the existing `Note` path (`report in (escaped)`). |
| Every candidate excluded or gated after an exit-without-report | non-recoverable for the round | existing `ErrAllGated` path: `haltBinding("... cannot switch: every candidate serving "builder" is gated: <token> (exited without a report until cleared) ...")`. |
| `PrintArgs` given an empty `dir` | not an error | agy gets `--add-dir ""`; do not special-case -- `startRound` always has `b.CWD`. |

Logging: one `slog.Warn("worktree escape suspected", "binding", "round", "worktree", "repo")` whenever `escapeCheck` returns anything but `EscapeNone`.

## 7. Ordered implementation steps

Each step ends with `go build ./... && go test ./internal/...` green unless
it says otherwise. Do not proceed past a red step.

**Step 1 -- store fields.** Add `Binding.Repo` and `Binding.RoundExcluded`
(§3.1) with doc comments. Add `ledger.ExitedNoReport` (§3.2) and the
`GateKindText` case. Verify: `go build ./...`; a test in
`internal/relay/ledger_test.go` (or the file where `GateKindText` is
already tested -- grep for it) asserts `GateKindText(ledger.ExitedNoReport) == "exited without a report"`.

**Step 2 -- add/fork write Repo.** §5.5. Verify: extend the existing
add and fork tests that inspect the returned `Binding` to assert `Repo`
equals the repo they passed in; run `go test ./internal/relay -run 'Add|Fork'`.

**Step 3 -- harness placeholder.** §3.4 and §4.1, including the `case "agy"`
comment. Update `headlessLaunch` (§4.2) and its caller (§5.1). Verify in
`internal/harness/harness_test.go`: agy `Launch(...).Print` contains the
adjacent pair `"--add-dir", DirPlaceholder`; `PrintArgs("p", 90*time.Minute, "/w")`
for agy contains `"--add-dir", "/w"` and no placeholder; claude and opencode
`PrintArgs(..., "/w")` do not contain `"/w"`. In `headless_test.go`, the
existing launch-argv test gains an assertion that the agy argv ends with the
binding's CWD after `--add-dir`.

**Step 4 -- prompt names the tree.** §4.3. Verify in `send_test.go`: the
composed prompt's first line is `Your working tree is: <b.CWD>` and it
contains `create the done marker, and do nothing else.`; every existing
prompt-shape assertion in `send_test.go`, `headless_test.go` and
`e2e_test.go` still passes (fix the expectations that quote the old first
line, do not weaken them).

**Step 5 -- escape decision, pure.** Create `escape.go` with §3.3, §4.4,
§4.6 and `escape_test.go` with a table over all eight input combinations of
`escapeOutcome`. Name the test `TestEscapeOutcome`. Mutation check, run by
you and reported: temporarily change the function to ignore
`treeUnchanged`; `TestEscapeOutcome` must fail; restore it. Do not run
this check by reverting the whole file -- edit the one condition.

**Step 6 -- escape check and closes.** Add §4.5 and §4.7; change
`closeOnMarker` (§4.8) and its pane caller (passes `""`); wire the three
headless closes (§5.2). Verify in `headless_test.go` with the fake Git
(look at how existing headless tests fake `SnapshotTree`/`Dirty`; extend
the fake if `Dirty` is not yet fakeable):
- `TestHeadlessMarkerCloseEscapedNote`: baseline tree == snapshot, `Repo`
  dirty, report + marker present -> report entry `Note == "escaped"`.
- `TestHeadlessExitNoReportEscapedHalts`: same tree facts, process exited,
  no report -> binding halted, `Halt` contains `worked outside its tree`,
  no switch entry in the log, `RoundSwitches` unchanged.
- `TestHeadlessExitNoReportNoRepoSwitches`: `Repo == ""`, same exit ->
  the existing switch behaviour (this pins that old bindings are untouched).

**Step 7 -- round exclusion.** §4.9, §4.10, §5.3, §5.4. Verify in
`switch_test.go`/`headless_test.go`: with two builder candidates A (first
in order) and B, an A exit-without-report switches to B and
`RoundExcluded == [A]`; a following B exit-without-report halts with a
message containing `exited without a report`; after `finishRound`,
`RoundExcluded` is nil. Mutation check, reported: make `roundExclusionGates`
return nil and confirm the first of those tests fails; restore.

**Step 8 -- agy definition.** §5.6. Verify: `grep -c subagent
internal/harness/agents/plan-executor.agy.md` counts only the
`subagent: false` frontmatter line and the "Why this agent cannot be a
sub-agent" section; `grep -n 'invoke_subagent\|manage_subagents\|researcher'`
returns nothing. Any test that loads the agy definition and checks its
tool list (grep `plan-executor.agy` in `*_test.go`) is updated to the new
list.

**Step 9 -- whole-tree check.** Run `make check` (gofmt over the tree, vet,
tidy, tests). Fix formatting only. Then commit on the current branch with a
conventional message
`fix(headless): pin agy to its worktree, name the tree in every round, detect escapes, exclude an exit-without-report from the round's pick (#192, #191)`
and the attribution trailer `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

**Local-only, not for you:** the live agy round that proves `--add-dir`
makes the builder's first `git status` report `On branch relay/<name>`
needs the `agy` binary and quota; the planner runs it. State in your report
that this verification is pending.

## Report

List per step: COMPLETED AS WRITTEN / COMPLETED WITH NOTES / BLOCKED. Include
the two mutation-check outcomes verbatim (which test failed, then passed),
`git diff --stat`, and the `make check` tail.
