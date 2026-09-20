# Remote rounds carry their usage: the server measures, the wire ships it, the client keeps it (#216)

Issue #216, third bullet ("a client-facing usage view per owner"). Spec
context: `docs/specs/2026-09-19-remote-builders-design.md` (wire type
`BindingView`, catch-up §), `docs/specs/2026-09-20-persistence-design.md`
§7a (why this matters: `relay history` and `relay.db` read the report
entry's `Usage`).

This plan stands alone: everything you need is in this file and in the
tree. If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

## 1. System overview

A round run by a remote builder closes on the server; the client absorbs
the result (`internal/relay/remote.go` `catchUp`) and writes its own
`report` entry through `queueReport`, which calls `recordUsage` with the
client's reader -- and the harness record lives on the server, so every
remote round records `Usage{Cost.Basis: unknown, Note: "shared cwd"}`. Three
gaps, in order: the server's `relay.Runtime` (`internal/serve/serve.go`
`runtimeAt`) has no `Usage` reader and no `Prices`, so even the server's
own report entry says `no reader`; `remote.BindingView` has no usage field;
the client never looks for one. After this round the server measures the
round from the headless stream it already keeps (`NNN-builder.jsonl`), the
view carries the figure, and the client's report entry stores it verbatim
-- so `relay status`, `tab`, `history` and the database show a remote
round's tokens and cost like a local one's.

## 2. File structure

```
internal/serve/serve.go              Config gains Usage usage.Reader, Prices usage.Prices; runtimeAt passes both
internal/serve/serve_test.go         + TestRoundCloseRecordsStreamUsage
cmd/relay/usage_wire.go              newUsageReader(configDir) (usage.Reader, usage.Prices) -- factored out of newRuntime
cmd/relay/main.go                    newRuntime calls newUsageReader
cmd/relay/serve.go                   serve.Config{...} gets Usage and Prices from newUsageReader
internal/remote/proto.go             BindingView.Usage *usage.Usage `json:"usage,omitempty"`
internal/remote/proto_test.go        + round-trip test (create the file if there is none)
internal/relay/served.go             ServedView fills Usage from the closed round's report entry
internal/relay/served_test.go        + test
internal/relay/reconcile.go          queueReport gains `usage *usage.Usage`; nil = record locally
internal/relay/remote.go             catchUp passes view.Usage; nil -> local record whose note says the server sent none
internal/relay/usage.go              recordUsage: no change; + remoteNoUsage(rt, b, start, end) *usage.Usage helper
internal/relay/*_test.go             every queueReport caller in tests gains the nil argument; + TestCatchUpKeepsServerUsage, TestCatchUpPreUsageServerNotes
README.md                            `### Round usage` gains one paragraph on remote rounds
```

## 3. Data structures

```
// internal/remote/proto.go
BindingView.Usage *usage.Usage `json:"usage,omitempty"`
    // The closed round's usage as the server recorded it on its report entry
    // (usage.Usage is already JSON-tagged; it is the same struct store.LogEntry.Usage holds).
    // nil from a pre-usage server, or when the closed round has no report entry.

// internal/serve/serve.go
Config.Usage  usage.Reader   // nil = the server records "no reader", as today
Config.Prices usage.Prices   // zero value = embedded defaults via usage.Fold's rules
```

`internal/remote` must not import `internal/relay` (it does not today);
importing `internal/usage` is fine (`internal/usage` imports nothing of
relay's). Confirm with `go list -deps ./internal/remote | grep internal/`
before and after: the only addition is `internal/usage`.

## 4. Interfaces

```
// cmd/relay/usage_wire.go
func newUsageReader(configDir string) (usage.Reader, usage.Prices)
    exactly the block in cmd/relay/main.go newRuntime today (LoadPrices with the stderr
    fallback message, sqlite3 LookPath -> binExec{} else nil, os.UserHomeDir, usagepkg.New);
    newRuntime and cmdServeRun both call it.

// internal/relay/reconcile.go
func queueReport(ctx, rt, tx, b, entries, path, payload, note string, gate *store.GateRecord, usage *usage.Usage) (store.Binding, error)
    usage != nil -> the report entry's Usage is that pointer's value (copied), recordUsage is NOT called
    usage == nil -> unchanged: recordUsage(ctx, rt, roundSource(...))

// internal/relay/served.go
func ServedView(b store.Binding, entries []store.LogEntry) remote.BindingView
    + Usage: the newest entries[i] with Round == b.Serve.ClosedRound && Kind == KindReport -> entries[i].Usage (may be nil)
      found in the same loop that already finds reportOutcome

// internal/relay/usage.go
func remoteNoUsage(rt Runtime, b store.Binding, start, end time.Time) *usage.Usage
    usage.Fold(nil, rt.Prices, plan?, "remote: server sent no usage") with Harness from the candidate,
    DurationMS from start/end -- the shape recordUsage returns for an unreadable round, with an honest note.
```

## 5. Pseudocode

```
server, cmd/relay/serve.go cmdServeRun:
    reader, prices := newUsageReader(configDir)      -- configDir is what serveRoot/newRuntime already resolve; reuse
    cfg.Usage, cfg.Prices = reader, prices
serve.runtimeAt: Runtime{..., Usage: s.cfg.Usage, Prices: s.cfg.Prices}
    -> the server's own daemon closes a round through the shared queueReport(…, gate, nil) -> recordUsage ->
       roundSource: builder is headless on the server, so Mode headless + StreamPath = NNN-builder.jsonl -> readStream

client, remote.go catchUp step 5:
    var u *usage.Usage = view.Usage
    if u == nil: u = remoteNoUsage(rt, b, b.RoundStartedAt, rt.Now().UTC())
    next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, note, nil, u)

queueReport:
    entry.Usage = usage if usage != nil else recordUsage(ctx, rt, roundSource(rt, b, roundStart, now))
    (everything else in the function unchanged)
```

Every other `queueReport` caller (grep `queueReport(` in `internal/relay`,
including the pane/headless close paths and the tests) passes `nil` for the
new argument. Do not change what they record.

## 6. Error handling

- A server whose reader fails records what `recordUsage` records today
  (`unknown` with the reader's note) and ships that; the client keeps it
  verbatim. Nothing new can fail a round close.
- A pre-usage server (no `usage` in the JSON) -> `view.Usage == nil` ->
  the client's entry says `remote: server sent no usage`, basis unknown.
- `usage.Usage` on the wire is the same JSON `store.LogEntry` already
  persists, so no new decoding path; a malformed value fails the whole
  `BindingView` decode exactly like any other field today.

## 7. Ordered implementation steps

### Task 1 -- factor the reader wiring, wire the server

**Files:** `cmd/relay/usage_wire.go`, `cmd/relay/main.go`, `cmd/relay/serve.go`, `internal/serve/serve.go`, `internal/serve/serve_test.go`.

**Test:** `TestRoundCloseRecordsStreamUsage` in `internal/serve`: build on
`TestRoundCloseServesFilesBundleAck` (`serve_test.go` ~1667). Give the
server config a `usage.Reader` fake that returns two samples for a
`ModeHeadless` source whose `StreamPath` ends in `001-builder.jsonl`
(`internal/usage` has a fake/`Reader` interface -- read `usage_test.go`
and `source.go` for the shape; if there is no reusable fake, a two-method
struct in the test is fine) and `Prices` with a known rate. After the round
closes, read the server-side binding's log: the report entry's `Usage` has
the summed tokens and a `measured`/`estimated` basis, not `no reader`.

**Verify:** `go test ./internal/serve/ ./cmd/relay/`.

### Task 2 -- wire type and ServedView

**Files:** `internal/remote/proto.go`, `internal/remote/proto_test.go`, `internal/relay/served.go`, `internal/relay/served_test.go`.

**Tests**
- `TestBindingViewUsageRoundTrip` (`internal/remote`): marshal a view with `Usage` set, unmarshal, equal; a view without it has no `"usage"` key.
- `TestServedViewCarriesClosedRoundUsage` (`internal/relay`): entries with a report for `ClosedRound` carrying `Usage` -> `view.Usage` equal; an older round's report is ignored; no report -> nil.

**Verify:** `go list -deps ./internal/remote | grep 'relay/internal/'` prints `internal/usage` (and whatever it printed before) but never `internal/relay`; `go test ./internal/remote/ ./internal/relay/ -run 'BindingView|ServedView'`.

### Task 3 -- queueReport argument and the client's catch-up

**Files:** `internal/relay/reconcile.go`, `remote.go`, `usage.go`, every test file that calls `queueReport(`.

**Tests**
- `TestCatchUpKeepsServerUsage`: build on `TestCatchUpWritesDiffEntryFromView` (~1834): the fake remote's view has `Usage{Tokens{In: 1000, Out: 200}, Cost{USD: 0.12, Basis: measured}, Harness: "opencode"}`; after catch-up the client's report entry `Usage` equals it and its note does not contain `shared cwd`. **Mutation check:** pass `nil` instead of `view.Usage` in `catchUp` and this must fail.
- `TestCatchUpPreUsageServerNotes`: view without `Usage` -> report entry `Usage.Cost.Basis == unknown` and note `remote: server sent no usage`.
- Existing tests: unchanged behaviour with the `nil` argument -- `go test ./internal/relay/` must pass without editing any assertion other than adding the argument.

**Verify:** `go test -race ./internal/relay/`.

### Task 4 -- README, full check, commit

`README.md` `### Round usage`: add one paragraph after "Where you see it":
a remote builder's round is measured on the server from the builder's
stream and shipped with the round; a server built before this prints
`unknown · remote: server sent no usage`.

Run: `gofmt -l .` (nothing), `go vet ./...`, `go test -race -count=1 ./...`,
`go mod tidy && git diff --exit-code go.mod go.sum`, then `make check`.
Known: `scripts/plugin-build_test.sh` case 3 fails on a clone without tags
(#242) -- if that is the **only** failure and `git tag` prints nothing, say
so and treat the check as passed. `make e2e` needs herdr and is the
planner's to run (it exercises `queueReport`); say in the report that you
did not run it.

**Commit** (one for the round):
`feat(remote): the server measures a remote round's usage and ships it; the client keeps it (#216)`

## Report

Per task: what was done, the test names, the verify result, the mutation
check's outcome (name the failing test). Then the commit sha and the
`make check` result. If any step was impossible as written, say which and
stop there.
