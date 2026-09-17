# `relay ui` split dashboard: rail beside a live pane (#180)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-17-relay-ui-split-design.md`
**Issue:** #180
**Depends on:** nothing open. #143, #135, #137 and #150 come *after* this
and slot into the rows this plan lays out.

The spec is in your worktree. Read the section a step cites when the
rationale is not obvious -- this plan tells you what, the spec tells you why.

**Goal:** At 110+ columns `relay ui` is one screen: a 34-column rail of
binding cards grouped by state in attention order, and a pane beside it
that follows the rail cursor and shows the selected binding's report,
terminal, diff or log. Below 110 columns the existing two-screen
drill-down stays, drawing the same cards and the same pane full-width.
The palette becomes the one in spec §7.

**Architecture:** `internal/ui` keeps its shape -- `fetch.go` is the only
file touching `rt` and is not modified; `Update` stays a pure function of
messages. Rendering moves out of `list.go`/`detail.go` into `rail.go`
(cards, headers, windowing over tagged lines) and `pane.go` (head, tabs,
source line, hint, diff colouring). `model.go` gains the layout switch,
`pointDetailAt`, the sort toggle and a clock. `internal/relay` gains a pure
`SortRows` and two additive `BindingStatus` fields.

**Tech stack:** Go, bubbletea + bubbles/viewport + lipgloss (already
dependencies). No new dependencies.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find
in the code, **stop and say so in your report**. Do not bend a test to fit,
and do not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run
its constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr`, `agy`, `claude` or `opencode` yourself. **No test is
added under `cmd/relay`** (CI runners have no `herdr`; CLAUDE.md). Every
test in this plan is in `internal/relay` or `internal/ui`, against the
existing `fakeHerdr` in `internal/ui/fake_test.go` and `store.New(t.TempDir())`.

## Global constraints

- No new dependencies; `go mod tidy` produces no diff.
- `internal/ui/fetch.go` is not modified. `Update` never calls `rt`
  directly; fetches are returned as `tea.Cmd`s (old spec §3, §6).
- The ui never writes state: no store writes, no log entries, no herdr
  calls beyond `ListAgents`/`ReadAgent` (the `fakeHerdr` fails the test on
  any other call).
- `resolveSticky` and `listWindow`'s five rules are not changed in
  meaning. `listWindow` becomes a wrapper over `railWindow` (Task 3).
- Every colour is an xterm-256 index from spec §7. No lipgloss colour
  outside `styles.go`.
- Every rendered line is ≤ the terminal width (`lipgloss.Width`). Golden
  tests assert it.
- `splitMinWidth = 110`, `railWidth = 34`: named constants in
  `layout.go`, nothing else hard-codes them.
- The rail reserves the two-column unread slot after the gutter (spec
  §3.3) even though nothing fills it yet. Do not "tidy" it away.
- Every new exported or package symbol gets a doc comment in the house
  style; the spec gives the text where it matters.
- Time comes from `Model.now` (Task 5), never `time.Now()` inside a
  render function, so ages are testable.
- One commit per task, on the worktree's branch. Do not push, do not
  open a PR. Do not create or switch branches.
- Existing tests that pin the old rendering are updated in the task that
  changes the rendering, as listed there -- never deleted, and never
  loosened beyond what the step says.

---

### Task 1: `SortRows`, `BindingStatus.Branch`, `BindingStatus.Waiting`

**Files:**
- Create: `internal/relay/sort.go`, `internal/relay/sort_test.go`
- Modify: `internal/relay/status.go` (`BindingStatus`, `statusRow`)
- Modify: `internal/relay/waiting.go` (json tags on `Waiting`)
- Modify: `internal/relay/status_test.go` (two new tests)
- Modify: `README.md` (the `status --json` field list)

**Interfaces:**
- Produces: `func SortRows(rows []BindingStatus, attention bool) []BindingStatus`
- Produces: `BindingStatus.Branch string`, `BindingStatus.Waiting *Waiting`
- Consumes: `WaitingOn`, `questionFirstLine` (`waiting.go`), unchanged.

- [ ] **Step 1: Write the failing `SortRows` tests.**

`internal/relay/sort_test.go`:

```go
package relay

import (
	"testing"
	"time"
)

func names(rows []BindingStatus) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSortRowsAttentionOrder: every display state, in every input order,
// lands NEEDS YOU, HELD, ACTIVE, DONE.
func TestSortRowsAttentionOrder(t *testing.T) {
	rows := []BindingStatus{
		{Name: "d", Display: "DONE"},
		{Name: "a", Display: "ACTIVE"},
		{Name: "n", Display: "NEEDS YOU"},
		{Name: "h", Display: "HELD"},
	}
	want := []string{"n", "h", "a", "d"}
	// Rotate the input through every starting point: four permutations
	// that each begin with a different state.
	for shift := 0; shift < len(rows); shift++ {
		in := append(append([]BindingStatus{}, rows[shift:]...), rows[:shift]...)
		got := names(SortRows(in, true))
		if !equalNames(got, want) {
			t.Errorf("shift %d: got %v want %v", shift, got, want)
		}
	}
}

// TestSortRowsWithinGroup: newest Last.TS first, nil Last last, name as
// the tiebreak.
func TestSortRowsWithinGroup(t *testing.T) {
	t0 := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	rows := []BindingStatus{
		{Name: "old", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-time.Hour)}},
		{Name: "none", Display: "ACTIVE"},
		{Name: "new", Display: "ACTIVE", Last: &LastEvent{TS: t0}},
		{Name: "tie-b", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
		{Name: "tie-a", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
	}
	got := names(SortRows(rows, true))
	want := []string{"new", "old", "tie-a", "tie-b", "none"}
	if !equalNames(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestSortRowsNameOrder: attention=false is plain name order regardless
// of state, and the input slice is untouched either way.
func TestSortRowsNameOrder(t *testing.T) {
	rows := []BindingStatus{
		{Name: "b", Display: "DONE"},
		{Name: "a", Display: "NEEDS YOU"},
		{Name: "c", Display: "ACTIVE"},
	}
	before := names(rows)
	got := names(SortRows(rows, false))
	if !equalNames(got, []string{"a", "b", "c"}) {
		t.Errorf("name order: got %v", got)
	}
	SortRows(rows, true)
	if !equalNames(names(rows), before) {
		t.Errorf("input mutated: %v -> %v", before, names(rows))
	}
}
```

- [ ] **Step 2:** `go test ./internal/relay -run TestSortRows` -- FAIL:
`SortRows` undefined.

- [ ] **Step 3: Implement `SortRows`.**

`internal/relay/sort.go`:

```go
package relay

import "sort"

// attentionRank orders display states for a human: what needs a decision
// first, what is waiting on the planner next, then what is working, then
// what is finished. Unknown states (none today) sort after DONE.
var attentionRank = map[string]int{
	"NEEDS YOU": 0,
	"HELD":      1,
	"ACTIVE":    2,
	"DONE":      3,
}

func rankOf(display string) int {
	if r, ok := attentionRank[display]; ok {
		return r
	}
	return len(attentionRank)
}

// SortRows orders status rows for a human. attention groups by display
// state -- NEEDS YOU, HELD, ACTIVE, DONE -- and within a group puts the
// most recent Last.TS first (a nil Last last), name as the tiebreak; name
// is the order Status has always returned. Stable; never mutates its
// input. Only `relay ui` calls it today; #143 moves `status` onto it.
func SortRows(rows []BindingStatus, attention bool) []BindingStatus {
	out := make([]BindingStatus, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if attention {
			if ra, rb := rankOf(a.Display), rankOf(b.Display); ra != rb {
				return ra < rb
			}
			switch {
			case a.Last != nil && b.Last != nil && !a.Last.TS.Equal(b.Last.TS):
				return a.Last.TS.After(b.Last.TS)
			case a.Last != nil && b.Last == nil:
				return true
			case a.Last == nil && b.Last != nil:
				return false
			}
		}
		return a.Name < b.Name
	})
	return out
}
```

- [ ] **Step 4:** `go test ./internal/relay -run TestSortRows` -- PASS.

- [ ] **Step 5: Write the failing `statusRow` tests.**

Find how `internal/relay/status_test.go` builds a `store.Binding` and a
`Runtime` for existing `statusRow`/`Status` tests (search for
`func TestStatus`) and use the same helpers. Add:

```go
// TestStatusRowBranch: the worktree branch reaches the row; a --cwd
// binding (no branch) leaves it empty.
func TestStatusRowBranch(t *testing.T) {
	// build rt and a binding exactly as the nearest existing statusRow
	// test does, then:
	b.Branch = "relay/webshop"
	row, err := statusRow(context.Background(), rt, b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.Branch != "relay/webshop" {
		t.Errorf("Branch = %q", row.Branch)
	}
	b.Branch = ""
	row, _ = statusRow(context.Background(), rt, b, nil, nil)
	if row.Branch != "" {
		t.Errorf("--cwd binding Branch = %q, want empty", row.Branch)
	}
}

// TestStatusRowWaiting: a needs_you binding whose round has a captured
// question carries Waiting{Cause: "blocked", Hint: "relay answer ..."};
// an active binding carries nil.
func TestStatusRowWaiting(t *testing.T) {
	// build rt, a binding in store.StateNeedsYou at round 2, and append a
	// to_planner/question log entry for round 2 through rt.Store the way
	// the existing needs_you tests in this file do; then:
	row, err := statusRow(context.Background(), rt, b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.Waiting == nil || row.Waiting.Cause != "blocked" {
		t.Fatalf("Waiting = %+v, want cause blocked", row.Waiting)
	}
	if row.Waiting.Hint != "relay answer --name "+b.Name {
		t.Errorf("Hint = %q", row.Waiting.Hint)
	}
	b.State = store.StateActive
	row, _ = statusRow(context.Background(), rt, b, nil, nil)
	if row.Waiting != nil {
		t.Errorf("active row Waiting = %+v, want nil", row.Waiting)
	}
}
```

If no existing test in the file seeds a question entry, look at
`internal/relay/waiting_test.go` for the entry shape and use
`rt.Store.AppendLog` (or whatever the store's append is called -- read
`internal/store/log.go`) directly.

- [ ] **Step 6:** `go test ./internal/relay -run 'TestStatusRow(Branch|Waiting)'`
-- FAIL: `row.Branch` / `row.Waiting` undefined.

- [ ] **Step 7: Add the fields and fill them.**

In `status.go`, after `Switches` in `BindingStatus`:

```go
	// Branch is the binding's worktree branch; "" for a --cwd binding,
	// which has no worktree of its own.
	Branch string `json:"branch,omitempty"`
	// Waiting is set when the binding is stalled on a human (WaitingOn):
	// the cause, the one-line reason, since when, and the verb that
	// resolves it. Nil otherwise, including for a switchable broken
	// binding the daemon is about to fix itself.
	Waiting *Waiting `json:"waiting,omitempty"`
```

In `statusRow`, add `Branch: b.Branch,` to the literal, and after the
`entries, err := rt.Store.ReadLog(b.Name)` block:

```go
	if w, ok := WaitingOn(b, entries, questionFirstLine(rt)); ok {
		row.Waiting = &w
	}
```

In `waiting.go`, give `Waiting` json tags so the field serialises in
`status --json` the way every other `BindingStatus` field does:

```go
type Waiting struct {
	Name  string    `json:"name"`
	Round int       `json:"round"`
	Cause string    `json:"cause"`
	Line  string    `json:"line"`
	Since time.Time `json:"since,omitempty"`
	Hint  string    `json:"hint"`
}
```

(Keep the existing trailing comments on each field.)

- [ ] **Step 8:** `go test ./internal/relay` -- PASS, including every
existing test. If any `status --json` golden in `cmd/relay` or
`internal/relay` now differs because of the two new fields, update that
golden's expected document and say so in the report; do not add
`omitempty` semantics the spec did not ask for.

- [ ] **Step 9: README.** Find the `status --json` field list in
`README.md` (search for `"last_close"` or `"consults"`) and add, in the
same style:

```
branch      the binding's worktree branch; absent for a --cwd binding
waiting     set when the binding is stalled on a human: cause, line, since, hint
```

- [ ] **Step 10:** run the four `make check` constituents. Green. Commit:

```
feat(status): rows carry branch and waiting; SortRows orders by attention (#180)
```

---

### Task 2: palette and layout constants

**Files:**
- Modify: `internal/ui/styles.go` (rewrite)
- Create: `internal/ui/layout.go`, `internal/ui/layout_test.go`
- Modify: `internal/ui/list_test.go` (`TestStateStylesDistinguishable`)

**Interfaces:**
- Produces (styles): `fgStyle, dimStyle, faintStyle, ruleStyle, selectedBg,
  headerBar, accentStyle, activeTabStyle, inactiveTabStyle, errorStyle,
  emptyStyle, diffAddStyle, diffDelStyle, diffHunkStyle, diffFileStyle`,
  plus `stateStyle(display string) lipgloss.Style` and
  `pillStyle(display string) lipgloss.Style`.
- Produces (layout): `splitMinWidth, railWidth, railGap, headerRows,
  footerRows, paneHeadRows, tabRows, sourceRows`; `type layout int`;
  `layoutSplit, layoutStack`; `func (m Model) layout() layout`;
  `func (m Model) paneWidth() int`; `func (m Model) viewportHeight() int`.

- [ ] **Step 1: Write the failing layout test.**

`internal/ui/layout_test.go`:

```go
package ui

import "testing"

func TestLayoutThreshold(t *testing.T) {
	m := Model{height: 40}
	m.width = splitMinWidth - 1
	if m.layout() != layoutStack {
		t.Errorf("%d columns: want stack", m.width)
	}
	m.width = splitMinWidth
	if m.layout() != layoutSplit {
		t.Errorf("%d columns: want split", m.width)
	}
}

func TestPaneGeometry(t *testing.T) {
	m := Model{width: 140, height: 40}
	if got := m.paneWidth(); got != 140-railWidth-railGap {
		t.Errorf("split paneWidth = %d", got)
	}
	m.width = 80
	if got := m.paneWidth(); got != 80 {
		t.Errorf("stack paneWidth = %d", got)
	}
	// 40 rows - header 2 - footer 1 - pane head 5 - tabs 2 - source 2 = 28
	if got := m.viewportHeight(); got != 28 {
		t.Errorf("viewportHeight = %d, want 28", got)
	}
	m.height = 5
	if got := m.viewportHeight(); got != 0 {
		t.Errorf("viewportHeight on a tiny terminal = %d, want 0", got)
	}
}
```

- [ ] **Step 2:** `go test ./internal/ui -run 'TestLayoutThreshold|TestPaneGeometry'`
-- FAIL: undefined.

- [ ] **Step 3: Write `layout.go`.**

```go
package ui

// Geometry of the two layouts (spec §3.1). Every number that shapes the
// screen is here and nowhere else.
const (
	splitMinWidth = 110 // columns; at or above, rail + pane
	railWidth     = 34  // columns, including the 1-column selection gutter
	railGap       = 2   // separator column + 1 pad
	headerRows    = 2   // header bar + blank
	footerRows    = 1
	paneHeadRows  = 5   // title, planner, builder, tree, blank
	tabRows       = 2   // tab bar + rule
	sourceRows    = 2   // source line + blank
)

// layout is which of the two screens the terminal width earns.
type layout int

const (
	layoutSplit layout = iota // rail beside pane
	layoutStack               // drill-down: list, then full-width detail
)

func (m Model) layout() layout {
	if m.width >= splitMinWidth {
		return layoutSplit
	}
	return layoutStack
}

// paneWidth is the columns the pane (and its viewport) gets.
func (m Model) paneWidth() int {
	if m.layout() == layoutSplit {
		return m.width - railWidth - railGap
	}
	return m.width
}

// bodyRows is the rows between the header and the footer, less whatever
// the error block takes. Zero before the first WindowSizeMsg.
func (m Model) bodyRows() int {
	if m.height <= 0 {
		return 0
	}
	rows := m.height - headerRows - footerRows - m.errorRows()
	if rows < 0 {
		return 0
	}
	return rows
}

// viewportHeight is bodyRows less the pane's own furniture, floored at 0.
// The same in both layouts: the pane head is drawn full-width on the
// detail screen too, so a resize across the threshold changes the
// viewport's width and nothing else.
func (m Model) viewportHeight() int {
	h := m.bodyRows() - paneHeadRows - tabRows - sourceRows
	if h < 0 {
		return 0
	}
	return h
}
```

`errorRows()` is the line count of `renderError(m.err, m.width)` when
`m.err != nil`, else 0 -- extract it from the existing `listRows` in
`list.go` (which becomes `bodyRows` in Task 3; for now add `errorRows` to
`list.go` next to `listRows` and have `listRows` use it).

- [ ] **Step 4: Rewrite `styles.go`.**

```go
package ui

import "github.com/charmbracelet/lipgloss"

// The palette is spec §7: every colour an xterm-256 index so a terminal
// theme renders it consistently. Nothing outside this file names a colour.
var (
	fgStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	faintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("239"))
	ruleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	selectedBg = lipgloss.NewStyle().Background(lipgloss.Color("235"))
	headerBar  = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))

	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))

	activeTabStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Background(lipgloss.Color("24"))
	inactiveTabStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	emptyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	normalStyle = lipgloss.NewStyle()

	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	diffFileStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))

	stateNeedsYouStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	stateHeldStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	stateActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stateDoneStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
)

// stateStyle colours a display word; the four states have four colours and
// anything else renders plain, so a new state is visible before it is
// styled.
func stateStyle(display string) lipgloss.Style {
	switch display {
	case "NEEDS YOU":
		return stateNeedsYouStyle
	case "HELD":
		return stateHeldStyle
	case "ACTIVE":
		return stateActiveStyle
	case "DONE":
		return stateDoneStyle
	}
	return normalStyle
}

// pillStyle is the pane title's state badge: black on the state colour.
func pillStyle(display string) lipgloss.Style {
	fg := stateStyle(display).GetForeground()
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("16")).Background(fg).Padding(0, 1)
}
```

Delete `headerStyle`, `footerStyle`, `cursorStyle`. They have callers in
`list.go`, `detail.go` and tests; those callers are rewritten in Tasks 3
and 4. To keep this task compiling on its own, leave **temporary**
aliases at the bottom of `styles.go`:

```go
// Removed in Task 3/4; kept so the old renderers compile until then.
var (
	headerStyle = fgStyle
	footerStyle = dimStyle
	cursorStyle = accentStyle
)
```

- [ ] **Step 5: Update `TestStateStylesDistinguishable`** in
`list_test.go` so it asserts over `stateStyle` for the four states
(`"NEEDS YOU"`, `"HELD"`, `"ACTIVE"`, `"DONE"`): each pair of rendered
words differs, and `stateStyle("HELD")` is not `normalStyle` (it was
unstyled before -- that is the regression this pins).

- [ ] **Step 6:** `go test ./internal/ui` -- PASS. (`styleDisplay` in
`list.go` still calls the old names through the aliases; that is fine
until Task 3.)

- [ ] **Step 7:** `make check` constituents green. Commit:

```
feat(ui): spec §7 palette and the split/stack layout constants (#180)
```

---

### Task 3: the rail

**Files:**
- Create: `internal/ui/rail.go`, `internal/ui/rail_test.go`
- Modify: `internal/ui/list.go` (remove `renderBorder`, `styleDisplay`,
  `renderListRow`, `listRows`; `listWindow` wraps `railWindow`; `listView`
  draws the rail full-width)
- Modify: `internal/ui/list_test.go` (needles, per Step 8)
- Modify: `internal/ui/model.go` (`sort`, `now`, `rows()`; `listRows` callers)

**Interfaces:**
- Consumes: `relay.SortRows`, `relay.HoldText`, `relay.NudgeText`,
  `BindingStatus.Waiting/Branch` (Task 1); styles and constants (Task 2).
- Produces:
  - `func (m Model) rows() []relay.BindingStatus`
  - `type railLine struct { text string; binding int }`
  - `func cardLines(b relay.BindingStatus, selected, showState bool, now time.Time) []string`
  - `func railLines(rows []relay.BindingStatus, cursor int, attention bool, now time.Time) []railLine`
  - `func railSpan(lines []railLine, binding int) (first, last int)`
  - `func railWindow(top, first, last, rows, n int) int`
  - `func (m Model) railView(width int) string` -- `bodyRows()` lines, each padded to `width`
  - `func ago(since, now time.Time) string` -- `"2m"`, `"3h"`, `"2d"`, `""` for zero
  - `func fit(s string, width int) string` -- pad or truncate to `width` by `lipgloss.Width`

- [ ] **Step 1: Model fields.** In `model.go` add to `Model`:

```go
	// sort is true for attention order (the default); s toggles it. Lives
	// for the process only -- persisting it is #143's sidecar question.
	sort bool
	// now is the clock every age on screen is measured against. time.Now
	// in production; fixed in tests so "2m ago" is deterministic.
	now func() time.Time
```

In `newModel` set `sort: true, now: time.Now`. Add:

```go
// rows is the report's bindings in display order. Every index in the
// model -- list.cursor, list.top -- indexes THIS slice, never
// report.Bindings directly.
func (m Model) rows() []relay.BindingStatus {
	return relay.SortRows(m.report.Bindings, m.sort)
}
```

Replace every `m.report.Bindings[...]`, `len(m.report.Bindings)` and
`resolveSticky(m.report)` use in `model.go`, `keys.go` and `list.go` that
indexes by cursor with `m.rows()`. `resolveSticky` takes a `relay.Report`;
call it as `m.list.resolveSticky(relay.Report{Bindings: m.rows()})` (its
body only reads `.Bindings`). The footer's "other binding NEEDS YOU" loop
may keep reading `m.report.Bindings` -- order does not matter there.

- [ ] **Step 2: Write the failing rail tests.**

`internal/ui/rail_test.go`:

```go
package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

// railNow is in the local zone on purpose: the ui formats clocks with
// .Local(), and a UTC fixture would make "13:02" depend on the machine.
var railNow = time.Date(2026, 9, 17, 14, 2, 0, 0, time.Local)

// plain strips ANSI and collapses runs of spaces, so a card assertion
// pins the words and their order, not the padding fit() adds.
func plain(s string) string { return strings.Join(strings.Fields(stripANSI(s)), " ") } // stripANSI: Step 3

func TestCardLinesShapes(t *testing.T) {
	blocked := relay.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		BuilderKind: "agy", BuilderStatus: "blocked", Branch: "relay/webshop", Dirty: true, Consults: 2,
		Waiting: &relay.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)},
	}
	held := relay.BindingStatus{
		Name: "ledger", Round: 3, Display: "HELD", BuilderKind: "agy", Branch: "relay/ledger",
		Pending: &relay.PendingInfo{Round: 3, Kind: store.KindReport, Hold: &relay.HoldInfo{QuietMS: 23000, GraceMS: 60000}},
	}
	headless := relay.BindingStatus{
		Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "opencode", BuilderStatus: "working",
		Branch: "relay/api", Headless: &relay.HeadlessInfo{PID: 48211},
	}
	cwd := relay.BindingStatus{Name: "docs", Round: 1, Display: "DONE", BuilderKind: "agy",
		Last: &relay.LastEvent{TS: railNow.Add(-3 * time.Hour)}}

	cases := []struct {
		name string
		b    relay.BindingStatus
		want []string // plain text, trailing spaces trimmed
	}{
		{"blocked", blocked, []string{
			"▎ webshop r4",
			"question · 2m",
			"dirty · 2 consults",
			"agy · relay/webshop",
		}},
		{"held", held, []string{
			"ledger r3",
			"report r3 · quiet 23s of 1m0s",
			"agy · relay/ledger",
		}},
		{"headless", headless, []string{
			"api r2",
			"working",
			"opencode · headless · relay/api",
		}},
		{"cwd done", cwd, []string{
			"docs r1",
			"done · 3h",
			"agy",
		}},
	}
	for _, tc := range cases {
		got := cardLines(tc.b, tc.name == "blocked", false, railNow)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %d lines, want %d:\n%s", tc.name, len(got), len(tc.want), strings.Join(got, "\n"))
		}
		for i := range got {
			if w := lipgloss.Width(got[i]); w != railWidth {
				t.Errorf("%s line %d width %d, want %d: %q", tc.name, i, w, railWidth, got[i])
			}
			if p := plain(got[i]); p != tc.want[i] {
				t.Errorf("%s line %d:\n got %q\nwant %q", tc.name, i, p, tc.want[i])
			}
		}
	}
}

func TestCardLinesNameOrderShowsState(t *testing.T) {
	b := relay.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working"}
	got := plain(cardLines(b, false, true, railNow)[1])
	if !strings.HasPrefix(got, "ACTIVE · working") {
		t.Errorf("line 2 = %q", got)
	}
	got = plain(cardLines(b, false, false, railNow)[1])
	if strings.Contains(got, "ACTIVE") {
		t.Errorf("attention order must not repeat the state on the card: %q", got)
	}
}

func TestCardLinesTruncateLongName(t *testing.T) {
	b := relay.BindingStatus{Name: strings.Repeat("x", 60), Round: 1, Display: "ACTIVE", BuilderKind: "agy"}
	for i, l := range cardLines(b, false, false, railNow) {
		if w := lipgloss.Width(l); w != railWidth {
			t.Errorf("line %d width %d", i, w)
		}
	}
}

func TestRailLinesGroupsAndTags(t *testing.T) {
	rows := []relay.BindingStatus{
		{Name: "n", Display: "NEEDS YOU", BuilderKind: "agy"},
		{Name: "a1", Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "a2", Display: "ACTIVE", BuilderKind: "agy"},
	}
	lines := railLines(rows, 1, true, railNow)
	// header, card n (3 lines), gap, header, card a1 (3), card a2 (3), gap
	if len(lines) != 1+3+1+1+3+3+1 {
		t.Fatalf("%d lines", len(lines))
	}
	if lines[0].binding != -1 || plain(lines[0].text) != "NEEDS YOU 1" {
		t.Errorf("first line = %+v", lines[0])
	}
	if lines[5].binding != -1 || plain(lines[5].text) != "ACTIVE 2" {
		t.Errorf("second header = %+v", lines[5])
	}
	first, last := railSpan(lines, 1)
	if first != 6 || last != 8 {
		t.Errorf("span of a1 = [%d,%d]", first, last)
	}
	// Name order: no headers, no gaps, every line tagged.
	for _, l := range railLines(rows, 0, false, railNow) {
		if l.binding < 0 {
			t.Errorf("name order emitted an untagged line %q", plain(l.text))
		}
	}
}

func TestRailWindowSpan(t *testing.T) {
	cases := []struct{ top, first, last, rows, n, want int }{
		{0, 0, 0, 5, 3, 0},   // everything fits
		{0, 8, 10, 5, 20, 6},  // span below the window: pull down so last is visible
		{10, 2, 4, 5, 20, 2},  // span above: pull up to first
		{0, 3, 12, 5, 20, 3},  // span taller than rows: pin to first
		{18, 0, 0, 5, 20, 0},  // clamp then follow
	}
	for _, c := range cases {
		if got := railWindow(c.top, c.first, c.last, c.rows, c.n); got != c.want {
			t.Errorf("railWindow(%d,%d,%d,%d,%d) = %d, want %d", c.top, c.first, c.last, c.rows, c.n, got, c.want)
		}
	}
}
```

- [ ] **Step 3: `stripANSI` test helper.** If `internal/ui` tests have no
ANSI-stripping helper yet, add to `rail_test.go`:

```go
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }
```

(`regexp` import.) Rendering in tests may or may not emit escapes
depending on lipgloss's colour-profile detection; stripping makes the
assertions independent of that.

- [ ] **Step 4:** `go test ./internal/ui -run 'TestCardLines|TestRailLines|TestRailWindow'`
-- FAIL: undefined.

- [ ] **Step 5: Write `rail.go`.**

```go
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
)

// railLine is one rendered rail row tagged with the binding it belongs
// to, so the window can keep a whole card visible. Headers and gap lines
// carry -1.
type railLine struct {
	text    string
	binding int
}

// sep joins card facts with a faint middle dot.
var sep = faintStyle.Render(" · ")

// fit pads or truncates s to width cells. Truncation is by cell, no
// ellipsis: at 34 columns an ellipsis costs more than it says.
func fit(s string, width int) string {
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// ago is a coarse age for a card: minutes under an hour, hours under a
// day, then days. "" for a zero time, so a caller can omit the fact.
func ago(since, now time.Time) string {
	if since.IsZero() {
		return ""
	}
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// whatAge is a card's second line: what the binding is on, and for how
// long, from the fields Status has today (spec §3.3 table). #135's labels
// and #137's PAUSED join here.
func whatAge(b relay.BindingStatus, now time.Time) (what, age string) {
	switch b.Display {
	case "NEEDS YOU":
		if b.Waiting != nil {
			what = b.Waiting.Cause
			if what == "blocked" {
				what = "question"
			}
			age = ago(b.Waiting.Since, now)
		} else if b.Detail != "" {
			what = b.Detail
		} else {
			what = "needs you"
		}
	case "HELD":
		if b.Pending != nil {
			what = fmt.Sprintf("%s r%d", b.Pending.Kind, b.Pending.Round)
		}
		age = relay.HoldText(b)
	case "ACTIVE":
		what = b.BuilderStatus
		if b.Nudge != nil {
			age = relay.NudgeText(*b.Nudge)
		}
	case "DONE":
		what = "done"
		if b.Last != nil {
			age = ago(b.Last.TS, now)
		}
	}
	return what, age
}

// facts is a card's optional third line: tree and round facts, in a fixed
// order, only those that apply. #143's live +N −M goes first here.
func facts(b relay.BindingStatus) []string {
	var out []string
	if b.Dirty {
		out = append(out, stateNeedsYouStyle.Render("dirty"))
	}
	if b.Consults > 0 {
		out = append(out, dimStyle.Render(fmt.Sprintf("%d consults", b.Consults)))
	}
	if b.Switches > 0 {
		out = append(out, dimStyle.Render(fmt.Sprintf("switched %dx", b.Switches)))
	}
	if b.ForkedFrom != "" {
		out = append(out, dimStyle.Render(fmt.Sprintf("forked from %s r%d", b.ForkedFrom, b.ForkedAtRound)))
	}
	return out
}

// cardLines renders one binding's card: three or four lines, each exactly
// railWidth wide. selected paints the gutter and background; showState
// puts the state word on line 2 (name order has no group headers).
func cardLines(b relay.BindingStatus, selected, showState bool, now time.Time) []string {
	gutter := " "
	if selected {
		gutter = accentStyle.Render("▎")
	}
	const unreadSlot = "  " // reserved for #143's ● -- keep the width
	nameStyle := fgStyle.Bold(true)
	if selected {
		nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	}
	round := dimStyle.Render(fmt.Sprintf("r%d", b.Round))
	nameWidth := railWidth - 1 - 2 - lipgloss.Width(round) - 1 // gutter, slot, round, pad
	name := nameStyle.Render(fit(b.Name, nameWidth))
	l1 := gutter + unreadSlot + name + round + " "

	what, age := whatAge(b, now)
	var l2 []string
	if showState {
		l2 = append(l2, stateStyle(b.Display).Render(b.Display))
	}
	if what != "" {
		if showState {
			l2 = append(l2, dimStyle.Render(what))
		} else {
			l2 = append(l2, fgStyle.Render(what))
		}
	}
	if age != "" {
		l2 = append(l2, dimStyle.Render(age))
	}

	var l4 []string
	if b.BuilderKind != "" {
		l4 = append(l4, dimStyle.Render(b.BuilderKind))
	}
	if b.Headless != nil {
		l4 = append(l4, dimStyle.Render("headless"))
	}
	if b.Branch != "" {
		l4 = append(l4, dimStyle.Render(b.Branch))
	}

	lines := []string{l1, "   " + strings.Join(l2, sep)}
	if f := facts(b); len(f) > 0 {
		lines = append(lines, "   "+strings.Join(f, sep))
	}
	lines = append(lines, "   "+strings.Join(l4, sep))

	for i, l := range lines {
		l = fit(l, railWidth)
		if selected {
			l = selectedBg.Render(l)
		}
		lines[i] = l
	}
	return lines
}

// groupOrder is the attention order of the rail's headers.
var groupOrder = []string{"NEEDS YOU", "HELD", "ACTIVE", "DONE"}

// railLines lays out every card. In attention order (which SortRows has
// already applied to rows) a header opens each state group and a blank
// line closes it; in name order it is cards back to back with the state
// on each. cursor is the index in rows of the selected binding.
func railLines(rows []relay.BindingStatus, cursor int, attention bool, now time.Time) []railLine {
	var out []railLine
	card := func(i int) {
		for _, l := range cardLines(rows[i], i == cursor, !attention, now) {
			out = append(out, railLine{text: l, binding: i})
		}
	}
	if !attention {
		for i := range rows {
			card(i)
		}
		return out
	}
	for _, state := range groupOrder {
		n := 0
		for _, r := range rows {
			if r.Display == state {
				n++
			}
		}
		if n == 0 {
			continue
		}
		header := " " + stateStyle(state).Render(state) + faintStyle.Render(fmt.Sprintf("  %d", n))
		out = append(out, railLine{text: fit(header, railWidth), binding: -1})
		for i, r := range rows {
			if r.Display == state {
				card(i)
			}
		}
		out = append(out, railLine{text: fit("", railWidth), binding: -1})
	}
	// A state outside groupOrder (none today) would vanish; append it so
	// nothing is ever hidden.
	for i, r := range rows {
		known := false
		for _, s := range groupOrder {
			if r.Display == s {
				known = true
			}
		}
		if !known {
			card(i)
		}
	}
	return out
}

// railSpan is the first and last line index of binding's card; (0, 0)
// when it has none.
func railSpan(lines []railLine, binding int) (first, last int) {
	first, last = -1, -1
	for i, l := range lines {
		if l.binding == binding {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0, 0
	}
	return first, last
}

// railWindow returns the first rail line to draw so that lines
// [first, last] -- the selected card -- are all visible in rows rows,
// moving top as little as possible. listWindow's five rules with the
// cursor widened to a span; rows <= 0 means no limit. A span taller than
// rows pins top to first: the name line wins.
func railWindow(top, first, last, rows, n int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	if top > n-rows {
		top = n - rows
	}
	if top < 0 {
		top = 0
	}
	if last >= top+rows {
		top = last - rows + 1
	}
	if first < top {
		top = first
	}
	return top
}

// railView draws bodyRows() rail lines at width, windowed on the cursor's
// card, or the three prose states when there is nothing to draw.
func (m Model) railView(width int) string {
	rows := m.bodyRows()
	var lines []string
	switch {
	case !m.statusLoaded && m.err != nil:
		lines = []string{"cannot reach herdr — see the error above"}
	case !m.statusLoaded:
		lines = []string{"loading…"}
	case len(m.rows()) == 0:
		lines = []string{"no bindings"}
	default:
		all := railLines(m.rows(), m.list.cursor, m.sort, m.now())
		first, last := railSpan(all, m.list.cursor)
		start := railWindow(m.list.top, first, last, rows, len(all))
		end := len(all)
		if rows > 0 && start+rows < end {
			end = start + rows
		}
		for _, l := range all[start:end] {
			lines = append(lines, l.text)
		}
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	if rows > 0 && len(lines) > rows {
		lines = lines[:rows]
	}
	for i := range lines {
		lines[i] = fit(lines[i], width)
	}
	return strings.Join(lines, "\n")
}
```

**`list.top` now indexes rail lines, not bindings.** Every place that
computes `m.list.top = listWindow(...)` (`keys.go` up/down, `model.go`
`WindowSizeMsg` and `statusMsg`) becomes:

```go
m.list.top = m.railTop()
```

with, in `rail.go`:

```go
// railTop re-windows the rail on the cursor's card. Called wherever the
// cursor, the rows or the row budget changed.
func (m Model) railTop() int {
	all := railLines(m.rows(), m.list.cursor, m.sort, m.now())
	first, last := railSpan(all, m.list.cursor)
	return railWindow(m.list.top, first, last, m.bodyRows(), len(all))
}
```

- [ ] **Step 6: Trim `list.go`.** Delete `renderBorder`, `styleDisplay`,
`renderListRow` and `listRows`. Keep `listModel`, `resolveSticky`,
`renderError`, `wrapLine`, `maxErrorLines`, `errorRows`. Replace
`listWindow` with a one-line wrapper so its tests stay green:

```go
// listWindow is railWindow for a one-line cursor; kept for its tests.
func listWindow(top, cursor, rows, n int) int {
	return railWindow(top, cursor, cursor, rows, n)
}
```

`listView` (the stack layout's list screen) becomes:

```go
func (m Model) listView() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	if m.err != nil {
		b.WriteString(renderError(m.err, m.width))
		b.WriteByte('\n')
	}
	b.WriteString(m.railView(m.width))
	b.WriteByte('\n')
	b.WriteString(m.footerView())
	return b.String()
}
```

`headerView` and `footerView` are Task 5's. For this task add
**temporary** versions in `list.go` that reproduce today's text so the
existing footer tests keep passing:

```go
func (m Model) headerView() string { return fit(headerBar.Render(" relay "), m.width) + "\n" }
func (m Model) footerView() string { return dimStyle.Render(m.footer()) }
```

(`headerView` returns two lines -- bar and blank -- to honour
`headerRows = 2`.) Remove the `cursorStyle`, `headerStyle`, `footerStyle`
aliases from `styles.go` once nothing references them; `detail.go` still
does until Task 4, so leave `headerStyle`/`footerStyle` for now and remove
`cursorStyle`.

- [ ] **Step 7:** `go test ./internal/ui -run 'TestCardLines|TestRailLines|TestRailWindow|TestListWindow'`
-- PASS.

- [ ] **Step 8: Update the tests that pinned the old list rendering.**
Each change is the minimum that re-points the assertion at the new
drawing; the behaviour under test is unchanged.

- `TestListRowsRenderFixedGoldenWidth` and `TestRenderListRowShowsHoldClock`
  tested `renderListRow`, which is gone. Delete both; `TestCardLinesShapes`
  covers the same fixtures (the held-clock case is its `held` row).
- `TestRenderBorderWidthWithBullet`, `TestRenderBorderOverlongTruncatesAndClose`:
  `renderBorder` is gone. Delete both.
- `assertCursorVisible`: the cursor needle becomes the selected card's
  name line -- `strings.Contains(plain(view), "▎ "+name)` (`plain`
  collapses spaces); the header
  needle becomes `"relay"`; the footer needle stays `"open"` (Task 5
  changes the footer text to `⏎ open`; `"open"` matches both). Keep the
  newline-count assertion.
- `TestListRowsBudget`: `listRows` is gone; assert `m.bodyRows()` instead.
  Its arithmetic changes: header is now 2 rows, so at height 24 with no
  error the budget is `24 - 2 - 1 = 21`, not 22. Update the expected
  numbers to `height - 3 - errLines`.
- `TestListViewKeepsCursorVisible*`, `TestListViewRewindows*`: these press
  `j`/`k` through ten one-line bindings with a 6-row height. Cards are now
  three lines plus headers, so **raise the fixture height** in
  `tenBindings` to `40` and keep the assertions; the point of the tests
  (the cursor's card is on screen after every move and after a resize)
  holds regardless of card height. If a test's expected `top` value is
  asserted numerically, recompute it with `railWindow` in the test rather
  than hard-coding.
- `TestListScreenThreeStates`: the four prose needles (`loading…`, `cannot
  reach herdr — see the error above`, `no bindings`, `webshop`) are
  unchanged. Passes as is.
- `TestEmptyBindingsList`, `TestQuitFromList`, `TestCursor*`: no rendering
  needles; pass as is. If one fails, stop and report.

- [ ] **Step 9:** `go test ./internal/ui` -- PASS. `go vet ./...` clean
(unused symbols in `detail.go` are not vet errors; unused *imports* are --
remove any `fmt`/`lipgloss` import `list.go` no longer needs).

- [ ] **Step 10:** `make check` constituents green. Commit:

```
feat(ui): rail cards grouped by state, windowed on the cursor's card (#180)
```

---

### Task 4: the pane

**Files:**
- Create: `internal/ui/pane.go`, `internal/ui/pane_test.go`
- Modify: `internal/ui/detail.go` (`detailView` draws the pane full-width;
  `chromeHeight` removed)
- Modify: `internal/ui/model.go`, `internal/ui/keys.go` (viewport sizing
  uses `viewportHeight()` / `paneWidth()`)
- Modify: `internal/ui/detail_test.go`, `internal/ui/model_test.go`
  (`chromeHeight` arithmetic, tab-bar needles)

**Interfaces:**
- Consumes: Task 2 styles/geometry; Task 3 `fit`, `ago`, `sep`, `stripANSI`.
- Produces:
  - `func (m Model) paneHead(b *relay.BindingStatus) []string` -- exactly `paneHeadRows` lines plus one per foreign agent
  - `func (m Model) tabBar() []string` -- `tabRows` lines
  - `func (m Model) sourceLine() string`
  - `func diffStat(patch string) (files, added, removed int)`
  - `func colourDiff(patch string) string`
  - `func (m Model) hintLine(b *relay.BindingStatus) (string, bool)`
  - `func (m Model) paneView(width int) string` -- `bodyRows()` lines

- [ ] **Step 1: Write the failing pane tests.**

`internal/ui/pane_test.go`:

```go
package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func paneModel(t *testing.T, b relay.BindingStatus, active tab) Model {
	t.Helper()
	m := Model{width: 140, height: 40, ready: true, statusLoaded: true, sort: true,
		now: func() time.Time { return railNow }}
	m.report = relay.Report{Bindings: []relay.BindingStatus{b}}
	m.detail = detailModel{name: b.Name, round: b.Round - 1, active: active,
		vp: viewport.New(m.paneWidth(), m.viewportHeight())}
	return m
}

func TestPaneHeadRows(t *testing.T) {
	b := relay.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		PlannerPane: "%1", PlannerKind: "claude", PlannerStatus: "idle", PlannerFocus: true,
		BuilderPane: "%7", BuilderKind: "agy", BuilderStatus: "blocked", Consults: 2,
		Branch: "relay/webshop", Dirty: true,
		LastClose: &relay.CloseInfo{Round: 3, Commits: 2, Tree: "a1c9f0e1234567"},
		Last:      &relay.LastEvent{TS: railNow.Add(-2 * time.Minute), Round: 4, Kind: store.KindQuestion},
	}
	m := paneModel(t, b, tabReport)
	head := m.paneHead(&b)
	if len(head) != paneHeadRows {
		t.Fatalf("%d head rows, want %d:\n%s", len(head), paneHeadRows, strings.Join(head, "\n"))
	}
	want := []string{
		"webshop  round 4   NEEDS YOU ",
		"planner  %1   claude    idle · focused",
		"builder  %7   agy       blocked · 2 consults",
		"tree     relay/webshop · dirty · last close a1c9f0e (2 commits)",
	}
	for i, w := range want {
		if got := stripANSI(head[i]); !strings.HasPrefix(got, w) {
			t.Errorf("head[%d]:\n got %q\nwant prefix %q", i, got, w)
		}
	}
	if !strings.Contains(stripANSI(head[0]), "question r4 · 2m ago") {
		t.Errorf("title row lacks the last event: %q", stripANSI(head[0]))
	}
	b.Foreign = []relay.ForeignAgent{{PaneID: "%9", Kind: "claude", Status: "working", Title: "reviewer"}}
	if got := len(m.paneHead(&b)); got != paneHeadRows+1 {
		t.Errorf("with a foreign agent: %d rows, want %d", got, paneHeadRows+1)
	}
}

func TestPaneHeadHeadlessAndCwd(t *testing.T) {
	b := relay.BindingStatus{
		Name: "api", Round: 2, Display: "ACTIVE", CWD: "/home/x/api",
		BuilderPane: "headless", BuilderKind: "opencode", BuilderStatus: "working", BuilderCandidate: "opencode-1",
		Headless: &relay.HeadlessInfo{PID: 48211, StartedAt: railNow.Add(-21 * time.Minute)},
	}
	m := paneModel(t, b, tabReport)
	head := m.paneHead(&b)
	if got := stripANSI(head[2]); !strings.Contains(got, "pid 48211 since") || !strings.Contains(got, "`opencode-1`") {
		t.Errorf("builder row = %q", got)
	}
	if got := stripANSI(head[3]); !strings.HasPrefix(got, "tree     /home/x/api") {
		t.Errorf("--cwd tree row = %q", got)
	}
}

func TestTabBarMarksActive(t *testing.T) {
	m := paneModel(t, relay.BindingStatus{Name: "a", Round: 1, Display: "ACTIVE"}, tabDiff)
	bar := m.tabBar()
	if len(bar) != tabRows {
		t.Fatalf("%d tab rows", len(bar))
	}
	if got := stripANSI(bar[0]); !strings.Contains(got, " 1 report ") || !strings.Contains(got, " 3 diff ") {
		t.Errorf("tab bar = %q", got)
	}
	if !strings.Contains(bar[0], activeTabStyle.Render(" 3 diff ")) {
		t.Errorf("diff tab not styled active: %q", bar[0])
	}
	if !strings.HasPrefix(stripANSI(bar[1]), "───") {
		t.Errorf("rule row = %q", stripANSI(bar[1]))
	}
}

func TestDiffStatAndColour(t *testing.T) {
	patch := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n context\n-old\n+new\n+more\ndiff --git a/y.go b/y.go\n@@ -1 +1 @@\n-a\n+b\n"
	files, add, del := diffStat(patch)
	if files != 2 || add != 3 || del != 2 {
		t.Errorf("diffStat = %d files +%d -%d", files, add, del)
	}
	out := strings.Split(colourDiff(patch), "\n")
	if out[0] != diffFileStyle.Render("diff --git a/x.go b/x.go") {
		t.Errorf("file header not styled: %q", out[0])
	}
	if out[3] != diffHunkStyle.Render("@@ -1,2 +1,3 @@") {
		t.Errorf("hunk not styled: %q", out[3])
	}
	if out[5] != diffDelStyle.Render("-old") || out[6] != diffAddStyle.Render("+new") {
		t.Errorf("+/- not styled: %q %q", out[5], out[6])
	}
	if out[1] != diffFileStyle.Render("--- a/x.go") || out[2] != diffFileStyle.Render("+++ b/x.go") {
		t.Errorf("---/+++ must be file headers, not del/add: %q %q", out[1], out[2])
	}
	if out[4] != " context" {
		t.Errorf("context line altered: %q", out[4])
	}
}

func TestSourceLinePerTab(t *testing.T) {
	b := relay.BindingStatus{Name: "a", Round: 3, Display: "ACTIVE", BuilderPane: "%7"}
	m := paneModel(t, b, tabReport)
	m.detail.cache[tabReport] = tabContent{loaded: true, body: "x", round: 2, at: railNow.Add(-time.Hour)}
	if got := stripANSI(m.sourceLine()); got != "report r2 · 13:02" {
		t.Errorf("report source = %q", got)
	}
	m.detail.active = tabTerminal
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: "l1\nl2\nl3", at: railNow.Add(-time.Second)}
	if got := stripANSI(m.sourceLine()); got != "%7 · captured 1s ago · 3 lines" {
		t.Errorf("terminal source = %q", got)
	}
	m.detail.active = tabDiff
	m.detail.cache[tabDiff] = tabContent{loaded: true, body: "diff --git a/x b/x\n+a\n-b\n"}
	if got := stripANSI(m.sourceLine()); got != "round 2 · 1 file · +1 −1" {
		t.Errorf("diff source = %q", got)
	}
	m.detail.active = tabLog
	m.detail.cache[tabLog] = tabContent{loaded: true, body: "e1\ne2"}
	if got := stripANSI(m.sourceLine()); got != "2 entries" {
		t.Errorf("log source = %q", got)
	}
	m.detail.active = tabReport
	m.detail.cache[tabReport] = tabContent{}
	if got := stripANSI(m.sourceLine()); got != "loading…" {
		t.Errorf("unloaded source = %q", got)
	}
}

func TestHintLineOnlyForBlockedTerminal(t *testing.T) {
	b := relay.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU",
		Waiting: &relay.Waiting{Cause: "blocked", Hint: "relay answer --name webshop"}}
	m := paneModel(t, b, tabTerminal)
	line, ok := m.hintLine(&b)
	if !ok || stripANSI(line) != "relay: relay answer --name webshop" {
		t.Errorf("hint = %q ok=%v", stripANSI(line), ok)
	}
	m.detail.active = tabReport
	if _, ok := m.hintLine(&b); ok {
		t.Error("hint must only show on the terminal tab")
	}
	m.detail.active = tabTerminal
	b.Waiting.Cause = "halted"
	if _, ok := m.hintLine(&b); ok {
		t.Error("hint must only show for a blocked builder")
	}
	b.Waiting = nil
	if _, ok := m.hintLine(&b); ok {
		t.Error("hint must not show without Waiting")
	}
}

func TestPaneViewRowsAndWidth(t *testing.T) {
	b := relay.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderPane: "%7",
		Waiting: &relay.Waiting{Cause: "blocked", Hint: "relay answer --name webshop"}}
	m := paneModel(t, b, tabTerminal)
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: strings.Repeat("screen line\n", 50)}
	m.detail.vp.SetContent(bodyOf(m.detail.cache[tabTerminal]))
	view := m.paneView(m.paneWidth())
	lines := strings.Split(view, "\n")
	if len(lines) != m.bodyRows() {
		t.Fatalf("%d pane rows, want bodyRows %d", len(lines), m.bodyRows())
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > m.paneWidth() {
			t.Errorf("row %d is %d wide, pane is %d: %q", i, w, m.paneWidth(), stripANSI(l))
		}
	}
	if got := stripANSI(lines[len(lines)-1]); !strings.HasPrefix(got, "relay: relay answer") {
		t.Errorf("last pane row must be the hint, got %q", got)
	}
}
```

- [ ] **Step 2: `tabContent` gains two fields.** The source line needs
what the fetch knew. In `fetch.go` -- **the one exception to "fetch.go is
unchanged", limited to these two fields and the lines that set them** --
add to `tabContent`:

```go
	round int       // the round the body belongs to (report, diff); 0 when not round-keyed
	at    time.Time // when the body was read; the source line's "13:38" and "captured 1s ago"
```

and set `at: time.Now()` in every place a `tabContent` is constructed
with `loaded: true`, and `round:` where the fetch already knows it
(`fetchReport` reports the round of the entry it read -- the same value it
puts in `tabMsg.round`; `fetchDiff` has its `round` argument). Nothing
else in `fetch.go` changes. Say in the report that this exception was
taken.

- [ ] **Step 3:** `go test ./internal/ui -run 'TestPane|TestTabBar|TestDiffStat|TestSourceLine|TestHintLine'`
-- FAIL: undefined.

- [ ] **Step 4: Write `pane.go`.**

```go
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
)

// paneHead is the pane's first rows: title with the state pill and the
// last event, planner, builder, tree, blank -- plus one `foreign` row per
// foreign agent, which is why the caller measures it rather than
// assuming paneHeadRows. Each row is unpadded; paneView fits them.
func (m Model) paneHead(b *relay.BindingStatus) []string {
	label := func(s string) string { return dimStyle.Render(fmt.Sprintf("%-9s", s)) }
	if b == nil {
		return []string{"", "", "", "", ""}
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render(b.Name) +
		dimStyle.Render(fmt.Sprintf("  round %d  ", b.Round)) + pillStyle(b.Display).Render(b.Display)
	if b.Last != nil {
		right := dimStyle.Render(fmt.Sprintf("%s r%d · %s ago", b.Last.Kind, b.Last.Round, ago(b.Last.TS, m.now())))
		title = spread(title, right, m.paneWidth())
	}

	planner := label("planner") + fmt.Sprintf("%-4s %-9s %s", b.PlannerPane, b.PlannerKind, b.PlannerStatus)
	if b.PlannerFocus {
		planner += sep + dimStyle.Render("focused")
	}

	var bparts []string
	if b.Headless != nil {
		bparts = append(bparts, stateStyle(b.Display).Render(b.BuilderStatus))
		if b.Headless.PID != 0 {
			bparts = append(bparts, dimStyle.Render(fmt.Sprintf("pid %d since %s", b.Headless.PID, b.Headless.StartedAt.Local().Format("15:04"))))
		}
		bparts = append(bparts, fgStyle.Render("`"+b.BuilderCandidate+"`"))
	} else {
		bparts = append(bparts, builderStatusStyle(b.BuilderStatus).Render(b.BuilderStatus))
	}
	if b.Consults > 0 {
		bparts = append(bparts, dimStyle.Render(fmt.Sprintf("%d consults", b.Consults)))
	}
	if b.Switches > 0 {
		bparts = append(bparts, dimStyle.Render(fmt.Sprintf("switched %dx", b.Switches)))
	}
	builder := label("builder") + fmt.Sprintf("%-4s %-9s ", b.BuilderPane, b.BuilderKind) + strings.Join(bparts, sep)

	rows := []string{title, planner, builder}
	for _, fa := range b.Foreign {
		rows = append(rows, label("foreign")+fmt.Sprintf("%-4s %-9s %s"+sep+"%s", fa.PaneID, fa.Kind, fa.Status, fa.Title))
	}

	var tparts []string
	if b.Branch != "" {
		tparts = append(tparts, fgStyle.Render(b.Branch))
	} else {
		tparts = append(tparts, fgStyle.Render(b.CWD))
	}
	if b.Dirty {
		tparts = append(tparts, stateNeedsYouStyle.Render("dirty"))
	}
	if b.LastClose != nil {
		tree := b.LastClose.Tree
		if len(tree) > 7 {
			tree = tree[:7]
		}
		tparts = append(tparts, dimStyle.Render(fmt.Sprintf("last close %s (%d commits)", tree, b.LastClose.Commits)))
	}
	rows = append(rows, label("tree")+strings.Join(tparts, sep), "")
	return rows
}

// builderStatusStyle: blocked is the one status a human must notice.
func builderStatusStyle(status string) lipgloss.Style {
	if status == "blocked" {
		return stateNeedsYouStyle
	}
	return fgStyle
}

// spread puts right at the right edge of a width-wide line, after left,
// dropping the gap when they would overlap (left wins; the caller decides
// what is important enough for the right).
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// tabBar is the numbered tab row and the rule under it.
func (m Model) tabBar() []string {
	var parts []string
	for i, t := range tabTitles {
		label := fmt.Sprintf(" %d %s ", i+1, t)
		if tab(i) == m.detail.active {
			parts = append(parts, activeTabStyle.Render(label))
		} else {
			parts = append(parts, inactiveTabStyle.Render(label))
		}
	}
	return []string{strings.Join(parts, " "), ruleStyle.Render(strings.Repeat("─", m.paneWidth()))}
}

// diffStat counts a patch the way `git diff --stat` would summarise it:
// files by `diff --git` headers, added and removed by leading +/- that
// are not the ---/+++ file markers.
func diffStat(patch string) (files, added, removed int) {
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			files++
		case strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}

// colourDiff styles a patch line by line: file headers bold, hunks blue,
// additions green, removals red, everything else untouched. It never
// parses further than the first characters of a line.
func colourDiff(patch string) string {
	lines := strings.Split(patch, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff --git "), strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "):
			lines[i] = diffFileStyle.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = diffHunkStyle.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = diffAddStyle.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = diffDelStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// sourceLine says, in one faint line, what the viewport is showing.
func (m Model) sourceLine() string {
	c := m.detail.cache[m.detail.active]
	if !c.loaded {
		return faintStyle.Render("loading…")
	}
	var s string
	switch m.detail.active {
	case tabReport:
		s = fmt.Sprintf("report r%d · %s", c.round, c.at.Local().Format("15:04"))
	case tabTerminal:
		pane := ""
		if r := row(m.report, m.detail.name); r != nil {
			pane = r.BuilderPane
		}
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		s = fmt.Sprintf("%s · captured %s ago · %d lines", pane, ago(c.at, m.now()), n)
	case tabDiff:
		files, add, del := diffStat(c.body)
		unit := "files"
		if files == 1 {
			unit = "file"
		}
		s = fmt.Sprintf("round %d · %d %s · +%d −%d", m.detail.round, files, unit, add, del)
	case tabLog:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		s = fmt.Sprintf("%d entries", n)
	}
	if c.err != nil || c.empty != "" {
		// The viewport carries the prose; the source line says only where
		// it looked.
		switch m.detail.active {
		case tabReport:
			s = "report"
		case tabDiff:
			s = fmt.Sprintf("round %d", m.detail.round)
		case tabLog:
			s = "log"
		}
	}
	return faintStyle.Render(s)
}

// hintLine is the one rendered line under a blocked builder's dialog on
// the terminal tab: the verb that resolves it (spec §3.4). The ui runs
// nothing; it names the command.
func (m Model) hintLine(b *relay.BindingStatus) (string, bool) {
	if b == nil || b.Waiting == nil || b.Waiting.Cause != "blocked" || m.detail.active != tabTerminal {
		return "", false
	}
	return accentStyle.Render("relay: ") + fgStyle.Render(b.Waiting.Hint), true
}

// paneView draws exactly bodyRows() rows at width: head, tabs, source,
// blank, viewport, with the hint replacing the last viewport row when it
// applies. The viewport is resized to what is left after a head that
// grew by foreign rows.
func (m Model) paneView(width int) string {
	b := row(m.report, m.detail.name)
	rows := []string{}
	rows = append(rows, m.paneHead(b)...)
	rows = append(rows, m.tabBar()...)
	rows = append(rows, m.sourceLine(), "")
	budget := m.bodyRows() - len(rows)
	if budget < 0 {
		budget = 0
	}
	hint, hasHint := m.hintLine(b)
	vpRows := budget
	if hasHint && vpRows > 0 {
		vpRows--
	}
	vp := m.detail.vp
	vp.Width = width
	vp.Height = vpRows
	if vpRows > 0 {
		rows = append(rows, strings.Split(vp.View(), "\n")...)
	}
	if hasHint && budget > 0 {
		rows = append(rows, hint)
	}
	for len(rows) < m.bodyRows() {
		rows = append(rows, "")
	}
	rows = rows[:m.bodyRows()]
	for i := range rows {
		rows[i] = fit(rows[i], width)
	}
	return strings.Join(rows, "\n")
}
```

`bodyOf` in `detail.go` gains the diff colouring: when the content is a
loaded, non-empty, error-free diff tab body, return `colourDiff(c.body)`.
`bodyOf` does not know the tab today; change its signature to
`bodyOf(t tab, c tabContent) string` and update its four callers
(`keys.go` `enter` and `switchTab`, `model.go` `tabMsg`, tests). Only
`tabDiff` colours; the other three return `c.body` as before.

- [ ] **Step 5: `detail.go`.** Delete `chromeHeight` and the old
`detailView` body. `detailView` (the stack layout's detail screen) becomes:

```go
func (m Model) detailView() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	b.WriteString(m.paneView(m.width))
	b.WriteByte('\n')
	b.WriteString(m.footerView())
	return b.String()
}
```

Every `m.height - chromeHeight` in `model.go` (`WindowSizeMsg`) and
`keys.go` (`enter`) becomes `m.viewportHeight()`, and every
`viewport.New(m.width, ...)` becomes `viewport.New(m.paneWidth(),
m.viewportHeight())`; `WindowSizeMsg` sets `m.detail.vp.Width =
m.paneWidth()`. The `lines` passed to `fetchFor`/`fetchTerminal` stays
`m.detail.vp.Height` (floored at 1) as today.

Remove the `headerStyle`/`footerStyle` aliases from `styles.go`; nothing
references them now.

- [ ] **Step 6:** `go test ./internal/ui -run 'TestPane|TestTabBar|TestDiffStat|TestSourceLine|TestHintLine'`
-- PASS.

- [ ] **Step 7: Update the tests that pinned `chromeHeight` and the old
tab bar.** In `detail_test.go` and `model_test.go`, `60-chromeHeight` /
`40-chromeHeight` become `m.viewportHeight()` computed on the model under
test (build the model, resize, then compare `m.detail.vp.Height` to
`m.viewportHeight()`). Any needle on `"[report]"` or the old
`activeTabStyle.Render("[" + t + "]")` becomes
`activeTabStyle.Render(" 1 report ")` (and `" 2 terminal "` etc.). A
needle on the old title format `name · round N · STATE` becomes a
`stripANSI` prefix check on `name  round N` -- the state is now a pill
after it. `TestRenderErrorAndListErrorBlock` step 5 builds
`viewport.New(80, 20)` by hand and asserts the footer marker; it passes
unchanged.

- [ ] **Step 8:** `go test ./internal/ui` -- PASS. `go vet ./...` clean.

- [ ] **Step 9:** `make check` constituents green. Commit:

```
feat(ui): pane with head, tab bar, source line, coloured diff and the answer hint (#180)
```

---

### Task 5: split layout, pane follows cursor, header, footer, keys

**Files:**
- Modify: `internal/ui/model.go` (`View`, `WindowSizeMsg`, `statusMsg`,
  `pointDetailAt`, `statusAt`, `headerView`, `footerView`)
- Modify: `internal/ui/keys.go` (split-mode keys, `s`)
- Modify: `internal/ui/list.go` (remove the temporary `headerView`/`footerView`)
- Create: `internal/ui/split_test.go`
- Modify: `internal/ui/list_test.go`, `internal/ui/invalidation_test.go`
  (footer needles, per Step 7)

**Interfaces:**
- Consumes: everything above.
- Produces:
  - `func (m Model) pointDetailAt(name string) (Model, tea.Cmd)`
  - `func (m Model) headerView() string` -- `headerRows` lines
  - `func (m Model) footerView() string` -- one line
  - `func (m Model) splitView() string`
  - `Model.statusAt time.Time`

- [ ] **Step 1: Write the failing split tests.**

`internal/ui/split_test.go`:

```go
package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func splitModel(t *testing.T, width, height int, rows ...relay.BindingStatus) Model {
	t.Helper()
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	m := newModel(context.Background(), relay.Runtime{Store: st, Herdr: fh}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: relay.Report{Bindings: rows}})
	return res.(Model)
}

func threeRows() []relay.BindingStatus {
	return []relay.BindingStatus{
		{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working", Last: &relay.LastEvent{TS: railNow.Add(-6 * time.Minute)}},
		{Name: "docs", Round: 1, Display: "DONE", BuilderKind: "agy", Last: &relay.LastEvent{TS: railNow.Add(-time.Hour)}},
		{Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderKind: "agy", BuilderStatus: "blocked", Last: &relay.LastEvent{TS: railNow.Add(-2 * time.Minute)}},
	}
}

func TestSplitViewShape(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 40 {
		t.Fatalf("%d lines at height 40", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 140 {
			t.Errorf("line %d is %d wide: %q", i, w, stripANSI(l))
		}
	}
	p := plain(view)
	if !strings.Contains(p, "relay 3 bindings · 1 needs you") {
		t.Errorf("header missing counts:\n%s", p)
	}
	// Attention order: webshop first and selected, pane shows it.
	if idx := strings.Index(p, "▎ webshop"); idx < 0 || idx > strings.Index(p, " api ") {
		t.Errorf("webshop must be the selected, first card:\n%s", p)
	}
	if !strings.Contains(p, "webshop round 4") {
		t.Errorf("pane must show the cursor's binding:\n%s", p)
	}
	if !strings.Contains(p, "⏎ focus pane") || !strings.Contains(p, "s sort: attention") {
		t.Errorf("split footer keys missing:\n%s", p)
	}
	if m.detail.name != "webshop" || m.detail.round != 3 {
		t.Errorf("detail not pointed at the cursor: %+v", m.detail)
	}
}

func TestPaneFollowsCursor(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	// The statusMsg pointed the pane at webshop and issued its fetch.
	if !m.tabInFlight {
		t.Fatal("initial point must fetch")
	}
	m.tabInFlight = false
	m.detail.active = tabDiff
	m.detail.scroll[tabDiff] = 7
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	if m.detail.name != "api" {
		t.Errorf("after j the pane shows %q, want api", m.detail.name)
	}
	if cmd == nil || !m.tabInFlight {
		t.Error("moving the cursor must issue exactly one fetch for the new binding")
	}
	if m.detail.active != tabDiff {
		t.Error("the active tab must survive the move")
	}
	if m.detail.scroll[tabDiff] != 0 || m.detail.cache[tabDiff].loaded {
		t.Error("parked scrolls and caches must be cleared")
	}
	// A stale reply for webshop is discarded.
	res, _ = m.Update(tabMsg{name: "webshop", round: 3, t: tabDiff, content: tabContent{loaded: true, body: "old"}})
	m = res.(Model)
	if m.detail.cache[tabDiff].loaded {
		t.Error("stale tabMsg for the previous binding must be discarded")
	}
	// With a fetch in flight, a second move issues none.
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	if cmd != nil {
		t.Error("a move while tabInFlight must not issue a second fetch")
	}
	if m.detail.name != "docs" {
		t.Errorf("pane must still re-point: %q", m.detail.name)
	}
}

func TestResizeAcrossThreshold(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	if m.screen != screenDetail {
		t.Fatal("enter in split focuses the pane")
	}
	res, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = res.(Model)
	if m.layout() != layoutStack || m.screen != screenDetail || m.detail.name != "webshop" {
		t.Errorf("narrowing with the pane focused must land on the full-width detail of the same binding: layout=%v screen=%v name=%q", m.layout(), m.screen, m.detail.name)
	}
	if m.detail.vp.Width != 100 {
		t.Errorf("viewport width after narrowing = %d", m.detail.vp.Width)
	}
	// Back to the list, then widen: the pane must point at the cursor.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = res.(Model)
	m.tabInFlight = false
	m.detail = detailModel{}
	res, cmd := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	if m.detail.name != "webshop" || cmd == nil {
		t.Errorf("widening from the list must point the pane at the cursor and fetch: %+v", m.detail)
	}
}

func TestSortToggleKeepsSelection(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	if m.rows()[m.list.cursor].Name != "api" {
		t.Fatalf("cursor on %q", m.rows()[m.list.cursor].Name)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = res.(Model)
	if m.sort {
		t.Error("s must switch to name order")
	}
	if got := m.rows()[m.list.cursor].Name; got != "api" {
		t.Errorf("cursor moved to %q on re-sort", got)
	}
	if m.rows()[0].Name != "api" || m.rows()[2].Name != "webshop" {
		t.Errorf("name order = %v", m.rows())
	}
	if !strings.Contains(stripANSI(m.View()), "s sort: name") {
		t.Error("footer must show the current order")
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !res.(Model).sort {
		t.Error("s twice is identity")
	}
}

func TestFooterNoticesAndRefreshAge(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // pane on api
	m = res.(Model)
	m.now = func() time.Time { return railNow.Add(2 * time.Second) }
	f := stripANSI(m.footerView())
	if !strings.Contains(f, "webshop NEEDS YOU") {
		t.Errorf("another binding at NEEDS YOU must be a footer notice: %q", f)
	}
	if !strings.HasSuffix(strings.TrimRight(f, " "), "refreshed 2s ago") {
		t.Errorf("footer must end with the refresh age: %q", f)
	}
	// The right side wins when they would overlap.
	m.width = 60
	f = stripANSI(m.footerView())
	if lipgloss.Width(f) > 60 || !strings.Contains(f, "NEEDS YOU") {
		t.Errorf("at 60 columns the notice must survive and the line must fit: %q", f)
	}
}

func TestHeaderGatesAndClock(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.report.Gated = []ledger.Gate{{Token: "codex", Kind: ledger.RateLimited, Since: railNow, Until: railNow.Add(88 * time.Minute)}}
	h := stripANSI(m.headerView())
	if !strings.Contains(h, "codex gated until 15:30") || !strings.Contains(h, "14:02") {
		t.Errorf("header = %q", h)
	}
	if strings.Count(h, "\n") != headerRows-1 {
		t.Errorf("header must be %d rows: %q", headerRows, h)
	}
}
```

`ledger.Gate` is `{Token, Kind, Since, Until, Note, Source, Binding}` and
the limit kind is `ledger.RateLimited` (`internal/ledger/ledger.go`).
`railNow` is already in the local zone (Task 3), so `14:02` and `15:30`
render the same on every machine.

- [ ] **Step 2:** `go test ./internal/ui -run 'TestSplit|TestPaneFollows|TestResize|TestSortToggle|TestFooterNotices|TestHeaderGates'`
-- FAIL.

- [ ] **Step 3: `model.go`.**

Add `statusAt time.Time` to `Model`; set `m.statusAt = m.now()` on a good
`statusMsg`.

`pointDetailAt`:

```go
// pointDetailAt re-targets the pane at the binding named: name, round
// (row.Round - 1), lastLogTS from row.Last, every cache cleared, every
// parked scroll zeroed. The active tab is kept -- a human reading diffs
// across bindings stays on diff. It issues the visible-tab fetch only if
// tabInFlight is clear; a fetch already in flight for the previous
// binding is discarded on arrival by tabMsg's name check, which exists
// for exactly this. A no-op when the pane already shows name.
func (m Model) pointDetailAt(name string) (Model, tea.Cmd) {
	if m.detail.name == name {
		return m, nil
	}
	r := row(m.report, name)
	if r == nil {
		return m, nil
	}
	vp := viewport.New(m.paneWidth(), m.viewportHeight())
	vp.SetContent(bodyOf(m.detail.active, tabContent{}))
	m.detail = detailModel{
		name:   name,
		round:  r.Round - 1,
		active: m.detail.active,
		vp:     vp,
	}
	if r.Last != nil {
		m.detail.lastLogTS = r.Last.TS
	}
	if m.tabInFlight {
		return m, nil
	}
	if cmd := m.visibleTabFetch(); cmd != nil {
		m.tabInFlight = true
		return m, cmd
	}
	return m, nil
}
```

`visibleTabFetch` today returns nil unless `m.screen == screenDetail`.
Change that guard to `!m.paneVisible()` with:

```go
// paneVisible: the pane is on screen in split layout always, and in
// stack layout only on the detail screen. visibleTabFetch and the tick
// both defer to it (old spec §6 rule 2).
func (m Model) paneVisible() bool {
	return m.layout() == layoutSplit || m.screen == screenDetail
}
```

`maybeInvalidate` likewise guards on `paneVisible()` instead of the
screen, and its "binding is gone" branch pops focus to the rail
(`m.screen = screenList`) and, in split layout, re-points at the cursor's
binding after `resolveSticky` (call `pointDetailAt(m.rows()[cursor].Name)`
when there are rows).

`WindowSizeMsg`:

```go
	case tea.WindowSizeMsg:
		m.width, m.height, m.ready = msg.Width, msg.Height, true
		m.detail.vp.Width = m.paneWidth()
		m.detail.vp.Height = m.viewportHeight()
		m.list.top = m.railTop()
		if m.layout() == layoutSplit && m.statusLoaded && len(m.rows()) > 0 {
			return m.pointDetailAt(m.rows()[m.list.cursor].Name)
		}
		return m, nil
```

`statusMsg` (good path), after `resolveSticky` and `m.list.top =
m.railTop()`: in split layout call `pointDetailAt` for the cursor's
binding when `m.detail.name` differs (covers the first load and a
vanished selection), then `maybeInvalidate` as today. Batch the two
commands if both return one.

`View`:

```go
func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	if m.layout() == layoutSplit {
		return m.splitView()
	}
	switch m.screen {
	case screenDetail:
		return m.detailView()
	default:
		return m.listView()
	}
}

// splitView is the one screen: header, error block, rail │ pane, footer.
func (m Model) splitView() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	if m.err != nil {
		b.WriteString(renderError(m.err, m.width))
		b.WriteByte('\n')
	}
	rail := strings.Split(m.railView(railWidth), "\n")
	pane := strings.Split(m.paneView(m.paneWidth()), "\n")
	bar := ruleStyle.Render("│") + " "
	for i := 0; i < m.bodyRows(); i++ {
		r, p := "", ""
		if i < len(rail) {
			r = rail[i]
		}
		if i < len(pane) {
			p = pane[i]
		}
		b.WriteString(fit(r, railWidth) + bar + p)
		b.WriteByte('\n')
	}
	b.WriteString(m.footerView())
	return b.String()
}
```

When there is no binding to point at (`!m.statusLoaded`, or no rows),
`paneView` receives a nil row and draws its five blank head rows, the tab
bar and an empty viewport -- `paneHead(nil)` already returns blanks; make
`sourceLine` return `""` when `m.detail.name == ""`.

`headerView` (replaces the temporary one in `list.go`; delete that):

```go
// headerView is the reversed bar and the blank under it (headerRows).
func (m Model) headerView() string {
	left := lipgloss.NewStyle().Bold(true).Render(" relay ")
	switch {
	case !m.statusLoaded:
		left += "  "
	case len(m.report.Bindings) == 0:
		left += "  no bindings"
	default:
		counts := map[string]int{}
		for _, b := range m.report.Bindings {
			counts[b.Display]++
		}
		left += fmt.Sprintf("  %d bindings", len(m.report.Bindings))
		if n := counts["NEEDS YOU"]; n > 0 {
			left += " · " + stateNeedsYouStyle.Render(fmt.Sprintf("%d needs you", n))
		}
		if n := counts["HELD"]; n > 0 {
			left += fmt.Sprintf(" · %d held", n)
		}
	}
	var right []string
	for _, g := range m.report.Gated {
		right = append(right, stateNeedsYouStyle.Render(fmt.Sprintf("%s gated %s", g.Token, relay.GateUntilText(g.Until))))
	}
	right = append(right, dimStyle.Render(m.now().Local().Format("15:04")+" "))
	bar := headerBar.Render(fit(spread(left, strings.Join(right, "  ·  "), m.width), m.width))
	return bar + "\n"
}
```

(The bar is one styled line; the `"\n"` is the blank row. `View` writes
one more `\n` after it, which is the blank row's line break. Check the
newline count in `TestSplitViewShape` and adjust: the total must be
exactly `height - 1` newlines.)

`footerView`:

```go
// footerView: keys for the current focus on the left, notices and the
// refresh age on the right. The right side wins when they would overlap:
// a notice is the part a human must not miss.
func (m Model) footerView() string {
	key := func(k, v string) string { return fgStyle.Render(k) + " " + dimStyle.Render(v) }
	order := "attention"
	if !m.sort {
		order = "name"
	}
	var keys []string
	switch {
	case m.layout() == layoutSplit && m.screen == screenList:
		keys = []string{key("↑↓", "move"), key("⏎", "focus pane"), key("tab", "next pane"), key("1-4", "pane"), key("s", "sort: "+order), key("q", "quit")}
	case m.layout() == layoutSplit:
		keys = []string{key("↑↓", "scroll"), key("esc", "back to rail"), key("tab", "next pane"), key("1-4", "pane"), key("s", "sort: "+order), key("q", "quit")}
	case m.screen == screenList:
		keys = []string{key("↑↓", "move"), key("⏎", "open"), key("s", "sort: "+order), key("q", "quit")}
	default:
		keys = []string{key("esc", "back"), key("tab", "next pane"), key("1-4", "pane"), key("q", "quit")}
	}
	left := strings.Join(keys, "   ")

	var notes []string
	if m.notice != "" {
		notes = append(notes, stateNeedsYouStyle.Render(m.notice))
	}
	if m.err != nil {
		notes = append(notes, errorStyle.Render("! refresh failed (retrying)"))
	}
	if m.paneVisible() {
		for _, b := range m.report.Bindings {
			if b.Name != m.detail.name && b.Display == "NEEDS YOU" {
				notes = append(notes, stateNeedsYouStyle.Render(b.Name+" NEEDS YOU"))
			}
		}
	}
	if !m.statusAt.IsZero() {
		notes = append(notes, faintStyle.Render("refreshed "+ago(m.statusAt, m.now())+" ago"))
	}
	right := strings.Join(notes, "   ")
	if lipgloss.Width(left)+1+lipgloss.Width(right) > m.width {
		left = lipgloss.NewStyle().MaxWidth(m.width - lipgloss.Width(right) - 1).Render(left)
	}
	return fit(spread(left, right, m.width), m.width)
}
```

Delete the old `footer()` once its callers are gone; the tests that call
`m.footer()` directly (`invalidation_test.go`, `list_test.go`) switch to
`stripANSI(m.footerView())` with the same needles (`webshop is gone`,
`! refresh failed (retrying)`).

- [ ] **Step 4: `keys.go`.** Restructure `updateKeys` by focus:

```go
func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Keys that work from either focus.
	switch msg.String() {
	case "s":
		m.sort = !m.sort
		m.list.resolveSticky(relay.Report{Bindings: m.rows()})
		m.list.top = m.railTop()
		return m, nil
	case "1", "2", "3", "4":
		if m.paneVisible() {
			return m.switchTab(tab(msg.String()[0] - '1'))
		}
	}
	railFocused := m.screen == screenList
	if railFocused {
		switch msg.String() {
		case "up", "k":
			return m.moveCursor(-1)
		case "down", "j":
			return m.moveCursor(+1)
		case "enter":
			if len(m.rows()) == 0 {
				return m, nil
			}
			m.screen = screenDetail
			return m.pointDetailAt(m.rows()[m.list.cursor].Name)
		}
		if m.layout() == layoutSplit {
			if msg.Type == tea.KeyTab || msg.Type == tea.KeyShiftTab || msg.String() == "tab" || msg.String() == "shift+tab" || msg.String() == "back_tab" {
				return m.cycleTab(msg)
			}
		}
		return m, nil
	}
	// Pane focused (split) or detail screen (stack).
	if msg.Type == tea.KeyEsc || msg.Type == tea.KeyBackspace || msg.String() == "esc" || msg.String() == "backspace" {
		m.screen = screenList
		return m, nil
	}
	if msg.Type == tea.KeyTab || msg.Type == tea.KeyShiftTab || msg.String() == "tab" || msg.String() == "shift+tab" || msg.String() == "back_tab" {
		return m.cycleTab(msg)
	}
	var cmd tea.Cmd
	m.detail.vp, cmd = m.detail.vp.Update(msg)
	return m, cmd
}

// moveCursor moves the rail cursor by delta, clamped, re-windows, and in
// split layout points the pane at the new binding.
func (m Model) moveCursor(delta int) (tea.Model, tea.Cmd) {
	rows := m.rows()
	if len(rows) == 0 {
		return m, nil
	}
	c := m.list.cursor + delta
	if c < 0 {
		c = 0
	}
	if c > len(rows)-1 {
		c = len(rows) - 1
	}
	m.list.cursor = c
	m.list.sticky = rows[c].Name
	m.list.top = m.railTop()
	if m.layout() == layoutSplit {
		return m.pointDetailAt(rows[c].Name)
	}
	return m, nil
}

// cycleTab is tab / shift+tab.
func (m Model) cycleTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyShiftTab || msg.String() == "shift+tab" || msg.String() == "back_tab" {
		return m.switchTab((m.detail.active - 1 + tabCount) % tabCount)
	}
	return m.switchTab((m.detail.active + 1) % tabCount)
}
```

`switchTab` is unchanged except `bodyOf(next, c)`. In split layout
`enter` from the rail focuses the pane; `pointDetailAt` is a no-op there
(the pane already shows the cursor's binding) -- that is what makes one
`enter` handler serve both layouts.

- [ ] **Step 5:** `go test ./internal/ui -run 'TestSplit|TestPaneFollows|TestResize|TestSortToggle|TestFooterNotices|TestHeaderGates'`
-- PASS.

- [ ] **Step 6:** `go test ./internal/ui` -- the single-flight,
invalidation, stale-reply and sticky-cursor tests must all still pass.
Where one built its model at width 80 and pressed `enter` to reach the
detail screen, it is exercising the stack layout and needs no change.
Where one asserted `m.screen == screenDetail` after `enter` at a width
≥ 110, it now asserts the same thing (enter focuses the pane) -- no
change. If a test now fails because `pointDetailAt` fetched on
`statusMsg` in split layout (an extra `tabInFlight = true` the test did
not expect), the test was built at a split width without meaning to:
give it `width: 80` and say so in the report. Do not weaken the
assertion.

- [ ] **Step 7: Footer needles.** `TestRenderErrorAndListErrorBlock`
steps 4-6 and the two `webshop is gone` checks in
`invalidation_test.go` call `m.footer()`; change to
`stripANSI(m.footerView())`. `assertCursorVisible`'s footer needle
`"open"` matches `⏎ open` in stack layout (its fixtures are 80 wide).

- [ ] **Step 8: Mutation checks**, each a temporary edit reverted after:
  1. In `pointDetailAt`, remove the `if m.tabInFlight { return m, nil }`
     guard: `TestPaneFollowsCursor` ("a move while tabInFlight must not
     issue a second fetch") must fail.
  2. In `sort.go`, swap `"HELD"` and `"ACTIVE"` ranks:
     `TestSortRowsAttentionOrder` must fail.
  3. Set `splitMinWidth = 109`: `TestLayoutThreshold` must fail.
  4. In `railWindow`, drop the `if first < top` rule: `TestRailWindowSpan`
     ("span above: pull up to first") must fail.
  Record all four outcomes in the report.

- [ ] **Step 9:** `make check` constituents green. Commit:

```
feat(ui): split dashboard -- rail beside a pane that follows the cursor; s sorts (#180)
```

---

### Task 6: golden views and docs

**Files:**
- Create: `internal/ui/golden_test.go`, `internal/ui/testdata/*.golden`
- Modify: `README.md` (the `relay ui` paragraph)
- Modify: `docs/design.md` if it describes the ui's layout (search for
  `relay ui`); one sentence.

**Interfaces:** consumes everything above; produces nothing new.

- [ ] **Step 1: Golden fixtures.** In `golden_test.go`, one table of
named fixtures, each a `relay.Report` and a width/height:

| name | width×height | rows |
| --- | --- | --- |
| `split-all-states` | 140×40 | NEEDS YOU (blocked, dirty, consults), HELD (hold clock), ACTIVE (pane builder, nudged), ACTIVE (headless, pid), DONE (`--cwd`, no branch); gated report |
| `split-long-name` | 140×40 | one ACTIVE row whose name is 40 characters |
| `split-foreign` | 140×40 | one ACTIVE row with two foreign agents |
| `split-empty` | 140×40 | no bindings |
| `split-error-before-load` | 140×40 | `statusMsg{err}` before any good report |
| `stack-all-states` | 80×30 | the `split-all-states` rows |
| `stack-detail` | 80×30 | `split-all-states` rows, `enter` on the first |

For each: build the model with `splitModel` (Task 5's helper; move it to
`golden_test.go` or a shared `helpers_test.go`), feed a terminal body for
the terminal tab through a `tabMsg` so the pane is not `loading…`, take
`stripANSI(m.View())`, and compare to `testdata/<name>.golden`. Use an
`-update` flag (`flag.Bool("update", false, ...)`) to write goldens. Also
assert, for every fixture, that no line is wider than the width and the
line count equals the height.

- [ ] **Step 2:** `go test ./internal/ui -run TestGolden -update`, then
**read every golden file** and check it against spec §3 line by line:
header counts, group headers, card lines, pane head rows, tab bar, source
line, footer. Fix the renderer where the golden is wrong -- a golden
written from a wrong renderer pins the wrong thing. Say in the report what
you corrected.

- [ ] **Step 3:** `go test ./internal/ui -run TestGolden` (no `-update`)
-- PASS.

- [ ] **Step 4: README.** Find the `relay ui` paragraph and replace its
description of the two screens with: at 110 columns or more, a rail of
bindings grouped by state beside a pane showing the selected binding's
report, terminal, diff or log (`⏎` focuses the pane, `s` toggles attention
and name order); narrower terminals get the list-then-detail flow. Keep
the `--interval` sentence.

- [ ] **Step 5:** `make check` constituents green. Commit:

```
test(ui): golden views for both layouts; docs for the split dashboard (#180)
```

---

## Report

Write `NNN-report.md` in the drop directory. Say, in this order:

1. Which `make check` constituents ran and their result, verbatim tails.
2. The four mutation-check outcomes from Task 5 step 8.
3. Every existing test you changed, and the one-line reason from the
   step that told you to.
4. Whether the `fetch.go` exception (Task 4 step 2) was taken and what
   it touched.
5. Anything you corrected after reading the goldens (Task 6 step 2).
6. Anything you stopped on, with the step number.
