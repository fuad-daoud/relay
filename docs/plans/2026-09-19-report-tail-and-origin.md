# Structured report tail and origin line (#133, #139)

Closes #133 and #139. Design approved in chat 2026-09-19; no separate spec.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`
(the planner runs it). Do not add a CLI verb or flag. Do not add a module
dependency. Run every command in the foreground; dispatch no sub-agents and
start no background work. Do not widen any exported signature that §4 does
not name -- if a test outside §2 stops compiling, halt and say which.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-report-tail-and-origin.md` in your worktree
and include it in the first commit.

## 1. System overview

Two changes to the round contract, both in existing paths.

**#133.** The builder ends its report with a fenced ```` ```relay ```` block
carrying `status`, `halted_at`, `changed_paths`, `commands_run`, `not_done`.
relay parses it once, leniently, at round close (`queueReport`) into new
fields on the report log entry. A round whose status is not `done` says so
in the first line of the payload the planner receives, in `relay status`,
in `relay log`, and as exit 5 from `relay wait`. A missing or malformed
block is `Outcome: unstructured` and the round closes exactly as today --
no nudge, no refusal. `changed_paths` is cross-checked against the diff's
file count and any mismatch is a note on the diff entry, never a refusal.

**#139.** Every payload relay types starts with one origin line naming
which agent it came from, produced by one helper and applied at exactly two
chokepoints: `composePrompt` (planner -> builder, pane and headless alike)
and `Queue` (anything -> planner: report, question, consult findings). The
report file and the dialog-question file are scanned for instruction-shaped
lines; the count goes on the log entry and into the payload; the files are
never altered.

## 2. File structure

```
internal/relay/
  reporttail.go            NEW  ReportTail type, ParseReportTail, outcome constants
  reporttail_test.go       NEW  parser fixtures
  origin.go                NEW  OriginLine helper
  origin_test.go           NEW  one case per direction/kind, idempotence
  scan.go                  NEW  ScanInstructionShaped, default pattern list
  scan_test.go             NEW  per-pattern fixtures, fenced false positive
  send.go                  EDIT builderPrompt text; composePrompt takes the binding name
  deliver.go               EDIT Queue prepends the origin line
  reconcile.go             EDIT queueReport parses tail, scans report, rewrites payload;
                                handleBlockedBuilder scans the question file
  headless.go              EDIT none beyond what queueReport already covers (verify only)
  wait.go                  EDIT WaitHalted = 5; WaitOutcome checks Outcome first
  status.go                EDIT LastEvent.Outcome; text form prints it
  statusline.go            EDIT "report in" gains the outcome
  logline.go               EDIT outcome= and flagged= on the first line
  send_test.go             EDIT first-line expectations
  deliver_test.go          EDIT payload expectations (origin prefix)
  reconcile_test.go        EDIT queueReport over seeded reports: outcome, flagged, paths note
  wait_test.go             EDIT exit 5 cases
  status_test.go           EDIT outcome in text form
  statusline_test.go       EDIT outcome in "report in"
  logline_test.go          EDIT outcome=/flagged=
  e2e_test.go              EDIT waitScreen string "Round 1 from the planner" -> "relay: round 1"
                                (do NOT run it; the planner runs make e2e)
internal/store/
  log.go                   EDIT LogEntry: Outcome, HaltedAt, ChangedPaths, CommandsRun, NotDone, Flagged
internal/policy/
  policy.go                EDIT Policy.ScanPatterns []string; Load validates each compiles
  policy_test.go           EDIT bad pattern -> ErrBadPolicy
internal/harness/agents/
  plan-executor.claude.md  EDIT OUTPUT FORMAT: the relay block skeleton + not_done prose
  plan-executor.agy.md     EDIT same
  plan-executor.opencode.md EDIT same
cmd/relay/
  main.go                  EDIT wait usage line: "5 halted/blocked per report"
README.md                  EDIT wait exit codes; new "The report block" subsection; policy scan_patterns
docs/plans/
  2026-09-19-report-tail-and-origin.md  NEW  this plan, copied in
```

Any test file in `internal/relay` not listed above that stops compiling
because of a signature change here is a halt, not a fix.

## 3. Data structures & type definitions

### 3.1 `relay.ReportTail` (`internal/relay/reporttail.go`)

| field | type | meaning | constraint |
|---|---|---|---|
| `Status` | `string` | one of the `Outcome*` constants below, never `unstructured` (that is the caller's word for "no tail") | required for `ok == true` |
| `HaltedAt` | `string` | where the builder stopped, free text | optional; meaningful when Status is halted/blocked |
| `ChangedPaths` | `[]string` | repo-relative paths the builder says it touched | optional; nil when absent; entries trimmed, empty ones dropped |
| `CommandsRun` | `[]string` | commands the builder ran | optional; as above |
| `NotDone` | `[]string` | adjacent work deliberately left | optional; as above |

Constants, all `string`:

```
OutcomeDone         = "done"
OutcomeHalted       = "halted"
OutcomeBlocked      = "blocked"
OutcomeDeferred     = "deferred"
OutcomeUnstructured = "unstructured"
```

`ReportTail` has no methods that touch the store; it is a value.

### 3.2 `store.LogEntry` additions (`internal/store/log.go`)

Appended after `Usage`, with a comment block in the style of the `Commits`/
`Tree` and `Usage` comments (say which kinds carry them, what zero means):

| field | json | type | on which entries | zero means |
|---|---|---|---|---|
| `Outcome` | `outcome,omitempty` | `string` | report | entry predates the field (readers treat "" like `unstructured`) |
| `HaltedAt` | `halted_at,omitempty` | `string` | report | none given |
| `ChangedPaths` | `changed_paths,omitempty` | `[]string` | report | none given |
| `CommandsRun` | `commands_run,omitempty` | `[]string` | report | none given |
| `NotDone` | `not_done,omitempty` | `[]string` | report | none given |
| `Flagged` | `flagged,omitempty` | `int` | report, question | nothing flagged (or predates the field) |

`Note` keeps its #117 vocabulary (`""`, `noreport`, `unmarked`, `scraped`,
`escaped`, joined forms) untouched. `Outcome` is orthogonal to `Note`.

### 3.3 `relay.LastEvent.Outcome` (`internal/relay/status.go`)

`Outcome string` with `json:"outcome,omitempty"`, copied from the entry the
event describes. Populated for both `Last` and `LastPayload`.

### 3.4 `policy.Policy.ScanPatterns` (`internal/policy/policy.go`)

`ScanPatterns []string` with `json:"scan_patterns,omitempty"`: extra
regular expressions appended to the built-in list. `Load` fails with
`ErrBadPolicy` and the message `scan_patterns[<i>]: <compile error>` when
one does not compile. Never replaces the built-in list.

### 3.5 `relay.WaitHalted` (`internal/relay/wait.go`)

`WaitHalted = 5`: the round closed and its report's `Outcome` is `halted` or
`blocked`. Documented in the constant block beside the others.

### 3.6 Built-in scan patterns (`internal/relay/scan.go`)

One package-level slice of compiled regular expressions, case-insensitive
where noted, applied per line:

1. `<system-reminder` (substring, any case)
2. `</?(human|assistant)>` (any case)
3. `^\s*(Human|Assistant|User):` (exact case)
4. `ignore (all )?(previous|prior) instructions` (any case)
5. `^\s*IMPORTANT:.*you must` (any case)

## 4. Interface definitions & component contracts

### 4.1 `ParseReportTail`

```
func ParseReportTail(report []byte) (ReportTail, bool)
```

Single responsibility: find and decode the builder's trailing relay block.
Pure; no I/O; never returns an error -- every failure is `ok == false`.

Preconditions: none (nil/empty input is a valid "no tail").

Postconditions when `ok == true`: `Status` is one of the four non-
`unstructured` constants; list fields are nil or non-empty with trimmed,
non-empty elements.

Rules (implement exactly these; each is a test case in §7 step 1):

- The block is the **last** fenced region in the report whose opening
  fence is three backticks followed by `relay` and nothing else on the line
  (trailing whitespace allowed). Its closing fence is the next line that is
  exactly three backticks (trailing whitespace allowed).
- After the closing fence only blank lines may follow; anything else ->
  `ok == false` (the block is not at the end).
- No opening fence, or an opening fence with no closing fence -> `ok == false`.
- Inside the block, each non-blank line is `key: value`. A `#` and
  everything after it is a comment **unless** inside a quoted scalar or
  inside `[...]`. Leading/trailing whitespace on key and value is trimmed.
- Scalar values: optionally wrapped in matching `"` or `'`; quotes
  stripped, no escape processing.
- List values: `[` ... `]` on one line, comma-separated, each element an
  optional-quoted scalar. An empty list `[]` is nil. A list key given a bare
  scalar is treated as a one-element list.
- Known keys: `status`, `halted_at`, `changed_paths`, `commands_run`,
  `not_done`. Unknown keys are ignored. A known key repeated: last wins.
- `status` missing, or not one of `done|halted|blocked|deferred` (exact,
  lowercase after trimming/unquoting) -> `ok == false`.
- Any line inside the block that is non-blank and has no `:` -> `ok == false`.

### 4.2 `OriginLine`

```
func OriginLine(name string, round int, dir store.Direction, kind store.Kind) string
```

Single responsibility: the one fixed first line for a typed payload. Pure.

Returns, by `(dir, kind)`:

- `DirToBuilder`, any kind:
  `relay: round <N> · to builder "<name>" · from the planner (not the human)`
- `DirToPlanner`, `KindReport` or `KindQuestion`:
  `relay: round <N> · to planner · about builder "<name>" (not the human)`
- `DirToPlanner`, `KindFindings`:
  `relay: consult · to planner · about builder "<name>" (not the human)`
- `DirToPlanner`, any other kind: same text as report/question (there is
  no such caller today; do not special-case).

The separator is ` · ` (space, U+00B7, space). No trailing newline.

```
func WithOrigin(payload, origin string) string
```

Returns `origin + "\n\n" + payload` unless `payload` already begins with
`relay: ` (after trimming leading whitespace), in which case `payload` is
returned unchanged. Pure. This is the idempotence guard: `Queue` may see an
entry a caller already prefixed.

### 4.3 `ScanInstructionShaped`

```
func ScanInstructionShaped(text []byte, extra []*regexp.Regexp) int
```

Single responsibility: count instruction-shaped lines. Pure.

Rules:

- Iterate lines. A line that is exactly three or more backticks (optionally
  followed by an info string) toggles "inside fence"; lines inside a fence
  are not scanned, and the fence lines themselves are not scanned.
- A line outside a fence counts **once** if any built-in pattern (§3.6) or
  any `extra` pattern matches it, however many match.
- Returns the count; 0 for empty input.

```
func compileScanPatterns(p policy.Policy) []*regexp.Regexp
```

Unexported helper in `scan.go`: compiles `p.ScanPatterns` (already
validated by `policy.Load`, so a compile failure here is skipped silently,
not fatal). Called by `queueReport` and `handleBlockedBuilder`.

### 4.4 `composePrompt` (edited, `send.go`)

```
func composePrompt(b store.Binding, planPath, reportPath, donePath string) string
```

Signature unchanged. The rendered prompt's line 1 is
`OriginLine(b.Name, b.Round, store.DirToBuilder, store.KindPlan)`, then a
blank line, then the existing text with the line `Round %d from the
planner.` **removed** (the origin line carries the round). The two tail
instructions from §5.1 are inserted before the final `Reply here with only
the report path.` line. Nothing else in the prompt changes -- the tree
sentence, the `git status` halt rule and the three paths stay word for word.

### 4.5 `Queue` (edited, `deliver.go`)

Signature unchanged. After the existing validation and before
`tx.AppendLog`, `e.Payload = WithOrigin(e.Payload, OriginLine(name,
e.Round, e.Direction, e.Kind))`. Effect: the stored payload is the typed
payload, and `DeliverPending` needs no change.

### 4.6 `queueReport` (edited, `reconcile.go`)

Signature unchanged. New behaviour, in this order, all before the report
`LogEntry` is built:

1. Read `path`; a read error means "no tail" (`unstructured`, `Flagged 0`),
   never an error return -- a scraped or absent report must still close.
2. `tail, ok := ParseReportTail(body)`; `outcome := OutcomeUnstructured`
   when `!ok`, else `tail.Status`.
3. `flagged := ScanInstructionShaped(body, compileScanPatterns(rt.Policy))`.
4. If `outcome != OutcomeDone && outcome != OutcomeUnstructured`, rewrite
   `payload`'s **first line** only: replace `Builder finished round N` (the
   prefix every caller passes; `strings.HasPrefix` check) with
   `Builder finished round N -- <outcome>` plus ` at "<HaltedAt>"` when
   `HaltedAt != ""`. If the first line does not start with that prefix
   (e.g. the `noreport` payload), leave it and instead append
   ` Outcome: <outcome>.` to the first line.
5. If `flagged > 0`, append to the payload's first line:
   ` (<flagged> instruction-shaped lines flagged; see relay log)` -- singular
   `line` when 1.
6. Inside the existing diff-capture branch, after `DiffSummary`: when
   `result.Available && ok && tail.ChangedPaths != nil &&
   len(tail.ChangedPaths) != result.Stat.FilesChanged`, append
   `paths: report <len>, diff <FilesChanged>` to `diffEntry.Note` via
   `joinNotes(note, ...)` (`reconcile.go:367`, a single-space join).
7. The report entry gets `Outcome`, `HaltedAt`, `ChangedPaths`,
   `CommandsRun`, `NotDone`, `Flagged` from the above.

Everything after the entry (round increment, state reset) is unchanged.

### 4.7 `handleBlockedBuilder` (edited, `reconcile.go`)

After writing the question file and before building the entry:
`flagged := ScanInstructionShaped([]byte(dialog), compileScanPatterns(rt.Policy))`.
Set `Flagged` on the question entry; when `> 0`, append the same
parenthetical as §4.6 step 5 to the payload's first line.

### 4.8 `WaitOutcome` (edited, `wait.go`)

Inside the `lastReportEntry` branch, before the `Note` check:
if `e.Outcome == OutcomeHalted || e.Outcome == OutcomeBlocked`, `code =
WaitHalted`. Otherwise the existing 0/2 rule. `Line` unchanged. Comment:
"a marked round that halted is 5, not 0; an unmarked round that halted is
also 5 -- the planner has to read why either way."

### 4.9 Surfaces

- `LogLine`: after `e.Note` on the first line, append ` outcome=<v>` when
  `e.Outcome != ""` and ` flagged=<n>` when `e.Flagged > 0`. Order:
  note, outcome, flagged, then the existing ` late`.
- `statusRow` (`status.go`): copy `Outcome` into both `LastEvent`s.
- Status text (`status.go` ~line 556): after the `(note)` parenthetical,
  print ` <outcome>` when non-empty and not `done` (a `done` outcome is the
  quiet case). `unstructured` is printed -- the planner should see that the
  builder did not follow the contract.
- `statusline.go` `KindReport` case: `report in`, `report in (<note>)`,
  and when Outcome is non-empty and not `done`: `report in · <outcome>` /
  `report in (<note>) · <outcome>`.
- `cmd/relay/main.go` usage line for `wait`: add `5 halted/blocked per
  report` between `2 unmarked` and `3 needs you`, matching the existing
  terse style.
- README `relay wait` bullet: add `5: closed, but the builder's report
  says halted or blocked -- read it before sending again; stdout is the
  report path.`

## 5. High-level pseudocode

### 5.1 Prompt text added to `builderPrompt`

Insert, verbatim, before the final `Reply here...` line (the `%` signs in
the existing format string are unaffected; this block contains none):

````
End the report with this block as its last lines, filled in honestly:

```relay
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # commands you ran, e.g. ["make check"]
not_done: []            # adjacent work you deliberately left
```
````

The same skeleton, preceded by one sentence -- "End every report with the
`relay` block relay's prompt shows you; list under `not_done` anything
adjacent you deliberately did not do, because that is where reviewers find
surprises." -- goes at the end of OUTPUT FORMAT in each of the three
plan-executor definitions. Change nothing else in those files.

### 5.2 Round close

```
queueReport(path, payload, note):
  body      <- read(path) or empty
  tail, ok  <- ParseReportTail(body)
  outcome   <- ok ? tail.Status : unstructured
  flagged   <- ScanInstructionShaped(body, extra(policy))
  if outcome not in {done, unstructured}:
      payload.firstLine <- annotate(payload.firstLine, outcome, tail.HaltedAt)
  if flagged > 0:
      payload.firstLine <- payload.firstLine + " (N instruction-shaped lines flagged; see relay log)"
  if no diff entry yet for this round:
      result, facts <- capture as today
      note <- DiffSummary(result, facts)
      if result.Available and ok and tail.ChangedPaths != nil
         and len(tail.ChangedPaths) != result.Stat.FilesChanged:
          note <- joinNotes(note, "paths: report X, diff Y")
      append diff entry
      payload <- payload + "\n" + DiffLine(...)          (unchanged)
  entry <- report entry as today + Outcome/HaltedAt/ChangedPaths/CommandsRun/NotDone/Flagged
  Queue(entry)                                            (Queue prepends the origin line)
  advance round as today
```

### 5.3 Wait

```
WaitOutcome:
  if report entry for round exists:
      code <- 0
      if entry.Outcome in {halted, blocked}: code <- 5
      else if entry.Note != "":             code <- 2
      ... rest unchanged
```

## 6. Error handling strategy

- **Parser**: never errors. Every malformed shape is `ok == false`, which
  the caller records as `unstructured`. This is the lenient contract from
  the issue; do not add a nudge, a refusal or a log line for it.
- **Report read failure** in `queueReport`: treated as empty body. The
  existing close paths (`noreport`, `scraped`) already cover the human-
  visible side.
- **Scan**: pure; cannot fail. A policy pattern that fails to compile is
  refused at `policy.Load` with `ErrBadPolicy`; `compileScanPatterns`
  skips anything that somehow still fails.
- **Origin line**: pure; `WithOrigin` is idempotent so a double application
  is harmless.
- **Cross-check mismatch**: a note, never a refusal, never a state change.
- Nothing in this plan changes `State`, `WaitingOn` or NEEDS YOU. A
  `blocked` report outcome does **not** flip the binding to NEEDS YOU
  (decided on the issue); exit 5 and the payload's first line carry it.

## 7. Ordered implementation steps

Run `go test ./internal/relay/ ./internal/store/ ./internal/policy/` after
every step; run `make check` at steps 9 and 10. Tests that create git repos
(none are expected here) must pass `-c commit.gpgsign=false`.

1. **`internal/store/log.go`** -- add the six fields (§3.2) with comments.
   Verify: `go build ./...` passes; `go test ./internal/store/` passes.

2. **`internal/relay/reporttail.go` + `_test.go`** -- §3.1 types,
   constants, `ParseReportTail` per §4.1. Tests, one per rule: complete
   block with every key; block with only `status: done`; each of the four
   statuses; unknown status -> false; missing block -> false; block not at
   the end (prose after the closing fence) -> false; an earlier ```` ```relay ````
   block followed by a later one -> the later one wins; unclosed fence ->
   false; unknown key ignored; repeated key last wins; `#` comments outside
   and inside quotes; `[]` -> nil; bare scalar for a list key -> one
   element; a line without `:` -> false; a report whose *prose* contains a
   ```` ```go ```` fence before the relay block still parses. Verify: tests
   pass; mutation: remove the "only blank lines after the fence" check and
   confirm the not-at-end test fails.

3. **`internal/relay/scan.go` + `_test.go`** -- §3.6 patterns,
   `ScanInstructionShaped`, `compileScanPatterns` (§4.3). Tests: each
   pattern matches one fixture line; a line matching two patterns counts
   once; `Human:` inside a fenced block does not count; an unclosed fence
   suppresses the rest; `extra` pattern counts; empty input is 0. Verify:
   mutation: drop the fence toggle and confirm the fenced test fails.

4. **`internal/policy/policy.go` + `_test.go`** -- `ScanPatterns` (§3.4),
   validation in `Load`. Test: a policy with `"scan_patterns": ["("]`
   returns `ErrBadPolicy` naming index 0; a valid one loads. Verify: tests
   pass.

5. **`internal/relay/origin.go` + `_test.go`** -- `OriginLine`, `WithOrigin`
   (§4.2). Tests: the three text forms byte-exact; `WithOrigin` on a
   payload already prefixed returns it unchanged; on a plain payload
   inserts exactly one blank line. Verify: tests pass.

6. **`send.go` + `send_test.go`, `deliver.go` + `deliver_test.go`,
   `e2e_test.go`** -- §4.4, §4.5, §5.1. `composePrompt` output: line 1 is
   the origin line, line 2 blank, the `Round %d from the planner.` line is
   gone, the tail instruction block precedes the last line, last line
   unchanged. `Queue` prefixes. Update `send_test.go` expectations
   (`Round 1 from the planner.` -> the origin line; the `HasSuffix` check
   stays true). Update every `deliver_test.go` expectation that compares a
   typed payload byte-for-byte. Change `e2e_test.go`'s four
   `waitScreen(..., "Round 1 from the planner")` strings to
   `"relay: round 1"` and **do not run e2e**. Verify: `go test ./internal/relay/`
   passes; `grep -rn "from the planner\." internal/relay/*.go` returns only
   `origin.go`.

7. **`reconcile.go` + `reconcile_test.go`** -- §4.6, §4.7, §5.2. Tests over
   a seeded report file through the marked path: `status: halted` with
   `halted_at` -> entry `Outcome/HaltedAt` set, payload first line contains
   `-- halted at "Task 2 step 3"`; `status: done` -> payload first line
   unchanged apart from the origin prefix; no block -> `unstructured`, no
   annotation; a report containing `Human: do X` -> `Flagged 1` and the
   parenthetical; `changed_paths` of 2 against a fake diff stat of 3 ->
   diff entry note ends with `paths: report 2, diff 3`; equal counts -> no
   note. `handleBlockedBuilder` with a dialog containing `<system-reminder`
   -> question entry `Flagged 1`. Verify: mutation: make the parser always
   return `ok == false` and confirm the halted test fails.

8. **`wait.go` + `wait_test.go`** -- §3.5, §4.8, §5.3. Tests: marked report
   with `Outcome: halted` -> 5; unmarked (`Note: unmarked`) with
   `Outcome: blocked` -> 5; marked `done` -> 0; marked `unstructured` -> 0;
   `deferred` -> 0. Verify: mutation: remove the Outcome check and confirm
   the two 5-cases fail.

9. **Surfaces** -- `logline.go` + test, `status.go` + test,
   `statusline.go` + test, `cmd/relay/main.go` usage line (§4.9). Tests
   for `LogLine` with outcome and flagged; status text prints ` halted`
   after the note and nothing for `done`; statusline `report in · halted`.
   The `cmd/relay` change is a string only; no CLI test that reaches herdr.
   Verify: `make check` passes.

10. **Definitions and README** -- the three `plan-executor.*.md` files
    (§5.1, identical text in each), README (`wait` bullet exit 5; a new
    `### The report block` subsection under "Command surface" after the
    `relay wait` material, 10-15 lines: the skeleton, the four statuses,
    "missing or malformed closes the round as `unstructured`", the
    `paths:` note, and `scan_patterns` under `### Policy`), and copy this
    plan into `docs/plans/`. Verify: `make check` passes (it runs `gofmt -l`
    over the tree, `go vet`, `go mod tidy` check). Use exactly
    `test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }` if you check
    gofmt by hand; the form without `exit 1` cannot fail.

**Report.** List per step: done / halted (with the step and reason). Name
every file changed. State the `make check` result verbatim. End the report
with the `relay` block from §5.1 -- yes, this round is the first consumer of
its own contract; a missing block is fine and closes as `unstructured`.
