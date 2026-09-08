# `relay ui` — an interactive reader for live bindings

Status: design approved, not yet implemented.
Issue: [#2](https://github.com/fuad-daoud/relay/issues/2).
Date: 2026-09-08.

## 1. System overview

`relay ui` is a full-screen terminal reader for relay's state. It answers one
question — *what is happening right now* — without making the human leave the
screen to run `relay diff`, `relay log`, or scroll a builder's pane by hand.

It is a **pure consumer**. It calls `relay.Status`, `store.ReadLog`,
`relay.ReadDiff` and `herdr.ReadAgent`; it never writes state, never takes a
write lock, never types into a pane, and never appends to `log.jsonl`. That last
one is a rule, not an accident: a reader that logged would pollute the audit
trail that the round-quiescence fix exists to protect.

Because it only reads, it cannot corrupt a handoff, and it cannot race the
daemon's `Reconcile` critical section into anything worse than stale data.

### Relationship to `relay watch`

`relay watch` stays. It is `Status` on a ticker with a clear-screen redraw, it
works over a dumb pipe, and it is the right tool inside a script. `relay ui` is
the interactive sibling: same data, plus the substance behind it.

### Amendment to `docs/design.md`

`docs/design.md` lists "A TUI. `relay status` / `relay watch` are enough" under
**Out of scope (YAGNI)**. That line is superseded by this spec and must be
amended in the implementing change. The same list already lost "auto-worktree
creation for parallel loops" when `relay fork` shipped; the list records what was
not yet needed, not what is forbidden.

## 2. Settled decisions

| Decision | Choice | Why |
| --- | --- | --- |
| Purpose | Seeing more at once, not acting | The friction is that state is visible but substance is not. |
| Layout | Drill-down: fleet list, then full-width detail | A diff hunk and a terminal capture are natively 80+ columns. A side pane wraps both into mush. |
| Scope | Read-only in v1 | Cannot corrupt a handoff. Actions are a cheap follow-up once it is known which ones get reached for. |
| Toolkit | bubbletea + bubbles/viewport + lipgloss | A viewport, raw-mode key decoding, SIGWINCH and alt-screen handling is 600+ fiddly lines that is nobody's idea of relay's value. |

Taking bubbletea ends relay's zero-dependency property. Accepted deliberately.
All additions are pure Go, so the cross-compile CI job is unaffected. `go.sum`
auditing becomes a maintenance obligation the project did not previously have.

## 3. File structure

```
internal/ui/
  ui.go        Run() entry: TTY check, alt-screen, program wiring
  model.go     root Model: screen switch, ticker, single-flight, global keys
  list.go      list screen: rows from relay.Status, selection, rendering
  detail.go    detail screen: tab set, per-tab cache, invalidation
  fetch.go     tea.Cmd constructors + Msg types -- the ONLY file touching rt
  keys.go      keymap
  styles.go    lipgloss styles, including state colouring
  model_test.go / detail_test.go / fetch_test.go / fake_test.go
cmd/relay/main.go   + cmdUI, + a usage line
docs/design.md      amend the stale Out-of-scope line (section 1)
```

Confining every `rt` call to `fetch.go` is load-bearing: it keeps `Update` a pure
function of messages, which is what makes everything else testable without a
terminal.

`internal/ui` imports `internal/relay`, `internal/store` and `internal/herdr`.
Nothing imports `internal/ui` except `cmd/relay`. No existing package changes.

## 4. Data structures

### `Options`

```go
type Options struct {
    // Interval is the list poll period. Floored at 500ms, default 2s to match
    // the daemon tick and `relay watch`.
    Interval time.Duration
}
```

One field by design. The `ReadAgent` line count is always the viewport height,
so it is not a knob.

### Screens and tabs

```go
type screen int
const (
    screenList screen = iota
    screenDetail
)

type tab int
const (
    tabReport tab = iota
    tabTerminal
    tabDiff
    tabLog
    tabCount
)
```

### `tabContent`

```go
// tabContent is one tab's rendered body plus why it might be empty.
type tabContent struct {
    body   string // rendered content, ready for the viewport
    loaded bool   // false until the first fetch returns
    err    error  // fetch failure, scoped to this tab only
    empty  string // prose explaining expected emptiness; NOT an error
}
```

`err` and `empty` are distinct and must not be collapsed. `err` means the read
failed. `empty` means the read succeeded and there is legitimately nothing —
see section 7.

### Root model

```go
type Model struct {
    rt     relay.Runtime
    opts   Options
    screen screen

    report relay.Report // newest good Status snapshot; survives a failed refresh
    err    error        // last refresh error, shown in the footer

    // Two guards, not one: a terminal read is a 30s-timeout herdr call, and a
    // single shared guard would let one slow ReadAgent stall every list
    // refresh behind it, freezing the fleet view for half a minute.
    statusInFlight bool // cleared by statusMsg
    tabInFlight    bool // cleared by tabMsg

    list   listModel
    detail detailModel

    width, height int
}
```

### List model

```go
type listModel struct {
    cursor int    // index into Model.report.Bindings
    sticky string // binding name the cursor was on, to survive re-sorting
}
```

`relay.Status` sorts by name, and a binding can be added or removed between
polls. The cursor therefore tracks a **name**, not an index: after each
`statusMsg`, re-resolve `sticky` to an index, clamping if that binding is gone.

### Detail model

```go
type detailModel struct {
    name      string    // binding under inspection
    round     int       // newest completed round: binding.Round - 1
    active    tab
    vp        viewport.Model // the live viewport; only ever shows `active`
    scroll    [tabCount]int  // parked offsets for INACTIVE tabs
    cache     [tabCount]tabContent
    lastLogTS time.Time      // invalidation key -- see rule 3
}
```

`vp` and `scroll` are not redundant. `vp.YOffset` is the truth for the active
tab; `scroll` holds the parked offset of every other tab. On a tab switch, write
`vp.YOffset` into `scroll[old]`, re-fill `vp` with `cache[new].body`, then
restore `vp.YOffset` from `scroll[new]`. Keeping one viewport rather than four
means one resize path.

### Messages

```go
type tickMsg   time.Time
type statusMsg struct { report relay.Report; err error }
type tabMsg    struct { name string; round int; t tab; content tabContent }
```

`tabMsg` carries `name` and `round` so a late reply that no longer matches the
current selection is discarded rather than shown. `fetchReport` is not
round-keyed -- it reads the newest planner-bound report or question -- and
reports the round of the entry it actually read, so the same staleness check
covers it.

## 5. Interface contracts

### Entry point

```go
// Run renders relay's state until the user quits or ctx is cancelled.
// It never mutates state.
//
// Preconditions:  stdout is a TTY; rt.Herdr and rt.Store are non-nil.
// Postconditions: the terminal is restored, including on panic.
// Errors:         startup failures only (see section 8, category 1).
//                 Refresh failures never escape.
//
// rt.Git is never used: diffs are read from the stored patch file via
// relay.ReadDiff. The supported nil-Git configuration is therefore safe.
func Run(ctx context.Context, rt relay.Runtime, opts Options) error
```

### Fetch commands (`fetch.go`)

Each returns a `tea.Cmd` producing exactly one message. None mutate state. None
are ever called from `Update`'s body — they are returned from it.

```go
func fetchStatus(ctx context.Context, rt relay.Runtime) tea.Cmd
func fetchReport(ctx context.Context, rt relay.Runtime, name string) tea.Cmd
func fetchTerminal(ctx context.Context, rt relay.Runtime, name string, lines int) tea.Cmd
func fetchDiff(ctx context.Context, rt relay.Runtime, name string, round int) tea.Cmd
func fetchLog(ctx context.Context, rt relay.Runtime, name string) tea.Cmd
```

## 6. Refresh model

Three rules. They exist to keep a second poller from hurting the daemon.

### Rule 1 — single-flight, on two independent guards

One status refresh and one tab refresh in flight at a time, each guarded
separately. The ticker always re-arms; a fetch is issued only if its own guard
is clear.

The guards are separate because the terminal tab's `ReadAgent` is a 30s-timeout
call. Sharing one guard would let a slow terminal read stall the list poll
behind it, freezing the fleet view — and the fleet view is what warns you that
another binding needs attention while you read.

`relay.Status` takes the state lock twice per binding (`ReadLog` and
`PendingForPlanner`), and the daemon can hold that lock through `Reconcile`'s
worst-case ~80s critical section. Without single-flight, a slow lock stacks
goroutines that each wait up to `lockAcquireLimit` (90s) and then stampede.

bubbletea makes this natural: fetches are `tea.Cmd`s returning `tea.Msg`, so the
UI goroutine never blocks. A slow store shows **stale data plus a spinner, never
a frozen screen.**

```
on tickMsg:
    cmds = [tick()]                          // always re-arm
    if not m.statusInFlight:
        m.statusInFlight = true
        cmds += fetchStatus(...)
    if not m.tabInFlight:
        if c := visibleTabFetch(m); c != nil:
            m.tabInFlight = true
            cmds += c
    return m, batch(cmds)

on statusMsg:
    m.statusInFlight = false
    if msg.err != nil:
        m.err = msg.err                  // keep m.report: last good snapshot
        return m, nil
    m.err = nil
    m.report = msg.report
    m.list.resolveSticky()
    return m, maybeInvalidateTabs(m)
```

### Rule 2 — only the visible tab polls

The list poll runs always: it is the ambient awareness that lets the detail
header warn when *another* binding flips to NEEDS YOU while you are reading.

Tab content polling is separate and stops when the tab is off-screen. This
exists almost entirely for the terminal tab, whose `ReadAgent` is a 30s-timeout
herdr call and the only genuinely expensive read.

Rules 2 and 3 would contradict each other if left implicit -- rule 2 says the
visible tab is fetched each tick, rule 3 says file-backed tabs are not re-read
on a timer. `visibleTabFetch` is where they are reconciled, and it is the single
definition both rules defer to:

```
visibleTabFetch(m):
    if m.screen != screenDetail:            return nil
    t = m.detail.active
    if t == tabTerminal:                    return fetchTerminal(...)  // every tick
    if not m.detail.cache[t].loaded:        return fetch for t         // first view,
                                                                       // or after
                                                                       // invalidation
    return nil                                                         // cached; no I/O
```

So a file-backed tab is fetched exactly twice per round: once when first opened,
once when rule 3 clears `loaded`. The terminal tab is fetched every tick it is
on screen, and never when it is not.

### Rule 3 — file-backed tabs invalidate on log change, not on a timer

`report`, `diff` and `log` are files that only change at round boundaries.
`relay.Status` already returns `Last *LastEvent{TS,…}` per binding. When that
timestamp moves for the selected binding, drop the cached tab content and
re-read; otherwise keep it.

A 200 KB patch is therefore read once, not every two seconds, and **scroll
position survives a refresh** — the difference between a usable pager and an
infuriating one.

```
maybeInvalidateTabs(m):
    row = m.report.row(m.detail.name)
    if row == nil or row.Last == nil: return nil
    if row.Last.TS == m.detail.lastLogTS: return nil
    m.detail.lastLogTS = row.Last.TS
    m.detail.round     = row.Round - 1
    invalidate cache[tabReport], cache[tabDiff], cache[tabLog]
    return visibleTabFetch(m)            // refetch only what is on screen
```

The terminal tab is the deliberate exception: nothing in the log signals a
screen change, so it polls on the ticker while visible. That is the honest cost
of the one tab whose whole point is liveness.

## 7. Tab sources and empty cases

| Tab | Source | Empty case renders as |
| --- | --- | --- |
| report | `ReadLog` → newest `DirToPlanner` of kind report or question → `e.Payload` | `round 1 in flight; no report yet` |
| terminal | `FindAgent(agents, b.Builder)` → `ReadAgent(target, viewportHeight)` | ``builder gone (`agy`); pane wM:p4 no longer exists`` |
| diff | `relay.ReadDiff(rt, name, round)` | `no diff recorded for round N — no baseline captured` |
| log | `ReadLog`, rendered as `relay log` does | `no entries yet` |

The report tab reads `LogEntry.Payload`, **not** `LogEntry.Path`. Both are set on
report and question entries, but `Payload` is the right source for three reasons:
it is byte-for-byte what the planner receives, it already carries the appended
diff summary line, and it exists even when the file does not. That last one is
decisive — a report recovered by terminal scrape (`reconcile.go`, note
`"scraped"`) records a `Path` the builder never wrote. Reading `Path` would fail
on exactly the abandoned rounds a reader most needs to show.

**Expected emptiness is never rendered as an error.** The diff row is the one
that matters: diff capture is deliberately best-effort and has no error in its
signature, because a failed snapshot must never block a handoff. A round without
a patch is therefore normal operation, and a TUI that painted it red would train
the reader to distrust a working system.

The terminal tab uses `FindAgent`, whose pane-id fallback is correct here: this
is *locating* an agent for display, not asking whether a specific builder
session is still alive. It is not `builderAlive` and must not be confused with
it.

Only the newest completed round (`binding.Round - 1`) is shown, matching
`relay diff`'s own default. Round stepping is out of scope (section 10).

## 8. Error handling strategy

1. **Fatal, before the alt-screen** — herdr not on PATH, unreadable state root,
   stdout not a TTY. Returned from `Run` and printed like any other CLI error,
   so `relay ui > file` fails with a sentence instead of emitting escape codes.
2. **Transient refresh failure** — keep the last good `report`, show
   `! refresh failed: <err> (retrying)` in the footer. Mirrors the daemon, which
   logs and retries rather than dying on a herdr hiccup. Never fatal.
3. **Per-tab failure** — recorded in that `tabContent.err` only. The other three
   tabs stay readable.
4. **Binding vanishes mid-view** — `relay unbind` or `gc` from another process
   yields `store.ErrNotFound`. Pop to the list with a footer note. Not a crash,
   and not an error dialog.

Terminal restoration must survive a panic: `Run` defers restore before entering
the alt-screen. A panic that leaves a raw-mode terminal is worse than the panic.

**Observability: none.** The TUI emits no log entries, no hooks, and no herdr
writes. It is invisible to the audit trail by design.

## 9. Testing

`Update` is a pure `(Model, Msg) → (Model, Cmd)`, so behaviour tests need no
terminal.

- **single-flight** — a `tickMsg` while `statusInFlight` produces no second
  status fetch, and the same independently for `tabInFlight`; critically, a
  `tickMsg` while only `tabInFlight` is set still issues a status fetch
- **stale snapshot** — a `statusMsg` carrying an error leaves `m.report` intact
  and sets the footer
- **invalidation** — unchanged `LastEvent.TS` refetches nothing; a changed TS
  drops exactly the three file-backed caches and refetches only the visible tab
- **scroll preservation** — a refresh that does not invalidate leaves
  `detail.scroll[active]` unchanged
- **stale reply** — a `tabMsg` for a binding or round no longer selected is
  discarded
- **sticky cursor** — a `statusMsg` that removes the selected binding clamps the
  cursor without panicking
- **empty cases** — each of the four renders prose, and no `tabContent.err`
- **View golden tests** — list rows at a fixed width

Fakes: `internal/ui` needs its own minimal `relay.Herdr` fake implementing only
`ListAgents` and `ReadAgent`. The existing `fakeHerdr`/`fakeGit` are `_test.go`
files in `internal/relay` and cannot be imported; duplicating two methods is
cheaper than extracting a shared testing package.

Per house practice, every guard above is verified by reintroducing the defect and
confirming that *exactly* the intended test fails. A green suite written by the
agent that wrote the code proves nothing on its own.

## 10. Out of scope

- **Any action.** No answer, no done, no unbind, no send. Issue #2 lists `a` and
  `d`; both are deferred. Every action is a way to act behind the planner's
  back — answering a blocked builder from the TUI silently desyncs the planner's
  model of the loop. That is already possible via the CLI, but a keybinding makes
  it effortless rather than deliberate.
- **Round stepping** (`[` / `]`). The stated need is seeing what is happening,
  not walking history. Newest completed round only.
- **Removing or changing `relay watch`.** It stays, unmodified.
- **Filesystem watching (fsnotify).** Agent status can only come from polling
  herdr, so the ticker is needed regardless; watching files would remove none of
  it while adding a dependency.
- **Mouse support, themes, configurable keybindings.**
- **Multi-binding split views.** The drill-down layout exists precisely to give
  wide artifacts their full width.
