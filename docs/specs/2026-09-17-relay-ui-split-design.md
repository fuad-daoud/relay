# `relay ui` as a split dashboard: fleet rail beside a live pane

**Issue:** #180. Canvas with the five screens and the palette:
https://claude.ai/code/artifact/ee57d36e-c37d-4f33-967b-df685445b11f
Supersedes the layout half of `2026-09-08-relay-tui-design.md` (§2 "Layout",
§10 "Multi-binding split views"); its refresh model (§6), tab sources (§7)
and error strategy (§8) stand and are referenced below, not restated.

## 1. System overview

`relay ui` today is two screens: a list of one-line rows under a
`+- relay ---+` border, and a full-width detail screen with four tab words
over a raw viewport. `relay.Status` already knows the planner and builder
panes, the hold clock, headless process facts, consults, switches, dirty
trees and machine-wide gates; the list shows a `Printf` of seven of those
in fixed columns, and the detail shows none.

This design makes one screen of it on a wide terminal: a **rail** of
binding cards on the left, grouped by state in attention order, and a
**pane** on the right that follows the rail cursor and shows the selected
binding's report, terminal, diff or log. The original spec chose
drill-down because "a diff hunk and a terminal capture are natively 80+
columns"; that stays true, so below **110 columns** the two-screen
drill-down survives, with the new cards as its list rows. Above it, the
pane has ≥ 74 columns beside a 34-column rail, which is enough for a hunk
and for what `ReadAgent` returns.

Three decisions were settled on the issue and are not reopened here:

| Decision | Choice | Why |
| --- | --- | --- |
| Sequencing | Layout first; #143's row fields (live `+N −M`, quiet age, unread) follow | Cards render what `Status` has today and grow a line when #143 lands. The visible win does not wait on git numstat and `viewed_at`. |
| Narrow terminals | Drill-down below 110 columns; no collapsed rail | One threshold, one rail width, one layout to golden-test. |
| Answer hint | Kept: one dim line under a blocked dialog naming the verb | The reader still runs nothing. `relay.WaitingOn` already computes the hint (`Waiting.Hint`). |

### Scope boundary

In scope: everything under `internal/ui` except `fetch.go`'s sources;
`styles.go` wholesale; two additive `BindingStatus` fields in
`internal/relay/status.go` (`Branch`, `Waiting`); a pure `SortRows` in
`internal/relay`; the spec amendment above; README's `status --json`
field list.

Out of scope (each has its issue): live `+N −M` and files-since-baseline,
quiet age on ACTIVE rows, the unread dot on cards and tabs, `viewed_at`
(#143); `stalled` / `exploring` / `stale` labels (#135); a `PAUSED` state
(#137); `seq` and `--after` on the log (#150); persisting the sort toggle
across runs (#143's sidecar question). The rail leaves room for each --
see §4.2 -- so none of them changes the layout.

## 2. File structure

```
internal/ui/
  ui.go        unchanged
  model.go     Model gains sort, statusAt; layout(); pointDetailAt(); split-aware WindowSizeMsg
  layout.go    NEW: splitMinWidth, railWidth, chrome constants, layout enum, pane geometry
  rail.go      NEW (replaces list.go's rendering): card lines, group headers, railWindow, railView
  list.go      keeps listModel, resolveSticky, renderError/wrapLine; listView becomes the
               drill-down list of the same cards (full width)
  pane.go      NEW (replaces detail.go's rendering): paneHead, tabBar, hint line, paneView
  detail.go    keeps detailModel, bodyOf, styleFor; detailView renders paneView full-width
  keys.go      s (sort), split-mode enter/esc, cursor moves that re-point the pane
  styles.go    the palette in §7, every colour an xterm-256 index
  fetch.go     unchanged
  *_test.go    per §9
internal/relay/
  status.go    BindingStatus.Branch, BindingStatus.Waiting; statusRow fills both
  sort.go      NEW: SortRows(rows []BindingStatus, attention bool) -- pure
  sort_test.go
README.md      status --json: branch, waiting
docs/specs/2026-09-08-relay-tui-design.md   §2 and §10 note the supersession
```

`fetch.go` stays the only file touching `rt`, and stays unchanged: the
pane reads exactly the four sources the detail screen reads today.

## 3. Layout

### 3.1 Geometry

```go
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

type layout int
const (
    layoutSplit layout = iota // rail beside pane
    layoutStack               // drill-down: list, then full-width detail
)

func (m Model) layout() layout {
    if m.width >= splitMinWidth { return layoutSplit }
    return layoutStack
}
```

Body rows = `height - headerRows - footerRows` (minus the error block,
§3.6). Viewport height = body rows − `paneHeadRows − tabRows − sourceRows`,
floored at 0, in both layouts: the pane head is drawn on the full-width
detail screen too, so a resize across the threshold changes the viewport's
width and nothing else. Viewport width = `width - railWidth - railGap` in
split, `width` in stack.

### 3.2 Header bar

One row, reversed on bg 236, always:

```
 relay   6 bindings · 1 needs you · 1 held                codex gated until 15:30  ·  14:02
```

Left: `relay` bold, then the counts -- total, then one `N <state>` per
non-zero state in attention order except ACTIVE and DONE (`needs you`
in 214, the rest in fg). Right: one clause per `Report.Gated` entry
(`<token> gated <GateUntilText>`, 214), then the wall clock `HH:MM`
(dim). When there are no bindings the left reads `relay   no bindings`;
`DoneHidden` is not a ui concern (it never hides DONE).

### 3.3 Rail

34 columns: a 1-column gutter, then cards, grouped under state headers
when the sort is attention order (§3.5). A header is one line,
`<STATE>  <count>` in the state's colour with the count faint. Cards in
a group run without blank lines between them; one blank line closes the
group. In name order there are no headers and no group gaps; the state
word moves onto the card's second line.

A card is three or four lines, rendered by `cardLines(b, selected,
showState) []string`, each line padded or truncated to `railWidth`:

```
▎● webshop                    r4      line 1: gutter, unread slot, name (bold), round (dim, right)
   question · 2m                      line 2: what · age
   dirty · 2 consults · switched 1x   line 3: tree and round facts -- OMITTED when empty
   agy · relay/webshop                line 4: builder kind [headless] · branch
```

- **Gutter**: `▎` in accent (75) on the selected card, space otherwise.
  The selected card's lines are drawn on bg 235.
- **Unread slot**: two columns, blank in this change. #143 puts `● `
  there. Reserving it now is what keeps #143 from re-laying the rail.
- **Line 2, `what · age`**, from fields `Status` has today:

  | Display | what | age |
  | --- | --- | --- |
  | NEEDS YOU | `Waiting.Cause` (`blocked` → `question`; `halted`; `broken`; `orphaned`; `needs you`) | since `Waiting.Since`, `Nm` / `Nh`; omitted when zero |
  | HELD | `<Pending.Kind> r<Pending.Round>` | `relay.HoldText(b)` |
  | ACTIVE | `BuilderStatus` (`working`, `idle`, `exited 3`, …) | `NudgeText(*Nudge)` when nudged, else omitted |
  | DONE | `done` | since `Last.TS` |

  In name order, line 2 is `<STATE> · what · age`, state coloured.
  #135's `stalled 17m` / `exploring` / `stale` replace or join `age`
  here; #137's PAUSED is one more table row.
- **Line 3** joins, in this order and only those that apply: `dirty`
  (214), `<Consults> consults`, `switched <Switches>x`, `forked from
  <ForkedFrom> r<ForkedAtRound>`. The line is dropped entirely when all
  four are empty, which is the common case. #143's live `+N −M` goes
  first on this line when it lands.
- **Line 4**: `BuilderKind`, then `headless` when `Headless != nil`,
  then `Branch`; `·`-separated, dim. A `--cwd` binding has no branch and
  no worktree: line 4 is the kind alone. `Foreign` agents are not on the
  card (they are in the pane head, §3.4).

Separators are ` · ` with the dot in faint (239). Truncation is by
`lipgloss.Width` at `railWidth - 1`, no ellipsis: a 33-column line under
a 2-space indent leaves 31 for content, and every line above fits a
14-character name and a 20-character branch. Names longer than that
truncate; that is the honest answer at 34 columns.

**Windowing.** The rail is a list of *lines*, each tagged with the index
of the binding it belongs to (headers and gap lines carry −1). The
existing `listWindow` (single-row cursor) is generalised to a span:

```go
// railWindow returns the first rail line to draw so that lines
// [first, last] -- the selected card -- are all visible in rows rows,
// moving top as little as possible. Same five rules as listWindow with
// the cursor widened to a span; rows <= 0 means no limit.
func railWindow(top, first, last, rows, n int) int
```

`listWindow` becomes `railWindow(top, cursor, cursor, rows, n)` and its
tests stay green unchanged.

### 3.4 Pane

The pane follows the rail cursor (`pointDetailAt`, §5). Its rows:

```
webshop  round 4  NEEDS YOU                                      question r4 · 2m ago
planner  %1   claude    idle · focused
builder  %7   agy       blocked · 2 consults
tree     relay/webshop · dirty · last close a1c9f0e (2 commits)

 1 report    2 terminal    3 diff    4 log
──────────────────────────────────────────────────────────────────────────
%7 · captured 1s ago · 40 lines

<viewport>
```

- **Title row**: name bold, `round N` dim, the state as a pill (black on
  the state colour). Right-aligned: `Last` as `<kind> r<round> · <age>
  ago`.
- **planner / builder rows**: label dim; pane id, kind, status; `·
  focused` on the planner; the builder row appends `pid P since HH:MM`
  and `` `<candidate>` `` for a headless builder, `<Consults> consults`
  and `switched Nx` when non-zero, exactly the facts `RenderStatus`
  prints, in the same words. Each `Foreign` agent adds a `foreign` row
  after `builder`, so the pane head grows by one row per foreign agent
  and the viewport shrinks by the same; `paneHeadRows` is the minimum.
- **tree row**: `Branch` (or the `CWD` for a `--cwd` binding), `dirty`
  when set, and from `LastClose` when present `last close r<Round>:
  <Commits> commit[s], <Tree>` -- `CloseInfo.Tree` is the word `clean` or
  `dirty` (#130), not a hash. #143 adds the live numstat here.
- **Tab bar** (amended 2026-09-18, round 3): four words, no numbers --
  `report   terminal   diff   log`, the active one bold white, the others
  dim -- over a rule whose span under the active word is `━` in accent
  (75) and `─` in 237 elsewhere. `1`-`4` still switch tabs; the footer
  says so. The unread dots #143 adds to `report` and `diff` go after the
  word.
- **Source line**: one faint line saying what the body is:
  `report r3 · 13:38`, `%7 · captured 1s ago · 40 lines` for a pane
  builder's screen, `headless · 002-builder.log · 412 lines · following`
  (or `· scrolled`) for a headless builder's log, `round 3 · 3 files ·
  +41 −6` (counted from the patch), `12 entries`. The empty-case prose of
  the old spec §7 renders in the viewport, unchanged.
- **Viewport content is wrapped, never clipped** (round 3): every body is
  word-wrapped to the viewport width before `SetContent`, and re-wrapped
  on resize, so the viewport's logical lines are its visual lines. Without
  this the viewport's own padding wraps long lines and pushes the rows
  under them off the bottom, where no scroll reaches them.
- **Terminal tab, headless builder** (round 3): the body is the whole
  round log (the last 5000 lines of it), scrollable, and the viewport
  follows the tail the way `tail -f` does: it is pinned to the bottom
  until the human scrolls up, and pinned again the moment they scroll
  back to the bottom. A pane builder's terminal tab stays a viewport-sized
  live screen capture: there is nothing above it to scroll to.
- **Transcript styling, headless builder** (round 3): the transcript
  renderer marks its lines -- `● <Tool> <arg>` for a call, `  ⎿ ok: …` /
  `  ⎿ error: …` for a result (`2026-09-17-headless-transcript-design.md`
  §4.3, amended). On the terminal tab of a headless builder the ui draws a
  call as `●` in 42, the tool name bold, and `(<arg>)` dim; an ok result
  as `⎿` and its text dim; an error result as `⎿ error: …` in 203; prose
  untouched. A pane builder's capture is never restyled.
- **Answer hint**: when the active tab is `terminal` and `Waiting != nil`
  with `Cause == "blocked"`, the last viewport row is replaced by
  `relay: <Waiting.Hint>` (`relay:` in accent, the verb in fg). It is a
  rendered line, not a viewport line, so scrolling never hides it. Any
  other cause shows nothing extra: the notification and `relay status`
  already carry those hints.

The diff body keeps `relay diff`'s text and colours it: `+` lines 78,
`-` lines 203, `@@` lines 110, `diff --git` / file headers bold white.
Colouring is a pure `colourDiff(string) string` over lines; it never
parses further.

### 3.5 Sort

```go
// SortRows orders status rows for a human. attention groups by display
// state -- NEEDS YOU, HELD, ACTIVE, DONE -- and within a group puts the
// most recent Last.TS first, name as the tiebreak; name is the order
// Status has always returned. Stable; never mutates its input.
func SortRows(rows []BindingStatus, attention bool) []BindingStatus
```

Lives in `internal/relay` because #143 wants `status` and the status
line on the same order; in this change only `ui` calls it, and `relay
status` output is unchanged. `s` toggles `Model.sort` between attention
(the default) and name; the choice lives for the process. The sticky
cursor (`resolveSticky`) already tracks a name, so a re-sort moves the
window, not the selection.

### 3.6 Footer and error block

```
↑↓ move   ⏎ focus pane   tab next pane   1-4 pane   s sort: attention   q quit      auth NEEDS YOU   refreshed 2s ago
```

Left: keys for the current focus (§6), key in fg, verb in dim. Right,
right-aligned: the notices the footer carries today (`m.notice`, `!
refresh failed (retrying)`, every *other* binding at NEEDS YOU -- now in
214 bold and without the `!`), then `refreshed Ns ago` in faint from
`Model.statusAt`, the time of the last good `statusMsg`. When the two
sides would overlap, the right side wins and the left truncates: the
notice is the part a human must not miss.

The error block (`renderError`, up to 8 wrapped lines) keeps its place
under the header in both layouts and still reduces the body rows.

### 3.7 Stack layout (below 110 columns)

The list screen is the rail at full width: the same `cardLines`, the
same headers, `railWindow` over the same tagged lines, footer keys `↑↓
move  ⏎ open  s sort  q quit`. The detail screen is `paneView` at full
width with `esc back` in the footer. The behaviour is the current
two-screen flow; only the drawing changes.

## 4. Data structures

```go
type Model struct {
    // ... existing fields unchanged ...
    sort     bool      // true = attention order (default); s toggles
    statusAt time.Time // time of the last good statusMsg; zero until then
}

// rows is the report's bindings in display order. Every index in the
// model -- list.cursor, list.top -- indexes THIS slice, never
// report.Bindings directly.
func (m Model) rows() []relay.BindingStatus {
    return relay.SortRows(m.report.Bindings, m.sort)
}

type railLine struct {
    text    string // rendered, exactly railWidth wide
    binding int    // index into rows(); -1 for a header or gap line
}
```

`BindingStatus` gains:

```go
// Branch is the binding's worktree branch; "" for a --cwd binding.
Branch string `json:"branch,omitempty"`
// Waiting is set when the binding is stalled on a human (WaitingOn):
// the cause, the one-line reason, since when, and the verb that
// resolves it. Nil otherwise.
Waiting *Waiting `json:"waiting,omitempty"`
```

`statusRow` has the binding and its log entries in hand; `WaitingOn` is
pure over those, so the cost is nil -- no extra lock. `Waiting.Line` is
what `relay wait` prints; the ui uses `Cause`, `Since` and `Hint`.

`listModel` and `detailModel` are unchanged. `screen` keeps its two
values and in split layout means **focus**: `screenList` is the rail,
`screenDetail` is the pane. The view draws both regardless.

## 5. Pane follows the cursor

```go
// pointDetailAt re-targets the pane at the binding under the cursor:
// name, round (row.Round - 1), lastLogTS from row.Last, every cache
// cleared, every parked scroll zeroed. The active tab is kept -- a
// human reading diffs across bindings stays on diff. It issues the
// visible-tab fetch only if tabInFlight is clear; a fetch already in
// flight for the previous binding is discarded on arrival by tabMsg's
// name check, which exists for exactly this.
func (m Model) pointDetailAt(name string) (Model, tea.Cmd)
```

Called from: a cursor move in split layout (`up`/`down`/`j`/`k`); `enter`
in stack layout (as today); a `WindowSizeMsg` that crosses into split
while `m.detail.name` is not the cursor's binding; a `statusMsg` whose
re-sort leaves the cursor on a different binding than the pane shows
(only possible when the selected binding vanished and `resolveSticky`
clamped).

Cost, against the old spec's §6: a cursor move issues at most one fetch,
guarded by `tabInFlight`; holding `j` across ten bindings issues one
fetch, discards nothing it did not have to, and lands on the tenth
binding's content when the guard clears and the next tick's
`visibleTabFetch` sees `loaded == false`. Rules 1-3 hold unchanged; the
only new trigger for `visibleTabFetch` is the cursor.

In stack layout the pane is not visible on the list screen, so
`visibleTabFetch` returns nil there as today. In split layout it is
always visible, so the terminal tab polls on every tick while selected --
the same cost the detail screen has today, now paid whenever the ui is
open on a terminal tab. That is the honest price of a split view and it
is the reason the terminal tab is not the default: `pointDetailAt` keeps
the active tab, and a fresh model starts on `report`.

## 6. Keys

| Layout | Focus | Key | Effect |
| --- | --- | --- | --- |
| both | rail / list | `↑` `k` `↓` `j` | move cursor; split: `pointDetailAt` |
| both | rail / list | `s` | toggle sort; cursor stays on its binding |
| split | rail | `⏎` | focus pane (`screenDetail`) |
| split | pane | `esc` `backspace` | focus rail (`screenList`) |
| stack | list | `⏎` | open detail (as today) |
| stack | detail | `esc` `backspace` | back to list (as today) |
| both | pane / detail | `tab` `shift+tab` `1`-`4` | switch tab (as today) |
| both | pane / detail | anything else | viewport scroll (as today) |
| both | any | `q` `ctrl+c` | quit |

In split layout with the pane focused, `↑`/`↓` scroll the pane; the
rail cursor does not move. **Focus is visible** (round 3): the `│`
separator between rail and pane is drawn in accent (75) while the pane is
focused and in 237 while the rail is, and the selected card's `▎` gutter
swaps the other way -- accent while the rail is focused, dim (245) while
the pane is. One of the two is always lit. `1`-`4` work from the rail too: switching tab
without leaving the rail is the common move, and it reads as a pane
operation only because it fetches. `s` works from the pane as well.

### 6.1 Mouse (round 4, 2026-09-18)

The program takes the mouse (`tea.WithMouseCellMotion`). Until it did, a
terminal turned the wheel into arrow keys, so a wheel over the transcript
walked the fleet whenever the rail had focus. With the mouse owned, the
wheel scrolls what is under the pointer and a click selects what is under
it; focus follows the click. Hit-testing is a pure function of the model's
geometry (`hit(x, y)`), the same numbers `splitView` draws with.

| Where | Wheel | Left click |
| --- | --- | --- |
| a rail card | move the cursor one binding per notch (the pane follows, as `j`/`k`) | select that binding; focus the rail |
| the tab row | -- | switch to the tab whose word is under the pointer; focus the pane |
| the pane body | scroll the viewport three lines per notch; the terminal tab's follow rule applies (at the bottom = following) | focus the pane |
| header, footer, separator | -- | -- |

In stack layout the list screen is all rail and the detail screen is all
pane; a click on a list card selects without opening (`⏎` opens). The old
spec's §10 "Mouse support" exclusion is superseded by this section.

## 7. Palette

`styles.go` is replaced. Every colour is an xterm-256 index so a
terminal theme renders it consistently; none of the current 205 / 62 /
212 survive.

| role | index | used for |
| --- | --- | --- |
| fg | 252 | body text |
| dim | 245 | labels, secondary facts |
| faint | 239 | separators' dots, source line, `refreshed` |
| rule | 237 | `│` separator column, `─` under tabs |
| selected bg | 235 | the selected card |
| header bg | 236 | header bar |
| accent | 75 | selection gutter, focused-pane separator, active-tab underline, `relay:` in the hint; #143's unread dot |
| active tab | 255 on 24 | |
| NEEDS YOU | 214 bold | state, `dirty`, notices, `needs you` count |
| HELD | 111 | |
| ACTIVE | 42 | |
| DONE | 243 | |
| error | 203 | `renderError`, tab `err` |
| diff + / − / @@ | 78 / 203 / 110 | |

Glyphs: `▎ ─ │ ● ⏎ ↑ ↓` -- each one cell wide in every monospace font;
no Nerd Font, no emoji. `TestStateStylesDistinguishable` extends to the
four states plus the pill.

## 8. Error handling

Unchanged from the old spec §8, with one addition: when `m.err` is set
and `statusLoaded` is false, the split layout draws the rail's body as
`cannot reach herdr — see the error above` and the pane as empty; there
is no binding to point at. A `store.ErrNotFound` on the pane's binding
pops focus to the rail with the existing `<name> is gone` notice and the
cursor clamped by `resolveSticky`; `pointDetailAt` then follows.

## 9. Testing

All pure; no test executes a subcommand or reaches herdr (CLAUDE.md).

`internal/relay`:
- `SortRows`: every permutation of the four display states lands in
  attention order; within a group newer `Last.TS` first, nil `Last`
  last, name as tiebreak; `attention=false` is name order; the input
  slice is untouched.
- `statusRow` fills `Branch` from the binding and `Waiting` from
  `WaitingOn` for a blocked builder (`Cause == "blocked"`, `Hint` names
  `relay answer`); nil for an ACTIVE row.

`internal/ui`:
- **Golden views** at 140×40 (split) and 80×30 (stack), one fixture per
  display state, plus: headless builder, `--cwd` binding, dirty with
  consults and a switch, a foreign agent, a gated report, a name longer
  than the card. Each golden is a full `View()` string; the width of
  every rendered line is asserted ≤ the terminal width.
- `cardLines`: three lines when line 3 is empty, four otherwise; every
  line exactly `railWidth` wide; name order puts the state on line 2;
  attention order does not.
- `railWindow`: `listWindow`'s five rules with `first == last`, then a
  span at the top edge, the bottom edge, and a span taller than `rows`
  (top pins to `first`).
- `layout()`: 109 → stack, 110 → split.
- **Pane follows cursor**: in split, `j` issues exactly one fetch for the
  new binding when `tabInFlight` is clear and none when set; the arriving
  `tabMsg` for the old binding is discarded; the active tab survives the
  move; parked scrolls are zero.
- **Resize across the threshold**: 140 → 100 with the pane focused
  leaves `screenDetail` and the same `detail.name`, viewport width now
  100; 100 → 140 from the list screen points the pane at the cursor's
  binding and issues its fetch.
- **Sort toggle**: `s` re-orders `rows()` and the cursor stays on the
  same name; `s` twice is identity.
- **Answer hint**: the terminal tab with `Waiting.Cause == "blocked"`
  renders `relay: relay answer --name <n>` as the last pane row; any
  other cause or tab does not.
- **Footer**: another binding at NEEDS YOU appears on the right in both
  layouts; `refreshed Ns ago` reads from `statusAt`; a long left side
  truncates before the right side does.
- **Existing tests**: single-flight, invalidation, stale reply, sticky
  cursor, empty cases, error block -- all stay, re-pointed at the new
  view where they assert on rendered text.

Mutation checks, per house practice: drop the `tabInFlight` guard in
`pointDetailAt` and the pane-follows-cursor test must fail; swap two
states in `SortRows`'s order table and the permutation test must fail;
set `splitMinWidth` to 109 and the threshold test must fail.

## 10. Out of scope

- Every #143 / #135 / #137 / #150 field, listed in §1. The rail's
  reserved slots are the whole of their layout impact.
- Actions. The answer hint is a rendered string; no key runs anything.
- Round stepping, mouse, themes, configurable keys, fsnotify -- as the
  old spec §10.
- Changing `relay status`'s row order (that is #143's decision, with
  `SortRows` ready for it).
- A collapsed rail between 90 and 110 columns (settled on the issue).
