# Status line edges: margin, payload vocabulary, short harness, tty stdin (#152, #154, #155)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-14-statusline-edges-design.md`. Section numbers
below (§) refer to it; `09-13 §N` refers to
`docs/specs/2026-09-13-statusline-design.md`.
**Issues:** #152, #154, #155. Closes all three.
**Depends on:** nothing open.

**Goal:** Four fixes to `relay statusline` from its first night of use: the
verb subtracts Claude Code's 4-cell chrome from `COLUMNS` (overridable by
`RELAY_STATUSLINE_MARGIN`); the middle column reads the last *payload* entry
(plan/report/question/answer) instead of whatever relay logged last, so
`drift` never shows; the builder cell is the harness segment of the token
(`agy`, not `agy/google/gemini-3.8-flash-high`); stdin is drained only when
it is not a terminal, so the verb returns when typed by hand.

**Architecture:** One additive field, `BindingStatus.LastPayload`, set in
`statusRow` beside `Last`. Two pure functions in `statusline.go`,
`StatusLineWidth` and `ShouldDrainStdin`, called from `cmdStatusline`.
`RenderStatusLine`'s signature and layout do not change; `waiting`/`age`
switch from `Last` to `LastPayload` and the builder cell goes through a
`harness()` helper. `RenderStatus`, the TUI and the store are untouched.

**Tech stack:** Go 1.22. Verification is the `make check` constituent set
(runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/statusline-edges` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/statusline-edges`, cut from `main`. |
| `~/.local/state/relay/statusline-edges` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself. **No test is added under `cmd/relay`** (CI
runners have no `herdr`; CLAUDE.md). All logic under test lives in
`internal/relay`.

## Global constraints

- `RenderStatusLine(r Report, now time.Time, columns int) string` keeps its
  signature and its layout rules (09-13 plan, Task 2). Only `waiting`, `age`
  and the builder cell change.
- `Last` keeps its meaning and `RenderStatus` keeps reading it. `LastPayload`
  is additive with `omitempty`.
- The payload kinds are exactly `store.KindPlan`, `store.KindReport`,
  `store.KindQuestion`, `store.KindAnswer`. No others.
- `waiting` has no default/fallthrough branch after this plan: the four kinds
  are exhaustive.
- `claudeCodeMargin` is a named constant with the measurement in its comment
  (§3.3). No other magic numbers.
- No new verb, no new flag. The override is the environment variable
  `RELAY_STATUSLINE_MARGIN` only.
- One commit per task, on the worktree's branch.

---

### Task 1: `LastPayload`

**Files:**
- Modify: `internal/relay/status.go` (`BindingStatus`, after the `Last` field; `statusRow`, after line 235's `row.Last = &LastEvent{...}` block)
- Modify: `internal/relay/status_test.go` (new test at the end)

**Interfaces:**
- Produces: field `LastPayload *LastEvent \`json:"last_payload,omitempty"\`` on
  `BindingStatus`, with a doc comment: the most recent plan/report/
  question/answer entry; nil when none; `Last` is any kind.
- Modifies: `statusRow` sets it by walking `entries` from the end (§5).

- [ ] **Step 1: Write the test**

`TestStatusLastPayloadSkipsBookkeepingKinds` in `status_test.go`. Use
`sentBinding(t, f)` (which logs `plan` for round 1), then append entries
directly with `rt.Store.AppendLog(name, store.LogEntry{...})`
(`internal/store/log.go:73`; `Send` uses the `*Tx` form of the same
method) so the log reads, in order: `plan` (from `sentBinding`), then `drift`
(`DirToPlanner`). Call `Status`; assert `rep.Bindings[0].Last.Kind ==
store.KindDrift` and `rep.Bindings[0].LastPayload.Kind == store.KindPlan`.

Second case in the same test: append `diff` then `report` (`DirToPlanner`,
`Note: "unmarked"`); assert `Last.Kind == report`, `LastPayload.Kind ==
report`, `LastPayload.Note == "unmarked"`.

Third case: a fresh `newRuntime`/`Bind` with no `Send` (use `seedBound`);
`Status`; assert `Last == nil` and `LastPayload == nil` for that binding.

- [ ] **Step 2: Run to verify it fails**

Run: `go test -count=1 ./internal/relay -run TestStatusLastPayload`
Expected: compile error, `LastPayload` undefined.

- [ ] **Step 3: Add the field and the assignment.** In `statusRow`,
directly after the `if n := len(entries); n > 0 { ... row.Last = ... }`
block, a backwards loop over `entries` that sets `row.LastPayload` on the
first entry whose `Kind` is one of the four payload kinds, then breaks.
Keep a small unexported `isPayloadKind(store.Kind) bool` beside it.

- [ ] **Step 4: Run the status tests**

Run: `go test -count=1 ./internal/relay -run 'TestStatus|TestRenderStatus|TestDone'`
Expected: PASS, with no edits to existing tests.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/status.go internal/relay/status_test.go
git commit -m "feat(relay): BindingStatus.LastPayload, the last plan/report/question/answer (#155)"
```

---

### Task 2: vocabulary over `LastPayload`, harness segment

**Files:**
- Modify: `internal/relay/statusline.go` (`waiting`, `age`, the `mid` assembly in `RenderStatusLine`; new `harness`)
- Modify: `internal/relay/statusline_test.go` (fixture at line 54; fallthrough test at line 221; one new test)

**Interfaces:**
- Modifies: `waiting(b BindingStatus) string` per §3.2 table -- reads
  `b.LastPayload`; branches `plan`, `report` (with note), `question`,
  `answer`, nil -> `no plan yet`; **no default branch**.
- Modifies: `age(b, now)` reads `b.LastPayload`.
- Produces: `harness(token string) string` -- `token[:i]` for the first
  `/` at `i`; the whole token when there is no `/`.
- Modifies: the `mid` assembly uses `harness(b.BuilderCandidate)`.

- [ ] **Step 1: Move the fixtures.** In `statuslineFixture` and
`TestRenderStatusLineWaitingFallthrough`, rename every `Last:` to
`LastPayload:`. Change the fixture's candidates to full tokens:
`api` -> `agy/google/gemini-3.8-flash-high`, `client` ->
`opencode/openrouter/z-ai/glm-5.3-flash`, `docs` -> `agy/google/gemini-3.8-flash-high`.
The existing prefix table (`○ api     r3 · agy · plan sent`,
`● client  r1 · opencode · builder pane gone`, `○ docs    r2 · agy · report → planner …`)
must now pass **because of** `harness()`, not because the fixture says `agy`.

- [ ] **Step 2: Rewrite the fallthrough rows.** In
`TestRenderStatusLineWaitingFallthrough`, delete the `KindSwitch` row and
add:

| row | expect `mid` to contain |
|---|---|
| `LastPayload = {Kind: store.KindQuestion}` | `question in` |
| `LastPayload = {Kind: store.KindAnswer}` | `answered` |
| `BuilderCandidate: "agy"` (no slash), `LastPayload` plan | `r1 · agy · plan sent` |

- [ ] **Step 3: Add `TestRenderStatusLineIgnoresBookkeepingLast`.** One
row with `Last = &LastEvent{Kind: store.KindDrift, TS: now - 1s}` and
`LastPayload = &LastEvent{Kind: store.KindPlan, TS: now - 12m}`. Assert
the plain line contains `plan sent`, does not contain `drift`, and its
right cell begins `12m · `.

- [ ] **Step 4: Run to verify they fail**

Run: `go test -count=1 ./internal/relay -run TestRenderStatusLine`
Expected: FAIL (fixtures now carry full tokens and `LastPayload`, which the
renderer does not read yet).

- [ ] **Step 5: Implement** `harness`, and switch `waiting`/`age`/`mid` per
the interfaces above. Remove the `default:` branch from `waiting`'s switch;
`go vet` must stay clean without it.

- [ ] **Step 6: Run the statusline tests**

Run: `go test -count=1 ./internal/relay -run 'TestRenderStatusLine|TestAgeText|TestPlannerStatus'`
Expected: PASS.

- [ ] **Step 7: Mutation check**

Change `waiting` and `age` back to reading `b.Last`. Run
`go test -count=1 ./internal/relay -run TestRenderStatusLineIgnoresBookkeepingLast`.
Expected: FAIL (line contains `drift`, or the age is `1s`). Restore; PASS.
Say which assertion failed in your report.

- [ ] **Step 8: Commit**

```bash
git add internal/relay/statusline.go internal/relay/statusline_test.go
git commit -m "fix(relay): statusline reads the last payload, not relay's bookkeeping; harness segment only (#152, #155)"
```

---

### Task 3: `StatusLineWidth` and `ShouldDrainStdin`

**Files:**
- Modify: `internal/relay/statusline.go`
- Modify: `internal/relay/statusline_test.go`

**Interfaces:**
- Produces: `const claudeCodeMargin = 4` with the comment:
  `// cells Claude Code's chrome takes from COLUMNS: measured 2026-09-14, 141 of 146 rendered before its own ellipsis (#155).`
- Produces: `func StatusLineWidth(columns int, override string) int` (§3.3):
  `columns <= 0` -> `0`; margin = `override` if it parses as an int `>= 0`,
  else `claudeCodeMargin`; result `max(columns - margin, 1)`.
- Produces: `func ShouldDrainStdin(mode os.FileMode) bool` (§3.4):
  `mode&os.ModeCharDevice == 0`.

- [ ] **Step 1: Table tests**

`TestStatusLineWidth`:

| columns | override | want |
|---|---|---|
| 146 | `""` | 142 |
| 146 | `"0"` | 146 |
| 146 | `"10"` | 136 |
| 146 | `"x"` | 142 |
| 146 | `"-1"` | 142 |
| 0 | `""` | 0 |
| -5 | `"0"` | 0 |
| 3 | `""` | 1 |

`TestShouldDrainStdin`: `os.ModeCharDevice` -> false; `os.FileMode(0)` ->
true; `os.ModeNamedPipe` -> true; `os.ModeCharDevice | os.ModeDevice` -> false.

- [ ] **Step 2: Run to verify they fail** (compile error, undefined).

- [ ] **Step 3: Implement both.**

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay -run 'TestStatusLineWidth|TestShouldDrainStdin'`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/statusline.go internal/relay/statusline_test.go
git commit -m "feat(relay): StatusLineWidth subtracts Claude Code's margin; ShouldDrainStdin spares a tty (#154, #155)"
```

---

### Task 4: the verb

**Files:**
- Modify: `cmd/relay/main.go` (`cmdStatusline`: line 1255's drain, line 1260's `columns`)

**Interfaces:**
- Modifies `cmdStatusline` per §4.3:
  - the unconditional `io.Copy(io.Discard, os.Stdin)` becomes: `fi, err :=
    os.Stdin.Stat()`; drain when `err != nil || relay.ShouldDrainStdin(fi.Mode())`.
  - after the `COLUMNS` parse (keep the `err != nil || columns <= 0` ->
    `0` rule), `columns = relay.StatusLineWidth(columns, os.Getenv("RELAY_STATUSLINE_MARGIN"))`.
  - nothing else changes.

- [ ] **Step 1: Make the two edits.**

- [ ] **Step 2: Check by hand (no herdr needed)**

```bash
go build ./... 
timeout 3 go run ./cmd/relay statusline; echo "tty exit=$?"          # expected: 0 (returns at once; 124 means it still blocks)
HERDR_PANE_ID= go run ./cmd/relay statusline </dev/null; echo "exit=$?"  # expected: no output, 0
```

The width checks need a pane with bindings and are the planner's, after
merge: `COLUMNS=146 relay statusline </dev/null` rows are 142 wide;
`RELAY_STATUSLINE_MARGIN=0 COLUMNS=146 …` rows are 146 wide.

- [ ] **Step 3: Full constituent set** (see "Running commands"). Expected: green.

- [ ] **Step 4: Commit**

```bash
git add cmd/relay/main.go
git commit -m "fix(relay): statusline subtracts the Claude Code margin and no longer blocks on a tty (#154, #155)"
```

---

### Task 5: docs

**Files:**
- Modify: `README.md:444-461` ("Status line" section)
- Modify: `docs/specs/2026-09-13-statusline-design.md` (header block)

- [ ] **Step 1: README.** After the preconditions paragraph (ends "is
inherited."), add:

```
Claude Code renders a few cells less than `COLUMNS`; relay subtracts 4 by
default (measured on the fullscreen TUI). If the right-hand `age · STATE`
cell is clipped or sits short of the edge, measure yours and set
`RELAY_STATUSLINE_MARGIN` in the environment Claude Code starts from. To
measure, put this in `statusLine.command` for one refresh and count the
cells before Claude Code's `…`:

    sh -c 'printf "%s" "$(seq -s . 1 $COLUMNS | cut -c1-$COLUMNS)"'
```

Change the last paragraph's `builder` explanation: the row's `builder` is
the harness segment of the candidate token, and `age` is time since the
last plan, report, question or answer crossed.

- [ ] **Step 2: 09-13 spec header.** Add the line
`**Amended by:** docs/specs/2026-09-14-statusline-edges-design.md (§3.2, §3.3, §4.4, §5, §6).`
after the `**Design record:**` line.

- [ ] **Step 3: Constituent set** (docs only: fmt/tidy no-ops). Expected: green.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/specs/2026-09-13-statusline-design.md
git commit -m "docs: statusline margin override and ruler; 09-13 spec points at its errata (#155)"
```

---

## Report

Your report is `NNN-report.md` in the drop directory, then the empty
`NNN-done`. It lists: each task's commit hash; the Task 2 mutation check and
which assertion failed; the Task 4 tty check's exit code; the final
constituent-set output; and any step where you stopped, with the reason.
