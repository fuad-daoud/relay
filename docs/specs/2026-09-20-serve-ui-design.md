# `relay serve ui`: the fleet view on the server, grouped by client

Status: draft for review. Follows #245 (status degrades without herdr) and
#246 (admin verbs follow the daemon pointer).

## 1. Problem

`relay ui` is planner-side by design: it reads the planner's store at
`~/.local/state/relay` and asks herdr for pane state. On a serve box neither
exists in a useful form -- the bindings live under
`<serve root>/bindings/<owner>/`, one store per enrolled client, and there is
no herdr. The only server-side view is `relay serve status`, a text dump with
no round files, no diff, no log tail. An admin watching several clients'
headless builders has to `cat` paths out of that dump.

## 2. Goals and non-goals

Goals:

- One command, `relay serve ui`, that renders the same four panes the
  planner UI has (`report`, `terminal`, `diff`, `log`) over **every** owner's
  bindings, with each row visibly belonging to its client.
- Zero herdr. The server UI must start and refresh on a box that has never
  had a herdr socket.
- Planner-side `relay ui` is unchanged in behaviour and in JSON. Every row
  field this design adds is `omitempty` and empty for a planner.
- One UI codebase. The server view is a different **data source** for the
  existing model, not a second bubbletea program.

Non-goals (explicitly out of this design):

- Actions from the UI (`unbind`, `gc`, `unavailable`). Admin verbs stay on
  the CLI. The UI remains read-only, as `ui.Run`'s contract says.
- A planner column that means anything on the server. The planner is on
  another machine; its pane state is unknowable here and is not shown.
- Terminal capture of a pane builder. Server builders are headless; the
  terminal tab shows the round log tail, which is the existing headless
  path.
- Cross-owner sort. Rows sort **within** a client; clients sort by label.
- `relay ui` auto-detecting a server. The two verbs stay separate.

## 3. Command

```
relay serve ui [--state <dir>] [--interval 2s]
```

- Root resolution and refusal are exactly `adminRoot` from #246: follow the
  daemon pointer when no `--state` is given (the `using the running daemon's
  state` note prints to stderr before the alternate screen opens, so it is
  visible after quit), refuse an uninitialised root with the same message.
  The refusal happens before any terminal check, so `relay serve ui --state
  /nowhere` fails the same way in a pipe and in a tty.
- Needs a terminal, same refusal text as `relay ui` but naming
  `relay serve status` as the alternative.
- Preferences (`sort`, `compact`, `rail_cols`) persist in
  `<serve root>/ui.json`, the server analogue of `<state root>/ui.json`.
- Server config is `serveAdminConfig(root)` (#246), so headless liveness
  comes through `proc.New()`.

## 4. Data model

### 4.1 `relay.BindingStatus` gains two fields

| field | type | JSON | planner value | server value |
|---|---|---|---|---|
| `Owner` | `string` | `owner,omitempty` | `""` | the enrolled client id (`remote.ClientID`, the `SHA256:<base64>` key fingerprint; its `Dir()` form is the 64-hex bindings directory) |
| `OwnerLabel` | `string` | `owner_label,omitempty` | `""` | `Clients.LabelOf(id)`; when the label is empty, `shortID` (§7.1) |

Both empty means "planner row"; every renderer keys its new behaviour on
`OwnerLabel != ""` and nothing else.

### 4.2 Row identity: `BindingStatus.Key()`

```
Key() = Name                  when Owner == ""
      = Owner + "/" + Name    otherwise
```

Two clients may each have a binding named `persist`. The UI's cursor
memory (`listModel.sticky`), the detail pane's subject (`detailModel.name`),
the invalidation key and every "same binding?" comparison switch from `Name`
to `Key()`. On a planner `Key() == Name`, so nothing moves.

### 4.3 `relay.SortRows` sorts by owner first

`SortRows(rows, attention)` gains `OwnerLabel` as the primary key
(lexicographic, stable), then the existing attention-rank / name keys. With
every label empty the comparison is a no-op and the order is today's order.
This is what keeps a client's cards contiguous under its header without the
rail having to re-sort.

## 5. The `Source` seam (`internal/ui/source.go`)

The model today reaches into `relay.Runtime` for exactly five things:
`relay.Status`, `Store.Load`, `Store.ReadLog`, `Store.BuilderLogPath`, and the
herdr terminal read. All of them go behind:

```
type Source interface {
    // Status is one refresh: the whole fleet this UI shows.
    Status(ctx) (relay.Report, error)
    // Runtime resolves a row key to the runtime that owns it and the bare
    // binding name inside that runtime's store. ok is false when the key
    // cannot be resolved (server: malformed key or unknown owner); a
    // planner source resolves every key.
    Runtime(key string) (rt relay.Runtime, name string, ok bool)
}
```

- `plannerSource{rt}`: `Status` = `relay.Status(ctx, rt)`; `Runtime(key)` =
  `(rt, key, true)`. This is today's behaviour with no branch.
- `serverSource{srv *serve.Server}`: `Status` = `serve.FlatStatus(ctx, srv)`
  (§6); `Runtime(key)` splits at the first `/`, looks up the owner through
  `srv.OwnerRuntime(id)`, returns `(rt, name, true)`; a malformed key or an
  unknown owner is `ok == false`.

`fetchStatus`, `fetchReport`, `fetchTerminal`, `fetchDiff`, `fetchLog`,
`fetchFor` take `(src Source, key string)` instead of `(rt, name)` and begin
with `rt, name, ok := src.Runtime(key)`; `!ok` yields the tab's existing
"binding gone" empty-state text. The model stores `src Source` in place of
`rt relay.Runtime`.

Entry points:

- `ui.Run(ctx, rt, opts)` keeps its signature and contract; it wraps `rt`
  in `plannerSource` and calls `RunSource`.
- `ui.RunSource(ctx, src Source, opts Options)` is new and is what
  `relay serve ui` calls. The tty check and interval clamp live here.

## 6. Server-side flattening (`internal/serve`)

Two exported additions, both UI-free so `serve` never imports `ui`:

- `func FlatStatus(ctx, s *Server) (relay.Report, error)` -- runs
  `AdminStatus`, then for every owner and every row sets `Owner` and
  `OwnerLabel` (§4.1) and appends to one `Report.Bindings`. `Report.Gated`
  is taken from the first owner's report: the ledger is server-wide
  (`ledger.json` at the serve root), so every owner's `Gated` is the same
  slice and repeating it would double-count. `DoneHidden` is left zero: the
  UI applies its own DONE handling. `HerdrError` is always `""` because the
  server runtime's herdr is the stub.
- `func (s *Server) OwnerRuntime(id remote.ClientID) (relay.Runtime, error)` --
  the exported form of the existing `runtime(owner)`. Same error for a
  malformed id.

`AdminStatus` locks `s.mu`; the UI process is its own process, so this is
the per-process mutex `relay serve status` already takes, and cross-process
safety stays with the store's file locks. Nothing new.

## 7. Rendering

### 7.1 Rail: a header per client

`railLines` partitions `rows` (already owner-sorted by §4.3) into runs of
equal `OwnerLabel`. When every label is empty there is one run and no
header -- today's output. Otherwise, for each run:

```
 zen  (SHA256:VLERFMZnvN5H…)  3          <- header, binding: -1
 <the run's cards, exactly as railLines renders them today,
  including the per-state sub-headers when sort is "attention">
                                         <- one blank line, binding: -1
```

The header is `" " + label + "  (" + shortID + ")  " + n`, `fit` to width,
where `shortID` is `Owner` truncated to `SHA256:` plus its first 12 fingerprint characters and an ellipsis (`SHA256:VLERFMZnvN5H…`), in `faintStyle`, and
`n` the run's card count. `binding` indices in the emitted `railLine`s stay
global row indices, so `railSpan`, `railWindow` and the mouse map need no
change. The compact mode header is the same line; compact only changes the
cards.

### 7.2 Pane head: client line instead of planner line

`paneHead` renders `planner  <pane> <kind> <status>` today. When
`OwnerLabel != ""` that line is replaced by

```
client   zen  (SHA256:VLERFMZnvN5H…)
```

The builder line is unchanged (`builder  headless  claude  working  pid …`).
The planner is on another host and always reads `gone` on the server; showing
that would be noise that looks like a fault.

### 7.3 Everything else

Tabs, viewport, invalidation on `lastLogTS`, the footer, `s`/`c` keys,
mouse, prefs: unchanged. The terminal tab for a headless builder already
renders the round log tail and never reaches herdr; on the server every
builder is headless, and if a pane-mode binding ever appears (it cannot be
created there today) the stub herdr's empty agent list yields the existing
"builder gone" text, which is honest.

Empty fleet: the rail's zero-row prose is reused verbatim ("no bindings");
on the server that is also true.

## 8. Errors

| condition | where | behaviour |
|---|---|---|
| uninitialised or unresolvable root | `cmdServeUI`, before the tty check | exit 1 with `adminRoot`'s message |
| not a terminal | `RunSource` | exit 1: `relay serve ui needs a terminal; use relay serve status when piping` |
| `AdminStatus` fails (unreadable owner store) | `serverSource.Status` | refresh error; the model keeps the last good snapshot and shows `! refresh failed (retrying)` -- the existing path |
| key not found in `Runtime` | any tab fetch | the tab's "binding gone" empty text; no error |
| owner dir with no matching client record | `FlatStatus` | row rendered with label = short id; nothing hidden |

No new logging. The UI never mutates state, so there is nothing to record.

## 9. Testing

- `internal/relay`: `TestBindingStatusKey` (planner vs owner); `TestSortRowsOwnerFirst`
  (mixed labels: owner order wins over rank and name; all-empty labels:
  identical to today's order -- pin with a golden of the existing test's
  input).
- `internal/serve`: `TestFlatStatusStampsOwnersAndDedupsGates` -- two owners
  each with a binding named `persist`; rows carry distinct `Owner`, labels
  from `clients.json`, `Gated` appears once; `TestOwnerRuntimeMalformedID`.
- `internal/ui`: `TestRailLinesGroupsByOwner` (headers, blank separators,
  global binding indices, no header when labels are empty);
  `TestPaneHeadShowsClientLine`; `TestServerSourceRuntimeSplitsKey` with a
  fake `serve.Server` root under `t.TempDir()`; `TestStickyFollowsKeyAcrossOwners`
  (cursor on `b/persist` stays there when `a/persist` is inserted above).
- `cmd/relay`: `TestServeUIRefusesUninitialisedRoot` -- `cmdServeUI([]string{"--state", dir})`
  returns the `no serve state at` error and creates nothing. It exits before
  the tty check, so it passes on CI, which has no tty and no herdr.

Mutations worth running: drop the owner key from `SortRows` (rail test
fails on header order); make `Key()` return `Name` always (sticky test
fails); take `Gated` from every owner (dedup test fails).

## 10. Files

```
internal/relay/status.go        Owner, OwnerLabel, Key()
internal/relay/sort.go          owner-first key
internal/serve/admin.go         FlatStatus
internal/serve/serve.go         OwnerRuntime
internal/ui/source.go           Source, plannerSource, serverSource, RunSource   (new)
internal/ui/ui.go               Run delegates to RunSource
internal/ui/fetch.go            src/key plumbing
internal/ui/model.go            src field; Key() comparisons
internal/ui/list.go             sticky by Key()
internal/ui/detail.go           name -> key
internal/ui/rail.go             owner headers
internal/ui/pane.go             client line
cmd/relay/serve.go              cmdServeUI; usage line
+ tests as in §9
```

`cmd/relay/main.go` is not touched: `relay ui` keeps calling `ui.Run`.

## 11. Open questions settled here

- *Why not one composite store?* `store.Store` is concrete and pervasive;
  an interface extraction is a refactor of the `relay` package for a
  read-only view. The seam belongs in `ui`, which is the only consumer that
  needs to span owners.
- *Why owner-first sort instead of sorting inside the rail?* The model,
  cursor arithmetic and mouse map all index the sorted slice; grouping in
  the rail alone would desynchronise them. One sort, one order.
- *Why no planner column on the server?* It would always read `gone` or
  `unknown`. A column that is never informative is a column that trains the
  eye to ignore the row.
