# Plan: invisible serve redeploys, round 1 of 2: the server side (#373)

**Read first:** `/home/fuad/projects/relay/docs/specs/2026-09-23-serve-redeploy-design.md`. Copy it into `docs/specs/` in your tree and commit it with this round. This round implements §4.1–4.3, plus the server half of §3 (the two feature tokens and logging `Relay-Client-Version`). Where this plan and the spec disagree, the spec wins. Stop and report rather than pick.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit.

**Parallel branch:** #372 edits `internal/store`, `internal/planner`, `internal/ledger`, `internal/policy`, `internal/candidate`, `internal/db` and `internal/relay/reconcile.go`. Don't touch those.

## 1. System overview

On SIGTERM, `relay serve` exits without waiting for in-flight requests or its tick. A tick killed between `Runner.Start` and `tx.Save` (in `relay.Admit`) leaves a live builder while the round still looks queued. A client retrying a start-round request for a round that is already open gets 409 `round_open` when running, or a re-queue when queued.

This round makes the server drain on SIGTERM and makes a repeated identical start-round request a no-op 200. The server advertises that with a feature token the client round (R2) will check. It also advertises `author`, sweeps stale request temp files at startup, and logs the client's version header.

## 2. File structure

```
docs/specs/2026-09-23-serve-redeploy-design.md  COPY
internal/serve/listen.go (+ listen_test.go)     drain (spec §4.1)
internal/serve/daemon.go (+ test)               Run: WithoutCancel per tick; return after the in-flight tick
internal/serve/rounds.go (+ serve_test.go)      identical-plan-for-open-round -> 200 (spec §4.2)
internal/serve/serve.go (+ test)                startup tmp sweep (spec §4.3) -- or a small new file sweep.go
internal/serve/routes.go                        advertise FeatureIdempotentSend and FeatureAuthor next to the existing features (routes.go:106)
internal/remote/proto.go                        the two Feature constants (next to FeatureTier… at 212-229)
internal/serve/<request logger>                 log Relay-Client-Version (find where the "http request" slog line is written)
```

## 3. Data structures

Two constants: `remote.FeatureIdempotentSend = "idempotent_send"` and `remote.FeatureAuthor = "author"`. No stored state changes.

## 4. Interfaces and contracts

- **`ListenAndServe` (listen.go:33-91):**
  - start `Run` in a goroutine tracked by a `sync.WaitGroup`, or a done channel;
  - on `<-ctx.Done()`: `srv.Shutdown` with a **30 s** timeout (today it is 5 s), then wait for `Run` to return, then return;
  - `ErrServerClosed` still maps to nil.
  - Keep every existing listen test green.
- **`Server.Run` (daemon.go:56-85):**
  - `s.Tick(context.WithoutCancel(ctx))`;
  - after each tick, `if ctx.Err() != nil { return nil }`.
- **`handleStartRound` (rounds.go):** add the branch from spec §4.2 **before** the `RoundStateOf == RoundRunning → 409` check at lines 98-103.
  - "Plan saved for `b.Round`" means the file the server wrote for that round's plan: find how the closed-round dedupe at 105-121 reads it, and reuse that exact read and sha comparison, factored into a helper.
  - "Queued" means `RoundStateOf == RoundQueued`.
  - The branch must return before any Send, any `QueuedAt` write, any log append and any absorb of the request's bundle. If absorb happens before this point in the handler, halt and report the order you found.
- **Startup sweep:** `func sweepTmp(dir string, olderThan time.Duration, now time.Time) (removed int, err error)`. It is pure apart from the filesystem, and is called from serve startup with `1*time.Hour`. It matches only the `req-body-*` and `plan-*` prefixes.
- **Client version header:** the constant `remote.HeaderClientVersion = "Relay-Client-Version"` goes in proto.go. The server's request log line gains `client_version=<value>` when the header is present, and nothing when it is absent. The server never rejects a request on it.

## 5. High-level pseudocode

```
handleStartRound:
  ... parse form (unchanged) ...
  st := RoundStateOf(b)
  if (st == RoundRunning || st == RoundQueued) && reqRound == b.Round && samePlan(saved(b.Round), reqPlan):
      return 200, ServedView(b)                 // idempotent retry
  if st == RoundRunning -> 409 round_open        (unchanged)
  ... unchanged ...
```

## 6. Error handling strategy

- A sweep error → Warn, and startup continues.
- A drain timeout → `Shutdown` returns its ctx error. Log it at Warn, still wait for `Run`, then return nil.
- Nothing in this round changes an HTTP status for any request that was not an identical retry.

## 7. Ordered implementation steps

1. Copy the spec. Add the proto constants, and advertise both features in WhoAmI. Test: WhoAmI's `Features` includes both, alongside the existing four.
2. `Run` and the drain. Tests:
   - a Server whose `Tick` blocks on a channel: cancel ctx, then release the tick → `Run` returns nil, and `Tick` saw a non-cancelled ctx;
   - `ListenAndServe` returns only after `Run` has returned. Use a hook or a counter; if `Tick` can't be faked without a large refactor, test through a narrow seam (e.g. an unexported `tickFn` field) and say so.

   Mutation: drop `WithoutCancel`, and the first test fails.
3. Idempotent send. `serve_test.go`, next to `TestRoundStartWhileRunningIs409` (:1856) and `TestRoundResendSamePlanIs200` (:2020):
   - running round, same round and same plan → 200, with the view's `Round` unchanged, no new plan log entry and the Runner's Start count unchanged;
   - queued round, same round and same plan → 200, `QueuedAt` unchanged;
   - running round, same round, **different** plan → 409 `round_open`;
   - `TestRoundStartWhileRunningIs409` must still pass unedited. If it sends the same plan for the same round (and so now gets 200), halt and report its request rather than edit it.

   Mutation: remove the `st == RoundQueued` clause, and the queued test fails.
4. Temp sweep: a `sweepTmp` test over a `t.TempDir()` holding an old `req-body-x`, a fresh `req-body-y`, an old `plan-z` and an old `other` → only `x` and `z` are removed.
5. Client version logging: a handler test with and without the header. Assert the log attribute if the package captures slog in tests; otherwise assert through whatever seam the request logger has, and say which.
6. Gate: `make check` and `make e2e`. Commit ending `(#373)`.

Declared scope: §2's files and their tests.
