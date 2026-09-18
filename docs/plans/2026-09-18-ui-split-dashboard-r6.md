# `relay ui` split dashboard, round 6: compact means narrow (#180)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-17-relay-ui-split-design.md` §6.0, the
compact paragraph (amended 2026-09-18, round 6).
**Earlier rounds:** `docs/plans/2026-09-17-ui-split-dashboard.md`, `-r2` … `-r5`.
**Issue:** #180

Round 5's compact rail is one line per binding at the full rail width.
The human meant the other axis: compact collapses the rail to a fixed
narrow strip, the way herdr collapses its sidebar. One task.

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

- No new dependencies; `fetch.go` untouched; `Update` pure.
- `railCompact = 18` is a constant in `layout.go`; nothing else hard-codes it.
- Goldens must not change: no fixture is compact. If one does, stop.
- One commit.

---

### Task 1: the compact rail is `railCompact` columns wide

**Files:**
- Modify: `internal/ui/layout.go` (`railCompact`; `railWidth` honours compact),
  `internal/ui/rail.go` (`compactLine`), `internal/ui/keys.go` (`<`/`>` no-op when compact),
  `internal/ui/mouse.go` (no drag when compact)
- Test: `internal/ui/layout_test.go`, `internal/ui/rail_test.go`, `internal/ui/split_test.go`, `internal/ui/mouse_test.go`

**Interfaces:**
- Produces: `const railCompact = 18`; `func clipName(name string, width int) string` -- `name` when it fits, else the first `width-1` cells plus `…`.
- Changes: `railWidth()` returns `railCompact` when `m.compact` (in split layout); `compactLine` renders at `railCompact` and drops the `what · age` qualifier.

- [ ] **Step 1: Failing tests.**

`layout_test.go`:

```go
func TestCompactRailIsNarrow(t *testing.T) {
	m := Model{width: 140, height: 40, railCols: 50, compact: true}
	if m.railWidth() != railCompact {
		t.Errorf("compact rail = %d, want %d", m.railWidth(), railCompact)
	}
	if m.paneWidth() != 140-railCompact-railGap {
		t.Errorf("pane = %d", m.paneWidth())
	}
	if m.railWidthStored() != 50 {
		t.Error("compact must not touch the remembered cards width")
	}
	m.compact = false
	if m.railWidth() != 50 {
		t.Errorf("cards mode is back to the stored width, got %d", m.railWidth())
	}
}
```

`rail_test.go` -- replace `TestCompactLineShape`:

```go
func TestClipName(t *testing.T) {
	if got := clipName("webshop", 10); got != "webshop" {
		t.Errorf("fits: %q", got)
	}
	if got := clipName("spaceapi-ingest", 10); got != "spaceapi-…" || lipgloss.Width(got) != 10 {
		t.Errorf("clipped: %q (%d)", got, lipgloss.Width(got))
	}
	if got := clipName("ab", 2); got != "ab" {
		t.Errorf("exact fit: %q", got)
	}
}

func TestCompactLineShape(t *testing.T) {
	b := relay.BindingStatus{Name: "spaceapi-ingest", Round: 12, Display: "NEEDS YOU", BuilderKind: "agy",
		Waiting: &relay.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)}}
	l := compactLine(b, true, false, railNow, true, railCompact)
	if w := lipgloss.Width(l); w != railCompact {
		t.Errorf("width %d, want %d", w, railCompact)
	}
	p := plain(l)
	if !strings.HasPrefix(p, "▎ spaceapi-") || !strings.HasSuffix(p, "r12") || !strings.Contains(p, "…") {
		t.Errorf("compact = %q", p)
	}
	if strings.Contains(p, "question") || strings.Contains(p, "2m") {
		t.Errorf("compact carries no qualifier: %q", p)
	}
	short := plain(compactLine(relay.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE"}, false, false, railNow, true, railCompact))
	if short != "api r2" {
		t.Errorf("short name = %q", short)
	}
	// Name order: the state colours the name, since there is no header.
	styled := compactLine(b, false, true, railNow, true, railCompact)
	if !strings.Contains(styled, stateStyle("NEEDS YOU").Bold(true).Render(clipName("spaceapi-ingest", railCompact-1-2-1-3-1))) {
		t.Errorf("name order: the name must take the state colour: %q", styled)
	}
}
```

(The clip width in the last assertion is `railCompact` less gutter (1),
unread slot (2), a space, the widest round the line reserves (3: `r12`),
and the trailing pad (1) -- the same arithmetic `compactLine` uses; if you
change how the round is reserved, change both.) Force the colour profile
in this test the way `TestCardGutterDimsWhenRailUnfocused` does.

`split_test.go`:

```go
func TestCompactIgnoresResize(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = res.(Model)
	before := m.railCols
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'>'}})
	m = res.(Model)
	if m.railCols != before || m.railWidth() != railCompact {
		t.Errorf("> while compact: cols %d width %d", m.railCols, m.railWidth())
	}
	view := m.View()
	for i, l := range strings.Split(view, "\n") {
		if w := lipgloss.Width(l); w > 140 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	if m.detail.vp.Width != 140-railCompact-railGap {
		t.Errorf("viewport must widen with the pane: %d", m.detail.vp.Width)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = res.(Model)
	if m.detail.vp.Width != m.paneWidth() || m.railWidth() != before {
		t.Errorf("back to cards: viewport %d pane %d rail %d", m.detail.vp.Width, m.paneWidth(), m.railWidth())
	}
}
```

`mouse_test.go`:

```go
func TestNoDragWhileCompact(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = res.(Model)
	sep := m.railWidth()
	res, _ = m.Update(tea.MouseMsg{X: sep, Y: headerRows + 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if res.(Model).drag {
		t.Error("the compact rail has no divider to drag")
	}
}
```

- [ ] **Step 2:** `go test ./internal/ui` -- FAIL.

- [ ] **Step 3: Implement.**

`layout.go`: add `railCompact = 18 // the collapsed rail, like herdr's sidebar`
to the constants; `railWidth()` returns `railCompact` first when
`m.compact` (before the clamp -- the compact width is below `railMin` by
design and never clamps), and add:

```go
// clipName is name when it fits width cells, else its first width-1
// cells and an ellipsis -- the compact rail's one truncation.
func clipName(name string, width int) string {
	if lipgloss.Width(name) <= width {
		return name
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(name) + "…"
}
```

`rail.go`, `compactLine`: reserve `roundW = 3` cells for the round
(`r99`; a three-digit round overflows into the pad, which is fine), clip
the name to `width - 1 - 2 - 1 - roundW - 1`, drop the qualifier:

```go
	roundW := 3
	nameW := width - 1 - 2 - 1 - roundW - 1
	nameStyle := fgStyle.Bold(true)
	switch {
	case selected:
		nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	case showState:
		nameStyle = stateStyle(b.Display).Bold(true)
	}
	name := nameStyle.Render(clipName(b.Name, nameW))
	round := dimStyle.Render(fmt.Sprintf("r%d", b.Round))
	l := fit(gutter+unreadSlot+fit(name, nameW)+" "+round+" ", width)
```

(`fit(name, nameW)` pads a short name so the round column lines up.)
Remove the `whatAge` call and the qualifier join from `compactLine`; the
`what, age` pair is cards-only now.

`compactLine` is always called with `m.railWidth()`, which is
`railCompact` while compact; `railView`, `railTop` and `railBindingAt`
need no change. `WindowSizeMsg` and `c` already re-fit the viewport
through `paneWidth()`; check that the `c` handler calls
`m.detail.vp.Width = m.paneWidth(); m.fillViewport()` -- if it does not
(round 5 may have left it at `railTop` only), add those two lines, since
the pane now changes width on `c`.

`keys.go`, `<`/`>`: `if m.compact { return m, nil }` first.
`mouse.go`, the drag press: add `!m.compact &&` to the condition.

- [ ] **Step 4:** `go test ./internal/ui` -- PASS; goldens unchanged.

- [ ] **Step 5: Mutation check.** Remove the `m.compact` early return in
`railWidth()`: `TestCompactRailIsNarrow` must fail. Revert; record.

- [ ] **Step 6:** constituents green. Commit:
`feat(ui): compact collapses the rail to 18 columns, names clipped (#180)`

## Report

`NNN-report.md`: the constituents' tails; the mutation outcome; whether
the `c` handler needed the viewport re-fit; anything stopped on.
