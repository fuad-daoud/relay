# Status line: `relay statusline` renders this planner's builders (#122)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-13-statusline-design.md`. Section numbers
below (§) refer to it.
**Issue:** #122. Closes it.
**Depends on:** nothing open.

**Goal:** `relay statusline` prints one row per live binding whose planner
is the pane the command runs in, laid out for Claude Code's `statusLine`
setting: `○ name  rN · builder · what relay is waiting on  …  age · STATE`,
right-aligned to `$COLUMNS`, coloured as the TUI colours states. It reads
the store only, never herdr. With nothing to show, or on any error, it
prints nothing.

**Architecture:** Three new functions in `internal/relay/statusline.go`.
`AgeText` humanises a duration. `RenderStatusLine(Report, now, columns)` is a
pure renderer over the `Report` that `status --json` already emits.
`PlannerStatus(ctx, rt, pane)` filters stored bindings to one planner pane
and builds rows through `buildReport`, which is the row-building half of
today's `Status` split out so it can run with `agents == nil`. `Status`'s
behaviour does not change. `cmd/relay/main.go` gains the verb as a thin
caller. No store field, no new state, no change to `RenderStatus` or the
TUI.

**Tech stack:** Go 1.22. Verification is `make check` (runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/statusline` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/statusline`, cut from `main`. |
| `~/.local/state/relay/statusline` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself. The live check (Task 4, last step) is the
planner's, after merge. **No test is added under `cmd/relay`** (CI runners
have no `herdr`; the rule is in CLAUDE.md). All logic under test lives in
`internal/relay`.

## Global constraints

- `PlannerStatus` **never calls `rt.Herdr`**. Task 3 has a test that fails
  if it does. Do not weaken that test.
- Row text reuses `HoldText`, `NudgeText` and `Detail` verbatim (§3.3). Do
  not reword them and do not add new phrasing beyond the table in §3.3.
- Colour sequences are exactly §3.5's four strings. Widths are computed on
  plain text; colour is applied after padding.
- `RenderStatusLine` returns `""` (not `"\n"`) for an empty report.
- `Status`, `statusRow`, `RenderStatus`, `HideDone`, the TUI and every file
  under `internal/store` are not modified, except the mechanical split of
  `Status` in Task 3.
- The verb takes no flags and no arguments.
- One commit per task, on the worktree's branch.

---

### Task 1: `AgeText`

**Files:**
- Create: `internal/relay/statusline.go`
- Create: `internal/relay/statusline_test.go`

**Interfaces:**
- Produces: `func AgeText(d time.Duration) string` (§3.4).

Rules: truncating; under a minute `Ns`; under an hour `Nm`; otherwise
`Nh Mm` with `M` the whole minutes past the hour, always present (`1h 0m`).
Negative renders `0s`. No days.

- [ ] **Step 1: Write the table test**

In `internal/relay/statusline_test.go`, `TestAgeText`, a table of
`(time.Duration, string)`:

| input | want |
|---|---|
| `0` | `0s` |
| `59 * time.Second` | `59s` |
| `60 * time.Second` | `1m` |
| `59*time.Minute + 59*time.Second` | `59m` |
| `time.Hour` | `1h 0m` |
| `27*time.Hour + 4*time.Minute + 30*time.Second` | `27h 4m` |
| `-5 * time.Second` | `0s` |

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 ./internal/relay -run TestAgeText`
Expected: compile error, `AgeText` undefined.

- [ ] **Step 3: Implement `AgeText`** in `internal/relay/statusline.go`, with
a package doc comment on the file naming the spec.

- [ ] **Step 4: Run the test**

Run: `go test -count=1 ./internal/relay -run TestAgeText`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/statusline.go internal/relay/statusline_test.go
git commit -m "feat(relay): AgeText, the status line's coarse duration (#122)"
```

---

### Task 2: `RenderStatusLine`

**Files:**
- Modify: `internal/relay/statusline.go`
- Modify: `internal/relay/statusline_test.go`

**Interfaces:**
- Consumes: `Report`, `BindingStatus`, `HoldText(BindingStatus) string`,
  `NudgeText(NudgeInfo) string` (all existing, `status.go`), `AgeText`
  (Task 1).
- Produces: `func RenderStatusLine(r Report, now time.Time, columns int) string`
  (§4.3, §5).

Layout, per row (§3.3, §5). All widths are rune counts of plain text;
every glyph used (`○ ● · → …`) is single-width:

```
dot   = "○" (dim) or "●" (needs-you) when Display == "NEEDS YOU"
name  = Name padded right to nameW = longest Name in r.Bindings
mid   = "r" + Round + [" · " + BuilderCandidate if != ""] + " · " + waiting
right = age + " · " + Display
leftW = 2 + nameW + 2
midW  = columns - leftW - 1 - runes(right)
midW >= 8 : dot " " name "  " pad(truncate(mid, midW)) " " right   -- exactly `columns` wide
midW <  8 : dot " " Name "  " mid " · " right                      -- unpadded
```

`truncate(s, w)`: if `runes(s) <= w` return `s`; else the first `w-1` runes
followed by `…`. `columns <= 0` means 80. `waiting` and `age` are §5's
`waiting(b)` and `age(b)`, verbatim.

Colour (§3.5) is wrapped around three spans only, after layout: the dot,
and the `Display` word inside `right` when it is `ACTIVE` or `NEEDS YOU`.
`HELD` is uncoloured. Every coloured span ends with the reset.

- [ ] **Step 1: Test helpers and fixture**

In `statusline_test.go` add:

- `stripSGR(s string) string` using the regexp `\x1b\[[0-9;]*m` → `""`.
- `width(s string) int` = `utf8.RuneCountInString(stripSGR(s))`.
- `statuslineFixture(now time.Time) Report` with three rows, in this order:

| Name | Round | Display | BuilderCandidate | other fields |
|---|---|---|---|---|
| `api` | 3 | `ACTIVE` | `agy` | `Last = &LastEvent{TS: now-12m, Kind: store.KindPlan, Direction: store.DirToBuilder}` |
| `client` | 1 | `NEEDS YOU` | `opencode` | `Detail = "builder pane gone"`, `Last = {TS: now-4m, Kind: plan}` |
| `docs` | 2 | `HELD` | `agy` | `Pending = &PendingInfo{Round: 2, Kind: store.KindReport, Hold: &HoldInfo{QuietMS: 23000, GraceMS: 60000}}`, `Last = {TS: now-23s, Kind: report, Direction: to_planner}` |

`now` is `baseTime` (existing test constant).

- [ ] **Step 2: Write the tests**

Each test calls `RenderStatusLine(statuslineFixture(now), now, columns)`,
splits on `"\n"`, and drops the trailing empty element. Assertions are on
`stripSGR` output unless a test says otherwise.

`TestRenderStatusLineEmpty`: `RenderStatusLine(Report{}, now, 80) == ""`.

`TestRenderStatusLineAt80`: three lines. For every line `width(line) == 80`.
Exact plain prefixes and suffixes:

| line | `HasPrefix` | `HasSuffix` |
|---|---|---|
| 0 | `○ api     r3 · agy · plan sent` | ` 12m · ACTIVE` |
| 1 | `● client  r1 · opencode · builder pane gone` | ` 4m · NEEDS YOU` |
| 2 | `○ docs    r2 · agy · report → planner · quiet 23s of 1m0s` | ` 23s · HELD` |

(`api` is padded to 6 = `len("client")`, then two spaces: `○ api     r3`.)

`TestRenderStatusLineTruncatesAt40`: every `width(line) == 40`; line 2's
plain text contains `…` and does not contain `1m0s`; each line still ends
with its `right` suffix from the table above.

`TestRenderStatusLineZeroColumnsIs80`: output for `columns: 0` equals
output for `columns: 80`.

`TestRenderStatusLineUnpaddedWhenTooNarrow`: at `columns: 20`, line 0's
plain text is exactly `○ api  r3 · agy · plan sent · 12m · ACTIVE` (no
padding, ` · ` before `right`).

`TestRenderStatusLineColours` (on the raw output, not stripped): line 0
contains `"\x1b[38;5;245m○\x1b[0m"` and `"\x1b[38;5;42mACTIVE\x1b[0m"`;
line 1 contains `"\x1b[1;38;5;214m●\x1b[0m"` and
`"\x1b[1;38;5;214mNEEDS YOU\x1b[0m"`; line 2 contains no `\x1b[` after the
dot's reset (i.e. `strings.Count(line2, "\x1b[") == 2`).

`TestRenderStatusLineWaitingFallthrough`, single-row reports at `columns:
80`, each row `Name: "api", Round: 1, Display: "ACTIVE", BuilderCandidate:
"agy"` unless the table overrides it, asserting the plain `mid` text between
the two-space gap and the padding:

| row | expect `mid` to contain |
|---|---|
| `Nudge = &NudgeInfo{QuietMS: 23000, GraceMS: 60000}`, `Last` plan | `nudged · quiet 23s of 1m0s` |
| `Last = {Kind: report, Note: "unmarked"}` | `report in (unmarked)` |
| `Last = {Kind: store.KindSwitch}` | `r1 · agy · switch` |
| `Last == nil` | `no plan yet`, and `right` begins `-- · ` |
| `BuilderCandidate == ""`, `Last` plan | `r1 · plan sent` (no empty separator) |

- [ ] **Step 3: Run to verify they fail**

Run: `go test -count=1 ./internal/relay -run TestRenderStatusLine`
Expected: compile error, `RenderStatusLine` undefined.

- [ ] **Step 4: Implement `RenderStatusLine`** per the layout block above.
Keep `waiting` and `age` as small unexported helpers so the fallthrough is
readable; the colour constants are unexported package vars.

- [ ] **Step 5: Run the tests**

Run: `go test -count=1 ./internal/relay -run 'TestRenderStatusLine|TestAgeText'`
Expected: PASS.

- [ ] **Step 6: Mutation check**

Comment out the `Detail` branch of `waiting`. Run
`go test -count=1 ./internal/relay -run TestRenderStatusLineAt80`. Expected:
FAIL on line 1's prefix. Restore the branch; re-run; PASS. Say in your
report which test failed.

- [ ] **Step 7: Commit**

```bash
git add internal/relay/statusline.go internal/relay/statusline_test.go
git commit -m "feat(relay): RenderStatusLine lays out one row per binding for Claude Code (#122)"
```

---

### Task 3: `buildReport` split and `PlannerStatus`

**Files:**
- Modify: `internal/relay/status.go:156-186` (`Status`)
- Modify: `internal/relay/statusline.go`
- Modify: `internal/relay/statusline_test.go`

**Interfaces:**
- Refactor: `func Status(ctx, rt) (Report, error)` keeps its signature and
  behaviour; its body after `rt.Herdr.ListAgents` moves, verbatim, into
  `func buildReport(ctx context.Context, rt Runtime, bindings []store.Binding, agents []herdr.Agent) (Report, error)`
  (§4.2): the `knownEndpoints` call, the row loop, the sort, `Gates`.
- Produces: `func PlannerStatus(ctx context.Context, rt Runtime, pane string) (Report, error)`
  (§4.1): `pane == ""` → `Report{}, nil`; else `rt.Store.List()`, keep
  bindings with `Planner.PaneID == pane && State != store.StateDone`, return
  `buildReport(ctx, rt, kept, nil)`.

- [ ] **Step 1: Write the tests**

`TestPlannerStatusFiltersToOnePane`: `f := &fakeHerdr{}`; `rt := newRuntime(t, f)`.
Save four bindings directly with `rt.Store.Save`, each with `Round: 1`,
`State: store.StateActive` unless noted, `Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy"}`,
`BuilderCandidate: testAgyRef`:

| Name | CWD | Planner.PaneID | State |
|---|---|---|---|
| `zeta` | `/a` | `w2:p3` | active |
| `alpha` | `/b` | `w2:p3` | active |
| `other` | `/c` | `w9:p1` | active |
| `finished` | `/d` | `w2:p3` | `store.StateDone` |

`PlannerStatus(ctx, rt, "w2:p3")` returns exactly `alpha`, `zeta` in that
order; each row's `PlannerPane == "w2:p3"`, `PlannerStatus == "gone"`,
`Foreign` empty. `PlannerStatus(ctx, rt, "w9:p1")` returns only `other`.

`TestPlannerStatusEmptyPaneIsEmpty`: same store; `PlannerStatus(ctx, rt, "")`
returns zero bindings and no error.

`TestPlannerStatusNeverProbesHerdr`: same store; `f.onList = func() { t.Fatal("PlannerStatus called ListAgents") }`;
call `PlannerStatus(ctx, rt, "w2:p3")`; additionally assert
`f.listCalls == 0`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./internal/relay -run TestPlannerStatus`
Expected: compile error, `PlannerStatus` undefined.

- [ ] **Step 3: Split `Status`** into the two-call form and `buildReport`.
Do not change any line inside the moved body.

- [ ] **Step 4: Run the existing status tests unedited**

Run: `go test -count=1 ./internal/relay -run 'TestStatus|TestRenderStatus|TestDone'`
Expected: PASS, with no edits to `status_test.go`.

- [ ] **Step 5: Implement `PlannerStatus`** in `statusline.go`.

- [ ] **Step 6: Run the new tests**

Run: `go test -count=1 ./internal/relay -run TestPlannerStatus`
Expected: PASS.

- [ ] **Step 7: Mutation check**

Change `PlannerStatus` to return `Status(ctx, rt)` filtered afterwards.
Run `go test -count=1 ./internal/relay -run TestPlannerStatusNeverProbesHerdr`.
Expected: FAIL. Restore; PASS. Say so in your report.

- [ ] **Step 8: Commit**

```bash
git add internal/relay/status.go internal/relay/statusline.go internal/relay/statusline_test.go
git commit -m "feat(relay): PlannerStatus builds one pane's rows from the store alone (#122)"
```

---

### Task 4: the `statusline` verb

**Files:**
- Modify: `cmd/relay/main.go:200` (dispatch switch, add after `case "status":`)
- Modify: `cmd/relay/main.go:55` (usage block, add after the `status` line)
- Modify: `cmd/relay/main.go` (new `cmdStatusline`, place after `cmdStatus`)

**Interfaces:**
- Produces: `func cmdStatusline(args []string) error` (§4.4, §5).

Behaviour:

1. Any argument → `fmt.Errorf("usage: relay statusline")` (the one case that
   returns non-nil: a human typo, not Claude Code).
2. `io.Copy(io.Discard, os.Stdin)` -- Claude Code writes session JSON; it
   is not used, but the pipe must not block.
3. `pane := os.Getenv("HERDR_PANE_ID")`; empty → return `nil`, nothing
   printed.
4. `columns` from `os.Getenv("COLUMNS")` via `strconv.Atoi`; error or `<= 0`
   → `0` (the renderer treats it as 80).
5. `rt, err := newRuntime()`; on error print `relay statusline: <err>` to
   stderr and return `nil`.
6. `rep, err := relay.PlannerStatus(ctx, rt, pane)`; on error, as 5.
7. `fmt.Print(relay.RenderStatusLine(rep, rt.Now(), columns))`.

Usage line: `  statusline  this planner's builders, one row each, for Claude Code's statusLine setting`.

No test in `cmd/relay` (CI rule). The verb has no logic beyond env reads
and two calls whose contracts Tasks 2-3 pin.

- [ ] **Step 1: Add the dispatch case, the usage line and `cmdStatusline`.**

- [ ] **Step 2: Build and run the verb outside herdr**

Run: `go build ./... && HERDR_PANE_ID= go run ./cmd/relay statusline; echo "exit=$?"`
Expected: no output, `exit=0`.

Run: `go run ./cmd/relay statusline extra; echo "exit=$?"`
Expected: `usage: relay statusline` on stderr, non-zero exit.

- [ ] **Step 3: `make check`**

Expected: green.

- [ ] **Step 4: Commit**

```bash
git add cmd/relay/main.go
git commit -m "feat(relay): statusline verb prints this pane's builders for Claude Code (#122)"
```

Planner's live check after merge, inside a herdr pane with at least one
binding: `relay statusline | wc -l` equals the number of live bindings this
pane owns; `COLUMNS=60 relay statusline` narrows; a pane with no bindings
prints nothing.

---

### Task 5: docs

**Files:**
- Modify: `README.md` (new section "Status line", next to whatever section
  describes `relay status` / `relay ui`)
- Modify: `docs/design.md:304` (the `~/.claude/statusline.py` sentence)

- [ ] **Step 1: README section**

Title `## Status line`. Content, in this order:

1. One sentence: what it shows (this planner's live bindings, one row each,
   under the Claude Code prompt) and what it does not (nothing outside
   herdr, nothing on error, never probes herdr).
2. The settings snippet, verbatim:

   ```json
   "statusLine": { "type": "command", "command": "relay statusline", "refreshInterval": 1 }
   ```

   with one line saying where it goes (`~/.claude/settings.json`).
3. The two preconditions: `relay` on the `PATH` of the Claude Code process;
   Claude Code started inside a herdr pane, so `HERDR_PANE_ID` is inherited.
4. The row anatomy in one line:
   `○ name  rN · builder · what relay is waiting on  …  age · STATE`, and
   that `age` is time since the last relayed message.

- [ ] **Step 2: `docs/design.md`**

Replace the sentence
`` - `relay status --json` — feeds `~/.claude/statusline.py` so any pane can show `⇄ upjo r3`. ``
with
`` - `relay statusline` — one row per binding this planner owns, for Claude Code's `statusLine` setting; store-only, never probes herdr (spec `docs/specs/2026-09-13-statusline-design.md`). ``

- [ ] **Step 3: `make check`**

Expected: green (docs only; this is the fmt/tidy check).

- [ ] **Step 4: Commit**

```bash
git add README.md docs/design.md
git commit -m "docs: relay statusline, the Claude Code status line (#122)"
```

---

## Report

Your report is `NNN-report.md` in the drop directory, then the empty
`NNN-done`. It lists: each task's commit hash; the two mutation checks and
which test failed under each; `make check`'s final output; and any step
where you stopped, with the reason.
