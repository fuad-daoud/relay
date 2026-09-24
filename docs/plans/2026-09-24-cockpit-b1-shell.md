# Cockpit B1: the k9s-style shell for `relevo ui`, still read-only

Spec: `docs/specs/2026-09-24-cockpit-design.md` §4.1, §4.2, §4.3 (fleet, rounds and
round detail rows only), §4.5 and §6.1. Mockups: https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA
(boards `:fleet`, `round detail`, `: command palette`).

**This plan runs in three rounds. Each send states which round it is. Do only the steps
of that round, then stop and report.** Rounds 2 and 3 name files and functions, but
their line numbers are refreshed in their own send, because round 1 moves code.

**If a step is impossible as written or contradicts what you find, stop and report. Do
not improvise.** A halt that surfaces a design error is worth more than a green suite
that bent a test to fit.

CI has no harness binary and no network. Nothing in this plan spawns a harness. A
`cmd/relevo` test must not run `relevo ui` (it needs a terminal); test the rule as a
pure function instead (step 3.1).

## 1. System overview

Today `relevo ui` (`internal/ui`, `internal/ui/dash`) is one bubbletea `Model`
(`internal/ui/model.go`, 803 lines). It has a rail of binding cards beside a detail pane
(split layout at ≥110 columns, a list-then-detail stack below that) and a dashboard
screen toggled with `d`.

B1 replaces it with a **shell** that hosts a **stack of views**:

- **Row 1, header:** the breadcrumb (`relevo › fleet › webshop › r4`) on the left. On
  the right, attention (`● N need you`), gated providers and the clock.
- **Row 2:** the top view's context line.
- **Body:** the top view.
- **Last two rows:** a rule, then the top view's keys, with notices on the right.
- **Keys:** `:` opens a command line with fuzzy completion, `?` a help overlay, Enter
  drills in, Esc pops.

Three views exist after B1, all built from today's code:

- **`:fleet`**: bindings as a table, which replaces the rail.
- **round detail**: today's pane (head, five tabs, viewport), now full screen.
- **`:rounds`**: today's dashboard (`internal/ui/dash`), hosted as a view.

`relevo` with no arguments on a terminal opens the cockpit, and `relevo ui :rounds`
starts on a view. The ui stays read-only. B2 adds actions, and C1/C2 add more views.

## 2. File structure (end of B1)

```
internal/ui/
  view.go          NEW  View interface, Env, KeyHelp, stack messages
  shell.go         NEW  root Model: view stack, status polling, global keys, message routing
  frame.go         NEW  header, context row, error block, footer rule and keys, help overlay rendering
  cmdline.go       NEW  ':' command line: command table, fuzzy completion, execute
  view_fleet.go    NEW  :fleet table
  view_round.go    NEW  round detail view (wraps roundPane)
  view_rounds.go   NEW  :rounds (hosts dash.Model)
  round_pane.go    NEW (round 1)  roundPane: detail state + fetch orchestration + pane rendering, moved out of Model
  pane.go          KEPT pure render helpers; its Model methods move to roundPane (round 1)
  detail.go        KEPT detailModel, bodyOf, wrapBody; detailHeader moves to roundPane (round 1)
  fetch.go         KEPT fetchers, tab enum, tabMsg, statusMsg (screen enum and scope arg removed in round 2)
  source.go        KEPT; RunSource builds the shell (round 2)
  prefs.go         KEPT; fields reduced (round 2)
  styles.go        KEPT; the only file naming colours
  ui.go            KEPT; Options changes (round 2)
  rail.go          DELETED in round 2, except ago, whatAge, fit, spread (moved to view_fleet.go / frame.go)
  list.go          DELETED in round 2; renderError, wrapLine, maxErrorLines move to frame.go
  model.go         DELETED in round 2 (replaced by shell.go)
  keys.go, layout.go, mouse.go, scope.go   DELETED in round 2
internal/ui/dash/
  model.go         + `Embedded bool` field (round 2)
  render.go        headerLine omitted when Embedded (round 2)
cmd/relevo/main.go run(): bare relevo on a terminal becomes `ui`; cmdUI: positional `:cmd`, `--dashboard` removed (round 3)
docs/design.md     the "read-only reader" sentence reworded (round 3)
```

## 3. Deletions: the closed list

B1 deletes exactly these behaviours. **Everything not on this list survives.** A test
that asserted surviving behaviour through a deleted mechanism is ported (its assertion
kept, its setup changed), not deleted. Every deleted test must cite its item number in
the report.

| # | deleted | replaced by |
|---|---|---|
| X1 | The split layout and the stack layout: `layout.go`, `splitMinWidth`, the rail beside the pane, the `<` / `>` rail-width keys, the `rail_cols` pref | full-width views |
| X2 | The rail's card rendering and compact mode: `cardLines`, `compactLine`, `histCardLines`, `histCompactLine`, `railLinesAll`, `railLines`, `groupOrder`, `railSpan`, `railWindow`, `railView`, `railTop`, `facts`, `unreadSlotText`, `histStateText`, `histFacts`, `sep`, `railLine`; the `c` key; the `compact` pref | the `:fleet` table |
| X3 | Scope all in the fleet: the `a` key, `scope.go`, `Model.dbRows`, `fetchStatus`'s `relevo.Bindings` call, `Options.Here`, the `scope` pref | archived bindings' rounds stay reachable through `:rounds` → Enter |
| X4 | Mouse capture: `mouse.go`, `tea.WithMouseCellMotion()` | keyboard only; the terminal's own selection and scrolling work again |
| X5 | The `d` key, `relevo ui --dashboard`, `Options.Dashboard` | `:rounds`, and `relevo ui :rounds` |
| X6 | The footer's "`<other>` NEEDS YOU" notes while the pane is visible | the header's `● N need you` |
| X7 | The list, split and stack-detail screen renderers: `listView`, `splitView`, `detailView`, the `screen` enum | the shell frame |

Surviving behaviour that must keep a test:

- Tabs fetch lazily and are cached; a stale or mismatched `tabMsg` is dropped.
- Scroll is parked and restored per tab.
- A newer log timestamp invalidates the caches; a vanished binding pops back with the
  notice "`<name>` is gone".
- `[` / `]` step rounds and refetch.
- The terminal tab follows its tail until you scroll up.
- An archived round opens from the dashboard and reads from the DB.
- `MarkViewed` is called when a live binding's detail opens.
- The sort toggle is saved.
- The dashboard query and sort are saved.
- The error block and the `! refresh failed (retrying)` note.
- The sticky notice cleared by the next key.
- The header's gates and clock.
- `refreshed Ns ago`.
- The non-terminal refusal.
- `serve ui`, including `:rounds` refused there (no DB).
- Sticky selection across refreshes.
- `q` / `ctrl+c` quit.

## 4. Data structures

### 4.1 `internal/ui/view.go` (round 2)

```
// View is one screen of the cockpit. Views are values: Update returns the next value.
type View interface {
    Crumbs() []string                          // breadcrumb segments this view adds, e.g. {"webshop", "r4"}; never empty
    Context(env Env) (left, right string)      // row 2, styled; each side may be ""
    Keys() []KeyHelp                           // this view's keys, in footer order (the shell appends the global ones)
    Capturing() bool                           // true while the view owns every key (an open text input): the shell
                                               // then forwards ':', '?', 'q' and 'esc' to it instead of acting
    Update(msg tea.Msg, env Env) (View, tea.Cmd)
    Body(env Env, width, height int) string    // exactly height lines, none wider than width
}

type KeyHelp struct{ Key, Help string }       // e.g. {"enter", "open"}; Key as displayed

// Env is what the shell lends a view on every call. Views never keep it.
type Env struct {
    Ctx      context.Context
    Src      Source
    Report   relevo.Report   // newest good status (zero before the first)
    Loaded   bool            // a status has arrived at least once
    StatusAt time.Time       // when it arrived
    Now      time.Time       // the shell's clock, read once per call
    Width    int             // terminal columns
    Height   int             // terminal rows
}

// Stack messages. A view returns these as commands; only the shell acts on them.
type pushMsg struct{ v View }          // push v on top
type popMsg struct{}                   // pop the top view (no-op at depth 1)
type rootMsg struct{ vs []View }       // replace the whole stack (a ':' command); len(vs) >= 1
type noticeMsg struct{ text string }   // set the sticky footer notice
type prefMsg struct{ key, value string } // key is one of "sort", "dashboard", "dashboard_sort"

func push(v View, init tea.Cmd) tea.Cmd   // tea.Batch(func() tea.Msg { return pushMsg{v} }, init)
func pop() tea.Cmd
func root(vs ...View) tea.Cmd
func notice(text string) tea.Cmd
```

### 4.2 Root model, `internal/ui/shell.go` (round 2)

`type Model struct` keeps its name, because `RunSource` and the tests construct it.

| field | type | purpose |
|---|---|---|
| `src` | `Source` | reads |
| `ctx` | `context.Context` | every fetch |
| `opts` | `Options` | interval, prefs store, notice, pipe hint, start command |
| `stack` | `[]View` | `stack[0]` is the root; never empty after `newModel` |
| `report` | `relevo.Report` | newest good status; kept on a failed refresh |
| `err` | `error` | last refresh error |
| `notice` | `string` | sticky footer notice; cleared by the next key |
| `statusInFlight` | `bool` | single-flight for the status fetch |
| `statusLoaded` | `bool` | a status has arrived |
| `statusAt` | `time.Time` | when |
| `started` | `bool` | `opts.Start` has been executed (after the first status) |
| `width`, `height` | `int` | terminal size |
| `ready` | `bool` | the first WindowSizeMsg has arrived |
| `now` | `func() time.Time` | test clock |
| `cmd` | `cmdLine` | the command line; `cmd.open` shows it |
| `help` | `bool` | help overlay shown |
| `prefs` | `prefs` | the reduced prefs struct (§4.6); saved on every `prefMsg` |

### 4.3 `fleetView`, `internal/ui/view_fleet.go` (round 2)

| field | type | purpose |
|---|---|---|
| `cursor` | `int` | index into `rows(env)` |
| `sticky` | `string` | the selected row's `Key()`, so the selection survives reordering (today's `resolveStickyRows` rule) |
| `top` | `int` | first visible row index |
| `attention` | `bool` | sort order: attention (`relevo.SortRows(rows, true)`) or name; from the `sort` pref |

**Columns.** A 3-cell gutter: `▎` (accent) on the selected row, then `●` (amber) when
`b.Unread`, then a space. Cells are padded to width and joined by 2 spaces.

| column | width | value |
|---|---|---|
| NAME | 14 | `b.Key()`, clipped with `…` |
| ACTOR | 12 | `b.Role`, or `"builder"` when empty |
| ON | 22 | `candidateText(b)`: `b.BuilderName` when that field exists and is non-empty (A1 round 2 adds it; if it is not in `BindingStatus` yet, skip this branch); otherwise the model part of `b.BuilderCandidate`, i.e. everything after the second `/` (`cline-pass/deepseek-v4.1-flash#high`), clipped; `-` when empty. One function, so the naming rule lives in one place. |
| RND | 4 | `r<round>` |
| STATE | 10 | `b.Display`, `stateStyle` |
| NOW | 24 | `whatAge(b, env.Now)`: `what`, plus `" · " + age` when age is non-empty |
| SPEND | 8 | `usage.MoneyShort(*b.Spend)` when set, else empty |
| PLANNER | 14 | `b.PlannerName`, or `-` |
| REPO | the rest, at least 10 | `b.CWD` with `$HOME` shown as `~`, clipped from the left with a leading `…` |

- **Narrow terminals:** when the fixed columns do not fit, drop REPO, then PLANNER,
  then SPEND, then ACTOR, in that order, until they fit.
- **The NEEDS YOU line:** a row with `Display == "NEEDS YOU"` and `Waiting != nil`
  and a non-empty `Waiting.Line` gets a second line: 5 spaces, `└ `, then
  `Waiting.Line` in dim, fitted to width.
- **Windowing:** it counts lines, not rows, and always keeps the cursor row's lines
  fully visible.

### 4.4 `roundPane`, `internal/ui/round_pane.go` (round 1)

| field | type | purpose |
|---|---|---|
| `src` | `Source` | fetches, `MarkViewed` |
| `ctx` | `context.Context` | fetches |
| `now` | `func() time.Time` | the pane head's ages |
| `report` | `relevo.Report` | for `row(report, name)` |
| `detail` | `detailModel` | unchanged type, moved here from `Model.detail` |
| `tabInFlight` | `bool` | moved here from `Model.tabInFlight` |
| `width` | `int` | the pane's width (today `m.paneWidth()`) |
| `rows` | `int` | the pane's rows (today `m.bodyRows()`) |

`func (p roundPane) viewportHeight() int` returns
`max(0, p.rows - paneHeadRows - tabRows - sourceRows)`. This is the same formula as
`Model.viewportHeight`, `layout.go:103-109`.

### 4.5 `roundView` and `roundsView` (round 2)

- `roundView{pane roundPane}` (`view_round.go`).
  - Crumbs: `{name, "r<round>"}`, where `name` is `pane.detail.name` and round is
    `pane.detail.round`.
  - It is constructed by `newRoundView(env, key string, round int) (View, tea.Cmd)`
    for a live row, or `newHistRoundView(env, h relevo.HistoryBinding, round int)
    (View, tea.Cmd)` for an archived one.
  - `round == 0` means "the default": `Round-1` for live, `Rounds` for hist. These
    are today's `pointDetailAt` / `pointDetailAtHist` rules.
- `roundsView{dash dash.Model}` (`view_rounds.go`).
  - Constructed by `newRoundsView(env, query, sortKey string) (View, tea.Cmd, error)`.
  - The error is `relevo.ErrNoDatabase` when `env.Src.Base().DB == nil`, as
    `enterDash` refuses today (`model.go:551-570`).

### 4.6 Prefs (round 2)

`prefs` keeps `sort`, `dashboard` and `dashboard_sort`, and drops `compact`,
`rail_cols` and `scope` (X1–X3). Old JSON that carries those keys still decodes: the
dropped keys are ignored, because `encoding/json` ignores unknown fields.

### 4.7 `cmdLine`, `internal/ui/cmdline.go` (round 2)

| field | type | purpose |
|---|---|---|
| `open` | `bool` | shown |
| `input` | `textinput.Model` | the text after `:` |
| `sel` | `int` | index into the current matches |

**The command table** (`[]command{name, args, help string}`):

| command | args | help | effect |
|---|---|---|---|
| `fleet` | | bindings on this machine | `root(fleet)` |
| `rounds` | `[query…]` | every round, filtered | `root(rounds)`; a non-empty query replaces the saved one |
| `round` | `<binding> [N]` | open one binding's round | `root(fleet, round)`; unknown binding: notice `unknown binding "x"` |
| `help` | | keys | opens the help overlay |
| `quit` | | leave | `tea.Quit` |

**Completion candidates:** each command name, plus `round <key>` for each live row's
`Key()`.

**Matching** is a case-insensitive subsequence of the typed text, excluding its
arguments. Candidates rank by, in order: the name has the typed text as a prefix; a
shorter name; alphabetical. At most 8 are shown.

## 5. Contracts

### 5.1 `roundPane` (round 1)

Every method is **moved** from `Model` with its body unchanged except the receiver.
Replace `m.detail` → `p.detail`, `m.tabInFlight` → `p.tabInFlight`, `m.src` → `p.src`,
`m.ctx` → `p.ctx`, `m.now()` → `p.now()`, `m.report` → `p.report`,
`m.paneWidth()` → `p.width`, `m.bodyRows()` → `p.rows`,
`m.viewportHeight()` → `p.viewportHeight()`, and `m.<moved method>` → `p.<moved method>`.

| method on `roundPane` | moved from | change beyond the receiver |
|---|---|---|
| `visibleTabFetch() tea.Cmd` | `model.go:161-181` | drop the `paneVisible()` guard; `Model` keeps it in a wrapper |
| `pointDetailAt(key string) (roundPane, tea.Cmd)` | `model.go:190-225` | none |
| `pointDetailAtHist(h relevo.HistoryBinding) (roundPane, tea.Cmd)` | `model.go:233-258` | none |
| `fillViewport()` (pointer receiver) | `model.go:277-288` | none |
| `invalidate() (roundPane, tea.Cmd, gone bool)` | the part of `maybeInvalidate` (`model.go:290-330`) after its first guard | when the row is gone, return `gone=true` and change nothing; `Model` handles the screen, the notice and the split re-point |
| `stepRound(delta int) (roundPane, tea.Cmd)` | `model.go:337-356` | none |
| `onTab(msg tabMsg) roundPane` | the `tabMsg` arm, `model.go:485-509` | the `paneVisible()` early return stays in `Model` |
| `cycleTab(msg tea.KeyMsg) (roundPane, tea.Cmd)` | `keys.go:147-152` | none |
| `switchTab(next tab) (roundPane, tea.Cmd)` | `keys.go:154-173` | none |
| `detailHeader() string` | `detail.go:98-107` | none |
| `paneHead`, `histPaneHead`, `tabBar`, `sourceLine`, `hintLine` | `pane.go` | none |
| `view(width int) string` | `paneView`, `pane.go:343-382`, from `b := row(...)` onward | the `m.empty()` branch stays in `Model.paneView` |

`Model` keeps a method of every old name, so every caller and test keeps compiling.
Each one:

1. calls `m.syncPane()`,
2. delegates,
3. stores the returned pane in `m.pane`,
4. returns what the old method returned.

`func (m *Model) syncPane()` sets `p.src`, `p.ctx`, `p.now`, `p.report`, `p.width =
m.paneWidth()`, `p.rows = m.bodyRows()`. It is called at the start of every wrapper
and in `View` before rendering.

**Postcondition of round 1:** `View()` output is byte-identical for every golden, and
every existing test passes after the mechanical rename in step 1.3 and nothing else.

### 5.2 Shell message routing (round 2)

| message | handling |
|---|---|
| `tea.WindowSizeMsg` | store, set `ready`, forward to every view in the stack |
| `tickMsg` | re-arm the tick; `fetchStatus` if not in flight; forward to every view |
| `statusMsg` | store `report` / `err` as today (`model.go:426-447`, without the scope-all fallback, X3); forward to every view; if `!started`, execute `opts.Start` through the command table and set `started` |
| `pushMsg` / `popMsg` / `rootMsg` | act on the stack; `popMsg` at depth 1 is a no-op |
| `noticeMsg` | `m.notice = text` |
| `prefMsg` | set that pref field; return `savePrefs` |
| `tea.KeyMsg` | clear `notice`, then the first rule that applies: (1) `ctrl+c` quits. (2) `cmd.open`: cmdline handles the key. (3) `help`: `esc`, `?` or `q` close the overlay, other keys are ignored. (4) `top.Capturing()`: forward to the top view. (5) `:` opens the cmdline. (6) `?` opens help. (7) `esc` pops when depth > 1, else does nothing. (8) `q` quits when depth == 1, else pops. (9) forward to the top view. |
| anything else (`tabMsg`, `dash.RowsMsg`, `dash.ErrMsg`, `dash.JumpMsg`, `roundOpenMsg`, `prefsSavedMsg`) | forward to every view in the stack, bottom to top; each view ignores what it does not know |

A view's `Update` result replaces it at its index. The commands from every view are
batched.

### 5.3 The frame, `internal/ui/frame.go` (round 2)

Rows, top to bottom:

1. **Header** (`headerBar` style, full width).
   - Left: `" relevo"` in bold, then `" › "` + each crumb of each stack view, bottom
     to top.
   - Right: `● N need you` (`stateNeedsYouStyle`; `● 1 needs you` when N is 1; left
     out when N is 0), then one `<token> gated <until>` per `report.Gated` entry (the
     text today's `headerView` builds, `model.go:743-746`), then the clock `15:04`.
     Parts are joined by `"  ·  "`.
2. **Context.** `top.Context(env)`, spread to width.
3. **Error block**, when `err != nil`: `renderError` (moved from `list.go`), at most
   `maxErrorLines`.
4. **Body.** When `cmd.open`, the first `min(len(matches)+2, 10)` body lines are
   replaced by the command box: an input line `:<text>█`, then one line per match
   (`▸` on `sel`, the name in accent, its help in dim). When `help` is on, the whole
   body is the help listing: a global section (`:` command, `/` filter, `?` help,
   `esc` back, `q` quit / back), then `top.Keys()`, as two columns. Otherwise the body
   is `top.Body(env, width, bodyHeight)`.
5. **A rule** (`ruleStyle`, `─` × width).
6. **Keys.**
   - Left: `top.Keys()` then `: command`, `? help`, and `esc back` when depth > 1 or
     `q quit` at depth 1, each rendered `fgStyle(key) + " " + dimStyle(help)` and
     joined by 3 spaces.
   - Right: the notice (`stateNeedsYouStyle`), then `! refresh failed (retrying)`
     (`errorStyle`) when `err != nil`.
   - The right side wins on overlap, as in today's `footerView`.

`bodyHeight = height - 2 (header, context) - errorRows - 2 (rule, keys)`. Before
`ready`, `View()` returns `"loading…"` as today.

### 5.4 View contracts (round 2)

**`fleetView`**
- **Context:** left `N bindings · N need you · N active`; right `sort attention` (or
  `sort name`) `· refreshed Ns ago` (faint).
- **Before the first status:** the body is `loading…`. **With no bindings:** it is
  `emptyPaneBlock` (`pane.go:311-337`).
- **Keys:** `↑↓`/`j k` move, `home`/`end`, `enter` open, `s` sort.
- **Enter** returns `push(newRoundView(env, row.Key(), 0))`.
- **On `statusMsg`:** re-resolve `cursor` from `sticky`, using today's
  `resolveStickyRows` logic (`list.go:64-87`) on the new rows.
- **`s`** toggles `attention`, keeps the selection, and returns
  `prefMsg{"sort", "attention"|"name"}`.

**`roundView`**
- **Context:** left is `pane.detailHeader()`; right is empty.
- **Body:** `pane.view(width)` with `pane.width = width` and `pane.rows = height`.
- **Keys:** `tab`/`shift+tab` next/previous tab, `1-5` tab, `[ ]` round, `↑↓ pgup
  pgdn` scroll (to the viewport).
- **On `tickMsg`:** `pane.visibleTabFetch()` when `!pane.tabInFlight`, as today's
  tick (`model.go:401-424`).
- **On `statusMsg`:** `pane.invalidate()`. When `gone`, return `tea.Batch(pop(),
  notice(name + " is gone"))`.
- **On `tabMsg`:** `pane.onTab(msg)`.
- **On `WindowSizeMsg`:** resize the viewport. The shell's body height is passed in
  `Body`, and the viewport height is set from it in `Update(WindowSizeMsg)`, computed
  exactly as `Body` will compute it.

**`roundsView`**
- `dash.Embedded = true`; `SetSize(width, bodyHeight)`.
- **Capturing:** `dash.Editing()`.
- **Context:** left `query: <dash.QueryText()>` (or `all rounds`); right `by <axis>`
  when grouped.
- **Keys:** `/` filter, `b` regroup, `s` sort, `S` flip, `enter` open, `r` refresh.
- **Every other message** goes to `dash.Update`. When the query or sort changed,
  return a `prefMsg` for `dashboard` / `dashboard_sort`, as `saveDash` does today.
- **On `tickMsg`:** `if dash.ShouldRefresh(now) { dash.Refresh() }`.
- **On `dash.JumpMsg`:** return the command `openRound(env, msg)` (below).
- **On `roundOpenMsg`:** push the round view it names.

**`openRound(env, msg dash.JumpMsg) tea.Cmd`**, asynchronous:

1. If a live row has `Name == msg.BindingName` (on a planner source, `Key() ==
   Name`), return `roundOpenMsg{liveKey, round: msg.Round}`.
2. Else run `relevo.Bindings(ctx, src.Base(), "")`, find the row with `ID ==
   msg.BindingID` (fall back to `Name == msg.BindingName`), and return
   `roundOpenMsg{hist: &h, round: msg.Round}`.
3. If none is found, return `noticeMsg{"<name> not found"}`.

This replaces `jumpFromDash`'s scope-all flip (X3).

## 6. Pseudocode: one key through the shell (round 2)

```
Update(KeyMsg k):
  m.notice = ""
  if k == ctrl+c                      -> quit
  if m.cmd.open                       -> m.cmd, cmd = m.cmd.update(k, env); return   // enter executes: see below
  if m.help                           -> if k in {esc, ?, q}: m.help = false; return
  top = m.stack[len-1]
  if top.Capturing()                  -> forward(k); return
  switch k:
    ":"  -> m.cmd = m.cmd.opened(); return
    "?"  -> m.help = true; return
    esc  -> if depth > 1: pop; return
    "q"  -> if depth == 1: quit else pop; return
  forward(k)

cmdLine.update(enter):
  name, args = split(input.Value())
  c = exact match on name, else matches[sel]
  switch c.name:
    fleet  -> root(newFleetView(prefs.sort))
    rounds -> v, init, err = newRoundsView(env, join(args) or prefs.dashboard, prefs.dashboard_sort)
              err -> notice("no database: " + err); else root(v) + init
    round  -> key = args[0]; N = atoi(args[1]) or 0
              no live row with Key()==key -> notice(`unknown binding "key"`)
              else root(newFleetView, roundView(key, N)) + init
    help   -> m.help = true
    quit   -> tea.Quit
  close the command line
```

## 7. Error handling

| case | behaviour |
|---|---|
| Refresh error | Stored in `err`: the error block under row 2, plus `! refresh failed (retrying)`. The last good `report` stays. Unchanged from today. |
| `:rounds` with no DB | Notice `no database: <err>`; the stack is unchanged. The same on `serve ui` (`Base().DB == nil`). |
| Unknown `:` command | Notice `unknown command ":x" (try :help)`. |
| `:round` with an unknown binding | Notice `unknown binding "x"`. |
| A round view's binding vanishes | Pop and notice `<name> is gone` (today's rule, `model.go:299-300`). |
| A jump target not found | Notice `<name> not found`. |
| A panic | Bubbletea restores the terminal, as today. |

None of these is fatal, and none is logged. The ui has no log.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch independent reads, searches and edits as parallel tool calls in one step.
- Read once, from the locations this plan names. Do not re-find what it located.
- Make every change to a file in one edit call, or one scripted edit when the change is
  mechanical (step 1.3 is scripted).
- Iterate on the focused command and fix every reported error before the next run:
  `go test ./internal/ui/...`. Regenerate goldens only in the step that says so:
  `go test ./internal/ui/... -run TestGoldenViews -update`.
- Run the full check once at the end of the round: `make check` (gofmt over tracked
  files, `go vet`, the `go mod tidy` check, `go test ./...`).

## 9. Ordered steps

### Round 1: extract `roundPane` (pure refactor, no behaviour change)

**1.1 Create `internal/ui/round_pane.go`.**
- Deliverable: the `roundPane` type (§4.4), `viewportHeight()`, and every method in
  the §5.1 table, moved with the receiver rewrite. Delete the originals from
  `model.go`, `keys.go`, `pane.go` and `detail.go`.
- Keep in `pane.go` the pure helpers that have no receiver: `builderStatusStyle`,
  `spread`, `diffStat`, `colourDiff`, `colourTranscript`, `emptyPaneBlock`.
- Depends on nothing.
- Verify: the package does not compile yet, which is expected until 1.2.

**1.2 Make `Model` hold the pane.**
- Deliverable in `internal/ui/model.go`:
  - Replace the fields `detail detailModel` (model.go:36) and `tabInFlight bool`
    (model.go:33) with `pane roundPane`. `statusInFlight` stays.
  - Add `syncPane()`.
  - Add one wrapper per moved method (§5.1), keeping the old names and signatures
    (`pointDetailAt`, `pointDetailAtHist`, `fillViewport`, `stepRound`,
    `visibleTabFetch`, `cycleTab`, `switchTab`, `paneHead`, `histPaneHead`, `tabBar`,
    `sourceLine`, `hintLine`, `detailHeader`, `paneView`).
  - `Model.visibleTabFetch` keeps the `paneVisible()` guard.
  - `Model.maybeInvalidate` keeps its first guard (model.go:291-296) and the gone
    branch (297-307), and delegates the rest to `pane.invalidate()`.
  - The `tabMsg` arm becomes: `m.pane.tabInFlight = false`; if `!m.paneVisible()`
    return; `m.syncPane(); m.pane = m.pane.onTab(msg)`.
  - `m.paneView(width)` keeps the `m.empty()` branch and otherwise returns
    `m.pane.view(width)`.
  - Everything else in `model.go`, `keys.go`, `mouse.go`, `rail.go`, `list.go` and
    `detail.go` that reads `m.detail` / `m.tabInFlight` is rewritten by 1.3's script.
- Depends on 1.1.

**1.3 Run the mechanical rename over the remaining code and the tests.** Run this
once:

```
cd internal/ui && perl -pi -e 's/\b(m|updated|low|high)\.detail\b/$1.pane.detail/g; s/\b(m|updated)\.tabInFlight\b/$1.pane.tabInFlight/g' $(ls *.go | grep -v '^round_pane\.go$')
```

It rewrites 418 `m.detail`, 40 `m.tabInFlight`, 13 `updated.detail`, 2
`updated.tabInFlight`, 3 `low.detail` and 3 `high.detail` (the counts on
ff04f5d). Then fix whatever still does not compile by hand. That should only be where
a wrapper's own body now reads `m.pane.pane` or similar: undo the doubled prefix.

- Depends on 1.2.
- Verify: `go build ./internal/ui/...` succeeds.

**1.4 Prove nothing changed.**
- Run `go test ./internal/ui/...` without `-update`. Every test and every golden must
  pass as is. **No golden file may change and no assertion may be edited** in this
  round, other than 1.3's rename. If a golden differs, a moved body changed: find it,
  do not re-record.
- Mutation check: in `roundPane.onTab`, remove the `msg.round != p.detail.round` guard.
  A stale-reply test in `model_test.go` must fail. Name it in the report, then revert.
- Then run `make check`.
- Depends on 1.3.

**1.5 Report.**
- List the moved methods with their new line ranges in `round_pane.go`.
- Give the final line counts of `model.go`, `pane.go`, `keys.go`, `detail.go` and
  `round_pane.go`.
- Say which test failed in the mutation check.
- Confirm `git diff --stat` touches only `internal/ui/*.go`.

### Round 2: the shell and the three views (a later send; do not start now)

The send for this round carries fresh line numbers. In outline:

- **2.1** `view.go`: §4.1.
- **2.2** `view_fleet.go`: §4.3, §5.4. `ago`, `whatAge`, `fit` and `spread` move here
  or to `frame.go`.
- **2.3** `view_round.go`: §4.5, §5.4.
- **2.4** `internal/ui/dash`: add `Embedded`; `render.go`'s `headerLine` is skipped and
  `gridHeight` subtracts one fewer row when it is set. Then `view_rounds.go`: §4.5,
  §5.4, `openRound`.
- **2.5** `cmdline.go`: §4.7, §6.
- **2.6** `frame.go` and `shell.go`: §4.2, §5.2, §5.3. `RunSource` builds the shell,
  without `tea.WithMouseCellMotion()` (X4). `prefs.go` is reduced (§4.6).
- **2.7** Delete `model.go`, `rail.go` (except what 2.2 moved), `list.go` (after
  moving `renderError`, `wrapLine` and `maxErrorLines` to `frame.go`), `keys.go`,
  `layout.go`, `mouse.go`, `scope.go`, and `Options.Here` (X1–X7).
- **2.8** Port the tests. Every deleted test cites an X item. Every test of surviving
  behaviour (§3) is ported to the shell or a view.
- **2.9** New tests: key routing (§5.2 rules 1–9), cmdline matching and each command,
  fleet columns and the narrow-drop order, the NEEDS YOU second line, windowing with
  two-line rows, `openRound` live / hist / not found, `:rounds` refused without a DB.

### Round 3: launching it, goldens, docs (a later send)

- **3.1** `cmd/relevo/main.go` `run()` (main.go:287-291): add a pure `bareArgs(stdinTTY,
  stdoutTTY bool) []string` that returns `[]string{"ui"}` when both are terminals and
  `nil` otherwise. When it returns non-nil, carry on as `relevo ui`, so the migrate
  guard applies as it does to `ui`. Otherwise print usage as today. Test `bareArgs`
  only.
- **3.2** `cmdUI` (main.go:2222-2275):
  - Drop `--dashboard` (X5).
  - Join the positional args. Strip one leading `:`, then set `Options.Start` (for
    example `relevo ui :rounds harness:agy` → `rounds harness:agy`).
  - Drop `Here` (X3).
  - Update the usage line (main.go:82).
- **3.3** Goldens: `fleet`, `fleet-narrow-80`, `round`, `round-archived`, `rounds`,
  `cmdline-open`, `help`, `error-before-load`, `empty`, at 140x40 plus 80x30 where
  named. Delete the old goldens (their screens are X1/X2/X7).
- **3.4** Docs:
  - `docs/design.md:413`: the ui is "a cockpit: read-only until B2".
  - The `ui` line in the usage text: `ui [:view [args]]  the cockpit: :fleet, :rounds,
    :round <binding> [N]`.
- **3.5** Run `make check` and `make e2e`, then report.
