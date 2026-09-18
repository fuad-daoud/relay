# `relay ui` split dashboard, round 4: the mouse (#180)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-17-relay-ui-split-design.md` §6.1 (new).
**Earlier rounds:** `docs/plans/2026-09-17-ui-split-dashboard.md`, `-r2`, `-r3`.
**Issue:** #180

Trying round 3 on a live binding: with the rail focused, a mouse wheel over
the transcript walked the fleet instead of scrolling the log -- the program
does not take the mouse, so the terminal turns the wheel into arrow keys.
This round takes the mouse: the wheel scrolls what is under the pointer, a
click selects a card or a tab, focus follows the click.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/ui-split` | **the git worktree. Every source edit goes here.** Your shell's cwd, branch `relay/ui-split`. |
| `~/.local/state/relay/ui-split` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find
in the code, **stop and say so in your report**. Do not bend a test to fit.

## Running commands

`make` is intercepted on this machine; run the constituents directly and
say so in the report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do not run `herdr` or any harness. No test under `cmd/relay`.

## Global constraints

- No new dependencies. Mouse support is bubbletea's own
  (`tea.WithMouseCellMotion`, `tea.MouseMsg`), already in `go.mod`.
- `fetch.go` is not modified. `Update` stays pure: a `tea.MouseMsg` is
  just another message.
- Hit-testing uses the constants and functions the views draw with
  (`railWidth`, `railGap`, `headerRows`, `errorRows`, `bodyRows`,
  `paneHead`, `tabSpans`); no second copy of any number.
- Keys keep every meaning they have. The mouse adds, it does not replace.
- Two commits: Task 1 and Task 2.

---

### Task 1: hit-testing and the tab spans, pure

**Files:**
- Create: `internal/ui/mouse.go`, `internal/ui/mouse_test.go`
- Modify: `internal/ui/pane.go` (`tabBar` draws from `tabSpans`)

**Interfaces:**
- Produces:
  ```go
  type region int
  const (
      hitNone region = iota
      hitRail        // a rail line; row is the index into the drawn rail (0 = first drawn line)
      hitTabs        // the tab row; col is the column within the pane
      hitPane        // the pane body (head, source, viewport); row/col within the pane
  )
  // hit maps a terminal cell to what is drawn there.
  func (m Model) hit(x, y int) (r region, row, col int)
  // tabSpans is each tab word's [start, end) column in the tab row, in
  // tab order; tabBar draws from it and hit reads it.
  func tabSpans() [tabCount][2]int
  // tabAt is the tab whose word covers col, or -1.
  func tabAt(col int) tab
  // railBindingAt is the index into rows() of the card drawn on rail
  // line row (0 = first drawn line, i.e. m.list.top), or -1 for a header,
  // a gap, or past the end.
  func (m Model) railBindingAt(row int) int
  ```

- [ ] **Step 1: Failing tests.** `internal/ui/mouse_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestTabSpansMatchTheDrawnRow(t *testing.T) {
	m := paneModel(t, threeRows()[0], tabReport)
	words := stripANSI(m.tabBar()[0])
	for i, t := range tabTitles {
		sp := tabSpans()[i]
		if got := words[sp[0]:sp[1]]; got != " "+t+" " {
			t.Errorf("span %d = %q, want %q", i, got, " "+t+" ")
		}
	}
	if tabAt(tabSpans()[tabDiff][0]+1) != tabDiff {
		t.Error("a column inside diff's word must resolve to tabDiff")
	}
	if tabAt(tabSpans()[tabReport][1]) != -1 {
		t.Error("the gap after a word resolves to no tab")
	}
	if tabAt(500) != -1 {
		t.Error("past the words resolves to no tab")
	}
}

func TestHitRegionsSplit(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	top := headerRows // no error block
	if r, _, _ := m.hit(3, 0); r != hitNone {
		t.Errorf("header row hits nothing, got %v", r)
	}
	if r, _, _ := m.hit(3, 39); r != hitNone {
		t.Errorf("footer row hits nothing, got %v", r)
	}
	if r, row, _ := m.hit(3, top+1); r != hitRail || row != 1 {
		t.Errorf("rail cell -> %v row %d", r, row)
	}
	if r, _, _ := m.hit(railWidth, top+1); r != hitNone {
		t.Errorf("the separator column hits nothing, got %v", r)
	}
	b := m.rows()[m.list.cursor]
	head := len(m.paneHead(&b))
	px := railWidth + railGap
	if r, row, col := m.hit(px+5, top+head); r != hitTabs || col != 5 || row != head {
		t.Errorf("tab row -> %v row %d col %d", r, row, col)
	}
	if r, row, _ := m.hit(px+5, top+head+3); r != hitPane || row != head+3 {
		t.Errorf("pane body -> %v row %d", r, row)
	}
	if r, _, _ := m.hit(px+5, top+2); r != hitPane {
		t.Errorf("pane head rows are pane, got %v", r)
	}
}

func TestHitRegionsStack(t *testing.T) {
	m := splitModel(t, 80, 30, threeRows()...)
	if r, _, _ := m.hit(60, headerRows+1); r != hitRail {
		t.Errorf("stack list screen is all rail, got %v", r)
	}
	m.screen = screenDetail
	m.detail.name = m.rows()[0].Name
	b := m.rows()[0]
	head := len(m.paneHead(&b))
	if r, _, _ := m.hit(3, headerRows+head); r != hitTabs {
		t.Errorf("stack detail tab row, got %v", r)
	}
	if r, _, _ := m.hit(3, headerRows+head+4); r != hitPane {
		t.Errorf("stack detail body, got %v", r)
	}
}

func TestHitShiftsUnderAnErrorBlock(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.err = errors.New("herdr: connection refused")
	e := m.errorRows()
	if e < 1 {
		t.Fatal("fixture needs an error block")
	}
	if r, row, _ := m.hit(3, headerRows+e+1); r != hitRail || row != 1 {
		t.Errorf("rail under the error block -> %v row %d", r, row)
	}
}

func TestRailBindingAt(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), true)
	for i, l := range lines {
		if got := m.railBindingAt(i); got != l.binding {
			t.Errorf("line %d: railBindingAt = %d, tag = %d (%q)", i, got, l.binding, strings.TrimSpace(stripANSI(l.text)))
		}
	}
	if m.railBindingAt(len(lines)+5) != -1 {
		t.Error("past the end resolves to no binding")
	}
	// A scrolled rail: row 0 is m.list.top.
	m.list.top = 3
	if got := m.railBindingAt(0); got != lines[3].binding {
		t.Errorf("scrolled: row 0 -> %d, want %d", got, lines[3].binding)
	}
}
```

(`errors` import.)

- [ ] **Step 2:** `go test ./internal/ui -run 'TestTabSpans|TestHit|TestRailBindingAt'` -- FAIL.

- [ ] **Step 3: Implement.** `internal/ui/mouse.go`:

```go
package ui

import "github.com/charmbracelet/lipgloss"

// region is what a terminal cell shows, for hit-testing (spec §6.1).
type region int

const (
	hitNone region = iota
	hitRail        // a rail line; row is the index into the drawn rail
	hitTabs        // the tab row; col is the column within the pane
	hitPane        // the pane body: head, source, viewport
)

// tabSpans is each tab word's [start, end) column in the tab row, in tab
// order. tabBar draws from it and hit reads it, so a click lands where the
// word is drawn by construction.
func tabSpans() [tabCount][2]int {
	var out [tabCount][2]int
	col := 0
	for i, t := range tabTitles {
		w := lipgloss.Width(" " + t + " ")
		out[i] = [2]int{col, col + w}
		col += w + 2 // the two-space join
	}
	return out
}

// tabAt is the tab whose word covers col, or -1.
func tabAt(col int) tab {
	for i, sp := range tabSpans() {
		if col >= sp[0] && col < sp[1] {
			return tab(i)
		}
	}
	return -1
}

// hit maps a terminal cell to what is drawn there, with the same numbers
// the views use: the body starts under the header and the error block;
// in split layout the rail is the first railWidth columns and the pane
// begins after the separator; in stack layout the screen is one or the
// other. row and col are relative to the region.
func (m Model) hit(x, y int) (region, int, int) {
	top := headerRows + m.errorRows()
	if y < top || y >= top+m.bodyRows() {
		return hitNone, 0, 0
	}
	row := y - top
	switch {
	case m.layout() == layoutSplit && x < railWidth:
		return hitRail, row, x
	case m.layout() == layoutSplit && x < railWidth+railGap:
		return hitNone, 0, 0
	case m.layout() == layoutStack && m.screen == screenList:
		return hitRail, row, x
	}
	col := x
	if m.layout() == layoutSplit {
		col = x - railWidth - railGap
	}
	head := paneHeadRows
	if b := row2(m.report, m.detail.name); b != nil {
		head = len(m.paneHead(b))
	}
	if row == head {
		return hitTabs, row, col
	}
	return hitPane, row, col
}

// railBindingAt is the index into rows() of the card drawn on rail line
// row (0 = the first drawn line, m.list.top), or -1 for a header, a gap
// or past the end.
func (m Model) railBindingAt(row int) int {
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), m.screen == screenList)
	i := m.list.top + row
	if i < 0 || i >= len(lines) {
		return -1
	}
	return lines[i].binding
}
```

`row2` above is a placeholder name: use the existing `row(rep, name)`
helper from `model.go` (it returns `*relay.BindingStatus`). Rename in the
code you write; do not add a second helper.

`pane.go`, `tabBar`: build the words from `tabSpans()` -- iterate
`tabTitles`, render each `" " + t + " "` as now, and join with two spaces
exactly as `tabSpans` assumes. The rule row already mirrors the words.
(If `tabBar` already joins with `"  "`, the only change is a comment
saying `tabSpans` depends on it.)

- [ ] **Step 4:** the tests pass. Constituents green. Commit:
`feat(ui): hit-testing for the rail, the tab row and the pane (#180)`

---

### Task 2: wheel and click

**Files:**
- Modify: `internal/ui/ui.go` (`tea.WithMouseCellMotion()`)
- Modify: `internal/ui/model.go` (`case tea.MouseMsg`)
- Modify: `internal/ui/mouse.go` (`updateMouse`)
- Test: `internal/ui/mouse_test.go`

**Interfaces:**
- Produces: `func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd)`

- [ ] **Step 1: Failing tests.** Append to `mouse_test.go`:

```go
func wheel(x, y int, down bool) tea.MouseMsg {
	b := tea.MouseButtonWheelUp
	if down {
		b = tea.MouseButtonWheelDown
	}
	return tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress}
}

func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

func longBody(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return strings.TrimRight(b.String(), "\n")
}

func TestWheelOverPaneScrollsWithoutMovingTheCursor(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.tabInFlight = false
	m.detail.cache[tabReport] = tabContent{loaded: true, body: longBody(200)}
	m.fillViewport()
	cursor := m.list.cursor
	px, py := railWidth+railGap+10, headerRows+20
	res, _ := m.Update(wheel(px, py, true))
	m = res.(Model)
	if m.detail.vp.YOffset != 3 {
		t.Errorf("one notch down scrolls three lines, got offset %d", m.detail.vp.YOffset)
	}
	if m.list.cursor != cursor || m.screen != screenList {
		t.Error("a wheel over the pane must move neither the cursor nor the focus")
	}
	res, _ = m.Update(wheel(px, py, false))
	if res.(Model).detail.vp.YOffset != 0 {
		t.Error("one notch up scrolls back")
	}
}

func TestWheelOverPaneKeepsTheTerminalFollowRule(t *testing.T) {
	rows := threeRows()
	rows[0].Headless = &relay.HeadlessInfo{PID: 1, LogPath: "/x/001-builder.log"}
	m := splitModel(t, 140, 40, rows...)
	m.tabInFlight = false
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = res.(Model)
	m.tabInFlight = false
	res, _ = m.Update(tabMsg{name: m.detail.name, t: tabTerminal, content: tabContent{loaded: true, body: longBody(200)}})
	m = res.(Model)
	px, py := railWidth+railGap+10, headerRows+20
	res, _ = m.Update(wheel(px, py, false))
	m = res.(Model)
	if m.detail.follow {
		t.Error("a wheel up over the terminal tab stops following")
	}
	for i := 0; i < 100 && !m.detail.vp.AtBottom(); i++ {
		res, _ = m.Update(wheel(px, py, true))
		m = res.(Model)
	}
	if !m.detail.follow {
		t.Error("wheeling back to the bottom resumes following")
	}
}

func TestWheelOverRailMovesTheCursor(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.tabInFlight = false
	res, _ := m.Update(wheel(3, headerRows+1, true))
	m = res.(Model)
	if m.list.cursor != 1 || m.detail.name != m.rows()[1].Name {
		t.Errorf("wheel down over the rail: cursor %d pane %q", m.list.cursor, m.detail.name)
	}
	res, _ = m.Update(wheel(3, headerRows+1, false))
	if res.(Model).list.cursor != 0 {
		t.Error("wheel up over the rail moves back")
	}
}

func TestClickSelectsCardAndTab(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.tabInFlight = false
	// Find the rail line of the third binding's name and click it.
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), true)
	target := -1
	for i, l := range lines {
		if l.binding == 2 {
			target = i
			break
		}
	}
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // focus the pane first
	m = res.(Model)
	res, cmd := m.Update(click(3, headerRows+target))
	m = res.(Model)
	if m.list.cursor != 2 || m.detail.name != m.rows()[2].Name || cmd == nil {
		t.Errorf("click on a card: cursor %d pane %q cmd %v", m.list.cursor, m.detail.name, cmd != nil)
	}
	if m.screen != screenList {
		t.Error("a click on the rail focuses the rail")
	}
	// Click a header line: nothing changes.
	res, _ = m.Update(click(3, headerRows+0))
	if res.(Model).list.cursor != 2 {
		t.Error("a click on a group header selects nothing")
	}
	// Click the diff tab.
	b := m.rows()[2]
	head := len(m.paneHead(&b))
	sp := tabSpans()[tabDiff]
	m.tabInFlight = false
	res, _ = m.Update(click(railWidth+railGap+sp[0]+1, headerRows+head))
	m = res.(Model)
	if m.detail.active != tabDiff || m.screen != screenDetail {
		t.Errorf("click on the diff tab: active %v screen %v", m.detail.active, m.screen)
	}
	// Click the pane body: focus only.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = res.(Model)
	res, _ = m.Update(click(railWidth+railGap+10, headerRows+head+6))
	if res.(Model).screen != screenDetail {
		t.Error("a click in the pane body focuses the pane")
	}
}

func TestClickOnStackListSelectsWithoutOpening(t *testing.T) {
	m := splitModel(t, 80, 30, threeRows()...)
	lines := railLines(m.rows(), m.list.cursor, m.sort, m.now(), true)
	target := -1
	for i, l := range lines {
		if l.binding == 1 {
			target = i
			break
		}
	}
	res, _ := m.Update(click(10, headerRows+target))
	m = res.(Model)
	if m.list.cursor != 1 || m.screen != screenList {
		t.Errorf("stack click: cursor %d screen %v", m.list.cursor, m.screen)
	}
}
```

(`fmt`, `tea`, `relay` imports as needed.)

- [ ] **Step 2:** `go test ./internal/ui -run 'TestWheel|TestClick'` -- FAIL.

- [ ] **Step 3: Implement.**

`ui.go`: `tea.NewProgram(newModel(ctx, rt, opts), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))`.

`model.go`, in `Update`, before the `tea.KeyMsg` case's sibling cases:

```go
	case tea.MouseMsg:
		m.notice = ""
		return m.updateMouse(msg)
```

`mouse.go`:

```go
// wheelLines is how far one wheel notch scrolls the pane -- the viewport's
// own MouseWheelDelta, so a wheel feels the same here as in any bubbles
// pager.
const wheelLines = 3

// updateMouse is spec §6.1's table: the wheel scrolls what is under the
// pointer, a click selects what is under it, focus follows the click.
func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	r, row, col := m.hit(msg.X, msg.Y)
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		down := msg.Button == tea.MouseButtonWheelDown
		switch r {
		case hitRail:
			if down {
				return m.moveCursor(+1)
			}
			return m.moveCursor(-1)
		case hitPane, hitTabs:
			if !m.paneVisible() {
				return m, nil
			}
			if down {
				m.detail.vp.LineDown(wheelLines)
			} else {
				m.detail.vp.LineUp(wheelLines)
			}
			if m.detail.active == tabTerminal {
				m.detail.follow = m.detail.vp.AtBottom()
			}
			return m, nil
		}
		return m, nil

	case tea.MouseButtonLeft:
		switch r {
		case hitRail:
			i := m.railBindingAt(row)
			if i < 0 {
				return m, nil
			}
			m.screen = screenList
			return m.moveCursor(i - m.list.cursor)
		case hitTabs:
			if !m.paneVisible() {
				return m, nil
			}
			m.screen = screenDetail
			if t := tabAt(col); t >= 0 && t != m.detail.active {
				return m.switchTab(t)
			}
			return m, nil
		case hitPane:
			if m.paneVisible() {
				m.screen = screenDetail
			}
			return m, nil
		}
	}
	return m, nil
}
```

`moveCursor` clamps and re-points the pane in split layout, which is what
a rail click needs; in stack layout it selects without opening. Setting
`m.screen = screenList` before it matters in split layout only for the
gutter/separator colours.

- [ ] **Step 4:** `go test ./internal/ui` -- PASS. Goldens are unaffected
(no drawing changed); if one differs, stop and report.

- [ ] **Step 5: Mutation checks.**
  1. Make `hit` return `hitRail` for the separator column too (drop the
     `railGap` case): `TestHitRegionsSplit` must fail on "the separator
     column hits nothing". Revert.
  2. In `updateMouse`, route the pane wheel through `moveCursor` instead
     of the viewport: `TestWheelOverPaneScrollsWithoutMovingTheCursor` must
     fail. Revert.
  Record both.

- [ ] **Step 6:** constituents green. Commit:
`feat(ui): the wheel scrolls what is under the pointer; clicks select cards and tabs (#180)`

---

## Report

`NNN-report.md`: the constituents' tails; the two mutation outcomes;
whether any golden changed; anything stopped on, with the step number.
