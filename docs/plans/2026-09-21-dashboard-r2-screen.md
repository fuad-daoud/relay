# Dashboard round 2: the `d` screen in `relay ui` -- tiles, grid, regroup, query line, jump to detail

Spec: `docs/specs/2026-09-21-dashboard-design.md` §6 (screen), §7
(errors), §8 (tests). Depends on round 1 (`internal/histq`, the new
`db.RoundRow` columns) -- confirm `internal/histq/group.go` exists; if
not, halt and report.

This plan stands alone: everything you need is in this file and in the
tree. If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

## 1. System overview

`relay ui` shows the fleet (rail + detail). This round adds a second
screen: press `d` and the terminal shows a totals row, a grid of every
round matching a query line, optionally regrouped by an axis with sums,
sortable; `enter` on a round jumps back to the fleet screen pointed at that
binding and round. The data comes from `histq` over one `db.Query` per
refresh. The screen is its own model in `internal/ui/dash`, hosted by the
fleet `Model` the way `detailModel` is.

## 2. File structure

```
internal/ui/dash/model.go        Model, New(opts), Update, View; the screen state (§3)
internal/ui/dash/fetch.go        fetch(ctx, db, query) tea.Cmd -> rowsMsg | errMsg
internal/ui/dash/render.go       tiles line, header, group rows, round rows, width tiers
internal/ui/dash/input.go        the `/` textinput, parse, error line
internal/ui/dash/sort.go         sort keys per level
internal/ui/dash/*_test.go       + testdata/*.golden
internal/ui/model.go             screenDash; hosts dash.Model; routes keys/size/tick; jump message handling
internal/ui/keys.go              `d` (enter/leave), keys forwarded while on the dash screen
internal/ui/prefs.go             Prefs.Dashboard, Prefs.DashboardSort
internal/ui/ui.go                Options.Dashboard bool (start on the dash screen)
internal/ui/golden_test.go       + goldens for the dash screen via the host
cmd/relay/main.go                cmdUI --dashboard
README.md                        `### relay ui` gains a "Dashboard" subsection (keys, query line, groups)
```

`internal/ui/dash` imports `internal/db`, `internal/histq`, `internal/relay`
(for `Runtime` only), bubbletea, bubbles `textinput`/`viewport`, lipgloss.
It must not import `internal/ui` (the host imports it).

## 3. Data structures

```
// internal/ui/dash
type Model struct {
    db       *db.DB              // nil -> the host never enters the screen
    loc      *time.Location
    now      func() time.Time
    query    histq.Query         // the applied query
    text     string              // the applied query's text (prefs)
    input    textinput.Model     // the / editor
    editing  bool
    parseErr string              // "" when none
    rows     []db.RoundRow       // last good result after Apply
    groups   []histq.GroupRow    // when query.By != none
    tiles    histq.Tiles
    expanded map[string]bool     // group key -> expanded
    cursor   int                 // index into the visible lines (§render)
    sortKey  string; sortDesc bool
    fetching bool; fetchErr string
    lastFetch time.Time
    width, height int
}
type rowsMsg struct{ rows []db.RoundRow; at time.Time }
type errMsg struct{ err error }
// JumpMsg is what the host receives when enter is pressed on a round row.
type JumpMsg struct{ BindingName string; BindingID string; Round int; Live bool /* unknown here; host decides */ }

// host (internal/ui)
screenDash screen
Model.dash dash.Model
Options.Dashboard bool
Prefs.Dashboard string `json:"dashboard,omitempty"`; Prefs.DashboardSort string `json:"dashboard_sort,omitempty"`
```

Visible lines: when `By == none`, one line per row in `rows` (sorted).
When grouped: one line per group (sorted), and for an expanded group its
`Rows` (newest first, or by the round sort) indented beneath it. `cursor`
addresses this flattened list; `render` rebuilds it every View.

## 4. Interfaces

```
func New(d *db.DB, loc *time.Location, now func() time.Time, queryText, sortKey string) Model
func (m Model) Init() tea.Cmd                          // first fetch
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd)     // returns dash.Model, not tea.Model; the host wraps
func (m Model) View() string
func (m Model) QueryText() string                        // for prefs
func (m Model) SortKey() string
func (m *Model) SetSize(w, h int)
func (m Model) ShouldRefresh(now time.Time) bool         // true when lastFetch older than 10s and not fetching

keys (inside Update, when not editing):
    "/"        editing = true; input.SetValue(text); input.Focus()
    "b"        query.By = next axis (none->binding->repo->feature->builder->harness->provider->model->day->outcome->none); regroup, cursor 0
    "s"        next sort key for the level under the cursor; "S" flips direction
    "enter"    group line: toggle expanded; round line: emit JumpMsg (via a tea.Cmd returning it)
    "r"        fetch now
    up/down/k/j/pgup/pgdown/home/end  move cursor; the viewport follows
    "esc"/"d"  handled by the host (leave)
while editing:
    "enter"    Parse(input.Value()); ok -> query/text set, parseErr "", editing false, fetch; err -> parseErr = err.Error(), stay editing
    "esc"      editing false, parseErr ""
    else       input.Update
fetch(ctx, db, query):
    rows := db.Query(query.Filter); rows = query.Apply(rows); rowsMsg
on rowsMsg: rows, groups = Group(rows, By, loc), tiles = Tiles(rows), fetching false, lastFetch
on errMsg:  fetchErr = err.Error(), fetching false (rows kept)
```

Host wiring (`internal/ui`):
- `d` on the fleet screen: if `m.src.Base().DB == nil` -> notice `no database: ...` (reuse the text `a` shows) and stay; else `m.screen = screenDash`, `m.dash.SetSize(...)`, and `m.dash.Init()` if never fetched.
- On `screenDash`: `d` and `esc` (when not editing) -> `m.screen = screenList`; every other `tea.KeyMsg` is forwarded to `m.dash.Update`; `tea.WindowSizeMsg` updates both; on the fleet's `statusMsg` tick, if `m.dash.ShouldRefresh(now)` issue its fetch.
- `JumpMsg`: turn on scope `all` if the binding is not in the live report (the same path key `a` takes), select the row by name (live row if present, else the hist row), set `detail.round = msg.Round`, `m.screen = screenList` (or `screenDetail` in stack layout), invalidate caches, fetch the active tab. If the name is in neither list: notice `<name> is not in the fleet or the database`.
- Prefs: save `Dashboard`/`DashboardSort` whenever they change (same debounce/save path as `Scope`).
- `Options.Dashboard`: start with `m.screen = screenDash` after the first `statusMsg` (so the rail has rows to jump to); `cmdUI --dashboard` sets it.

## 5. Rendering (`render.go`)

```
line 1  " relay · dashboard   <query text or (all rounds)>   by:<axis>            / filter  b regroup  s sort  d fleet  r refresh"
        (right part dropped below 120 cols; query text truncated with … to fit)
line 2  tiles: "rounds N   cost $X.XX (U unknown)   tokens T   halted H · exited E   median Mm   bindings B · builders K"
        (fetchErr replaces this line in the error style; "fetching…" appended while fetching; parseErr shown on line 3 while editing)
line 3  while editing: "/ " + input.View(); else the column header
grid    round line:  "2026-09-20 22:01  persist       r5  claude/anthropic/sonnet             reported   +1  clean  pass   1.2M   $0.42   27m"
        columns: started (local, 16), binding (12, …), rN (3), builder (36, …), outcome (13, coloured), commits (+N or -), tree, gate, tokens (ShortTokens), cost ($ / ~$ / ?), duration (Nm or -)
        group line:  "▸ agy/antigravity/claude-sonnet-4-6    31   27   3   58   22.1M   $9.10   2026-09-20"   ("▾" when expanded), then its rounds indented two spaces
        cursor line: the rail's selected style; archived rounds: archivedStyle; outcome colours: halted/exited attention, open live, others normal
width tiers: < 140 drop gate; < 120 drop tree, commits; < 100 drop tokens; < 80 drop duration
```

Use the existing `internal/ui` styles by exporting the handful the dash
needs (`Styles` struct passed at `New`, or export the vars) rather than
duplicating colours -- pick whichever is smaller; say which in the report.

## 6. Error handling

Per spec §7: no db -> host notice, no screen change; query error -> line
2 error style, rows kept; parse error -> under the input, query kept;
jump to a name that vanished -> notice. `enter` on an empty grid is a
no-op. Nothing writes to the db.

## 7. Ordered implementation steps

### Task 1 -- `dash` model, fetch, keys (no rendering yet beyond a plain View)

**Files:** `internal/ui/dash/{model,fetch,input,sort}.go`, `model_test.go`.

**Tests** (db seeded with `internal/ingest.Ingest` over a copy of `internal/ingest/testdata/binding-three-rounds` plus a second copy renamed, so two bindings exist; or, simpler and preferred, a fake fetch: make `fetch` a field `fetchFn func(ctx, query) ([]db.RoundRow, error)` on the Model, defaulting to the db path, so tests hand it literal rows):
- `TestInitFetchesAndGroups`: rowsMsg with the r1 fixture rows and `by:builder` -> groups non-nil, tiles.Rounds right.
- `TestBCyclesAxes` (ten presses return to none), `TestEnterTogglesGroup`, `TestEnterOnRoundEmitsJump` (the cmd's message is a `JumpMsg` with the right name/round).
- `TestSlashEditApplyAndError`: `/`, type `harness:agy`, enter -> query applied and a fetch cmd; `/`, type `bogus:1`, enter -> parseErr set, query unchanged, no fetch.
- `TestSortCyclesAndFlips`, `TestCursorBoundsAndExpansion`.
- `TestShouldRefreshEvery10s`.

**Verify:** `go test ./internal/ui/dash/`.

### Task 2 -- rendering and goldens

**Files:** `internal/ui/dash/render.go`, `render_test.go`, `testdata/*.golden`.

**Goldens** (fixed now, fixed rows, width 160×40 and 100×30): `flat.golden`,
`grouped-builder.golden`, `grouped-expanded.golden`, `editing-parse-error.golden`,
`empty.golden`, `fetch-error.golden`, `narrow-100.golden`. Generate with the
same `-update` convention `internal/ui/golden_test.go` uses (read it first
and match it).

**Verify:** `go test ./internal/ui/dash/`; eyeball each golden once and say in the report that you did.

### Task 3 -- host wiring, prefs, `--dashboard`, README

**Files:** `internal/ui/{model,keys,prefs,ui}.go`, `golden_test.go` (+ `split-dash.golden` through the host: fleet model, press `d`, rowsMsg injected), `prefs_test.go`, `cmd/relay/main.go`, `README.md`.

**Tests**
- `TestKeyDEntersAndLeavesDash`, `TestKeyDWithoutDBNotices`.
- `TestJumpFromDashPointsDetail`: fleet with one live binding `persist` and a hist-only `oldapi`; a `JumpMsg{oldapi, round 2}` -> scope all, detail.name oldapi, detail.round 2, screen list/detail; a jump to `persist` round 1 keeps scope.
- `TestPrefsDashboardRoundTrip`.
- `TestOptionsDashboardStartsOnDash` (after the first statusMsg).

README: a "Dashboard" subsection under `### relay ui`: the `d` key, the
tiles, the query line with three examples (reuse round 1's), `b`/`s`/`enter`, `--dashboard`.

**Verify:** `go test ./internal/ui/... ./cmd/relay/...`. The interactive
check (`relay ui`, `d`, `/harness:agy`, `b` twice, `enter` on a round,
`[`) is the planner's, on the planner's machine; say you left it.

### Task 4 -- full check and commit

Run: `gofmt -l .` (nothing), `go vet ./...`, `go test -race -count=1 ./...`,
`go mod tidy && git diff --exit-code go.mod go.sum`, then `make check`.
Known: `scripts/plugin-build_test.sh` case 3 fails on a clone without tags
(#242) -- if that is the **only** failure and `git tag` prints nothing, say
so and treat the check as passed.

**Commit** (one for the round):
`feat(ui): dashboard screen -- every round ever, filtered by a query line, regrouped with sums, jump to detail (#172)`

## Report

Per task: what was done, the test names, which goldens you generated and
what each shows, the verify result. Then the commit sha and the `make
check` result. If any step was impossible as written, say which and stop
there.
