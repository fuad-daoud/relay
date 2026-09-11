# The TUI list screen windows its rows around the cursor

**Issue:** #18

## 1. System overview

`listView` (`internal/ui/list.go`) renders a header line, an optional error
block, one line per binding, and a footer line, with no regard for the
terminal height. bubbletea keeps only the last `height` lines of a view, so
on a terminal shorter than the binding count the header and the first rows
are cut off -- possibly including the `>` cursor row -- while `j`/`k` keep
moving a cursor the user cannot see. The detail screen got a `viewport`
when it was written; the list never did.

This design windows the rows around the cursor instead of adding a second
`viewport.Model`: the list keeps a `top` index, a pure function computes
where `top` must be for the cursor to stay visible in the rows the
terminal has, and `listView` renders only `[top, top+rows)`. Windowing was
preferred over a viewport because the list has no free-scrolling
requirement -- the cursor is the only thing that moves -- and a viewport
would need its own resize path and would have to be told where the cursor
is on every keystroke anyway. The sticky-cursor logic (`resolveSticky`) is
untouched; the window follows the cursor it produces.

Before the first `WindowSizeMsg` the height is zero and the list renders
every row, exactly as today, so every existing view test keeps passing.

### Scope boundary

In scope: `listModel.top`, `listWindow`, `Model.listRows`, the three
`Update` paths that move the cursor or change the row budget, and
`listView`'s render loop.

Out of scope: overflow indicators (`▲ 3 more` / `▼ 5 more`), page-up/down
keys, mouse wheel, any change to the detail screen or to `resolveSticky`.

## 2. File structure

```
internal/ui/list.go        listModel.top; listWindow; Model.listRows; listView renders the window
internal/ui/list_test.go   listWindow table; small-height view tests
internal/ui/keys.go        up/down re-window after moving the cursor
internal/ui/model.go       WindowSizeMsg and statusMsg re-window
```

## 3. Data structures and type definitions

### 3.1 `listModel` (modified)

```
type listModel struct {
    cursor int    // index into Model.report.Bindings
    sticky string // binding NAME the cursor is on
    // top is the index of the first rendered row. It moves only as far as
    // it must to keep cursor visible, so the list scrolls a row at a time
    // at either edge rather than re-centring on every keystroke.
    top int
}
```

Invariant after any `Update` that touches the cursor or the height:
`top == listWindow(top, cursor, listRows(), len(Bindings))`.

## 4. Interface definitions and component contracts

### 4.1 `listWindow` (new, `list.go`)

```
// listWindow returns where the first rendered row must be for cursor to be
// visible in a window of rows rows over n items, moving top as little as
// possible. rows <= 0 means no limit: the answer is 0 and every row renders.
func listWindow(top, cursor, rows, n int) int
```

Rules, in order:

1. `rows <= 0` or `n <= rows` → `0`.
2. clamp `top` into `[0, n-rows]`.
3. `cursor < top` → `top = cursor`.
4. `cursor >= top+rows` → `top = cursor - rows + 1`.
5. return `top`.

Pure. Total over all integer inputs (a negative or out-of-range cursor is
clamped by the callers before it gets here, but the function must not panic
on one).

### 4.2 `Model.listRows` (new, `list.go`)

```
// listRows is how many binding rows the list screen can show: the terminal
// height less the header, the footer, and whatever the error block takes.
// Zero before the first WindowSizeMsg, which listWindow reads as no limit.
// Never less than one once a height is known, so the cursor row is always
// drawn even on an absurdly short terminal.
func (m Model) listRows() int
```

`m.height <= 0` → `0`. Otherwise `m.height - 2 - errLines`, where `errLines`
is the number of lines `renderError(m.err, m.width)` produces (`0` when
`m.err == nil`; otherwise `strings.Count(rendered, "\n") + 1`). Floor at `1`.

### 4.3 `listView` (modified)

When `len(m.report.Bindings) > 0`, renders rows `i` in
`[start, end)` where `start = listWindow(m.list.top, m.list.cursor, m.listRows(), n)`
and `end = min(start + rows, n)` (`end = n` when `rows <= 0`). It recomputes
the window rather than trusting `m.list.top` so a render can never hide
the cursor even if a future `Update` path forgets to re-window; the two
agree whenever the invariant in §3.1 holds.

Header, error block, footer, and the `loading…` / `no bindings` /
`cannot reach herdr` branches are unchanged.

### 4.4 `Update` paths (modified)

After each of the following, set
`m.list.top = listWindow(m.list.top, m.list.cursor, m.listRows(), len(m.report.Bindings))`:

- `keys.go`: the `up`/`k` and `down`/`j` cases, after the cursor moves.
- `model.go`: `tea.WindowSizeMsg`, after `m.height` is set.
- `model.go`: `statusMsg` success, after `m.list.resolveSticky(m.report)`.

`resolveSticky` itself is not changed: it owns the cursor, the window owns
`top`, and the window is derived from the cursor.

## 5. High-level pseudocode

```
listView():
    write header
    write error block if any
    if not loaded / empty: existing branches
    else:
        n := len(bindings); rows := m.listRows()
        start := listWindow(m.list.top, m.list.cursor, rows, n)
        end := n; if rows > 0 and start+rows < n: end = start+rows
        for i in [start, end): write renderListRow(bindings[i], i == cursor)
    write footer

on key down:   cursor++ (existing clamp); sticky = name; top = listWindow(...)
on key up:     cursor-- (existing clamp); sticky = name; top = listWindow(...)
on resize:     height = msg.Height (existing); top = listWindow(...)
on statusMsg:  resolveSticky (existing);       top = listWindow(...)
```

## 6. Error handling strategy

Nothing here can fail. `listWindow` is total; `listRows` floors at one;
`listView` clamps `end` to `n`. The error block's line count feeds the row
budget so a long herdr error shrinks the window rather than pushing the
footer off screen.

## 7. Behavioural rules and their rationale

1. **The cursor is always rendered.** The whole point; pinned by every view
   test in §8.
2. **`top` moves as little as possible.** Scrolling one row at the edge is
   what every list widget does; re-centring is disorienting.
3. **Height zero means no windowing.** Preserves the pre-resize render and
   every existing test byte for byte.
4. **The window is recomputed at render.** Cheap, and it makes the
   cursor-visible guarantee local to `listView` instead of depending on
   every `Update` path remembering to re-window.
5. **The error block counts against the rows.** The footer must stay on
   the last line, and `renderError` already caps itself at `maxErrorLines`.

## 8. Testing requirements

All in `internal/ui/list_test.go`, driving `Model` the way
`TestListScreenThreeStates` does (`newModel`, set `ready`/`width`/`height`,
feed `statusMsg` and `tea.KeyMsg` through `Update`, inspect `View()`).

1. `TestListWindow` table: `(top, cursor, rows, n) → top`:
   `(0,0,0,10)→0` (no limit); `(0,3,5,3)→0` (fits); `(0,7,5,10)→3`
   (cursor below); `(6,2,5,10)→2` (cursor above); `(3,4,5,10)→3`
   (already visible, unchanged); `(9,4,5,10)→4` (clamped from past the
   end, then cursor visible: `9→5`, then `4 < 5 → 4`); `(0,9,5,10)→5`
   (last row).
2. `TestListRowsBudget`: height 24 no error → 22; height 24 with a
   two-line error → 20; height 2 → 1; height 0 → 0.
3. `TestListViewKeepsCursorVisibleWhenScrollingDown`: ten bindings named
   `b00`..`b09`, height 6 (header + 4 rows + footer). Press `j` nine times,
   asserting after each press that `View()` contains the `>` cursor row for
   the current binding, contains the header (`+- relay`), contains the
   footer (`enter open`), and has exactly `6` lines
   (`strings.Count(view, "\n") == 5`).
4. `TestListViewKeepsCursorVisibleWhenScrollingUp`: same setup, jump to
   the bottom with nine `j`, then nine `k`, same assertions each step;
   after the first `k` from row 9 the view still shows rows `b06`..`b09`
   (top did not move; rule 2).
5. `TestListViewRewindowsOnResize`: cursor on `b07` at height 6, then a
   `tea.WindowSizeMsg{Height: 4}` → the view has 4 lines and still shows
   `> b07`.
6. `TestListViewRewindowsWhenBindingRemoved`: cursor on `b09` (top 6),
   then a `statusMsg` with `b00`..`b04` only → cursor clamps to `b04` via
   `resolveSticky` and the view shows `> b04` with the header present.
7. `TestListViewUnlimitedBeforeResize`: height 0, ten bindings → all ten
   rows render (existing behaviour, pinned).

Mutation checks (CLAUDE.md): remove rule 4 of `listWindow` (`cursor >=
top+rows`) → test 3 fails on the first press that leaves the window;
render `[0, rows)` instead of the window in `listView` → test 3 fails at
`b04`; drop the re-window from the `WindowSizeMsg` path → test 5 still
passes because of §4.3's recompute, which is intended -- test 5 pins the
render, not the update, and that is enough.

No test in this plan touches `cmd/relay` or herdr.

## 9. Explicitly out of scope

- Overflow indicators, page keys, mouse wheel.
- A `viewport.Model` for the list.
- Any change to the detail screen's `chromeHeight` or its viewport sizing.
