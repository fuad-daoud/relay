# The TUI list screen windows its rows around the cursor (#18)

**Design spec:** `docs/specs/2026-09-11-ui-list-scroll-design.md`
**Issue:** #18

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- No new dependencies. `go mod tidy` must produce no diff. Do **not** add a
  `viewport.Model` to the list (spec §1: windowing was chosen over it).
- **No test may execute a `cmd/relay` subcommand or reach herdr.** Every
  test in this plan is in `internal/ui` against `newFakeHerdr`.
- `resolveSticky` is not modified. It owns the cursor; the window is
  derived from it (spec §4.4).
- Height zero renders every row, so every existing test in
  `internal/ui` passes unchanged. If one breaks, stop.
- The cursor row is always rendered once a height is known (spec §7.1).
- The detail screen (`detail.go`, `chromeHeight`) is not touched.
- Every new symbol gets a doc comment in the house style; the spec gives
  the text.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: window the list rows around the cursor

**Files:**
- Modify: `internal/ui/list.go`, `internal/ui/list_test.go`
- Modify: `internal/ui/keys.go` (the `up`/`k` and `down`/`j` cases)
- Modify: `internal/ui/model.go` (the `tea.WindowSizeMsg` and `statusMsg` cases)

**Interfaces produced** (spec §3, §4):
- `listModel.top int`
- `listWindow(top, cursor, rows, n int) int`
- `(m Model) listRows() int`

- [ ] **Step 1: `listWindow`, test first** (spec §4.1, §8 item 1)

  In `internal/ui/list_test.go` add `TestListWindow`, a table of
  `(top, cursor, rows, n, want)` with exactly the seven rows in spec §8
  item 1, each with a short name (`no limit`, `fits`, `cursor below`,
  `cursor above`, `already visible`, `clamped from past the end`,
  `last row`).

  Run: `go test ./internal/ui/ -run ListWindow`. Expected: compile failure.

  In `internal/ui/list.go`, add the `top` field to `listModel` with the
  comment from spec §3.1, and `listWindow` with the comment from spec §4.1
  and the five rules from §4.1 in order.

  Run: `go test ./internal/ui/ -run ListWindow`. Expected: PASS.

- [ ] **Step 2: `listRows`, test first** (spec §4.2, §8 item 2)

  Add `TestListRowsBudget`: build a model as `TestListScreenThreeStates`
  does, set `m.width = 80`, and assert: `m.height = 24, m.err = nil` →
  `22`; `m.height = 24, m.err = errors.New("line one\nline two")` → `20`;
  `m.height = 2, m.err = nil` → `1`; `m.height = 0` → `0`.

  Run: `go test ./internal/ui/ -run ListRowsBudget`. Expected: compile
  failure.

  Add `(m Model) listRows() int` to `list.go` per spec §4.2: `0` when
  `m.height <= 0`; else `m.height - 2 - errLines` floored at `1`, where
  `errLines` is `0` for a nil error and otherwise
  `strings.Count(renderError(m.err, m.width), "\n") + 1`.

  Run: `go test ./internal/ui/ -run ListRowsBudget`. Expected: PASS.

- [ ] **Step 3: the view tests** (spec §8 items 3-7)

  Add a helper at the top of the new tests:

  ```
  // tenBindings builds a model at the given height showing b00..b09.
  func tenBindings(t *testing.T, height int) Model
  ```

  which constructs the model like `TestListScreenThreeStates`, sets
  `ready = true`, `width = 80`, `height = height`, sends a
  `tea.WindowSizeMsg{Width: 80, Height: height}` through `Update`, then a
  `statusMsg` whose report has ten `relay.BindingStatus{Name: fmt.Sprintf("b%02d", i), Display: "ACTIVE"}`.
  Also a `press(t, m, 'j')` helper that sends
  `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}` and returns the
  `Model`, and an `assertCursorVisible(t, m, name)` helper that checks
  `View()` contains `"> " + name` (the cursor glyph is styled by
  `cursorStyle`; if lipgloss emits escape codes in the test environment,
  strip them with the same approach `TestListRowsRenderFixedGoldenWidth`
  uses -- look at how that test compares rows -- rather than loosening the
  assertion), contains `"+- relay"`, contains `"enter open"`, and that
  `strings.Count(m.View(), "\n") == m.height-1` (read the height off the
  model, so the same helper works after a resize).

  Then the five tests exactly as spec §8 items 3-7 describe them:

  - `TestListViewKeepsCursorVisibleWhenScrollingDown` -- height 6, nine
    `j` presses, `assertCursorVisible` after each with the expected name.
  - `TestListViewKeepsCursorVisibleWhenScrollingUp` -- height 6, nine `j`
    then nine `k`, the same assertion after each `k`; after the **first**
    `k` also assert the view contains `b06` and does not contain `b05`
    (top stayed at 6).
  - `TestListViewRewindowsOnResize` -- height 6, seven `j` (cursor `b07`),
    then `Update(tea.WindowSizeMsg{Width: 80, Height: 4})`; assert
    `assertCursorVisible(t, m, "b07")` with the line count now `3`
    newlines.
  - `TestListViewRewindowsWhenBindingRemoved` -- height 6, nine `j`
    (cursor `b09`), then a `statusMsg` whose report has only `b00`..`b04`;
    assert `m.list.cursor == 4` and `assertCursorVisible(t, m, "b04")`.
  - `TestListViewUnlimitedBeforeResize` -- `tenBindings(t, 0)` **without**
    the `WindowSizeMsg` (build it by hand or give the helper a flag), and
    assert all of `b00`..`b09` appear in `View()`.

  Run: `go test ./internal/ui/ -run 'ListView'`. Expected: FAIL -- the
  scrolling-down test fails once the cursor passes `b03`, the resize and
  removal tests fail on the line count or the cursor row.

- [ ] **Step 4: render the window and re-window on update** (spec §4.3, §4.4)

  In `listView`, replace the `for i, binding := range m.report.Bindings`
  loop with the window from spec §5: `n`, `rows := m.listRows()`,
  `start := listWindow(m.list.top, m.list.cursor, rows, n)`, `end := n`
  narrowed to `start+rows` when `rows > 0 && start+rows < n`, and render
  `i` in `[start, end)` with `selected := i == m.list.cursor`. Add a
  one-line comment that the window is recomputed here on purpose (spec
  §7.4).

  In `keys.go`, at the end of both the `up`/`k` and `down`/`j` cases
  (before `return m, nil`), add
  `m.list.top = listWindow(m.list.top, m.list.cursor, m.listRows(), len(m.report.Bindings))`.

  In `model.go`, add the same line after `m.detail.vp.Height = vpHeight`
  in the `tea.WindowSizeMsg` case, and after `m.list.resolveSticky(m.report)`
  in the `statusMsg` case.

  Run: `go test ./internal/ui/`. Expected: PASS, all of it, including
  every pre-existing test.

- [ ] **Step 5: full check and mutations**

  `make check` (or constituents) green.

  Do each, run the named test, revert, record:
  1. In `listWindow`, delete rule 4 (`cursor >= top+rows`). Expect
     `TestListViewKeepsCursorVisibleWhenScrollingDown` to fail on `b04`.
  2. In `listView`, render `[0, rows)` instead of `[start, end)`. Expect
     the same test to fail on `b04`.
  3. In `listWindow`, delete rule 3 (`cursor < top`). Expect
     `TestListViewKeepsCursorVisibleWhenScrollingUp` to fail once the
     cursor climbs above `b06`.
  4. In `listRows`, stop subtracting the error lines. Expect
     `TestListRowsBudget` to fail on the two-line error case.

- [ ] **Step 6: commit**

  One commit. Subject:
  `fix(ui): window the list screen around the cursor so it scrolls (#18)`.
  Body: why windowing over a viewport (one sentence), that height zero
  renders everything, and that the window is recomputed at render so the
  cursor can never be hidden. Do not push.

## Report

- `git diff --stat` against your base.
- The `make check` (or constituents) output tail.
- The mutation table, all four.
- Anything you stopped on, or any place the plan and the code disagreed.
