# `relay ui` split dashboard, round 5: draggable divider, compact rail, remembered preferences (#180)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-17-relay-ui-split-design.md` §6.0 (new)
and the amended §6.1 table.
**Earlier rounds:** `docs/plans/2026-09-17-ui-split-dashboard.md`, `-r2`, `-r3`, `-r4`.
**Issue:** #180

Three things, one spec section: the rail width becomes a model field the
human sets with `<` / `>` or by dragging the `│` divider; `c` toggles a
one-line-per-binding compact rail; sort, compact and the width are
remembered in `ui.json` under relay's state root.

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

Do not run `herdr` or any harness. No test under `cmd/relay` may execute a
subcommand; `cmd/relay/main.go` changes by two lines and needs no test.

## Global constraints

- No new dependencies.
- `fetch.go` is not modified. The only new filesystem access is
  `prefs.go` (the ui's own `ui.json`); it never touches relay state, the
  log, or herdr. Writes happen only inside a `tea.Cmd`, never in `Update`.
- `Update` stays pure. A drag is a sequence of `tea.MouseMsg` values.
- The constants `railMin = 20`, `paneMin = 80`, `railDefault = 34`,
  `railStep = 2` live in `layout.go`; nothing else hard-codes them.
- Goldens must not change: every fixture runs at the default width, cards
  mode, attention sort. If one does, stop and report.
- Three commits, one per task.

---

### Task 1: `railCols` -- the width is a field; `<` / `>`; drag

**Files:**
- Modify: `internal/ui/layout.go`, `internal/ui/model.go`, `internal/ui/rail.go`,
  `internal/ui/mouse.go`, `internal/ui/keys.go`
- Test: `internal/ui/layout_test.go`, `internal/ui/mouse_test.go`, `internal/ui/split_test.go`

**Interfaces:**
- Produces:
  ```go
  const (
      railDefault = 34 // columns, including the 1-column selection gutter
      railMin     = 20
      paneMin     = 80 // a hunk's width; the rail never eats into it
      railStep    = 2  // < and > move the divider this much
  )
  // Model.railCols int    -- the rail's width; Model.drag bool -- a divider drag is in progress
  func (m Model) railWidth() int          // railCols clamped to the current terminal
  func (m Model) clampRail(cols int) int
  ```
- Changes: `cardLines(b, selected, showState bool, now time.Time, focused bool, width int)`,
  `railLines(rows, cursor int, attention bool, now time.Time, focused bool, width int)`.

- [ ] **Step 1: Failing tests.**

`layout_test.go`:

```go
func TestRailWidthClamps(t *testing.T) {
	m := Model{width: 140, height: 40, railCols: railDefault}
	if m.railWidth() != railDefault {
		t.Errorf("default = %d", m.railWidth())
	}
	if got := m.clampRail(5); got != railMin {
		t.Errorf("below the floor: %d", got)
	}
	if got := m.clampRail(200); got != 140-railGap-paneMin {
		t.Errorf("above the ceiling: %d", got)
	}
	// A narrower terminal lowers the ceiling below a stored width.
	m.railCols = 50
	m.width = 120
	if got := m.railWidth(); got != 120-railGap-paneMin {
		t.Errorf("stored 50 at 120 cols renders %d", got)
	}
	if m.paneWidth() != paneMin {
		t.Errorf("pane must keep paneMin, got %d", m.paneWidth())
	}
}
```

`split_test.go`:

```go
func TestRailResizeKeys(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'>'}})
	m = res.(Model)
	if m.railCols != railDefault+railStep {
		t.Errorf("> widens by railStep: %d", m.railCols)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'<'}})
	res, _ = res.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'<'}})
	m = res.(Model)
	if m.railCols != railDefault-railStep {
		t.Errorf("< narrows by railStep: %d", m.railCols)
	}
	for i := 0; i < 50; i++ {
		res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'<'}})
		m = res.(Model)
	}
	if m.railCols != railMin {
		t.Errorf("< stops at railMin: %d", m.railCols)
	}
	view := m.View()
	for i, l := range strings.Split(view, "\n") {
		if w := lipgloss.Width(l); w > 140 {
			t.Errorf("line %d is %d wide after resizing", i, w)
		}
	}
	// The pane's viewport follows the divider.
	if m.detail.vp.Width != m.paneWidth() {
		t.Errorf("viewport width %d, pane %d", m.detail.vp.Width, m.paneWidth())
	}
}
```

`mouse_test.go`:

```go
func TestDragDividerResizesRail(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	sep := m.railWidth()
	y := headerRows + 5
	press := tea.MouseMsg{X: sep, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	res, _ := m.Update(press)
	m = res.(Model)
	if !m.drag {
		t.Fatal("a press on the separator starts a drag")
	}
	res, _ = m.Update(tea.MouseMsg{X: sep + 10, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = res.(Model)
	if m.railCols != sep+10 {
		t.Errorf("motion moves the divider: %d, want %d", m.railCols, sep+10)
	}
	res, _ = m.Update(tea.MouseMsg{X: 3, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = res.(Model)
	if m.railCols != railMin {
		t.Errorf("a drag past the floor clamps: %d", m.railCols)
	}
	res, _ = m.Update(tea.MouseMsg{X: 3, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	m = res.(Model)
	if m.drag {
		t.Error("release ends the drag")
	}
	// Motion without a drag in progress does nothing.
	res, _ = m.Update(tea.MouseMsg{X: 60, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	if res.(Model).railCols != railMin {
		t.Error("stray motion must not move the divider")
	}
	// A press elsewhere is still a click, not a drag.
	res, _ = m.Update(click(railWidthOf(m)+railGap+10, headerRows+20))
	if res.(Model).drag {
		t.Error("a press in the pane is a click")
	}
}

func railWidthOf(m Model) int { return m.railWidth() }
```

Also, in every existing test that references the `railWidth` constant
(`TestCardLinesShapes`, `TestCardLinesTruncateLongName`, `TestHitRegionsSplit`,
`TestPaneGeometry`, `TestWheel*`, `TestClick*`, and the golden helper if it
does), replace it with `railDefault` where a number is meant and with
`m.railWidth()` where the model's width is meant; the extra `width`
argument to `cardLines`/`railLines` is `railDefault` in the rail tests.

- [ ] **Step 2:** `go test ./internal/ui` -- FAIL (compile: `railWidth` is
no longer a constant).

- [ ] **Step 3: Implement.**

`layout.go`: replace the `railWidth` constant with the four constants
above and add:

```go
// clampRail keeps the rail between railMin and what leaves the pane its
// paneMin, on the current terminal. On a terminal too narrow for both,
// the floor wins: a rail is useless below railMin, and the split has a
// width threshold anyway.
func (m Model) clampRail(cols int) int {
	max := m.width - railGap - paneMin
	if cols > max {
		cols = max
	}
	if cols < railMin {
		cols = railMin
	}
	return cols
}

// railWidth is the rail's drawn width: the stored preference, clamped to
// the terminal it is drawn on. Zero (a fresh model before prefs) reads as
// the default.
func (m Model) railWidth() int {
	if m.railCols == 0 {
		return m.clampRail(railDefault)
	}
	return m.clampRail(m.railCols)
}
```

`paneWidth` uses `m.railWidth()`. `Model` gains `railCols int` and `drag
bool`; `newModel` sets `railCols: railDefault`.

`rail.go`: `cardLines` and `railLines` take `width int` as the last
parameter and use it wherever they used the constant; `railView` and
`railTop` pass `m.railWidth()`. `model.go` `splitView` uses
`m.railWidth()` in both places. `mouse.go` `hit` uses `m.railWidth()`.

`keys.go`, in the keys-that-work-from-either-focus switch:

```go
	case "<", ">":
		d := railStep
		if msg.String() == "<" {
			d = -railStep
		}
		return m.setRail(m.railCols + d)
```

`model.go`:

```go
// setRail stores a new rail width, clamped, and re-fits the pane to the
// width that leaves. Task 3 adds the prefs save here.
func (m Model) setRail(cols int) (tea.Model, tea.Cmd) {
	m.railCols = m.clampRail(cols)
	m.detail.vp.Width = m.paneWidth()
	m.fillViewport()
	m.list.top = m.railTop()
	return m, nil
}
```

`WindowSizeMsg` already sets `vp.Width = m.paneWidth()`, which now
reflects the clamp; nothing else to add there.

`mouse.go`, `updateMouse`: the `if msg.Action != tea.MouseActionPress`
early return becomes drag handling:

```go
	switch msg.Action {
	case tea.MouseActionMotion:
		if m.drag {
			return m.setRail(msg.X)
		}
		return m, nil
	case tea.MouseActionRelease:
		if m.drag {
			m.drag = false
			return m, nil // Task 3: save prefs here
		}
		return m, nil
	case tea.MouseActionPress:
	default:
		return m, nil
	}
```

and, in the left-button case, before the region switch:

```go
		if m.layout() == layoutSplit && msg.X >= m.railWidth() && msg.X < m.railWidth()+railGap && m.hitBody(msg.Y) {
			m.drag = true
			return m, nil
		}
```

where `hitBody(y)` is the `top <= y < top+bodyRows` test `hit` already
does -- extract it so both use it.

- [ ] **Step 4:** `go test ./internal/ui` -- PASS, goldens unchanged.

- [ ] **Step 5: Mutation check.** In `clampRail`, drop the `paneMin`
ceiling: `TestRailWidthClamps` must fail on "above the ceiling". Revert;
record.

- [ ] **Step 6:** constituents green. Commit:
`feat(ui): the rail width is the human's -- < > and a draggable divider (#180)`

---

### Task 2: compact rail

**Files:**
- Modify: `internal/ui/rail.go`, `internal/ui/model.go`, `internal/ui/keys.go`
- Test: `internal/ui/rail_test.go`, `internal/ui/split_test.go`

**Interfaces:**
- Produces: `Model.compact bool`; `func compactLine(b relay.BindingStatus, selected, showState bool, now time.Time, focused bool, width int) string`
- Changes: `railLines(..., width int, compact bool)`.

- [ ] **Step 1: Failing tests.** `rail_test.go`:

```go
func TestCompactLineShape(t *testing.T) {
	b := relay.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderKind: "agy",
		Waiting: &relay.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)}}
	l := compactLine(b, true, false, railNow, true, railDefault)
	if w := lipgloss.Width(l); w != railDefault {
		t.Errorf("width %d", w)
	}
	if p := plain(l); p != "▎ webshop r4 question · 2m" {
		t.Errorf("compact = %q", p)
	}
	if p := plain(compactLine(b, false, true, railNow, true, railDefault)); !strings.HasPrefix(p, "webshop r4 NEEDS YOU · question · 2m") {
		t.Errorf("name order carries the state: %q", p)
	}
	// Narrow: the qualifier is what gives way, the name and round stay.
	if p := plain(compactLine(b, false, false, railNow, true, railMin)); !strings.HasPrefix(p, "webshop r4") {
		t.Errorf("at railMin = %q", p)
	}
}

func TestRailLinesCompact(t *testing.T) {
	rows := threeRows()
	full := railLines(rows, 0, true, railNow, true, railDefault, false)
	compact := railLines(rows, 0, true, railNow, true, railDefault, true)
	// 3 headers + 3 gaps + 3 one-line cards.
	if len(compact) != 9 {
		t.Errorf("compact rail has %d lines, want 9 (full has %d)", len(compact), len(full))
	}
	for i, l := range compact {
		if l.binding >= 0 && lipgloss.Width(l.text) != railDefault {
			t.Errorf("line %d width %d", i, lipgloss.Width(l.text))
		}
	}
}
```

`split_test.go`:

```go
func TestCompactToggleKeepsSelection(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	name := m.rows()[m.list.cursor].Name
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = res.(Model)
	if !m.compact || m.rows()[m.list.cursor].Name != name || m.detail.name != name {
		t.Errorf("c: compact %v cursor %q pane %q", m.compact, m.rows()[m.list.cursor].Name, m.detail.name)
	}
	if !strings.Contains(stripANSI(m.View()), "c cards") {
		t.Error("footer names the toggle's other state")
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if res.(Model).compact {
		t.Error("c twice is identity")
	}
}
```

- [ ] **Step 2:** FAIL.

- [ ] **Step 3: Implement.** `rail.go`:

```go
// compactLine is a binding's one-line card (spec §6.0): gutter, unread
// slot, name, round, then what · age as far as the width allows. The
// qualifier gives way first; the name and round always fit.
func compactLine(b relay.BindingStatus, selected, showState bool, now time.Time, focused bool, width int) string {
	gutter := " "
	if selected {
		if focused {
			gutter = accentStyle.Render("▎")
		} else {
			gutter = dimStyle.Render("▎")
		}
	}
	const unreadSlot = "  "
	nameStyle := fgStyle.Bold(true)
	if selected {
		nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	}
	round := dimStyle.Render(fmt.Sprintf("r%d", b.Round))
	head := gutter + unreadSlot + nameStyle.Render(b.Name) + " " + round + " "
	what, age := whatAge(b, now)
	var q []string
	if showState {
		q = append(q, stateStyle(b.Display).Render(b.Display))
	}
	if what != "" {
		q = append(q, dimStyle.Render(what))
	}
	if age != "" {
		q = append(q, dimStyle.Render(age))
	}
	l := fit(head+strings.Join(q, sep), width)
	if selected {
		l = selectedBg.Render(l)
	}
	return l
}
```

`railLines` gains `compact bool` and, per binding, emits
`compactLine(...)` as a single tagged line instead of `cardLines(...)`.
`railView` and `railTop` pass `m.compact`. `railBindingAt` (mouse.go)
passes it too.

`keys.go`: `case "c": m.compact = !m.compact; m.list.top = m.railTop();
return m, nil` (Task 3 adds the save). `footerView`: the key list gains
`key("c", "cards")` when compact and `key("c", "compact")` otherwise, in
both rail- and pane-focused split lists and in the stack list.

- [ ] **Step 4:** PASS; goldens unchanged.

- [ ] **Step 5:** constituents green. Commit:
`feat(ui): c toggles a one-line compact rail (#180)`

---

### Task 3: preferences remembered in `ui.json`

**Files:**
- Create: `internal/ui/prefs.go`, `internal/ui/prefs_test.go`
- Modify: `internal/ui/ui.go` (`Options.PrefsPath`, load at startup),
  `internal/ui/model.go`, `internal/ui/keys.go`, `internal/ui/mouse.go` (save on change)
- Modify: `cmd/relay/main.go` (`cmdUI` passes the path)

**Interfaces:**
- Produces:
  ```go
  // prefs is what ui.json holds: the three things a human sets and would
  // not want to set again next time.
  type prefs struct {
      Sort     string `json:"sort"`      // "attention" | "name"
      Compact  bool   `json:"compact"`
      RailCols int    `json:"rail_cols"`
  }
  func loadPrefs(path string) prefs               // defaults on any error; never errors
  func savePrefs(path string, p prefs) tea.Cmd     // atomic write; the message it returns is prefsSavedMsg{}
  func (m Model) prefs() prefs
  func (m Model) applyPrefs(p prefs) Model
  type prefsSavedMsg struct{}
  ```
- `Options.PrefsPath string` -- empty means no file: nothing loaded, nothing saved (tests, and any caller that wants a stateless ui).

- [ ] **Step 1: Failing tests.** `prefs_test.go`:

```go
package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrefsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.json")
	if got := loadPrefs(path); got != (prefs{}) {
		t.Errorf("missing file must load zero prefs, got %+v", got)
	}
	want := prefs{Sort: "name", Compact: true, RailCols: 42}
	if msg := savePrefs(path, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(path); got != want {
		t.Errorf("round trip: %+v", got)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadPrefs(path); got != (prefs{}) {
		t.Errorf("garbage must load zero prefs, got %+v", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("save must leave no temp file behind: %v", entries)
	}
}

func TestApplyPrefs(t *testing.T) {
	m := Model{width: 140, height: 40, sort: true}
	m = m.applyPrefs(prefs{Sort: "name", Compact: true, RailCols: 40})
	if m.sort || !m.compact || m.railCols != 40 {
		t.Errorf("applied: sort %v compact %v rail %d", m.sort, m.compact, m.railCols)
	}
	m = m.applyPrefs(prefs{})
	if !m.sort || m.compact || m.railCols != railDefault {
		t.Errorf("zero prefs restore defaults: sort %v compact %v rail %d", m.sort, m.compact, m.railCols)
	}
	if p := m.prefs(); p != (prefs{Sort: "attention", Compact: false, RailCols: railDefault}) {
		t.Errorf("prefs() = %+v", p)
	}
}

func TestChangesSaveWhenAPathIsSet(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.opts.PrefsPath = filepath.Join(t.TempDir(), "ui.json")
	for _, r := range []rune{'s', 'c', '>'} {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd == nil {
			t.Errorf("%q must return a save command", r)
		}
	}
	m.opts.PrefsPath = ""
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}); cmd != nil {
		t.Error("no path: no save command")
	}
}
```

(`tea` import.) The `'s'` case: `s` currently returns `(m, nil)`; with a
path it returns the save `tea.Cmd`. `'>'` likewise via `setRail`.

- [ ] **Step 2:** FAIL.

- [ ] **Step 3: Implement.** `prefs.go`:

```go
package ui

import (
	"encoding/json"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

// prefs is what ui.json holds: the three things a human sets and would
// not want to set again next time (spec §6.0). The ui's own file, under
// relay's state root, written by the ui alone and read by nothing else.
type prefs struct {
	Sort     string `json:"sort"` // "attention" | "name"
	Compact  bool   `json:"compact"`
	RailCols int    `json:"rail_cols"`
}

type prefsSavedMsg struct{}

// loadPrefs reads path; any error -- missing, unreadable, not JSON -- is
// the zero prefs, which applyPrefs reads as the defaults. Never errors:
// a preference file is not worth refusing to start over.
func loadPrefs(path string) prefs {
	var p prefs
	data, err := os.ReadFile(path)
	if err != nil {
		return prefs{}
	}
	if json.Unmarshal(data, &p) != nil {
		return prefs{}
	}
	return p
}

// savePrefs writes p to path atomically (temp file in the same directory,
// then rename) from inside the command, so Update stays pure. A failed
// save is silent: the change still applies for this run.
func savePrefs(path string, p prefs) tea.Cmd {
	return func() tea.Msg {
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return prefsSavedMsg{}
		}
		dir := filepath.Dir(path)
		_ = os.MkdirAll(dir, 0o755)
		tmp, err := os.CreateTemp(dir, ".ui-*.json")
		if err != nil {
			return prefsSavedMsg{}
		}
		name := tmp.Name()
		if _, err := tmp.Write(append(data, '\n')); err != nil {
			tmp.Close()
			os.Remove(name)
			return prefsSavedMsg{}
		}
		if err := tmp.Close(); err != nil {
			os.Remove(name)
			return prefsSavedMsg{}
		}
		if err := os.Rename(name, path); err != nil {
			os.Remove(name)
		}
		return prefsSavedMsg{}
	}
}

// prefs is the model's current preferences, as saved.
func (m Model) prefs() prefs {
	sort := "attention"
	if !m.sort {
		sort = "name"
	}
	return prefs{Sort: sort, Compact: m.compact, RailCols: m.railWidthStored()}
}

// applyPrefs sets the model from p; zero values mean the defaults.
func (m Model) applyPrefs(p prefs) Model {
	m.sort = p.Sort != "name"
	m.compact = p.Compact
	m.railCols = railDefault
	if p.RailCols > 0 {
		m.railCols = p.RailCols
	}
	return m
}

// save is the command every preference change returns: the save when a
// path is configured, nil otherwise.
func (m Model) save() tea.Cmd {
	if m.opts.PrefsPath == "" {
		return nil
	}
	return savePrefs(m.opts.PrefsPath, m.prefs())
}
```

`railWidthStored()` returns `m.railCols`, or `railDefault` when zero (the
unclamped preference: a width set on a wide terminal is kept, not
squashed by a narrow one). Add it to `layout.go`.

Wire the save: `s` returns `m, m.save()`; `c` likewise; `setRail` returns
`m, m.save()`; the drag's `MouseActionRelease` branch returns `m,
m.save()`. `Update` gains `case prefsSavedMsg: return m, nil`.

`ui.go`: `Options` gains

```go
	// PrefsPath is the ui's own preference file (spec §6.0); "" keeps the
	// ui stateless -- nothing loaded, nothing saved.
	PrefsPath string
```

and `Run`, after clamping `Interval`: `model := newModel(ctx, rt, opts);
if opts.PrefsPath != "" { model = model.applyPrefs(loadPrefs(opts.PrefsPath)) }`
then `tea.NewProgram(model, ...)`.

`cmd/relay/main.go`, `cmdUI`: resolve the state root the way
`newRuntime` does (`store.DefaultRoot()`) -- or, if `newRuntime` already
exposes the root on the runtime it returns, read it from there; do not
add a second resolver -- and pass `PrefsPath: filepath.Join(root,
"ui.json")`. Two lines plus the import.

- [ ] **Step 4:** `go test ./internal/ui` -- PASS; goldens unchanged
(`splitModel` sets no `PrefsPath`, so the fixtures stay stateless).

- [ ] **Step 5: Mutation check.** Make `savePrefs` write to `path`
directly with `os.WriteFile` instead of temp-and-rename, and make it
leave the temp file behind: `TestPrefsRoundTrip` must fail on "no temp
file behind" (introduce the leak deliberately: create the temp file and
skip the rename). Revert; record.

- [ ] **Step 6:** constituents green. Commit:
`feat(ui): sort, compact and the rail width are remembered in ui.json (#180)`

---

## Report

`NNN-report.md`: the constituents' tails; the two mutation outcomes;
confirmation that no golden changed; the `cmd/relay/main.go` diff (it
should be the import and the `PrefsPath` line); anything stopped on, with
the step number.
