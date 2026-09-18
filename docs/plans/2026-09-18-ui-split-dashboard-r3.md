# `relay ui` split dashboard, round 3: scrollable headless terminal, wrapped viewport, styled transcript, tabs and focus (#180)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-17-relay-ui-split-design.md` (§3.4 and
§6 amended 2026-09-18 -- read those two sections first) and
`docs/specs/2026-09-17-headless-transcript-design.md` §4.3 (vocabulary
amended the same day).
**Earlier rounds:** `docs/plans/2026-09-17-ui-split-dashboard.md`, `-r2.md`.
**Issue:** #180

Trying the branch on a live binding found four things. Each is one task.

1. **Long lines wrap and push rows off the bottom.** bubbles' viewport pads
   its lines with `lipgloss.Style.Width`, which word-wraps anything wider
   than the viewport, then `MaxHeight` cuts the overflow -- so a log line
   wider than the pane wraps and the rows under it become unreachable. Fix:
   wrap every body to the viewport width *before* `SetContent`, so logical
   lines are visual lines and scrolling reaches everything.
2. **The headless terminal tab cannot scroll.** It fetches viewport-height
   lines and replaces them each tick. Fix: the whole round log, following
   the tail until the human scrolls up.
3. **Tool actions in the transcript are unstyled** and cannot be styled,
   because `Read /path` and a line of prose look the same. Fix at the
   source: the transcript marks a call `● ` and a result `  ⎿ `; the ui
   styles those on a headless builder's terminal tab.
4. **Tabs carry numbers; a focused pane looks like an unfocused one; the
   tree row misreads `CloseInfo.Tree` as a hash.** Fix: word tabs with an
   accent underline, a lit separator or gutter for focus, and `last close
   r2: 1 commit, clean`.

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

- No new dependencies; `go mod tidy` produces no diff. Wrapping uses
  `lipgloss.NewStyle().Width(w).Render`, which is already what the
  viewport does internally -- just earlier.
- `fetch.go` changes only where Task 2 says (the headless branch of
  `fetchTerminal`). `Update` stays pure; every `rt` call stays in
  `fetch.go`.
- The pane builder's terminal tab is untouched in behaviour: viewport-sized
  live capture, refetched every tick, never restyled.
- Every colour comes from `styles.go`. Every glyph is one cell wide.
- One commit per task, `feat(ui): …` / `feat(transcript): …` / `test(ui): …`.
- Existing tests change only as a step says; never loosened.

---

### Task 1: wrap viewport content to the pane width

**Files:**
- Modify: `internal/ui/detail.go` (`wrapBody`, `detailModel.follow`)
- Modify: `internal/ui/model.go` (`pointDetailAt`, `WindowSizeMsg`, `tabMsg`)
- Modify: `internal/ui/keys.go` (`switchTab`)
- Test: `internal/ui/pane_test.go`

**Interfaces:**
- Produces: `func wrapBody(body string, width int) string`
- Produces: `func (m *Model) fillViewport()` -- sets the viewport's content
  to the active tab's body wrapped to `m.detail.vp.Width`, preserving
  `YOffset` (clamped by the viewport).

- [ ] **Step 1: Failing test.** Append to `pane_test.go`:

```go
func TestWrapBodyMakesEveryLineReachable(t *testing.T) {
	long := "alpha " + strings.Repeat("word ", 40) + "omega"
	body := long + "\nsecond\nthird"
	wrapped := wrapBody(body, 50)
	for i, l := range strings.Split(wrapped, "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("line %d is %d wide: %q", i, w, l)
		}
	}
	if !strings.Contains(wrapped, "omega") || !strings.Contains(wrapped, "third") {
		t.Errorf("wrapping lost text:\n%s", wrapped)
	}
	// A path with no spaces still breaks rather than overflowing.
	path := strings.Repeat("/abcdefghij", 12)
	for i, l := range strings.Split(wrapBody(path, 50), "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("path line %d is %d wide", i, w)
		}
	}
	// Styled input keeps its styling and its width.
	styled := colourDiff("+" + strings.Repeat("x", 120))
	for i, l := range strings.Split(wrapBody(styled, 50), "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("styled line %d is %d wide", i, w)
		}
	}
}

func TestViewportReachesBottomOfLongLines(t *testing.T) {
	b := relay.BindingStatus{Name: "a", Round: 3, Display: "ACTIVE"}
	m := paneModel(t, b, tabLog)
	m.detail.vp.Width = 40
	m.detail.vp.Height = 3
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("entry %d %s END%d", i, strings.Repeat("w ", 30), i))
	}
	m.detail.cache[tabLog] = tabContent{loaded: true, body: strings.Join(lines, "\n")}
	m.fillViewport()
	m.detail.vp.GotoBottom()
	if v := stripANSI(m.detail.vp.View()); !strings.Contains(v, "END4") {
		t.Errorf("the last line must be reachable at the bottom, got:\n%s", v)
	}
	if m.detail.vp.TotalLineCount() <= 5 {
		t.Errorf("wrapped content must have more logical lines than raw (%d)", m.detail.vp.TotalLineCount())
	}
}
```

(`fmt` import if missing.)

- [ ] **Step 2:** `go test ./internal/ui -run 'TestWrapBody|TestViewportReaches'` -- FAIL: undefined.

- [ ] **Step 3: Implement.** In `detail.go`:

```go
// wrapBody word-wraps body to width so the viewport's logical lines are its
// visual lines. The viewport pads with lipgloss.Width itself, which wraps
// anything wider and then cuts the overflow with MaxHeight -- rows under a
// long line fall off the bottom where no scroll reaches them. Wrapping
// first, to the same width, is the whole fix. width <= 0 returns body.
func wrapBody(body string, width int) string {
	if width <= 0 || body == "" {
		return body
	}
	return lipgloss.NewStyle().Width(width).Render(body)
}
```

Add to `detailModel`:

```go
	// follow is the terminal tab's tail rule (spec §3.4): while true the
	// viewport is pinned to the bottom on every refresh; scrolling up clears
	// it, scrolling back to the bottom sets it. True on every re-point.
	follow bool
```

In `model.go`:

```go
// fillViewport sets the viewport to the active tab's body, wrapped to the
// viewport's width, keeping the current offset (the viewport clamps it).
// Every SetContent goes through here so a resize re-wraps.
func (m *Model) fillViewport() {
	y := m.detail.vp.YOffset
	c := m.detail.cache[m.detail.active]
	m.detail.vp.SetContent(wrapBody(bodyOf(m.detail.active, c, m.detail.headless), m.detail.vp.Width))
	m.detail.vp.SetYOffset(y)
}
```

`m.detail.headless` is Task 3's field; for this task add it to
`detailModel` now (`headless bool // the builder is headless (row.Headless != nil)`),
set it in `pointDetailAt` from `r.Headless != nil`, and give `bodyOf` the
third parameter with no behaviour yet (Task 3 fills it in). Update
`bodyOf`'s callers and tests to the three-argument form.

Replace the four `SetContent` sites:

- `pointDetailAt`: after building `m.detail`, set `m.detail.follow = true`
  and call `m.fillViewport()` instead of `vp.SetContent(bodyOf(...))`.
- `WindowSizeMsg`: after setting `vp.Width`/`vp.Height`, call
  `m.fillViewport()` (re-wrap at the new width).
- `tabMsg` (active-tab branch): replace the three lines
  `currY := …; SetContent(…); SetYOffset(currY)` with `m.fillViewport()`.
  Task 2 adds the follow rule here.
- `switchTab`: replace `m.detail.vp.SetContent(bodyOf(next, c))` with
  `m.fillViewport()` (it reads `m.detail.active`, already set to `next`),
  keeping the `SetYOffset(m.detail.scroll[next])` restore after it.

`paneView` keeps its copy-and-resize of the viewport for rendering; the
content is already wrapped to `m.detail.vp.Width`, which `WindowSizeMsg`
keeps equal to `paneWidth()`.

- [ ] **Step 4:** `go test ./internal/ui` -- PASS, including the goldens
(bodies in the fixtures are narrower than the pane; if a golden changes,
read the diff: only a body line that was wider than the pane may change,
and it must now wrap rather than vanish. Regenerate with `-update` only
after confirming that, and say so).

- [ ] **Step 5:** constituents green. Commit:
`fix(ui): wrap viewport bodies to the pane width so no row falls off the bottom (#180)`

---

### Task 2: headless terminal tab -- the whole log, following the tail

**Files:**
- Modify: `internal/ui/fetch.go` (headless branch of `fetchTerminal` only)
- Modify: `internal/ui/model.go` (`tabMsg` follow rule)
- Modify: `internal/ui/keys.go` (follow tracking after a scroll; `switchTab`)
- Modify: `internal/ui/pane.go` (`sourceLine`, terminal case)
- Test: `internal/ui/fetch_test.go`, `internal/ui/split_test.go`, `internal/ui/pane_test.go`

**Interfaces:**
- Produces: `const headlessLogLines = 5000`
- Consumes: `detailModel.follow`, `fillViewport` (Task 1).

- [ ] **Step 1: Failing tests.**

In `fetch_test.go`, find the existing test that exercises the headless
branch of `fetchTerminal` (it seeds a binding with a headless builder and a
log file; search for `BuilderLogPath` or `headless builder`). Add beside
it:

```go
func TestFetchTerminalHeadlessReturnsWholeLog(t *testing.T) {
	// Seed exactly as the neighbouring headless fetchTerminal test does,
	// but write a 40-line log; then:
	msg := fetchTerminal(context.Background(), rt, name, 5)().(tabMsg)
	if got := strings.Count(msg.content.body, "\n") + 1; got != 40 {
		t.Errorf("headless terminal body has %d lines, want all 40 regardless of the lines argument", got)
	}
	// And a log longer than the cap keeps only its tail.
	// (rewrite the log with headlessLogLines+10 numbered lines, then:)
	msg = fetchTerminal(context.Background(), rt, name, 5)().(tabMsg)
	lines := strings.Split(msg.content.body, "\n")
	if len(lines) != headlessLogLines || !strings.HasSuffix(lines[len(lines)-1], fmt.Sprint(headlessLogLines+10)) {
		t.Errorf("capped body: %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}
```

In `split_test.go`:

```go
func TestTerminalFollowsTailUntilScrolledUp(t *testing.T) {
	rows := threeRows()
	rows[0].Headless = &relay.HeadlessInfo{PID: 1, LogPath: "/x/002-builder.log"}
	m := splitModel(t, 140, 40, rows...)
	// Attention order puts webshop (NEEDS YOU) first; make api the one under
	// test by moving to it, then to the terminal tab.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	m.tabInFlight = false
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = res.(Model)
	m.tabInFlight = false
	if !m.detail.follow {
		t.Fatal("a fresh terminal tab must follow")
	}
	body := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "line %d\n", i)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	res, _ = m.Update(tabMsg{name: "api", t: tabTerminal, content: tabContent{loaded: true, body: body(100)}})
	m = res.(Model)
	if !m.detail.vp.AtBottom() {
		t.Error("following: a refresh must land at the bottom")
	}
	// Scroll up: follow clears.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // focus the pane
	m = res.(Model)
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = res.(Model)
	if m.detail.follow {
		t.Error("scrolling up must stop following")
	}
	y := m.detail.vp.YOffset
	res, _ = m.Update(tabMsg{name: "api", t: tabTerminal, content: tabContent{loaded: true, body: body(120)}})
	m = res.(Model)
	if m.detail.vp.YOffset != y {
		t.Errorf("not following: a refresh must hold the offset (%d -> %d)", y, m.detail.vp.YOffset)
	}
	// Back to the bottom: follow resumes.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = res.(Model)
	if !m.detail.follow || !m.detail.vp.AtBottom() {
		t.Error("scrolling to the bottom must resume following")
	}
}
```

(`tea.KeyEnd` is `G`/end in the viewport's default keymap; if the viewport
does not bind it, use `tea.KeyPgDown` repeatedly until `AtBottom()`.)

In `pane_test.go`, extend `TestSourceLinePerTab`'s terminal case: with
`b.Headless = &relay.HeadlessInfo{LogPath: "/x/002-builder.log"}` and
`m.detail.headless = true`, `m.detail.follow = true`, a 3-line body gives
`headless · 002-builder.log · 3 lines · following`; with `follow = false`,
`… · scrolled`. The pane-builder case (`%7 · captured 1s ago · 3 lines`)
stays as it is.

- [ ] **Step 2:** run the three -- FAIL.

- [ ] **Step 3: Implement.**

`fetch.go`, headless branch of `fetchTerminal`: replace the
`if all := strings.Split(body, "\n"); len(all) > lines { … }` tail with

```go
			if all := strings.Split(body, "\n"); len(all) > headlessLogLines {
				body = strings.Join(all[len(all)-headlessLogLines:], "\n")
			}
```

and declare, near `tabContent`:

```go
// headlessLogLines caps how much of a round log the terminal tab holds:
// the whole log for any round a human would read, a bounded body for a
// runaway one. The pane-builder branch still reads viewport-height lines
// -- that one is a screen, this one is a file.
const headlessLogLines = 5000
```

The `lines` argument is now unused on the headless path; leave the
signature alone (the pane path uses it).

`model.go`, `tabMsg` active-tab branch, after `m.fillViewport()`:

```go
		if msg.t == tabTerminal && m.detail.follow {
			m.detail.vp.GotoBottom()
		}
```

`keys.go`, pane-focused branch, after `m.detail.vp, cmd = m.detail.vp.Update(msg)`:

```go
	if m.detail.active == tabTerminal {
		// The tail rule: at the bottom means following; anywhere else
		// means the human is reading and the refresh must hold still.
		m.detail.follow = m.detail.vp.AtBottom()
	}
```

`switchTab`, after the offset restore: `if next == tabTerminal &&
m.detail.follow { m.detail.vp.GotoBottom() }`.

`pane.go`, `sourceLine` terminal case:

```go
	case tabTerminal:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		r := row(m.report, m.detail.name)
		if m.detail.headless {
			src := "headless"
			if r != nil && r.Headless != nil && r.Headless.LogPath != "" {
				src += " · " + filepath.Base(r.Headless.LogPath)
			}
			mode := "following"
			if !m.detail.follow {
				mode = "scrolled"
			}
			s = fmt.Sprintf("%s · %d lines · %s", src, n, mode)
		} else {
			pane := ""
			if r != nil {
				pane = r.BuilderPane
			}
			s = fmt.Sprintf("%s · captured %s ago · %d lines", pane, ago(c.at, m.now()), n)
		}
```

- [ ] **Step 4:** `go test ./internal/ui` -- PASS.

- [ ] **Step 5: Mutation check.** Remove the `GotoBottom()` in the
`tabMsg` branch: `TestTerminalFollowsTailUntilScrolledUp` must fail on
"following: a refresh must land at the bottom". Revert; record.

- [ ] **Step 6:** constituents green. Commit:
`feat(ui): headless terminal tab holds the whole round log and follows its tail (#180)`

---

### Task 3: transcript markers, styled on the terminal tab

**Files:**
- Modify: `internal/transcript/transcript.go` (`toolLine`, `okLine`, `errLine`)
- Modify: `internal/transcript/transcript_test.go` (tables; `-update` flag on `TestFixtures`)
- Modify: `internal/transcript/testdata/{agy,claude,opencode}.log`
- Modify: `README.md` (line ~334)
- Modify: `internal/ui/pane.go` (`colourTranscript`), `internal/ui/detail.go` (`bodyOf`)
- Test: `internal/ui/pane_test.go`

**Interfaces:**
- Produces: `func colourTranscript(body string) string`
- Consumes: `bodyOf(t tab, c tabContent, headless bool)` (Task 1).

- [ ] **Step 1: Transcript.** In `transcript.go`:

```go
// toolLine is "● <name> <main argument>", or "● <name>" when no parameter
// is a non-empty string. The marker is what lets a reader tell a call from
// assistant prose (spec §4.3, amended for #180).
func toolLine(name string, params map[string]any) string {
	if arg, ok := mainArg(params); ok {
		return "● " + name + " " + oneLine(arg)
	}
	return "● " + name
}
```

`okLine` returns `"  ⎿ ok"` / `"  ⎿ ok: " + output`; `errLine` returns
`"  ⎿ error"` / `"  ⎿ error: " + msg`; update their doc comments to match.

- [ ] **Step 2: Tables.** In `transcript_test.go`, every expected tool-call
string gains the `● ` prefix and every `"  -> ` becomes `"  ⎿ `. Do it
by reading each table entry, not by a blind replace: a `denied: …` line
and a `[type]` line keep their shape. Then add an update flag to
`TestFixtures`:

```go
var update = flag.Bool("update", false, "rewrite testdata/*.log from the renderer")
```

and, in the loop, when `*update` is set, write the rendered output to the
`.log` path instead of comparing. Run `go test ./internal/transcript
-run TestFixtures -update`, then `git diff internal/transcript/testdata`:
**every changed line must differ only by its prefix** (`● ` added, `->`
→ `⎿`). If any other line changed, stop and report. Then `go test
./internal/transcript` -- PASS.

- [ ] **Step 3: README.** Line ~334 shows the format; change the two
examples to `● Bash go test ./...` and `  ⎿ ok: ok  github.com/… 0.4s`,
`  ⎿ error: …`.

- [ ] **Step 4: Failing ui test.** Append to `pane_test.go`:

```go
func TestColourTranscript(t *testing.T) {
	body := "Now running the tests.\n● Bash go test ./...\n  ⎿ ok: ok  github.com/x 0.4s\n● Read\n  ⎿ error: no such file\n[system]"
	out := strings.Split(colourTranscript(body), "\n")
	if out[0] != "Now running the tests." {
		t.Errorf("prose must be untouched: %q", out[0])
	}
	if p := stripANSI(out[1]); p != "● Bash(go test ./...)" {
		t.Errorf("call = %q", p)
	}
	if !strings.Contains(out[1], stateActiveStyle.Render("●")) || !strings.Contains(out[1], lipgloss.NewStyle().Bold(true).Render("Bash")) {
		t.Errorf("call not styled: %q", out[1])
	}
	if p := stripANSI(out[2]); p != "  ⎿ ok: ok  github.com/x 0.4s" {
		t.Errorf("ok result text changed: %q", p)
	}
	if out[2] != dimStyle.Render("  ⎿ ok: ok  github.com/x 0.4s") {
		t.Errorf("ok result not dim: %q", out[2])
	}
	if p := stripANSI(out[3]); p != "● Read" {
		t.Errorf("call without argument = %q (no empty parens)", p)
	}
	if out[4] != errorStyle.Render("  ⎿ error: no such file") {
		t.Errorf("error result not styled: %q", out[4])
	}
	if out[5] != "[system]" {
		t.Errorf("unknown-event line must be untouched: %q", out[5])
	}
}

func TestBodyOfStylesOnlyHeadlessTerminal(t *testing.T) {
	c := tabContent{loaded: true, body: "● Bash ls"}
	if got := bodyOf(tabTerminal, c, false); got != "● Bash ls" {
		t.Errorf("a pane capture must never be restyled: %q", got)
	}
	if got := bodyOf(tabTerminal, c, true); stripANSI(got) != "● Bash(ls)" {
		t.Errorf("headless terminal = %q", stripANSI(got))
	}
	if got := bodyOf(tabLog, c, true); got != "● Bash ls" {
		t.Errorf("only the terminal tab styles transcript lines: %q", got)
	}
}
```

- [ ] **Step 5:** run them -- FAIL: undefined.

- [ ] **Step 6: Implement.** In `pane.go`:

```go
// colourTranscript styles a headless builder's log for the terminal tab,
// by the transcript's own markers and nothing else: a call is a green
// bullet, a bold tool name and its argument in parentheses, dim; an ok
// result is dim; an error result is red; every other line -- assistant
// prose, [unknown] events, the relay-exit trailer -- is left alone.
func colourTranscript(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "● "):
			rest := strings.TrimPrefix(l, "● ")
			name, arg, _ := strings.Cut(rest, " ")
			out := stateActiveStyle.Render("●") + " " + lipgloss.NewStyle().Bold(true).Render(name)
			if arg != "" {
				out += dimStyle.Render("(" + arg + ")")
			}
			lines[i] = out
		case strings.HasPrefix(l, "  ⎿ error"):
			lines[i] = errorStyle.Render(l)
		case strings.HasPrefix(l, "  ⎿ "):
			lines[i] = dimStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}
```

In `detail.go`, `bodyOf(t tab, c tabContent, headless bool)`: for a
loaded, error-free, non-empty body, `tabDiff` returns `colourDiff(c.body)`
(as now) and `tabTerminal && headless` returns `colourTranscript(c.body)`;
everything else as before. Update the doc comment.

- [ ] **Step 7:** `go test ./internal/ui ./internal/transcript` -- PASS.

- [ ] **Step 8:** constituents green. Commit as two commits, in order:
`feat(transcript): mark tool calls with ● and results with ⎿ (#180)` (transcript + README), then
`feat(ui): style transcript calls and results on a headless terminal tab (#180)`.

---

### Task 4: word tabs with an underline, visible focus, the tree row

**Files:**
- Modify: `internal/ui/pane.go` (`tabBar`, `paneHead` tree row)
- Modify: `internal/ui/rail.go` (`cardLines`, `railLines`, `railView`, `railTop` -- a `focused` parameter)
- Modify: `internal/ui/model.go` (`splitView` separator)
- Test: `internal/ui/pane_test.go`, `internal/ui/rail_test.go`, `internal/ui/split_test.go`, goldens

**Interfaces:**
- Changes: `cardLines(b, selected, showState bool, now time.Time, focused bool)`,
  `railLines(rows, cursor int, attention bool, now time.Time, focused bool)`.
  `focused` is "the rail has focus": `m.screen == screenList`.

- [ ] **Step 1: Failing tests.**

Replace `TestTabBarMarksActive` in `pane_test.go`:

```go
func TestTabBarWordsAndUnderline(t *testing.T) {
	m := paneModel(t, relay.BindingStatus{Name: "a", Round: 1, Display: "ACTIVE"}, tabDiff)
	bar := m.tabBar()
	if len(bar) != tabRows {
		t.Fatalf("%d tab rows", len(bar))
	}
	words := stripANSI(bar[0])
	if strings.ContainsAny(words, "1234") {
		t.Errorf("tabs must not carry numbers: %q", words)
	}
	if !strings.Contains(words, " report ") || !strings.Contains(words, " diff ") {
		t.Errorf("tab words = %q", words)
	}
	if !strings.Contains(bar[0], lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render(" diff ")) {
		t.Errorf("active tab not bold white: %q", bar[0])
	}
	rule := stripANSI(bar[1])
	if lipgloss.Width(rule) != m.paneWidth() {
		t.Errorf("rule is %d wide, pane is %d", lipgloss.Width(rule), m.paneWidth())
	}
	// The heavy segment sits exactly under the active word.
	start := strings.Index(words, " diff ")
	seg := []rune(rule)[start : start+lipgloss.Width(" diff ")]
	if string(seg) != strings.Repeat("━", len(seg)) {
		t.Errorf("underline under diff = %q", string(seg))
	}
	before := []rune(rule)[:start]
	if strings.ContainsRune(string(before), '━') {
		t.Errorf("heavy rule outside the active word: %q", rule)
	}
	if !strings.Contains(bar[1], accentStyle.Render(strings.Repeat("━", len(seg)))) {
		t.Errorf("underline not in accent: %q", bar[1])
	}
}
```

Add to `TestPaneHeadRows`' `want` the corrected tree row:
`"tree     relay/webshop · dirty · last close r3: 2 commits, clean"` with
the fixture's `LastClose` changed to `{Round: 3, Commits: 2, Tree: "clean"}`.
Add a one-commit case somewhere in the same test:
`LastClose: {Round: 1, Commits: 1, Tree: "dirty"}` renders
`last close r1: 1 commit, dirty`.

In `rail_test.go`, `TestCardLinesShapes` and the others pass `true` as
the new last argument. Add:

```go
func TestCardGutterDimsWhenRailUnfocused(t *testing.T) {
	b := relay.BindingStatus{Name: "a", Round: 1, Display: "ACTIVE", BuilderKind: "agy"}
	lit := cardLines(b, true, false, railNow, true)[0]
	dim := cardLines(b, true, false, railNow, false)[0]
	if !strings.Contains(lit, accentStyle.Render("▎")) {
		t.Errorf("focused rail: gutter must be accent: %q", lit)
	}
	if !strings.Contains(dim, dimStyle.Render("▎")) || strings.Contains(dim, accentStyle.Render("▎")) {
		t.Errorf("unfocused rail: gutter must be dim: %q", dim)
	}
}
```

In `split_test.go`:

```go
func TestFocusIsVisible(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	railFocused := m.View()
	if !strings.Contains(railFocused, ruleStyle.Render("│")) || strings.Contains(railFocused, accentStyle.Render("│")) {
		t.Error("rail focused: separator must be in the rule colour")
	}
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	paneFocused := res.(Model).View()
	if !strings.Contains(paneFocused, accentStyle.Render("│")) {
		t.Error("pane focused: separator must be in accent")
	}
	if !strings.Contains(paneFocused, dimStyle.Render("▎")) {
		t.Error("pane focused: the selected card's gutter must dim")
	}
}
```

- [ ] **Step 2:** run them -- FAIL.

- [ ] **Step 3: Implement.**

`tabBar`:

```go
// tabBar is the four tab words and, under them, a rule whose heavy accent
// segment sits under the active word (spec §3.4). No numbers: 1-4 still
// switch, the footer says so.
func (m Model) tabBar() []string {
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	var words, rule []string
	for i, t := range tabTitles {
		label := " " + t + " "
		if tab(i) == m.detail.active {
			words = append(words, activeStyle.Render(label))
			rule = append(rule, accentStyle.Render(strings.Repeat("━", lipgloss.Width(label))))
		} else {
			words = append(words, inactiveTabStyle.Render(label))
			rule = append(rule, ruleStyle.Render(strings.Repeat("─", lipgloss.Width(label))))
		}
	}
	gap := ruleStyle.Render("──")
	line := strings.Join(rule, gap)
	if pad := m.paneWidth() - lipgloss.Width(line); pad > 0 {
		line += ruleStyle.Render(strings.Repeat("─", pad))
	}
	return []string{strings.Join(words, "  "), line}
}
```

(The words are joined by two spaces and the rule segments by two `─`, so
the columns line up.) Move `activeStyle` into `styles.go` as
`activeTabStyle` -- replace the old bg-24 definition; nothing else uses
it after this.

`paneHead` tree row: replace the `LastClose` clause with

```go
	if lc := b.LastClose; lc != nil {
		unit := "commits"
		if lc.Commits == 1 {
			unit = "commit"
		}
		s := fmt.Sprintf("last close r%d: %d %s", lc.Round, lc.Commits, unit)
		if lc.Tree != "" {
			s += ", " + lc.Tree
		}
		tparts = append(tparts, dimStyle.Render(s))
	}
```

`rail.go`: `cardLines` gains `focused bool` as its last parameter; the
gutter is `accentStyle.Render("▎")` when `selected && focused`,
`dimStyle.Render("▎")` when `selected && !focused`, a space otherwise.
`railLines` gains `focused bool` and passes it through; `railView` and
`railTop` pass `m.screen == screenList`.

`splitView`: `bar := ruleStyle.Render("│") + " "` becomes

```go
	sepStyle := ruleStyle
	if m.screen == screenDetail {
		sepStyle = accentStyle
	}
	bar := sepStyle.Render("│") + " "
```

- [ ] **Step 4:** `go test ./internal/ui` -- the goldens fail on the tab
bar and the tree row. Regenerate with `-update`, then read every golden:
the only changes must be the tab row, the rule row, the tree row, and (for
`stack-detail`, where the pane is focused) nothing else -- stack layout
has no separator and its rail is off-screen. Say what changed.

- [ ] **Step 5:** `go test ./internal/ui` -- PASS.

- [ ] **Step 6:** constituents green. Commit:
`feat(ui): word tabs with an accent underline, lit focus, honest tree row (#180)`

---

- [ ] **Finish:** constituents green, tree clean, six commits on the branch
for this round (Task 3 makes two).

## Report

`NNN-report.md`: the constituents' tails; the Task 2 mutation outcome;
the transcript fixture diff summary (Task 3 step 2) and what the goldens
changed (Task 1 step 4, Task 4 step 4); every existing test changed and
why; anything stopped on, with the step number.
