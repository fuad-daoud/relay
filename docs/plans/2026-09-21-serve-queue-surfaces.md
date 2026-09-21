# Served builders, part 2 of 3: the queue on every surface (#285)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. The
worktree has **no `origin`**: never fetch, pull or rebase. Never run `make
check` here; run the gate commands in §6 exactly as written. Every command
in the foreground; no sub-agents for edits. Do not touch any file outside
this worktree.

Part 1 (`docs/plans/2026-09-21-serve-queue-core.md`, already in this tree)
added the cap, `Binding.QueuedAt`, `remote.RoundQueued`,
`remote.BindingView.Queue`, `remote.WhoAmI.Builders` and the `queue` log
kind. Read that plan's §3 before starting: every type this part renders is
defined there. This part touches **no server-side admission logic**; it is
rendering and one client-side field. Vocabulary is **queued**, nowhere
"parked" or "pending".

## 1. System overview

A client sees a queued round today as the bare word `queued` in `relay
status` (part 1's tolerance). This part gives every surface the facts:

- `relay status` / `relay wait`: `queued (3/3 busy on contabo, 2 ahead, 4m)`.
- `relay servers`: `builders 2/3, 1 queued, scopes off` per server.
- `relay doctor`: the servers check shows the same line and warns when a
  queue-aware server reports `scopes: false` (part 3 makes that true on a
  configured box; until then the warning is correct: a restart there kills
  builders).
- `relay serve status` and `relay serve ui`: a header `builders 3/3,
  queued 2` and per-row `queued 2m (1 ahead)`.
- The client keeps the last queue facts on its binding
  (`Endpoint.RemoteQueue`) so `status` renders without a network call, and
  clears them on any other state.

## 2. Files

```
internal/store/types.go               Endpoint.RemoteQueue *QueueFacts; QueueFacts
internal/relay/remote.go              observeRemote: fill/clear RemoteQueue
internal/relay/remote_test.go         queued view -> RemoteQueue; running view -> nil
internal/relay/status.go              statusRow remote branch: queued text; helper queueText
internal/relay/status_test.go         the exact strings
internal/relay/remote.go              ServerProbe: Builders fields from WhoAmI; RenderServers line
internal/relay/remote_test.go         RenderServers with and without Builders
cmd/relay/doctor.go                   serverChecks: builders line; scopes warning
cmd/relay/doctor_test.go              pure check over a ServerProbe slice (no network, no herdr)
internal/serve/admin.go               AdminStatus/FlatStatus: Builders census + queued rows
internal/serve/admin_test.go          header and row text
cmd/relay/serve.go                    serve status prints the header line
internal/ui/source.go                 serverSource shows the header (only if FlatStatus feeds it a field; see §4.5)
docs/plans/2026-09-21-serve-queue-surfaces.md   copy of this plan (last step)
```

Before adding a field, grep the struct for one with the same meaning
(`RemoteStatus` exists and stays the state word; `RemoteQueue` is the
facts). If part 1 already added something this plan names, use it.

## 3. Data structures

```go
// internal/store/types.go, on Endpoint next to RemoteStatus
// RemoteQueue is what the server's last GET said while the round was
// queued (#285); nil in every other round state.
RemoteQueue *QueueFacts `json:"remote_queue,omitempty"`

type QueueFacts struct {
    Position int       `json:"position"` // 1-based
    Ahead    int       `json:"ahead"`
    Running  int       `json:"running"`
    Cap      int       `json:"cap"`
    Since    time.Time `json:"since"`
}

// internal/relay/remote.go, on ServerProbe
QueueAware bool   // WhoAmI.Features contains remote.FeatureQueue
Builders   *remote.BuildersView // nil when !QueueAware

// internal/serve/admin.go
AdminStatus gains: Builders remote.BuildersView   (Running, Queued, Cap; Scopes/Slice as the server reports them)
FlatStatus rows gain: Queued *remote.QueueView    (nil unless that row's round is queued)
```

## 4. Contracts

### 4.1 `observeRemote` (`internal/relay/remote.go`)

In the `remote.RoundQueued` case part 1 added: `b.Builder.RemoteQueue =
&store.QueueFacts{Position, Ahead, Running, Cap, Since}` copied from
`view.Queue` (nil view.Queue -> nil). In every other case set
`b.Builder.RemoteQueue = nil` before the case's existing handling. The
round-closed `catchUp` path must also leave it nil.

### 4.2 `statusRow` (`internal/relay/status.go`)

In the remote branch, when `b.Builder.RemoteStatus == string(remote.RoundQueued)`:

```
queueText(q *store.QueueFacts, server string, now time.Time) string
    q == nil            -> "queued"
    else                -> fmt.Sprintf("queued (%d/%d busy on %s, %d ahead, %s)",
                             q.Running, q.Cap, server, q.Ahead, shortAge(now.Sub(q.Since)))
```

`shortAge` is whatever helper the status package already uses for row
ages (`quiet age` from #279 -- grep `status.go` for the function that
renders durations like `4m`, `1h2m`); do not add a second formatter. The
row is **not** an attention row: it sorts and colours like an active
running row. `relay wait`'s periodic line, if it prints the builder
status, uses the same `BuilderStatus` and needs no change -- verify by
reading `wait.go`; if it formats its own text, route it through
`queueText`.

### 4.3 `relay servers` (`ProbeServers`, `RenderServers`)

`ProbeServers` sets `QueueAware` and `Builders` from `WhoAmI`. `RenderServers`
appends to an enrolled server's line: `builders %d/%d, %d queued, scopes %s`
with `on (<slice>)` when `Scopes && Slice != ""`, `on` when `Scopes`,
`off` otherwise. A server without `QueueAware` prints nothing extra (an
old server). Keep the current column shape; append after the tier words.

### 4.4 `relay doctor` (`cmd/relay/doctor.go`)

`serverChecks` already turns `[]ServerProbe` into rows. Each enrolled,
queue-aware server's row gains the same `builders ...` text. When
`QueueAware && !Builders.Scopes`, the row is a **warning** (whatever
`doctor.Check` level means "warn", not "fail") with `scopes unavailable on
<name>: a daemon restart kills its builders`. Test as a pure function over
a hand-built `[]relay.ServerProbe` -- CI runners have no herdr and no
network; this package's `TestMain` isolates config.

### 4.5 `relay serve status` / `serve ui` (`internal/serve/admin.go`, `cmd/relay/serve.go`)

`AdminStatus` fills `Builders` from the server's census (part 1's
`(*Server).census()` and `cap()`; if `AdminStatus` is a free function over
a root rather than a `*Server` method, add an unexported census-over-root
helper in `admit.go` that both share -- do not duplicate the walk). Per
binding row: when the round is queued, `Queued = &QueueView{...}` with the
position computed as `handleGetBinding` does. `RenderAdminStatus` prints
`builders <running>/<cap>, queued <n>` as the first line and, on a queued
row, `queued <age> (<ahead> ahead)` where the row shows the builder status
today. `FlatStatus` rows carry `Queued` so `internal/ui`'s `serverSource`
can render the same words in the status column; change `serverSource` only
if it formats the status column itself (read it first; if it just copies
a string from `FlatStatus`, put the words in that string in `FlatStatus`).

## 5. Tests

- `internal/relay/remote_test.go`: `TestObserveRemoteQueuedKeepsFacts`
  (queued view with Queue -> `RemoteQueue` set, `RemoteStatus == "queued"`,
  no halt), `TestObserveRemoteRunningClearsQueue` (a binding with
  `RemoteQueue` set sees a running view -> nil). Build the views the way
  the existing observe tests do (fake `RemoteClient`).
- `internal/relay/status_test.go`: `TestStatusRowQueuedText` pins
  `queued (3/3 busy on contabo, 2 ahead, 4m)` and the nil-facts `queued`.
- `internal/relay/remote_test.go`: `TestRenderServersBuilders`: a
  queue-aware probe with `{2,1,3,false,""}` renders `builders 2/3, 1
  queued, scopes off`; with `{2,1,3,true,"relay.slice"}` renders `scopes
  on (relay.slice)`; a non-aware probe renders no `builders` word.
- `cmd/relay/doctor_test.go`: `TestServerChecksScopesWarning`: aware +
  `Scopes:false` -> warn row with the exact message; aware + true -> ok;
  unaware -> no builders text.
- `internal/serve/admin_test.go`: `TestAdminStatusBuildersHeader` (one
  running, one queued, cap 1 -> `builders 1/1, queued 1` and the queued
  row text). Use `setupTestEnv` and the part-1 `scriptRunner`.

Each test must fail when its rule is removed (the planner mutation-tests
`queueText`'s format string and the doctor warning condition).

## 6. Steps and gate

1. `store` field; `observeRemote`; tests.
2. `statusRow` + `queueText`; tests.
3. `ProbeServers`/`RenderServers`; tests.
4. `doctor`; test.
5. `admin.go` + `serve status` + `serverSource` if needed; test.
6. Gate:

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/ ./internal/serve/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Copy this plan to `docs/plans/2026-09-21-serve-queue-surfaces.md`, commit
as `chore(plans): serve queue surfaces`. Code commits `feat(status): ...`
/ `feat(serve): ...` per step naming #285.

## Report

Per step: test names, gate tail, commit shas; every exported identifier
added or changed with its signature; anything done that this plan did not
say, or where you halted.
