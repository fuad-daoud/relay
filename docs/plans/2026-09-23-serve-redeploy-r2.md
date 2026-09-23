# Plan: invisible serve redeploys, round 2 of 2: the client side (#373)

**Read first:** `docs/specs/2026-09-23-serve-redeploy-design.md` in your tree (committed in round 1), §3, §4.4, §4.5 and §4.6. Round 1 added `remote.FeatureIdempotentSend`, `remote.FeatureAuthor` and `remote.HeaderClientVersion`, and made the server idempotent. Where this plan and the spec disagree, the spec wins. Stop and report rather than pick.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit.

**Parallel branch:** #372 edits `internal/store`, `internal/planner`, `internal/ledger`, `internal/policy`, `internal/candidate`, `internal/db` and `internal/relay/reconcile.go`. Don't touch those. In `internal/relay`, you edit only `remote.go`, `runtime.go` and the new `authgrace.go` (plus tests).

## 1. System overview

During a server redeploy, the client currently:
- surfaces Cloudflare's 502 HTML page as the error;
- never retries;
- can lose a round's report forever when the reply to `/ack` is lost (the server then says Idle, and the Idle branch never catches up);
- halts every binding on a transient 401 such as clock skew after a reboot.

This round fixes all four and adds per-call deadlines to the two download calls that had none.

## 2. File structure

```
internal/remote/client/client.go (+ client_test.go)   §4.4: gateway statuses -> ErrUnreachable; retry[T]; retried methods; StartRound retry option; deadlines; header; client.Version var
internal/relay/authgrace.go (+ _test)                 NEW AuthGrace (spec §3)
internal/relay/runtime.go                             Runtime.AuthGrace *AuthGrace
internal/relay/remote.go (+ remote_test.go)           §4.5 Idle catch-up; StartRound retry flag from features; Author warning; §4.6 401 classification
cmd/relay/main.go                                     daemon: rt.AuthGrace = relay.NewAuthGrace(); set client.Version = buildVersion() once at startup (for every command)
```

## 3. Data structures

`relay.AuthGrace` and `authGraceLimit`, exactly as spec §3.

## 4. Interfaces and contracts

- **Gateway statuses and retry.** Spec §4.4, verbatim.
  - The retry helper's sleep must be injectable, so tests don't wait 7 s: a package-level `var sleep = func(ctx context.Context, d time.Duration) error`, which tests replace.
  - StartRound's retry reuses the spooled temp file. Seek to the start before each attempt, and remove the file once after the last attempt.
- **Idle catch-up, send retry, Author warning.** Spec §4.5, verbatim.
  - The Idle branch lives at `remote.go:743-747`.
  - The entries for `HasEntry` come from the same source the Closed branch and `remote.go:646-648` use.
  - The Author warning fires once per (process, server): use a `sync.Map` or a small set on the Runtime. Keep it simple.
- **401 classification.** Spec §4.6. The branch lives at `remote.go:629-636`.
  - Read the code from `httpErr.Body.Code`, or whatever field `remote.CodeOf`'s wire value arrives in: check `ErrorBody`.
  - A successful `GetBinding` clears the grace.

## 5. High-level pseudocode

```
observeRemote(...):
  view, err := GetBinding(...)        // now retried inside the client
  if httpErr 401:
     code := httpErr.Body.<code field>
     if code == "revoked" -> halt (unchanged text)
     first := rt.AuthGrace.Note(name, now)
     b.RemoteStatus = "auth: " + code
     if rt.AuthGrace.Expired(name, now, authGraceLimit) -> halt "<name>: <server>: <code> for <dur> -- check this machine's clock and relay servers"
     return b, false, nil
  ...
  on success: rt.AuthGrace.Clear(name)
  case RoundIdle:
     if view.ClosedRound >= b.Round && !HasEntry(entries, b.Round, DirToPlanner, KindReport):
         b, err = catchUp(ctx, rt, tx, b, view); return b, true, err
     (unchanged Broken -> Active handling)
```

`RemoteStatus` may not exist under that name. Use whatever field `ErrCertChanged` sets (`RemoteStatus="cert"`, per remote.go). Add **no** new field to `store.Binding`: #372 is adding a format guard to that struct in parallel.

## 6. Error handling strategy

- A retry that exhausts returns the last `ErrUnreachable`. Every caller's existing unreachable handling applies unchanged, including the 30-minute-past-budget halt.
- A transient 401 is visible in `RemoteStatus` and as a Warn on first sight (log once, when `Note` reports that `first == now`), and halts only after 15 min in the daemon. With a nil `AuthGrace` (CLI one-shots) it never halts.

## 7. Ordered implementation steps

1. **The client package (§4.4).** Tests with `httptest.Server`:
   - 502 with an HTML body → the error wraps `ErrUnreachable`, and its text has no `<html`;
   - 503 twice then 200 → GetBinding succeeds after 3 attempts (the injected sleep records 1 s and 2 s);
   - four 502s → `ErrUnreachable` after 4 attempts;
   - Done gets one attempt only on a 502;
   - StartRound with `retry: true`, a 502 then 200 → the second request carries the full plan and bundle bytes (assert they are equal); with `retry: false` → one attempt;
   - the header is present on every request;
   - RoundFile's deadline: the server sleeps past a test-shortened deadline. Make the deadline a package var if needed.

   Mutations:
   - drop the 5xx mapping → the 502 test fails;
   - drop the StartRound seek → the bundle-equality test fails.
2. **`AuthGrace`** and its tests: nil-safe; `Note` keeps the first time; `Expired` at the limit; `Clear`.
3. **`remote.go`** (§4.5, §4.6). Tests next to `TestReconcileRemote401Halts` (:2502) and `TestCatchUpOrderAndIdempotence` (:2814):
   - Idle view with `ClosedRound == b.Round` and no local report → catchUp runs: the report entry appears and Ack is called;
   - the same with the report already present → no catchUp (Ack not called);
   - 401 `revoked` → halt;
   - 401 `stale` with a nil AuthGrace → no halt;
   - 401 `stale` with an AuthGrace, first sight → no halt, and `RemoteStatus` is `auth: stale`; again with the clock 16 min later → halt;
   - a success between two stale polls → the grace resets;
   - `sendRemote` passes `retry=true` only when WhoAmI advertises `idempotent_send`.

   `TestReconcileRemote401Halts` encodes the old rule for whatever code it uses. If its code is not `revoked`, **update it to `revoked`** and add the stale test beside it. This one test edit is authorised by this plan. Report exactly what changed.

   Mutations:
   - drop the Idle catch-up → the first test fails;
   - make `stale` halt immediately → the grace test fails.
4. **Wiring in cmd/relay:** `client.Version = buildVersion()` early in `main` (before any command runs), and `rt.AuthGrace = relay.NewAuthGrace()` in the daemon next to `rt.Watched`. Add no cmd/relay test that reaches the network (CLAUDE.md).
5. **Gate:** `make check` and `make e2e`. Commit ending `(#373)`.

Declared scope: §2's files and their tests.
