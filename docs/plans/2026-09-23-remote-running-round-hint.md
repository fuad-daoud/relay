# Plan: a running remote round's hints name a verb that works (#331)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. In
particular, if step 3 or 4 shows that a plain `relay unbind` does **not**
stop a running remote round, halt there. That is a design error in this
plan, not something to work around.

## 1. System Overview

With a remote binding's round running:

- `relay done <name>` gets a 409 from the server and says
  `round N is running on <server>; wait or relay unbind --force`. The local
  `relay unbind` has no `--force` flag. Only the server-side admin verb
  `relay serve unbind --force` does.
- `relay stop <name>` refuses with
  `binding "<name>" is remote; relay done ends a remote round`, which is
  circular because done refuses a running round.

What actually works today, with no flag: local `relay.Unbind`
(`internal/relay/bind.go` ~line 698) calls `rt.Remote.Unbind`, which is
`POST /v1/bindings/{name}/unbind`. The server's `handleUnbind`
(`internal/serve/bindings.go` ~line 264) has **no** running-round guard. It
calls `relay.Unbind(ctx, rt, name, true)`, which kills the headless builder
(`stopProcess`) and archives the server's copy. The local record is then
deleted. Only the server's *admin* verb `AdminUnbind` has a running-round
guard with `--force`.

So this change corrects the two messages to name `relay unbind <name>`, and
adds tests that pin the behaviour the new messages promise. It adds no new
verbs, flags or wire endpoints. A non-destructive client-side stop of a
remote round (keeping the binding) is out of scope and will be filed
separately.

## 2. File Structure

```
internal/relay/status.go       MODIFY  Done's 409 message (~line 786)
internal/relay/stop.go         MODIFY  Stop's remote refusal (~line 79)
internal/relay/remote_test.go  MODIFY  TestDoneRemoteForwardsFirst's expected string; one new test
internal/relay/stop_test.go    MODIFY  (or whichever _test.go already holds Stop tests) one new test
internal/serve/bindings_test.go MODIFY or CREATE  one new wire test (put it beside TestUnbindDropsQueued in admit_test.go if that is the closer fit; your call, say which)
internal/serve/admin.go        MODIFY  AdminUnbind doc comment only
```

## 3. Data Structures & Type Definitions

None. No type, field, flag, endpoint or wire shape changes.

The two message strings are the contract:

| Where | Exact new text (fmt verbs in order) |
|-------|--------------------------------------|
| `Done`, 409 branch | `"round %d is running on %s; wait for it, or relay unbind %s to stop it and drop the binding"` with `b.Round, b.Builder.Server, b.Name` |
| `Stop`, remote refusal | `"binding %q is remote and relay stop cannot reach its round; wait for it, or relay unbind %s to stop it and drop the binding"` with `name, name` |

Neither message may contain `--force`.

## 4. Interface Definitions & Component Contracts

No signatures change.

- `relay.Done`: unchanged behaviour. Only the text of the error returned on
  a server 409 changes. The binding stays unchanged (state active), as
  `TestDoneRemoteForwardsFirst` already asserts.
- `relay.Stop`: unchanged behaviour. It still refuses a remote binding
  before any change, and only the text changes.
- `relay.Unbind` on a remote binding with an open round: behaviour
  unchanged, now pinned by a test (step 3). It calls `rt.Remote.Unbind`
  first, then deletes the local record, with no error, whether or not
  `RoundStartedAt` is set.
- Wire `POST /v1/bindings/{name}/unbind` on a running round: behaviour
  unchanged, now pinned by a test (step 4). It returns 200, the server's
  builder process is killed, and the binding is archived (gone from the
  owner's live store).
- `serve.AdminUnbind`: code unchanged. Its doc comment currently says the
  running-round refusal is "matching relay unbind's own guardedness". That
  is false, because neither the local unbind nor the wire unbind guards. Reword
  that sentence to: "A running round is refused unless force is set: this is
  the admin's guard against clearing a live client's work by mistake; the
  owning client's own unbind (the wire verb) needs no force."

## 5. High-Level Pseudocode

```
Done(remote binding, server replies 409):
    return error(fmt(DoneMsg, b.Round, b.Builder.Server, b.Name))   # binding untouched

Stop(remote binding):
    return error(fmt(StopMsg, name, name))                           # nothing changed
```

No other flow changes.

## 6. Error Handling Strategy

No new errors or categories. Both messages are user-facing refusals, and both
are recoverable: they tell the user to wait or unbind. Do not add logging.

## 7. Ordered Implementation Steps

### Step 1: the two messages

- Deliverable: the exact strings in §3 in `status.go` and `stop.go`.
- Depends on: nothing.
- Verify: `grep -rn "unbind --force" internal/ cmd/` finds nothing outside
  `cmd/relay/serve.go` (the admin verb's own usage) and tests of the
  admin verb. Report the grep output.

### Step 2: update the Done test, add a Stop test

- In `TestDoneRemoteForwardsFirst` (`internal/relay/remote_test.go` ~1799),
  change the expected substring to
  `"round 1 is running on zen; wait for it, or relay unbind api to stop it and drop the binding"`
  and also assert `!strings.Contains(err.Error(), "--force")`.
- New `TestStopRemoteNamesUnbind` in the file that already holds Stop tests.
  Find it with `grep -ln "func TestStop" internal/relay/`. Save
  `remoteBinding("zen")` (the helper in remote_test.go) with a non-zero
  `RoundStartedAt`, then call `Stop(ctx, rt, "api", StopOptions{})`. Assert
  that the error contains `relay unbind api`, does not contain `relay done`,
  and does not contain `--force`, and that the reloaded binding is
  unchanged (same State and Round).
- Depends on: step 1.
- Verify: both pass. Mutation (report it): revert step 1's Done string.
  `TestDoneRemoteForwardsFirst` fails. Restore.

### Step 3: pin local unbind of a running remote round

- New `TestUnbindRemoteRunningRoundForwardsAndDeletes` in
  `internal/relay/remote_test.go`, next to `TestUnbindRemote404Proceeds`
  (~1824) and following its setup. Use a binding from `remoteBinding("zen")`
  with `RoundStartedAt` set (the round is open), and a `fakeRemote` whose
  Unbind succeeds. Call `Unbind(ctx, rt, "api", false)`. Assert that it
  returns no error, that `fr.calls` contains `"Unbind:zen:api"`, and that
  `st.Load("api")` now returns `store.ErrNotFound` (check with `errors.Is`).
- Depends on: nothing (it can run before step 1).
- Verify: passes. Mutation (report it): temporarily make `Unbind` return an
  error when `b.Builder.Remote() && !b.RoundStartedAt.IsZero()`. The test
  fails. Restore.

### Step 4: pin the wire unbind of a running round

- New `TestWireUnbindStopsRunningRound` in `internal/serve`, next to
  `TestUnbindDropsQueued` (`admit_test.go` ~519), using the same harness:
  `setupTestEnv(t)` with default config (so the round is admitted and
  running, not queued), then `sendRound(...)` and `requireCreated`. Assert
  that the view's `RoundState == remote.RoundRunning`.
  Before unbinding, record the pid of the running builder. Read it from the
  owner's server-side store: find how the other serve tests reach the
  owner's `relay.Runtime` / binding, e.g. `env.srv.runtime(<owner id>)`
  then `rt.Store.Load("api")`, and use `b.Builder.PID`.
  Then `doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/unbind", nil, "")`.
  Assert:
  1. status 200;
  2. `env.runner.alive[pid] == false` (read it under `env.runner.mu`):
     `scriptRunner.Kill` records the kill that way;
  3. the owner's store no longer has a live binding `"api"`
     (`Load` → `store.ErrNotFound`).
- Depends on: nothing.
- Verify: passes. If the process is **not** killed or the handler refuses,
  STOP and report (see the header). Mutation (report it): temporarily add
  a 409 refusal to `handleUnbind` when the round is running. The test
  fails. Restore.
- This is an `internal/serve` test with a fake runner and an in-process
  `httptest` server, so it is CI-safe. Do not add any `cmd/relay` test: CI
  runners have no harness binary and no network.

### Step 5: AdminUnbind doc comment

- Deliverable: the reworded sentence in §4. Comment only; no code change.
- Depends on: nothing.
- Verify: `git diff internal/serve/admin.go` shows only comment lines.

### Step 6: full check

- `make check` passes.
- `git diff --stat` touches only the files in §2.
- Report: each step's verify output, all three mutation results, the step 1
  grep output, and the final `git diff --stat`. Commit on the binding's
  branch with message
  `fix(remote): a running remote round's done/stop hints name relay unbind, which works (#331)`.
