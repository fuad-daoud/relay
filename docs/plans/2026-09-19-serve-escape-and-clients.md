# Served bindings: skip the escape check, and `serve clients` says "no clients"

If any step below is impossible as written or contradicts the code you find,
**stop and report** -- do not improvise around it. A halt that surfaces a
design error is worth more than a green suite that bent a test to fit.

## 1. System Overview

Two bugs from the first real `relay serve` round.

**Escape check on served bindings.** `escapeCheck` (`internal/relay/escape.go`,
#192) decides whether a headless builder worked outside its worktree by
comparing two signals: the worktree's tree object is unchanged since the round
began, *and* the source repo `b.Repo` is dirty. On a served binding `b.Repo` is
the **bare** mirror the client's bundle unpacks into
(`internal/serve/bindings.go`: `Repo: bare`, also `Serve.BareRepo`), so
`rt.Git.Dirty(ctx, b.Repo)` runs `git status --porcelain` in a bare repository
and git refuses: `fatal: this operation must be run in a work tree`. The check
logs `escape check skipped` at Warn on every server tick of a running round.
The cwd is not wrong; the check is inapplicable: a server has no source
checkout for a builder to escape into, so the second signal has nothing to
measure. The fix is a precondition, not a different directory: a served
binding (`b.Serve != nil`) never runs the escape check. This is already
reproducible in-process: `go test ./internal/e2e -run TestRemoteRoundEndToEnd -v`
prints the warning today; step 2 pins it.

**`relay serve clients` on a fresh state.** `cmdServeClients`
(`cmd/relay/serve.go`) loops over `clients.List()` and prints one line per
client, so an empty list prints nothing. `relay serve status` prints
`no owners` in the same state via `serve.RenderAdminStatus`. Give `clients` a
`serve.RenderClients` counterpart that prints `no clients`.

No new files except tests. No protocol, store, or wire change.

## 2. File Structure

```
internal/relay/escape.go            MODIFY  add escapeApplies; escapeCheck calls it
internal/relay/escape_test.go       MODIFY  add TestEscapeApplies
internal/serve/admin.go             MODIFY  add RenderClients
internal/serve/admin_test.go        MODIFY  add TestRenderClients
internal/e2e/remote_test.go         MODIFY  add TestRemoteRoundTicksWithoutEscapeWarning (capture slog)
cmd/relay/serve.go                  MODIFY  cmdServeClients prints RenderClients
```

Nothing else. In particular do not touch `internal/serve/bindings.go`,
`internal/git/client.go`, or `internal/store`.

## 3. Data Structures & Type Definitions

No new types. Fields used:

- `store.Binding.Serve *store.ServeFacts` -- nil on local bindings, non-nil on
  every binding the server creates (`bindings.go`). This is the discriminator.
- `serve.Client` (`internal/serve/clients.go`): `ID remote.ClientID`,
  `Label string`, `EnrolledAt time.Time`, `RevokedAt time.Time` (zero = not
  revoked). `Clients.List() []Client` is the existing accessor.

## 4. Interface Definitions & Component Contracts

### 4.1 `internal/relay/escape.go`

```
// escapeApplies is the pure precondition behind escapeCheck: does this
// binding have the two directories the check compares?
//   - !b.Builder.Headless()      -> false (pane builders are out of scope, #192)
//   - b.Repo == ""               -> false
//   - b.RoundBaselineTree == ""  -> false
//   - b.Serve != nil             -> false (served binding: b.Repo is the bare
//                                   mirror, there is no source checkout to
//                                   escape into and `git status` cannot run
//                                   in a bare repo)
//   - otherwise                  -> true
func escapeApplies(b store.Binding) bool
```

`escapeCheck` keeps its signature and its `rt.Git == nil` guard, and replaces
the inline three-condition test with `!escapeApplies(b) || rt.Git == nil`.
Update the doc comment's "Preconditions for running at all" list to name
`b.Serve == nil` and why. Behaviour on local bindings is byte-for-byte
unchanged.

Postcondition: on a binding with `Serve != nil`, `escapeCheck` returns
`EscapeNone` without calling `rt.Git` and without logging.

### 4.2 `internal/serve/admin.go`

```
// RenderClients formats `relay serve clients`: one line per client,
//   "<id>  <label>  enrolled YYYY-MM-DD[  revoked YYYY-MM-DD]\n"
// in the order given. Empty input prints "no clients\n" -- the same shape
// RenderAdminStatus gives an empty owner list.
func RenderClients(clients []Client) string
```

The per-line format is exactly what `cmdServeClients` prints today (move it,
do not redesign it). `cmdServeClients` becomes: load, `fmt.Print(serve.RenderClients(clients.List()))`.

### 4.3 `internal/e2e/remote_test.go`

```
// captureLogs swaps slog's default handler for one that writes to a buffer
// for the test's lifetime and returns the buffer. Restores the previous
// default in t.Cleanup. Not safe under t.Parallel (no e2e test uses it).
func captureLogs(t *testing.T) *bytes.Buffer
```

Same pattern as `internal/relay/reconcile_log_test.go:22`.

## 5. High-Level Pseudocode

```
escapeCheck(ctx, rt, b, hasReport):
    if !escapeApplies(b) || rt.Git == nil: return EscapeNone
    ... unchanged from here ...

cmdServeClients(args):
    parse --state; root := serveRoot(fs)
    clients := serve.LoadClients(root/clients.json)      // already tolerates a missing file
    fmt.Print(serve.RenderClients(clients.List()))
    return nil

TestRemoteRoundTicksWithoutEscapeWarning:
    logs := captureLogs(t)
    server, client, enrol, repo  -- exactly as TestRemoteRoundEndToEnd steps 1-2
    relay.Add(--server); relay.Send(plan)                 // round 1 running on the server
    srv.Tick(ctx)                                         // the tick that warned before the fix
    srv.Tick(ctx)
    assert !strings.Contains(logs.String(), "escape check skipped")
    assert !strings.Contains(logs.String(), "must be run in a work tree")
    // then prove the round is otherwise healthy:
    sb := serverStore.Load("api"); assert sb.Serve != nil && sb.RoundBaselineTree != ""
```

Do not call `finishRound` in this test; the point is the *running* round's
ticks, which is where the warning fired every poll.

## 6. Error Handling Strategy

- `escapeApplies` cannot fail. `escapeCheck`'s existing "any git error is a
  skipped check, logged at Warn" contract is unchanged for local bindings.
- `RenderClients` cannot fail. `LoadClients` on a missing file already
  returns an empty set (`refresh` tolerates `os.ErrNotExist`) -- verify that
  in `clients.go` before relying on it; if it does not, **stop and report**,
  do not add tolerance in this round.
- No new error values, codes, or log lines.

## 7. Ordered Implementation Steps

Run `make check` at the end of every step (it adds `gofmt -l`, `go vet` and
a `go mod tidy` check over `go test ./...`). Use the Makefile's gofmt form
if you run it by hand: `test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }`.

### Step 1 -- pin the precondition (test first)

Deliverable: `TestEscapeApplies` in `internal/relay/escape_test.go`.

Table over: local headless binding with Repo and baseline (true); pane
builder (false); Repo "" (false); RoundBaselineTree "" (false); served
binding -- same fields as the first case plus `Serve: &store.ServeFacts{RepoID: "x", BareRepo: "/tmp/x.git"}`
(false). Build the "true" case once and derive the others from it so the
test reads as "this one field flips it". Use whatever constructs a headless
`store.Endpoint` elsewhere in the package's tests (`Mode: store.ModeHeadless`).

Verify: `go test ./internal/relay -run TestEscapeApplies` fails to compile
(escapeApplies undefined). That is the expected state; proceed.

### Step 2 -- reproduce in the e2e (test first)

Deliverable: `captureLogs` and `TestRemoteRoundTicksWithoutEscapeWarning`
in `internal/e2e/remote_test.go`, per §4.3 and §5.

Verify: `go test ./internal/e2e -run TestRemoteRoundTicksWithoutEscapeWarning -count=1`
**fails** with the captured line containing
`escape check skipped` / `must be run in a work tree`. Paste the failing
assertion output in your report. If it passes before any fix, the test is
not observing the daemon's logger -- stop and report.

### Step 3 -- the fix

Deliverable: `escapeApplies` in `internal/relay/escape.go` and `escapeCheck`
using it; doc comment updated per §4.1.

Depends on: 1, 2.

Verify: `go test ./internal/relay -run 'TestEscape' -count=1` and
`go test ./internal/e2e -count=1` both pass. Then mutation-test: temporarily
delete the `b.Serve != nil` clause, confirm **both** `TestEscapeApplies` and
`TestRemoteRoundTicksWithoutEscapeWarning` fail, restore it by re-editing
(not `git checkout` -- that would drop the fix). Report which tests failed
under the mutation.

### Step 4 -- `RenderClients` (test first)

Deliverable: `TestRenderClients` in `internal/serve/admin_test.go`, then
`RenderClients` in `internal/serve/admin.go` per §4.2.

Cases: empty → `"no clients\n"`; one enrolled client → exactly one line in
the format above; one revoked client → the line carries `  revoked YYYY-MM-DD`;
two clients → order preserved. Use fixed `time.Date` values so the expected
strings are literal.

Depends on: nothing (independent of steps 1-3).

Verify: `go test ./internal/serve -run TestRenderClients -count=1` passes.

### Step 5 -- wire the CLI

Deliverable: `cmdServeClients` in `cmd/relay/serve.go` prints
`serve.RenderClients(clients.List())` and nothing else. Remove the inline
loop.

Depends on: 4.

Do **not** add a test in `cmd/relay` for this: CI runners have no `herdr`
binary and `cmd/relay` tests must not execute subcommands; the rule is
already covered as a pure function by step 4.

Verify: `go build ./... && go run ./cmd/relay serve clients --state "$(mktemp -d)"`
prints exactly `no clients`. Then `make check` passes clean.

### Step 6 -- report

In the report list: the failing output from step 2 before the fix; which
tests failed under the step 3 mutation; the step 5 command output; and
`git diff --stat`, which must touch only the six files in §2.
