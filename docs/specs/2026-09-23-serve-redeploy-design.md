# Invisible `relay serve` redeploys (#373)

Status: approved design, 2026-09-23. Implementation: `docs/plans/2026-09-23-serve-redeploy-r1.md` (server) and `-r2.md` (client).

## 1. System overview

Releases go out several times a day, and each one redeploys `relay serve` on contabo-01 (`srv.fish deploy relay-serve`). Verified live on 2026-09-23: served builders already run in their own `relay-round-*.scope` (`scopes=on (slice relay.slice)`), and `relay-serve.service`'s cgroup holds only the server process, so a restart doesn't kill them. #285 re-queues a round that is lost anyway.

What still breaks during a redeploy is the HTTP conversation and its bookkeeping:
- the server exits without waiting for in-flight requests or the tick;
- a lost reply to `/ack` loses a round's report forever;
- a retried send is refused (409) or re-queued;
- the client never retries, and a Cloudflare 502 page reaches the planner as the error text;
- a transient 401 (clock skew after a reboot) halts every binding.

This design makes a redeploy invisible to clients.

Out of scope:
- moving network I/O out of the client's store lock. This design adds deadlines only;
- persisting the nonce window. A restart makes a request captured in the last 5 minutes replayable, and requests are signed and TLS-fronted;
- cluster placement (#359);
- the deploy script in the servers repo, which is a separate plan (`2026-09-23-serve-deploy-gate.md`).

## 2. Components and files

```
R1 server
  internal/serve/listen.go        drain: wait for Shutdown and for Run's current tick before returning
  internal/serve/daemon.go        Run: each tick under context.WithoutCancel; returns after the in-flight tick on cancel
  internal/serve/rounds.go        handleStartRound: identical plan for the OPEN round (running or queued) -> 200 + view, no re-Send
  internal/serve/serve.go         startup sweep of <root>/tmp (req-body-*, plan-* older than 1h)
  internal/serve/auth.go (or the request logger)   log the Relay-Client-Version header when present
  internal/remote/proto.go        FeatureIdempotentSend = "idempotent_send"; FeatureAuthor = "author"; advertised by the server
R2 client
  internal/remote/client/client.go  5xx-gateway -> ErrUnreachable; retry with backoff for idempotent calls; per-call deadlines; Relay-Client-Version header
  internal/relay/remote.go          Idle-branch catch-up; 401 classification + daemon-only grace; StartRound retry only when the server has FeatureIdempotentSend
  internal/relay/runtime.go         Runtime.AuthGrace *AuthGrace (daemon only)
  internal/relay/authgrace.go       NEW AuthGrace
  cmd/relay/main.go                 daemon: rt.AuthGrace = relay.NewAuthGrace(); client version wiring
```

## 3. Data structures

- **`remote.FeatureIdempotentSend = "idempotent_send"`**: the server returns 200 plus the current view for a start-round request whose `round` equals the open round and whose plan sha256 equals the plan saved for that round.
- **`remote.FeatureAuthor = "author"`**: the server honours `CreateBindingRequest.Author` (#335). The client does not warn: servers since #335 honour the author without advertising `author`, so a missing token doesn't mean it is ignored; `author` is advertised from #373 on for future use.
- **`relay.AuthGrace`**:
  - fields: `mu sync.Mutex` and `since map[string]time.Time`, keyed by binding name;
  - methods:
    - `Note(name string, now time.Time) (first time.Time)`;
    - `Clear(name string)`;
    - `Expired(name string, now time.Time, limit time.Duration) bool`;
  - it is nil-safe. A nil `AuthGrace` never expires, so a CLI one-shot never halts on a transient 401.
- `const authGraceLimit = 15 * time.Minute`.
- **Header `Relay-Client-Version`**: `buildVersion()` of the client. It is informational, and never part of the signature.

## 4. Contracts

### 4.1 Server drain (R1)

`ListenAndServe(ctx, lc)`, when `ctx` is cancelled:
- it calls `srv.Shutdown` with a 30 s timeout;
- it waits for `Run` to return. `Run` finishes its in-flight tick under `WithoutCancel` and starts no new one;
- only then does it return.

A request handler that is mid-upload keeps its own context until `Shutdown`'s deadline. `Run` itself follows #371's `Daemon.Run` pattern: tick under `WithoutCancel`, and return `nil` after cancel.

### 4.2 Idempotent send (R1)

In `handleStartRound`, before today's "running → 409 `round_open`" check:

- If `RoundStateOf(b)` is `running` or `queued`, `reqRound == b.Round`, and the sha256 of the request plan equals that of the plan saved for `b.Round`: return 200 with the current `ServedView`. There is no `Send`, no `QueuedAt` reset, no new log entry, and the bundle is not absorbed a second time.
- A different plan for the open round → 409 `round_open`, as today.
- The existing closed-round dedupe (`reqRound == b.Round-1`) is unchanged.

### 4.3 Temp sweep (R1)

At `serve` start, before listening: remove `<root>/tmp/req-body-*` and `<root>/tmp/plan-*` whose mtime is older than 1 h. Errors are a Warn. The one-hour margin keeps the sweep from touching a file of a server that is still running on the same root (it won't be, because the daemon lock prevents that, but the margin costs nothing).

### 4.4 Client transport (R2)

- **`doRequest`**: statuses 502, 503, 504, 520–527 and 530 → `fmt.Errorf("%w: server returned %d %s", ErrUnreachable, status, http.StatusText(status))`. The body is never included. The status text is empty for the 52x codes, so fall back to `"gateway error"`.
- **`func retry[T any](ctx context.Context, attempts int, base time.Duration, f func(context.Context) (T, error)) (T, error)`**:
  - it retries only on `errors.Is(err, ErrUnreachable)`;
  - it sleeps `base·2^i`, 1 s, 2 s, then 4 s, honouring `ctx`;
  - it makes 4 attempts in total.
- **Retried:** WhoAmI, Candidates, GetBinding, RoundFile, RoundBundle, Ack, Available and Unavailable.
- **Never retried** (not idempotent): CreateBinding, Done, Unbind, Resume and Stop.
- **StartRound** is retried only when the caller passes `retry: true` (a new trailing parameter, or an options struct if the signature gets unwieldy). It re-reads its own spooled temp file on each attempt.
- **Per-attempt deadlines:**
  - RoundFile and RoundBundle: 2 min;
  - StartRound: `30s + size/256KiB·1s`, capped at 10 min;
  - every other call keeps its 30 s.
- Every request sets `Relay-Client-Version` from a package variable, `client.Version`, which cmd/relay sets at startup.

### 4.5 Client catch-up and send (R2)

- **The Idle branch of `observeRemote`:**
  - condition: `view.ClosedRound >= b.Round`, and no `KindReport` entry for `b.Round` (`HasEntry(entries, b.Round, DirToPlanner, KindReport)`);
  - action: `catchUp(ctx, rt, tx, b, view)`, the same call the Closed branch makes;
  - `Ack` is idempotent on the server, so a repeat ack is harmless.

  This recovers a report whose ack reached the server while the local bookkeeping didn't.
- **`sendRemote`:** it passes `retry: slices.Contains(who.Features, remote.FeatureIdempotentSend)`. A 200 for the open round is treated as success, exactly like a 201, and is recorded locally.
- **Author:** the client does not warn: servers since #335 honour the author without advertising `author`, so a missing token doesn't mean it is ignored; `author` is advertised from #373 on for future use.

### 4.6 401 classification (R2)

The error codes are `not_enrolled`, `revoked`, `bad_signature` and `stale` (`remote.CodeOf`).

- `revoked` → halt immediately, as today.
- `stale`, `bad_signature`, `not_enrolled` → transient:
  - `RemoteStatus = "auth: <code>"`;
  - `first := rt.AuthGrace.Note(name, now)`;
  - halt only when `rt.AuthGrace.Expired(name, now, authGraceLimit)`. The halt text names the code and how long it has persisted, and says "check this machine's clock and `relay servers`".
- Any successful poll → `rt.AuthGrace.Clear(name)`.
- 404 → unchanged, a halt.

## 5. Flow: a redeploy mid-round

```
srv deploy: SIGTERM -> server stops accepting; in-flight uploads finish (<=30s); tick finishes; exit
            builder scopes keep running; systemd starts the new binary
client daemon tick during the gap: GetBinding -> 502 (cloudflared) -> ErrUnreachable -> retried 1s,2s,4s -> maybe still down
            -> today's unreachable handling (Warn; halt only past round budget + 30 min)
client send during the gap: StartRound retried when the server advertised idempotent_send; the planner sees success or one "unreachable" error
server back: GetBinding ok -> Running (builder adopted, #370) -> ... Closed -> catchUp -> Ack
            reply lost? -> next poll Idle with ClosedRound >= b.Round and no local report -> catchUp again (Ack idempotent)
```

## 6. Errors and observability

- A retry exhausts → the same error as today (`ErrUnreachable`), with no new failure mode.
- A transient 401 → `RemoteStatus` shows `auth: stale`, plus a Warn on first sight. There is a halt only after 15 min, and only in the daemon.
- The server logs the client version on each request, so skew is visible in the journal.
- CI: every rule is a pure function or runs against `httptest` servers in `internal/remote/client` and the existing fakes in `internal/relay`. No harness, and no real network (CLAUDE.md).
