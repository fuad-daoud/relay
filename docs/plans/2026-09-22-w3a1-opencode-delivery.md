# Wave 3, batch A: planner delivery as a port, opencode's HTTP push path, and the text a push carries (#300, #297)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (the planner runs it); run the gate commands in §8
exactly as written. Every command in the foreground; no sub-agents for
edits. No git fetch/rebase (this worktree has no origin).

Spec: `docs/specs/2026-09-22-opencode-delivery-design.md`, already in your
base commit. **Read it first** -- this plan implements it, and where the
two disagree the spec is right and you should say so in your report.

## 1. System overview

relay delivers a planner-bound payload by typing it into the planner's
terminal pane. #124 replaced that for Claude Code with a real push (an MCP
channel). This round does the same for **opencode**, whose server takes
`POST /api/session/{sessionID}/prompt`, and generalises the seam so agy can
follow later: `Runtime.Deliverers`, a map from planner kind to a
`PlannerDeliverer`, consulted by `DeliverPending` before the pane path. No
deliverer for a kind means today's behaviour, unchanged.

Two rules carry the design:

- **A 2xx is not delivery.** Posting a valid session id to the *wrong*
  opencode server process returns 200 and the message is silently
  dropped, because `opencode.db` is shared between server processes but
  live delivery rides the owning process's event bus. relay confirms by
  reading the session back out of that db and never on a status code.
- **A push carries the report's text, not a pointer** (#297). The stored
  payload says `Builder finished round N. Report: <path>` because it was
  composed for an input box; a push has no input box, so one shared
  helper expands it at delivery time.

Nothing is deleted this round: `held.go`, the HELD state, the grace clock
and the pane path all stay until agy also has a deliverer.

## 2. File structure

```
internal/relay/push.go                 PushText, MaxPushBytes (#297): expand a report/findings/edge entry into payload + blank line + file text, capped
internal/relay/push_test.go            expansion, kind allowlist, no path, read error, truncation, origin line stays first
internal/relay/deliverer.go            Outcome (NotMine/Unavailable/Delivered), PlannerDeliverer interface
internal/relay/deliver_opencode.go     OpencodeDeliverer + opencodeService + readService/loopback/validSessionID/seen
internal/relay/deliver_opencode_test.go  httptest + fake Exec: happy path, the silent-200 case, idempotence, every NotMine and Unavailable row of this plan's §4.1 table, password never leaks
internal/relay/deliver.go              DeliverPending: read the pending entry first, then consult rt.Deliverers before the pane path
internal/relay/deliver_test.go         + an opencode planner is never typed at and is confirmed; a nil Deliverers map behaves exactly as today
internal/relay/drain.go                push PushText(entry, os.ReadFile) instead of entry.Payload
internal/relay/drain_test.go           + the pushed content carries the report's text
internal/relay/herdr.go                Runtime: + Deliverers map[string]PlannerDeliverer (nil == none)
internal/mcp/instructions.go           the report-kind bullet: the body IS the report; one sentence for the truncated case
cmd/relay/main.go                      newRuntime: wire Deliverers with an OpencodeDeliverer when sqlite3 is on PATH
Makefile                               check: gofmt over tracked files, not the whole filesystem (see §7 step 7)
README.md                              the delivery section (find it; if there is none, add two sentences under the existing planner/daemon prose): opencode planners are pushed to, everything else is typed
docs/plans/2026-09-22-w3a1-opencode-delivery.md   this plan; already in your base -- do not rewrite it or the spec
```

No file outside this list changes. `internal/relay/held.go`,
`reconcile.go`, `internal/serve/*` and `internal/ui/*` are untouched.

## 3. Data structures

### 3.1 `internal/relay/push.go`

```go
// MaxPushBytes caps the file text a push carries. Far above any report
// relay has produced; the full text stays one read away at the entry's
// Path, which every push path also carries as metadata.
const MaxPushBytes = 64 << 10

// PushText returns the text a push path should carry for e: the stored
// Payload (origin line included) for every kind that needs no file, and
// Payload + blank line + the file's contents for the kinds whose Path
// names a text artifact the planner would otherwise have to open.
//
// ok reports whether an expansion happened. A read error is not a
// delivery failure: PushText returns e.Payload and false, because an
// unreadable report is still a report that arrived.
//
// The origin line stays the first line of the result in every case;
// OpencodeDeliverer's confirmation query depends on it.
func PushText(e store.LogEntry, read func(string) ([]byte, error)) (string, bool)
```

Expand only when `e.Path != ""` **and** `e.Kind` is one of
`store.KindReport`, `store.KindFindings`, `store.KindEdge`. `KindDiff` and
`KindDrift` are patches the planner reads with `relay diff`; every other
kind's payload is already its whole text.

Truncation: keep at most `MaxPushBytes` of file text, cut back to the last
`\n` within that budget (if there is none, cut at the budget), and append

```
[truncated at 64 KiB -- full text at <e.Path>]
```

as a final line. Write the size in the message from `MaxPushBytes`, do not
hardcode "64 KiB" twice.

### 3.2 `internal/relay/deliverer.go`

```go
// Outcome is what a PlannerDeliverer reports back to DeliverPending.
type Outcome int

const (
    // NotMine: this deliverer cannot address that planner -- wrong kind,
    // no usable session id, a missing dependency, or it has given up
    // (FallbackAfter). The pane path takes the payload this same tick.
    NotMine Outcome = iota
    // Unavailable: the push path exists but is not reachable right now.
    // The payload stays pending; the next tick retries.
    Unavailable
    // Delivered: the planner's session provably received the payload.
    // Only this value may confirm the entry.
    Delivered
)

// PlannerDeliverer hands one payload to one planner over that harness's
// own push path. Implementations are keyed by planner kind and are called
// from the daemon's tick goroutine.
//
// Deliver must be idempotent in effect: after any non-Delivered outcome it
// is called again next tick with the same payload, so it must not deliver
// twice.
//
// The returned string is a short reason for the log and Delivery.Reason,
// and must never contain a credential. The error is for a bug in relay
// (a request it could not build); an unreachable planner is Unavailable,
// not an error.
type PlannerDeliverer interface {
    Deliver(ctx context.Context, planner store.Endpoint, payload, path string, queuedAt time.Time) (Outcome, string, error)
}
```

### 3.3 `internal/relay/deliver_opencode.go`

```go
const (
    OpencodeRequestTimeout = 5 * time.Second
    OpencodeConfirmWindow  = 3 * time.Second
    OpencodeConfirmPoll    = 250 * time.Millisecond
    DefaultFallbackAfter   = 30 * time.Second
)

type OpencodeDeliverer struct {
    Client        *http.Client  // nil -> &http.Client{Timeout: OpencodeRequestTimeout}
    StateFile     string        // $XDG_STATE_HOME/opencode/service.json
    DBPath        string        // $XDG_DATA_HOME/opencode/opencode.db
    Exec          usage.Exec    // the sqlite3 shell-out; nil -> NotMine
    Now           func() time.Time      // nil -> time.Now
    Alive         func(pid int) bool    // nil -> the same rule FileClaims uses
    FallbackAfter time.Duration         // zero -> DefaultFallbackAfter
}

// opencodeService is $XDG_STATE_HOME/opencode/service.json. Verified shape
// on opencode 2.0.12: {"id","version","url","pid","password"}.
type opencodeService struct {
    URL      string `json:"url"`
    Password string `json:"password"`
    PID      int    `json:"pid"`
    Version  string `json:"version"`
}
```

`internal/relay/herdr.go`, at the end of `Runtime` (append, do not reorder
existing fields):

```go
// Deliverers routes a planner-bound payload to that planner kind's own
// push path instead of typing it into a pane
// (docs/specs/2026-09-22-opencode-delivery-design.md). A kind with no
// entry, and a nil map, take the pane path exactly as before.
Deliverers map[string]PlannerDeliverer
```

## 4. Contracts

### 4.1 `OpencodeDeliverer.Deliver`

Preconditions: none; every input is validated.

In order, returning at the first match:

| condition | outcome | reason string |
|---|---|---|
| `planner.Kind != "opencode"` | `NotMine` | `""` |
| `Exec == nil` | `NotMine` | `"no sqlite3"` |
| `planner.SessionID` fails `^ses_[A-Za-z0-9]+$` | `NotMine` | `"no opencode session id"` |
| `queuedAt` non-zero and `Now().Sub(queuedAt) > FallbackAfter` | `NotMine` | `"opencode push gave up after <FallbackAfter>"` |
| `StateFile` missing / unparseable / `PID` not alive | `Unavailable` | `"opencode service not running"` |
| `URL` is not `http://127.0.0.1[:port]` or `http://localhost[:port]` | `Unavailable` | `"opencode service url is not loopback"` |
| origin already present in the db | `Delivered` | `"already present"` |
| POST transport error | `Unavailable` | `"post: <err>"` |
| POST status not 2xx | `Unavailable` | `"post: <status>"` |
| origin seen within `OpencodeConfirmWindow` | `Delivered` | `""` |
| otherwise | `Unavailable` | `"posted but not seen in the session"` |

The `FallbackAfter` check comes **before** any I/O so a long outage costs
nothing per tick.

Postconditions: `Delivered` only when the read-back saw the origin line.
No reason string and no error ever contains `svc.Password`. Add a test
that asserts exactly that.

The request:

```
POST {svc.URL}/api/session/{planner.SessionID}/prompt
Authorization: Basic base64("opencode:" + svc.Password)
Content-Type: application/json

{"text": <payload>, "delivery": "queue", "resume": true}
```

`delivery` is `"queue"`, not `"steer"` -- see the spec §3.3; do not
change it.

The read-back (`seen`), through `Exec.Run(ctx, "sqlite3", "-readonly", DBPath, q)`:

```sql
select count(*) from part p join message m on m.id = p.message_id
 where p.session_id = '<sessionID>'
   and json_extract(m.data, '$.role') = 'user'
   and json_extract(p.data, '$.type') = 'text'
   and json_extract(p.data, '$.text') like '%<origin>%'
```

Escape `'` as `''` in both interpolated values, the way
`usage.OpencodeQuery` already does for a directory. `seen` is true when
the first line of stdout parses as an integer > 0. A failing `sqlite3` is
not `seen` and not an error -- it becomes `Unavailable` at the call site.

`origin` is the payload's **first line**. Do not recompute it with
`OriginLine`: take `payload` up to the first `\n`, which is what `Queue`
wrote and what `PushText` preserves.

### 4.2 `DeliverPending`

Signature unchanged. Two changes inside:

1. **Read the pending entry earlier.** Today `PendingForPlanner` is called
   after `FindAgent` and the planner-status gate. Move that call up, to
   just after the existing channel-claim check, and have the `!found` case
   return `Delivery{Empty: true, Reason: "nothing pending"}` with
   `clearPlannerScreen(b)` exactly as it does today. Everything the pane
   path does afterwards stays in the same order relative to itself.
   **This is the riskiest edit in the round.** The existing tests for
   "planner is gone", "planner is working", and the held/grace cases must
   pass unchanged; if any of them now fails, stop and report rather than
   adjusting the test.
2. **Consult the deliverer**, after the entry is in hand and before
   `FindAgent`:

```
if d, ok := rt.Deliverers[b.Planner.Kind]; ok {
    text, _ := PushText(pending, os.ReadFile)
    out, reason, err := d.Deliver(ctx, b.Planner, text, pending.Path, pending.TS)
    if err != nil { return b, Delivery{}, fmt.Errorf("deliver to planner: %w", err) }
    switch out {
    case Delivered:
        if err := tx.ConfirmIndex(b.Name, idx); err != nil { return b, Delivery{}, err }
        return clearPlannerScreen(b), Delivery{Delivered: true, Reason: reason, Round: pending.Round}, nil
    case Unavailable:
        return clearPlannerScreen(b), Delivery{Reason: reason, Round: pending.Round}, nil
    }
    // NotMine falls through to the pane path.
}
```

### 4.3 `Drain` (#297)

One line: push `PushText(entry, os.ReadFile)`'s first return instead of
`entry.Payload`. The meta map, the confirm order and everything else stay
as they are.

### 4.4 `internal/mcp/instructions.go`

In the `kind="report"` bullet, keep "the block's body is the report" true
and add one sentence: a very large report is cut and the block ends with a
line naming the full path. Do not restructure the rest of the text.

## 5. How fallback is measured

`Deliver` needs to know when the payload was queued, to decide whether
`FallbackAfter` has passed. It receives the payload, not the entry, and
the map holds one shared deliverer instance, so this cannot come from a
field on the struct. It is a parameter:

```go
Deliver(ctx context.Context, planner store.Endpoint, payload, path string, queuedAt time.Time) (Outcome, string, error)
```

`DeliverPending` passes `pending.TS`, which `Queue` stamps. A zero
`queuedAt` means "treat as fresh": never fall back. Use this signature in
§3.2 and §4.2; there is no `QueuedAt` field on `OpencodeDeliverer`.

## 6. Error handling

- Nothing in §4.1 can lose a payload: the entry is confirmed only on
  `Delivered`; every other outcome leaves it pending for the next tick or
  for the pane.
- A deliverer error (the `error` return) fails that binding's tick, which
  `deliverAndSettle` already propagates. Reserve it for a request relay
  could not build; do not use it for an unreachable planner.
- Logging: one `slog.Info` on the transitions only -- the first
  `Unavailable` for an entry, the `Delivered`, and the fallback to the
  pane. Never per tick: the daemon reconciles every couple of seconds and
  a per-tick line would drown the log, which is why `payload held` already
  logs only on the transition into held. If tracking "first" needs state
  you do not have, log `Delivered` and the fallback only, and say so in
  the report.
- Never log, wrap, or return `svc.Password`.

## 7. Ordered steps

1. **`PushText`** -- `push.go` + `push_test.go`. Verify:
   `go test ./internal/relay -run PushText`. Cases: a report entry with a
   readable path expands (payload, blank line, text); `KindDiff` with a
   path does not; no path does not; a read error returns the payload and
   `false`; a file of `MaxPushBytes+1000` bytes is cut at a newline and
   ends with the truncation line naming the path; the origin line is the
   first line of every result.
2. **`Drain` uses it** -- `drain.go` one line, `drain_test.go` one case.
   Verify: `go test ./internal/relay -run Drain`. Depends on 1.
3. **Instructions** -- `internal/mcp/instructions.go` per §4.4. Verify:
   `go test ./internal/mcp`. Depends on 1.
4. **The port** -- `deliverer.go` (`Outcome`, `PlannerDeliverer` with the
   §5 signature), `Runtime.Deliverers`. Verify: `go build ./...`.
5. **`OpencodeDeliverer`** -- `deliver_opencode.go` +
   `deliver_opencode_test.go`, against `httptest.NewServer` and a fake
   `usage.Exec` you control (it records the query and returns the count
   you choose; no real sqlite3 in tests). Verify:
   `go test ./internal/relay -run Opencode`. Every row of §4.1's table
   gets a case, plus:
   - the POST's path, `Authorization` header, and body fields
     (`delivery: "queue"`, `resume: true`) are asserted exactly;
   - **the silent-200 case**: the server returns 200 and the fake Exec
     always returns 0 -> `Unavailable`. Then mutation-check it: make
     `Deliver` return `Delivered` on 2xx, confirm this test fails, and
     restore. Report that you did it.
   - idempotence: Exec returns 1 before any POST -> `Delivered` and the
     httptest server recorded **zero** requests;
   - the password appears in no returned reason and no error string.
   Depends on 4.
6. **`DeliverPending`** -- the reorder and the consult, per §4.2, plus the
   two cases in `deliver_test.go` (an opencode planner with a stub
   deliverer returning `Delivered` is confirmed and `fakeHerdr` records
   zero prompts; a nil `Deliverers` map behaves as today). Verify:
   `go test ./internal/relay` -- **the whole package, not just the new
   tests**, because of the reorder. Depends on 4, 5.
7. **Makefile** -- `check`'s gofmt line runs over tracked files only:
   `gofmt -l $$(git ls-files '*.go')` in place of `gofmt -l .`, keeping
   the same `test -z ... || { ...; exit 1; }` shape and printing the same
   list on failure. Reason: `gofmt -l .` walks the filesystem, so an
   ignored directory inside the checkout (an agent worktree under
   `.claude/worktrees/`, a vendored tree) fails the gate with files the
   author never touched. Verify: `gofmt -l $(git ls-files '*.go')` prints
   nothing; then `printf 'package main\nfunc  x( ){}\n' > /tmp/bad.go &&
   cp /tmp/bad.go ./badfmt_check.go && gofmt -l $(git ls-files '*.go') |
   head` -- it must print nothing (the file is untracked), while
   `gofmt -l .` prints it; then `rm ./badfmt_check.go`. Paste both
   outputs.
8. **Wiring** -- `cmd/relay/main.go` `newRuntime`: build the deliverer
   map. `sqlite3` on PATH (`exec.LookPath`) gives an `OpencodeDeliverer`
   with `Exec: binExec{}`, `StateFile` and `DBPath` resolved the way
   `store.DefaultRoot` resolves XDG (`$XDG_STATE_HOME` else
   `~/.local/state`; `$XDG_DATA_HOME` else `~/.local/share`), both under
   `opencode/`. No sqlite3 -> leave `Deliverers` nil. Verify:
   `go build ./...` and `go test ./cmd/relay`. Depends on 5.
9. **README** -- per §2. Verify: nothing to run; quote the added lines in
   the report.

## 8. Gate

In the worktree root, in this order, pasting each command's tail into the
report:

```
gofmt -l $(git ls-files '*.go')      # must print nothing
go vet ./...
go test ./... 2>&1 | tail -40
```

Do not run `make check` or `make e2e`. Commit on the current branch:

```
feat(deliver): planner delivery is a port, opencode planners are pushed to over their own API, and a push carries the report's text (#300, #297)
```

plus the attribution line. Report: what landed per step, the gate output,
the mutation check in step 5, and anything you had to leave out and why.
