# ui: an empty fleet is a deliberate state, not a blank dashboard (#193)

Closes #193. Design in this file; no separate spec.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the files listed in §2 -- everything is under
`internal/ui`. Do not change `relay status` text or anything in
`internal/relay`. Do not run `make e2e`. Do not add a module dependency.
Run every command in the foreground; dispatch no sub-agents. Do not widen
any exported signature. Regenerate golden files only with the test's own
`-update` flag, only for the two frames §8 names, and inspect each
regenerated file before committing it -- a golden that changed for a frame
you did not intend to change is a halt.

**Commits.** Squash to ONE commit before the gate, subject starting
`fix(ui):` (not `feat:` -- the drift guard counts feats and this is a fix).

**Commit the plan with the work.** Copy this plan file to
`docs/plans/2026-09-19-ui-empty-fleet.md` in your worktree and include it.

**Before step 1**: `git status --short` and `git branch --show-current`; you
must be on `relay/<binding-name>` under `~/.local/state/relay/.worktrees/`.
Otherwise halt.

## 1. System overview

`relay ui` with zero bindings (a new user, or everyone after `relay gc`)
draws the full dashboard chrome with the data missing: in split layout the
pane shows five blank head rows, a tab bar with `diff` highlighted, a blank
viewport; the footer advertises `↑↓ ⏎ tab 1-4 esc`, every one of which
operates on a binding that does not exist; and focus can move into the pane
via `tab`, `1`-`4` or a click. Only the rail knows the fleet is empty
(`railView`'s `len(m.rows()) == 0` arm prints `no bindings`).

This plan makes "no bindings" one state the whole model knows:

1. **The pane becomes a prose block.** With zero rows `paneView` draws no
   head, tab bar or source line -- one block in `emptyStyle` that names the
   verbs that create a binding.
2. **Focus cannot enter the pane.** `tab`/`shift+tab`, `1`-`4`, and pane or
   tab-bar clicks are no-ops at zero rows, as `enter` already is. If rows
   drop to zero while the pane is focused (a `gc` under a running ui), the
   next `statusMsg` snaps `screen` to `screenList`.
3. **The footer matches**: at zero rows only `s sort`, `c cards`, `q quit`.
4. **Stack layout too**: `screenDetail` is unreachable at zero rows and the
   list screen shows the same prose block.

A single predicate `m.empty()` carries the rule so the four sites cannot
drift apart.

## 2. File structure

```
internal/ui/
  model.go          EDIT empty() predicate; footerView zero-row arm; statusMsg snaps screen to list when empty
  pane.go           EDIT paneView: empty arm draws emptyPaneBlock; emptyPaneBlock helper
  rail.go           EDIT stack list screen: the same block under the "no bindings" line (or verify it already renders through paneView -- see §5.4)
  keys.go           EDIT tab/shift+tab and 1-4 no-ops when empty
  mouse.go          EDIT tab-bar and pane clicks no-ops when empty
  keys_test.go      EDIT (or model_test.go, whichever holds key tests today) TestEmptyFleetKeysDoNotFocusPane
  mouse_test.go     EDIT TestEmptyFleetClicksDoNotFocusPane
  model_test.go     EDIT TestEmptyFleetSnapsBackToList, TestEmptyFleetFooter
  golden_test.go    EDIT add "stack-empty" case; "split-empty" stays and is regenerated
  testdata/split-empty.golden   REGENERATE
  testdata/stack-empty.golden   NEW
docs/plans/2026-09-19-ui-empty-fleet.md   NEW  this file
```

## 3. Data structures & type definitions

No new types. One new unexported method and one helper:

```go
// empty reports whether the fleet has no rows to show. It is the one place
// the zero-binding rule lives; paneView, footerView, the key and mouse
// handlers and the statusMsg arm all ask it, never len(m.rows()) directly.
func (m Model) empty() bool   // return m.statusLoaded && len(m.rows()) == 0

// emptyPaneBlock is the prose the pane shows at zero rows, already styled
// and fitted to width and height (padded with blank rows to height).
func emptyPaneBlock(width, height int) []string
```

`empty()` requires `statusLoaded`: before the first status arrives the fleet
is unknown, not empty, and the existing pre-load frames
(`split-error-before-load`) must not change.

## 4. Interface definitions & component contracts

Text of the block (the exact wording is fixed here so the golden is
reviewable; two-space gutter, verbs left-aligned, one blank line after the
title):

```
no bindings

  relay bind                   put a builder on this tree
  relay add --name <name>      put a builder on its own worktree
  relay candidates             list what can be bound
```

Rendered in `emptyStyle` line by line (the same style `bodyOf` uses for
`tabContent.empty`), then `fit` to width.

Contracts:
- `paneView(width)`: when `m.empty()`, returns exactly `m.bodyRows()` rows:
  `emptyPaneBlock(width, m.bodyRows())`. No head, tab bar, source line,
  hint or viewport. Otherwise unchanged.
- `footerView()`: when `m.empty()`, keys are `s sort: <order>`, `c <compact>`, `q quit`,
  in that order; notes (notice/error/age) unchanged.
- keys: `tab`, `shift+tab`, `back_tab`, `1`-`4` return `(m, nil)` when
  `m.empty()`, before any `paneVisible()` check. `enter` already does.
  `up`/`down` already no-op through `moveCursor`.
- mouse: a click that would set `screen = screenDetail` or switch a tab
  returns `(m, nil)` when `m.empty()`.
- `statusMsg` arm: after `m.report = msg.report` and the sticky resolve, if
  `m.empty() && m.screen == screenDetail` then `m.screen = screenList`
  (no notice: `maybeInvalidate` already posts "<name> is gone" when the
  focused binding vanished; do not post twice -- check ordering so that the
  snap happens even when `detail.name == ""`).

## 5. High-level pseudocode

### 5.1 `paneView`
```
if m.empty(): return join(emptyPaneBlock(width, m.bodyRows()), "\n")
... existing body
```

### 5.2 `footerView`
```
switch:
  case m.empty(): keys = [s sort, compactKey, q quit]
  case split && list: (existing)
  ...
```
The `empty` case is first so it wins over layout/screen.

### 5.3 keys / mouse
```
case "1","2","3","4": if m.empty() { return m, nil }; if m.paneVisible() {...}
railFocused branch, split layout tab handling: if m.empty() { return m, nil } before cycleTab
pane-focused branch: unreachable when empty after 5.5, but guard cycleTab there too
mouse: at each `m.screen = screenDetail` / switchTab site: if m.empty() { return m, nil }
```

### 5.4 Stack layout
`listView` (rail.go) at zero rows: verify what it draws today. If the list
screen already shows only the `no bindings` line, replace that arm's body
with the same `emptyPaneBlock(width, rows)` text so both layouts read the
same. `screenDetail` in stack layout is only reachable via `enter` (already
guarded) and mouse (guarded in 5.3); the `statusMsg` snap (5.5) covers rows
dropping to zero while on the detail screen.

### 5.5 `statusMsg`
```
m.report = msg.report; sticky; top
if m.empty() && m.screen == screenDetail: m.screen = screenList
... existing pointDetailAt / maybeInvalidate
```

## 6. Error handling strategy

None new: pure view/state code. The pre-load state (`!statusLoaded`) is
deliberately not "empty", so error-before-load frames are unchanged.

## 7. Golden frames

- `split-empty` (140x40): regenerate; expected: header `relay   no bindings`,
  rail `no bindings`, pane = the block from §4, footer `s sort: attention   c cards   q quit`.
- `stack-empty` (100x30, below the 110-column split threshold): new case
  with an empty report; expected: the block and the same footer.

Regenerate ONLY these two: `go test ./internal/ui -run TestGoldenViews/split-empty -update`
and the same for `stack-empty`. Then `git diff --stat internal/ui/testdata`
must list exactly those two files. Paste both golden files' first 12 lines
in the report.

## 8. Tests

- `TestEmptyFleetKeysDoNotFocusPane`: empty loaded model in split layout;
  send `tab`, `shift+tab`, `1`, `4`, `enter`; after each, `screen ==
  screenList` and `detail.active` unchanged. **Mutation check (run and
  report):** remove the `m.empty()` guard in front of the split-layout
  `cycleTab` call in `keys.go`; this test must fail with `screen ==
  screenDetail` after `tab` (the issue's named mutation); restore by
  re-editing, re-run, pass.
- `TestEmptyFleetClicksDoNotFocusPane`: clicks at the old tab-bar row and in
  the pane body leave `screen == screenList`.
- `TestEmptyFleetSnapsBackToList`: model with one row, pane focused
  (`screen = screenDetail`); deliver a `statusMsg` with zero bindings;
  `screen == screenList`; exactly one notice (the existing "is gone" one),
  not two.
- `TestEmptyFleetFooter`: footer at zero rows contains `s sort`, `c`, `q`
  and none of `↑↓`, `⏎`, `tab`, `1-4`, `esc`.
- `TestEmptyIsFalseBeforeLoad`: `statusLoaded == false` -> `empty() == false`.
- Existing golden cases other than the two in §7 must pass with their files
  untouched.

## 9. Ordered implementation steps

1. `empty()` + `emptyPaneBlock` + `paneView` arm; `TestEmptyIsFalseBeforeLoad`.
2. `footerView` arm + `TestEmptyFleetFooter`.
3. keys + mouse guards; the two focus tests; run the mutation check, record both outcomes.
4. `statusMsg` snap + `TestEmptyFleetSnapsBackToList`.
5. Stack layout (§5.4); `stack-empty` golden case; regenerate the two goldens per §7; confirm `git diff --stat internal/ui/testdata` lists exactly two files.
6. Squash to one `fix(ui):` commit including the plan copy. Gate, exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Report the tail verbatim, the mutation outcomes, the two golden excerpts, and `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block (`status`, `halted_at`,
`changed_paths`, `commands_run`, `not_done`).
