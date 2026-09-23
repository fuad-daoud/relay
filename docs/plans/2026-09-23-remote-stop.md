# Plan: `relay stop` on a remote binding stops the round and keeps the binding (#344)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. In
particular, halt and report if either of these turns out to be false:

- `closeStopped` → `queueReport` on a **server-owned** binding (`b.Owner != ""`)
  writes a `KindDiff` entry and a `KindReport` entry for the stopped round and
  advances `b.Round`, just as it does locally. (Step 3 relies on it.)
- A round on a server-owned binding is only ever queued (`QueuedAt` non-zero)
  through `SendOptions.Defer`, which only `internal/serve/rounds.go` sets. A
  local binding never has a non-zero `QueuedAt`.

## 1. System Overview

Today a client can stop its own running remote round only with
`relay unbind <name>`. That kills the server's builder, but it also drops the
binding on both sides. `relay stop <name>` refuses a remote binding
(`internal/relay/stop.go` ~line 79). `relay done` gets a 409 while the round
runs.

This change adds a remote stop that keeps the binding, with four parts:

1. **A wire verb**, `POST /v1/bindings/{name}/stop`, owner-checked like its
   siblings. It runs `relay.Stop` in the owner's server runtime. The server
   advertises it with a new `FeatureStop` token in `WhoAmI.Features`.
2. **Server-side `relay.Stop` fixes** for owned bindings:
   - (a) After `closeStopped`, it runs `closeServedRound`, so `ServedView`
     reports `RoundClosed` with `ClosedRound` = the stopped round. Without
     this the client sees `idle` and its round stays open forever.
   - (b) A **queued** round (#285) is dropped from the queue and closed as
     stopped, with nothing to kill. Decision: **drop it, do not refuse it.**
     Refusing would leave the same dead end #318 describes.
3. **Wire view**: `BindingView` gains `Stopped string` (how the closed round
   was stopped, "" if it was not). `catchUp` uses it. A stopped round with no
   report file then queues the same "Builder was stopped …" payload that
   `closeStopped` writes locally. Today it **halts** the binding.
4. **Client `relay stop`** on a remote binding checks `FeatureStop`, calls the
   server, and then runs one observe pass in the same lock, so the round is
   usually closed locally before the command returns. The two hints that #339
   pointed at `unbind` now name `relay stop` first.

Out of scope: changing a binding's builder (#318). Clearing a `needs_you`
server binding by stopping it. Any MCP tool for stop. The server admin verbs
(`relay serve …`).

## 2. File Structure

```
internal/remote/proto.go             MODIFY  FeatureStop, CodeNothingToStop, BindingView.Stopped
internal/remote/client/client.go     MODIFY  (*Client).Stop
internal/relay/runtime.go            MODIFY  RemoteClient gains Stop
internal/relay/stop.go               MODIFY  stopDequeue action; served close; remote branch; stopPayload helper
internal/relay/served.go             MODIFY  ServedView fills Stopped
internal/relay/remote.go             MODIFY  catchUp: stopped close no longer halts
internal/relay/status.go             MODIFY  Done's two hint strings (~773, ~786)
internal/relay/text.go               MODIFY  StopText: "dequeued" case
internal/serve/routes.go             MODIFY  route + FeatureStop in WhoAmI
internal/serve/bindings.go           MODIFY  handleStop; handleDone queued hint (~270)
internal/relay/stop_test.go          MODIFY  decision table, dequeue, served close, remote client tests
internal/relay/remote_test.go        MODIFY  fake client Stop; catchUp stopped tests; updated Done hint string
internal/relay/served_test.go        MODIFY  ServedView.Stopped
internal/relay/status_test.go        MODIFY  queued-hint expectation (~594)
internal/serve/serve_test.go         MODIFY  wire tests (or admit_test.go for the queued one; say which)
internal/serve/admit_test.go         MODIFY  queued-hint expectation (~757)
README.md                            MODIFY  the `relay stop` section (~828): remote paragraph
```

Every other type that implements `relay.RemoteClient` (search the tests for
`Unbind(ctx context.Context, server, name string) error`) gets a `Stop`
method.

## 3. Data Structures & Type Definitions

### `internal/remote/proto.go`

- `const FeatureStop = "stop"`. The `WhoAmI.Features` token a server with
  `POST /v1/bindings/{name}/stop` advertises (#344). Doc comment in the style
  of `FeatureQueue`.
- `CodeNothingToStop Code = "nothing_to_stop"`. A 409 meaning the binding has
  no running or queued round. Add it to the `Code` const block with a
  one-line doc comment.
- `BindingView.Stopped string` with json tag `stopped,omitempty`. It is how
  the closed round (`ClosedRound`) was stopped: `"killed"` or `"dequeued"`.
  It is "" when that round closed any other way, on a pre-stop server, or
  when `ClosedRound` is 0. Place it after `ReportOutcome` with a doc comment.

### `internal/relay/stop.go`

- `stopAction` gains `stopDequeue`: a queued round with no process, dropped
  from the queue.
- `StopResult.Action` doc now reads
  `"killed" | "dequeued" | "nothing"`. No new fields.

### Exact strings (the contract)

| Where | Text (fmt verbs in order) |
|---|---|
| `stopPayload`, report on disk | `"Builder was stopped (%s) for round %d%s. Report: %s"` with `how, round, where, reportPath`. `where` is `""` locally, `" on <server>"` remotely |
| `stopPayload`, no report | `"Builder was stopped (%s) for round %d%s; no report was written."` with `how, round, where` |
| `stopPayload` note | `"stopped"` with a report, `"noreport stopped"` without |
| Stop, pre-stop server | `"server %s predates remote stop (no %q feature); relay unbind %s to stop the round and drop the binding"` with `server, remote.FeatureStop, name` |
| Stop, server 404 | `"%s no longer has binding %q; relay unbind %s to drop it here"` with `server, name, name` |
| Stop, server 409 `round_halted` | `"%s: round %d is halted there: %s"` with `server, b.Round, httpErr.Body.Message` |
| Done 409 running (status.go ~786) | `"round %d is running on %s; relay stop %s to stop it and keep the binding, or relay unbind %s to drop it"` with `b.Round, b.Builder.Server, b.Name, b.Name` |
| Done queued (status.go ~773) and wire `handleDone` queued (bindings.go ~270) | `"round %d is queued; relay stop to drop it from the queue, or unbind"` with `b.Round` |
| `StopText`, `"dequeued"` | `"%s round %d stopped: dropped from the server queue before it started; round closed without a report"` with `name, res.Round` |

With a local stop (`where == ""`), `stopPayload` must produce exactly the text
`closeStopped` produces today. Existing local stop tests must pass with no
change to their expected strings.

## 4. Interface Definitions & Component Contracts

### `relay.RemoteClient.Stop` (`internal/relay/runtime.go`)

```
Stop(ctx context.Context, server, name string) (remote.BindingView, error)
```

- Responsibility: ask the server to stop the binding's open round and return
  the binding's view after the stop.
- Errors: whatever `(*Client).do` returns. That covers `*client.HTTPError`
  (404, 409 with `Body.Code`, 500), `client.ErrUnreachable` and
  `client.ErrCertChanged`.

### `(*client.Client).Stop` (`internal/remote/client/client.go`)

Same signature. Model it on `(*Client).Resume`: a 30s timeout, then
`POST /v1/bindings/<PathEscape(name)>/stop` with no body. Decode the
`remote.BindingView` from a 200.

### `(*serve.Server).handleStop` (`internal/serve/bindings.go`)

Route: `mux.HandleFunc("POST /v1/bindings/{name}/stop", s.handleStop)` in
`routes.go`, beside `/done` and `/unbind`. Also change
`who.Features` (routes.go ~105) to
`[]string{remote.FeatureTier, remote.FeatureQueue, remote.FeatureStop}`.

- Precondition: none beyond auth. It takes `s.mu` like its siblings.
- Responses:
  - 404 `not_found`: the binding is not loadable, or `Allowed(caller, "stop", b)` is false. Same as `handleDone`.
  - 409 `nothing_to_stop`: `RoundStateOf` is `idle` or `closed`, or `relay.Stop` returns `ErrNothingToStop`.
  - 409 `round_halted`: `RoundStateOf` is `needs_you`. The message is `b.Halt`.
  - 500 with `err.Error()`: any other `relay.Stop` error.
  - 200 with `relay.ServedView(reloaded binding, reloaded entries)`: stopped. The tail is the same as `handleDone`'s.
- Postcondition on 200: the builder process is gone and the round is closed
  as stopped. `view.RoundState == closed`, `view.ClosedRound` is the stopped
  round, and `view.Stopped` is `"killed"` or `"dequeued"`. The binding still
  exists and is `active`.

### `relay.Stop` (`internal/relay/stop.go`), changed contract

Signature unchanged:
`Stop(ctx, rt Runtime, name string, opts StopOptions) (StopResult, error)`.

- **Local binding** (not `Builder.Remote()`): behaves as today, plus the
  `stopDequeue` branch and the served close (see pseudocode). A local binding
  never hits either, per the halt conditions above.
- **Remote binding**: the old refusal is gone. See pseudocode §5.2.
- Errors: `ErrNothingToStop` (the CLI prints "nothing to stop", exit 0),
  `ErrRemoteUnavailable`, the refusals in the strings table, and wrapped
  transport errors.

### `stopDecision` (pure), extended

`stopDecision(b store.Binding, now time.Time) stopAction`:

- `b.QueuedAt` non-zero → `stopDequeue`. Check this first: a queued round has
  a zero `RoundStartedAt`.
- `b.RoundStartedAt` zero → `stopNothing`.
- Otherwise → `stopKill`.

### `stopPayload` (pure, new, `stop.go`)

`stopPayload(how string, round int, where, reportPath string, haveReport bool) (payload, note string)`

Returns the strings in the table above. `closeStopped` uses it (with
`where == ""`, and `haveReport` from its existing `os.Stat`), and so does
`catchUp`.

### `ServedView` (`internal/relay/served.go`)

In the same loop that finds the report entry for `b.Serve.ClosedRound` (or a
sibling loop), find the newest `KindStop` entry with
`Round == b.Serve.ClosedRound`. If its `Note` has the prefix `"stopped/"`,
set `Stopped` to the rest of the note (`"killed"` or `"dequeued"`).
Otherwise leave it "". `closeStopped` already writes `Note: "stopped/"+how`.

## 5. High-Level Pseudocode

### 5.1 `relay.Stop`, local branch (inside the existing `WithLock`)

```
load b
if b is remote → §5.2
refuse Done / Paused as today
out.Round = b.Round
switch stopDecision(b, now):
  stopNothing → return ErrNothingToStop
  stopKill    → stopProcess(...) as today; b.Builder = clearProcess(b.Builder)
                how = "killed"
  stopDequeue → b.QueuedAt = zero        // nothing to kill; the server's queue
                how = "dequeued"          // is derived from QueuedAt, so this drops it
b = closeStopped(ctx, rt, tx, b, how)
if b.Owner != "":
    b = closeServedRound(ctx, rt, b)      // same order as markerClose: queueReport, then served close
b.StalledSince = zero
out.Action = how
tx.Save(b)
```

`closeStopped` changes only to build its payload and note through
`stopPayload(how, stoppedRound, "", reportPath, haveReport)`.

### 5.2 `relay.Stop`, remote branch (inside the same `WithLock`)

```
refuse Done / Paused (same messages as local)
if rt.Remote == nil → ErrRemoteUnavailable
who, err = rt.Remote.WhoAmI(ctx, server)
  err → return fmt.Errorf("%s: %w", server, err)
  FeatureStop not in who.Features → pre-stop-server refusal (strings table)
view, err = rt.Remote.Stop(ctx, server, name)
  HTTPError 409 code nothing_to_stop → return ErrNothingToStop
  HTTPError 409 code round_halted    → halted refusal (strings table)
  HTTPError 404                      → server-404 refusal (strings table)
  ErrUnreachable                     → fmt.Errorf("%s unreachable: %w", server, err)
  other                              → fmt.Errorf("%s: %w", server, err)
out.Round = b.Round
out.Action = view.Stopped, or "killed" if view.Stopped is ""
// Collect the close now instead of waiting for the next sync.
run observeRemote(ctx, rt, tx, b) and handle its result exactly as
SyncRemote handles it for one binding (save what it returns; do not deliver)
```

The server has already stopped the round. So if `catchUp` cannot finish
(bundle absorb blocked by a checked-out branch, a transient fetch failure),
Stop still returns success. The next sync, `relay pull` or `relay wait`
collects the round, as `catchUp` already retries.

### 5.3 `catchUp`, stopped close (`internal/relay/remote.go`)

```
fetch report for round n
  404 and view.Stopped == ""  → halt as today (unchanged)
  404 and view.Stopped != ""  → haveReport = false; write nothing; continue
  other error                 → warn, return b (unchanged)
  ok                          → write it; haveReport = true
fetch diff, log, stream, bundle; absorb; ack   (unchanged)
write the client diff entry from the view      (unchanged)
if view.Stopped != "":
    payload, note = stopPayload(view.Stopped, n, " on "+server, reportPath, haveReport)
else:
    payload = "Builder finished round %d on %s. Report: %s"   (unchanged)
    note = ""
append the Diff line to payload                (unchanged)
if view.DirtyCommit != "": note = joinNotes(note, "uncommitted work at refs/relay/<name>/round-<n>")
next = queueReport(..., payload, note, nil, u, view.Rusage)
if view.Stopped != "":
    append KindStop entry {Round: n, DirToPlanner, Note: "stopped/"+view.Stopped, Confirmed: true}
      // after queueReport, the same order closeStopped uses
mark idle (unchanged)
```

The dirty note currently *replaces* `note`. It must now be joined with
`joinNotes`, so that a stopped round with uncommitted work carries both.
When `view.Stopped == ""` the result is byte-identical to today's.

### 5.4 State transitions

```
server binding:  running --stop--> closed(stopped/killed)   --client ack--> idle
                 queued  --stop--> closed(stopped/dequeued) --client ack--> idle
                 idle/closed --stop--> 409 nothing_to_stop (no change)
                 needs_you   --stop--> 409 round_halted    (no change)
client binding:  round open --relay stop--> server 200 --observe--> round closed, payload pending
```

## 6. Error Handling Strategy

- **User answers, not failures**: `ErrNothingToStop`, locally or from a 409
  `nothing_to_stop`. The CLI keeps its "nothing to stop" line and exit 0. No
  CLI change is needed.
- **Refusals that change nothing**: done/paused, a pre-stop server, a 404, a
  halted round. They return an error before anything is written locally.
- **Partial success**: the server stop succeeded but the local catch-up did
  not finish. This is not an error. The round is closed on the server, and
  every existing sync path finishes it.
- **Server internal errors**: a 500 carrying `relay.Stop`'s message, the same
  as `handleUnbind`.
- **Logging**: no new log lines. `stopProcess` already writes its marker, and
  `closeStopped` writes the `KindStop` entry.

## 7. Ordered Implementation Steps

Run `make check` after each step. Every test is a pure-package test in
`internal/relay`, `internal/serve` or `internal/remote/client`. **Add no test
in `cmd/relay`.** CI runners have no harness binary and no network, and a
`cmd/relay` test must not run `relay stop` against anything.

1. **Wire vocabulary.** Add `FeatureStop`, `CodeNothingToStop` and
   `BindingView.Stopped` in `proto.go`. Add `Stop` to `relay.RemoteClient`,
   `(*client.Client).Stop`, and a `Stop` method on every fake client.
   *Verify:* `make check` is green, and a client test (next to the existing
   `Resume`/`Done` client tests, if there are any; otherwise skip it and say
   so) shows `Stop` POSTs to `/v1/bindings/<name>/stop`.

2. **`stopPayload` and `closeStopped` refactor.** Add the pure helper and
   route `closeStopped` through it.
   *Verify:* a table test for `stopPayload` covers both `haveReport` values
   and both `where` values. The existing local stop tests pass with
   **unchanged** expected strings.

3. **Server-side `relay.Stop`: dequeue and served close.** Extend
   `stopDecision`, add the `stopDequeue` branch, and add `closeServedRound`
   for owned bindings. Add the `"dequeued"` case to `StopText`.
   *Verify:* `TestStopDecisionTable` gains a queued row. A new test on an
   owned binding with an open running round sets up `b.Serve` and a bare
   repo, the way the `closeServedRound` tests do. After `Stop`,
   `b.Serve.ClosedRound` equals the stopped round and
   `RoundStateOf(...) == remote.RoundClosed`. A second test on a queued owned
   binding shows `Action == "dequeued"`, `QueuedAt` zero, no kill
   (`Runner.Kill` not called) and the round closed.

4. **`ServedView.Stopped`.**
   *Verify:* a `served_test.go` test gives a `KindStop` entry
   `"stopped/killed"` on `ClosedRound`, and the view has
   `Stopped == "killed"`. The same entry on an *earlier* round gives "".

5. **Wire handler.** Add the route, `handleStop` and the `FeatureStop`
   advertisement.
   *Verify:* use the existing `scriptRunner` setup in `serve_test.go`:
   - Stopping a running round returns 200. The script process is killed. The
     binding still exists and is `active`, with `round_state == closed`,
     `stopped == "killed"`, and `closed_round` = the stopped round.
   - Stopping again returns 409 `nothing_to_stop`.
   - Stopping a queued round (set up the way `TestUnbindDropsQueued` or
     similar does) returns 200 with `stopped == "dequeued"`. The server's
     queue census no longer lists it.
   - Another client's binding returns 404.
   - WhoAmI's features include `"stop"`.

6. **`catchUp` accepts a stopped close.**
   *Verify:* in `remote_test.go`, with the fake client:
   - (a) The view has `RoundState: closed, Stopped: "killed"` and a report
     404. After observe, the binding is **not** halted. A pending
     `KindReport` has a payload that starts
     `Builder was stopped (killed) for round N on <server>; no report was written.`
     and a note containing `noreport stopped`. A `KindStop` entry
     `stopped/killed` exists for round N.
   - (b) The same, but the report file exists: the payload contains
     `. Report: `, and the note contains `stopped` but not `noreport`.
   - (c) `Stopped: ""` with a report 404 still halts with the existing text
     (a regression guard).
   - (d) Stopped plus `DirtyCommit`: the note contains both `stopped` and
     `uncommitted work at`.

   *Mutation:* make the halt ignore `view.Stopped` and confirm (a) fails.

7. **Client remote `Stop`.** Replace the refusal with §5.2.
   *Verify:* replace `TestStopRemoteNamesUnbind` with:
   - (a) The fake has `FeatureStop`, `Stop` returns a closed/stopped view, and
     `GetBinding` returns the same view. `Stop` calls WhoAmI and then `Stop`
     (record the call order in the fake). It returns `Action "killed"`, and
     the binding's round is closed locally with the stopped payload pending
     and **not** delivered.
   - (b) The fake has no `FeatureStop`: the pre-stop refusal, and the fake's
     `Stop` is never called.
   - (c) 409 `nothing_to_stop` gives `errors.Is(err, ErrNothingToStop)`.
   - (d) 404 gives the server-404 text.
   - (e) `GetBinding` fails after a successful stop: `Stop` still returns nil
     error with `Action "killed"`, and the binding is not halted.

8. **Hints and README.** Update the Done strings (status.go ~773, ~786),
   `handleDone`'s queued message (bindings.go ~270), and the tests that pin
   them (remote_test.go ~1882, status_test.go ~594, admit_test.go ~757).
   Add a short paragraph to README's `relay stop` section (~828): a remote
   binding's round is stopped on its server, the binding stays, a queued
   round is dropped from the queue, and a server that predates this refuses
   with a pointer to `relay unbind`.
   *Verify:* `grep -rn "wait for it, or relay unbind" internal` returns
   nothing, and `make check` is green.

Report: the files touched, the decision you made wherever this plan said
"your call", and the mutation check from step 6 (what you broke and which
test failed).
