# Plan: `relay serve log/show/tab` read an owner's bindings on the server (#216); delete the unused opencode.db usage reader

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. In
particular, halt and report if any of these turns out to be false:

- **Part A:** `relay.Show` (`internal/relay/show.go`) and `relay.FollowLog`
  (`internal/relay/logfollow.go`) need only `rt.Store` (and `rt.DB` for a
  non-live binding, which Part A never sets). They must not touch `rt.Git`,
  `rt.Runner`, `rt.Remote` or `rt.Candidates`. The admin CLI builds its
  `serve.Server` from `serveAdminConfig`, which has no Git and no candidates.
- **Part A:** `(*serve.Server).resolveOwner` (`internal/serve/admin.go`) is
  the one place a `--owner <label|id>` is resolved. Part A reuses it through
  one exported wrapper and writes no second resolver.
- **Part B:** nothing outside `_test.go` files calls `opencodeDB`,
  `OpencodeQuery`, `opencodeRows`, `opencodeRow` or `opencodeMessage`
  (`internal/usage/opencode.go`), and nothing reads the `reader` struct's
  `exec` or `home` fields (`internal/usage/source.go`). If anything does,
  the reader is not dead. Stop.

This round has two independent parts, with one commit per part, in order.
Another builder is working on #314 (CPU pinning) in parallel. It touches
`internal/proc`, the scope code in `internal/relay` (`headless.go`,
`runner.go`), the policy `scope` block and `internal/doctor`. Do not edit
those files.

## 1. System Overview

**Part A (#216, first bullet).** On a server, the admin can see every owner's
bindings with `relay serve status` and remove one with `relay serve unbind`.
Reading a served binding's log, a round's files or its spend means reading
JSON and tarballs by hand under `<serve root>/bindings/<owner dir>/`. The
remote-builders spec (§6.4) lists `relay serve log/show/tab --owner L <name>`
as the follow-up. This part adds the three **read-only** verbs. Each one
resolves the owner with the existing resolver, builds that owner's `Runtime`
(`(*Server).OwnerRuntime`), and renders with **the same text functions** the
client verbs use: `relay.LogLine`, `relay.Show`'s result rendering, and
`relay.RenderTab`. So the server's output reads exactly like a client's.

Decisions:

1. **`--owner` is required** for `serve log` and `serve show`. A binding name
   is unique only within one owner, so guessing is not allowed; this matches
   `serve unbind`.
2. **`serve tab` takes no binding name**, because `relay tab` takes none, and
   **`--owner` is optional**:
   - With `--owner`, it sums that owner's bindings. Rows are grouped by
     binding name.
   - Without it, it sums every owner. The binding group is
     `<label>/<name>`, and a new `--by owner` groups by owner label. This is
     the spec's "`relay tab --by owner` on the server", scoped to
     `serve tab`. The client `relay tab --by owner` is refused.
3. **Strictly read-only.** None of the verbs writes anything:
   - No `.viewed` stamp. The client verbs call `Store.MarkViewed`; the
     server verbs must not, because the viewed stamp is the owner's, not the
     admin's.
   - No database is created. `serve show` reads **live bindings only**. The
     client's fallback to `openDB` creates the file, so the server does not
     use it.
   - No store root is created for an unknown or empty owner. `Store.WithLock`
     runs `MkdirAll` on the root, so the verbs check with `os.Stat` that the
     owner's root exists before touching the store.
   - Locking is no more than the client verbs do: the same `Store.ReadLog…`
     calls, each taking the owner store's lock briefly.
4. `--follow` is supported on `serve log`, through the same `relay.FollowLog`.

**Part B (cleanup found in wave 1).** `usage.opencodeDB`, the reader that
shells out to `sqlite3` against `opencode.db`, has had no production caller
since #327 deleted `readPane`. Its query builder and row parser serve only
it, and the usage `reader` stores an `exec` and a `home` it never reads.
This part deletes the unused reader and the two unused fields, and drops
`usage.New`'s two parameters. `usage.Exec` **stays**: the opencode *delivery*
path (`internal/relay/deliver_opencode.go`, `cmd/relay/main.go`
`newDeliverers`) and doctor's session count (`internal/doctor/opencode.go`)
still use `sqlite3`.

## 2. File Structure

```
Part A
internal/serve/admin.go        MODIFY  AdminOwnerRuntime; AdminTabEntries
internal/serve/admin_test.go   MODIFY  owner-runtime and tab-entries tests
internal/relay/tab.go          MODIFY  TabEntry.Owner; "owner" grouping; TabEntries (extracted gather)
internal/relay/tab_test.go     MODIFY  owner grouping; TabEntries over a temp store
cmd/relay/main.go              MODIFY  cmdLog body moves into printLog (behaviour unchanged)
cmd/relay/show.go              MODIFY  cmdShow body after flag parsing moves into printShow (behaviour unchanged)
cmd/relay/tab.go               MODIFY  cmdTab uses relay.TabEntries and renderTabReport; refuses --by owner
cmd/relay/serve.go             MODIFY  dispatch + usage lines; cmdServeLog, cmdServeShow, cmdServeTab
cmd/relay/serve_test.go        MODIFY  usage/exit-2 tests only (pattern: TestServeUnbindWithoutOwnerExits2)
cmd/relay/read_verbs_test.go   CREATE  printLog/printShow with markViewed=false write nothing (temp store, no harness)
README.md                      MODIFY  the `relay serve` admin list (~line 633)

Part B
internal/usage/opencode.go       MODIFY  delete OpencodeQuery, opencodeDB, opencodeRow, opencodeMessage, opencodeRows (+ now-unused imports)
internal/usage/opencode_test.go  MODIFY  delete TestOpencodeQueryShape, TestOpencodeRows, TestOpencodeDBRunsSqlite3ReadOnly, TestOpencodeDBNotes and any fake used only by them
internal/usage/source.go         MODIFY  reader drops exec, home; New() takes no parameters
internal/usage/*_test.go         MODIFY  New(nil, dir) → New() at every call site
cmd/relay/usage_wire.go          MODIFY  newUsageReader stops looking up sqlite3; usagepkg.New()
```

`usage.Exec` (`internal/usage/exec.go`) is **kept**. Update its doc comment,
which currently says "Only sqlite3 goes through it", to name its remaining
user: the opencode deliverer's delivery confirmation.

## 3. Data Structures & Type Definitions

### `relay.TabEntry` (`internal/relay/tab.go`)

- New field `Owner string`: the owner's label when entries come from a server
  (`serve tab`). It is "" on a client.

### `relay.TabRows`

- `by` accepts `"owner"` as well; its key is `e.Owner`.
- `ErrBadBy`'s text becomes `--by wants binding, model, provider or owner`.

### Exact strings (the contract)

| Where | Text |
|---|---|
| `serve` usage block, new lines (after `status`) | `       relay serve log --owner <label\|id> <name> [--round N] [--after N] [--json] [--follow] [--state <dir>]` / `       relay serve show --owner <label\|id> <name> [--round N] [--plan\|--report\|--diff\|--drift\|--log\|--transcript] [--json] [--state <dir>]` / `       relay serve tab [--owner <label\|id>] [--since 7d] [--by binding\|model\|provider\|owner] [--json] [--state <dir>]` |
| `serve log`/`serve show` without `--owner` or name | that verb's usage line on stderr, exit 2, nothing on stdout |
| unknown owner | `relay serve <verb>: no such client: <owner>`, exit 1 |
| ambiguous label | `relay serve <verb>: label "<owner>" is ambiguous: <id>, <id>` (the resolver's existing text), exit 1 |
| binding not live under that owner (including an owner with no bindings dir) | `relay serve <verb>: <owner>/<name>: binding not found`, exit 1. For `show`, add `(serve show reads live bindings only)` |
| client `relay tab --by owner` | `"owner": --by owner is for relay serve tab`, exit 1 |
| `serve show` header on stderr | the client's header, prefixed with `<owner label>/`, e.g. `alice/api round 3 of 3 · report` |
| `serve log` lines | `relay.LogLine(e)` unchanged. `--json` prints the raw entry, as `relay log` does |
| `serve tab` body | `relay.RenderTab(rep)` unchanged. `--json` prints the `TabReport`, as `relay tab` does |

## 4. Interface Definitions & Component Contracts

### `serve.AdminOwnerRuntime` (`internal/serve/admin.go`)

`AdminOwnerRuntime(s *Server, owner string) (relay.Runtime, string, error)`

- Returns the owner's `Runtime` (via `s.OwnerRuntime`) and the owner's label
  (`s.clients.LabelOf(id)`).
- Resolves with `s.resolveOwner` while holding `s.mu`, the same way
  `AdminUnbind` does.
- Errors:
  - the resolver's own errors (unknown owner is `ErrNoSuchClient`; an
    ambiguous label names the ids);
  - `store.ErrNotFound` when the owner's bindings root does not exist
    (checked with `os.Stat`). Nothing is created.
- Postcondition: no file or directory is created.

### `serve.AdminTabEntries` (`internal/serve/admin.go`)

`AdminTabEntries(s *Server, owner string, cut time.Time, warn func(string)) ([]relay.TabEntry, error)`

- **`owner != ""`:** resolve with `AdminOwnerRuntime`, then
  `relay.TabEntries(rt, cut, warn)`, and set `Owner = label` on every entry.
  `Binding` stays the bare name.
- **`owner == ""`:** go through every owner directory, the same way
  `AdminStatus` does (`remote.IDFromDir`, skipping non-dirs). Build each
  owner's runtime with `s.OwnerRuntime`. Set `Owner = label` and
  `Binding = label + "/" + name`.
- Read-only. No store is created for an owner directory that already exists
  (`WithLock`'s `MkdirAll` is then a no-op).

### `relay.TabEntries` (`internal/relay/tab.go`, extracted from `cmdTab`)

`TabEntries(rt Runtime, cut time.Time, warn func(string)) ([]TabEntry, error)`

- This is today's gather loop in `cmd/relay/tab.go`, moved verbatim: live
  bindings' logs, then archives (skipping an archive older than `cut`), with
  an unreadable archive reported through `warn(<path>: <err>)` and skipped.
- `cmdTab` passes a `warn` that prints today's exact
  `relay tab: skip %s: %v` line.

### `cmd/relay` helpers (package main, unexported)

- `printLog(rt relay.Runtime, name string, round, after int, asJSON, follow, markViewed bool) error`
  - This is `cmdLog`'s body after flag parsing and `newRuntime()`, moved
    verbatim. Every `MarkViewed` call is guarded by `markViewed`.
  - `cmdLog` calls it with `markViewed=true`. Its output and exit behaviour
    are unchanged.
- `printShow(rt relay.Runtime, opts relay.ShowOptions, markViewed, allowDB bool, headerPrefix string) error`
  - This is `cmdShow`'s body after section resolution, moved verbatim.
  - `allowDB=false` means a non-live binding returns `store.ErrNotFound`
    instead of opening the database.
  - `headerPrefix` is prepended to the stderr header.
  - `cmdShow` calls it with `(…, true, true, "")`.
- `renderTabReport(entries []relay.TabEntry, by string, cut time.Time, asJSON bool) error`
  - `cmdTab`'s tail, from `TabRows` through printing, moved verbatim.

### New commands (`cmd/relay/serve.go`)

`cmdServeLog`, `cmdServeShow` and `cmdServeTab` follow `cmdServeUnbind`'s
shape: a `flag.FlagSet` with `--state`, then `adminRoot(fs)`, then
`serve.New(serveAdminConfig(root))`. `serveAdminConfig` needs no candidates.
Then:

- `serve log`: `AdminOwnerRuntime`, then `printLog(rt, name, …, markViewed=false)`.
- `serve show`: `AdminOwnerRuntime`, then `showSectionFlags`, then
  `printShow(rt, …, markViewed=false, allowDB=false, headerPrefix=label+"/")`.
- `serve tab`: `relay.ParseSince`, then `AdminTabEntries`, then
  `renderTabReport`.

Map `ErrNoSuchClient` and `store.ErrNotFound` to the strings in §3, with
exit 1.

## 5. High-Level Pseudocode

```
relay serve log --owner O NAME [flags]
  parse; missing O or NAME → usage, exit 2
  root = adminRoot; s = serve.New(serveAdminConfig(root))
  rt, label, err = serve.AdminOwnerRuntime(s, O)
    ErrNoSuchClient      → "relay serve log: no such client: O", exit 1
    ambiguous            → "relay serve log: <resolver text>", exit 1
    store.ErrNotFound    → "relay serve log: O/NAME: binding not found", exit 1
  printLog(rt, NAME, round, after, json, follow, markViewed=false)
    (store.ErrNotFound from its Load → same "O/NAME: binding not found")

relay serve show --owner O NAME [section] [flags]
  same resolution; printShow(..., markViewed=false, allowDB=false, headerPrefix=label+"/")

relay serve tab [--owner O] [--since S] [--by B] [--json]
  cut = ParseSince(S); entries = AdminTabEntries(s, O, cut, warn→stderr "relay serve tab: skip …")
  renderTabReport(entries, B, cut, json)
```

## 6. Error Handling Strategy

- **Usage errors:** exit 2 with the usage line on stderr and nothing on
  stdout, exactly like `serve unbind`.
- **Resolution and not-found errors:** exit 1 with one line on stderr.
- **Unreadable archives in `tab`:** a skip warning, not a failure, as today.
- **No logging additions.** Read verbs print nothing beyond their output.

## 7. Ordered Implementation Steps

Run `make check` after each step, and check gofmt explicitly with
`gofmt -l $(git ls-files '*.go')` (it must print nothing). Tests live in
`internal/serve`, `internal/relay` and `internal/usage`. Any `cmd/relay`
test must not spawn a harness or reach the network, and must use only a
temp store. The package's TestMain already points HOME and XDG at a temp
root (#235). A test that needs its own state sets `XDG_STATE_HOME` to a
`t.TempDir()`.

**Part A**

1. **Extract, with no behaviour change.** Add `relay.TabEntries`, and the
   `printLog`, `printShow` and `renderTabReport` helpers. `cmdLog`,
   `cmdShow` and `cmdTab` call them with today's behaviour
   (`markViewed=true`, `allowDB=true`, empty prefix).
   *Verify:* every existing `relay log`/`show`/`tab` test passes unchanged,
   and a new `internal/relay` `TabEntries` test over a temp store with one
   live and one archived binding returns both bindings' entries and honours
   `cut` for the archive.

2. **Owner grouping.** Add `TabEntry.Owner`, the `"owner"` key in
   `TabRows`/`tabKey`, the new `ErrBadBy` text, and the client refusal of
   `--by owner` in `cmdTab`.
   *Verify:* a `TabRows` test with entries from two owners groups by
   label, and an unknown `by` still wraps `ErrBadBy`.

3. **Server admin functions.** Add `AdminOwnerRuntime` and `AdminTabEntries`.
   *Verify*, in `admin_test.go`, reusing `TestAdminUnbindByLabelAndId`'s
   setup for two owners:
   - It resolves by label and by id.
   - An ambiguous label errors with both ids.
   - An unknown owner is `ErrNoSuchClient`.
   - An enrolled owner with no bindings dir is `store.ErrNotFound`, **and
     the dir still does not exist afterwards**.
   - `AdminTabEntries(s, "", …)` yields `label/name` bindings with `Owner`
     set for both owners.
   - With an owner, it yields bare names for that owner only.

4. **The three verbs.** Add dispatch, usage lines, `cmdServeLog`,
   `cmdServeShow` and `cmdServeTab`.
   *Verify:*
   - `serve_test.go` gains `TestServeLogWithoutOwnerExits2` and
     `TestServeShowWithoutOwnerExits2`, patterned exactly on
     `TestServeUnbindWithoutOwnerExits2`.
   - The new `read_verbs_test.go` builds a `relay.Runtime` over
     `store.New(t.TempDir())` holding one binding with a report round. It
     calls `printLog(…, markViewed=false)` and
     `printShow(…, markViewed=false, allowDB=false, "alice/")`. It asserts
     the output contains the expected `LogLine` and report text, that
     **no `.viewed` sidecar exists** (`Store.ViewedAt` false), and that
     `DBPath()` does not exist.
   - With `markViewed=true` the sidecar does exist: that is the control.
   - `printShow` with `allowDB=false` on an unknown name returns
     `store.ErrNotFound`, and no DB file appears.

   *Mutation (required):* make `printLog` ignore `markViewed` and always
   stamp. The new no-sidecar assertion must fail. Restore it.

5. **README.** In the `relay serve` admin list (~line 633), add three
   bullets for `serve log`, `serve show` and `serve tab`. Say that they are
   read-only, that `--owner` takes a label or id, and that `serve tab`
   without `--owner` sums every owner and supports `--by owner`.
   Commit Part A:
   `feat(serve): relay serve log/show/tab read an owner's bindings on the server (#216)`.

**Part B**

6. **Delete the unused reader.** Check the Part B halt condition first, with
   `grep -rn "opencodeDB\|OpencodeQuery\|opencodeRows\|opencodeMessage\|opencodeRow\b" --include='*.go' .`
   and a read of every use of `r.exec`/`r.home` in `internal/usage`. Then:
   - Delete the listed symbols and their tests.
   - Drop `reader.exec` and `reader.home`, and make `New()` parameterless.
   - Update every `New(nil, …)` call.
   - Make `newUsageReader` drop its `sqlite3` lookup.
   - Fix `usage.Exec`'s doc comment.
   - Keep `opencodeStream`, `opencodeTokens`, `opencodeEvent` (the headless
     stream reader) and `usage.Exec`.

   *Verify:* the grep above returns nothing, `make check` is green, and
   `internal/relay`'s deliverer tests still pass.

   *Mutation (required):* temporarily delete the `usage.Exec` type and
   confirm the build fails in `internal/relay/deliver_opencode.go`. That
   proves the type still has a live user and was rightly kept. Restore it.

   Commit:
   `refactor(usage): delete the opencode.db usage reader nothing has called since #327`.

## Report

For each part, report:
- the files touched per step;
- the halt-condition evidence (the greps and what `Show`/`FollowLog` touch);
- the mutation checks: what you broke and which test or build failed;
- any `cmd/relay` test you added, and why it needs no harness or network.
