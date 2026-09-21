# Wave 2 chain J, step 3: subscribe to herdr's socket events -- pane status changes, exits and closes wake reconcile for that binding; files stay polled; CLI polling remains the fallback (#146)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo with no
herdr and no herdr socket. Never run `make check` or `make e2e` here (the
planner runs them; `make e2e` is mandatory for this change and the planner
will run it); run the gate commands in §7 exactly as written. Every
command in the foreground; no sub-agents for edits. No git fetch/rebase.

## 1. System overview

`relay daemon` runs `herdr agent list` (a process spawn + JSON parse)
every tick (`internal/relay/daemon.go:90`), so a blocked dialog is seen up
to `--interval` + ~600 ms late and the daemon's CPU is mostly herdr
spawns. herdr 0.9.1 (protocol 22) speaks newline-delimited JSON over the
session's unix socket (`$HERDR_SOCKET_PATH`, else
`~/.config/herdr/herdr.sock`): `events.subscribe` with per-pane
`pane.agent_status_changed` filters and session-wide `pane.exited`,
`pane.closed`, `pane.agent_detected`; the first reply acknowledges
(`{"type":"subscription_started"}`), later lines are pushed events
`{"event": <kind>, "data": {...}}`, no replay. After this round the daemon
opens one subscription for every planner and builder pane relay has
bound plus the three session-wide kinds; a status event reconciles **that
binding only**, at once, against an agent snapshot the daemon keeps
current from the events (status changes update it; exit/close remove;
`agent_detected` triggers one `ListAgents` refresh because a new pane
appeared); the file tick still reconciles every binding on `--interval`
but uses the cached snapshot instead of spawning `herdr agent list`. When
the socket cannot be opened (no `HERDR_SOCKET_PATH`, older herdr, permission
error, `relay serve`'s stub) the daemon logs once and behaves exactly as
today; when the stream drops it reconnects with backoff, re-snapshots, and
polls per tick in the gap -- never worse than today. Screen reads are
unchanged. Design questions 1 and 2: not in this round.

## 2. File structure

```
internal/herdr/socket.go           + Event, Subscription types; SocketPath(); (c *Client) Subscribe(ctx, paneIDs []string) (<-chan Event, error) -- dials the socket, sends events.subscribe, reads the ack, streams
internal/herdr/socket_test.go      + a fake unix-socket server (net.Listen("unix", filepath.Join(t.TempDir(), "s.sock"))) speaking the NDJSON protocol
internal/herdr/errors.go           + ErrNoSocket
internal/relay/herdr.go            Herdr + Subscribe(ctx, paneIDs []string) (<-chan herdr.Event, error)
internal/relay/fake_test.go        fakeHerdr + subscribe: scripted channel / error; subscribeCalls [][]string
internal/ui/fake_test.go, internal/pick/fake_test.go, internal/e2e/fakes_test.go, internal/serve/herdr.go   + Subscribe returning herdr.ErrNoSocket (stubs)
internal/relay/daemon.go           the event loop: subscribe/bootstrap, snapshot cache, per-binding wake, reconnect/backoff, fallback
internal/relay/daemon_events.go    + agentCache (apply events), boundPanes(bindings), backoff schedule -- pure helpers
internal/relay/daemon_test.go      + tests (§7)
internal/relay/daemon_events_test.go + pure tests
cmd/relay/main.go                  cmdDaemon: log line "events: socket <path>" / "events: unavailable (<why>); polling every <interval>"
README.md                          daemon section: events vs polling, the fallback, HERDR_SOCKET_PATH
docs/plans/2026-09-21-w2j3-socket-events.md   copy of this plan
```

Every implementer of `relay.Herdr` must compile:
`grep -rn "ListAgents(ctx context.Context) (\[\]herdr.Agent, error)\|ListAgents(_ context.Context)" internal/ --include=*.go` --
expected: `*herdr.Client`, `internal/relay/fake_test.go` fakeHerdr,
`internal/ui/fake_test.go`, `internal/pick/fake_test.go`,
`internal/e2e/fakes_test.go`, `internal/serve/herdr.go` stubHerdr. List
the grep in the report.

## 3. Data structures

```
// internal/herdr/socket.go
type Event struct {
    Kind        string // normalised: "pane_agent_status_changed" | "pane_exited" | "pane_closed" | "pane_agent_detected" | other (passed through)
    PaneID      string
    WorkspaceID string
    AgentStatus string // for status changes; "" otherwise
    Agent       string // optional harness name when herdr sends it
}
// wire: {"event": "<kind>", "data": {"pane_id", "workspace_id", "agent_status", "agent", ...}}; kind arrives either as "pane_agent_status_changed" or "pane.agent_status_changed" -- normalise '.' to '_'
var ErrNoSocket = errors.New("herdr socket unavailable")

// internal/relay/daemon_events.go
type agentCache struct { mu sync.Mutex; agents []herdr.Agent; at time.Time }
    // Snapshot() []herdr.Agent (copy); Replace(agents); Apply(ev herdr.Event) (touchedPane string, refresh bool)
    //   pane_agent_status_changed: set Status on the agent with that PaneID (absent -> refresh=true)
    //   pane_exited / pane_closed: remove the agent with that PaneID
    //   pane_agent_detected: refresh=true (a new pane; the snapshot is stale)
func boundPanes(bs []store.Binding) []string   // sorted, unique planner+builder PaneIDs of non-DONE, non-PAUSED, non-remote, non-headless-builder endpoints
func backoffAfter(attempt int) time.Duration    // 1s, 2s, 4s, ... capped at 30s
```

## 4. Interfaces

```
// internal/herdr/socket.go
func SocketPath() string   // $HERDR_SOCKET_PATH, else ~/.config/herdr/herdr.sock
func (c *Client) Subscribe(ctx context.Context, paneIDs []string) (<-chan Event, error)
    // conn, err := net.DialTimeout("unix", SocketPath(), 2s); err -> fmt.Errorf("%w: %v", ErrNoSocket, err)
    // write one line: {"id":"relay-sub-<n>","method":"events.subscribe","params":{"subscriptions":[ {"type":"pane.agent_status_changed","pane_id":P} for P in paneIDs, {"type":"pane.exited"}, {"type":"pane.closed"}, {"type":"pane.agent_detected"} ]}}
    // read one line (deadline 5s): a success_response whose result.type is "subscription_started" or "ok" -> proceed; an error_response -> fmt.Errorf("%w: subscribe: %s", ErrNoSocket, message); anything else -> ErrNoSocket
    // then a goroutine: bufio.Scanner (1 MiB lines) over conn; each line -> decode {"event", "data"} -> Event (unknown fields ignored, non-JSON lines skipped); send on an unbuffered chan; ctx.Done() or read error -> close(ch), conn.Close()
    // the CLI client is otherwise untouched: ListAgents stays the snapshot source

// internal/relay/daemon.go
func (d *Daemon) Run(ctx context.Context) error
    // ticker as today
    // events, err := d.subscribe(ctx)   // see below; on error: slog.Info("events unavailable; polling", "why", err) ONCE, events = nil
    // loop select:
    //   <-ctx.Done(): return
    //   <-ticker.C: d.Tick(ctx)   -- Tick uses d.cache when d.eventsLive, else ListAgents (today)
    //   ev, ok := <-events: !ok -> d.eventsLive = false; go d.reconnect(ctx) (backoff, resubscribe, on success: snapshot via ListAgents, d.cache.Replace, d.eventsLive = true, log "events resumed")
    //                        ok  -> pane, refresh := d.cache.Apply(ev); if refresh { agents, err := ListAgents; err == nil -> d.cache.Replace(agents) }
    //                               if name := d.bindingForPane(pane); name != "" { d.tickOne(ctx, name) }   // reconcile that binding only, with d.cache.Snapshot()
func (d *Daemon) subscribe(ctx) (<-chan herdr.Event, error)
    // bindings := Store.List(); panes := boundPanes(bindings); ch, err := d.rt.Herdr.Subscribe(ctx, panes); err -> return
    // BOOTSTRAP ORDER (documented by herdr): subscribe first, THEN snapshot: agents := ListAgents; d.cache.Replace(agents); d.subscribedPanes = panes; d.eventsLive = true
func (d *Daemon) Tick(ctx) error
    // as today, except: agents := d.cache.Snapshot() when d.eventsLive, else ListAgents (unchanged error handling)
    // after the per-binding loop: if d.eventsLive && !equal(boundPanes(fresh), d.subscribedPanes) { d.resubscribe(ctx) }   // a pane bound after startup joins on the next tick: close the old stream (cancel its ctx), open a new one with the full list, re-snapshot
func (d *Daemon) tickOne(ctx, name string) 
    // the per-binding body of Tick (WithLock, Load, Reconcile with d.cache.Snapshot(), Save) factored so both call it; then the metadata/notify/ingest steps stay tick-only
```

## 5. Pseudocode

Covered by §4. Concurrency: `Run` is single-goroutine except the socket
reader (writes to the channel) and `reconnect` (which only touches
`d.cache` under its mutex and sets `eventsLive` via an atomic/bool under
`d.mu`). `Tick` and `tickOne` are never concurrent: both run on `Run`'s
goroutine.

## 6. Error handling

- No socket / stub Herdr (serve, tests) -> `ErrNoSocket` -> polling, one
  log line. Every existing daemon and reconcile test passes unchanged in
  this mode (fakes return `ErrNoSocket` by default).
- Stream drop -> polling resumes on the next tick immediately (the cache
  is not trusted while `!eventsLive`), reconnect in the background with
  `backoffAfter`; each attempt logs at Debug, success at Info.
- A malformed event line is skipped; a status event for an unknown pane
  triggers one snapshot refresh (never an error).
- `ListAgents` failing during a refresh keeps the old cache and logs at
  Warn; the tick's own `ListAgents` error path is unchanged when polling.

## 7. Ordered implementation steps

Commit prefix: exactly **one** `feat(daemon):` commit for the code (squash
step commits, or `chore:`/`test:` per step); the plan copy may be its own
`chore(plans):` commit.

### Task 1 -- socket client

**Files:** `internal/herdr/socket.go`, `errors.go`, `socket_test.go`, `internal/relay/herdr.go`, the six implementers.

**Tests first** (`socket_test.go`, a goroutine `net.Listen("unix", …)` server that reads the request line, asserts its shape, writes the ack, then writes scripted event lines, then closes):
- `TestSubscribeSendsFiltersAndStreams`: paneIDs `[w1:p1 w1:p2]` -> the request JSON has two `pane.agent_status_changed` subscriptions with those pane ids plus the three session-wide kinds; after the ack, two pushed events (one dotted kind, one snake_case) arrive as `Event{Kind: "pane_agent_status_changed", PaneID, AgentStatus: "blocked"}`; a `pane_closed` arrives; a garbage line is skipped; server close -> channel closed.
- `TestSubscribeNoSocketIsErrNoSocket`: `HERDR_SOCKET_PATH` pointing at a missing file -> `errors.Is(err, ErrNoSocket)`.
- `TestSubscribeErrorReplyIsErrNoSocket`: server answers `{"id":..,"error":{"code":"unsupported","message":"no"}}`.
- `TestSocketPathPrecedence`: env set -> it; unset -> `~/.config/herdr/herdr.sock` (use `t.Setenv("HOME", …)`).
Then the interface method and the six stubs (`ErrNoSocket`). **Verify:** `go build ./... && go test -race -count=1 ./internal/herdr/ && go vet ./...`.

### Task 2 -- the daemon

**Files:** `internal/relay/daemon.go`, `daemon_events.go`, `daemon_events_test.go`, `daemon_test.go`, `fake_test.go`.

**Tests first**
- Pure (`daemon_events_test.go`): `TestAgentCacheApply` (status change updates; unknown pane -> refresh; exited/closed remove; detected -> refresh); `TestBoundPanes` (excludes DONE/PAUSED/remote/headless builders, dedups, sorted); `TestBackoffSchedule`.
- `TestDaemonEventWakesOneBinding` (`daemon_test.go`, with `fakeHerdr.subscribe` returning a channel the test writes to): two bound pane bindings; `Run` in a goroutine with a long interval (1 h); `ListAgents` called exactly once at bootstrap (`listCalls == 1`); send a `blocked` status event for binding A's builder pane -> within 1 s binding A is `needs_you`/blocked-handled (whatever `handleBlockedBuilder` does today: assert the observable the existing blocked test asserts) and binding B untouched; `listCalls` still 1. **Mutation check:** ignore the event channel in `Run` and this fails.
- `TestDaemonPaneClosedMarksBuilderMissing`: `pane_closed` for a builder pane -> that binding's `BuilderMissingSince` set on the next reconcile (the gone path).
- `TestDaemonDetectedRefreshesSnapshot`: `pane_agent_detected` -> `listCalls` becomes 2.
- `TestDaemonFallsBackToPollingWithoutSocket`: `subscribe` returns `ErrNoSocket` -> `Tick` calls `ListAgents` every tick as today (run three ticks with a short interval -> `listCalls == 3`); every existing daemon test passes unchanged.
- `TestDaemonReconnectsAfterStreamClose`: close the scripted channel -> the next tick polls (`listCalls` grows); `subscribeCalls` grows by one within the first backoff (use a 1 s-capped backoff hook: make `backoffAfter` a Daemon field with a test override) and `ListAgents` re-snapshots.
- `TestDaemonResubscribesWhenAPaneIsBound`: bind a new pane binding between ticks -> `subscribeCalls[len-1]` includes the new pane id.

**Then** the code per §4. **Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 3 -- cmd, README, plan copy, gate

`cmdDaemon`: one startup log line per §2 (the daemon knows after `subscribe`; log it from `Run` with the socket path from `herdr.SocketPath()`). README daemon section: events vs polling, the fallback, what `HERDR_SOCKET_PATH` does, that `relay serve` always polls (stub Herdr).

Copy the plan file to `docs/plans/2026-09-21-w2j3-socket-events.md` and commit.

Gate, in the foreground, stop at the first failure:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/herdr/ ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
`make e2e` (a real herdr session; the harness sets `HERDR_SOCKET_PATH`, so the daemon path under test WILL subscribe there) is the planner's; say so.

Final commit message: `feat(daemon): subscribe to herdr's socket events -- a pane status change, exit or close wakes reconcile for that binding; files stay polled; CLI polling is the fallback (#146)`

## Report

Per task: what was done, test names, verify output, the mutation check's
failing test, the implementer grep, the exact request line your client
sends (so the planner can compare it with `herdr api schema`). Commit
shas (one `feat:`). If any step was impossible as written, say which and
stop there.
