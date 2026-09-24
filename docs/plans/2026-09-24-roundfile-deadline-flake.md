# TestRoundFileDeadline: the slow server waits for the client to give up

## 1. System Overview

`TestRoundFileDeadline` (`internal/remote/client/client_internal_test.go`,
lines 311-350) pins the per-attempt deadline on `Client.RoundFile`: with
`roundFileDeadline` shortened to 50 ms, a server slower than that must make
the call fail with `ErrUnreachable`. The test's server "is slow" by sleeping a
fixed 200 ms and then answering 200 OK.

It flakes on macOS CI ("RoundFile against a server slower than the deadline
succeeded", run 36000092722 attempt 1, test time 0.23 s). Root cause,
reproduced on Linux (26 failures in 150 runs) by freezing the test process
with SIGSTOP for 250 ms every 30 ms: a stall across the 50-200 ms window
expires both timers at once. `context.WithTimeout` cancels from a separate
goroutine, so the server's already-due reply can reach the client before
the cancel runs. The production code is correct. The test's server lets the
two timers race, and it should not.

The fix is test-only. The handler no longer answers on a timer. It blocks
until the client abandons the request (the request's context is done), and
it answers only after a 10 s fallback. A client that honours its deadline
can therefore never get a reply, however late its timer fires. A client
with the deadline removed (the mutation) still gets its reply after 10 s,
and the test fails, as the mutation note promises.

## 2. File Structure

```
internal/remote/client/client_internal_test.go   (edit: TestRoundFileDeadline only)
```

No other file changes. No production code changes.

## 3. Data Structures & Type Definitions

None new. Add one test-local constant inside `TestRoundFileDeadline` (or
as an unexported package-level test constant next to it):

- `slowServerFallback` -- `time.Duration`, value `10 * time.Second`. It is how
  long the handler waits for the client to give up before it answers anyway.
  It must be comfortably above the test's own `elapsed > 2*time.Second`
  bound, so a mutated client that waits for the reply fails on the
  "succeeded" assertion and never passes.

## 4. Interface Definitions & Component Contracts

The only contract that changes is the httptest handler inside the test:

- Precondition: a GET arrives.
- Behaviour: wait for whichever comes first:
  - the request's context is done (`r.Context().Done()`): the client
    cancelled or closed the connection. Return without writing anything.
  - `slowServerFallback` elapses: set `Content-Type: text/plain` and write
    `"report\n"`, exactly as today.
- Postcondition: the handler never writes a response before the client has
  had `slowServerFallback` to give up.

Everything else in the test stays exactly as it is: `testClient`,
`injectSleep`, the `roundFileDeadline = 50ms` override and its cleanup, the
`errors.Is(err, ErrUnreachable)` check, the `retryAttempts-1` backoff count,
and the `elapsed > 2*time.Second` check.

## 5. High-Level Pseudocode

```
handler(w, r):
    select:
        on r.Context().Done():  return            // client gave up: no reply
        on after(slowServerFallback): write text/plain "report\n"
```

Doc comment above the test (lines 311-317): keep the first paragraph. Change
the mutation paragraph to say that without the `WithTimeout` in `RoundFile`,
the call waits out the server's 10 s fallback and succeeds. Add one sentence
saying the server waits for the client to give up instead of sleeping a fixed
time, so a late-firing deadline timer on a stalled CI machine cannot let the
reply win the race (the macOS flake).

## 6. Error Handling Strategy

No change. The call still fails on each attempt with the deadline's
`ErrUnreachable` wrap, and `retry` exhausts `retryAttempts`.

## 7. Working Efficiently

- The change is one handler body and one doc-comment paragraph in one file.
  Make both in a single edit of lines 311-324.
- Focused test: `go test -count=20 -run '^TestRoundFileDeadline$' ./internal/remote/client/`
- Package: `go test ./internal/remote/client/`
- Full check at the end: `make check` (it adds gofmt, go vet and the
  go mod tidy check). If the environment blocks `make check`, run
  `gofmt -l ./internal/remote/client/ && go vet ./internal/remote/client/ && go test ./...`
  and say so in the report.
- This test starts only an in-process httptest server. It spawns no harness
  and reaches no network, so it is safe on CI runners.

## 8. Ordered Implementation Steps

1. **Rewrite the handler and the doc comment** in
   `internal/remote/client/client_internal_test.go`, `TestRoundFileDeadline`
   (comment lines 311-317, handler lines 320-324), as in sections 3-5.
   Verify: the focused test passes 20 times in a row.
2. **Mutation check.** Temporarily delete the
   `context.WithTimeout(ctx, roundFileDeadline)` in `Client.RoundFile`
   (`internal/remote/client/client.go`, around line 496) and pass `ctx`
   through, so the call has no per-attempt deadline. Run the focused test
   once with `-count=1`. It must FAIL with "succeeded" or with the elapsed
   bound, after about 10 s. Then restore `client.go` exactly
   (`git diff internal/remote/client/client.go` must be empty) and put the
   result in the report.
3. **Full check** as in section 7. `git diff --stat` must list only
   `internal/remote/client/client_internal_test.go`.

If any step is impossible as written or contradicts the code (for example,
the test is not at those lines or the handler is not the fixed sleep
described), stop and report. Do not improvise.
